package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

var skipTestLibrarySeq int64

// newSkipTestTrack inserts a library/album/track chain with the given track
// duration (0 = unknown) and returns the track ID.
func newSkipTestTrack(t *testing.T, ctx context.Context, s *Store, durationMillis int64) int64 {
	t.Helper()
	root := fmt.Sprintf("/music/%d", atomic.AddInt64(&skipTestLibrarySeq, 1))
	result, err := s.db.ExecContext(ctx, `INSERT INTO libraries(name,root_path) VALUES('Test',?)`, root)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := result.LastInsertId()
	result, err = s.db.ExecContext(ctx, `INSERT INTO albums(library_id,title,sort_title,grouping_key) VALUES(?,?,?,?)`, libraryID, "Album", "album", fmt.Sprintf("album-%d", libraryID))
	if err != nil {
		t.Fatal(err)
	}
	albumID, _ := result.LastInsertId()
	result, err = s.db.ExecContext(ctx, `INSERT INTO tracks(album_id,title,sort_title,duration_ms) VALUES(?,?,?,NULLIF(?,0))`, albumID, "Track", "track", durationMillis)
	if err != nil {
		t.Fatal(err)
	}
	trackID, _ := result.LastInsertId()
	return trackID
}

func skipCountOf(t *testing.T, ctx context.Context, s *Store, trackID int64) (int64, string) {
	t.Helper()
	var count int64
	var lastSkippedAt string
	err := s.db.QueryRowContext(ctx, `SELECT skip_count,COALESCE(last_skipped_at,'') FROM playback_progress WHERE track_id=?`, trackID).Scan(&count, &lastSkippedAt)
	if err != nil {
		t.Fatalf("skip count query: %v", err)
	}
	return count, lastSkippedAt
}

func report(t *testing.T, ctx context.Context, s *Store, update PlaybackUpdate) {
	t.Helper()
	if err := s.UpdatePlayback(ctx, update); err != nil {
		t.Fatalf("update %+v: %v", update, err)
	}
}

