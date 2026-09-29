package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/storage"
)

// seriesHTTPFixture builds three grouped works plus one standalone work.
func seriesHTTPFixture(t *testing.T, store *storage.Store) (series storage.WorkSeries, groupedA, groupedB, solo storage.Work) {
	t.Helper()
	ctx := context.Background()
	var err error
	if groupedA, err = store.CreateWork(ctx, storage.WorkInput{Title: "Series One", Type: "anime", Year: 2019}); err != nil {
		t.Fatal(err)
	}
	if groupedB, err = store.CreateWork(ctx, storage.WorkInput{Title: "Series Two", Type: "anime", Year: 2021}); err != nil {
		t.Fatal(err)
	}
	var groupedC storage.Work
	if groupedC, err = store.CreateWork(ctx, storage.WorkInput{Title: "Series Three", Type: "anime", Year: 2023}); err != nil {
		t.Fatal(err)
	}
	if solo, err = store.CreateWork(ctx, storage.WorkInput{Title: "Standalone", Type: "anime", Year: 2020}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ApplyAutoSeries(ctx, 0, [][]int64{{groupedA.ID, groupedB.ID, groupedC.ID}}); err != nil {
		t.Fatal(err)
	}
	series, _, err = store.SeriesForWork(ctx, groupedA.ID)
	if err != nil {
		t.Fatal(err)
	}
	return series, groupedA, groupedB, solo
}

func TestWorksSeriesAuthAndCSRF(t *testing.T) {
	app, store, handler := credentialTestApp(t)
	series, groupedA, _, _ := seriesHTTPFixture(t, store)
	paths := []string{
		"/admin/works/" + strconv.FormatInt(groupedA.ID, 10) + "/series/detach",
		"/admin/works/" + strconv.FormatInt(groupedA.ID, 10) + "/series",
		"/admin/series/" + strconv.FormatInt(series.ID, 10) + "/rename",
		"/admin/series/" + strconv.FormatInt(series.ID, 10) + "/dissolve",
	}
	// Unauthenticated requests bounce to the login page without touching data.
	for _, path := range paths {
		req := httptestRequest(path)
		rec := httptestRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
			t.Fatalf("POST %s unauthenticated: code=%d location=%q", path, rec.Code, rec.Header().Get("Location"))
		}
	}
	if got := seriesMemberCount(t, store, series.ID); got != 3 {
		t.Fatalf("members changed without auth: %d", got)
	}
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	// A bad CSRF token is rejected.
	rec := postForm(handler, cookie, paths[0], url.Values{"csrfToken": {"wrong"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bad csrf: code=%d", rec.Code)
	}
	if got := seriesMemberCount(t, store, series.ID); got != 3 {
		t.Fatalf("members changed with bad csrf: %d", got)
	}
	_ = app
}

func TestWorksSeriesDetachRenameAdd(t *testing.T) {
	app, store, handler := credentialTestApp(t)
	series, groupedA, groupedB, solo := seriesHTTPFixture(t, store)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	workPath := func(id int64) string { return "/admin/works/" + strconv.FormatInt(id, 10) }

	// Detach: membership removed, lock written.
	rec := postSecurity(t, app, handler, cookie, workPath(groupedB.ID)+"/series/detach", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("detach code=%d body=%s", rec.Code, rec.Body.String())
	}
	if locked, _ := store.WorkSeriesLocked(context.Background(), groupedB.ID); !locked {
		t.Fatal("detach did not lock the work")
	}
	if got := seriesMemberCount(t, store, series.ID); got != 2 {
		t.Fatalf("members after detach=%d", got)
	}

	// Rename: title becomes manual.
	rec = postSecurity(t, app, handler, cookie, "/admin/series/"+strconv.FormatInt(series.ID, 10)+"/rename", url.Values{"title": {"用户的系列名"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("rename code=%d body=%s", rec.Code, rec.Body.String())
	}
	fresh, _ := store.WorkSeriesByID(context.Background(), series.ID)
	if fresh.Title != "用户的系列名" || fresh.TitleSource != "manual" {
		t.Fatalf("series after rename=%+v", fresh)
	}

	// Manual add: joins the series and clears the lock.
	rec = postSecurity(t, app, handler, cookie, workPath(groupedB.ID)+"/series", url.Values{"seriesId": {strconv.FormatInt(series.ID, 10)}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("add code=%d body=%s", rec.Code, rec.Body.String())
	}
	if locked, _ := store.WorkSeriesLocked(context.Background(), groupedB.ID); locked {
		t.Fatal("manual add did not clear the lock")
	}
	members, _ := store.SeriesMembers(context.Background(), series.ID)
	sources := map[int64]string{}
	for _, member := range members {
		sources[member.Work.ID] = member.Source
	}
	if sources[groupedB.ID] != "manual" || sources[groupedA.ID] != "auto" {
		t.Fatalf("members=%v", sources)
	}

	// Adding the standalone work to a missing series is a 404.
	rec = postSecurity(t, app, handler, cookie, workPath(solo.ID)+"/series", url.Values{"seriesId": {"9999"}})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("add missing series code=%d", rec.Code)
	}
}

func TestWorksSeriesDissolve(t *testing.T) {
	app, store, handler := credentialTestApp(t)
	series, groupedA, groupedB, _ := seriesHTTPFixture(t, store)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	rec := postSecurity(t, app, handler, cookie, "/admin/series/"+strconv.FormatInt(series.ID, 10)+"/dissolve", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("dissolve code=%d body=%s", rec.Code, rec.Body.String())
	}
	if all, _ := store.ListSeries(context.Background()); len(all) != 0 {
		t.Fatalf("series left=%+v", all)
	}
	for _, id := range []int64{groupedA.ID, groupedB.ID} {
		if locked, _ := store.WorkSeriesLocked(context.Background(), id); !locked {
			t.Fatalf("work %d not locked after dissolve", id)
		}
	}
	// The automatic pass cannot rebuild a dissolved series.
	if _, err := store.ApplyAutoSeries(context.Background(), 0, [][]int64{{groupedA.ID, groupedB.ID}}); err != nil {
		t.Fatal(err)
	}
	if all, _ := store.ListSeries(context.Background()); len(all) != 0 {
		t.Fatalf("series rebuilt=%+v", all)
	}
}

// The works list folds a series into one row and the detail page shows the
// series section with its operations.
func TestWorksPagesRenderSeries(t *testing.T) {
	_, store, handler := credentialTestApp(t)
	series, groupedA, _, solo := seriesHTTPFixture(t, store)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	body := getWithCookie(handler, "/admin/works", cookie).Body.String()
	if !strings.Contains(body, "共 3 部") {
		t.Fatal("folded series row missing")
	}
	if !strings.Contains(body, ">Series One<") || !strings.Contains(body, ">Standalone<") {
		t.Fatal("expected series title and standalone work")
	}
	// The folded list counts rows, not works: 2 rows for 3 works.
	if !strings.Contains(body, "2 个动画、影视或游戏作品") {
		t.Fatal("folded total incorrect")
	}
	// Members are reachable from the expanded row.
	if !strings.Contains(body, "/admin/works/"+strconv.FormatInt(groupedA.ID, 10)) {
		t.Fatal("member link missing")
	}
	// Detail page: series section with members and operations.
	detail := getWithCookie(handler, "/admin/works/"+strconv.FormatInt(groupedA.ID, 10), cookie).Body.String()
	for _, fragment := range []string{"所属系列", "Series One", "series/detach", "/admin/series/" + strconv.FormatInt(series.ID, 10) + "/rename", "/dissolve", "解散后这些作品将不再参与自动归组"} {
		if !strings.Contains(detail, fragment) {
			t.Fatalf("detail page missing %q", fragment)
		}
	}
	// Standalone work page offers joining an existing series.
	standalone := getWithCookie(handler, "/admin/works/"+strconv.FormatInt(solo.ID, 10), cookie).Body.String()
	if !strings.Contains(standalone, "不属于任何系列") || !strings.Contains(standalone, "手动加入系列") {
		t.Fatal("standalone page missing join form")
	}
}

func seriesMemberCount(t *testing.T, store *storage.Store, seriesID int64) int {
	t.Helper()
	members, err := store.SeriesMembers(context.Background(), seriesID)
	if err != nil {
		t.Fatal(err)
	}
	return len(members)
}

func httptestRequest(path string) *http.Request {
	req, _ := http.NewRequest(http.MethodPost, path, strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func httptestRecorder() *httptest.ResponseRecorder {
	return httptest.NewRecorder()
}

func postForm(handler http.Handler, cookie *http.Cookie, path string, form url.Values) *httptest.ResponseRecorder {
	req, _ := http.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// M4: the series handlers honor a safe in-site returnTo and fall back to
// their default target otherwise (batch 6's management page reuses them).
func TestWorksSeriesReturnTo(t *testing.T) {
	app, store, handler := credentialTestApp(t)
	series, groupedA, groupedB, solo := seriesHTTPFixture(t, store)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	workPath := func(id int64) string { return "/admin/works/" + strconv.FormatInt(id, 10) }

	// Detach honors returnTo, including an existing query string.
	rec := postSecurity(t, app, handler, cookie, workPath(groupedB.ID)+"/series/detach", url.Values{"returnTo": {"/admin/work-review?tab=series"}})
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/admin/work-review?tab=series&notice=") {
		t.Fatalf("detach returnTo location=%q", loc)
	}
	// An external or non-admin returnTo is refused; the fallback is the work page.
	rec = postSecurity(t, app, handler, cookie, workPath(solo.ID)+"/series/detach", url.Values{"returnTo": {"https://evil.example/x"}})
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, workPath(solo.ID)+"?notice=") {
		t.Fatalf("detach unsafe returnTo location=%q", loc)
	}
	// Manual add joins the series and returns to the given admin page.
	rec = postSecurity(t, app, handler, cookie, workPath(groupedB.ID)+"/series", url.Values{"seriesId": {strconv.FormatInt(series.ID, 10)}, "returnTo": {"/admin/works?group=series"}})
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/admin/works?group=series&notice=") {
		t.Fatalf("add returnTo location=%q", loc)
	}
	// Rename: returnTo beats the representative-work default.
	rec = postSecurity(t, app, handler, cookie, "/admin/series/"+strconv.FormatInt(series.ID, 10)+"/rename", url.Values{"title": {"改名"}, "returnTo": {"/admin/enrichment"}})
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/admin/enrichment?notice=") {
		t.Fatalf("rename returnTo location=%q", loc)
	}
	// Rename without returnTo keeps the old default: the representative work page.
	rec = postSecurity(t, app, handler, cookie, "/admin/series/"+strconv.FormatInt(series.ID, 10)+"/rename", url.Values{"title": {"再改名"}})
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, workPath(groupedA.ID)+"?notice=") {
		t.Fatalf("rename default location=%q", loc)
	}
	// Dissolve: returnTo honored, default /admin/works preserved otherwise.
	rec = postSecurity(t, app, handler, cookie, "/admin/series/"+strconv.FormatInt(series.ID, 10)+"/dissolve", url.Values{"returnTo": {"/admin/enrichment"}})
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/admin/enrichment?notice=") {
		t.Fatalf("dissolve returnTo location=%q", loc)
	}
	if all, _ := store.ListSeries(context.Background()); len(all) != 0 {
		t.Fatalf("series left=%+v", all)
	}
}
