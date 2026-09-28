package storage

import (
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

func TestRVH1SeasonTwoBangumiRemovalDoesNotReviveAlbumOrTrackInference(t *testing.T) {
	for _, tc := range []struct {
		name, album string
		raw         map[string][]string
	}{
		{"album-title", "TVアニメ『X Season 2』 Original Soundtrack", nil},
		{"track-tag", "Plain Album", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAlbumMergeFixture(t)
			input := ImportInput{LibraryID: f.library, RelativePath: "A/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: tc.album, Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1, Raw: tc.raw}}
			if err := f.store.ImportTrack(f.ctx, input); err != nil {
				t.Fatal(err)
			}
			albumID := f.albumID(t, tc.album)
			work, err := f.store.CreateWork(f.ctx, WorkInput{Title: "X Season 2", Type: "anime"})
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "track-tag" {
				input.Metadata.Raw = map[string][]string{"WORKTITLE": {"TVアニメ『X Season 2』オープニングテーマ"}}
			}
			if _, err = f.store.db.ExecContext(f.ctx, `INSERT INTO album_works(album_id,work_id,source,inferred_key) VALUES(?,?,'bangumi','bangumi:1:2')`, albumID, work.ID); err != nil {
				t.Fatal(err)
			}
			if err = f.store.RemoveWorkAlbum(f.ctx, work.ID, albumID); err != nil {
				t.Fatal(err)
			}
			input.ModifiedAtNS = 2
			if err = f.store.ImportTrack(f.ctx, input); err != nil {
				t.Fatal(err)
			}
			if _, err = f.store.RefreshAlbumWorks(f.ctx, true); err != nil {
				t.Fatal(err)
			}
			if n := f.count(t, `SELECT COUNT(*) FROM album_works WHERE album_id=? AND work_id=?`, albumID, work.ID); n != 0 {
				t.Fatalf("album link revived=%d", n)
			}
			if n := f.count(t, `SELECT COUNT(*) FROM work_tracks wt JOIN tracks t ON t.id=wt.track_id WHERE t.album_id=? AND wt.work_id=?`, albumID, work.ID); n != 0 {
				t.Fatalf("track link revived=%d", n)
			}
			if n := f.count(t, `SELECT COUNT(*) FROM album_work_suppressions WHERE album_id=? AND inferred_key='x|anime|2'`, albumID); n != 1 {
				t.Fatalf("season suppression=%d", n)
			}
		})
	}
}

func TestRVH2DeleteBangumiWorkSuppressesAlbumAndTrackRecreation(t *testing.T) {
	f := newAlbumMergeFixture(t)
	input := ImportInput{LibraryID: f.library, RelativePath: "A/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "TVアニメ『Deleted X』 Original Soundtrack", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1, Raw: map[string][]string{"WORKTITLE": {"TVアニメ『Deleted X』オープニングテーマ"}}}}
	if err := f.store.ImportTrack(f.ctx, input); err != nil {
		t.Fatal(err)
	}
	albumID := f.albumID(t, input.Metadata.Album)
	var trackID int64
	_ = f.store.db.QueryRowContext(f.ctx, `SELECT id FROM tracks WHERE album_id=?`, albumID).Scan(&trackID)
	work, _ := f.store.CreateWork(f.ctx, WorkInput{Title: "Deleted X", Type: "anime"})
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO album_works(album_id,work_id,source,inferred_key) VALUES(?,?,'bangumi','bangumi:1:2')`, albumID, work.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO work_tracks(work_id,track_id,role,source,inferred_key) VALUES(?,?,'op','bangumi','bangumi:1:2')`, work.ID, trackID); err != nil {
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
	if n := f.count(t, `SELECT COUNT(*) FROM works WHERE normalized_title='deleted x'`); n != 0 {
		t.Fatalf("work recreated=%d", n)
	}
}

func TestRVH3RejectedWorkMatchIsNotReusedByAlbumConfirmation(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Music", "Music", 1, 1)
	albumID := f.albumID(t, "Music")
	local, _ := f.store.CreateWork(f.ctx, WorkInput{Title: "Rejected X", Type: "anime"})
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO work_match_candidates(work_id,source,external_id,title,type,score,status) VALUES(?,'bangumi','123','Rejected X','anime',90,'rejected')`, local.ID); err != nil {
		t.Fatal(err)
	}
	targets, _ := f.store.AlbumsForBangumiTieup(f.ctx, true, albumID)
	if err := f.store.SaveAlbumSubjectCandidates(f.ctx, albumID, []AlbumSubjectCandidate{{ExternalID: "1", Title: "Music", Tieups: []BangumiTieup{{SubjectID: 123, Title: "Rejected X", Type: "anime", Role: "op", Date: "2024-01-01"}}}}); err != nil {
		t.Fatal(err)
	}
	candidates, _ := f.store.AlbumSubjectCandidates(f.ctx, albumID)
	outcome, ids, err := f.store.ConfirmAlbumSubjectCandidate(f.ctx, albumID, candidates[0].ID, 0, targets[0].Fingerprint, true, []int64{123})
	if err != nil || outcome != "succeeded" || len(ids) != 1 || ids[0] == local.ID {
		t.Fatalf("outcome=%s ids=%v local=%d err=%v", outcome, ids, local.ID, err)
	}
}

func TestRVM3AutoTrackSuppressionBlocksBangumiTrackWrite(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Music", "Music", 1, 1)
	albumID := f.albumID(t, "Music")
	work, _ := f.store.CreateWork(f.ctx, WorkInput{Title: "Work", Type: "anime"})
	var trackID int64
	_ = f.store.db.QueryRowContext(f.ctx, `SELECT id FROM tracks WHERE album_id=?`, albumID).Scan(&trackID)
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO work_tracks(work_id,track_id,role,source,inferred_key) VALUES(?,?,'op','auto','work|anime|0')`, work.ID, trackID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.RemoveWorkTrack(f.ctx, work.ID, trackID, "op", 0, 0); err != nil {
		t.Fatal(err)
	}
	tx, _ := f.store.db.BeginTx(f.ctx, nil)
	if err := linkBangumiAlbumTracks(f.ctx, tx, albumID, work.ID, "Music", "op", "bangumi:1:2"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM work_tracks WHERE work_id=? AND track_id=?`, work.ID, trackID); n != 0 {
		t.Fatalf("suppressed row written=%d", n)
	}
}

func TestRVL4IdentityCollisionReturnsReviewAndKeepsCandidate(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Music", "Music", 1, 1)
	albumID := f.albumID(t, "Music")
	first, _ := f.store.CreateWork(f.ctx, WorkInput{Title: "Collision", Type: "anime", Year: 2024})
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO work_external_profiles(work_id,source,external_id,title,type,year,raw_json,fetched_at) VALUES(?,'bangumi','old','Collision','anime',2024,'{}','2024-01-01T00:00:00Z')`, first.ID); err != nil {
		t.Fatal(err)
	}
	targets, _ := f.store.AlbumsForBangumiTieup(f.ctx, true, albumID)
	if err := f.store.SaveAlbumSubjectCandidates(f.ctx, albumID, []AlbumSubjectCandidate{{ExternalID: "1", Title: "Music", Evidence: []string{"原有证据"}, Tieups: []BangumiTieup{{SubjectID: 2, Title: "Collision", Type: "anime", Role: "op", Date: "2024-01-01"}}}}); err != nil {
		t.Fatal(err)
	}
	candidates, _ := f.store.AlbumSubjectCandidates(f.ctx, albumID)
	outcome, _, err := f.store.ConfirmAlbumSubjectCandidate(f.ctx, albumID, candidates[0].ID, 0, targets[0].Fingerprint, true, []int64{2})
	if err != nil || outcome != "review" {
		t.Fatalf("outcome=%s err=%v", outcome, err)
	}
	candidates, _ = f.store.AlbumSubjectCandidates(f.ctx, albumID)
	if candidates[0].Status != "candidate" || len(candidates[0].Evidence) != 2 || candidates[0].Evidence[0] != "原有证据" || candidates[0].Evidence[1] != "本地作品身份冲突，需人工审核" {
		t.Fatalf("candidate=%+v", candidates[0])
	}
	outcome, _, err = f.store.ConfirmAlbumSubjectCandidate(f.ctx, albumID, candidates[0].ID, 0, targets[0].Fingerprint, true, []int64{2})
	if err != nil || outcome != "review" {
		t.Fatalf("second outcome=%s err=%v", outcome, err)
	}
	candidates, _ = f.store.AlbumSubjectCandidates(f.ctx, albumID)
	if len(candidates[0].Evidence) != 2 {
		t.Fatalf("duplicate evidence=%v", candidates[0].Evidence)
	}
}

func TestRVL6BoundBangumiProfileRawAndFetchedAtArePreserved(t *testing.T) {
	f := newAlbumMergeFixture(t)
	work, _ := f.store.CreateWork(f.ctx, WorkInput{Title: "Bound", Type: "anime"})
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO work_external_profiles(work_id,source,external_id,title,type,raw_json,fetched_at) VALUES(?,'bangumi','123','Bound','anime','{"old":true}','2020-01-01T00:00:00Z')`, work.ID); err != nil {
		t.Fatal(err)
	}
	tx, _ := f.store.db.BeginTx(f.ctx, nil)
	id, err := resolveBangumiWork(f.ctx, tx, BangumiTieup{SubjectID: 123, Title: "Bound", Type: "anime", Raw: []byte(`{"new":true}`)}, 0)
	if err != nil || id != work.ID {
		t.Fatalf("id=%d err=%v", id, err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var raw, fetched string
	if err = f.store.db.QueryRowContext(f.ctx, `SELECT raw_json,fetched_at FROM work_external_profiles WHERE work_id=?`, work.ID).Scan(&raw, &fetched); err != nil || raw != `{"old":true}` || fetched != "2020-01-01T00:00:00Z" {
		t.Fatalf("raw=%s fetched=%s err=%v", raw, fetched, err)
	}
}

func TestRVM5AlbumMergeUpgradesTargetAutoToSourceBangumi(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "T/01.flac", "Target", "A", 1, 1)
	f.importFile(t, "S/01.flac", "Source", "B", 1, 1)
	target, source := f.albumID(t, "Target"), f.albumID(t, "Source")
	work, _ := f.store.CreateWork(f.ctx, WorkInput{Title: "Work", Type: "anime"})
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO album_works(album_id,work_id,role,source,inferred_key) VALUES(?,?,'other','auto','auto')`, target, work.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO album_works(album_id,work_id,role,source,inferred_key) VALUES(?,?,'ost','bangumi','bangumi:1:2')`, source, work.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.MergeAlbums(f.ctx, target, []int64{source}); err != nil {
		t.Fatal(err)
	}
	var gotSource, role string
	if err := f.store.db.QueryRowContext(f.ctx, `SELECT source,role FROM album_works WHERE album_id=? AND work_id=?`, target, work.ID).Scan(&gotSource, &role); err != nil || gotSource != "bangumi" || role != "ost" {
		t.Fatalf("source=%s role=%s err=%v", gotSource, role, err)
	}
}

func TestRVL5RemovingManualTrackDoesNotWriteSuppression(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Album", "Song", 1, 1)
	work, _ := f.store.CreateWork(f.ctx, WorkInput{Title: "Work", Type: "anime"})
	var trackID int64
	_ = f.store.db.QueryRowContext(f.ctx, `SELECT id FROM tracks LIMIT 1`).Scan(&trackID)
	if err := f.store.AddWorkTrack(f.ctx, work.ID, WorkTrackInput{TrackID: trackID, Role: "op"}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.RemoveWorkTrack(f.ctx, work.ID, trackID, "op", 0, 0); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM track_work_suppressions WHERE track_id=?`, trackID); n != 0 {
		t.Fatalf("manual suppression=%d", n)
	}
}
