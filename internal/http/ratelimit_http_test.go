package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/enrichment"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

// rateLimitTestApp builds an admin app with a real enrichment manager, an
// imported artist, and a logged-in session.
func rateLimitTestApp(t *testing.T) (*App, *storage.Store, http.Handler, *enrichment.Manager, *http.Cookie, string) {
	t.Helper()
	store := credentialTestStore(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := enrichment.New(context.Background(), store, logger, t.TempDir())
	app, err := NewApp(credentialTestConfig(t), store, nil, manager, logger, "test")
	if err != nil {
		t.Fatal(err)
	}
	handler := app.Handler()
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	csrf := csrfOf(t, app, cookie)
	ctx := context.Background()
	if err = store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "Artist/Album/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Track", Album: "Album", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	return app, store, handler, manager, cookie, csrf
}

func postAdminForm(t *testing.T, handler http.Handler, cookie *http.Cookie, csrf, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	if form == nil {
		form = url.Values{}
	}
	form.Set("csrfToken", csrf)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func enableSource(t *testing.T, store *storage.Store, source string) {
	t.Helper()
	setting, err := store.MetadataSourceSetting(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	setting.Enabled = true
	setting.AutoMatch = true
	if source == "lastfm" {
		setting.APIKey = "key"
	}
	if source == "musicbrainz" {
		setting.Contact = "me@example.com"
	}
	if err = store.SaveMetadataSourceSetting(context.Background(), setting); err != nil {
		t.Fatal(err)
	}
}

func notice303Of(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status=%d body=%s, want 303", rec.Code, rec.Body.String())
	}
	return noticeOf(t, rec)
}

// M1: a manual match during the MusicBrainz backoff gets an immediate,
// friendly Chinese notice instead of an English error or a 500.
func TestMatchArtistRateLimitNotice(t *testing.T) {
	_, store, handler, manager, cookie, csrf := rateLimitTestApp(t)
	ctx := context.Background()
	enableSource(t, store, "musicbrainz")
	artists, err := store.ArtistsForMatching(ctx)
	if err != nil || len(artists) == 0 {
		t.Fatalf("artists=%v err=%v", artists, err)
	}
	manager.NoteRateLimited("musicbrainz", 5*time.Minute)
	rec := postAdminForm(t, handler, cookie, csrf, "/admin/artists/"+strconv.FormatInt(artists[0].ID, 10)+"/match", nil)
	notice := notice303Of(t, rec)
	if !strings.Contains(notice, "MusicBrainz 限流中") || !strings.Contains(notice, "分钟后再试") {
		t.Fatalf("notice=%q", notice)
	}
}

// M1: confirming a candidate during the backoff still succeeds (it is a
// database operation), but the notice mentions that image/biography refresh
// is deferred by the rate limit.
func TestConfirmArtistMatchRateLimitNotice(t *testing.T) {
	_, store, handler, manager, cookie, csrf := rateLimitTestApp(t)
	ctx := context.Background()
	enableSource(t, store, "musicbrainz")
	artists, err := store.ArtistsForMatching(ctx)
	if err != nil || len(artists) == 0 {
		t.Fatalf("artists=%v err=%v", artists, err)
	}
	artistID := artists[0].ID
	if err = store.ReplaceArtistCandidates(ctx, artistID, []storage.ArtistCandidate{{Source: "musicbrainz", ExternalID: "mbid-confirm-1", DisplayName: "Artist", MBID: "mbid-confirm-1", Score: 95}}); err != nil {
		t.Fatal(err)
	}
	candidates, err := store.ArtistCandidates(ctx, artistID)
	if err != nil || len(candidates) == 0 {
		t.Fatalf("candidates=%v err=%v", candidates, err)
	}
	manager.NoteRateLimited("musicbrainz", 5*time.Minute)
	rec := postAdminForm(t, handler, cookie, csrf, "/admin/artists/"+strconv.FormatInt(artistID, 10)+"/confirm/"+strconv.FormatInt(candidates[0].ID, 10), nil)
	notice := notice303Of(t, rec)
	if !strings.Contains(notice, "已确认匹配；MusicBrainz 限流中") || !strings.Contains(notice, "手动刷新") {
		t.Fatalf("notice=%q", notice)
	}
}

// M1: a manual biography refresh during the backoff shows the friendly
// notice instead of the English error text.
func TestRefreshArtistBiographiesRateLimitNotice(t *testing.T) {
	_, store, handler, manager, cookie, csrf := rateLimitTestApp(t)
	ctx := context.Background()
	artists, err := store.ArtistsForMatching(ctx)
	if err != nil || len(artists) == 0 {
		t.Fatalf("artists=%v err=%v", artists, err)
	}
	// Confirmed MBID so the (default-enabled) Wikipedia path issues a
	// MusicBrainz relations request, which the backoff rejects immediately.
	profile := storage.ExternalArtistProfile{Source: "musicbrainz", ExternalID: "mbid-bio-refresh-1", DisplayName: "Artist"}
	if err = store.UpsertExternalArtistProfile(ctx, artists[0].ID, profile); err != nil {
		t.Fatal(err)
	}
	manager.NoteRateLimited("musicbrainz", 5*time.Minute)
	rec := postAdminForm(t, handler, cookie, csrf, "/admin/artists/"+strconv.FormatInt(artists[0].ID, 10)+"/biographies/refresh", nil)
	notice := notice303Of(t, rec)
	if !strings.Contains(notice, "MusicBrainz 限流中") || strings.Contains(notice, "rate limited") {
		t.Fatalf("notice=%q", notice)
	}
}

// L3: the admin settings form rejects contacts containing control characters
// with a Chinese notice and does not persist them.
func TestSaveMetadataSettingsRejectsControlCharContact(t *testing.T) {
	_, store, handler, _, cookie, csrf := rateLimitTestApp(t)
	form := url.Values{
		"scope":                           {"musicbrainz"},
		"musicbrainz_enabled":             {"on"},
		"musicbrainz_contact":             {"me@example.com\r\nX-Injected: yes"},
		"musicbrainz_application_name":    {"032 Music Server"},
		"musicbrainz_application_version": {"dev"},
	}
	rec := postAdminForm(t, handler, cookie, csrf, "/admin/settings/metadata", form)
	notice := notice303Of(t, rec)
	if !strings.Contains(notice, "联系方式不能包含") {
		t.Fatalf("notice=%q", notice)
	}
	setting, err := store.MetadataSourceSetting(context.Background(), "musicbrainz")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(setting.Contact, "\r\n") || setting.Enabled {
		t.Fatalf("rejected setting was persisted: %+v", setting)
	}
}

// L-2: a manual match that confirms first and only then hits a rate limit
// (image/biography fetch) must say the match succeeded, not failed.
func TestMatchArtistConfirmedThenRateLimitNotice(t *testing.T) {
	_, store, handler, manager, cookie, csrf := rateLimitTestApp(t)
	ctx := context.Background()
	// Give the fixture artist a tagged MBID so the lookup auto-confirms
	// (score 100) before any rate limit.
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "Artist/Album/02.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Track 2", Album: "Album", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 2, Raw: map[string][]string{"MUSICBRAINZ_ARTISTID": {"11111111-1111-4111-8111-111111111111"}}}}); err != nil {
		t.Fatal(err)
	}
	// The lookup (inc=aliases...) succeeds; the biography relations request
	// (inc=url-rels) after the confirmation is rate limited.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "aliases") {
			_, _ = io.WriteString(w, `{"id":"11111111-1111-4111-8111-111111111111","name":"Artist","sort-name":"Artist","aliases":[],"tags":[],"relations":[]}`)
			return
		}
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	manager.SetMusicBrainzBaseURL(server.URL + "/mb")
	enableSource(t, store, "musicbrainz")
	artists, err := store.ArtistsForMatching(ctx)
	if err != nil || len(artists) == 0 {
		t.Fatalf("artists=%v err=%v", artists, err)
	}
	rec := postAdminForm(t, handler, cookie, csrf, "/admin/artists/"+strconv.FormatInt(artists[0].ID, 10)+"/match", nil)
	notice := notice303Of(t, rec)
	if !strings.Contains(notice, "已自动确认匹配，但图片/简介因 MusicBrainz 限流暂未获取，约 1 分钟后可重试") {
		t.Fatalf("notice=%q", notice)
	}
}
