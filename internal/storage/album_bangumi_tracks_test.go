package storage

import (
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

func TestR4BangumiAmoreLinksOnlyMatchingAndVersionTracks(t *testing.T) {
	f := newAlbumMergeFixture(t)
	tracks := []struct {
		path, title, trackType string
	}{
		{"A/01.flac", "Amore", "regular"},
		{"A/02.flac", "Amore (TV ver.)", "tv_size"},
		{"A/03.flac", "Amore (Instrumental)", "instrumental"},
		{"A/04.flac", "それは魔法でした", "regular"},
	}
	for i, v := range tracks {
		input := ImportInput{LibraryID: f.library, RelativePath: v.path, FileSize: 100, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: v.title, Album: "Amore", Artists: []string{"ReoNa"}, AlbumArtists: []string{"ReoNa"}, DiscNumber: 1, TrackNumber: i + 1, TrackType: v.trackType}}
		if err := f.store.ImportTrack(f.ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	albumID := f.albumID(t, "Amore")
	targets, err := f.store.AlbumsForBangumiTieup(f.ctx, true, albumID)
	if err != nil || len(targets) != 1 {
		t.Fatalf("targets=%+v err=%v", targets, err)
	}
	if err = f.store.SaveAlbumSubjectCandidates(f.ctx, albumID, []AlbumSubjectCandidate{{ExternalID: "660542", Title: "Amore", Tieups: []BangumiTieup{{SubjectID: 541285, Title: "きみが死ぬまで恋をしたい", Type: "anime", Role: "op"}}}}); err != nil {
		t.Fatal(err)
	}
	candidates, _ := f.store.AlbumSubjectCandidates(f.ctx, albumID)
	outcome, ids, err := f.store.ConfirmAlbumSubjectCandidate(f.ctx, albumID, candidates[0].ID, 0, targets[0].Fingerprint, true, []int64{541285})
	if err != nil || outcome != "succeeded" || len(ids) != 1 {
		t.Fatalf("outcome=%s ids=%v err=%v", outcome, ids, err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM album_works WHERE album_id=? AND role='other' AND source='bangumi'`, albumID); n != 1 {
		t.Fatalf("album link count=%d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM work_tracks WHERE work_id=? AND role='op' AND source='bangumi'`, ids[0]); n != 3 {
		t.Fatalf("track link count=%d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM work_tracks wt JOIN tracks t ON t.id=wt.track_id WHERE wt.work_id=? AND COALESCE(t.user_title,t.title)='それは魔法でした'`, ids[0]); n != 0 {
		t.Fatalf("c/w linked=%d", n)
	}
}

func TestR4BangumiIS6AndOSTStayAlbumOnly(t *testing.T) {
	for _, tc := range []struct {
		name, album, music, role, wantAlbumRole string
	}{
		{"IS6", "infinite synthesis 6", "infinite synthesis 6", "op", "other"},
		{"OST", "Anime OST", "Anime OST", "ost", "ost"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAlbumMergeFixture(t)
			f.importFile(t, "A/01.flac", tc.album, "Unrelated Track", 1, 1)
			albumID := f.albumID(t, tc.album)
			targets, _ := f.store.AlbumsForBangumiTieup(f.ctx, true, albumID)
			if err := f.store.SaveAlbumSubjectCandidates(f.ctx, albumID, []AlbumSubjectCandidate{{ExternalID: "375293", Title: tc.music, Tieups: []BangumiTieup{{SubjectID: 269645, Title: "Work", Type: "game", Role: tc.role}}}}); err != nil {
				t.Fatal(err)
			}
			candidates, _ := f.store.AlbumSubjectCandidates(f.ctx, albumID)
			outcome, ids, err := f.store.ConfirmAlbumSubjectCandidate(f.ctx, albumID, candidates[0].ID, 0, targets[0].Fingerprint, true, []int64{269645})
			if err != nil || outcome != "succeeded" {
				t.Fatalf("outcome=%s err=%v", outcome, err)
			}
			if n := f.count(t, `SELECT COUNT(*) FROM album_works WHERE album_id=? AND role=?`, albumID, tc.wantAlbumRole); n != 1 {
				t.Fatalf("album role count=%d", n)
			}
			if n := f.count(t, `SELECT COUNT(*) FROM work_tracks WHERE work_id=?`, ids[0]); n != 0 {
				t.Fatalf("unexpected track rows=%d", n)
			}
		})
	}
}

func TestH2BangumiTrackWinsAutoAndRemovalSuppressesRerun(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Amore", "Amore", 1, 1)
	albumID := f.albumID(t, "Amore")
	work, err := f.store.CreateWork(f.ctx, WorkInput{Title: "Anime", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	var trackID int64
	if err = f.store.db.QueryRowContext(f.ctx, `SELECT id FROM tracks WHERE album_id=?`, albumID).Scan(&trackID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.db.ExecContext(f.ctx, `INSERT INTO work_tracks(work_id,track_id,role,source,inferred_key) VALUES(?,?,'op','auto','anime|anime|0')`, work.ID, trackID); err != nil {
		t.Fatal(err)
	}
	tx, err := f.store.db.BeginTx(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = linkBangumiAlbumTracks(f.ctx, tx, albumID, work.ID, "Amore", "op", "bangumi:660542:541285"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM work_tracks WHERE work_id=? AND track_id=? AND source='bangumi'`, work.ID, trackID); n != 1 {
		t.Fatalf("bangumi rows=%d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM work_tracks WHERE work_id=? AND track_id=? AND source='auto'`, work.ID, trackID); n != 0 {
		t.Fatalf("auto rows=%d", n)
	}
	if err = f.store.RemoveWorkTrack(f.ctx, work.ID, trackID, "op", 0, 0); err != nil {
		t.Fatal(err)
	}
	tx, err = f.store.db.BeginTx(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = linkBangumiAlbumTracks(f.ctx, tx, albumID, work.ID, "Amore", "op", "bangumi:660542:541285"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM work_tracks WHERE work_id=? AND track_id=?`, work.ID, trackID); n != 0 {
		t.Fatalf("removed row resurrected=%d", n)
	}
}
