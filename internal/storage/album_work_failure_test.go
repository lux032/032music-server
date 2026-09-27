package storage

import (
	"github.com/lux032/032music-server/internal/metadata"
	"testing"
)

func TestRefreshAlbumWorksCountsFailedTagAlbumAndContinues(t *testing.T) {
	s, ctx, failedAlbum := albumWorkFixture(t, "GOLD", "Song")
	lib, _ := s.LibraryByRoot(ctx, "/music")
	if err := s.ImportTrack(ctx, ImportInput{LibraryID: lib.ID, RelativePath: "Album/01.flac", FileSize: 1, ModifiedAtNS: 2, Metadata: metadata.AudioMetadata{Title: "Song", Album: "GOLD", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 1, Raw: map[string][]string{"WORKTITLE": {"TVアニメ「Y」オープニングテーマ"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ImportTrack(ctx, ImportInput{LibraryID: lib.ID, RelativePath: "Next/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Z Original Soundtrack", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER fail_auto_work BEFORE INSERT ON work_tracks WHEN NEW.source='auto' BEGIN SELECT RAISE(FAIL,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	stats, err := s.RefreshAlbumWorks(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if stats.AlbumsFailed != 1 || stats.AlbumsRefreshed != 1 || stats.WorksCreated != 1 {
		t.Fatalf("refresh stats %+v", stats)
	}
	var fingerprint string
	if err = s.db.QueryRowContext(ctx, `SELECT COALESCE(work_fingerprint,'') FROM albums WHERE id=?`, failedAlbum).Scan(&fingerprint); err != nil || fingerprint != "" {
		t.Fatalf("failed album fingerprint=%q err=%v", fingerprint, err)
	}
}
