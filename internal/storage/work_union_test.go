package storage

import "testing"

func TestTracksForWorkUnionDeduplicatesAlbumAndTrack(t *testing.T) {
	s, ctx, album := albumWorkFixture(t, "Hollow Knight Original Soundtrack", "Track")
	w, e := s.CreateWork(ctx, WorkInput{Title: "Manual", Type: "other"})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.AddWorkAlbum(ctx, w.ID, album, "ost"); e != nil {
		t.Fatal(e)
	}
	var track int64
	if e = s.db.QueryRowContext(ctx, `SELECT id FROM tracks WHERE album_id=?`, album).Scan(&track); e != nil {
		t.Fatal(e)
	}
	if e = s.AddWorkTrack(ctx, w.ID, WorkTrackInput{TrackID: track, Role: "op"}); e != nil {
		t.Fatal(e)
	}
	got, e := s.TracksForWork(ctx, w.ID)
	if e != nil || len(got) != 1 || got[0].Source != "manual" || got[0].Role != "op" {
		t.Fatalf("union %+v err %v", got, e)
	}
	w, e = s.WorkByID(ctx, w.ID)
	if e != nil || w.TrackCount != 1 {
		t.Fatalf("count %+v %v", w, e)
	}
	if e = s.RemoveWorkTrack(ctx, w.ID, track, "", 0, 0); e != nil {
		t.Fatal(e)
	}
	got, e = s.TracksForWork(ctx, w.ID)
	if e != nil || len(got) != 1 || got[0].Source != "album" {
		t.Fatalf("album fallback %+v %v", got, e)
	}
}
