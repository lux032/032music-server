package storage

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
)

func seedLoudnessTrack(t *testing.T, s *Store, ctx context.Context, libID int64, rel, title, album string, size, modNS, durMs int64) (trackID, albumID, fileID int64) {
	t.Helper()
	err := s.ImportTrack(ctx, ImportInput{
		LibraryID:    libID,
		RelativePath: rel,
		FileSize:     size,
		ModifiedAtNS: modNS,
		Metadata: metadata.AudioMetadata{
			Title:          title,
			Album:          album,
			Artists:        []string{"Artist"},
			AlbumArtists:   []string{"Artist"},
			DiscNumber:     1,
			TrackNumber:    1,
			DurationMillis: durMs,
		},
		AudioProps: metadata.AudioProps{Codec: "flac", SampleRate: 44100, BitDepth: 16},
	})
	if err != nil {
		t.Fatalf("import %s: %v", rel, err)
	}
	if err = s.db.QueryRowContext(ctx,
		`SELECT af.id, af.track_id, t.album_id FROM audio_files af JOIN tracks t ON t.id = af.track_id WHERE af.library_id = ? AND af.relative_path = ?`,
		libID, rel).Scan(&fileID, &trackID, &albumID); err != nil {
		t.Fatalf("lookup %s: %v", rel, err)
	}
	return trackID, albumID, fileID
}

func TestLoudnessMigrationIdempotentAndCascade(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("re-run migrate: %v", err)
	}
	if err := s.EnsureLibrary(ctx, "Default", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	_, albumID, fileID := seedLoudnessTrack(t, s, ctx, lib.ID, "a/01.flac", "T1", "Alb1", 100, 10, 180_000)
	if err = s.SaveAudioLoudness(ctx, AudioLoudnessRecord{
		AudioFileID:     fileID,
		FileSize:        100,
		ModifiedAtNS:    10,
		AnalyzerVersion: LoudnessAnalyzerVersion,
		IntegratedLUFS:  -9.5,
		TruePeakDB:      -0.4,
	}, albumID); err != nil {
		t.Fatal(err)
	}
	// Deleting the audio_file cascades into audio_loudness.
	if _, err = s.db.ExecContext(ctx, `DELETE FROM audio_files WHERE id = ?`, fileID); err != nil {
		t.Fatal(err)
	}
	var remain int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audio_loudness WHERE audio_file_id = ?`, fileID).Scan(&remain); err != nil || remain != 0 {
		t.Fatalf("expected cascade delete, got remain=%d err=%v", remain, err)
	}
	if err = s.ReconcileAlbumLoudness(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM album_loudness WHERE album_id = ?`, albumID).Scan(&remain); err != nil || remain != 0 {
		t.Fatalf("expected empty album_loudness cleaned up, got remain=%d err=%v", remain, err)
	}
}

