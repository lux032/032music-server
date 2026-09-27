package storage

import (
	"github.com/lux032/032music-server/internal/metadata"
	"testing"
)

func TestReimportTrackInvalidatesWorkRole(t *testing.T) {
	s, ctx, album := albumWorkFixture(t, "マリア様がみてる オープニングテーマ", "pastel pure")
	lib, _ := s.LibraryByRoot(ctx, "/music")
	if e := s.ImportTrack(ctx, ImportInput{LibraryID: lib.ID, RelativePath: "Album/02.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "pastel pure", Album: "マリア様がみてる オープニングテーマ", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 2}}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	var track int64
	if e := s.db.QueryRowContext(ctx, `SELECT id FROM tracks WHERE album_id=?`, album).Scan(&track); e != nil {
		t.Fatal(e)
	}
	input := ImportInput{LibraryID: lib.ID, RelativePath: "Album/01.flac", FileSize: 1, ModifiedAtNS: 2, Metadata: metadata.AudioMetadata{Title: "c/w song", Album: "マリア様がみてる オープニングテーマ", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 1}}
	if e := s.ImportTrack(ctx, input); e != nil {
		t.Fatal(e)
	}
	stats, e := s.RefreshAlbumWorks(ctx, false)
	if e != nil || stats.AlbumsRefreshed != 1 {
		t.Fatalf("refresh %+v err %v", stats, e)
	}
	var count int
	if e = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_tracks WHERE track_id=? AND source='auto'`, track).Scan(&count); e != nil || count != 0 {
		t.Fatalf("automatic role overrides=%d err=%v", count, e)
	}
}
