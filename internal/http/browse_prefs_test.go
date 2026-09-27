package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
