package storage

import (
	"github.com/lux032/032music-server/internal/metadata"
	"testing"
)

func TestAlbumWorkRemoveAddRemoveStaysSuppressed(t *testing.T) {
	s, ctx, album := albumWorkFixture(t, "Hollow Knight Original Soundtrack", "Track")
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	works, _ := s.ListWorks(ctx, WorkFilters{})
	id := works[0].ID
	if e := s.RemoveWorkAlbum(ctx, id, album); e != nil {
		t.Fatal(e)
	}
	if e := s.AddWorkAlbum(ctx, id, album, ""); e != nil {
		t.Fatal(e)
	}
	if e := s.RemoveWorkAlbum(ctx, id, album); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	var count int
	if e := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM album_works WHERE album_id=?`, album).Scan(&count); e != nil || count != 0 {
		t.Fatalf("rebuilt link %d %v", count, e)
	}
}
func TestTrackWorkExplicitTagSuppressedAfterRemovalAndDeletion(t *testing.T) {
	s, ctx, album := albumWorkFixture(t, "Plain Album", "Song")
	lib, _ := s.LibraryByRoot(ctx, "/music")
	input := ImportInput{LibraryID: lib.ID, RelativePath: "Album/01.flac", FileSize: 1, ModifiedAtNS: 2, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Plain Album", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 1, Raw: map[string][]string{"WORKTITLE": {"TVアニメ『Title』オープニングテーマ"}}}}
	if e := s.ImportTrack(ctx, input); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	works, _ := s.ListWorks(ctx, WorkFilters{})
	if len(works) != 1 {
		t.Fatalf("works %+v", works)
	}
	var track int64
	if e := s.db.QueryRowContext(ctx, `SELECT id FROM tracks WHERE album_id=?`, album).Scan(&track); e != nil {
		t.Fatal(e)
	}
	if _, e := s.UpdateWork(ctx, works[0].ID, WorkInput{Title: "Renamed Y", Type: "game"}); e != nil {
		t.Fatal(e)
	}
	if e := s.RemoveWorkTrack(ctx, works[0].ID, track, "", 0, 0); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	var count int
	if e := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_tracks WHERE track_id=?`, track).Scan(&count); e != nil || count != 0 {
		t.Fatalf("removed tag rebuilt %d %v", count, e)
	}
	if _, e := s.db.ExecContext(ctx, `DELETE FROM track_work_suppressions WHERE track_id=?`, track); e != nil {
		t.Fatal(e)
	}
	input.ModifiedAtNS = 3
	if e := s.ImportTrack(ctx, input); e != nil {
		t.Fatal(e)
	}
	if e := s.DeleteWork(ctx, works[0].ID); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	works, _ = s.ListWorks(ctx, WorkFilters{})
	if len(works) != 0 {
		t.Fatalf("deleted tag work rebuilt %+v", works)
	}
}
