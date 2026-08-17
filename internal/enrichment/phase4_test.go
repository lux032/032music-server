package enrichment

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := New(store, logger, t.TempDir())
	manager.phaseEndpoints = phase4Endpoints{VGMdbSearch: server.URL + "/vgmdb/search/%s", VGMdbAlbum: server.URL + "/vgmdb/album/%s", Bangumi: server.URL + "/bangumi", MusicBrainz: server.URL + "/mb"}
	manager.client = server.Client()
	for _, source := range []string{"vgmdb", "bangumi"} {
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

func TestNormalizeCatalogNumber(t *testing.T) {
	if got := NormalizeCatalogNumber(" svwc  - 70658 "); got != "SVWC70658" {
		t.Fatalf("got %q", got)
	}
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

func TestStartRunVGMdbExactMatchAndCache(t *testing.T) {
	requests := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/vgmdb/search"):
			io.WriteString(w, `{"results":{"albums":[{"link":"album/999","catalog":"SVWC-70658","name":"Remote"}]}}`)
		case strings.HasPrefix(r.URL.Path, "/vgmdb/album"):
			io.WriteString(w, `{"link":"album/999","catalog":"SVWC-70658","name":"Remote","release_date":"2024-01-02","organizations":[{"names":{"en":"Label"}}],"discs":[{"tracks":[{"names":{"en":"Track"},"credits":[{"role":"composer","name":"Remote Composer"}]}]}]}`)
		default:
			http.NotFound(w, r)
		}
	})
	manager, store, albumID, _ := phase4TestManager(t, handler)
	run, err := manager.StartRun(context.Background(), RunRequest{Scope: "album", TargetID: albumID})
	if err != nil {
		t.Fatal(err)
	}
	finished := waitRun(t, store, run.ID)
	if finished.Succeeded != 1 || finished.Failed != 0 {
		t.Fatalf("run=%#v", finished)
	}
	first := requests
	run, err = manager.StartRun(context.Background(), RunRequest{Scope: "album", TargetID: albumID, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = waitRun(t, store, run.ID)
	if requests <= first {
		t.Fatalf("force did not request again: %d", requests)
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