func TestSkipInferenceLocalPlayback(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "skip.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		duration int64
		reports  []PlaybackUpdate
		want     int64
	}{
		{
			name:     "early skip reports zero position",
			duration: 240000,
			reports: []PlaybackUpdate{
				{State: "playing", PositionMillis: 5000, DurationMillis: 240000},
				{State: "stopped", PositionMillis: 0, DurationMillis: 240000, Continuing: true},
			},
			want: 1,
		},
		{
			name:     "late stop is not a skip",
			duration: 240000,
			reports: []PlaybackUpdate{
				{State: "playing", PositionMillis: 200000, DurationMillis: 240000},
				{State: "stopped", PositionMillis: 0, DurationMillis: 240000, Continuing: true},
			},
			want: 0,
		},
		{
			name:     "pause then skip still counts",
			duration: 240000,
			reports: []PlaybackUpdate{
				{State: "playing", PositionMillis: 25000, DurationMillis: 240000},
				{State: "paused", PositionMillis: 25000, DurationMillis: 240000},
				{State: "stopped", PositionMillis: 25000, DurationMillis: 240000, Continuing: true},
			},
			want: 1,
		},
		{
			name:     "sonos stop with real early position",
			duration: 240000,
			reports: []PlaybackUpdate{
				{State: "playing", PositionMillis: 10000, DurationMillis: 240000},
				{State: "stopped", PositionMillis: 12000, DurationMillis: 240000, Continuing: true},
			},
			want: 1,
		},
		{
			name:     "sonos stop past threshold is not a skip",
			duration: 240000,
			reports: []PlaybackUpdate{
				{State: "playing", PositionMillis: 10000, DurationMillis: 240000},
				{State: "stopped", PositionMillis: 60000, DurationMillis: 240000, Continuing: true},
			},
			want: 0,
		},
		{
			name:     "user stop without continuing is not a skip",
			duration: 240000,
			reports: []PlaybackUpdate{
				{State: "playing", PositionMillis: 5000, DurationMillis: 240000},
				{State: "stopped", PositionMillis: 5000, DurationMillis: 240000, Continuing: false},
			},
			want: 0,
		},
		{
			name:     "duplicate stop report is not counted again",
			duration: 240000,
			reports: []PlaybackUpdate{
				{State: "playing", PositionMillis: 5000, DurationMillis: 240000},
				{State: "stopped", PositionMillis: 0, DurationMillis: 240000, Continuing: true},
				{State: "stopped", PositionMillis: 0, DurationMillis: 240000, Continuing: true},
			},
			want: 1,
		},
		{
			name:     "first ever record being stopped is not a skip",
			duration: 240000,
			reports: []PlaybackUpdate{
				{State: "stopped", PositionMillis: 0, DurationMillis: 240000, Continuing: true},
			},
			want: 0,
		},
		{
			name:     "short track threshold is half duration",
			duration: 20000,
			reports: []PlaybackUpdate{
				{State: "playing", PositionMillis: 8000, DurationMillis: 20000},
				{State: "stopped", PositionMillis: 0, DurationMillis: 20000, Continuing: true},
			},
			want: 1,
		},
		{
			name:     "short track past half duration is not a skip",
			duration: 20000,
			reports: []PlaybackUpdate{
				{State: "playing", PositionMillis: 12000, DurationMillis: 20000},
				{State: "stopped", PositionMillis: 0, DurationMillis: 20000, Continuing: true},
			},
			want: 0,
		},
		{
			name:     "unknown duration falls back to 30 seconds",
			duration: 0,
			reports: []PlaybackUpdate{
				{State: "playing", PositionMillis: 25000},
				{State: "stopped", PositionMillis: 0, Continuing: true},
			},
			want: 1,
		},
		{
			name:     "unknown duration past 30 seconds is not a skip",
			duration: 0,
			reports: []PlaybackUpdate{
				{State: "playing", PositionMillis: 35000},
				{State: "stopped", PositionMillis: 0, Continuing: true},
			},
			want: 0,
		},
		{
			// tracks.duration_ms=40000 wins over the reported 240000: threshold is
			// 20000, not 30000, so a 25s position is past the threshold.
			name:     "library duration wins over reported duration",
			duration: 40000,
			reports: []PlaybackUpdate{
				{State: "playing", PositionMillis: 25000, DurationMillis: 240000},
				{State: "stopped", PositionMillis: 0, DurationMillis: 240000, Continuing: true},
			},
			want: 0,
		},
		{
			// No library duration and no recorded duration: the incoming 40000
			// report duration supplies the 20000 threshold for this decision.
			name:     "reported duration used when library duration unknown",
			duration: 0,
			reports: []PlaybackUpdate{
				{State: "playing", PositionMillis: 25000},
				{State: "stopped", PositionMillis: 0, DurationMillis: 40000, Continuing: true},
			},
			want: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			trackID := newSkipTestTrack(t, ctx, s, tc.duration)
			for _, update := range tc.reports {
				update.TrackID = trackID
				report(t, ctx, s, update)
			}
			count, lastSkippedAt := skipCountOf(t, ctx, s, trackID)
			if count != tc.want {
				t.Fatalf("skip_count = %d, want %d", count, tc.want)
			}
			if tc.want > 0 && lastSkippedAt == "" {
				t.Fatal("last_skipped_at must be set when a skip was counted")
			}
			if tc.want == 0 && lastSkippedAt != "" {
				t.Fatalf("last_skipped_at must stay empty, got %q", lastSkippedAt)
			}
		})
	}
}

