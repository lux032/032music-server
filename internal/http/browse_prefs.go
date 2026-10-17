package httpapi

import (
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"

	"github.com/lux032/032music-server/internal/storage"
)

// Browse preferences (sort order, album grid density) are remembered in
// cookies so they survive navigating away from and back to a list page,
// whether the page is loaded in full or swapped in by PJAX.

const browsePrefMaxAge = 365 * 24 * 60 * 60

// librarySortOptions lists the sort values each list page accepts; the key is
// also the cookie suffix.
var librarySortOptions = map[string][]string{
	"credits":       {"name", "tracks"},
	"albums":        {"title", "year", "date", "added", "random"},
	"tracks":        {"title", "year", "date", "plays", "recentlyPlayed", "added", "duration", "random"},
	"artists-album": {"name", "albums", "random"},
	"artists-track": {"name", "tracks", "random"},
	"works":         {"title", "year", "updated"},
}

func sortCookieName(page string) string { return "032_sort_" + page }

// libraryPageSizes 是各列表页可选的每页数量；批次 8 只有作品页开放。
var libraryPageSizes = map[string][]int{
	"works": {24, 36, 60, 120},
}

const defaultPageSize = 36

func pageSizeCookieName(page string) string { return "032_pagesize_" + page }

// rememberPageSize 与 rememberSort 同机制：URL 带合法的 size 时记入 cookie；
// URL 没有 size 时从 cookie 恢复并改写进请求 URL，分页/筛选链接由此自动
// 带上 size；非法值回落默认值且不覆盖已记住的选择。
func rememberPageSize(w http.ResponseWriter, r *http.Request, page string) int {
	allowed := libraryPageSizes[page]
	name := pageSizeCookieName(page)
	q := r.URL.Query()
	if q.Has("size") {
		if value, err := strconv.Atoi(q.Get("size")); err == nil && slices.Contains(allowed, value) {
			http.SetCookie(w, &http.Cookie{Name: name, Value: strconv.Itoa(value), Path: "/admin", MaxAge: browsePrefMaxAge, HttpOnly: true, SameSite: http.SameSiteLaxMode})
			return value
		}
		// 非法 size：回落默认并从 URL 移除，避免分页链接继续携带坏值。
		q.Del("size")
		r.URL.RawQuery = q.Encode()
		return defaultPageSize
	}
	cookie, err := r.Cookie(name)
	if err == nil {
		if value, convErr := strconv.Atoi(cookie.Value); convErr == nil && slices.Contains(allowed, value) {
			q.Set("size", strconv.Itoa(value))
			r.URL.RawQuery = q.Encode()
			return value
		}
	}
	return defaultPageSize
}

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

// clearPathFor returns the "clear all conditions" link. When a sort or a
// sort direction is active it carries explicit empty values so the remembered
// preferences are reset as well instead of being restored on the bare path.
func clearPathFor(r *http.Request, path string) string {
	q := r.URL.Query()
	switch {
	case q.Get("sort") != "" && q.Get("order") != "":
		return path + "?order=&sort="
	case q.Get("sort") != "":
		return path + "?sort="
	case q.Get("order") != "":
		return path + "?order="
	}
	return path
}

func orderCookieName(page string) string { return "032_order_" + page }

// rememberOrder is rememberSort for the sort direction (order=asc|desc):
// an explicit value is remembered, an explicit empty "order=" restores the
// sort's natural direction and forgets the preference, and a request without
// order gets the remembered one written into its URL. Pages that do not list
// sort directions (credits, works) never call it.
func rememberOrder(w http.ResponseWriter, r *http.Request, page string) {
	name := orderCookieName(page)
	q := r.URL.Query()
	if q.Has("order") {
		value := q.Get("order")
		if value == "asc" || value == "desc" {
			http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/admin", MaxAge: browsePrefMaxAge, HttpOnly: true, SameSite: http.SameSiteLaxMode})
			return
		}
		if value == "" {
			http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/admin", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		}
		q.Del("order")
		r.URL.RawQuery = q.Encode()
		return
	}
	cookie, err := r.Cookie(name)
	if err != nil || (cookie.Value != "asc" && cookie.Value != "desc") {
		return
	}
	q.Set("order", cookie.Value)
	r.URL.RawQuery = q.Encode()
}

// ensureRandomSeed gives a random sort a seed in the request URL, so every
// pagination link and returnTo of this shuffle shows the same permutation.
// A new shuffle ("换一批") is the same URL without seed.
func ensureRandomSeed(r *http.Request) {
	q := r.URL.Query()
	if q.Get("sort") != "random" {
		return
	}
	if _, err := strconv.ParseInt(q.Get("seed"), 10, 64); err == nil {
		return
	}
	q.Set("seed", strconv.FormatInt(rand.Int64N(1<<31), 10))
	r.URL.RawQuery = q.Encode()
}

// rememberBrowseOrder applies the remembered sort, direction and random seed
// of a library list page.
func rememberBrowseOrder(w http.ResponseWriter, r *http.Request, page string) {
	rememberSort(w, r, page)
	rememberOrder(w, r, page)
	ensureRandomSeed(r)
}

// browseOrderFilters reads the direction and seed that rememberBrowseOrder
// left in the URL.
func browseOrderFilters(r *http.Request, f *storage.Filters) {
	q := r.URL.Query()
	if order := q.Get("order"); storage.ValidBrowseOrder(order) && f.Sort != "random" {
		f.Order = order
	}
	f.Seed, _ = strconv.ParseInt(q.Get("seed"), 10, 64)
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
