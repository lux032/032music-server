package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/lastfm"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

// lastFMLoveTestApp wires an app with one imported track and a fake Last.fm
// endpoint that accepts every call.
func lastFMLoveTestApp(t *testing.T) (*App, *storage.Store, http.Handler, *http.Cookie, string, int64) {
	t.Helper()
	app, store, handler := credentialTestApp(t)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		_, _ = io.WriteString(w, `{"lfm":{"status":"ok"}}`)
	}))
	t.Cleanup(fake.Close)
	service := lastfm.NewService(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	service.SetClientFactory(func(key, secret string) *lastfm.Client {
		client := lastfm.NewClient(key, secret)
		client.Endpoint = fake.URL
		return client
	})
	app.SetLastFM(service)
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: "a.flac", FileSize: 1, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{Title: "夜に駆ける", Album: "THE BOOK", Artists: []string{"YOASOBI"}, DurationMillis: 261000, DiscNumber: 1, TrackNumber: 2}}); err != nil {
		t.Fatal(err)
	}
	tracks, err := store.ListTracks(ctx, storage.Filters{Limit: 10})
	if err != nil || len(tracks) != 1 {
		t.Fatalf("tracks: %v %v", tracks, err)
	}
	if err = store.SaveMetadataSourceSetting(ctx, storage.MetadataSourceSetting{Source: "lastfm", APIKey: "key"}); err != nil {
		t.Fatal(err)
	}
	if err = store.SaveLastFMScrobblePreferences(ctx, true, true, "secret"); err != nil {
		t.Fatal(err)
	}
	if err = store.SetLastFMSession(ctx, "listener", "SK"); err != nil {
		t.Fatal(err)
	}
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	return app, store, handler, cookie, csrfOf(t, app, cookie), tracks[0].ID
}

