package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/storage"
)

// M-new-1：分组头海报必须用 {{with .Poster}} 渲染——{{if .Poster}} 不会
// 重绑定 dot，src 会拿到整个 group 结构体（被 URL 转义后输出）。
func TestSeriesSuggestionGroupHeaderPosterURL(t *testing.T) {
	app, _, _ := credentialTestApp(t)
	data := workReviewPageData{ActiveTab: "series"}
	data.SeriesGroups = []seriesSuggestionGroup{{
		Work:   storage.Work{ID: 7, Title: "海报作品", Type: "anime"},
		Poster: "/admin/works/7/poster?v=abc",
		Total:  2,
	}}
	var buf bytes.Buffer
	if err := app.templates.ExecuteTemplate(&buf, "work-review.html", data); err != nil {
		t.Fatal(err)
	}
	body := buf.String()
	if !strings.Contains(body, `src="/admin/works/7/poster?v=abc"`) {
		t.Fatalf("group header poster src missing or wrong")
	}
	if strings.Contains(body, "ZgotmplZ") {
		t.Fatal("template rendered ZgotmplZ")
	}
	// 无海报时回退占位块。
	data.SeriesGroups[0].Poster = ""
	buf.Reset()
	if err := app.templates.ExecuteTemplate(&buf, "work-review.html", data); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `review-poster-thumb cover-placeholder`) {
		t.Fatal("placeholder missing when poster is empty")
	}
}

