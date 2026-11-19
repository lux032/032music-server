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

	"github.com/lux032/032music-server/internal/listenbrainz"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func listenBrainzTestApp(t *testing.T) (*App, *storage.Store, http.Handler, *http.Cookie, string, int64) {
	t.Helper()
	app, store, handler := credentialTestApp(t)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/1/validate-token":
			if r.Header.Get("Authorization") != "Token good-token" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"code":401,"error":"Invalid authorization token."}`)
				return
			}
			_, _ = io.WriteString(w, `{"code":200,"valid":true,"user_name":"lb-user"}`)
		default:
			_, _ = io.WriteString(w, `{"status":"ok"}`)
		}
	}))
	t.Cleanup(fake.Close)
	svc := listenbrainz.NewService(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.SetClientFactory(func(_ string) *listenbrainz.Client {
		return listenbrainz.NewClient(fake.URL)
	})
	app.SetListenBrainz(svc)

	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ImportTrack(ctx, storage.ImportInput{
		LibraryID: lib.ID, RelativePath: "lb.flac", FileSize: 1, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album", Artists: []string{"Artist"}, DurationMillis: 200000, DiscNumber: 1, TrackNumber: 1},
	}); err != nil {
		t.Fatal(err)
	}
	tracks, err := store.ListTracks(ctx, storage.Filters{Limit: 10})
	if err != nil || len(tracks) != 1 {
		t.Fatalf("tracks: %v %v", tracks, err)
	}
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	return app, store, handler, cookie, csrfOf(t, app, cookie), tracks[0].ID
}

func TestListenBrainzAdminSettingsAndLoveFlow(t *testing.T) {
	_, store, handler, cookie, csrf, trackID := listenBrainzTestApp(t)
	ctx := context.Background()

	body := getWithCookie(handler, lastFMSettingsPath, cookie).Body.String()
	if !strings.Contains(body, "ListenBrainz Scrobble") || !strings.Contains(body, "ListenBrainz 红心") {
		t.Fatalf("settings page lacks ListenBrainz cards")
	}

	// Bad token is rejected and does not connect.
	badResp := lovePost(t, handler, cookie, "/admin/settings/listenbrainz", url.Values{
		"csrfToken":               {csrf},
		"listenbrainz_enabled":    {"1"},
		"listenbrainz_server_url": {"https://lb.example.com"},
		"listenbrainz_user_token": {"bad-token"},
	})
	if badResp.Code != http.StatusSeeOther {
		t.Fatalf("bad token status = %d", badResp.Code)
	}
	if s, _ := store.ListenBrainzSettings(ctx); s.Connected() {
		t.Fatalf("bad token should not connect: %+v", s)
	}

	// Good token validates and connects, and token is never echoed back in HTML.
	goodResp := lovePost(t, handler, cookie, "/admin/settings/listenbrainz", url.Values{
		"csrfToken":                {csrf},
		"listenbrainz_enabled":     {"1"},
		"listenbrainz_now_playing": {"1"},
		"listenbrainz_server_url":  {"https://lb.example.com/"},
		"listenbrainz_user_token":  {"good-token"},
	})
	if goodResp.Code != http.StatusSeeOther {
		t.Fatalf("good token status = %d", goodResp.Code)
	}
	settings, _ := store.ListenBrainzSettings(ctx)
	if !settings.Connected() || settings.Username != "lb-user" || settings.ServerURL != "https://lb.example.com" {
		t.Fatalf("settings = %+v", settings)
	}
	pageAfter := getWithCookie(handler, lastFMSettingsPath, cookie).Body.String()
	if strings.Contains(pageAfter, "good-token") || !strings.Contains(pageAfter, "lb-user") {
		t.Fatalf("token leaked or username missing")
	}

	// Enable love sync, queue favorite, and inspect love-status JSON.
	_ = lovePost(t, handler, cookie, "/admin/settings/listenbrainz/love-sync", url.Values{"csrfToken": {csrf}, "sync_loves": {"1"}})
	if err := store.SetTrackFavorite(ctx, trackID, true); err != nil {
		t.Fatal(err)
	}
	status := getWithCookie(handler, "/admin/settings/listenbrainz/love-status", cookie)
	if status.Code != http.StatusOK {
		t.Fatalf("love-status = %d", status.Code)
	}
	var payload struct {
		Pending int64 `json:"pending"`
	}
	if err := json.Unmarshal(status.Body.Bytes(), &payload); err != nil || payload.Pending != 1 {
		t.Fatalf("love-status payload = %s err=%v", status.Body.String(), err)
	}

	// Backfill start, cancel, resume (from cursor), and disable love sync with "0".
	lib, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ImportTrack(ctx, storage.ImportInput{
		LibraryID: lib.ID, RelativePath: "lb2.flac", FileSize: 2, ModifiedAtNS: 2,
		Metadata: metadata.AudioMetadata{Title: "Song 2", Album: "Album", Artists: []string{"Artist"}, DurationMillis: 200000, DiscNumber: 1, TrackNumber: 2},
	}); err != nil {
		t.Fatal(err)
	}
	tracks, err := store.ListTracks(ctx, storage.Filters{Limit: 10})
	if err != nil || len(tracks) != 2 {
		t.Fatalf("tracks: %v %v", tracks, err)
	}
	var secondTrackID int64
	for _, tr := range tracks {
		if tr.ID != trackID {
			secondTrackID = tr.ID
		}
	}
	if err := store.SetTrackFavorite(ctx, secondTrackID, true); err != nil {
		t.Fatal(err)
	}
	_ = lovePost(t, handler, cookie, "/admin/settings/listenbrainz/love-backfill", url.Values{"csrfToken": {csrf}})
	if run, _ := store.ListenBrainzLoveBackfill(ctx); run.State != storage.LoveBackfillRunning || run.Total != 2 {
		t.Fatalf("backfill after start = %+v", run)
	}
	if err := store.AdvanceListenBrainzLoveBackfill(ctx, trackID, 0, 0); err != nil {
		t.Fatal(err)
	}
	_ = lovePost(t, handler, cookie, "/admin/settings/listenbrainz/love-backfill/cancel", url.Values{"csrfToken": {csrf}})
	if run, _ := store.ListenBrainzLoveBackfill(ctx); run.State != storage.LoveBackfillCancelled || run.Cursor != trackID || run.Processed != 1 {
		t.Fatalf("backfill after cancel = %+v", run)
	}
	_ = lovePost(t, handler, cookie, "/admin/settings/listenbrainz/love-backfill", url.Values{"csrfToken": {csrf}})
	if run, _ := store.ListenBrainzLoveBackfill(ctx); run.State != storage.LoveBackfillRunning || run.Cursor != trackID || run.Processed != 1 || run.Total != 2 {
		t.Fatalf("backfill after resume = %+v", run)
	}

	// Turning love sync off via sync_loves=0 (the UI hidden input) must actually disable it.
	_ = lovePost(t, handler, cookie, "/admin/settings/listenbrainz/love-sync", url.Values{"csrfToken": {csrf}, "sync_loves": {"0"}})
	if s, _ := store.ListenBrainzSettings(ctx); s.SyncLoves {
		t.Fatalf("expected sync_loves=false after posting sync_loves=0, got %+v", s)
	}

	// Disconnect clears connection.
	_ = lovePost(t, handler, cookie, "/admin/settings/listenbrainz/disconnect", url.Values{"csrfToken": {csrf}})
	if s, _ := store.ListenBrainzSettings(ctx); s.Connected() {
		t.Fatalf("expected disconnected after disconnect: %+v", s)
	}
}
