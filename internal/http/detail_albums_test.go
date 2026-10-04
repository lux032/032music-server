package httpapi

// Contract and safe-return tests for album bulk operations surfaced on the
// artist and credit detail pages (detail-albums batch).

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

// importSoloAlbum inserts one album whose only artist association is the
// named artist, so deleting the album orphan-cleans that artist.
func importSoloAlbum(t *testing.T, app *App, artist, albumTitle, composer string) int64 {
	t.Helper()
	ctx := context.Background()
	if err := app.store.EnsureLibrary(ctx, "Default", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := app.store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	meta := metadata.AudioMetadata{Title: albumTitle + " song", Album: albumTitle, Artists: []string{artist}, AlbumArtists: []string{artist}, DiscNumber: 1, TrackNumber: 1}
	if composer != "" {
		meta.Composer = composer
	}
	if err := app.store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: albumTitle + "/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: meta}); err != nil {
		t.Fatal(err)
	}
	albums, err := app.store.ListAlbums(ctx, storage.Filters{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, album := range albums {
		if album.Title == albumTitle {
			return album.ID
		}
	}
	t.Fatalf("album %q not imported", albumTitle)
	return 0
}

func artistIDByName(t *testing.T, app *App, name string) int64 {
	t.Helper()
	options, err := app.store.ListArtistOptions(context.Background(), "all", name, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, option := range options {
		if option.Name == name {
			return option.ID
		}
	}
	t.Fatalf("artist %q not found", name)
	return 0
}

func postAlbumBulkReturnTo(t *testing.T, app *App, cookie *http.Cookie, path, csrf, returnTo string, ids ...int64) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"csrfToken": {csrf}, "returnTo": {returnTo}}
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

func followLocation(t *testing.T, app *App, cookie *http.Cookie, location string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, location, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	return rec
}

// Blocker-1: deleting the last album of an artist orphan-cleans the artist,
// so the detail return address would 404 and PJAX would misreport a committed
// success as "保存失败". The handler must fall back to the matching list page
// with the success notice intact.
func TestAdminDeleteAlbumsArtistGoneReturnsToArtistList(t *testing.T) {
	app, cookie, csrf := csrfSessionApp(t)
	albumID := importSoloAlbum(t, app, "SoloSinger", "SoloAlbum", "")
	artistID := artistIDByName(t, app, "SoloSinger")
	returnTo := "/admin/artists/" + strconv.FormatInt(artistID, 10)
	rec := postAlbumBulkReturnTo(t, app, cookie, "/admin/albums/delete", csrf, returnTo, albumID)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	location := rec.Header().Get("Location")
	if !strings.HasPrefix(location, "/admin/artists/album?") || strings.Count(location, "notice=") != 1 {
		t.Fatalf("Location = %q, want /admin/artists/album with a single success notice", location)
	}
	if _, err := app.store.CanonicalArtistID(context.Background(), artistID); err == nil {
		t.Fatal("orphaned artist should be gone")
	}
	if follow := followLocation(t, app, cookie, location); follow.Code != http.StatusOK || !strings.Contains(follow.Body.String(), "已从曲库删除") {
		t.Fatalf("followed redirect status = %d", follow.Code)
	}
}

// Blocker-1, credit variant: same orphan cleanup on a pure backstage person.
func TestAdminDeleteAlbumsCreditGoneReturnsToCreditList(t *testing.T) {
	app, cookie, csrf := csrfSessionApp(t)
	albumID := importSoloAlbum(t, app, "CreditSinger", "CreditAlbum", "OnlyComposer")
	creditID := artistIDByName(t, app, "OnlyComposer")
	returnTo := "/admin/credits/" + strconv.FormatInt(creditID, 10)
	rec := postAlbumBulkReturnTo(t, app, cookie, "/admin/albums/delete", csrf, returnTo, albumID)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	location := rec.Header().Get("Location")
	if !strings.HasPrefix(location, "/admin/credits?") || strings.Count(location, "notice=") != 1 {
		t.Fatalf("Location = %q, want /admin/credits with a single success notice", location)
	}
	if follow := followLocation(t, app, cookie, location); follow.Code != http.StatusOK || !strings.Contains(follow.Body.String(), "已从曲库删除") {
		t.Fatalf("followed redirect status = %d", follow.Code)
	}
}

// High-2: the artist detail page renders shared album cards plus exactly one
// bulk-action bar whose returnTo is the detail address itself.
func TestArtistDetailPageRendersAlbumSelectionUI(t *testing.T) {
	app, cookie, _ := csrfSessionApp(t)
	importSoloAlbum(t, app, "CardSinger", "CardAlbum", "")
	artistID := artistIDByName(t, app, "CardSinger")
	rec := followLocation(t, app, cookie, fmt.Sprintf("/admin/artists/%d", artistID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`class="album-select"`, `data-album-bulk-form`, `data-album-bulk="merge"`, `data-album-bulk="delete"`, `class="album-browser detail-album-grid"`, `class="artist-discography"`, `data-album-title="CardAlbum"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("artist page missing %s", want)
		}
	}
	if !strings.Contains(body, fmt.Sprintf(`name="returnTo" value="/admin/artists/%d"`, artistID)) {
		t.Fatal("bulk form returnTo should be the artist detail address")
	}
	if strings.Count(body, `class="album-selection-bar"`) != 1 {
		t.Fatalf("album-selection-bar count = %d, want 1", strings.Count(body, `class="album-selection-bar"`))
	}
}

// High-2, credit variant: returnTo keeps the current role/offset context.
func TestCreditDetailPageRendersAlbumSelectionUI(t *testing.T) {
	app, cookie, _ := csrfSessionApp(t)
	importSoloAlbum(t, app, "CreditCardSinger", "CreditCardAlbum", "CardComposer")
	creditID := artistIDByName(t, app, "CardComposer")
	rec := followLocation(t, app, cookie, fmt.Sprintf("/admin/credits/%d?role=composer", creditID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`class="album-select"`, `data-album-bulk-form`, `data-album-bulk="merge"`, `data-album-bulk="delete"`, `id="credit-albums"`, `class="album-browser detail-album-grid"`, `data-album-title="CreditCardAlbum"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("credit page missing %s", want)
		}
	}
	if !strings.Contains(body, fmt.Sprintf(`name="returnTo" value="/admin/credits/%d?role=composer"`, creditID)) {
		t.Fatal("bulk form returnTo should keep the credit detail role context")
	}
	if strings.Count(body, `class="album-selection-bar"`) != 1 {
		t.Fatalf("album-selection-bar count = %d, want 1", strings.Count(body, `class="album-selection-bar"`))
	}
}