// H1（D58）：管理页合并选“自动（按规则）”时，名字交给存储层按 D58 处理；
// 两边都是自动名时保留成员多的一方，跳转到保留下来的系列。
func TestSeriesMergeAutoNamingRedirectToKept(t *testing.T) {
	app, store, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	ctx := context.Background()

	// keep 只有 1 名成员（自动名），drop 有 2 名成员（自动名）：D58 保留 drop。
	ka := batch6Work(t, store, "Auto Keep Solo", "anime", 2019)
	da := batch6Work(t, store, "Auto Drop One", "anime", 2015)
	db := batch6Work(t, store, "Auto Drop Two", "anime", 2017)
	keepID, err := store.CreateWorkSeries(ctx, "", []int64{ka.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{da.ID, db.ID}}); err != nil {
		t.Fatal(err)
	}
	dropSeries, _, err := store.SeriesForWork(ctx, da.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 合并表单默认就是“自动（按规则）”（titleChoice 为空）。
	rec := postSecurity(t, app, handler, cookie, "/admin/series/"+strconv.FormatInt(keepID, 10)+"/merge", url.Values{
		"dropId":      {strconv.FormatInt(dropSeries.ID, 10)},
		"titleChoice": {""},
		"returnTo":    {"/admin/series/" + strconv.FormatInt(keepID, 10)},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("merge code=%d body=%s", rec.Code, rec.Body.String())
	}
	// 跳转目标是保留下来的系列（drop，成员多），而不是 returnTo 指向的被删方。
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/admin/series/"+strconv.FormatInt(dropSeries.ID, 10)+"?notice=") {
		t.Fatalf("redirect=%q, want kept series %d", loc, dropSeries.ID)
	}
	// L-new-1：保留方被交换时提示方向也要反过来（本系列被并入保留方）。
	parsed, _ := url.Parse(loc)
	if notice := parsed.Query().Get("notice"); !strings.Contains(notice, "本系列已并入《Auto Drop One》") {
		t.Fatalf("swap notice=%q", notice)
	}
	kept, err := store.WorkSeriesByID(ctx, dropSeries.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 名字仍然是自动名（跟随代表作），成员全部并入。
	if kept.TitleSource != "auto" || kept.Title != "Auto Drop One" || kept.MemberCount != 3 {
		t.Fatalf("kept series=%+v", kept)
	}
	if _, err := store.WorkSeriesByID(ctx, keepID); err == nil {
		t.Fatal("absorbed series still exists")
	}

	// 只有一边是手动名：titleChoice 为空时存储层沿用该手动名。
	ma := batch6Work(t, store, "Manual Side", "anime", 2018)
	manualID, err := store.CreateWorkSeries(ctx, "手动名的系列", []int64{ma.ID})
	if err != nil {
		t.Fatal(err)
	}
	xa := batch6Work(t, store, "Auto Partner One", "anime", 2016)
	xb := batch6Work(t, store, "Auto Partner Two", "anime", 2020)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{xa.ID, xb.ID}}); err != nil {
		t.Fatal(err)
	}
	autoSeries, _, err := store.SeriesForWork(ctx, xa.ID)
	if err != nil {
		t.Fatal(err)
	}
	rec = postSecurity(t, app, handler, cookie, "/admin/series/"+strconv.FormatInt(manualID, 10)+"/merge", url.Values{
		"dropId":      {strconv.FormatInt(autoSeries.ID, 10)},
		"titleChoice": {""},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("merge one-manual code=%d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/admin/series/"+strconv.FormatInt(manualID, 10)+"?notice=") {
		t.Fatalf("one-manual redirect=%q", loc)
	}
	// 保留方未交换时提示保持“并入”方向。
	parsed, _ = url.Parse(rec.Header().Get("Location"))
	if notice := parsed.Query().Get("notice"); !strings.Contains(notice, "成员已全部并入") {
		t.Fatalf("one-manual notice=%q", notice)
	}
	fresh, err := store.WorkSeriesByID(ctx, manualID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Title != "手动名的系列" || fresh.TitleSource != "manual" || fresh.MemberCount != 3 {
		t.Fatalf("one-manual merged series=%+v", fresh)
	}
}

// 合并表单的命名单选：提供“自动（按规则）”选项；当前系列未改名时默认选中它，
// 已改名时默认选中“保留当前”。
func TestSeriesMergeNamingRadioDefaults(t *testing.T) {
	_, store, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	ctx := context.Background()
	a := batch6Work(t, store, "Radio Auto A", "anime", 2019)
	b := batch6Work(t, store, "Radio Auto B", "anime", 2021)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID}}); err != nil {
		t.Fatal(err)
	}
	series, _, err := store.SeriesForWork(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	body := getAdmin(t, handler, cookie, "/admin/series/"+strconv.FormatInt(series.ID, 10))
	if !strings.Contains(body, "自动（按规则）") || !strings.Contains(body, `value="" checked`) {
		t.Fatal("auto series must default to the 自动（按规则） option")
	}
	if err := store.RenameWorkSeries(ctx, series.ID, "改过的名字"); err != nil {
		t.Fatal(err)
	}
	body = getAdmin(t, handler, cookie, "/admin/series/"+strconv.FormatInt(series.ID, 10))
	if !strings.Contains(body, `value="current" checked`) {
		t.Fatal("manual series must default to 保留当前")
	}
}

// L2：系列建议超过 200 条时提示“仅显示前 200 条”。
func TestSeriesSuggestionTabTruncatedHint(t *testing.T) {
	_, store, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	ctx := context.Background()
	hub := batch6Work(t, store, "Trunc Hub", "anime", 2000)
	var inputs []storage.SeriesSuggestionInput
	var processed []int64
	processed = append(processed, hub.ID)
	for i := 0; i < 201; i++ {
		peer := batch6Work(t, store, "Trunc Peer "+strconv.Itoa(i), "anime", 2001+i)
		inputs = append(inputs, storage.SeriesSuggestionInput{WorkA: hub.ID, WorkB: peer.ID, SubjectA: 9000, SubjectB: int64(9100 + i), RelationAB: "游戏", RelationBA: "动画", Kind: "cross"})
		processed = append(processed, peer.ID)
	}
	if err := store.ReplaceSeriesSuggestions(ctx, 1, inputs, processed); err != nil {
		t.Fatal(err)
	}
	body := getAdmin(t, handler, cookie, "/admin/work-review?tab=series")
	if !strings.Contains(body, "仅显示前 200 条") {
		t.Fatal("truncated hint missing")
	}
	badges := tabBadgeCounts(t, body)
	if len(badges) != 4 || badges[3] != "201" {
		t.Fatalf("badges=%v, want series count 201", badges)
	}
	// 页面只渲染前 200 条。
	if n := strings.Count(body, "series-suggestion-card"); n != 200 {
		t.Fatalf("cards=%d, want 200", n)
	}
}

// L3：withSeries 的移入提示排除本系列（excludeSeries）。
func TestWorkOptionsWithSeriesExcludesCurrent(t *testing.T) {
	_, store, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	ctx := context.Background()
	a := batch6Work(t, store, "Excl Work A", "anime", 2019)
	b := batch6Work(t, store, "Excl Work B", "anime", 2020)
	seriesA, err := store.CreateWorkSeries(ctx, "当前系列", []int64{a.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateWorkSeries(ctx, "别的系列", []int64{b.ID}); err != nil {
		t.Fatal(err)
	}
	q := url.QueryEscape("Excl Work")
	rec := getWithCookie(handler, "/admin/options/works?q="+q+"&withSeries=1&excludeSeries="+strconv.FormatInt(seriesA, 10), cookie)
	body := rec.Body.String()
	// a 已在当前系列：不提示移入；b 在别的系列：提示将从《别的系列》移入。
	if strings.Contains(body, "将从《当前系列》移入") {
		t.Fatalf("current series must be excluded: %s", body)
	}
	if !strings.Contains(body, "将从《别的系列》移入") {
		t.Fatalf("other series hint missing: %s", body)
	}
}

// L4：改名/解散旧 handler 带 returnTo 时给友好提示，不带时保持原行为。
func TestSeriesLegacyHandlersFriendlyErrorsWithReturnTo(t *testing.T) {
	app, store, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	ctx := context.Background()
	a := batch6Work(t, store, "Legacy A", "anime", 2019)
	b := batch6Work(t, store, "Legacy B", "anime", 2021)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID}}); err != nil {
		t.Fatal(err)
	}
	series, _, err := store.SeriesForWork(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	detail := "/admin/series/" + strconv.FormatInt(series.ID, 10)

	// 重命名不存在的系列：带 returnTo → 303 + 友好提示；不带 → 404。
	rec := postSecurity(t, app, handler, cookie, "/admin/series/99999/rename", url.Values{"title": {"X"}, "returnTo": {detail}})
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusSeeOther || !strings.Contains(loc.Query().Get("notice"), "系列不存在") {
		t.Fatalf("rename missing with returnTo: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	rec = postSecurity(t, app, handler, cookie, "/admin/series/99999/rename", url.Values{"title": {"X"}})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("rename missing without returnTo: code=%d, want 404", rec.Code)
	}
	// 重命名为空标题：带 returnTo → 303 + “系列名不能为空”；不带 → 400。
	rec = postSecurity(t, app, handler, cookie, detail+"/rename", url.Values{"title": {""}, "returnTo": {detail}})
	loc, _ = url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusSeeOther || !strings.Contains(loc.Query().Get("notice"), "系列名不能为空") {
		t.Fatalf("rename empty with returnTo: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	rec = postSecurity(t, app, handler, cookie, detail+"/rename", url.Values{"title": {""}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("rename empty without returnTo: code=%d, want 400", rec.Code)
	}
	// 解散不存在的系列：带 returnTo → 303 + 友好提示；不带 → 404。
	rec = postSecurity(t, app, handler, cookie, "/admin/series/99999/dissolve", url.Values{"returnTo": {detail}})
	loc, _ = url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusSeeOther || !strings.Contains(loc.Query().Get("notice"), "系列不存在") {
		t.Fatalf("dissolve missing with returnTo: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	rec = postSecurity(t, app, handler, cookie, "/admin/series/99999/dissolve", url.Values{})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("dissolve missing without returnTo: code=%d, want 404", rec.Code)
	}
}
