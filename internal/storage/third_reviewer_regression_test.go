package storage

import (
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

func TestTRH1aRenamedWorkRemovalWritesOriginalIdentityAndDoesNotRevive(t *testing.T) {
	f := newAlbumMergeFixture(t)
	input := ImportInput{LibraryID: f.library, RelativePath: "X/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "TVアニメ『X』OST", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1}}
	if err := f.store.ImportTrack(f.ctx, input); err != nil {
		t.Fatal(err)
	}
	albumID := f.albumID(t, input.Metadata.Album)
	work, _ := f.store.CreateWork(f.ctx, WorkInput{Title: "X", Type: "anime"})
	addFRBangumiProfile(t, f, work.ID, "201", "X", "anime")
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO album_works(album_id,work_id,source,inferred_key) VALUES(?,?,'bangumi','bangumi:1:201')`, albumID, work.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.UpdateWork(f.ctx, work.ID, WorkInput{Title: "Y", Type: "anime"}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.RemoveWorkAlbum(f.ctx, work.ID, albumID); err != nil {
		t.Fatal(err)
	}
	input.ModifiedAtNS = 2
	if err := f.store.ImportTrack(f.ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RefreshAlbumWorks(f.ctx, true); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM album_works WHERE album_id=?`, albumID); n != 0 {
		t.Fatalf("links revived=%d", n)
	}
}

func TestTRH1aTranslatedIdentityRemovalDoesNotRevive(t *testing.T) {
	f := newAlbumMergeFixture(t)
	input := ImportInput{LibraryID: f.library, RelativePath: "X/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "X物语 原声集", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1}}
	if err := f.store.ImportTrack(f.ctx, input); err != nil {
		t.Fatal(err)
	}
	albumID := f.albumID(t, input.Metadata.Album)
	work, _ := f.store.CreateWork(f.ctx, WorkInput{Title: "Xの物語", TranslatedTitle: "X物语", Type: "anime"})
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO work_external_profiles(work_id,source,external_id,title,translated_title,type,raw_json,fetched_at) VALUES(?,'bangumi','202','Xの物語','X物语','anime','{}','2026-01-01T00:00:00Z')`, work.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO album_works(album_id,work_id,source,inferred_key) VALUES(?,?,'bangumi','bangumi:1:202')`, albumID, work.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.RemoveWorkAlbum(f.ctx, work.ID, albumID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RefreshAlbumWorks(f.ctx, true); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM album_works WHERE album_id=?`, albumID); n != 0 {
		t.Fatalf("translated link revived=%d", n)
	}
}

func TestTRH1bRemovedBangumiTrackOldNameDoesNotReviveAfterRename(t *testing.T) {
	f := newAlbumMergeFixture(t)
	input := ImportInput{LibraryID: f.library, RelativePath: "A/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Plain", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1}}
	if err := f.store.ImportTrack(f.ctx, input); err != nil {
		t.Fatal(err)
	}
	albumID := f.albumID(t, "Plain")
	var trackID int64
	_ = f.store.db.QueryRowContext(f.ctx, `SELECT id FROM tracks WHERE album_id=?`, albumID).Scan(&trackID)
	work, _ := f.store.CreateWork(f.ctx, WorkInput{Title: "Old X", Type: "anime"})
	addFRBangumiProfile(t, f, work.ID, "203", "Old X", "anime")
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO work_tracks(work_id,track_id,role,source,inferred_key) VALUES(?,?,'op','bangumi','bangumi:1:203')`, work.ID, trackID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.UpdateWork(f.ctx, work.ID, WorkInput{Title: "New Y", Type: "anime"}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.RemoveWorkTrack(f.ctx, work.ID, trackID, "op", 0, 0); err != nil {
		t.Fatal(err)
	}
	input.Metadata.Raw = map[string][]string{"WORKTITLE": {"TVアニメ『Old X』オープニングテーマ"}}
	input.ModifiedAtNS = 2
	if err := f.store.ImportTrack(f.ctx, input); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM work_tracks WHERE track_id=? AND source='auto'`, trackID); n != 0 {
		t.Fatalf("auto track revived=%d", n)
	}
}

func TestTRM1TypeChangedBangumiDeletionSuppressesOriginalAnimeInference(t *testing.T) {
	f := newAlbumMergeFixture(t)
	input := ImportInput{LibraryID: f.library, RelativePath: "X/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "TVアニメ『X』OST", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1}}
	if err := f.store.ImportTrack(f.ctx, input); err != nil {
		t.Fatal(err)
	}
	albumID := f.albumID(t, input.Metadata.Album)
	work, _ := f.store.CreateWork(f.ctx, WorkInput{Title: "X", Type: "anime"})
	addFRBangumiProfile(t, f, work.ID, "206", "X", "anime")
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO album_works(album_id,work_id,source,inferred_key) VALUES(?,?,'bangumi','bangumi:1:206')`, albumID, work.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.UpdateWork(f.ctx, work.ID, WorkInput{Title: "X", Type: "game"}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DeleteWork(f.ctx, work.ID); err != nil {
		t.Fatal(err)
	}
	input.ModifiedAtNS = 2
	if err := f.store.ImportTrack(f.ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RefreshAlbumWorks(f.ctx, true); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM album_works WHERE album_id=?`, albumID); n != 0 {
		t.Fatalf("type-changed work revived=%d", n)
	}
}

func TestTRM1AlbumSuppressionBlocksDifferentTypeTrackTag(t *testing.T) {
	f := newAlbumMergeFixture(t)
	input := ImportInput{LibraryID: f.library, RelativePath: "A/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Not X", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1, Raw: map[string][]string{"WORKTITLE": {"ゲーム『X』サウンドトラック"}}}}
	if err := f.store.ImportTrack(f.ctx, input); err != nil {
		t.Fatal(err)
	}
	albumID := f.albumID(t, "Not X")
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO album_work_suppressions(album_id,inferred_key) VALUES(?,'x|anime|0')`, albumID); err != nil {
		t.Fatal(err)
	}
	input.ModifiedAtNS = 2
	if err := f.store.ImportTrack(f.ctx, input); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM work_tracks wt JOIN tracks t ON t.id=wt.track_id WHERE t.album_id=? AND wt.source='auto'`, albumID); n != 0 {
		t.Fatalf("game tag auto rows=%d", n)
	}
}

func TestTRM2ManualAcceptOverridesTitleSuppression(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "X/01.flac", "X Original Soundtrack", "X", 1, 1)
	albumID := f.albumID(t, "X Original Soundtrack")
	if _, err := f.store.RefreshAlbumWorks(f.ctx, true); err != nil {
		t.Fatal(err)
	}
	var autoWork int64
	_ = f.store.db.QueryRowContext(f.ctx, `SELECT work_id FROM album_works WHERE album_id=? AND source='auto'`, albumID).Scan(&autoWork)
	if err := f.store.RemoveWorkAlbum(f.ctx, autoWork, albumID); err != nil {
		t.Fatal(err)
	}
	targets, _ := f.store.AlbumsForBangumiTieup(f.ctx, true, albumID)
	if err := f.store.SaveAlbumSubjectCandidates(f.ctx, albumID, []AlbumSubjectCandidate{{ExternalID: "1", Title: "X", Tieups: []BangumiTieup{{SubjectID: 204, Title: "X", Type: "anime", Role: "op"}}}}); err != nil {
		t.Fatal(err)
	}
	candidates, _ := f.store.AlbumSubjectCandidates(f.ctx, albumID)
	outcome, _, err := f.store.ConfirmAlbumSubjectCandidate(f.ctx, albumID, candidates[0].ID, 0, targets[0].Fingerprint, false, []int64{204})
	if err != nil || outcome != "succeeded" {
		t.Fatalf("outcome=%s err=%v", outcome, err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM album_works WHERE album_id=? AND source='bangumi'`, albumID); n != 1 {
		t.Fatalf("bangumi links=%d", n)
	}
	// D35/D36: other is a wildcard for manual acceptance, so the prior
	// title suppression is cleared while whole-entry refusals remain guarded.
	if n := f.count(t, `SELECT COUNT(*) FROM album_work_suppressions WHERE album_id=? AND inferred_key='x|other|0'`, albumID); n != 0 {
		t.Fatalf("wildcard suppression count=%d", n)
	}
}

func TestTRL1SuppressionDeletesAllAutoAlbumRows(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "X/01.flac", "X Original Soundtrack", "Song", 1, 1)
	albumID := f.albumID(t, "X Original Soundtrack")
	w1, _ := f.store.CreateWork(f.ctx, WorkInput{Title: "X", Type: "other"})
	w2, _ := f.store.CreateWork(f.ctx, WorkInput{Title: "Stale", Type: "other"})
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO album_works(album_id,work_id,source,inferred_key) VALUES(?,?,'auto','x|other|0'),(?,?,'auto','stale|other|0')`, albumID, w1.ID, albumID, w2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO album_work_suppressions(album_id,inferred_key) VALUES(?,'x|other|0')`, albumID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RefreshAlbumWorks(f.ctx, true); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM album_works WHERE album_id=? AND source='auto'`, albumID); n != 0 {
		t.Fatalf("auto rows remain=%d", n)
	}
}

func TestTRL2CandidateEvidenceJSONNormalizationAndConflictFallback(t *testing.T) {
	for _, bad := range []string{"null", "not-json"} {
		t.Run(bad, func(t *testing.T) {
			f := newAlbumMergeFixture(t)
			f.importFile(t, "A/01.flac", "Music", "Music", 1, 1)
			albumID := f.albumID(t, "Music")
			if err := f.store.SaveAlbumSubjectCandidates(f.ctx, albumID, []AlbumSubjectCandidate{{ExternalID: "1", Title: "Music", Evidence: nil, Tieups: []BangumiTieup{{SubjectID: 205, Title: "Collision", Type: "anime", Role: "op", Date: "2024-01-01"}}}}); err != nil {
				t.Fatal(err)
			}
			var evidence string
			_ = f.store.db.QueryRowContext(f.ctx, `SELECT evidence_json FROM album_subject_candidates WHERE album_id=?`, albumID).Scan(&evidence)
			if evidence != "[]" {
				t.Fatalf("nil evidence=%q", evidence)
			}
			if _, err := f.store.db.ExecContext(f.ctx, `UPDATE album_subject_candidates SET evidence_json=? WHERE album_id=?`, bad, albumID); err != nil {
				t.Fatal(err)
			}
			bound, _ := f.store.CreateWork(f.ctx, WorkInput{Title: "Collision", Type: "anime", Year: 2024})
			addFRBangumiProfile(t, f, bound.ID, "old-205", "Collision", "anime")
			targets, _ := f.store.AlbumsForBangumiTieup(f.ctx, true, albumID)
			candidates, _ := f.store.AlbumSubjectCandidates(f.ctx, albumID)
			outcome, _, err := f.store.ConfirmAlbumSubjectCandidate(f.ctx, albumID, candidates[0].ID, 0, targets[0].Fingerprint, true, []int64{205})
			if err != nil || outcome != "review" {
				t.Fatalf("outcome=%s err=%v", outcome, err)
			}
			_ = f.store.db.QueryRowContext(f.ctx, `SELECT evidence_json FROM album_subject_candidates WHERE album_id=?`, albumID).Scan(&evidence)
			if evidence != `["本地作品身份冲突，需人工审核"]` {
				t.Fatalf("fallback evidence=%q", evidence)
			}
		})
	}
}