func TestSkipInferenceWindowAndCompletionGuards(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "skip-guards.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	t.Run("stale last played at is not a skip", func(t *testing.T) {
		trackID := newSkipTestTrack(t, ctx, s, 240000)
		report(t, ctx, s, PlaybackUpdate{TrackID: trackID, State: "playing", PositionMillis: 5000, DurationMillis: 240000})
		if _, err = s.db.ExecContext(ctx, `UPDATE playback_progress SET last_played_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-31 minutes') WHERE track_id=?`, trackID); err != nil {
			t.Fatal(err)
		}
		report(t, ctx, s, PlaybackUpdate{TrackID: trackID, State: "stopped", PositionMillis: 0, DurationMillis: 240000, Continuing: true})
		if count, _ := skipCountOf(t, ctx, s, trackID); count != 0 {
			t.Fatalf("skip_count = %d, want 0", count)
		}
	})

	t.Run("last played at 29 minutes ago still counts", func(t *testing.T) {
		trackID := newSkipTestTrack(t, ctx, s, 240000)
		report(t, ctx, s, PlaybackUpdate{TrackID: trackID, State: "playing", PositionMillis: 5000, DurationMillis: 240000})
		if _, err = s.db.ExecContext(ctx, `UPDATE playback_progress SET last_played_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-29 minutes') WHERE track_id=?`, trackID); err != nil {
			t.Fatal(err)
		}
		report(t, ctx, s, PlaybackUpdate{TrackID: trackID, State: "stopped", PositionMillis: 0, DurationMillis: 240000, Continuing: true})
		if count, _ := skipCountOf(t, ctx, s, trackID); count != 1 {
			t.Fatalf("skip_count = %d, want 1", count)
		}
	})

	t.Run("already completed round is not a skip", func(t *testing.T) {
		trackID := newSkipTestTrack(t, ctx, s, 240000)
		report(t, ctx, s, PlaybackUpdate{TrackID: trackID, State: "playing", PositionMillis: 5000, DurationMillis: 240000})
		if _, err = s.db.ExecContext(ctx, `UPDATE playback_progress SET state='paused',position_ms=5000,last_completed_at=last_played_at WHERE track_id=?`, trackID); err != nil {
			t.Fatal(err)
		}
		report(t, ctx, s, PlaybackUpdate{TrackID: trackID, State: "stopped", PositionMillis: 0, DurationMillis: 240000, Continuing: true})
		if count, _ := skipCountOf(t, ctx, s, trackID); count != 0 {
			t.Fatalf("skip_count = %d, want 0", count)
		}
	})
}

func TestSkipInferenceRealisticSequence(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "skip-sequence.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	trackID := newSkipTestTrack(t, ctx, s, 240000)

	// Round one completes: late stop is not a skip, then the scrobble lands.
	report(t, ctx, s, PlaybackUpdate{TrackID: trackID, State: "playing", PositionMillis: 230000, DurationMillis: 240000})
	report(t, ctx, s, PlaybackUpdate{TrackID: trackID, State: "stopped", PositionMillis: 0, DurationMillis: 240000, Continuing: true})
	if err = s.Scrobble(ctx, trackID, 230000, 240000); err != nil {
		t.Fatal(err)
	}
	// Realistic timing: the completed round finished one second ago. Without
	// this, the same-millisecond timestamps would exercise the equality edge of
	// the completion guard instead of the intended round semantics.
	if _, err = s.db.ExecContext(ctx, `UPDATE playback_progress SET last_played_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-1 second'),last_completed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-1 second') WHERE track_id=?`, trackID); err != nil {
		t.Fatal(err)
	}
	if count, _ := skipCountOf(t, ctx, s, trackID); count != 0 {
		t.Fatalf("completed round skip_count = %d, want 0", count)
	}

	// Round two starts fresh and is skipped early.
	report(t, ctx, s, PlaybackUpdate{TrackID: trackID, State: "playing", PositionMillis: 5000, DurationMillis: 240000})
	report(t, ctx, s, PlaybackUpdate{TrackID: trackID, State: "stopped", PositionMillis: 0, DurationMillis: 240000, Continuing: true})
	if count, _ := skipCountOf(t, ctx, s, trackID); count != 1 {
		t.Fatalf("new round skip_count = %d, want 1", count)
	}
}

