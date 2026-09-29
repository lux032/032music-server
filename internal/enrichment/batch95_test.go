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

func TestBatch95AlbumWithoutTieupsBecomesMiss(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "empty-tieups.db"))
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
	if err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: "A/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "No Tie", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	setting, _ := store.MetadataSourceSetting(ctx, "bangumi")
	setting.Enabled = true
	if err = store.SaveMetadataSourceSetting(ctx, setting); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/search/subjects":
			io.WriteString(w, `{"data":[{"id":3,"type":3,"name":"No Tie"}]}`)
		case "/v0/subjects/3":
			io.WriteString(w, `{"id":3,"type":3,"name":"No Tie","infobox":[{"key":"艺术家","value":"Singer"}]}`)
		case "/v0/subjects/3/persons", "/v0/subjects/3/subjects":
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
	if err != nil || outcome != "skipped" {
		t.Fatalf("outcome=%q err=%v", outcome, err)
	}
	candidates, err := store.AlbumSubjectCandidates(ctx, targets[0].ID)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("candidates=%v err=%v", candidates, err)
	}
}
