package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Before the catch-all existed, ServeMux method patterns answered OPTIONS
// with a bare 405, so the CORS middleware never ran and browser preflights
// always failed.
func TestAPIOptionsPreflight(t *testing.T) {
	app, _, _ := setupTestApp(t)
	handler := app.Handler()

	for _, path := range []string{
		"/api/v1/playlists",
		"/api/v1/tracks/1/stream",
		"/api/v1/health",
		"/api/v1/no-such-resource",
	} {
		req := httptest.NewRequest(http.MethodOptions, path, nil)
		req.Header.Set("Origin", "https://player.example")
		req.Header.Set("Access-Control-Request-Headers", "Authorization, Range")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusNoContent {
			t.Fatalf("OPTIONS %s: status = %d, want 204", path, rec.Code)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
			t.Fatalf("OPTIONS %s: Allow-Origin = %q, want *", path, got)
		}
		allowHeaders := rec.Header().Get("Access-Control-Allow-Headers")
		for _, want := range []string{"Authorization", "Range"} {
			if !strings.Contains(allowHeaders, want) {
				t.Fatalf("OPTIONS %s: Allow-Headers %q missing %s", path, allowHeaders, want)
			}
		}
	}
}

// Non-OPTIONS requests that match no specific API route must still pass
// through authentication and end at a JSON 404 instead of a mux 405.
func TestAPIFallbackNotFound(t *testing.T) {
	app, _, token := setupTestApp(t)
	handler := app.Handler()

	unauthorized := httptest.NewRequest(http.MethodGet, "/api/v1/no-such-resource", nil)
	unauthorizedRec := httptest.NewRecorder()
	handler.ServeHTTP(unauthorizedRec, unauthorized)
	if unauthorizedRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", unauthorizedRec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/no-such-resource", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
}

// A path registered under other methods must get a JSON 405 with an Allow
// header, mirroring ServeMux's native method-mismatch behavior.
func TestAPIFallbackMethodNotAllowed(t *testing.T) {
	app, _, token := setupTestApp(t)
	handler := app.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/albums", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405, body = %s", rec.Code, rec.Body.String())
	}
	allow := rec.Header().Get("Allow")
	for _, want := range []string{"GET", "HEAD", "OPTIONS"} {
		if !strings.Contains(allow, want) {
			t.Fatalf("Allow = %q, missing %s", allow, want)
		}
	}
	if !strings.Contains(rec.Body.String(), "method_not_allowed") {
		t.Fatalf("body = %s, want method_not_allowed", rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Allow-Origin = %q, want *", got)
	}
}

// A session-authenticated write that falls through to the fallback (unknown
// path) is still a session write: it must pass the CSRF header check before
// it may reach the 404.
func TestAPIFallbackSessionWriteStillRequiresCSRF(t *testing.T) {
	app, cookie, csrfToken := csrfSessionApp(t)
	handler := app.Handler()

	noHeader := httptest.NewRequest(http.MethodPost, "/api/v1/no-such-resource", nil)
	noHeader.AddCookie(cookie)
	noHeaderRec := httptest.NewRecorder()
	handler.ServeHTTP(noHeaderRec, noHeader)
	if noHeaderRec.Code != http.StatusForbidden {
		t.Fatalf("without CSRF header status = %d, want 403", noHeaderRec.Code)
	}

	withHeader := httptest.NewRequest(http.MethodPost, "/api/v1/no-such-resource", nil)
	withHeader.AddCookie(cookie)
	withHeader.Header.Set("X-CSRF-Token", csrfToken)
	withHeaderRec := httptest.NewRecorder()
	handler.ServeHTTP(withHeaderRec, withHeader)
	if withHeaderRec.Code != http.StatusNotFound {
		t.Fatalf("with CSRF header status = %d, want 404", withHeaderRec.Code)
	}
}

// A bare /api/v1 (no trailing slash) keeps the pre-fallback behavior: a
// plain 404 rather than a ServeMux subtree redirect.
func TestAPIBarePathNotFound(t *testing.T) {
	app, _, token := setupTestApp(t)
	handler := app.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
