package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func adminCookie(t *testing.T, app *App) *http.Cookie {
	t.Helper()
	login := httptest.NewRecorder()
	if _, err := app.sessions.create(login, "admin"); err != nil {
		t.Fatal(err)
	}
	return login.Result().Cookies()[0]
}

func TestAdminOptionsEndpoints(t *testing.T) {
	ctx := context.Background()
	app, store, _ := setupTestApp(t)
	if err := store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	importArtist := func(name string) {
		t.Helper()
		if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "opt-" + name + ".flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song " + name, Album: "Album " + name, Artists: []string{name}, AlbumArtists: []string{name}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	importArtist("Alpha Singer")
	importArtist("Beta Singer")

	// Unauthenticated requests get a 401 JSON error (these endpoints are
	// consumed via fetch; a redirect would resolve to the login HTML page).
	req := httptest.NewRequest(http.MethodGet, "/admin/options/artists?role=album", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d", rec.Code)
	}
	var apiErr struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &apiErr); err != nil || apiErr.Error.Code != "unauthorized" {
		t.Fatalf("unauthenticated body: %q (%v)", rec.Body.String(), err)
	}

	cookie := adminCookie(t, app)
	get := func(path string) ([]optionItem, int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		var items []optionItem
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
				t.Fatalf("invalid JSON %q: %v", rec.Body.String(), err)
			}
		}
		return items, rec.Code
	}

	artists, code := get("/admin/options/artists?role=album")
	if code != http.StatusOK || len(artists) != 2 {
		t.Fatalf("artists = %v (%d)", artists, code)
	}
	if artists[0].ID == 0 || artists[0].Label == "" {
		t.Fatalf("bad shape: %+v", artists[0])
	}

	// q filters, role filters.
	filtered, _ := get("/admin/options/artists?role=album&q=Alpha")
	if len(filtered) != 1 || filtered[0].Label != "Alpha Singer" {
		t.Fatalf("filtered = %+v", filtered)
	}
	none, _ := get("/admin/options/artists?role=album&q=zzz")
	if len(none) != 0 {
		t.Fatalf("none = %+v", none)
	}

	albums, code := get("/admin/options/albums?q=Beta")
	if code != http.StatusOK || len(albums) != 1 || albums[0].Label != "Album Beta Singer" {
		t.Fatalf("albums = %+v (%d)", albums, code)
	}

	// Default limit is 20; anything above 100 is capped at 100.
	for i := 0; i < 130; i++ {
		importArtist("Singer " + strconv.Itoa(i))
	}
	capped, _ := get("/admin/options/artists?role=album&limit=500")
	if len(capped) != 100 {
		t.Fatalf("capped = %d, want 100", len(capped))
	}
	defaulted, _ := get("/admin/options/artists?role=album")
	if len(defaulted) != 20 {
		t.Fatalf("defaulted = %d, want 20", len(defaulted))
	}
}

func TestAdminPagesResolveSelectedFilterNames(t *testing.T) {
	ctx := context.Background()
	app, store, _ := setupTestApp(t)
	if err := store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "sel.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Selected Album", Artists: []string{"Selected Artist"}, AlbumArtists: []string{"Selected Artist"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	artists, err := store.ListArtists(ctx, storage.Filters{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(artists) != 1 {
		t.Fatalf("artists=%v", artists)
	}
	albums, err := store.ListAlbums(ctx, storage.Filters{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(albums) != 1 {
		t.Fatalf("albums=%v", albums)
	}
	cookie := adminCookie(t, app)
	get := func(path string) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", path, rec.Code)
		}
		return rec.Body.String()
	}
	// The selected artist/album must appear as a labelled option and as a
	// resolved filter chip even though it is not inside an options page of
	// size zero.
	id := strconv.FormatInt(artists[0].ID, 10)
	body := get("/admin/albums?artist=" + id)
	if !containsAll(body, "Selected Artist", "歌手：Selected Artist") {
		t.Fatalf("albums page missing selected artist label")
	}
	albumID := strconv.FormatInt(albums[0].ID, 10)
	body = get("/admin/tracks?album=" + albumID)
	if !containsAll(body, "Selected Album", "专辑：Selected Album") {
		t.Fatalf("tracks page missing selected album label")
	}
}

func containsAll(body string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(body, needle) {
			return false
		}
	}
	return true
}

func TestAdminAlbumsPageIgnoresAlbumParamWithoutPhantomCard(t *testing.T) {
	ctx := context.Background()
	app, store, _ := setupTestApp(t)
	if err := store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		name := "Grid " + strconv.Itoa(i)
		if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: name + ".flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song " + name, Album: "Album " + name, Artists: []string{"Singer " + name}, AlbumArtists: []string{"Singer " + name}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	albums, err := store.ListAlbums(ctx, storage.Filters{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(albums) != 3 {
		t.Fatalf("albums=%d", len(albums))
	}
	cookie := adminCookie(t, app)
	req := httptest.NewRequest(http.MethodGet, "/admin/albums?album="+strconv.FormatInt(albums[0].ID, 10), nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	cards := strings.Count(body, "library-album-card")
	if cards != 3 {
		t.Fatalf("grid cards = %d, want 3 (phantom card?)", cards)
	}
	if !strings.Contains(body, "3 个结果") {
		t.Fatalf("result count missing: %s", body[:400])
	}
}

func TestAdminAlbumsPageResolvesSelectedArtistBeyondFirstOptionsPage(t *testing.T) {
	ctx := context.Background()
	app, store, _ := setupTestApp(t)
	if err := store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	// "AAA First" sorts before "ZZZ Last"; with the options page size forced
	// to 1 the selected artist falls outside the first options page.
	for _, name := range []string{"AAA First", "ZZZ Last"} {
		if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: name + ".flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album " + name, Artists: []string{name}, AlbumArtists: []string{name}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	artists, err := store.ListArtists(ctx, storage.Filters{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var selected int64
	for _, artist := range artists {
		if artist.Name == "ZZZ Last" {
			selected = artist.ID
		}
	}
	if selected == 0 {
		t.Fatalf("artists=%v", artists)
	}

	oldLimit := adminOptionsPageLimit
	adminOptionsPageLimit = 1
	defer func() { adminOptionsPageLimit = oldLimit }()

	cookie := adminCookie(t, app)
	req := httptest.NewRequest(http.MethodGet, "/admin/albums?artist="+strconv.FormatInt(selected, 10), nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "歌手：ZZZ Last") {
		t.Fatalf("filter chip not resolved: %s", body[:600])
	}
	if !strings.Contains(body, `value="`+strconv.FormatInt(selected, 10)+`" selected`) {
		t.Fatalf("selected option missing from dropdown")
	}
}