func TestLoudnessCandidatePriorityAndFingerprintInvalidation(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	if err := s.EnsureLibrary(ctx, "Default", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	// Album 1 (older, favorited track) vs Album 2 (newer, not favorited) vs Album 3 (played recently).
	t1, a1, f1 := seedLoudnessTrack(t, s, ctx, lib.ID, "a1/01.flac", "Fav1", "Alb1", 100, 1, 200_000)
	_, _, f1b := seedLoudnessTrack(t, s, ctx, lib.ID, "a1/02.flac", "Fav2", "Alb1", 100, 1, 200_000)
	_, _, f2 := seedLoudnessTrack(t, s, ctx, lib.ID, "a2/01.flac", "Cold", "Alb2", 100, 1, 200_000)
	t3, _, f3 := seedLoudnessTrack(t, s, ctx, lib.ID, "a3/01.flac", "Played", "Alb3", 100, 1, 200_000)

	if err = s.SetTrackFavorite(ctx, t1, true); err != nil {
		t.Fatal(err)
	}
	playSessionOnce(t, ctx, s, "sess-prio", t3, 1000, 200_000)

	now := time.Now()
	cands, err := s.NextAudioLoudnessCandidates(ctx, LoudnessAnalyzerVersion, now, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 4 {
		t.Fatalf("expected 4 candidates, got %d", len(cands))
	}
	// Cold track f2 must come last (behind favorited album a1's tracks and played album a3's track).
	if cands[3].AudioFileID != f2 {
		t.Fatalf("expected unplayed/unfavorited file %d last, got order: %+v", f2, cands)
	}
	// Excluding f1 & f3 leaves f1b and f2.
	filtered, err := s.NextAudioLoudnessCandidates(ctx, LoudnessAnalyzerVersion, now, []int64{f1, f3}, 10)
	if err != nil || len(filtered) != 2 || filtered[0].AudioFileID != f1b || filtered[1].AudioFileID != f2 {
		t.Fatalf("exclude mismatch: %+v err=%v", filtered, err)
	}

	// Save measurement for f1 -> disappears from candidates.
	if err = s.SaveAudioLoudness(ctx, AudioLoudnessRecord{
		AudioFileID: f1, FileSize: 100, ModifiedAtNS: 1,
		AnalyzerVersion: LoudnessAnalyzerVersion, IntegratedLUFS: -8.5, TruePeakDB: -0.2,
	}, a1); err != nil {
		t.Fatal(err)
	}
	// Mutating file fingerprint re-enqueues f1.
	if _, err = s.db.ExecContext(ctx, `UPDATE audio_files SET file_size = 200 WHERE id = ?`, f1); err != nil {
		t.Fatal(err)
	}
	afterMut, err := s.NextAudioLoudnessCandidates(ctx, LoudnessAnalyzerVersion, now, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range afterMut {
		if c.AudioFileID == f1 && c.FileSize == 200 {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected modified file %d back in candidates: %+v", f1, afterMut)
	}
}

func TestLoudnessRejectsNonFiniteAndTracksFailures(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	if err := s.EnsureLibrary(ctx, "Default", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, _ := s.LibraryByRoot(ctx, "/music")
	_, a1, f1 := seedLoudnessTrack(t, s, ctx, lib.ID, "bad.flac", "Bad", "Alb", 50, 5, 10_000)

	if err := s.SaveAudioLoudness(ctx, AudioLoudnessRecord{
		AudioFileID: f1, FileSize: 50, ModifiedAtNS: 5, IntegratedLUFS: math.Inf(-1), TruePeakDB: -1,
	}, a1); err == nil {
		t.Fatal("expected error saving -Inf loudness")
	}
	if err := s.SaveAudioLoudness(ctx, AudioLoudnessRecord{
		AudioFileID: f1, FileSize: 50, ModifiedAtNS: 5, IntegratedLUFS: -12, TruePeakDB: math.NaN(),
	}, a1); err == nil {
		t.Fatal("expected error saving NaN true peak")
	}

	now := time.Now().UTC()
	cand := AudioLoudnessCandidate{AudioFileID: f1, FileSize: 50, ModifiedAtNS: 5}
	if err := s.RecordAudioLoudnessFailure(ctx, cand, LoudnessAnalyzerVersion, "silent track", true, now); err != nil {
		t.Fatal(err)
	}
	cands, err := s.NextAudioLoudnessCandidates(ctx, LoudnessAnalyzerVersion, now.Add(48*time.Hour), nil, 10)
	if err != nil || len(cands) != 0 {
		t.Fatalf("permanent failure should skip candidate, got %+v err=%v", cands, err)
	}
	fails, err := s.AudioLoudnessFailures(ctx, LoudnessAnalyzerVersion, 10)
	if err != nil || len(fails) != 1 || !fails[0].Permanent {
		t.Fatalf("expected 1 permanent failure, got %+v err=%v", fails, err)
	}
	if n, err := s.ClearAudioLoudnessFailures(ctx); err != nil || n != 1 {
		t.Fatalf("clear failures: n=%d err=%v", n, err)
	}
	cands, err = s.NextAudioLoudnessCandidates(ctx, LoudnessAnalyzerVersion, now, nil, 10)
	if err != nil || len(cands) != 1 {
		t.Fatalf("expected candidate back after retry clear, got %+v err=%v", cands, err)
	}
}

func TestLoudnessAlbumAggregationAndTrackExtrasJSON(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	if err := s.EnsureLibrary(ctx, "Default", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, _ := s.LibraryByRoot(ctx, "/music")
	t1, a1, f1 := seedLoudnessTrack(t, s, ctx, lib.ID, "alb/01.flac", "Loud", "Alb", 100, 1, 200_000)
	t2, _, f2 := seedLoudnessTrack(t, s, ctx, lib.ID, "alb/02.flac", "Quiet", "Alb", 100, 1, 200_000)
	t3, _, _ := seedLoudnessTrack(t, s, ctx, lib.ID, "other/01.flac", "Unmeasured", "Other", 100, 1, 200_000)

	lra := 6.5
	// TruePeakDB = 0.0 must still serialize explicitly as 0, not omitted.
	if err := s.SaveAudioLoudness(ctx, AudioLoudnessRecord{
		AudioFileID: f1, FileSize: 100, ModifiedAtNS: 1,
		AnalyzerVersion: LoudnessAnalyzerVersion, IntegratedLUFS: -8.0, TruePeakDB: 0.0, LoudnessRange: &lra,
	}, a1); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAudioLoudness(ctx, AudioLoudnessRecord{
		AudioFileID: f2, FileSize: 100, ModifiedAtNS: 1,
		AnalyzerVersion: LoudnessAnalyzerVersion, IntegratedLUFS: -14.0, TruePeakDB: -1.5,
	}, a1); err != nil {
		t.Fatal(err)
	}

	// Expected equal-duration energy average of -8 and -14 LUFS:
	// 10 * log10((10^-0.8 + 10^-1.4)/2) = -10.04 dB, max peak = 0.0 dBTP.
	expectedAlbumLUFS := math.Round(10.0*math.Log10((math.Pow(10, -0.8)+math.Pow(10, -1.4))/2.0)*100) / 100

	track1, err := s.TrackByID(ctx, t1)
	if err != nil {
		t.Fatal(err)
	}
	raw1, err := json.Marshal(track1)
	if err != nil {
		t.Fatal(err)
	}
	var m1 map[string]any
	if err = json.Unmarshal(raw1, &m1); err != nil {
		t.Fatal(err)
	}
	if m1["loudnessLufs"] != -8.0 {
		t.Fatalf("unexpected loudnessLufs: %v", m1["loudnessLufs"])
	}
	if v, ok := m1["truePeakDb"]; !ok || v != 0.0 {
		t.Fatalf("expected explicit truePeakDb=0, got ok=%v v=%v json=%s", ok, v, raw1)
	}
	if m1["loudnessRange"] != 6.5 {
		t.Fatalf("unexpected loudnessRange: %v", m1["loudnessRange"])
	}
	if m1["albumLoudnessLufs"] != expectedAlbumLUFS {
		t.Fatalf("expected albumLoudnessLufs=%v, got %v", expectedAlbumLUFS, m1["albumLoudnessLufs"])
	}
	if v, ok := m1["albumTruePeakDb"]; !ok || v != 0.0 {
		t.Fatalf("expected explicit albumTruePeakDb=0, got ok=%v v=%v", ok, v)
	}

	// SyncTracks must expose identical TrackExtras fields, and unmeasured track t3 must omit them.
	synced, err := s.SyncTracks(ctx, SyncTracksParams{})
	if err != nil || len(synced.Items) != 3 {
		t.Fatalf("sync tracks: %d %v", len(synced.Items), err)
	}
	for _, item := range synced.Items {
		b, _ := json.Marshal(item)
		var fields map[string]any
		_ = json.Unmarshal(b, &fields)
		if item.ID == t3 {
			for _, k := range []string{"loudnessLufs", "truePeakDb", "loudnessRange", "albumLoudnessLufs", "albumTruePeakDb"} {
				if _, exists := fields[k]; exists {
					t.Fatalf("unmeasured track %d leaked %s in %s", t3, k, b)
				}
			}
		} else if item.ID == t2 {
			if fields["loudnessLufs"] != -14.0 || fields["albumLoudnessLufs"] != expectedAlbumLUFS {
				t.Fatalf("unexpected sync track t2 loudness: %s", b)
			}
		}
	}

	// Stats + median of {-8.0, -14.0} = -11.0.
	st, err := s.AudioLoudnessStats(ctx, LoudnessAnalyzerVersion, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if st.Eligible != 3 || st.Analyzed != 2 || st.LibraryMedianLUFS == nil || *st.LibraryMedianLUFS != -11.0 {
		t.Fatalf("unexpected stats: %+v", st)
	}

	// Marking f1 missing and reconciling updates album loudness to only f2 (-14.0 / -1.5).
	if _, err = s.db.ExecContext(ctx, `UPDATE audio_files SET status = 'missing' WHERE id = ?`, f1); err != nil {
		t.Fatal(err)
	}
	if err = s.ReconcileAlbumLoudness(ctx); err != nil {
		t.Fatal(err)
	}
	track2, err := s.TrackByID(ctx, t2)
	if err != nil {
		t.Fatal(err)
	}
	if track2.AlbumLoudnessLUFS == nil || *track2.AlbumLoudnessLUFS != -14.0 || track2.AlbumTruePeakDB == nil || *track2.AlbumTruePeakDB != -1.5 {
		t.Fatalf("expected album loudness recomputed after track missing, got %+v", track2.TrackExtras)
	}
}
