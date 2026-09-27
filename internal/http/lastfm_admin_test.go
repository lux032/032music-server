package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/lastfm"
	"github.com/lux032/032music-server/internal/storage"
)

func TestLastFMScrobbleAdminFlow(t *testing.T) {
	app, store, handler := credentialTestApp(t)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("method") == "auth.getToken" {
			_, _ = io.WriteString(w, `{"token":"GOOD"}`)
			return
		}
		if r.Form.Get("method") == "auth.getSession" && r.Form.Get("token") == "GOOD" {
			_, _ = io.WriteString(w, `{"session":{"name":"listener","key":"SK"}}`)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":14,"message":"Unauthorized Token"}`)
	}))
	defer fake.Close()
	service := lastfm.NewService(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	service.SetClientFactory(func(key, secret string) *lastfm.Client {
		client := lastfm.NewClient(key, secret)
		client.Endpoint = fake.URL
		return client
	})
	app.SetLastFM(service)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	csrf := csrfOf(t, app, cookie)
	ctx := context.Background()

	post := func(path string, form url.Values) *httptest.ResponseRecorder {
		form.Set("csrfToken", csrf)
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	get := func(path string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	page := get("/admin/settings/metadata")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Last.fm Scrobble") || !strings.Contains(page.Body.String(), "lastfm_api_secret") {
		t.Fatalf("settings page: %d", page.Code)
	}

	if err := store.SaveMetadataSourceSetting(ctx, storage.MetadataSourceSetting{Source: "lastfm", APIKey: "key"}); err != nil {
		t.Fatal(err)
	}
	if response := post("/admin/settings/lastfm-scrobble", url.Values{"lastfm_api_secret": {"the-secret"}, "scrobble_enabled": {"on"}, "now_playing_enabled": {"on"}}); response.Code != http.StatusSeeOther {
		t.Fatalf("save: %d", response.Code)
	}
	if page = get("/admin/settings/metadata"); strings.Contains(page.Body.String(), "the-secret") {
		t.Fatal("secret echoed on the page")
	}

	// A forged callback before any authorisation was started binds nothing.
	get(lastFMCallbackPath + "?token=GOOD")
	if settings, _ := store.LastFMScrobbleSettings(ctx); settings.Connected() {
		t.Fatal("callback without a pending authorisation connected an account")
	}

	// Connecting must not redirect a form submission to last.fm: the admin
	// page's CSP (form-action 'self') makes browsers refuse that. It returns
	// to the settings page, which offers a plain link instead.
	response := post("/admin/settings/lastfm-scrobble/connect", url.Values{})
	location, _ := url.Parse(response.Header().Get("Location"))
	if response.Code != http.StatusSeeOther || location == nil || location.Host != "" || location.Path != lastFMSettingsPath {
		t.Fatalf("connect redirect: %d %q", response.Code, response.Header().Get("Location"))
	}
	body := get(lastFMSettingsPath).Body.String()
	if !strings.Contains(body, `href="https://www.last.fm/api/auth/?api_key=key&amp;token=GOOD"`) || !strings.Contains(body, "/admin/settings/lastfm-scrobble/complete") {
		t.Fatalf("settings page lacks the approval link / complete form")
	}

	response = post("/admin/settings/lastfm-scrobble/complete", url.Values{})
	if response.Code != http.StatusSeeOther || !strings.Contains(response.Header().Get("Location"), url.QueryEscape("listener")) {
		t.Fatalf("complete: %d %q", response.Code, response.Header().Get("Location"))
	}
	settings, _ := store.LastFMScrobbleSettings(ctx)
	if !settings.Ready() || settings.Username != "listener" || settings.APISecret != "the-secret" {
		t.Fatalf("after connect: %+v", settings)
	}
	if body = get(lastFMSettingsPath).Body.String(); strings.Contains(body, "/admin/settings/lastfm-scrobble/complete") {
		t.Fatal("approval link still offered after connecting")
	}

	if response = post("/admin/settings/lastfm-scrobble/disconnect", url.Values{}); response.Code != http.StatusSeeOther {
		t.Fatalf("disconnect: %d", response.Code)
	}
	if settings, _ = store.LastFMScrobbleSettings(ctx); settings.Connected() || settings.Username != "" {
		t.Fatalf("after disconnect: %+v", settings)
	}

	// Missing CSRF is rejected.
	request := httptest.NewRequest(http.MethodPost, "/admin/settings/lastfm-scrobble/connect", nil)
	request.AddCookie(cookie)
	rejected := httptest.NewRecorder()
	handler.ServeHTTP(rejected, request)
	if rejected.Code != http.StatusForbidden {
		t.Fatalf("connect without CSRF: %d", rejected.Code)
	}
}
