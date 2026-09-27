package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func importBulkAlbums(t *testing.T, app *App, titles ...string) map[string]int64 {
	t.Helper()
	ctx := context.Background()
	if err := app.store.EnsureLibrary(ctx, "Default", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := app.store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range titles {
		if err := app.store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: title + "/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: title + " song", Album: title, Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	albums, err := app.store.ListAlbums(ctx, storage.Filters{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, album := range albums {
		ids[album.Title] = album.ID
	}
	return ids
}

func postAlbumBulk(t *testing.T, app *App, cookie *http.Cookie, path, csrf string, ids ...int64) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"csrfToken": {csrf}, "returnTo": {"/admin/albums?sort=title"}}
	for _, id := range ids {
		form.Add("album", strconv.FormatInt(id, 10))
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	return rec
}

func TestAdminMergeAlbumsUsesFirstSelectionAsMain(t *testing.T) {
	app, cookie, csrf := csrfSessionApp(t)
	ids := importBulkAlbums(t, app, "Alpha", "Beta", "Gamma")
	// Beta is ticked first, so it is the main album even though it is not first by id.
	rec := postAlbumBulk(t, app, cookie, "/admin/albums/merge", csrf, ids["Beta"], ids["Gamma"], ids["Alpha"])
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	location := rec.Header().Get("Location")
	if !strings.HasPrefix(location, "/admin/albums?") || !strings.Contains(location, "sort=title") {
		t.Fatalf("Location = %q, want return to the album list", location)
	}
	albums, err := app.store.ListAlbums(context.Background(), storage.Filters{Limit: 50})
	if err != nil || len(albums) != 1 || albums[0].ID != ids["Beta"] {
		t.Fatalf("albums = %+v, err = %v; want only Beta", albums, err)
	}
	tracks, err := app.store.ListTracks(context.Background(), storage.Filters{AlbumID: ids["Beta"], Limit: 50})
	if err != nil || len(tracks) != 3 {
		t.Fatalf("tracks = %d, err = %v; want 3", len(tracks), err)
	}
}

func TestAdminDeleteAlbums(t *testing.T) {
	app, cookie, csrf := csrfSessionApp(t)
	ids := importBulkAlbums(t, app, "Alpha", "Beta", "Gamma")
	rec := postAlbumBulk(t, app, cookie, "/admin/albums/delete", csrf, ids["Alpha"], ids["Gamma"])
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	albums, err := app.store.ListAlbums(context.Background(), storage.Filters{Limit: 50})
	if err != nil || len(albums) != 1 || albums[0].ID != ids["Beta"] {
		t.Fatalf("albums = %+v, err = %v; want only Beta", albums, err)
	}
}

func TestAdminAlbumBulkRejectsBadInput(t *testing.T) {
	app, cookie, csrf := csrfSessionApp(t)
	ids := importBulkAlbums(t, app, "Alpha", "Beta")
	if rec := postAlbumBulk(t, app, cookie, "/admin/albums/delete", "forged", ids["Alpha"]); rec.Code != http.StatusForbidden {
		t.Fatalf("forged CSRF status = %d, want 403", rec.Code)
	}
	if rec := postAlbumBulk(t, app, cookie, "/admin/albums/merge", csrf, ids["Alpha"]); rec.Code != http.StatusSeeOther {
		t.Fatalf("single-album merge status = %d, want redirect with notice", rec.Code)
	}
	if rec := postAlbumBulk(t, app, cookie, "/admin/albums/merge", csrf, ids["Alpha"], ids["Beta"]+100); rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "notice=") {
		t.Fatalf("stale selection status = %d Location = %q", rec.Code, rec.Header().Get("Location"))
	}
	albums, _ := app.store.ListAlbums(context.Background(), storage.Filters{Limit: 50})
	if len(albums) != 2 {
		t.Fatalf("albums = %d, rejected requests must not change the library", len(albums))
	}
}

func TestAdminAlbumsPageRendersSelectionControls(t *testing.T) {
	app, cookie, _ := csrfSessionApp(t)
	importBulkAlbums(t, app, "Alpha")
	req := httptest.NewRequest(http.MethodGet, "/admin/albums", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	for _, want := range []string{`class="album-select"`, `data-album-bulk-form`, `data-album-bulk="merge"`, `data-album-bulk="delete"`, `data-album-title="Alpha"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("albums page missing %s", want)
		}
	}
}

func TestAdminAlbumsPageReleaseDateSortAndArtistLinks(t *testing.T) {
	app, cookie, _ := csrfSessionApp(t)
	importBulkAlbums(t, app, "Alpha")
	req := httptest.NewRequest(http.MethodGet, "/admin/albums?sort=date", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `value="date" selected`) || !strings.Contains(body, "排序：发行日期") {
		t.Fatalf("release date sort not applied (status %d)", rec.Code)
	}
	var saved bool
	for _, c := range rec.Result().Cookies() {
		saved = saved || (c.Name == "032_sort_albums" && c.Value == "date")
	}
	if !saved {
		t.Fatal("release date sort should be remembered")
	}
	if !strings.Contains(body, `class="album-card-artists"><a href="/admin/artists/`) {
		t.Fatal("album card artist name should link to the artist page")
	}
}