func lovePost(t *testing.T, handler http.Handler, cookie *http.Cookie, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

// The card appears, the switch round-trips, and the status endpoint reports the
// queue and the backfill progress the poller renders.
func TestLastFMLoveSyncAdminFlow(t *testing.T) {
	_, store, handler, cookie, csrf, trackID := lastFMLoveTestApp(t)
	ctx := context.Background()

	body := getWithCookie(handler, lastFMSettingsPath, cookie).Body.String()
	if !strings.Contains(body, "Last.fm 红心") || !strings.Contains(body, "开启红心同步") || !strings.Contains(body, "开始回填") {
		t.Fatalf("settings page lacks the love card")
	}

	// Enabling stores the switch; the redirect returns to the settings page.
	response := lovePost(t, handler, cookie, "/admin/settings/lastfm-scrobble/love-sync", url.Values{"csrfToken": {csrf}, "sync_loves": {"1"}})
	if response.Code != http.StatusSeeOther {
		t.Fatalf("save switch: %d %q", response.Code, response.Header().Get("Location"))
	}
	if location, _ := url.Parse(response.Header().Get("Location")); location.Path != lastFMSettingsPath {
		t.Fatalf("save switch redirect: %q", response.Header().Get("Location"))
	}
	settings, _ := store.LastFMScrobbleSettings(ctx)
	if !settings.SyncLoves || !settings.LoveSyncReady() {
		t.Fatalf("switch not saved: %+v", settings)
	}

	// A favourite now queues a change, visible through the status endpoint.
	if err := store.SetTrackFavorite(ctx, trackID, true); err != nil {
		t.Fatal(err)
	}
	status := getWithCookie(handler, "/admin/settings/lastfm-scrobble/love-status", cookie)
	if status.Code != http.StatusOK {
		t.Fatalf("status: %d", status.Code)
	}
	var payload struct {
		Pending  int64 `json:"pending"`
		Backfill struct {
			State   string  `json:"state"`
			Total   int64   `json:"total"`
			Percent float64 `json:"percent"`
		} `json:"backfill"`
	}
	if err := json.Unmarshal(status.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Pending != 1 {
		t.Fatalf("pending = %d", payload.Pending)
	}

	// Starting the backfill counts the existing favourite and runs it.
	response = lovePost(t, handler, cookie, "/admin/settings/lastfm-scrobble/love-backfill", url.Values{"csrfToken": {csrf}})
	if response.Code != http.StatusSeeOther {
		t.Fatalf("start backfill: %d", response.Code)
	}
	run, _ := store.LastFMLoveBackfill(ctx)
	if run.State != storage.LoveBackfillRunning || run.Total != 1 {
		t.Fatalf("backfill not started: %+v", run)
	}
	// A second start while running is refused with a notice, not a second run.
	response = lovePost(t, handler, cookie, "/admin/settings/lastfm-scrobble/love-backfill", url.Values{"csrfToken": {csrf}})
	if response.Code != http.StatusSeeOther || !strings.Contains(response.Header().Get("Location"), "notice=") {
		t.Fatalf("double start: %d %q", response.Code, response.Header().Get("Location"))
	}

	// Cancelling stops the run and keeps the processed count.
	response = lovePost(t, handler, cookie, "/admin/settings/lastfm-scrobble/love-backfill/cancel", url.Values{"csrfToken": {csrf}})
	if response.Code != http.StatusSeeOther {
		t.Fatalf("cancel backfill: %d", response.Code)
	}
	if run, _ = store.LastFMLoveBackfill(ctx); run.State != storage.LoveBackfillCancelled {
		t.Fatalf("not cancelled: %+v", run)
	}

	// Turning the switch off is a separate action and leaves the queue alone.
	response = lovePost(t, handler, cookie, "/admin/settings/lastfm-scrobble/love-sync", url.Values{"csrfToken": {csrf}})
	if response.Code != http.StatusSeeOther {
		t.Fatalf("disable: %d", response.Code)
	}
	if settings, _ = store.LastFMScrobbleSettings(ctx); settings.SyncLoves {
		t.Fatal("switch still on")
	}
}

// Missing CSRF is rejected on every love action, like the other admin forms.
func TestLastFMLoveSyncRequiresCSRF(t *testing.T) {
	_, _, handler, cookie, _, _ := lastFMLoveTestApp(t)
	for _, path := range []string{
		"/admin/settings/lastfm-scrobble/love-sync",
		"/admin/settings/lastfm-scrobble/love-backfill",
		"/admin/settings/lastfm-scrobble/love-backfill/cancel",
		"/admin/settings/lastfm-scrobble/love-retry",
	} {
		request := httptest.NewRequest(http.MethodPost, path, nil)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s without CSRF: %d", path, response.Code)
		}
	}
}

// The retry action clears the backoff of queued love changes.
func TestLastFMLoveRetryClearsBackoff(t *testing.T) {
	_, store, handler, cookie, csrf, trackID := lastFMLoveTestApp(t)
	ctx := context.Background()
	if err := store.SetLastFMLoveSync(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTrackFavorite(ctx, trackID, true); err != nil {
		t.Fatal(err)
	}
	// Park the change in the future as if a transient failure had deferred it.
	items, err := store.DueLastFMLoves(ctx, time.Now().Add(time.Hour), 5)
	if err != nil || len(items) != 1 {
		t.Fatalf("queued: %+v %v", items, err)
	}
	if err = store.DeferLastFMLove(ctx, trackID, time.Now().Add(time.Hour), "Service Offline"); err != nil {
		t.Fatal(err)
	}
	if items, _ = store.DueLastFMLoves(ctx, time.Now(), 5); len(items) != 0 {
		t.Fatal("deferred change is due too early")
	}
	response := lovePost(t, handler, cookie, "/admin/settings/lastfm-scrobble/love-retry", url.Values{"csrfToken": {csrf}})
	if response.Code != http.StatusSeeOther {
		t.Fatalf("retry: %d", response.Code)
	}
	if items, _ = store.DueLastFMLoves(ctx, time.Now(), 5); len(items) != 1 {
		t.Fatalf("retry did not make the change due: %+v", items)
	}
}

// The backfill cannot start without a connected account and the switch.
func TestLastFMLoveBackfillNeedsReadyAccount(t *testing.T) {
	_, store, handler, cookie, csrf, _ := lastFMLoveTestApp(t)
	ctx := context.Background()
	if err := store.ClearLastFMSession(ctx, ""); err != nil {
		t.Fatal(err)
	}
	response := lovePost(t, handler, cookie, "/admin/settings/lastfm-scrobble/love-backfill", url.Values{"csrfToken": {csrf}})
	location, _ := url.Parse(response.Header().Get("Location"))
	if response.Code != http.StatusSeeOther || !strings.Contains(location.Query().Get("notice"), "连接") {
		t.Fatalf("start without session: %d %q", response.Code, response.Header().Get("Location"))
	}
	if run, _ := store.LastFMLoveBackfill(ctx); run.State == storage.LoveBackfillRunning {
		t.Fatalf("backfill started without a session: %+v", run)
	}
}
