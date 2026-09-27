package enrichment

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func phase4TestManager(t *testing.T, handler http.Handler) (*Manager, *storage.Store, int64, int64) {
	t.Helper()
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "phase4.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	library, _ := store.LibraryByRoot(ctx, "/music")
	if err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "Album/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Track", Album: "Album", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 1, Raw: map[string][]string{"CATALOGNUMBER": {"SVWC-70658"}}}}); err != nil {
		t.Fatal(err)
	}
	albums, _ := store.ListAlbums(ctx, storage.Filters{Limit: 10})
	work, err := store.CreateWork(ctx, storage.WorkInput{Title: "葬送のフリーレン", Type: "anime", Year: 2023})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.AddWorkAlbum(ctx, work.ID, albums[0].ID, "other"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := New(ctx, store, logger, t.TempDir())
	manager.phaseEndpoints = phase4Endpoints{Bangumi: server.URL + "/bangumi"}
	manager.client = server.Client()
	allowed, _ := url.Parse(server.URL)
	upstream := manager.client.Transport
	if upstream == nil {
		upstream = http.DefaultTransport
	}
	manager.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != allowed.Host {
			t.Errorf("unexpected outbound request to %s", r.URL)
			return nil, fmt.Errorf("test forbids outbound request to %s", r.URL.Host)
		}
		return upstream.RoundTrip(r)
	})
	manager.musicBrainzBase = server.URL + "/mb"
	manager.bangumiInterval = 0
	for _, source := range []string{"bangumi"} {
		setting, _ := store.MetadataSourceSetting(ctx, source)
		setting.Enabled = true
		setting.AutoMatch = true
		setting.CacheDays = 30
		if err = store.SaveMetadataSourceSetting(ctx, setting); err != nil {
			t.Fatal(err)
		}
	}
	return manager, store, albums[0].ID, work.ID
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func waitRun(t *testing.T, store *storage.Store, id int64) storage.EnrichmentRun {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		run, err := store.EnrichmentRun(context.Background(), id)
		if err == nil && (run.Status == "completed" || run.Status == "failed") {
			return run
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("run did not finish")
	return storage.EnrichmentRun{}
}

func TestBangumiScoreRequiresStrongEvidence(t *testing.T) {
	work := storage.WorkEnrichmentTarget{Title: "葬送のフリーレン", Type: "anime", Year: 2023}
	score, _ := scoreBangumi(work, "葬送のフリーレン", "Frieren", "2023-09-29", 2)
	if score < 90 {
		t.Fatalf("score=%d", score)
	}
	bad, _ := scoreBangumi(work, "同名ではない", "", "1999-01-01", 2)
	if bad >= 90 {
		t.Fatalf("bad score=%d", bad)
	}
}

func TestStartRunRejectsRetiredAlbumScope(t *testing.T) {
	manager, _, albumID, _ := phase4TestManager(t, http.NotFoundHandler())
	if _, err := manager.StartRun(context.Background(), RunRequest{Scope: "album", TargetID: albumID}); err == nil {
		t.Fatal("album scope (VGMdb) should be rejected")
	}
}

func TestStartRunAbortsAfterConsecutiveFailures(t *testing.T) {
	var requests atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "down", http.StatusBadGateway)
	})
	manager, store, albumID, _ := phase4TestManager(t, handler)
	for i := 0; i < maxConsecutiveFailures+5; i++ {
		created, err := store.CreateWork(context.Background(), storage.WorkInput{Title: fmt.Sprintf("Work %d", i), Type: "anime"})
		if err == nil {
			err = store.AddWorkAlbum(context.Background(), created.ID, albumID, "other")
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	run, err := manager.StartRun(context.Background(), RunRequest{Scope: "all"})
	if err != nil {
		t.Fatal(err)
	}
	finished := waitRun(t, store, run.ID)
	if finished.Status != "failed" || finished.Failed != maxConsecutiveFailures || finished.Total != maxConsecutiveFailures+6 {
		t.Fatalf("run=%#v", finished)
	}
	if got := int(requests.Load()); got != maxConsecutiveFailures {
		t.Fatalf("requests=%d, want %d", got, maxConsecutiveFailures)
	}
}

func TestStartRunBangumiAutoConfirm(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":123,"type":2,"name":"葬送のフリーレン","name_cn":"葬送的芙莉莲","date":"2023-09-29","images":{"large":"https://example/poster.jpg"}}]}`)
	})
	manager, store, _, workID := phase4TestManager(t, handler)
	run, err := manager.StartRun(context.Background(), RunRequest{Scope: "work", TargetID: workID})
	if err != nil {
		t.Fatal(err)
	}
	finished := waitRun(t, store, run.ID)
	if finished.Succeeded != 1 {
		t.Fatalf("run=%#v", finished)
	}
	work, err := store.WorkByID(context.Background(), workID)
	if err != nil || work.Year != 2023 || work.PosterURL == "" {
		t.Fatalf("work=%#v err=%v", work, err)
	}
}
