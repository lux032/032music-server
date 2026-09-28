package enrichment

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

// M5: stages are collected in order. Music searches (type 3) happen before the
// work search (type 2), which is only collected after the track stage.
func TestPhase4StagesRunAlbumThenTrackThenWork(t *testing.T) {
	var mu sync.Mutex
	var kinds []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		kinds = append(kinds, string(body))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[]}`)
	})
	manager, store, _, _ := phase4TestManager(t, handler)
	ctx := context.Background()
	if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: mustLibrary(t, store), RelativePath: "Anime/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Opening", Album: "Anime Album", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1, TrackType: "tv_size"}}); err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateEnrichmentRun(ctx, "all", 0, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	manager.executePhase4Run(ctx, run.ID, RunRequest{Scope: "all", Force: true})
	finished, _ := store.EnrichmentRun(ctx, run.ID)
	if finished.Status != "completed" || finished.Total < 3 {
		t.Fatalf("run=%+v", finished)
	}
	mu.Lock()
	defer mu.Unlock()
	music, work := -1, -1
	for i, body := range kinds {
		if contains(body, "[3]") && music < 0 {
			music = i
		}
		if contains(body, "[2]") && work < 0 {
			work = i
		}
	}
	if music < 0 || work < music {
		t.Fatalf("order=%v", kinds)
	}
}

func TestPhase4AlbumScopeIncludesItsTracks(t *testing.T) {
	var searches atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "search") {
			searches.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[]}`)
	})
	manager, store, albumID, _ := phase4TestManager(t, handler)
	ctx := context.Background()
	// The shared fixture links the album by hand, which correctly keeps it out of
	// the album stage. Drop that link so this run has an album search to make.
	if err := store.RemoveWorkAlbum(ctx, workOf(t, store, albumID), albumID); err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateEnrichmentRun(ctx, "album", albumID, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	manager.executePhase4Run(ctx, run.ID, RunRequest{Scope: "album", TargetID: albumID, Force: true})
	finished, err := store.EnrichmentRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("run=%+v searches=%d", finished, searches.Load())
	// The album search plus the track search. The linked work is already
	// confirmed, so the work stage makes no third request.
	if searches.Load() < 2 {
		t.Fatalf("album scope made %d searches, want album and track", searches.Load())
	}
	if finished.Status != "completed" {
		t.Fatalf("run=%+v", finished)
	}
}

func TestPhase4CancelStopsBeforeLaterStages(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The POST body must be consumed, otherwise the server never starts the
		// background read that turns a client-side cancel into r.Context().Done().
		_, _ = io.Copy(io.Discard, r.Body)
		once.Do(func() { close(started) })
		<-r.Context().Done()
	})
	manager, store, _, _ := phase4TestManager(t, handler)
	ctx, cancel := context.WithCancel(context.Background())
	run, err := store.CreateEnrichmentRun(ctx, "tracks", 0, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		manager.executePhase4Run(ctx, run.ID, RunRequest{Scope: "tracks", Force: true})
		close(done)
	}()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop after cancellation")
	}
	finished, _ := store.EnrichmentRun(context.Background(), run.ID)
	if finished.Status != "cancelled" {
		t.Fatalf("status=%s message=%s", finished.Status, finished.ErrorMessage)
	}
}

func TestPhase4RequestFailureWritesNothing(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	})
	manager, store, _, _ := phase4TestManager(t, handler)
	ctx := context.Background()
	run, err := store.CreateEnrichmentRun(ctx, "tracks", 0, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	manager.executePhase4Run(ctx, run.ID, RunRequest{Scope: "tracks"})
	if n := phase4Count(t, store, `SELECT COUNT(*) FROM track_enrichment_misses`); n != 0 {
		t.Fatalf("misses=%d", n)
	}
	if n := phase4Count(t, store, `SELECT COUNT(*) FROM track_subject_candidates`); n != 0 {
		t.Fatalf("candidates=%d", n)
	}
}

func TestPhase4ForceMemoFetchesEachKeyOnce(t *testing.T) {
	var searches atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "search") {
			searches.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":1,"type":3,"name":"Opening"}]}`)
	})
	manager, store, _, _ := phase4TestManager(t, handler)
	ctx := context.Background()
	lib := mustLibrary(t, store)
	for i := 0; i < 2; i++ {
		if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib, RelativePath: "Dupe/" + string(rune('a'+i)) + ".flac", FileSize: 1, ModifiedAtNS: int64(i + 1), Metadata: metadata.AudioMetadata{Title: "Opening", Album: "Dupe " + string(rune('A'+i)), Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1, TrackType: "tv_size"}}); err != nil {
			t.Fatal(err)
		}
	}
	run, err := store.CreateEnrichmentRun(ctx, "tracks", 0, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	manager.executePhase4Run(ctx, run.ID, RunRequest{Scope: "tracks", Force: true})
	// One search per distinct keyword. Both tracks share the title, so the run
	// memo must collapse the forced refreshes onto a single request. The fixture
	// album contributes its own search, which is a different keyword.
	if searches.Load() != 2 {
		t.Fatalf("forced searches=%d, want 2", searches.Load())
	}
}

func phase4Count(t *testing.T, store *storage.Store, query string, args ...any) int {
	t.Helper()
	db := readOnlyDB(t, store)
	var n int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func workOf(t *testing.T, store *storage.Store, albumID int64) int64 {
	t.Helper()
	works, err := store.WorksForAlbum(context.Background(), albumID)
	if err != nil || len(works) == 0 {
		t.Fatalf("works=%+v err=%v", works, err)
	}
	return works[0].ID
}

func mustLibrary(t *testing.T, store *storage.Store) int64 {
	t.Helper()
	library, err := store.LibraryByRoot(context.Background(), "/music")
	if err != nil {
		t.Fatal(err)
	}
	return library.ID
}

func contains(s, part string) bool {
	return indexOf(s, part) >= 0
}

func indexOf(s, part string) int {
	for i := 0; i+len(part) <= len(s); i++ {
		if s[i:i+len(part)] == part {
			return i
		}
	}
	return -1
}
