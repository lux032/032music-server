package httpapi

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func TestVersionedAssetServedImmutable(t *testing.T) {
	app, _, _ := setupTestApp(t)
	handler := app.Handler()

	req := httptest.NewRequest(http.MethodGet, app.assets.assetURL("tokens.css"), nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control = %q, want immutable", got)
	}
	if rec.Header().Get("ETag") == "" {
		t.Fatal("missing ETag")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Fatalf("Content-Type = %q, want text/css", ct)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("empty body")
	}
	if strings.Contains(rec.Body.String(), "--accent") == false {
		t.Fatal("body does not look like tokens.css")
	}
}

func TestVersionedAssetServesPrecompressedGzip(t *testing.T) {
	app, _, _ := setupTestApp(t)
	handler := app.Handler()

	req := httptest.NewRequest(http.MethodGet, app.assets.assetURL("player-bar.js"), nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if vary := rec.Header().Get("Vary"); !strings.Contains(vary, "Accept-Encoding") {
		t.Fatalf("Vary = %q, want Accept-Encoding", vary)
	}
	reader, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	plain, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read gzip body: %v", err)
	}
	if !strings.Contains(string(plain), "global-player") {
		t.Fatal("decompressed body does not look like player-bar.js")
	}
}

func TestStaleAssetHashRedirects(t *testing.T) {
	app, _, _ := setupTestApp(t)
	handler := app.Handler()

	req := httptest.NewRequest(http.MethodGet, "/admin/assets/000000000000/tokens.css", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if location := rec.Header().Get("Location"); location != app.assets.assetURL("tokens.css") {
		t.Fatalf("Location = %q, want %q", location, app.assets.assetURL("tokens.css"))
	}
}

func TestLegacyAssetPathNoCacheAndConditional(t *testing.T) {
	app, _, _ := setupTestApp(t)
	handler := app.Handler()

	req := httptest.NewRequest(http.MethodGet, "/admin/assets/tokens.css", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", got)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}

	conditional := httptest.NewRequest(http.MethodGet, "/admin/assets/tokens.css", nil)
	conditional.Header.Set("If-None-Match", etag)
	conditionalRec := httptest.NewRecorder()
	handler.ServeHTTP(conditionalRec, conditional)
	if conditionalRec.Code != http.StatusNotModified {
		t.Fatalf("conditional status = %d, want %d", conditionalRec.Code, http.StatusNotModified)
	}
	if conditionalRec.Body.Len() != 0 {
		t.Fatal("304 response must not carry a body")
	}
}

func TestAssetFuncUsedInTemplates(t *testing.T) {
	app, cookie, _ := csrfSessionApp(t)
	handler := app.Handler()

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="`+app.assets.assetURL("tokens.css")+`"`) {
		t.Fatal("dashboard does not reference the versioned stylesheet")
	}
	if !strings.Contains(body, `<meta name="app-build" content="`+app.assets.hash+`">`) {
		t.Fatal("dashboard missing app-build meta")
	}
	if !strings.Contains(body, `<meta name="csrf-token" content="`) {
		t.Fatal("dashboard missing csrf-token meta")
	}
}

func TestHeadReferencesPlayerModule(t *testing.T) {
	app, cookie, _ := csrfSessionApp(t)
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `<script type="module" src="`+app.assets.assetURL("player-main.js")+`"></script>`) {
		t.Fatal("dashboard missing versioned module entry")
	}
}

// The shared head must pull in the full split stylesheet set and the icon
// sprite meta tag on every chrome page.
func TestHeadReferencesSplitAssets(t *testing.T) {
	app, cookie, _ := csrfSessionApp(t)
	handler := app.Handler()

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	for _, name := range []string{"tokens.css", "base.css", "shell.css", "pages.css"} {
		if !strings.Contains(body, `href="`+app.assets.assetURL(name)+`"`) {
			t.Fatalf("dashboard head missing %s", name)
		}
	}
	if !strings.Contains(body, `<meta name="icon-sprite" content="`+app.assets.assetURL("icons.svg")+`">`) {
		t.Fatal("dashboard missing icon-sprite meta")
	}
}

