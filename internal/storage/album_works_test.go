package storage

import (
	"context"
	"github.com/lux032/032music-server/internal/metadata"
	"path/filepath"
	"testing"
)

func TestRefreshAlbumWorksAndSuppression(t *testing.T) {
	ctx := context.Background()
	s, e := Open(filepath.Join(t.TempDir(), "works.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.EnsureLibrary(ctx, "Test", "/music"); e != nil {
		t.Fatal(e)
	}
	lib, _ := s.LibraryByRoot(ctx, "/music")
	input := ImportInput{LibraryID: lib.ID, RelativePath: "Music/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Track", Album: "Hollow Knight Original Soundtrack", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 1}}
	if e = s.ImportTrack(ctx, input); e != nil {
		t.Fatal(e)
	}
	stats, e := s.RefreshAlbumWorks(ctx, true)
	if e != nil {
		t.Fatal(e)
	}
	if stats.AlbumsRefreshed != 1 || stats.WorksCreated != 1 {
		t.Fatalf("refresh: %+v", stats)
	}
	works, e := s.ListWorks(ctx, WorkFilters{})
	if e != nil || len(works) != 1 || works[0].TrackCount != 1 {
		t.Fatalf("works: %+v, %v", works, e)
	}
	albums, e := s.AlbumsForWork(ctx, works[0].ID)
	if e != nil || len(albums) != 1 {
		t.Fatalf("albums: %+v %v", albums, e)
	}
	if e = s.DeleteWork(ctx, works[0].ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	works, e = s.ListWorks(ctx, WorkFilters{})
	if e != nil || len(works) != 0 {
		t.Fatalf("suppressed: %+v %v", works, e)
	}
}
