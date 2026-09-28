package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/lux032/032music-server/internal/config"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

var httpTestDBPath = map[*storage.Store]string{}

func setupTestApp(t *testing.T) (*App, *storage.Store, string) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "http-sync.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	httpTestDBPath[store] = dbPath
	t.Cleanup(func() {
		delete(httpTestDBPath, store)
		store.Close()
	})

	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	token := "valid-test-token-at-least-24-chars"
	cfg := config.Config{
		APIToken:      token,
		MediaToken:    token,
		CookieSecure:  false,
		DataDirectory: t.TempDir(),
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app, err := NewApp(cfg, store, nil, nil, logger, "1.0.0-test")
	if err != nil {
		t.Fatal(err)
	}

	return app, store, token
}

func TestSyncAlbumsEndpoint(t *testing.T) {
	ctx := context.Background()
	app, store, token := setupTestApp(t)
	if err := store.EnsureLibrary(ctx, "Default", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "Artist/Album/01.flac", FileSize: 1024, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Track", Album: "Album", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sync/albums?limit=1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response storage.SyncAlbumsResult
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 1 || response.Total != 1 || response.Items[0].Title != "Album" || response.Items[0].Artist != "Artist" {
		t.Fatalf("response = %#v", response)
	}
}

func TestSyncTracksEndpointAuth(t *testing.T) {
	app, _, _ := setupTestApp(t)
	handler := app.Handler()

	// 1. Without auth header -> 401
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sync/tracks", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", rec.Code)
	}

	// 2. With invalid token -> 401
	req = httptest.NewRequest(http.MethodGet, "/api/v1/sync/tracks", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", rec.Code)
	}
}

func TestSyncTracksEndpointCursorAndAlias(t *testing.T) {
	ctx := context.Background()
	app, store, token := setupTestApp(t)
	handler := app.Handler()

	// Insert test data
	if err := store.EnsureLibrary(ctx, "Default", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}

	// Insert album and tracks via DB
	for i := 1; i <= 3; i++ {
		err := store.ImportTrack(ctx, storage.ImportInput{
			LibraryID:    lib.ID,
			RelativePath: "Artist/Album/0" + strconv.Itoa(i) + ".flac",
			FileSize:     1024 * 1024,
			ModifiedAtNS: 1000,
			Metadata: metadata.AudioMetadata{
				Title:          "Track Title " + strconv.Itoa(i),
				Album:          "Album Title",
				Artists:        []string{"Singer One"},
				AlbumArtists:   []string{"Singer One"},
				Year:           2024,
				TrackNumber:    i,
				DiscNumber:     1,
				DurationMillis: 180000,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// 1. Test /api/v1/sync/tracks with cursor pagination
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sync/tracks?limit=2", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Items      []storage.SyncTrack `json:"items"`
		NextCursor string              `json:"nextCursor"`
		HasMore    bool                `json:"hasMore"`
		Total      int64               `json:"total"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode json failed: %v", err)
	}

	if len(resp.Items) != 2 || !resp.HasMore || resp.Total != 3 || resp.NextCursor == "" {
		t.Fatalf("page 1 unexpected: len=%d, hasMore=%v, total=%d, nextCursor=%q", len(resp.Items), resp.HasMore, resp.Total, resp.NextCursor)
	}

	if resp.Items[0].Title != "Track Title 1" || resp.Items[0].Artist != "Singer One" || resp.Items[0].Album != "Album Title" {
		t.Fatalf("track 0 mismatch: %#v", resp.Items[0])
	}
	if resp.Items[0].StreamURL != "/api/v1/tracks/"+strconv.FormatInt(resp.Items[0].ID, 10)+"/stream" {
		t.Fatalf("streamUrl mismatch: %q", resp.Items[0].StreamURL)
	}

	// Page 2
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/sync/tracks?cursor="+resp.NextCursor+"&limit=2", nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	var resp2 struct {
		Items      []storage.SyncTrack `json:"items"`
		NextCursor string              `json:"nextCursor"`
		HasMore    bool                `json:"hasMore"`
		Total      int64               `json:"total"`
	}
	if err := json.NewDecoder(rec2.Body).Decode(&resp2); err != nil {
		t.Fatalf("decode json failed: %v", err)
	}

	if len(resp2.Items) != 1 || resp2.HasMore || resp2.NextCursor != "" || resp2.Total != 3 {
		t.Fatalf("page 2 unexpected: len=%d, hasMore=%v, total=%d, nextCursor=%q", len(resp2.Items), resp2.HasMore, resp2.Total, resp2.NextCursor)
	}
	if resp2.Items[0].Title != "Track Title 3" {
		t.Fatalf("track 2 mismatch: %#v", resp2.Items[0])
	}

	// 2. Test route alias /api/v1/tracks/sync
	reqAlias := httptest.NewRequest(http.MethodGet, "/api/v1/tracks/sync?limit=10", nil)
	reqAlias.Header.Set("Authorization", "Bearer "+token)
	recAlias := httptest.NewRecorder()
	handler.ServeHTTP(recAlias, reqAlias)

	if recAlias.Code != http.StatusOK {
		t.Fatalf("alias route status = %d", recAlias.Code)
	}
	var aliasResp struct {
		Items []storage.SyncTrack `json:"items"`
		Total int64               `json:"total"`
	}
	if err := json.NewDecoder(recAlias.Body).Decode(&aliasResp); err != nil {
		t.Fatalf("decode alias json failed: %v", err)
	}
	if len(aliasResp.Items) != 3 || aliasResp.Total != 3 {
		t.Fatalf("alias returned len=%d, total=%d", len(aliasResp.Items), aliasResp.Total)
	}

	// 3. Test capabilities feature flag
	reqCap := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
	reqCap.Header.Set("Authorization", "Bearer "+token)
	recCap := httptest.NewRecorder()
	handler.ServeHTTP(recCap, reqCap)

	if recCap.Code != http.StatusOK {
		t.Fatalf("capabilities status = %d", recCap.Code)
	}
	var capResp struct {
		Features map[string]bool `json:"features"`
	}
	if err := json.NewDecoder(recCap.Body).Decode(&capResp); err != nil {
		t.Fatal(err)
	}
	if !capResp.Features["syncAlbums"] || !capResp.Features["syncTracks"] {
		t.Fatalf("expected syncAlbums=true and syncTracks=true in capabilities, got %#v", capResp.Features)
	}
}
