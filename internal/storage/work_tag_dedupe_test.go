package storage

import (
	"github.com/lux032/032music-server/internal/metadata"
	"testing"
)

func TestAlbumTagMatchesAlbumOrSuppression(t *testing.T) {
	s, ctx, album := albumWorkFixture(t, "TVアニメ『X』Original Soundtrack", "Track")
	lib, _ := s.LibraryByRoot(ctx, "/music")
	raw := map[string][]string{"CONTENTGROUP": {"X Original Soundtrack"}}
	for n := 1; n <= 2; n++ {
		if e := s.ImportTrack(ctx, ImportInput{LibraryID: lib.ID, RelativePath: "Album/0" + string(rune('0'+n)) + ".flac", FileSize: 1, ModifiedAtNS: int64(n + 1), Metadata: metadata.AudioMetadata{Title: "Music", Album: "TVアニメ『X』Original Soundtrack", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: n, Raw: raw}}); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	var count int
	if e := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_tracks WHERE source='auto'`).Scan(&count); e != nil || count != 0 {
		t.Fatalf("album duplicate auto=%d %v", count, e)
	}
	works, _ := s.ListWorks(ctx, WorkFilters{})
	if len(works) != 1 {
		t.Fatalf("works %+v", works)
	}
	var albumCount int
	if e := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM album_works WHERE album_id=?`, album).Scan(&albumCount); e != nil || albumCount != 1 {
		t.Fatalf("album links=%d %v", albumCount, e)
	}
	if e := s.RemoveWorkAlbum(ctx, works[0].ID, album); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	if e := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_tracks WHERE source='auto'`).Scan(&count); e != nil || count != 0 {
		t.Fatalf("suppressed album duplicate auto=%d %v", count, e)
	}
	if e := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM album_works WHERE album_id=?`, album).Scan(&albumCount); e != nil || albumCount != 0 {
		t.Fatalf("suppressed album links=%d %v", albumCount, e)
	}
}
func TestDeleteAlbumWorkWithDifferentlyTypedTagsDoesNotRecreate(t *testing.T) {
	s, ctx, _ := albumWorkFixture(t, "TVアニメ『X』Original Soundtrack", "Track")
	lib, _ := s.LibraryByRoot(ctx, "/music")
	if e := s.ImportTrack(ctx, ImportInput{LibraryID: lib.ID, RelativePath: "Album/01.flac", FileSize: 1, ModifiedAtNS: 2, Metadata: metadata.AudioMetadata{Title: "Track", Album: "TVアニメ『X』Original Soundtrack", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 1, Raw: map[string][]string{"CONTENTGROUP": {"X Original Soundtrack"}}}}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	works, _ := s.ListWorks(ctx, WorkFilters{})
	if len(works) != 1 {
		t.Fatalf("works %+v", works)
	}
	if e := s.DeleteWork(ctx, works[0].ID); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	works, _ = s.ListWorks(ctx, WorkFilters{})
	if len(works) != 0 {
		t.Fatalf("deleted work recreated %+v", works)
	}
}
func TestSeasonAlbumTagTypeDifferenceNotRecreated(t *testing.T) {
	s, ctx, album := albumWorkFixture(t, "TVアニメ『X』第2期 Original Soundtrack", "Track")
	lib, _ := s.LibraryByRoot(ctx, "/music")
	if e := s.ImportTrack(ctx, ImportInput{LibraryID: lib.ID, RelativePath: "Album/01.flac", FileSize: 1, ModifiedAtNS: 2, Metadata: metadata.AudioMetadata{Title: "Track", Album: "TVアニメ『X』第2期 Original Soundtrack", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 1, Raw: map[string][]string{"CONTENTGROUP": {"X 第2期 OST"}}}}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	works, _ := s.ListWorks(ctx, WorkFilters{})
	if len(works) != 1 {
		t.Fatalf("season works %+v", works)
	}
	if e := s.RemoveWorkAlbum(ctx, works[0].ID, album); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	var n int
	if e := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_tracks WHERE source='auto'`).Scan(&n); e != nil || n != 0 {
		t.Fatalf("season tag auto=%d %v", n, e)
	}
}
func TestUnrelatedExplicitWorkOnCompilationAlbum(t *testing.T) {
	s, ctx, _ := albumWorkFixture(t, "GOLD", "Track")
	lib, _ := s.LibraryByRoot(ctx, "/music")
	if e := s.ImportTrack(ctx, ImportInput{LibraryID: lib.ID, RelativePath: "Album/02.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Opening", Album: "GOLD", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 2, Raw: map[string][]string{"WORKTITLE": {"TVアニメ「Y」オープニングテーマ"}}}}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	var count int
	if e := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_tracks WHERE source='auto'`).Scan(&count); e != nil || count != 1 {
		t.Fatalf("explicit compilation link=%d err=%v", count, e)
	}
}
