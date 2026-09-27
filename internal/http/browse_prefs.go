package httpapi

import (
	"net/http"
	"slices"
	"strconv"
)

// Browse preferences (sort order, album grid density) are remembered in
// cookies so they survive navigating away from and back to a list page,
// whether the page is loaded in full or swapped in by PJAX.

const browsePrefMaxAge = 365 * 24 * 60 * 60

// librarySortOptions lists the sort values each list page accepts; the key is
// also the cookie suffix.
var librarySortOptions = map[string][]string{
	"albums":        {"title", "year", "added"},
	"tracks":        {"title", "year"},
	"artists-album": {"name", "albums"},
	"artists-track": {"name", "tracks"},
	"works":         {"title", "year", "updated"},
}

func sortCookieName(page string) string { return "032_sort_" + page }

// rememberSort persists an explicitly chosen sort order and restores the
// remembered one when the request carries no sort parameter. An explicit empty
// "sort=" resets to the default order and forgets the preference. The request
// URL is rewritten in place so every link derived from it (pagination, index
// letters, filter chips, returnTo) keeps the effective sort.
func rememberSort(w http.ResponseWriter, r *http.Request, page string) {
	allowed := librarySortOptions[page]
	name := sortCookieName(page)
	q := r.URL.Query()
	if q.Has("sort") {
		value := q.Get("sort")
		if value != "" && slices.Contains(allowed, value) {
			http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/admin", MaxAge: browsePrefMaxAge, HttpOnly: true, SameSite: http.SameSiteLaxMode})
			return
		}
		if value == "" {
			http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/admin", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
			q.Del("sort")
			r.URL.RawQuery = q.Encode()
		}
		return
	}
	cookie, err := r.Cookie(name)
	if err != nil || !slices.Contains(allowed, cookie.Value) {
		return
	}
	q.Set("sort", cookie.Value)
	r.URL.RawQuery = q.Encode()
}

// clearPathFor returns the "clear all conditions" link. When a sort order is
// active it carries an explicit empty sort so the remembered order is reset as
// well instead of being restored on the bare path.
func clearPathFor(r *http.Request, path string) string {
	if r.URL.Query().Get("sort") != "" {
		return path + "?sort="
	}
	return path
}

const (
	albumGridColsCookie  = "032_album_cols"
	albumGridMinCols     = 2
	albumGridMaxCols     = 10
	albumGridDefaultCols = 4
)

// albumGridCols returns the remembered number of album covers per row, or 0
// when the user never changed it (the stylesheet default then applies).
func albumGridCols(r *http.Request) int {
	cookie, err := r.Cookie(albumGridColsCookie)
	if err != nil {
		return 0
	}
	cols, err := strconv.Atoi(cookie.Value)
	if err != nil || cols < albumGridMinCols || cols > albumGridMaxCols {
		return 0
	}
	return cols
}
