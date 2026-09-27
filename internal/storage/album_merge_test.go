package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

type albumMergeFixture struct {
	ctx     context.Context
	store   *Store
	library int64
}

func newAlbumMergeFixture(t *testing.T) albumMergeFixture {
	t.Helper()
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "albums.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.EnsureLibrary(ctx, "Music", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	return albumMergeFixture{ctx: ctx, store: store, library: library.ID}
}

// importFile (re)imports one file; bump modified to simulate a rescan.
func (f albumMergeFixture) importFile(t *testing.T, path, album, title string, disc, modified int) {
	t.Helper()
	input := ImportInput{LibraryID: f.library, RelativePath: path, FileSize: 100, ModifiedAtNS: int64(modified), Metadata: metadata.AudioMetadata{Title: title, Album: album, Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: disc, TrackNumber: 1}}
	if err := f.store.ImportTrack(f.ctx, input); err != nil {
		t.Fatal(err)
	}
}

func (f albumMergeFixture) albumID(t *testing.T, title string) int64 {
	t.Helper()
	var id int64
	if err := f.store.db.QueryRowContext(f.ctx, `SELECT id FROM albums WHERE title=?`, title).Scan(&id); err != nil {
		t.Fatalf("album %q: %v", title, err)
	}
	return id
}

func (f albumMergeFixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.store.db.QueryRowContext(f.ctx, query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMergeAlbumsSurvivesRescan(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "Main/01.flac", "Main", "A", 1, 1)
	f.importFile(t, "CD2/01.flac", "Main CD2", "B", 2, 1)
	f.importFile(t, "CD3/01.flac", "Main CD3", "C", 3, 1)
	main, cd2, cd3 := f.albumID(t, "Main"), f.albumID(t, "Main CD2"), f.albumID(t, "Main CD3")
	if err := f.store.SetAlbumFavorite(f.ctx, cd3, true); err != nil {
		t.Fatal(err)
	}

	merged, err := f.store.MergeAlbums(f.ctx, main, []int64{cd2, cd3, cd2})
	if err != nil || merged != 2 {
		t.Fatalf("MergeAlbums = %d, %v", merged, err)
	}
	if got := f.count(t, `SELECT COUNT(*) FROM albums`); got != 1 {
		t.Fatalf("albums after merge = %d, want 1", got)
	}
	if got := f.count(t, `SELECT COUNT(*) FROM tracks WHERE album_id=?`, main); got != 3 {
		t.Fatalf("tracks in main = %d, want 3", got)
	}
	if got := f.count(t, `SELECT is_favorite FROM albums WHERE id=?`, main); got != 1 {
		t.Fatal("source favorite should carry over to the main album")
	}
	if got := f.count(t, `SELECT disc_count FROM albums WHERE id=?`, main); got != 3 {
		t.Fatalf("disc_count = %d, want 3", got)
	}

	// Full rescan of every file: no album may be resurrected.
	f.importFile(t, "Main/01.flac", "Main", "A", 1, 2)
	f.importFile(t, "CD2/01.flac", "Main CD2", "B", 2, 2)
	f.importFile(t, "CD3/01.flac", "Main CD3", "C", 3, 2)
	// A new file in a merged folder joins the main album too.
	f.importFile(t, "CD2/02.flac", "Main CD2", "B2", 2, 2)
	if err = f.store.CleanupOrphans(f.ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.count(t, `SELECT COUNT(*) FROM albums`); got != 1 {
		t.Fatalf("albums after rescan = %d, want 1", got)
	}
	if got := f.count(t, `SELECT COUNT(*) FROM tracks WHERE album_id=?`, main); got != 4 {
		t.Fatalf("tracks in main after rescan = %d, want 4", got)
	}
}

func TestMergeAlbumsChainsEarlierMerges(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "A", "a", 1, 1)
	f.importFile(t, "B/01.flac", "B", "b", 1, 1)
	f.importFile(t, "C/01.flac", "C", "c", 1, 1)
	a, b, c := f.albumID(t, "A"), f.albumID(t, "B"), f.albumID(t, "C")
	if _, err := f.store.MergeAlbums(f.ctx, b, []int64{a}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.MergeAlbums(f.ctx, c, []int64{b}); err != nil {
		t.Fatal(err)
	}
	f.importFile(t, "A/01.flac", "A", "a", 1, 2)
	f.importFile(t, "B/01.flac", "B", "b", 1, 2)
	if got := f.count(t, `SELECT COUNT(*) FROM albums`); got != 1 {
		t.Fatalf("albums = %d, want 1", got)
	}
	if got := f.count(t, `SELECT COUNT(*) FROM tracks WHERE album_id=?`, c); got != 3 {
		t.Fatalf("tracks in C = %d, want 3", got)
	}
}

func TestMergeAlbumsRejectsInvalidSelection(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "A", "a", 1, 1)
	a := f.albumID(t, "A")
	for name, sources := range map[string][]int64{"self": {a}, "missing": {a + 100}, "empty": nil} {
		if _, err := f.store.MergeAlbums(f.ctx, a, sources); !errors.Is(err, ErrAlbumSelection) {
			t.Fatalf("%s: err = %v, want ErrAlbumSelection", name, err)
		}
	}
	if got := f.count(t, `SELECT COUNT(*) FROM tracks WHERE album_id=?`, a); got != 1 {
		t.Fatal("failed merge must not change the library")
	}
}

func TestDeleteAlbumsSurvivesRescan(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "Keep/01.flac", "Keep", "k", 1, 1)
	f.importFile(t, "Gone/01.flac", "Gone", "g", 1, 1)
	f.importFile(t, "Part/01.flac", "Part", "p", 1, 1)
	keep, gone, part := f.albumID(t, "Keep"), f.albumID(t, "Gone"), f.albumID(t, "Part")
	// Part is merged into Gone; deleting Gone must drop Part's files as well.
	if _, err := f.store.MergeAlbums(f.ctx, gone, []int64{part}); err != nil {
		t.Fatal(err)
	}
	deleted, err := f.store.DeleteAlbums(f.ctx, []int64{gone})
	if err != nil || deleted != 1 {
		t.Fatalf("DeleteAlbums = %d, %v", deleted, err)
	}
	if got := f.count(t, `SELECT COUNT(*) FROM tracks`); got != 1 {
		t.Fatalf("tracks = %d, want 1", got)
	}
	if got := f.count(t, `SELECT COUNT(*) FROM audio_files`); got != 1 {
		t.Fatalf("audio_files = %d, want 1", got)
	}
	f.importFile(t, "Gone/01.flac", "Gone", "g", 1, 2)
	f.importFile(t, "Part/01.flac", "Part", "p", 1, 2)
	f.importFile(t, "Keep/01.flac", "Keep", "k", 1, 2)
	if got := f.count(t, `SELECT COUNT(*) FROM albums`); got != 1 {
		t.Fatalf("albums after rescan = %d, want 1", got)
	}
	if _, err = f.store.AlbumByID(f.ctx, keep); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.AlbumByID(f.ctx, gone); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted album still readable: %v", err)
	}
}

func TestDeletedMergeTargetDoesNotRedirectAfterOrphanCleanup(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "A", "a", 1, 1)
	f.importFile(t, "B/01.flac", "B", "b", 1, 1)
	a, b := f.albumID(t, "A"), f.albumID(t, "B")
	if _, err := f.store.MergeAlbums(f.ctx, a, []int64{b}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.ExecContext(f.ctx, `DELETE FROM audio_files; DELETE FROM albums WHERE id=?`, a); err != nil {
		t.Fatal(err)
	}
	if got := f.count(t, `SELECT COUNT(*) FROM album_key_rules`); got != 0 {
		t.Fatalf("rules = %d, want 0 once the target is gone", got)
	}
	f.importFile(t, "B/01.flac", "B", "b", 1, 2)
	f.albumID(t, "B")
}
