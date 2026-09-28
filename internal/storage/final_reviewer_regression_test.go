package storage

import (
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

func addFRBangumiProfile(t *testing.T, f albumMergeFixture, workID int64, external, title, typ string) {
	t.Helper()
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO work_external_profiles(work_id,source,external_id,title,type,raw_json,fetched_at) VALUES(?,'bangumi',?,?,?,'{}','2026-01-01T00:00:00Z')`, workID, external, title, typ); err != nil {
		t.Fatal(err)
	}
}

func TestFRH1RemoveBangumiAnimeSuppressesOtherAlbumInference(t *testing.T) {
	f := newAlbumMergeFixture(t)
	input := ImportInput{LibraryID: f.library, RelativePath: "X/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Track", Album: "X Original Soundtrack", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1}}
	if err := f.store.ImportTrack(f.ctx, input); err != nil {
		t.Fatal(err)
	}
	albumID := f.albumID(t, input.Metadata.Album)
	work, err := f.store.CreateWork(f.ctx, WorkInput{Title: "X", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	addFRBangumiProfile(t, f, work.ID, "100", "X", "anime")
	if _, err = f.store.db.ExecContext(f.ctx, `INSERT INTO album_works(album_id,work_id,role,source,inferred_key) VALUES(?,?,'ost','bangumi','bangumi:1:100')`, albumID, work.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.RefreshAlbumWorks(f.ctx, true); err != nil {
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
	if n := f.count(t, `SELECT COUNT(*) FROM album_works WHERE album_id=?`, albumID); n != 0 {
		t.Fatalf("album association revived=%d", n)
	}
}

func TestFRH1DeleteBangumiAnimeSuppressesOtherAlbumInference(t *testing.T) {
	f := newAlbumMergeFixture(t)
	input := ImportInput{LibraryID: f.library, RelativePath: "X/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Track", Album: "X Original Soundtrack", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1}}
	if err := f.store.ImportTrack(f.ctx, input); err != nil {
		t.Fatal(err)
	}
	albumID := f.albumID(t, input.Metadata.Album)
	work, _ := f.store.CreateWork(f.ctx, WorkInput{Title: "X", Type: "anime"})
	addFRBangumiProfile(t, f, work.ID, "101", "X", "anime")
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO album_works(album_id,work_id,role,source,inferred_key) VALUES(?,?,'ost','bangumi','bangumi:1:101')`, albumID, work.ID); err != nil {
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
		t.Fatalf("album association revived=%d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM works WHERE normalized_title='x' AND type='other'`); n != 0 {
		t.Fatalf("other work recreated=%d", n)
	}
}

func TestFRH1DeletedOtherAutoSuppressionBlocksBangumiAnimeConfirmation(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "X/01.flac", "X Original Soundtrack", "X", 1, 1)
	albumID := f.albumID(t, "X Original Soundtrack")
	if _, err := f.store.RefreshAlbumWorks(f.ctx, true); err != nil {
		t.Fatal(err)
	}
	var autoWork int64
	if err := f.store.db.QueryRowContext(f.ctx, `SELECT work_id FROM album_works WHERE album_id=? AND source='auto'`, albumID).Scan(&autoWork); err != nil {
		t.Fatal(err)
	}
	if err := f.store.RemoveWorkAlbum(f.ctx, autoWork, albumID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.CleanupAutoWorks(f.ctx, &RefreshStats{}); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM works WHERE id=?`, autoWork); n != 0 {
		t.Fatalf("auto work not cleaned=%d", n)
	}
	targets, err := f.store.AlbumsForBangumiTieup(f.ctx, true, albumID)
	if err != nil || len(targets) != 1 {
		t.Fatalf("targets=%+v err=%v", targets, err)
	}
	if err = f.store.SaveAlbumSubjectCandidates(f.ctx, albumID, []AlbumSubjectCandidate{{ExternalID: "1", Title: "X", Tieups: []BangumiTieup{{SubjectID: 102, Title: "X", Type: "anime", Role: "op"}}}}); err != nil {
		t.Fatal(err)
	}
	candidates, _ := f.store.AlbumSubjectCandidates(f.ctx, albumID)
	outcome, _, err := f.store.ConfirmAlbumSubjectCandidate(f.ctx, albumID, candidates[0].ID, 0, targets[0].Fingerprint, true, []int64{102})
	if err != nil || outcome != "review" {
		t.Fatalf("outcome=%s err=%v", outcome, err)
	}
}

func TestFRH1ConcreteAnimeSuppressionDoesNotBlockGame(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "X Original Soundtrack", "X", 1, 1)
	f.importFile(t, "B/01.flac", "Other Album", "X", 1, 1)
	albumA, albumB := f.albumID(t, "X Original Soundtrack"), f.albumID(t, "Other Album")
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO album_work_suppressions(album_id,inferred_key) VALUES(?,'x|anime|0')`, albumA); err != nil {
		t.Fatal(err)
	}
	confirm := func(albumID, subject int64) string {
		targets, err := f.store.AlbumsForBangumiTieup(f.ctx, true, albumID)
		if err != nil || len(targets) != 1 {
			t.Fatalf("targets=%+v err=%v", targets, err)
		}
		if err = f.store.SaveAlbumSubjectCandidates(f.ctx, albumID, []AlbumSubjectCandidate{{ExternalID: "1", Title: "X", Tieups: []BangumiTieup{{SubjectID: subject, Title: "X", Type: "game", Role: "op"}}}}); err != nil {
			t.Fatal(err)
		}
		candidates, _ := f.store.AlbumSubjectCandidates(f.ctx, albumID)
		outcome, _, err := f.store.ConfirmAlbumSubjectCandidate(f.ctx, albumID, candidates[0].ID, 0, targets[0].Fingerprint, true, []int64{subject})
		if err != nil {
			t.Fatal(err)
		}
		return outcome
	}
	if outcome := confirm(albumA, 301); outcome != "review" {
		t.Fatalf("suppressed album outcome=%s", outcome)
	}
	if outcome := confirm(albumB, 302); outcome != "succeeded" {
		t.Fatalf("other album outcome=%s", outcome)
	}
}
