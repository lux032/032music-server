package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/storage"
)

// batch6Work creates a work with an explicit type for series tests.
func batch6Work(t *testing.T, store *storage.Store, title, typ string, year int) storage.Work {
	t.Helper()
	work, err := store.CreateWork(context.Background(), storage.WorkInput{Title: title, Type: typ, Year: year})
	if err != nil {
		t.Fatal(err)
	}
	return work
}

// batch6Suggestion inserts a suggestion directly (as the generation run
// would) and returns its row id.
func batch6Suggestion(t *testing.T, store *storage.Store, workA, workB storage.Work, subjectA, subjectB int64, relAB, relBA, kind string) int64 {
	t.Helper()
	ctx := context.Background()
	input := storage.SeriesSuggestionInput{WorkA: workA.ID, WorkB: workB.ID, SubjectA: subjectA, SubjectB: subjectB, RelationAB: relAB, RelationBA: relBA, Kind: kind}
	if err := store.ReplaceSeriesSuggestions(ctx, 1, []storage.SeriesSuggestionInput{input}, []int64{workA.ID, workB.ID}); err != nil {
		t.Fatal(err)
	}
	suggestions, err := store.PendingSeriesSuggestions(ctx, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	a, b := workA.ID, workB.ID
	if a > b {
		a, b = b, a
	}
	for _, suggestion := range suggestions {
		if suggestion.WorkA.ID == a && suggestion.WorkB.ID == b {
			return suggestion.ID
		}
	}
	t.Fatalf("suggestion for works %d/%d not found", workA.ID, workB.ID)
	return 0
}

func batch6PendingCount(t *testing.T, store *storage.Store) int {
	t.Helper()
	suggestions, err := store.PendingSeriesSuggestions(context.Background(), 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	return len(suggestions)
}

// assertNoEnumWords 页面上不出现内部枚举值（manual/auto/cross/sequel 必须
// 写成中文说法）。注意 autocomplete="off" 这类 HTML 属性含 "auto"，
// 枚举词按展示形态检查。
func assertNoEnumWords(t *testing.T, body string) {
	t.Helper()
	for _, word := range []string{"manual", "cross", "sequel", "title_source", "kind=", ">auto<", `"auto"`, "'auto'"} {
		if strings.Contains(body, word) {
			t.Fatalf("page contains internal enum word %q", word)
		}
	}
}

func TestSeriesSuggestionTabAuthAndCSRF(t *testing.T) {
	app, store, handler := credentialTestApp(t)
	a := batch6Work(t, store, "Sugg Auth A", "anime", 2019)
	b := batch6Work(t, store, "Sugg Auth B", "game", 2021)
	suggID := batch6Suggestion(t, store, a, b, 11, 22, "游戏", "动画", "cross")

	// 未登录：GET 与 POST 都跳登录页。
	if rec := getWithCookie(handler, "/admin/work-review?tab=series", nil); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
		t.Fatalf("unauthenticated GET: code=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}
	for _, path := range []string{
		"/admin/work-review/series-suggestions/" + strconv.FormatInt(suggID, 10) + "/accept",
		"/admin/work-review/series-suggestions/" + strconv.FormatInt(suggID, 10) + "/reject",
	} {
		rec := postForm(handler, nil, path, url.Values{})
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
			t.Fatalf("unauthenticated POST %s: code=%d location=%q", path, rec.Code, rec.Header().Get("Location"))
		}
	}
	if got := batch6PendingCount(t, store); got != 1 {
		t.Fatalf("suggestion touched without auth: %d", got)
	}
	// 错误 CSRF：403，数据不变。
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	rec := postForm(handler, cookie, "/admin/work-review/series-suggestions/"+strconv.FormatInt(suggID, 10)+"/accept", url.Values{"csrfToken": {"wrong"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bad csrf: code=%d", rec.Code)
	}
	if got := batch6PendingCount(t, store); got != 1 {
		t.Fatalf("suggestion touched with bad csrf: %d", got)
	}
	_ = app
}

// 系列建议 Tab：四种归属的接受效果预览、命名单选、冲突/失效/不存在提示、
// 拒绝后不再出现、角标一致。
func TestSeriesSuggestionTabRenderAndFlows(t *testing.T) {
	app, store, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	ctx := context.Background()

	// 新建：两部作品都不在任何系列。
	newA := batch6Work(t, store, "Sugg New A", "anime", 2019)
	newB := batch6Work(t, store, "Sugg New B", "game", 2021)
	newID := batch6Suggestion(t, store, newA, newB, 11, 22, "游戏", "动画", "cross")

	// 加入：A 已在《加入目标》。
	joinA := batch6Work(t, store, "Sugg Join A", "anime", 2018)
	joinB := batch6Work(t, store, "Sugg Join B", "anime", 2020)
	if _, err := store.CreateWorkSeries(ctx, "加入目标", []int64{joinA.ID}); err != nil {
		t.Fatal(err)
	}
	joinID := batch6Suggestion(t, store, joinA, joinB, 33, 44, "衍生", "主线故事", "cross")

	// 合并（双方都改过名）：必须选择名字。
	mergeA := batch6Work(t, store, "Sugg Merge A", "anime", 2015)
	mergeB := batch6Work(t, store, "Sugg Merge B", "anime", 2017)
	seriesA, _ := store.CreateWorkSeries(ctx, "改名甲", []int64{mergeA.ID})
	seriesB, _ := store.CreateWorkSeries(ctx, "改名乙", []int64{mergeB.ID})
	mergeID := batch6Suggestion(t, store, mergeA, mergeB, 55, 66, "续集", "前传", "sequel")

	// 合并（只有一方改过名）：默认保留该方，不显示单选。
	oneA := batch6Work(t, store, "Sugg One A", "anime", 2010)
	oneB := batch6Work(t, store, "Sugg One B", "anime", 2012)
	if _, err := store.CreateWorkSeries(ctx, "", []int64{oneA.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateWorkSeries(ctx, "唯一手动名", []int64{oneB.ID}); err != nil {
		t.Fatal(err)
	}
	oneID := batch6Suggestion(t, store, oneA, oneB, 77, 88, "番外篇", "主线故事", "cross")

	// 合并（双方都是自动名）：不显示命名选项。
	autoA := batch6Work(t, store, "Sugg Auto A", "anime", 2001)
	autoB := batch6Work(t, store, "Sugg Auto B", "anime", 2003)
	if _, err := store.CreateWorkSeries(ctx, "", []int64{autoA.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateWorkSeries(ctx, "", []int64{autoB.ID}); err != nil {
		t.Fatal(err)
	}
	autoID := batch6Suggestion(t, store, autoA, autoB, 99, 110, "总集篇", "全集", "cross")

	// 拒绝用建议。
	rejA := batch6Work(t, store, "Sugg Reject A", "anime", 2005)
	rejB := batch6Work(t, store, "Sugg Reject B", "anime", 2007)
	rejID := batch6Suggestion(t, store, rejA, rejB, 111, 122, "不同演绎", "不同演绎", "cross")

	// ---- 渲染 ----
	body := getAdmin(t, handler, cookie, "/admin/work-review?tab=series")
	for _, fragment := range []string{
		"系列建议",
		"新建系列（Sugg New A、Sugg New B）",
		"把 Sugg Join B 加入《加入目标》",
		"合并《改名甲》与《改名乙》",
		"动画 ↔ 游戏",
		"前传 ↔ 续集",
		"《Sugg New B》是《Sugg New A》的游戏；《Sugg New A》是《Sugg New B》的动画",
		"《Sugg Merge B》是《Sugg Merge A》的续集；《Sugg Merge A》是《Sugg Merge B》的前传",
		"《Sugg Auto B》是《Sugg Auto A》的总集篇；《Sugg Auto A》是《Sugg Auto B》的全集",
		"《Sugg Reject A》与《Sugg Reject B》互为不同演绎",
		"两个系列都改过名，请选择合并后的名字",
		"保留《改名甲》", "保留《改名乙》", "新名字",
		"合并后保留名字《唯一手动名》",
		"合并后名字按规则自动确定",
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("series tab missing %q", fragment)
		}
	}
	assertNoEnumWords(t, body)
	// 系列建议 Tab 不显示分组切换。
	if strings.Contains(body, "按专辑分组") || strings.Contains(body, "按作品分组") {
		t.Fatal("series tab must not show the grouping toggle")
	}
	// Tab 角标包含系列建议数（与 L-A 口径一致）。
	badges := tabBadgeCounts(t, body)
	if len(badges) != 4 || badges[3] != "6" {
		t.Fatalf("tab badges=%v, want 4 badges with series count 6", badges)
	}
	acceptPath := func(id int64) string {
		return "/admin/work-review/series-suggestions/" + strconv.FormatInt(id, 10) + "/accept"
	}
	rejectPath := func(id int64) string {
		return "/admin/work-review/series-suggestions/" + strconv.FormatInt(id, 10) + "/reject"
	}

	// ---- 接受：新建系列 ----
	rec := postSecurity(t, app, handler, cookie, acceptPath(newID), url.Values{})
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/admin/work-review?tab=series&notice=") {
		t.Fatalf("accept new location=%q", loc)
	}
	members := seriesIDsOfWork(t, store, newA.ID)
	if members[newB.ID] != "manual" || members[newA.ID] != "manual" {
		t.Fatalf("new series members=%v", members)
	}

	// ---- 接受：加入已有系列 ----
	rec = postSecurity(t, app, handler, cookie, acceptPath(joinID), url.Values{})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("accept join code=%d", rec.Code)
	}
	members = seriesIDsOfWork(t, store, joinA.ID)
	if len(members) != 2 || members[joinB.ID] != "manual" {
		t.Fatalf("join series members=%v", members)
	}
	if _, _, err := store.SeriesForWork(ctx, joinA.ID); err != nil {
		t.Fatal(err)
	}

	// ---- 接受：合并需要命名但没给 → 冲突提示，建议保留 ----
	rec = postSecurity(t, app, handler, cookie, acceptPath(mergeID), url.Values{})
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if notice := loc.Query().Get("notice"); !strings.Contains(notice, "两个系列都改过名，请选择合并后的名字") {
		t.Fatalf("conflict notice=%q", notice)
	}
	if got := batch6PendingCount(t, store); got != 4 {
		t.Fatalf("conflict must keep the suggestion, pending=%d", got)
	}

	// ---- 接受：合并选择新名字 ----
	rec = postSecurity(t, app, handler, cookie, acceptPath(mergeID), url.Values{"titleChoice": {"new"}, "title": {"合并后的新系列"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("accept merge code=%d", rec.Code)
	}
	fresh, err := store.WorkSeriesByID(ctx, seriesA)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Title != "合并后的新系列" || fresh.TitleSource != "manual" || fresh.MemberCount != 2 {
		t.Fatalf("merged series=%+v", fresh)
	}
	if _, err := store.WorkSeriesByID(ctx, seriesB); err == nil {
		t.Fatal("drop series still exists")
	}

	// ---- 接受：合并默认保留一方的手动名（隐藏单选值由页面给出）----
	rec = postSecurity(t, app, handler, cookie, acceptPath(oneID), url.Values{"titleChoice": {"b"}, "seriesTitleB": {"唯一手动名"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("accept one-manual code=%d", rec.Code)
	}
	oneSeries, _, err := store.SeriesForWork(ctx, oneA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if oneSeries.Title != "唯一手动名" || oneSeries.MemberCount != 2 {
		t.Fatalf("one-manual merged series=%+v", oneSeries)
	}

	// ---- 接受：双方都自动名，按 D58 自动处理 ----
	rec = postSecurity(t, app, handler, cookie, acceptPath(autoID), url.Values{})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("accept auto code=%d", rec.Code)
	}
	autoSeries, _, err := store.SeriesForWork(ctx, autoA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if autoSeries.MemberCount != 2 || autoSeries.TitleSource != "auto" {
		t.Fatalf("auto merged series=%+v", autoSeries)
	}

	// ---- 失效：接受前作品被锁定 ----
	staleA := batch6Work(t, store, "Sugg Stale A", "anime", 2008)
	staleB := batch6Work(t, store, "Sugg Stale B", "anime", 2009)
	staleID := batch6Suggestion(t, store, staleA, staleB, 133, 144, "游戏", "动画", "cross")
	tmpSeries, _ := store.CreateWorkSeries(ctx, "临时", []int64{staleB.ID})
	if err := store.DissolveWorkSeries(ctx, tmpSeries); err != nil {
		t.Fatal(err)
	}
	rec = postSecurity(t, app, handler, cookie, acceptPath(staleID), url.Values{})
	loc, _ = url.Parse(rec.Header().Get("Location"))
	if notice := loc.Query().Get("notice"); !strings.Contains(notice, "建议已失效，已移除") {
		t.Fatalf("stale notice=%q", notice)
	}
	if got := batch6PendingCount(t, store); got != 1 {
		t.Fatalf("stale suggestion must be removed, pending=%d", got)
	}

	// ---- 不存在：404（不能是 500）----
	for _, path := range []string{acceptPath(99999), rejectPath(99999)} {
		rec = postSecurity(t, app, handler, cookie, path, url.Values{})
		if rec.Code != http.StatusNotFound {
			t.Fatalf("POST %s missing suggestion: code=%d, want 404", path, rec.Code)
		}
	}

	// ---- 拒绝：建议消失且永不再建议 ----
	rec = postSecurity(t, app, handler, cookie, rejectPath(rejID), url.Values{})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("reject code=%d", rec.Code)
	}
	if got := batch6PendingCount(t, store); got != 0 {
		t.Fatalf("reject did not close suggestion, pending=%d", got)
	}
	// 下一轮生成同一对：决定记忆拦住，不会再插入。
	input := storage.SeriesSuggestionInput{WorkA: rejA.ID, WorkB: rejB.ID, SubjectA: 111, SubjectB: 122, RelationAB: "不同演绎", RelationBA: "不同演绎", Kind: "cross"}
	if err := store.ReplaceSeriesSuggestions(ctx, 2, []storage.SeriesSuggestionInput{input}, []int64{rejA.ID, rejB.ID}); err != nil {
		t.Fatal(err)
	}
	if got := batch6PendingCount(t, store); got != 0 {
		t.Fatalf("rejected pair suggested again, pending=%d", got)
	}
	body = getAdmin(t, handler, cookie, "/admin/work-review?tab=series")
	if !strings.Contains(body, "暂无系列建议") {
		t.Fatal("empty state missing after all suggestions handled")
	}
}

// M3：关系标签的自然语言方向——relationAB 是 A 的关系列表里对 B 的标注
// （描述 B），所以句子是“《B》是《A》的 relationAB”；两个方向相同时合为
// 一句“互为”。短胶囊按 RelationBA ↔ RelationAB 排列（各词贴着它描述的作品）。
func TestSeriesRelationSentenceDirection(t *testing.T) {
	wa := storage.Work{ID: 1, Title: "某动画"}
	wb := storage.Work{ID: 2, Title: "某游戏"}
	got := seriesRelationSentence(wa, wb, "游戏", "动画")
	want := "《某游戏》是《某动画》的游戏；《某动画》是《某游戏》的动画"
	if got != want {
		t.Fatalf("sentence=%q, want %q", got, want)
	}
	got = seriesRelationSentence(wa, wb, "续集", "前传")
	want = "《某游戏》是《某动画》的续集；《某动画》是《某游戏》的前传"
	if got != want {
		t.Fatalf("sentence=%q, want %q", got, want)
	}
	got = seriesRelationSentence(wa, wb, "不同演绎", "不同演绎")
	want = "《某动画》与《某游戏》互为不同演绎"
	if got != want {
		t.Fatalf("sentence=%q, want %q", got, want)
	}
}

func seriesIDsOfWork(t *testing.T, store *storage.Store, workID int64) map[int64]string {
	t.Helper()
	series, members, err := store.SeriesForWork(context.Background(), workID)
	if err != nil {
		t.Fatalf("work %d has no series: %v", workID, err)
	}
	if series.ID == 0 {
		t.Fatal("empty series")
	}
	out := map[int64]string{}
	for _, member := range members {
		out[member.Work.ID] = member.Source
	}
	return out
}

// 同一部作品出现在多条建议中时按作品分组显示，避免刷屏。
func TestSeriesSuggestionTabGroupByWork(t *testing.T) {
	_, store, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	hub := batch6Work(t, store, "Fate Hub", "game", 2004)
	peer1 := batch6Work(t, store, "Fate Peer One", "anime", 2006)
	peer2 := batch6Work(t, store, "Fate Peer Two", "anime", 2010)
	batch6Suggestion(t, store, hub, peer1, 11, 22, "游戏", "动画", "cross")
	batch6Suggestion(t, store, hub, peer2, 11, 33, "游戏", "动画", "cross")

	body := getAdmin(t, handler, cookie, "/admin/work-review?tab=series")
	if !strings.Contains(body, "2 条建议涉及本作品") {
		t.Fatalf("group header missing: %s", body)
	}
	if n := strings.Count(body, "series-suggestion-card"); n != 2 {
		t.Fatalf("suggestion cards=%d, want 2", n)
	}
}

// returnTo 只接受站内 /admin 路径。
func TestSeriesSuggestionReturnToSafety(t *testing.T) {
	app, store, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	a := batch6Work(t, store, "Return A", "anime", 2019)
	b := batch6Work(t, store, "Return B", "anime", 2020)
	suggID := batch6Suggestion(t, store, a, b, 11, 22, "游戏", "动画", "cross")
	path := "/admin/work-review/series-suggestions/" + strconv.FormatInt(suggID, 10) + "/reject"

	// 站内路径（含查询串）被尊重。
	rec := postSecurity(t, app, handler, cookie, path, url.Values{"returnTo": {"/admin/series?created=1"}})
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/admin/series?created=1&notice=") {
		t.Fatalf("safe returnTo location=%q", loc)
	}
	// 站外/非 admin 路径回退到系列建议 Tab（每次用新的 subject 对，避免被
	// 决定记忆拦住）。
	for i, evil := range []string{"https://evil.example/x", "//evil.example", "/api/v1/works", "\\evil", "/admin/..\\.."} {
		subject := int64(900 + i)
		suggID = batch6Suggestion(t, store, a, b, subject, subject+50, "游戏", "动画", "cross")
		rec = postSecurity(t, app, handler, cookie, "/admin/work-review/series-suggestions/"+strconv.FormatInt(suggID, 10)+"/reject", url.Values{"returnTo": {evil}})
		if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/admin/work-review?tab=series&notice=") {
			t.Fatalf("unsafe returnTo %q location=%q", evil, loc)
		}
	}
}

// 系列管理列表页：搜索、分页、类型分布、已改名标注、新建入口。
func TestSeriesManagementListPage(t *testing.T) {
	app, store, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	ctx := context.Background()

	a1 := batch6Work(t, store, "List Anime One", "anime", 2019)
	a2 := batch6Work(t, store, "List Anime Two", "anime", 2020)
	g1 := batch6Work(t, store, "List Game", "game", 2021)
	mixed, err := store.CreateWorkSeries(ctx, "命运混合系列", []int64{a1.ID, a2.ID, g1.ID})
	if err != nil {
		t.Fatal(err)
	}
	_ = mixed

	// 未登录 → 303。
	if rec := getWithCookie(handler, "/admin/series", nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("unauthenticated list: code=%d", rec.Code)
	}
	body := getAdmin(t, handler, cookie, "/admin/series")
	for _, fragment := range []string{"系列管理", "命运混合系列", "共 3 部作品", "动画 2 · 游戏 1", "已改名", "新建系列", "/admin/series/" + strconv.FormatInt(mixed, 10)} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("series list missing %q", fragment)
		}
	}
	assertNoEnumWords(t, body)
	// 搜索：成员名也能命中。
	body = getAdmin(t, handler, cookie, "/admin/series?q="+url.QueryEscape("List Game"))
	if !strings.Contains(body, "命运混合系列") {
		t.Fatal("member-name search did not find the series")
	}
	body = getAdmin(t, handler, cookie, "/admin/series?q="+url.QueryEscape("不存在的系列"))
	if strings.Contains(body, "命运混合系列") || !strings.Contains(body, "没有找到系列") {
		t.Fatal("search filter broken")
	}

	// 分页：再建 50 个单成员系列（共 51），第二页只剩 1 行。
	for i := 0; i < 50; i++ {
		w := batch6Work(t, store, "Page Work "+strconv.Itoa(i), "anime", 2000+i)
		if _, err := store.CreateWorkSeries(ctx, "分页系列"+strconv.Itoa(i), []int64{w.ID}); err != nil {
			t.Fatal(err)
		}
	}
	body = getAdmin(t, handler, cookie, "/admin/series")
	if !strings.Contains(body, "共 51 个系列") {
		t.Fatal("total count wrong")
	}
	body = getAdmin(t, handler, cookie, "/admin/series?page=2")
	if n := strings.Count(body, `class="series-list-row"`); n != 1 {
		t.Fatalf("page 2 rows=%d, want 1", n)
	}
	_ = app
}

// 系列管理详情页与全部写操作：新建、加入（移入提示）、移出写锁、合并命名、
// 解散提示、类型分组、鉴权与 CSRF。
func TestSeriesManagementDetailAndActions(t *testing.T) {
	app, store, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	ctx := context.Background()

	// 混合类型系列：2 名自动成员 + 1 名手动成员。
	auto1 := batch6Work(t, store, "Detail Auto One", "anime", 2019)
	auto2 := batch6Work(t, store, "Detail Auto Two", "anime", 2021)
	manual1 := batch6Work(t, store, "Detail Manual Game", "game", 2020)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{auto1.ID, auto2.ID}}); err != nil {
		t.Fatal(err)
	}
	series, _, err := store.SeriesForWork(ctx, auto1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddWorkToSeries(ctx, manual1.ID, series.ID); err != nil {
		t.Fatal(err)
	}
	detailPath := "/admin/series/" + strconv.FormatInt(series.ID, 10)

	// ---- 渲染：多类型分组头、来源、移出按钮、加入/合并/解散区 ----
	body := getAdmin(t, handler, cookie, detailPath)
	for _, fragment := range []string{
		"series-type-group-head",
		">动画</h3>", ">游戏</h3>",
		"自动归组", "手动加入",
		"移出后该作品不再自动归组",
		"加入作品", "合并系列", "解散系列",
		"解散后这些作品将不再参与自动归组",
		"/admin/works/" + strconv.FormatInt(auto1.ID, 10),
		"/members/" + strconv.FormatInt(manual1.ID, 10) + "/remove",
		"/admin/options/works?withSeries=1",
		"/admin/options/series",
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("series detail missing %q", fragment)
		}
	}
	assertNoEnumWords(t, body)

	// 404：不存在的系列。
	if rec := getWithCookie(handler, "/admin/series/99999", cookie); rec.Code != http.StatusNotFound {
		t.Fatalf("missing series detail: code=%d", rec.Code)
	}

	// ---- 新建：至少一部作品；名字可留空（跟随代表作）----
	rec := postSecurity(t, app, handler, cookie, "/admin/series", url.Values{"title": {"无作品系列"}})
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if notice := loc.Query().Get("notice"); !strings.Contains(notice, "至少需要一部作品") {
		t.Fatalf("empty create notice=%q", notice)
	}
	newWork := batch6Work(t, store, "Create Follow Me", "anime", 2022)
	rec = postSecurity(t, app, handler, cookie, "/admin/series", url.Values{"workId": {strconv.FormatInt(newWork.ID, 10)}})
	loc, _ = url.Parse(rec.Header().Get("Location"))
	newSeriesID := parseInt64(strings.TrimPrefix(loc.Path, "/admin/series/"))
	if newSeriesID == 0 {
		t.Fatalf("create location=%q", loc)
	}
	created, err := store.WorkSeriesByID(ctx, newSeriesID)
	if err != nil {
		t.Fatal(err)
	}
	if created.Title != "Create Follow Me" || created.TitleSource != "auto" {
		t.Fatalf("created series=%+v", created)
	}

	// ---- 加入：从别的系列移入会有提示 ----
	rec = postSecurity(t, app, handler, cookie, detailPath+"/members", url.Values{"workId": {strconv.FormatInt(newWork.ID, 10)}})
	loc, _ = url.Parse(rec.Header().Get("Location"))
	if notice := loc.Query().Get("notice"); !strings.Contains(notice, "已从《Create Follow Me》移入本系列") {
		t.Fatalf("move notice=%q", notice)
	}
	members := seriesIDsOfWork(t, store, auto1.ID)
	if members[newWork.ID] != "manual" || len(members) != 4 {
		t.Fatalf("members after move=%v", members)
	}
	// 原单成员系列被退化清理。
	if _, err := store.WorkSeriesByID(ctx, newSeriesID); err == nil {
		t.Fatal("degenerate source series not cleaned")
	}

	// ---- 移出：写锁 + 提示 ----
	rec = postSecurity(t, app, handler, cookie, detailPath+"/members/"+strconv.FormatInt(newWork.ID, 10)+"/remove", url.Values{})
	loc, _ = url.Parse(rec.Header().Get("Location"))
	if notice := loc.Query().Get("notice"); !strings.Contains(notice, "不再参与自动归组") {
		t.Fatalf("remove notice=%q", notice)
	}
	if locked, _ := store.WorkSeriesLocked(ctx, newWork.ID); !locked {
		t.Fatal("remove did not write the lock")
	}
	if got := seriesMemberCount(t, store, series.ID); got != 3 {
		t.Fatalf("members after remove=%d", got)
	}
	// 不在本系列的作品：友好提示，不误拆别的系列。
	rec = postSecurity(t, app, handler, cookie, detailPath+"/members/"+strconv.FormatInt(newWork.ID, 10)+"/remove", url.Values{})
	loc, _ = url.Parse(rec.Header().Get("Location"))
	if notice := loc.Query().Get("notice"); !strings.Contains(notice, "不在任何系列") {
		t.Fatalf("remove non-member notice=%q", notice)
	}

	// ---- 合并：命名规则（保留当前/保留被并入/新名字）----
	// 先改名让双方都变成手动名，再验证三种选择。
	if rec := postSecurity(t, app, handler, cookie, "/admin/series/"+strconv.FormatInt(series.ID, 10)+"/rename", url.Values{"title": {"当前名"}, "returnTo": {detailPath}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("rename code=%d", rec.Code)
	}
	// 保留当前名
	dropA := batch6Work(t, store, "Merge Drop A", "anime", 2016)
	dropSeries, _ := store.CreateWorkSeries(ctx, "被并入名", []int64{dropA.ID})
	rec = postSecurity(t, app, handler, cookie, detailPath+"/merge", url.Values{"dropId": {strconv.FormatInt(dropSeries, 10)}, "titleChoice": {"current"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("merge code=%d", rec.Code)
	}
	fresh, _ := store.WorkSeriesByID(ctx, series.ID)
	if fresh.Title != "当前名" || fresh.MemberCount != 4 {
		t.Fatalf("series after merge current=%+v", fresh)
	}
	members = seriesIDsOfWork(t, store, auto1.ID)
	if members[dropA.ID] != "manual" {
		t.Fatalf("merged member source=%q", members[dropA.ID])
	}
	// 保留被并入系列的名字
	dropB := batch6Work(t, store, "Merge Drop B", "anime", 2017)
	dropSeries2, _ := store.CreateWorkSeries(ctx, "第二个被并入", []int64{dropB.ID})
	rec = postSecurity(t, app, handler, cookie, detailPath+"/merge", url.Values{"dropId": {strconv.FormatInt(dropSeries2, 10)}, "titleChoice": {"drop"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("merge drop-name code=%d", rec.Code)
	}
	fresh, _ = store.WorkSeriesByID(ctx, series.ID)
	if fresh.Title != "第二个被并入" {
		t.Fatalf("series after merge drop=%+v", fresh)
	}
	// 新名字
	dropC := batch6Work(t, store, "Merge Drop C", "anime", 2018)
	dropSeries3, _ := store.CreateWorkSeries(ctx, "第三个被并入", []int64{dropC.ID})
	rec = postSecurity(t, app, handler, cookie, detailPath+"/merge", url.Values{"dropId": {strconv.FormatInt(dropSeries3, 10)}, "titleChoice": {"new"}, "title": {"最终命名"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("merge new-name code=%d", rec.Code)
	}
	fresh, _ = store.WorkSeriesByID(ctx, series.ID)
	if fresh.Title != "最终命名" || fresh.MemberCount != 6 {
		t.Fatalf("series after merge new=%+v", fresh)
	}
	// 合并到自己：友好提示。
	rec = postSecurity(t, app, handler, cookie, detailPath+"/merge", url.Values{"dropId": {strconv.FormatInt(series.ID, 10)}, "titleChoice": {"current"}})
	loc, _ = url.Parse(rec.Header().Get("Location"))
	if notice := loc.Query().Get("notice"); !strings.Contains(notice, "不能") {
		t.Fatalf("self merge notice=%q", notice)
	}

	// ---- 解散：回到列表，全体成员写锁 ----
	rec = postSecurity(t, app, handler, cookie, "/admin/series/"+strconv.FormatInt(series.ID, 10)+"/dissolve", url.Values{"returnTo": {"/admin/series"}})
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/admin/series?notice=") {
		t.Fatalf("dissolve location=%q", loc)
	}
	for _, id := range []int64{auto1.ID, auto2.ID, manual1.ID} {
		if locked, _ := store.WorkSeriesLocked(ctx, id); !locked {
			t.Fatalf("work %d not locked after dissolve", id)
		}
	}

	// ---- 鉴权与 CSRF ----
	for _, path := range []string{"/admin/series", detailPath + "/members", detailPath + "/merge", detailPath + "/members/1/remove"} {
		rec := postForm(handler, nil, path, url.Values{})
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
			t.Fatalf("unauthenticated POST %s: code=%d", path, rec.Code)
		}
		rec = postForm(handler, cookie, path, url.Values{"csrfToken": {"wrong"}})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("bad csrf POST %s: code=%d", path, rec.Code)
		}
	}
}

// 单一类型的系列在详情页/展开区/作品页都不显示类型分组头（D51）。
func TestSeriesTypeGroupingSingleTypeHidesHeader(t *testing.T) {
	_, store, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	ctx := context.Background()
	a := batch6Work(t, store, "Single Type A", "anime", 2019)
	b := batch6Work(t, store, "Single Type B", "anime", 2021)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID}}); err != nil {
		t.Fatal(err)
	}
	series, _, err := store.SeriesForWork(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 详情页：无分组头。
	body := getAdmin(t, handler, cookie, "/admin/series/"+strconv.FormatInt(series.ID, 10))
	if strings.Contains(body, "series-type-group-head") {
		t.Fatal("single-type series detail must not show group headers")
	}
	// 作品页系列条：无分组头。
	body = getAdmin(t, handler, cookie, "/admin/works/"+strconv.FormatInt(a.ID, 10))
	if strings.Contains(body, "series-type-group-head") {
		t.Fatal("single-type work page series bar must not show group headers")
	}
}

// 作品页只平铺作品本身：系列成员不折叠、不插入系列条，类型筛选按作品计数。
func TestWorksPageListsWorksWithoutSeriesRows(t *testing.T) {
	_, store, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	ctx := context.Background()
	anime1 := batch6Work(t, store, "Filter Anime One", "anime", 2019)
	movie := batch6Work(t, store, "Filter Movie", "movie", 2021)
	anime2 := batch6Work(t, store, "Filter Anime Two", "anime", 2023)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{anime1.ID, movie.ID, anime2.ID}}); err != nil {
		t.Fatal(err)
	}
	gridOf := func(body string) string {
		if start := strings.Index(body, `class="works-grid"`); start >= 0 {
			body = body[start:]
		}
		if end := strings.Index(body, "</section>"); end >= 0 {
			body = body[:end]
		}
		return body
	}
	body := getAdmin(t, handler, cookie, "/admin/works")
	grid := gridOf(body)
	for _, fragment := range []string{"works-series-row", "series-badge", "works-member-card"} {
		if strings.Contains(grid, fragment) {
			t.Fatalf("works grid must not render series rows, found %q", fragment)
		}
	}
	if n := strings.Count(grid, `class="work-card"`); n != 3 {
		t.Fatalf("work cards=%d, want 3", n)
	}
	if !strings.Contains(body, "共 3 个动画、影视或游戏作品") {
		t.Fatal("total must count individual works")
	}
	body = getAdmin(t, handler, cookie, "/admin/works?type=movie")
	grid = gridOf(body)
	if n := strings.Count(grid, `class="work-card"`); n != 1 || !strings.Contains(grid, "Filter Movie") {
		t.Fatalf("type filter should list only the movie, cards=%d", n)
	}
	if !strings.Contains(body, "共 1 个动画、影视或游戏作品") {
		t.Fatal("filtered total incorrect")
	}
}

// 作品详情页：系列条按类型分组（多类型时）并提供“在系列管理页打开”链接。
func TestWorkDetailSeriesBarGroupsAndManageLink(t *testing.T) {
	_, store, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	ctx := context.Background()
	a := batch6Work(t, store, "Bar Anime", "anime", 2019)
	g := batch6Work(t, store, "Bar Game", "game", 2021)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, g.ID}}); err != nil {
		t.Fatal(err)
	}
	series, _, err := store.SeriesForWork(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	body := getAdmin(t, handler, cookie, "/admin/works/"+strconv.FormatInt(a.ID, 10))
	for _, fragment := range []string{"series-type-group-head", "在系列管理页打开", "/admin/series/" + strconv.FormatInt(series.ID, 10)} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("work detail series bar missing %q", fragment)
		}
	}
}

