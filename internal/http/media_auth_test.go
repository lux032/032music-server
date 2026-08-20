package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/lux032/032music-server/internal/config"
	"github.com/lux032/032music-server/internal/storage"
)

func testSessionManager(t *testing.T) *sessionManager {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return newSessionManager(false, store)
}

func TestMediaAccessAcceptsQueryToken(t *testing.T) {
	app := &App{
		config:   config.Config{APIToken: "api-token-at-least-24-characters", MediaToken: "media-token-at-least-24-characters"},
		sessions: testSessionManager(t),
	}
	handler := app.requireMediaAccess(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	request := httptest.NewRequest(http.MethodGet, "/stream?mediaToken=media-token-at-least-24-characters", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
}

func TestMediaAccessRejectsInvalidToken(t *testing.T) {
	app := &App{
		config:   config.Config{APIToken: "api-token-at-least-24-characters", MediaToken: "media-token-at-least-24-characters"},
		sessions: testSessionManager(t),
	}
	handler := app.requireMediaAccess(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	request := httptest.NewRequest(http.MethodGet, "/stream?mediaToken=wrong", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestMediaAccessAllowsOptionsAndCORSHeaders(t *testing.T) {
	app := &App{
		config:   config.Config{APIToken: "api-token-at-least-24-characters", MediaToken: "media-token-at-least-24-characters"},
		sessions: testSessionManager(t),
	}
	handler := app.requireMediaAccess(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	request := httptest.NewRequest(http.MethodOptions, "/api/v1/tracks/1/stream", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if response.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want *", response.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestRequireAPIOrAdminAllowsSessionAndToken(t *testing.T) {
	app := &App{
		config:   config.Config{APIToken: "api-token-at-least-24-characters"},
		sessions: testSessionManager(t),
	}
	loginRec := httptest.NewRecorder()
	_, _ = app.sessions.create(loginRec, "admin")
	sessionCookie := loginRec.Result().Cookies()[0]

	handler := app.requireAPIOrAdmin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	// 1. Valid Session Cookie
	req1 := httptest.NewRequest(http.MethodGet, "/api/v1/playback/timeline", nil)
	req1.AddCookie(sessionCookie)
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("session auth status = %d, want %d", rec1.Code, http.StatusOK)
	}

	// 2. Valid Bearer Token
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/playback/timeline", nil)
	req2.Header.Set("Authorization", "Bearer api-token-at-least-24-characters")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("token auth status = %d, want %d", rec2.Code, http.StatusOK)
	}

	// 3. No auth -> 401
	req3 := httptest.NewRequest(http.MethodGet, "/api/v1/playback/timeline", nil)
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want %d", rec3.Code, http.StatusUnauthorized)
	}
}