// The icon sprite must be served with an SVG content type so <use>
// references resolve.
func TestIconSpriteServedAsSVG(t *testing.T) {
	app, _, _ := setupTestApp(t)
	handler := app.Handler()

	req := httptest.NewRequest(http.MethodGet, app.assets.assetURL("icons.svg"), nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/svg+xml") {
		t.Fatalf("Content-Type = %q, want image/svg+xml", ct)
	}
}

// importTestTrackWithArtwork inserts a track whose album carries a cached
// cover, so the album grid is guaranteed to render an artwork URL.
func importTestTrackWithArtwork(t *testing.T, app *App) int64 {
	t.Helper()
	ctx := context.Background()
	if err := app.store.EnsureLibrary(ctx, "Default", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := app.store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	coverPath := filepath.Join(t.TempDir(), "cover.jpg")
	cover := []byte{0xFF, 0xD8, 0xFF, 0xD9} // minimal JPEG markers; never served in this test
	if err := os.WriteFile(coverPath, cover, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := app.store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "Artist/Album/01.flac", FileSize: 1024, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Track", Album: "Album", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 1}, Artwork: &storage.ArtworkInput{Hash: "0123456789abcdef", MIMEType: "image/jpeg", CachePath: coverPath, SourceType: "embedded", SourcePath: coverPath, ByteSize: int64(len(cover))}}); err != nil {
		t.Fatal(err)
	}
	tracks, err := app.store.ListTracks(ctx, storage.Filters{Limit: 1})
	if err != nil || len(tracks) != 1 {
		t.Fatalf("ListTracks = %v, %v", tracks, err)
	}
	return tracks[0].ID
}

func TestThumbURL(t *testing.T) {
	if got := thumbURL("", 256); got != "" {
		t.Fatalf("thumbURL(empty) = %q, want empty", got)
	}
	if got := thumbURL("/api/v1/artwork/12", 512); got != "/api/v1/artwork/12?size=512" {
		t.Fatalf("thumbURL = %q", got)
	}
	if got := thumbURL("/api/v1/artists/3/image?foo=bar", 256); got != "/api/v1/artists/3/image?foo=bar&size=256" {
		t.Fatalf("thumbURL with query = %q", got)
	}
}

func TestAcceptsGzipQValues(t *testing.T) {
	cases := []struct {
		header string
		want   bool
	}{
		{"gzip", true},
		{"br, gzip", true},
		{"gzip;q=0.5", true},
		{"gzip; q=1", true},
		{"gzip;q=0", false},
		{"gzip;q=0.0", false},
		{"deflate", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := acceptsGzip(tc.header); got != tc.want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}
}

func TestETagMatchesWeakValidators(t *testing.T) {
	if !etagMatches(`"abc123"`, `"abc123"`) {
		t.Error("strong match should succeed")
	}
	if !etagMatches(`W/"abc123"`, `"abc123"`) {
		t.Error("weak validator should match the same entity")
	}
	if !etagMatches(`"other", W/"abc123"`, `"abc123"`) {
		t.Error("weak entry in a list should match")
	}
	if etagMatches(`W/"different"`, `"abc123"`) {
		t.Error("different entity must not match")
	}
}

func TestLibraryPagesUseThumbnails(t *testing.T) {
	app, cookie, _ := csrfSessionApp(t)
	importTestTrackWithArtwork(t, app)
	handler := app.Handler()

	req := httptest.NewRequest(http.MethodGet, "/admin/albums", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `/api/v1/artwork/`) {
		t.Fatal("album grid lost its artwork URL entirely (test data setup broken)")
	}
	if !strings.Contains(body, `?size=512`) {
		t.Fatal("album grid artwork must request the 512px thumbnail")
	}
	if !strings.Contains(body, `decoding="async"`) {
		t.Fatal("album grid images should use async decoding")
	}
}

func TestVersionedPlayerModulesJavaScriptMIME(t *testing.T) {
	app, _, _ := setupTestApp(t)
	for _, name := range []string{"player-main.js", "router.js", "player-core.js", "queue.js", "now-playing.js", "player-bar.js", "lyrics.js", "shortcuts.js", "util.js", "state.js", "fullscreen.js", "cast.js"} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, app.assets.assetURL(name), nil)
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			ct := rec.Header().Get("Content-Type")
			if !strings.HasPrefix(ct, "text/javascript") && !strings.HasPrefix(ct, "application/javascript") {
				t.Fatalf("Content-Type = %q", ct)
			}
		})
	}
}