// 系列搜索自动补全：鉴权、过滤、上限 10、标签含成员数。
func TestSeriesOptionsEndpoint(t *testing.T) {
	_, store, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	ctx := context.Background()
	for i := 0; i < 12; i++ {
		w := batch6Work(t, store, "Option Work "+strconv.Itoa(i), "anime", 2000+i)
		if _, err := store.CreateWorkSeries(ctx, "选项系列"+strconv.Itoa(i), []int64{w.ID}); err != nil {
			t.Fatal(err)
		}
	}
	// 未登录：401 JSON。
	if rec := getWithCookie(handler, "/admin/options/series?q=选项", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated options: code=%d", rec.Code)
	}
	rec := getWithCookie(handler, "/admin/options/series?q="+url.QueryEscape("选项系列")+"&limit=50", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("options code=%d", rec.Code)
	}
	body := rec.Body.String()
	if n := strings.Count(body, `"id":`); n != 10 {
		t.Fatalf("options returned %d items, want clamp 10", n)
	}
	if !strings.Contains(body, "（共 1 部）") {
		t.Fatalf("option label missing member count: %s", body)
	}
	rec = getWithCookie(handler, "/admin/options/series?q="+url.QueryEscape("不存在"), cookie)
	if strings.Contains(rec.Body.String(), `"id":`) {
		t.Fatal("options filter broken")
	}
}