func TestSkipExplicitFlag(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "skip-explicit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	yes, no := true, false

	t.Run("explicit skip counts on first record", func(t *testing.T) {
		trackID := newSkipTestTrack(t, ctx, s, 240000)
		report(t, ctx, s, PlaybackUpdate{TrackID: trackID, State: "stopped", PositionMillis: 0, Continuing: true, Skipped: &yes})
		count, lastSkippedAt := skipCountOf(t, ctx, s, trackID)
		if count != 1 || lastSkippedAt == "" {
			t.Fatalf("skip_count = %d, lastSkippedAt = %q", count, lastSkippedAt)
		}
	})

	t.Run("explicit skip counts on existing record", func(t *testing.T) {
		trackID := newSkipTestTrack(t, ctx, s, 240000)
		report(t, ctx, s, PlaybackUpdate{TrackID: trackID, State: "playing", PositionMillis: 200000, DurationMillis: 240000})
		report(t, ctx, s, PlaybackUpdate{TrackID: trackID, State: "stopped", PositionMillis: 200000, DurationMillis: 240000, Continuing: true, Skipped: &yes})
		if count, _ := skipCountOf(t, ctx, s, trackID); count != 1 {
			t.Fatalf("skip_count = %d, want 1", count)
		}
	})

	t.Run("explicit false suppresses inference", func(t *testing.T) {
		trackID := newSkipTestTrack(t, ctx, s, 240000)
		report(t, ctx, s, PlaybackUpdate{TrackID: trackID, State: "playing", PositionMillis: 5000, DurationMillis: 240000})
		report(t, ctx, s, PlaybackUpdate{TrackID: trackID, State: "stopped", PositionMillis: 0, DurationMillis: 240000, Continuing: true, Skipped: &no})
		if count, _ := skipCountOf(t, ctx, s, trackID); count != 0 {
			t.Fatalf("skip_count = %d, want 0", count)
		}
	})

	t.Run("explicit true requires stopped state", func(t *testing.T) {
		trackID := newSkipTestTrack(t, ctx, s, 240000)
		if err = s.UpdatePlayback(ctx, PlaybackUpdate{TrackID: trackID, State: "playing", Skipped: &yes}); err == nil || !strings.Contains(err.Error(), "stopped") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("repeat explicit skip counts twice", func(t *testing.T) {
		trackID := newSkipTestTrack(t, ctx, s, 240000)
		report(t, ctx, s, PlaybackUpdate{TrackID: trackID, State: "playing", PositionMillis: 5000, DurationMillis: 240000})
		report(t, ctx, s, PlaybackUpdate{TrackID: trackID, State: "stopped", PositionMillis: 0, DurationMillis: 240000, Continuing: true, Skipped: &yes})
		report(t, ctx, s, PlaybackUpdate{TrackID: trackID, State: "stopped", PositionMillis: 0, DurationMillis: 240000, Continuing: true, Skipped: &yes})
		if count, _ := skipCountOf(t, ctx, s, trackID); count != 2 {
			t.Fatalf("skip_count = %d, want 2", count)
		}
	})

	t.Run("explicit skip on never played track stays out of track JSON", func(t *testing.T) {
		trackID := newSkipTestTrack(t, ctx, s, 240000)
		report(t, ctx, s, PlaybackUpdate{TrackID: trackID, State: "stopped", PositionMillis: 0, Continuing: true, Skipped: &yes})
		track, err := s.TrackByID(ctx, trackID)
		if err != nil {
			t.Fatal(err)
		}
		if track.SkipCount != nil {
			t.Fatalf("never-played explicit skip must not expose skipCount: %+v", track)
		}
		if count, _ := skipCountOf(t, ctx, s, trackID); count != 1 {
			t.Fatalf("skip_count = %d, want 1", count)
		}
	})

	t.Run("client id accepted and bounded", func(t *testing.T) {
		trackID := newSkipTestTrack(t, ctx, s, 240000)
		if err = s.UpdatePlayback(ctx, PlaybackUpdate{TrackID: trackID, State: "playing", PositionMillis: 1000, ClientID: strings.Repeat("c", 128)}); err != nil {
			t.Fatalf("128-byte clientId rejected: %v", err)
		}
		if err = s.UpdatePlayback(ctx, PlaybackUpdate{TrackID: trackID, State: "playing", PositionMillis: 1000, ClientID: strings.Repeat("c", 129)}); err == nil || !strings.Contains(err.Error(), "128") {
			t.Fatalf("129-byte clientId err = %v", err)
		}
	})
}

func TestSkipCountJSONAndHistory(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "skip-json.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	trackID := newSkipTestTrack(t, ctx, s, 240000)

	track, err := s.TrackByID(ctx, trackID)
	if err != nil {
		t.Fatal(err)
	}
	if track.SkipCount != nil || track.ViewCount != nil || track.LastViewedAt != nil {
		t.Fatalf("unplayed track exposes playback counters: %+v", track)
	}

	report(t, ctx, s, PlaybackUpdate{TrackID: trackID, State: "playing", PositionMillis: 5000, DurationMillis: 240000})
	report(t, ctx, s, PlaybackUpdate{TrackID: trackID, State: "stopped", PositionMillis: 0, DurationMillis: 240000, Continuing: true})

	track, err = s.TrackByID(ctx, trackID)
	if err != nil {
		t.Fatal(err)
	}
	if track.SkipCount == nil || *track.SkipCount != 1 || track.ViewCount == nil || *track.ViewCount != 0 {
		t.Fatalf("played track extras: %+v", track)
	}

	history, total, err := s.PlaybackHistory(ctx, 100, 0)
	if err != nil || total != 1 || len(history) != 1 {
		t.Fatalf("history: %v %d %v", history, total, err)
	}
	if history[0].SkipCount != 1 || history[0].LastSkippedAt == "" {
		t.Fatalf("history record: %+v", history[0])
	}

	if err = s.ClearPlaybackHistory(ctx); err != nil {
		t.Fatal(err)
	}
	report(t, ctx, s, PlaybackUpdate{TrackID: trackID, State: "playing", PositionMillis: 5000, DurationMillis: 240000})
	history, total, err = s.PlaybackHistory(ctx, 100, 0)
	if err != nil || total != 1 {
		t.Fatalf("history after clear: %v %d %v", history, total, err)
	}
	if history[0].SkipCount != 0 || history[0].LastSkippedAt != "" {
		t.Fatalf("cleared history record: %+v", history[0])
	}
}

func TestSkipInferenceConcurrentReports(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "skip-concurrent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	trackID := newSkipTestTrack(t, ctx, s, 240000)

	const workers = 20
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.UpdatePlayback(ctx, PlaybackUpdate{TrackID: trackID, State: "playing", PositionMillis: 5000, DurationMillis: 240000}); err != nil {
				errs <- err
				return
			}
			if err := s.UpdatePlayback(ctx, PlaybackUpdate{TrackID: trackID, State: "stopped", PositionMillis: 0, DurationMillis: 240000, Continuing: true}); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent update: %v", err)
	}
	count, _ := skipCountOf(t, ctx, s, trackID)
	if count < 1 || count > workers {
		t.Fatalf("skip_count = %d, want 1..%d", count, workers)
	}
}

func TestSkipInferenceMigrationOnExistingDatabase(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "skip-migration.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = store.db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL) STRICT`); err != nil {
		t.Fatal(err)
	}
	for version := 1; version <= 17; version++ {
		matches, err := migrationFiles.ReadDir("migrations")
		if err != nil {
			t.Fatal(err)
		}
		var name string
		for _, entry := range matches {
			if len(entry.Name()) >= 3 && entry.Name()[:3] == fmt.Sprintf("%03d", version) {
				name = entry.Name()
				break
			}
		}
		if name == "" {
			t.Fatalf("missing migration %d", version)
		}
		script, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.db.ExecContext(ctx, string(script)); err != nil {
			t.Fatalf("migration %d: %v", version, err)
		}
		if _, err = store.db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name) VALUES(?,?)`, version, name); err != nil {
			t.Fatal(err)
		}
	}

	result, err := store.db.ExecContext(ctx, `INSERT INTO libraries(name,root_path) VALUES('Test','/music')`)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := result.LastInsertId()
	result, err = store.db.ExecContext(ctx, `INSERT INTO albums(library_id,title,sort_title,grouping_key) VALUES(?,'Album','album','album')`, libraryID)
	if err != nil {
		t.Fatal(err)
	}
	albumID, _ := result.LastInsertId()
	result, err = store.db.ExecContext(ctx, `INSERT INTO tracks(album_id,title,sort_title,duration_ms) VALUES(?,'Track','track',240000)`, albumID)
	if err != nil {
		t.Fatal(err)
	}
	trackID, _ := result.LastInsertId()
	if _, err = store.db.ExecContext(ctx, `INSERT INTO playback_progress(track_id,state,position_ms,duration_ms,play_count,last_played_at) VALUES(?,'playing',5000,240000,2,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, trackID); err != nil {
		t.Fatal(err)
	}

	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var skipCount int64
	var lastSkippedAt any
	if err = store.db.QueryRowContext(ctx, `SELECT skip_count,last_skipped_at FROM playback_progress WHERE track_id=?`, trackID).Scan(&skipCount, &lastSkippedAt); err != nil {
		t.Fatal(err)
	}
	if skipCount != 0 || lastSkippedAt != nil {
		t.Fatalf("migrated row: skip_count=%d last_skipped_at=%v", skipCount, lastSkippedAt)
	}
	// Existing rows keep counting after migration.
	if err = store.UpdatePlayback(ctx, PlaybackUpdate{TrackID: trackID, State: "stopped", PositionMillis: 0, DurationMillis: 240000, Continuing: true}); err != nil {
		t.Fatal(err)
	}
	if count, lastSkipped := skipCountOf(t, ctx, store, trackID); count != 1 || lastSkipped == "" {
		t.Fatalf("post-migration inference: %d %q", count, lastSkipped)
	}
}
