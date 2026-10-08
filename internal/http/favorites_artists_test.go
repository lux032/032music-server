package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func postArtistFavorite(t *testing.T, app *App, cookie *http.Cookie, csrf string, id int64, favorite, returnTo string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"csrfToken": {csrf}, "favorite": {favorite}, "returnTo": {returnTo}}
	req := httptest.NewRequest(http.MethodPost, "/admin/favorites/artists/"+strconv.FormatInt(id, 10), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	return rec
}

func mustContain(t *testing.T, label, body string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Fatalf("%s missing %q", label, want)
		}
	}
}

// The no-JS fallback route favorites an artist and returns to the page.
func TestAdminArtistFavoriteRoute(t *testing.T) {
	app, cookie, csrf := csrfSessionApp(t)
	importSoloAlbum(t, app, "RouteSinger", "RouteAlbum", "")
	id := artistIDByName(t, app, "RouteSinger")
	if rec := postArtistFavorite(t, app, cookie, "forged", id, "1", "/admin/artists/album"); rec.Code != http.StatusForbidden {
		t.Fatalf("forged CSRF status = %d", rec.Code)
	}
	rec := postArtistFavorite(t, app, cookie, csrf, id, "1", "/admin/artists/album")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/admin/artists/album?") {
		t.Fatalf("status = %d, Location = %q", rec.Code, rec.Header().Get("Location"))
	}
	if detail, err := app.store.ArtistDetail(context.Background(), id); err != nil || !detail.IsFavorite {
		t.Fatalf("artist not favorited: %v", err)
	}
	if rec := postArtistFavorite(t, app, cookie, csrf, id, "0", "/admin/favorites"); rec.Code != http.StatusSeeOther {
		t.Fatalf("unfavorite status = %d", rec.Code)
	}
	if detail, _ := app.store.ArtistDetail(context.Background(), id); detail.IsFavorite {
		t.Fatal("artist still favorited")
	}
}

// Favorited singers and credited people get their own sections, each linking
// to the matching detail page with a filled heart.
func TestAdminFavoritesShowsSingersAndCredits(t *testing.T) {
	app, cookie, _ := csrfSessionApp(t)
	ctx := context.Background()
	importSoloAlbum(t, app, "FavSinger", "FavSingerAlbum", "FavComposer")
	singer := artistIDByName(t, app, "FavSinger")
	composer := artistIDByName(t, app, "FavComposer")
	for _, id := range []int64{singer, composer} {
		if err := app.store.SetArtistFavorite(ctx, id, true); err != nil {
			t.Fatal(err)
		}
	}
	body := followLocation(t, app, cookie, "/admin/favorites").Body.String()
	mustContain(t, "header", body, "1 位歌手")
	mustContain(t, "header", body, "1 位幕后人员")
	singers := followLocation(t, app, cookie, "/admin/favorites?kind=singers").Body.String()
	credits := followLocation(t, app, cookie, "/admin/favorites?kind=credits").Body.String()
	mustContain(t, "singers", singers, `id="favorite-singers"`, `href="/admin/artists/`+strconv.FormatInt(singer, 10)+`"`, "FavSinger", `action="/admin/favorites/artists/`+strconv.FormatInt(singer, 10)+`"`, `artist-fav-icon is-favorite`)
	mustContain(t, "credits", credits, `id="favorite-credits"`, `href="/admin/credits/`+strconv.FormatInt(composer, 10)+`"`, "FavComposer", "幕后 1 首")
	if strings.Contains(singers, "FavComposer") {
		t.Fatal("credit-only person listed as a singer")
	}
}

// Every artist entry point exposes the favorite heart.
func TestArtistFavoriteEntryPoints(t *testing.T) {
	app, cookie, _ := csrfSessionApp(t)
	importSoloAlbum(t, app, "EntrySinger", "EntryAlbum", "EntryComposer")
	singer := artistIDByName(t, app, "EntrySinger")
	composer := artistIDByName(t, app, "EntryComposer")
	if err := app.store.SetArtistFavorite(context.Background(), composer, true); err != nil {
		t.Fatal(err)
	}
	singerForm := `action="/admin/favorites/artists/` + strconv.FormatInt(singer, 10) + `" data-favorite-toggle`
	composerForm := `action="/admin/favorites/artists/` + strconv.FormatInt(composer, 10) + `" data-favorite-toggle`
	for _, tc := range []struct{ path, form, want string }{
		{"/admin/artists/album", singerForm, `artist-fav-icon" aria-pressed="false"`},
		{"/admin/credits", composerForm, `artist-fav-icon is-favorite" aria-pressed="true"`},
		{"/admin/artists/" + strconv.FormatInt(singer, 10), singerForm, `<span class="fav-label">收藏</span>`},
		{"/admin/credits/" + strconv.FormatInt(composer, 10), composerForm, `<span class="fav-label">已收藏</span>`},
	} {
		rec := followLocation(t, app, cookie, tc.path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d", tc.path, rec.Code)
		}
		mustContain(t, tc.path, rec.Body.String(), tc.form, tc.want)
	}
}
