package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

// csrfSessionAppWithToken additionally returns the configured API token.
func csrfSessionAppWithToken(t *testing.T) (*App, *http.Cookie, string) {
	t.Helper()
	app, cookie, _ := csrfSessionApp(t)
	return app, cookie, app.config.APIToken
}

// importTestTrack inserts one available track and returns its ID. Several
// playback/write endpoints enforce foreign keys, so a real row is required.
func importTestTrack(t *testing.T, app *App) int64 {
	t.Helper()
	ctx := context.Background()
	if err := app.store.EnsureLibrary(ctx, "Default", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := app.store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "Artist/Album/01.flac", FileSize: 1024, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Track", Album: "Album", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	tracks, err := app.store.ListTracks(ctx, storage.Filters{Limit: 1})
	if err != nil || len(tracks) != 1 {
		t.Fatalf("ListTracks = %v, %v", tracks, err)
	}
	return tracks[0].ID
}

// csrfSessionApp returns an app wired with a session store plus the cookie
// and CSRF token of a freshly created admin session.
func csrfSessionApp(t *testing.T) (*App, *http.Cookie, string) {
	t.Helper()
	app, _, _ := setupTestApp(t)
	loginRec := httptest.NewRecorder()
	session, err := app.sessions.create(loginRec, "admin")
	if err != nil {
		t.Fatal(err)
	}
	cookies := loginRec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("login did not set a session cookie")
	}
	return app, cookies[0], session.CSRFToken
}

func TestSessionWriteWithoutCSRFHeaderRejected(t *testing.T) {
	app, cookie, _ := csrfSessionApp(t)
	handler := app.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/events", strings.NewReader(`{"trackId":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "csrf_invalid") {
		t.Fatalf("body = %s, want csrf_invalid error code", rec.Body.String())
	}
}

func TestSessionWriteWithWrongCSRFHeaderRejected(t *testing.T) {
	app, cookie, _ := csrfSessionApp(t)
	handler := app.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/events", strings.NewReader(`{"trackId":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", "definitely-not-the-session-token")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestSessionWriteWithValidCSRFHeaderAccepted(t *testing.T) {
	app, cookie, csrfToken := csrfSessionApp(t)
	trackID := importTestTrack(t, app)
	handler := app.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/events", strings.NewReader(fmt.Sprintf(`{"clientId":"device-1","clientKind":"web","sessionId":"session-csrf-001","seq":1,"type":"start","trackId":%d,"state":"playing","positionMillis":1000,"durationMillis":60000}`, trackID)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", csrfToken)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
		t.Fatalf("status = %d, want 2xx, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Code < 200 || rec.Code >= 300 {
		t.Fatalf("status = %d, want 2xx, body = %s", rec.Code, rec.Body.String())
	}
}

func TestBearerWriteWithoutCSRFHeaderAccepted(t *testing.T) {
	app, _, token := setupTestApp(t)
	trackID := importTestTrack(t, app)
	handler := app.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/events", strings.NewReader(fmt.Sprintf(`{"clientId":"device-1","clientKind":"web","sessionId":"session-csrf-002","seq":1,"type":"start","trackId":%d,"state":"playing","positionMillis":1000,"durationMillis":60000}`, trackID)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code < 200 || rec.Code >= 300 {
		t.Fatalf("status = %d, want 2xx, body = %s", rec.Code, rec.Body.String())
	}
}

func TestSessionGetUnaffectedByCSRFCheck(t *testing.T) {
	app, cookie, _ := csrfSessionApp(t)
	handler := app.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/playlists", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

// TestValidBearerBypassesSessionCSRF: a request carrying both a session
// cookie and a valid Bearer token is treated as an API client, so the CSRF
// header is not required.
func TestValidBearerBypassesSessionCSRF(t *testing.T) {
	app, cookie, token := csrfSessionAppWithToken(t)
	trackID := importTestTrack(t, app)
	handler := app.Handler()

	req := httptest.NewRequest(http.MethodPut, "/api/v1/tracks/"+fmt.Sprint(trackID)+"/favorite", nil)
	req.AddCookie(cookie)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code < 200 || rec.Code >= 300 {
		t.Fatalf("status = %d, want 2xx, body = %s", rec.Code, rec.Body.String())
	}
}

// TestInvalidBearerDoesNotFallBackToSession: a forged Authorization header
// must not silently downgrade to ambient session auth (which would bypass
// the CSRF requirement from a cross-site context). The request is rejected
// with 401 before the session is even considered.
func TestInvalidBearerDoesNotFallBackToSession(t *testing.T) {
	app, cookie, _ := csrfSessionAppWithToken(t)
	trackID := importTestTrack(t, app)
	handler := app.Handler()

	req := httptest.NewRequest(http.MethodPut, "/api/v1/tracks/"+fmt.Sprint(trackID)+"/favorite", nil)
	req.AddCookie(cookie)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

func TestSessionPutAndDeleteRequireCSRF(t *testing.T) {
	app, cookie, csrfToken := csrfSessionApp(t)
	trackID := importTestTrack(t, app)
	handler := app.Handler()

	favoriteURL := "/api/v1/tracks/" + fmt.Sprint(trackID) + "/favorite"

	// PUT without the header is rejected.
	req := httptest.NewRequest(http.MethodPut, favoriteURL, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("PUT without CSRF status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	// PUT with the correct header succeeds.
	req = httptest.NewRequest(http.MethodPut, favoriteURL, nil)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrfToken)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT with CSRF status = %d, want %d, body = %s", rec.Code, http.StatusNoContent, rec.Body.String())
	}

	// DELETE without the header is rejected.
	req = httptest.NewRequest(http.MethodDelete, favoriteURL, nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("DELETE without CSRF status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	// DELETE with the correct header succeeds.
	req = httptest.NewRequest(http.MethodDelete, favoriteURL, nil)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrfToken)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE with CSRF status = %d, want %d, body = %s", rec.Code, http.StatusNoContent, rec.Body.String())
	}
}
