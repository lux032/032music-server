package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/scanner"
	"github.com/lux032/032music-server/internal/storage"
)

func TestAdminWorkRefreshRequiresCSRF(t *testing.T) {
	app, cookie, token := csrfSessionApp(t)
	app.scanner = scanner.New(context.Background(), app.store, slog.New(slog.NewTextHandler(io.Discard, nil)), storage.Library{}, t.TempDir())
	handler := app.Handler()
	send := func(values url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/admin/works/refresh", strings.NewReader(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := send(url.Values{}); rec.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status %d", rec.Code)
	}
	if rec := send(url.Values{"csrfToken": {token}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("refresh status=%d body=%s", rec.Code, rec.Body.String())
	}
}
func TestAdminWorkRefreshDuringScanRedirects(t *testing.T) {
	app, cookie, token := csrfSessionApp(t)
	root := t.TempDir()
	if e := app.store.EnsureLibrary(context.Background(), "Music", root); e != nil {
		t.Fatal(e)
	}
	lib, e := app.store.LibraryByRoot(context.Background(), root)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(root, "track.flac"), []byte("not audio"), 0600); e != nil {
		t.Fatal(e)
	}
	manager := scanner.New(context.Background(), app.store, slog.New(slog.NewTextHandler(io.Discard, nil)), lib, t.TempDir())
	app.scanner = manager
	job, e := manager.Start(context.Background(), "incremental")
	if e != nil {
		t.Fatal(e)
	}
	_ = job
	req := httptest.NewRequest(http.MethodPost, "/admin/works/refresh", strings.NewReader(url.Values{"csrfToken": {token}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	manager.Wait()
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), url.QueryEscape("扫描进行中，请稍后")) {
		t.Fatalf("scan-running status %d redirect %s", rec.Code, rec.Header().Get("Location"))
	}
}
