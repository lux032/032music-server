package scanner

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func testStore(t *testing.T) (*storage.Store, storage.Library) {
	t.Helper()
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "scanner.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return store, storage.Library{}
}

func logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func waitJob(t *testing.T, store *storage.Store, jobID int64) storage.ScanJob {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, err := store.LatestScanJob(context.Background())
		if err == nil && job.ID == jobID && (job.Status == "completed" || job.Status == "failed") {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("scan job did not finish")
	return storage.ScanJob{}
}

// S1: a scan that discovers zero files while the library has available files
// must refuse reconciliation instead of wiping the library.
func TestScanRefusesEmptyDiscovery(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	libraryRoot := t.TempDir()
	if err := store.EnsureLibrary(ctx, "Test", libraryRoot); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, libraryRoot)
	if err != nil {
		t.Fatal(err)
	}
	// Seed the library with one available track, as a previous scan would.
	if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "Album/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "T", Album: "A", Artists: []string{"X"}, AlbumArtists: []string{"X"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}

	manager := New(ctx, store, logger(), library, t.TempDir())
	jobID, err := manager.Start(ctx, "full")
	if err != nil {
		t.Fatal(err)
	}
	manager.Wait()
	job := waitJob(t, store, jobID)
	if job.Status != "failed" {
		t.Fatalf("job status = %q, want failed (empty discovery must be refused)", job.Status)
	}
	available, err := store.CountAvailableAudioFiles(ctx, library.ID)
	if err != nil || available != 1 {
		t.Fatalf("available files = %d, err=%v; track must survive the refused scan", available, err)
	}
	stats, err := store.Statistics(ctx)
	if err != nil || stats.Tracks != 1 {
		t.Fatalf("tracks = %d, err=%v; CleanupOrphans must not delete tracks", stats.Tracks, err)
	}
}

// S1: even after files legitimately go missing, the track rows (and their
// client data) must be kept — only the file status changes.
func TestCleanupOrphansKeepsMissingTracks(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	libraryRoot := t.TempDir()
	if err := store.EnsureLibrary(ctx, "Test", libraryRoot); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, libraryRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "Album/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "T", Album: "A", Artists: []string{"X"}, AlbumArtists: []string{"X"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTrackFavorite(ctx, 1, true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkMissing(ctx, library.ID, storage.ScanTimestamp()); err != nil {
		t.Fatal(err)
	}
	if err := store.CleanupOrphans(ctx); err != nil {
		t.Fatal(err)
	}
	stats, err := store.Statistics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Tracks != 1 || stats.FavoriteTracks != 1 {
		t.Fatalf("tracks=%d favorites=%d; missing tracks and their client data must be kept", stats.Tracks, stats.FavoriteTracks)
	}
}
