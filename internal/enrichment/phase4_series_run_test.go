package enrichment

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

// seriesRunFixture builds a manager plus one imported album so works can be
// referenced (the work alignment stage only processes referenced works).
func seriesRunFixture(t *testing.T, server *httptest.Server) (*Manager, *storage.Store, int64) {
	t.Helper()
	manager, store := newSeriesManager(t, server)
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Music", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "Album/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Track", Album: "Album", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	albums, err := store.ListAlbums(ctx, storage.Filters{Limit: 1})
	if err != nil || len(albums) == 0 {
		t.Fatalf("albums=%v err=%v", albums, err)
	}
	return manager, store, albums[0].ID
}

// bindRunWork creates a work bound to a Bangumi subject whose cached profile
// reports the given platform; the work starts untyped (unlocked, D38) so the
// alignment stage can correct it. referenced links the work to the album.
func bindRunWork(t *testing.T, store *storage.Store, albumID int64, title string, subjectID int64, platform string, referenced bool) int64 {
	t.Helper()
	ctx := context.Background()
	work, err := store.CreateWork(ctx, storage.WorkInput{Title: title})
	if err != nil {
		t.Fatal(err)
	}
	if referenced {
		if err = store.AddWorkAlbum(ctx, work.ID, albumID, "other"); err != nil {
			t.Fatal(err)
		}
	}
	raw := fmt.Sprintf(`{"id":%d,"type":2,"platform":%q}`, subjectID, platform)
	profile := storage.ExternalWorkProfile{Source: "bangumi", ExternalID: fmt.Sprintf("%d", subjectID), Title: title, Type: "anime", Raw: json.RawMessage(raw), FetchedAt: "2026-01-01T00:00:00Z"}
	if err = store.UpsertExternalWorkProfile(ctx, work.ID, profile); err != nil {
		t.Fatal(err)
	}
	return work.ID
}

// The works scope runs the work alignment stage and then the series stage.
func TestPhase4WorksScopeRunsSeriesStage(t *testing.T) {
	server := syntheticSeriesGraphServer(t, chainEdges([][]int64{{1, 2}}), nil)
	defer server.Close()
	manager, store, albumID := seriesRunFixture(t, server)
	ctx := context.Background()
	bindRunWork(t, store, albumID, "Run Season 1", 1, "TV", true)
	bindRunWork(t, store, albumID, "Run Season 2", 2, "TV", true)
	run, err := store.CreateEnrichmentRun(ctx, "works", 0, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	manager.executePhase4Run(ctx, run.ID, RunRequest{Scope: "works"})
	finished, err := store.EnrichmentRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != "completed" {
		t.Fatalf("run=%+v", finished)
	}
	if seriesCount(t, store) != 1 {
		t.Fatalf("series=%d", seriesCount(t, store))
	}
}

// Work alignment items succeed and only the series stage fails: the run is
// completed, with the series failure visible in the counters and message.
func TestPhase4SeriesFailureKeepsRunCompleted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream down", http.StatusBadGateway)
	}))
	defer server.Close()
	manager, store, albumID := seriesRunFixture(t, server)
	ctx := context.Background()
	// The 剧场版 platform corrects each untyped work to movie without any HTTP.
	for i := 0; i < maxConsecutiveFailures; i++ {
		bindRunWork(t, store, albumID, fmt.Sprintf("Movie %d", i), int64(10+i), "剧场版", true)
	}
	run, err := store.CreateEnrichmentRun(ctx, "works", 0, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	manager.executePhase4Run(ctx, run.ID, RunRequest{Scope: "works"})
	finished, err := store.EnrichmentRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != "completed" {
		t.Fatalf("status=%s, want completed (%+v)", finished.Status, finished)
	}
	if finished.Succeeded != maxConsecutiveFailures || finished.Failed != 1 {
		t.Fatalf("succeeded=%d failed=%d, want %d/1", finished.Succeeded, finished.Failed, maxConsecutiveFailures)
	}
	if !strings.Contains(finished.ErrorMessage, "consecutive failures") {
		t.Fatalf("series failure not visible: %q", finished.ErrorMessage)
	}
	if seriesCount(t, store) != 0 {
		t.Fatalf("series=%d", seriesCount(t, store))
	}
}

// No work alignment items at all and the series stage fails: every unit of
// the run failed, so the run itself is failed.
func TestPhase4SeriesFailureAloneMarksRunFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream down", http.StatusBadGateway)
	}))
	defer server.Close()
	manager, store, albumID := seriesRunFixture(t, server)
	ctx := context.Background()
	// Unreferenced works are skipped by the alignment stage but are still
	// series seeds.
	for i := 0; i < maxConsecutiveFailures; i++ {
		bindRunWork(t, store, albumID, fmt.Sprintf("Unlinked %d", i), int64(20+i), "TV", false)
	}
	run, err := store.CreateEnrichmentRun(ctx, "works", 0, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	manager.executePhase4Run(ctx, run.ID, RunRequest{Scope: "works"})
	finished, err := store.EnrichmentRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != "failed" || finished.Failed != 1 {
		t.Fatalf("run=%+v, want failed with the series stage as the only item", finished)
	}
	if !strings.Contains(finished.ErrorMessage, "consecutive failures") {
		t.Fatalf("message=%q", finished.ErrorMessage)
	}
}
