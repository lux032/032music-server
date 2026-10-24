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

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/scanner"
)

func TestIdentityRebuildAuthenticationCSRFAndDuplicate(t *testing.T) {
	app, store, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "test.flac"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureLibrary(ctx, "Music", root); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	manager := scanner.New(ctx, store, slog.New(slog.NewTextHandler(io.Discard, nil)), library, t.TempDir())
	app.scanner = manager
	entered, release := make(chan struct{}), make(chan struct{})
	manager.SetMetadataReader(func(string) (metadata.AudioMetadata, error) {
		close(entered)
		<-release
		return metadata.AudioMetadata{Title: "Test", Album: "Test", Artists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1}, nil
	})
	t.Cleanup(func() { cancel(); manager.Wait() })
	// Composition mirrors production; no new background worker or overwritten production callback.
	manager.SetOnComplete(func() {
		if err := store.CompleteReleaseGroupRebuild(ctx); err != nil {
			t.Error(err)
		}
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/identity/rebuild", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/admin/login") {
		t.Fatalf("unauthenticated: %d", rec.Code)
	}
	rec = postSecurity(t, app, handler, cookie, "/admin/identity/rebuild", url.Values{"csrfToken": {"wrong"}})
	if rec.Code != http.StatusForbidden {
		t.Fatal(rec.Code)
	}
	state, err := store.ReleaseGroupRebuildState(ctx)
	if err != nil || state.Status != "not_ready" {
		t.Fatalf("%+v %v", state, err)
	}
	body := getWithCookie(handler, "/admin", cookie).Body.String()
	if !strings.Contains(body, "身份未就绪") || strings.Contains(body, testEnvAPIToken) || strings.Contains(body, testEnvMediaToken) {
		t.Fatal("missing readiness or leaked secret")
	}
	rec = postSecurity(t, app, handler, cookie, "/admin/identity/rebuild", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatal(rec.Code)
	}
	<-entered
	state, err = store.ReleaseGroupRebuildState(ctx)
	if err != nil || state.Status != "rebuilding" {
		t.Fatalf("%+v %v", state, err)
	}
	first := state.ScanJobID
	rec = postSecurity(t, app, handler, cookie, "/admin/identity/rebuild", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatal(rec.Code)
	}
	state, err = store.ReleaseGroupRebuildState(ctx)
	if err != nil || state.ScanJobID != first {
		t.Fatalf("duplicate started: %+v %v", state, err)
	}
	close(release)
	manager.Wait()
	state, err = store.ReleaseGroupRebuildState(ctx)
	if err != nil || state.Status != "ready" {
		t.Fatalf("%+v %v", state, err)
	}
}
