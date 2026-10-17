package httpapi

import (
	"html"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// albumCardTitles returns the album titles of a rendered albums page in grid order.
func albumCardTitles(body string) []string {
	var titles []string
	for _, match := range regexp.MustCompile(`Album \d\d`).FindAllString(body, -1) {
		if len(titles) == 0 || titles[len(titles)-1] != match {
			titles = append(titles, match)
		}
	}
	return titles
}

func TestLibraryPagesOrderRandomAndYearRange(t *testing.T) {
	app, store, _ := setupTestApp(t)
	seedBrowseLibrary(t, store)
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
		return rec, html.UnescapeString(rec.Body.String())
	}
	cookie := func(rec *httptest.ResponseRecorder, name string) *http.Cookie {
		for _, c := range rec.Result().Cookies() {
			if c.Name == name {
				return c
			}
		}
		return nil
	}

	// Natural direction first, with a toggle to the other direction.
	_, body := get("/admin/albums?sort=year")
	if titles := albumCardTitles(body); len(titles) < 2 || titles[0] != "Album 11" {
		t.Fatalf("year desc titles = %v", titles)
	}
	if !strings.Contains(body, `href="/admin/albums?order=asc&sort=year"`) || !strings.Contains(body, "从新到旧") {
		t.Fatalf("order toggle missing")
	}
	// An explicit direction is applied and remembered.
	rec, body := get("/admin/albums?sort=year&order=asc")
	if titles := albumCardTitles(body); titles[0] != "Album 01" || titles[len(titles)-1] != "Album 12" {
		t.Fatalf("year asc titles = %v", titles)
	}
	saved := cookie(rec, "032_order_albums")
	if saved == nil || saved.Value != "asc" {
		t.Fatalf("order cookie = %+v", saved)
	}
	_, body = get("/admin/albums?sort=year", saved)
	if titles := albumCardTitles(body); titles[0] != "Album 01" || !strings.Contains(body, "从旧到新") {
		t.Fatalf("remembered order not applied: %v", titles)
	}
	// Clearing the conditions also forgets the direction.
	if !strings.Contains(body, `href="/admin/albums?order=&sort="`) {
		t.Fatalf("clear link does not reset order")
	}
	rec, _ = get("/admin/albums?order=", saved)
	if c := cookie(rec, "032_order_albums"); c == nil || c.MaxAge >= 0 {
		t.Fatalf("order cookie not cleared: %+v", c)
	}

	// Random: the seed is written into the links so pages share one shuffle.
	_, body = get("/admin/albums?sort=random&page=1")
	seed := regexp.MustCompile(`seed=(\d+)`).FindStringSubmatch(body)
	if seed == nil || !strings.Contains(body, "换一批") || strings.Contains(body, "从新到旧") {
		t.Fatalf("random page lacks seed links or reshuffle")
	}
	_, first := get("/admin/albums?sort=random&seed=" + seed[1])
	_, again := get("/admin/albums?sort=random&seed=" + seed[1])
	if a, b := albumCardTitles(first), albumCardTitles(again); strings.Join(a, ",") != strings.Join(b, ",") || len(a) != 12 {
		t.Fatalf("same seed, different shuffle: %v vs %v", a, b)
	}

	// Album year range: filter, chips and trigger label.
	_, body = get("/admin/albums?yearFrom=2003&yearTo=2005&sort=year&order=asc")
	if titles := albumCardTitles(body); strings.Join(titles, ",") != "Album 03,Album 04,Album 05" {
		t.Fatalf("year range titles = %v", titles)
	}
	for _, want := range []string{"起始年份：2003", "结束年份：2005", "2003 – 2005", `name="yearFrom" min="1800" max="9999" inputmode="numeric" value="2003"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("year range page lacks %q", want)
		}
	}
	// Pagination and index links keep the range.
	if !strings.Contains(body, "yearFrom=2003") || !strings.Contains(body, "index=A&order=asc&sort=year&yearFrom=2003&yearTo=2005") {
		t.Fatalf("index links lost the year range")
	}
	_, body = get("/admin/albums?yearFrom=2005&yearTo=2003")
	if !strings.Contains(body, "已忽略无效筛选") || len(albumCardTitles(body)) != 12 {
		t.Fatalf("reversed range not ignored")
	}

	// Tracks: new sorts with a direction.
	_, body = get("/admin/tracks?sort=duration&order=desc")
	if i, j := strings.Index(body, "Song 12"), strings.Index(body, "Song 01"); i < 0 || j < 0 || i > j || !strings.Contains(body, "从长到短") {
		t.Fatalf("tracks duration desc not applied")
	}
	// Artists: random is offered and seeded.
	_, body = get("/admin/artists/album?sort=random")
	if !strings.Contains(body, `data-value="random">随机`) || !regexp.MustCompile(`seed=\d+`).MatchString(body) {
		t.Fatalf("artist random sort missing")
	}
}
