package httpapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func gunzip(t *testing.T, body io.Reader) string {
	t.Helper()
	reader, err := gzip.NewReader(body)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	plain, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read gzip body: %v", err)
	}
	return string(plain)
}

func TestGzipCompressesHTML(t *testing.T) {
	app, cookie, _ := csrfSessionApp(t)
	handler := app.Handler()

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if rec.Header().Get("Content-Length") != "" {
		t.Fatal("Content-Length must be removed on compressed responses")
	}
	if vary := rec.Header().Get("Vary"); !strings.Contains(vary, "Accept-Encoding") {
		t.Fatalf("Vary = %q, want Accept-Encoding", vary)
	}
	if plain := gunzip(t, rec.Body); !strings.Contains(plain, "控制台") {
		t.Fatal("decompressed body does not look like the dashboard")
	}
}

func TestGzipCompressesJSON(t *testing.T) {
	app, _, token := setupTestApp(t)
	handler := app.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if plain := gunzip(t, rec.Body); !strings.Contains(plain, `"status"`) {
		t.Fatal("decompressed body does not look like the status JSON")
	}
}

// importStreamTrack creates a real audio file on disk so handleStream can
// serve it, and returns its track ID.
func importStreamTrack(t *testing.T, app *App) int64 {
	t.Helper()
	ctx := context.Background()
	musicDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(musicDir, "Artist", "Album"), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 4096)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	if err := os.WriteFile(filepath.Join(musicDir, "Artist", "Album", "01.flac"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := app.store.EnsureLibrary(ctx, "Default", musicDir); err != nil {
		t.Fatal(err)
	}
	library, err := app.store.LibraryByRoot(ctx, musicDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "Artist/Album/01.flac", FileSize: int64(len(payload)), ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Track", Album: "Album", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	tracks, err := app.store.ListTracks(ctx, storage.Filters{Limit: 1})
	if err != nil || len(tracks) != 1 {
		t.Fatalf("ListTracks = %v, %v", tracks, err)
	}
	return tracks[0].ID
}

// TestGzipSkipsRangeStream proves the middleware never wraps media streams:
// a Range request must come back as an untouched 206 partial response.
func TestGzipSkipsRangeStream(t *testing.T) {
	app, cookie, _ := csrfSessionApp(t)
	trackID := importStreamTrack(t, app)
	handler := app.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tracks/"+strconv.FormatInt(trackID, 10)+"/stream", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Range", "bytes=0-99")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusPartialContent, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty on media streams", got)
	}
	if got := rec.Header().Get("Content-Range"); !strings.HasPrefix(got, "bytes 0-99/") {
		t.Fatalf("Content-Range = %q, want bytes 0-99/...", got)
	}
	if rec.Body.Len() != 100 {
		t.Fatalf("body length = %d, want 100", rec.Body.Len())
	}
}

// TestGzipSkipsStreamPath: even without a Range header the /stream path is
// excluded from compression entirely.
func TestGzipSkipsStreamPath(t *testing.T) {
	app, cookie, _ := csrfSessionApp(t)
	trackID := importStreamTrack(t, app)
	handler := app.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tracks/"+strconv.FormatInt(trackID, 10)+"/stream", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty on /stream responses", got)
	}
}

// TestGzipSkipsRangeRequests: any request carrying a Range header is left
// untouched even on compressible HTML pages.
func TestGzipSkipsRangeRequests(t *testing.T) {
	app, cookie, _ := csrfSessionApp(t)
	handler := app.Handler()

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Range", "bytes=0-99")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty when the request has a Range header", got)
	}
}

func TestGzipSkipsNoContent(t *testing.T) {
	app, _, token := setupTestApp(t)
	trackID := importTestTrack(t, app)
	handler := app.Handler()

	// A successful timeline write answers 204 without a body; the gzip
	// middleware must forward it untouched.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/timeline", strings.NewReader(fmt.Sprintf(`{"trackId":%d,"positionMillis":1000,"durationMillis":60000,"state":"playing"}`, trackID)))
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusNoContent, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty on 204", got)
	}
	if rec.Body.Len() != 0 {
		t.Fatal("204 response must not carry a body")
	}
}

func TestGzipSkipsAssets(t *testing.T) {
	app, _, _ := setupTestApp(t)
	handler := app.Handler()

	// Assets are pre-compressed by the registry; the middleware must leave
	// them alone even on the legacy no-cache path.
	req := httptest.NewRequest(http.MethodGet, "/admin/assets/admin.css", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	// The registry itself answers with gzip when accepted; what must not
	// happen is double compression by the middleware.
	plain := gunzip(t, rec.Body)
	if strings.Contains(plain, "\x1f\x8b") {
		t.Fatal("asset body looks double-compressed")
	}
	if !strings.Contains(plain, "roon-shell") {
		t.Fatal("asset body does not look like admin.css")
	}
}

func TestRenderBufferPoolDropsOversized(t *testing.T) {
	big := bytes.NewBuffer(make([]byte, 0, 300*1024))
	putRenderBuffer(big)
	if got := renderBufferPool.Get().(*bytes.Buffer); got == big {
		t.Fatal("oversized buffer must not be returned to the pool")
	}
	small := &bytes.Buffer{}
	putRenderBuffer(small)
	if got := renderBufferPool.Get().(*bytes.Buffer); got != small {
		t.Fatal("small buffer should be reused from the pool")
	}
}

// statusSpy records every WriteHeader call so informational (1xx) responses
// can be observed; httptest.ResponseRecorder only keeps the first one.
type statusSpy struct {
	header   http.Header
	statuses []int
	body     bytes.Buffer
}

func (s *statusSpy) Header() http.Header         { return s.header }
func (s *statusSpy) WriteHeader(status int)      { s.statuses = append(s.statuses, status) }
func (s *statusSpy) Write(p []byte) (int, error) { return s.body.Write(p) }

func TestGzipForwardsInformationalStatus(t *testing.T) {
	app, _, _ := setupTestApp(t)
	wrapped := app.gzipResponse(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusEarlyHints)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html><body>hello</body></html>"))
	}))

	spy := &statusSpy{header: http.Header{}}
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	wrapped.ServeHTTP(spy, req)

	if len(spy.statuses) != 2 || spy.statuses[0] != http.StatusEarlyHints || spy.statuses[1] != http.StatusOK {
		t.Fatalf("statuses = %v, want [103 200]", spy.statuses)
	}
	if got := spy.header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip on the final HTML response", got)
	}
}

func TestGzipWriterPoolResetsBeforeReuse(t *testing.T) {
	writer := gzipWriterPool.Get().(*gzip.Writer)
	var first bytes.Buffer
	writer.Reset(&first)
	_, _ = writer.Write([]byte("payload"))
	_ = writer.Close()
	writer.Reset(io.Discard)
	gzipWriterPool.Put(writer)

	reused := gzipWriterPool.Get().(*gzip.Writer)
	var second bytes.Buffer
	reused.Reset(&second)
	_, _ = reused.Write([]byte("fresh"))
	if err := reused.Close(); err != nil {
		t.Fatal(err)
	}
	if got := gunzip(t, &second); got != "fresh" {
		t.Fatalf("reused writer produced %q, want %q", got, "fresh")
	}
}

func TestRenderBuffersBeforeWriting(t *testing.T) {
	app, _, _ := setupTestApp(t)
	rec := httptest.NewRecorder()
	app.render(rec, http.StatusOK, "does-not-exist.html", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if ct := rec.Header().Get("Content-Type"); strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want plain error response", ct)
	}
}
