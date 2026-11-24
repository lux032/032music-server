package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func TestBrowsePagesRememberSortAndAlbumGridCols(t *testing.T) {
	app, _, _ := setupTestApp(t)
	admin := adminCookie(t, app)
	get := func(path string, cookies ...*http.Cookie) (*httptest.ResponseRecorder, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(admin)
		for _, cookie := range cookies {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status %d", path, rec.Code)
		}
		return rec, rec.Body.String()
	}
	sortCookie := func(rec *httptest.ResponseRecorder, name string) *http.Cookie {
		for _, cookie := range rec.Result().Cookies() {
			if cookie.Name == name {
				return cookie
			}
		}
		return nil
	}

	// Choosing a sort stores it.
	rec, _ := get("/admin/albums?sort=year")
	saved := sortCookie(rec, "032_sort_albums")
	if saved == nil || saved.Value != "year" || saved.MaxAge <= 0 {
		t.Fatalf("sort cookie not saved: %+v", saved)
	}
	// Returning to the bare page restores it, including derived links.
	_, body := get("/admin/albums", saved)
	if !strings.Contains(body, `value="year" selected`) || !strings.Contains(body, "排序：发行年份") {
		t.Fatalf("remembered sort not applied")
	}
	if !strings.Contains(body, `/admin/albums?index=A&amp;sort=year`) {
		t.Fatalf("index links lost remembered sort")
	}
	// The chip's remove link and the clear link reset explicitly.
	if !strings.Contains(body, `href="/admin/albums?sort="`) {
		t.Fatalf("sort reset link missing")
	}
	rec, body = get("/admin/albums?sort=", saved)
	if c := sortCookie(rec, "032_sort_albums"); c == nil || c.MaxAge >= 0 {
		t.Fatalf("sort cookie not cleared: %+v", c)
	}
	if strings.Contains(body, "排序：发行年份") {
		t.Fatalf("sort still applied after reset")
	}
	// Invalid values are neither stored nor restored; pages are independent.
	rec, _ = get("/admin/albums?sort=bogus")
	if sortCookie(rec, "032_sort_albums") != nil {
		t.Fatalf("invalid sort stored")
	}
	_, body = get("/admin/tracks", saved)
	if strings.Contains(body, "排序：") {
		t.Fatalf("albums sort leaked into tracks page")
	}
	_, body = get("/admin/works", &http.Cookie{Name: "032_sort_works", Value: "updated"})
	if !strings.Contains(body, `value="updated" selected`) {
		t.Fatalf("works sort not restored")
	}

	// Album grid density.
	_, body = get("/admin/albums")
	if strings.Contains(body, "--album-cols") || !strings.Contains(body, `data-grid-cols`) {
		t.Fatalf("default grid should not set columns but render the slider")
	}
	_, body = get("/admin/albums", &http.Cookie{Name: "032_album_cols", Value: "7"})
	if !strings.Contains(body, `style="--album-cols: 7"`) || !strings.Contains(body, `value="7"`) {
		t.Fatalf("grid columns not applied")
	}
	_, body = get("/admin/albums", &http.Cookie{Name: "032_album_cols", Value: "99"})
	if strings.Contains(body, "--album-cols") {
		t.Fatalf("out-of-range columns applied")
	}
}

func TestArtistGridColsCookieAndDualModeShell(t *testing.T) {
	app, cookie, _ := csrfSessionApp(t)
	ctx := t.Context()
	root := t.TempDir()
	if err := app.store.EnsureLibrary(ctx, "GridTest", root); err != nil {
		t.Fatal(err)
	}
	library, err := app.store.LibraryByRoot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if err = app.store.ImportTrack(ctx, storage.ImportInput{
		LibraryID: library.ID, RelativePath: "Album1/01.flac", FileSize: 1, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{
			Title: "Song 1", Album: "Album 1", Artists: []string{"Yorushika"}, AlbumArtists: []string{"Yorushika"},
			DiscNumber: 1, TrackNumber: 1, Raw: map[string][]string{"DATE": {"2024-01-01"}, "RELEASETYPE": {"album"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	artists, err := app.store.ListArtists(ctx, storage.Filters{Query: "Yorushika", Limit: 10})
	if err != nil || len(artists) != 1 {
		t.Fatalf("artists = %+v, err = %v", artists, err)
	}
	artistPath := "/admin/artists/" + strconv.FormatInt(artists[0].ID, 10)

	get := func(path string, extra ...*http.Cookie) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(cookie)
		for _, c := range extra {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
		return rec.Body.String()
	}

	// Default artist page emits slider without inline --artist-cols.
	body := get(artistPath)
	if strings.Contains(body, "--artist-cols") || !strings.Contains(body, `data-artist-grid-cols`) {
		t.Fatalf("default artist page should render slider without inline --artist-cols")
	}
	// Valid 032_artist_cols cookie emits inline style and slider value.
	body = get(artistPath, &http.Cookie{Name: "032_artist_cols", Value: "6"})
	if !strings.Contains(body, `style="--artist-cols: 6"`) || !strings.Contains(body, `value="6"`) {
		t.Fatalf("artist grid columns cookie not applied")
	}
	// Out-of-range cookie is ignored.
	body = get(artistPath, &http.Cookie{Name: "032_artist_cols", Value: "99"})
	if strings.Contains(body, "--artist-cols") {
		t.Fatalf("out-of-range artist grid columns applied")
	}

	// Dual-mode shell: player page vs admin workspace page + integrated queue/lyrics panel.
	homeBody := get("/admin/home")
	if strings.Contains(homeBody, `class="sidebar is-admin-workspace"`) {
		t.Fatalf("home page should not activate admin workspace sidebar")
	}
	for _, want := range []string{`id="sidebar-admin-entry"`, `id="np-tab-queue"`, `id="np-tab-lyrics"`, `id="lyrics-scroll-container"`} {
		if !strings.Contains(homeBody, want) {
			t.Fatalf("home page missing %s", want)
		}
	}

	for _, adminPath := range []string{"/admin", "/admin/library/missing", "/admin/audio-analysis", "/admin/ai-tagging", "/admin/artist-aliases", "/admin/online-lyrics", "/admin/lyrics-translations", "/admin/settings/acquisition"} {
		adminBody := get(adminPath)
		if !strings.Contains(adminBody, `class="sidebar is-admin-workspace"`) {
			t.Fatalf("%s should activate admin workspace sidebar", adminPath)
		}
	}
}
