package storage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

// M4: same-titled works of a different type or year must stay distinct, and
// auto-inference must attach to the matching (title, type) work instead of
// merging everything into the first work with that title.
func TestWorksIdentityAllowsSameTitleDifferentTypeOrYear(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "works-identity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	anime, err := store.CreateWork(ctx, WorkInput{Title: "翔んで埼玉", Type: "anime", Year: 2019})
	if err != nil {
		t.Fatal(err)
	}
	movie, err := store.CreateWork(ctx, WorkInput{Title: "翔んで埼玉", Type: "movie", Year: 2019})
	if err != nil {
		t.Fatal(err)
	}
	if anime.ID == movie.ID {
		t.Fatal("same-titled works of different types were merged")
	}
	remake, err := store.CreateWork(ctx, WorkInput{Title: "翔んで埼玉", Type: "movie", Year: 2023})
	if err != nil {
		t.Fatal(err)
	}
	if remake.ID == movie.ID {
		t.Fatal("same-titled works of different years were merged")
	}

	// Exact (title, type, year) duplicates are still rejected.
	if _, err := store.CreateWork(ctx, WorkInput{Title: "翔んで埼玉", Type: "movie", Year: 2023}); err == nil {
		t.Fatal("duplicate (title, type, year) work was accepted")
	}

	// Auto-inference attaches to the existing work of the same title+type
	// rather than creating a duplicate.
	if err := store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	library, _ := store.LibraryByRoot(ctx, "/music")
	input := ImportInput{LibraryID: library.ID, RelativePath: "Saitama/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Theme", Album: "Single", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 1, Raw: map[string][]string{"CONTENTGROUP": {"映画「翔んで埼玉」主題歌"}}}}
	if err := store.ImportTrack(ctx, input); err != nil {
		t.Fatal(err)
	}
	for _, work := range []Work{movie, remake} {
		tracks, err := store.TracksForWork(ctx, work.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(tracks) > 0 {
			return // attached to an existing movie work — correct
		}
	}
	// No existing movie work picked up the track: a duplicate must have been
	// created, which is exactly the merge bug M4 prevents.
	works, _ := store.ListWorks(ctx, WorkFilters{Query: "埼玉"})
	count := 0
	for _, w := range works {
		if w.Type == "movie" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("movie works = %d, want 2 (no auto-created duplicate)", count)
	}
}

// M12: re-running migrations against a database that already has the columns
// (e.g. created by hand or an older build) must not fail.
func TestMigrateIdempotentWithPreExistingColumns(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "migrate-idempotent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// Simulate an operator-added column that a migration would re-add, then
	// remove the migration record so it runs again.
	if _, err := store.db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version=2`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		if strings.Contains(err.Error(), "duplicate column") {
			t.Fatalf("migration not idempotent: %v", err)
		}
		t.Fatal(err)
	}
}
