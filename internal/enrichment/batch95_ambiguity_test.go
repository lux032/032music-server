package enrichment

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func TestBatch95FilteredEmptyTieupStillPreventsAutoAccept(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "ambiguity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.EnsureLibrary(ctx, "Music", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, _ := store.LibraryByRoot(ctx, "/music")
	if err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: "A/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Ambiguous", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1, Raw: map[string][]string{"DATE": {"2024-01-01"}}}}); err != nil {
		t.Fatal(err)
	}
	setting, _ := store.MetadataSourceSetting(ctx, "bangumi")
	setting.Enabled, setting.AutoMatch = true, true
	if err = store.SaveMetadataSourceSetting(ctx, setting); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/search/subjects":
			io.WriteString(w, `{"data":[{"id":101,"type":3,"name":"Ambiguous"},{"id":102,"type":3,"name":"Ambiguous"}]}`)
		case "/v0/subjects/101", "/v0/subjects/102":
			io.WriteString(w, `{"id":101,"type":3,"name":"Ambiguous","date":"2024-01-01","infobox":[{"key":"艺术家","value":"Singer"}]}`)
		case "/v0/subjects/101/persons", "/v0/subjects/102/persons":
			io.WriteString(w, `[]`)
		case "/v0/subjects/101/subjects":
			io.WriteString(w, `[{"id":201,"type":2,"name":"Anime","platform":"TV","relation":"片头曲"}]`)
		case "/v0/subjects/201/subjects":
			io.WriteString(w, `[{"id":101,"relation":"片头曲"}]`)
		case "/v0/subjects/102/subjects":
			io.WriteString(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	manager := New(ctx, store, slog.New(slog.NewTextHandler(io.Discard, nil)), t.TempDir())
	manager.phaseEndpoints.BangumiAPI = server.URL
	manager.client = server.Client()
	manager.bangumiInterval = 0
	targets, err := store.AlbumsForBangumiTieup(ctx, true, 0)
	if err != nil || len(targets) != 1 {
		t.Fatalf("targets=%v err=%v", targets, err)
	}
	outcome, err := manager.enrichBangumiAlbum(ctx, 0, targets[0], true)
	if err != nil || outcome != "review" {
		t.Fatalf("outcome=%q err=%v", outcome, err)
	}
	candidates, err := store.AlbumSubjectCandidates(ctx, targets[0].ID)
	if err != nil || len(candidates) != 1 || candidates[0].ExternalID != "101" || candidates[0].Status != "candidate" {
		t.Fatalf("candidates=%+v err=%v", candidates, err)
	}
}