// 加入作品搜索的移入提示：withSeries=1 时标签标出当前所在系列。
func TestWorkOptionsWithSeriesHint(t *testing.T) {
	_, store, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	ctx := context.Background()
	a := batch6Work(t, store, "Hint Work", "anime", 2019)
	if _, err := store.CreateWorkSeries(ctx, "原系列", []int64{a.ID}); err != nil {
		t.Fatal(err)
	}
	rec := getWithCookie(handler, "/admin/options/works?q="+url.QueryEscape("Hint Work")+"&withSeries=1", cookie)
	if !strings.Contains(rec.Body.String(), "将从《原系列》移入") {
		t.Fatalf("withSeries hint missing: %s", rec.Body.String())
	}
	rec = getWithCookie(handler, "/admin/options/works?q="+url.QueryEscape("Hint Work"), cookie)
	if strings.Contains(rec.Body.String(), "移入") {
		t.Fatal("hint must only appear with withSeries=1")
	}
}

// 角标一致性（L-A）：锁定作品的建议不计入 Tab 角标与导航角标。
func TestSeriesSuggestionBadgeMatchesLockFilter(t *testing.T) {
	_, store, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	ctx := context.Background()
	a := batch6Work(t, store, "Badge A", "anime", 2019)
	b := batch6Work(t, store, "Badge B", "anime", 2020)
	batch6Suggestion(t, store, a, b, 11, 22, "游戏", "动画", "cross")
	body := getAdmin(t, handler, cookie, "/admin/work-review?tab=series")
	if badges := tabBadgeCounts(t, body); len(badges) != 4 || badges[3] != "1" {
		t.Fatalf("badges=%v", badges)
	}
	if !strings.Contains(body, `nav-badge">1<`) {
		t.Fatal("nav badge must include the series suggestion")
	}
	// 锁定 b：行还在，但两个角标都归零。
	tmp, _ := store.CreateWorkSeries(ctx, "临时", []int64{b.ID})
	if err := store.DissolveWorkSeries(ctx, tmp); err != nil {
		t.Fatal(err)
	}
	body = getAdmin(t, handler, cookie, "/admin/work-review?tab=series")
	if badges := tabBadgeCounts(t, body); len(badges) != 4 || badges[3] != "0" {
		t.Fatalf("badges after lock=%v", badges)
	}
	if strings.Contains(body, "nav-badge") {
		t.Fatal("nav badge must disappear when nothing is pending")
	}
	if !strings.Contains(body, "暂无系列建议") {
		t.Fatal("locked suggestion must be hidden from the list")
	}
}
