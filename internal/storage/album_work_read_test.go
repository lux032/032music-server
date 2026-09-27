package storage

import (
	"testing"
)

func TestWorksForAlbumUnionDeduplicates(t *testing.T) {
	s, ctx, album := albumWorkFixture(t, "OST Original Soundtrack", "Song")
	first, e := s.CreateWork(ctx, WorkInput{Title: "First", Type: "anime"})
	if e != nil {
		t.Fatal(e)
	}
	second, e := s.CreateWork(ctx, WorkInput{Title: "Second", Type: "anime"})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.AddWorkAlbum(ctx, first.ID, album, "ost"); e != nil {
		t.Fatal(e)
	}
	var track int64
	if e = s.db.QueryRowContext(ctx, `SELECT id FROM tracks WHERE album_id=?`, album).Scan(&track); e != nil {
		t.Fatal(e)
	}
	for _, id := range []int64{first.ID, second.ID} {
		if e = s.AddWorkTrack(ctx, id, WorkTrackInput{TrackID: track, Role: "op"}); e != nil {
			t.Fatal(e)
		}
	}
	got, e := s.WorksForAlbum(ctx, album)
	if e != nil || len(got) != 2 || got[0].ID != first.ID || got[0].TrackID != 0 || got[1].ID != second.ID || got[1].TrackTitle != "Song" {
		t.Fatalf("works=%+v err=%v", got, e)
	}
}
