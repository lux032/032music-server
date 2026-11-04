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

func batchItem(kind string, ids ...int64) string {
	parts := []string{kind}
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return strings.Join(parts, ":")
}

func batchNotice(t *testing.T, rec interface {
	Result() *http.Response
}) (string, string) {
	t.Helper()
	res := rec.Result()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("batch status=%d", res.StatusCode)
	}
	loc := res.Header.Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	return loc, u.Query().Get("notice")
}

func pendingAlbumCandidateIDs(t *testing.T, app *App, albumID int64) []int64 {
	t.Helper()
	cands, err := app.store.AlbumSubjectCandidates(context.Background(), albumID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, c := range cands {
		if c.Status == "candidate" {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

// Issue #3：批量接受专辑候选，未改动勾选的卡片等同“接受全部”，只勾一部作品
// 的卡片只写这一部；批量拒绝写抑制；每条决定与单卡按钮同一存储路径。
func TestWorkReviewBatchAlbumsAcceptAndReject(t *testing.T) {
	app, cookie, csrf, albumOne, albumTwo, _ := setupBatch3HTTPFixture(t)
	handler := app.Handler()
	ctx := context.Background()
	if err := app.store.SaveAlbumSubjectCandidates(ctx, albumOne, []storage.AlbumSubjectCandidate{{
		ExternalID: "900001", Title: "Batch One", Score: 90,
		Tieups: []storage.BangumiTieup{
			{SubjectID: 9101, Title: "Batch Anime A", Type: "anime", Role: "op"},
			{SubjectID: 9102, Title: "Batch Anime B", Type: "anime", Role: "ed"},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := app.store.SaveAlbumSubjectCandidates(ctx, albumTwo, []storage.AlbumSubjectCandidate{{
		ExternalID: "900002", Title: "Batch Two", Score: 80,
		Tieups: []storage.BangumiTieup{{SubjectID: 9201, Title: "Batch Anime C", Type: "anime", Role: "op"}},
	}}); err != nil {
		t.Fatal(err)
	}
	one := pendingAlbumCandidateIDs(t, app, albumOne)[0]
	two := pendingAlbumCandidateIDs(t, app, albumTwo)[0]

	// 页面渲染批量栏与每张卡片的复选框。
	body := getAdmin(t, handler, cookie, "/admin/work-review?tab=albums")
	for _, want := range []string{`id="work-review-batch"`, `action="/admin/work-review/batch"`, `value="` + batchItem("album", albumOne, one) + `"`, `value="` + batchItem("album", albumTwo, two) + `"`, "已选 0 / 2 条", "批量拒绝并抑制"} {
		if !strings.Contains(body, want) {
			t.Fatalf("albums tab missing %q", want)
		}
	}

	// 错误 CSRF：403，不改数据。
	rec := postForm(handler, cookie, "/admin/work-review/batch", url.Values{"csrfToken": {"wrong"}, "decision": {"accept"}, "item": {batchItem("album", albumOne, one)}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bad csrf code=%d", rec.Code)
	}
	// 未登录：跳登录页。
	if rec = postForm(handler, nil, "/admin/work-review/batch", url.Values{}); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
		t.Fatalf("unauthenticated code=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}

	// 卡片一只勾了 9102；卡片二没带状态（等同接受全部）。
	stateOne := url.Values{"workSubjectIds": {"9102"}}.Encode()
	rec = postForm(handler, cookie, "/admin/work-review/batch", url.Values{
		"csrfToken": {csrf},
		"decision":  {"accept"},
		"returnTo":  {"/admin/work-review?tab=albums&group=album"},
		"item":      {batchItem("album", albumOne, one), batchItem("album", albumTwo, two)},
		"state-" + batchItem("album", albumOne, one): {stateOne},
	})
	loc, notice := batchNotice(t, rec)
	if !strings.HasPrefix(loc, "/admin/work-review?tab=albums&group=album&notice=") || notice != "已接受 2 条" {
		t.Fatalf("accept location=%q notice=%q", loc, notice)
	}
	if got := httpCount(t, app.store, `SELECT COUNT(*) FROM album_works aw JOIN works w ON w.id=aw.work_id WHERE aw.album_id=? AND aw.source='bangumi'`, albumOne); got != 1 {
		t.Fatalf("album one bangumi links=%d, want only the selected work", got)
	}
	if got := httpCount(t, app.store, `SELECT COUNT(*) FROM album_works WHERE album_id=? AND source='bangumi'`, albumTwo); got != 1 {
		t.Fatalf("album two bangumi links=%d, want 1", got)
	}
	if len(pendingAlbumCandidateIDs(t, app, albumOne))+len(pendingAlbumCandidateIDs(t, app, albumTwo)) != 0 {
		t.Fatal("accepted candidates still pending")
	}

	// 再处理同一批：都已不再待审，汇总为未处理而不是 500。
	rec = postForm(handler, cookie, "/admin/work-review/batch", url.Values{"csrfToken": {csrf}, "decision": {"reject"}, "item": {batchItem("album", albumOne, one)}})
	if _, notice = batchNotice(t, rec); notice != "已拒绝 0 条，1 条未处理（候选已处理过）" {
		t.Fatalf("repeat notice=%q", notice)
	}

	// 批量拒绝：写入整条目抑制。
	if err := app.store.SaveAlbumSubjectCandidates(ctx, albumTwo, []storage.AlbumSubjectCandidate{{ExternalID: "900003", Title: "Batch Three", Score: 70}}); err != nil {
		t.Fatal(err)
	}
	three := pendingAlbumCandidateIDs(t, app, albumTwo)[0]
	rec = postForm(handler, cookie, "/admin/work-review/batch", url.Values{"csrfToken": {csrf}, "decision": {"reject"}, "item": {batchItem("album", albumTwo, three), batchItem("album", albumTwo, three)}})
	if _, notice = batchNotice(t, rec); notice != "已拒绝 1 条" {
		t.Fatalf("reject notice=%q (duplicates must collapse)", notice)
	}
	if got := httpCount(t, app.store, `SELECT COUNT(*) FROM album_work_suppressions WHERE album_id=? AND inferred_key='bangumi:900003'`, albumTwo); got != 1 {
		t.Fatalf("reject suppression=%d", got)
	}
}

// 曲目候选：带状态时按勾选与改过的角色写入；没有状态时按卡片默认值（全部作品）。
func TestWorkReviewBatchTracksHonourCardState(t *testing.T) {
	app, cookie, csrf, _, _, trackIDs := setupBatch3HTTPFixture(t)
	handler := app.Handler()
	ctx := context.Background()
	for i, trackID := range trackIDs[:2] {
		if err := app.store.SaveTrackSubjectCandidates(ctx, trackID, []storage.TrackSubjectCandidate{{
			ExternalID: "91000" + strconv.Itoa(i), Title: "Batch Track " + strconv.Itoa(i), MatchKind: "multi_title",
			Tieups: []storage.BangumiTieup{{SubjectID: int64(9300 + i), Title: "Batch Track Anime " + strconv.Itoa(i), Type: "anime", Role: "op"}},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	cands, err := app.store.PendingTrackSubjectCandidates(ctx, 0, 10)
	if err != nil || len(cands) != 2 {
		t.Fatalf("track candidates: %v %d", err, len(cands))
	}
	body := getAdmin(t, handler, cookie, "/admin/work-review?tab=tracks")
	if !strings.Contains(body, `value="`+batchItem("track", cands[0].TrackID, cands[0].ID)+`"`) || !strings.Contains(body, "已选 0 / 2 条") {
		t.Fatal("tracks tab missing batch selection")
	}
	first := batchItem("track", cands[0].TrackID, cands[0].ID)
	second := batchItem("track", cands[1].TrackID, cands[1].ID)
	sub := strconv.FormatInt(cands[0].Tieups[0].SubjectID, 10)
	rec := postForm(handler, cookie, "/admin/work-review/batch", url.Values{
		"csrfToken":      {csrf},
		"decision":       {"accept"},
		"item":           {first, second},
		"state-" + first: {url.Values{"workSubjectIds": {sub}, "role-" + sub: {"ed"}}.Encode()},
	})
	loc, notice := batchNotice(t, rec)
	if notice != "已接受 2 条" || !strings.HasPrefix(loc, "/admin/work-review?tab=tracks") {
		t.Fatalf("track accept location=%q notice=%q", loc, notice)
	}
	if got := httpCount(t, app.store, `SELECT COUNT(*) FROM work_tracks WHERE track_id=? AND role='ed' AND source='manual'`, cands[0].TrackID); got != 1 {
		t.Fatalf("overridden role not stored as manual ed: %d", got)
	}
	if got := httpCount(t, app.store, `SELECT COUNT(*) FROM work_tracks WHERE track_id=? AND role='op' AND source='bangumi'`, cands[1].TrackID); got != 1 {
		t.Fatalf("default track accept missing bangumi op: %d", got)
	}

	// 带状态但没有勾选任何作品：这一条不处理并给出原因。
	if err := app.store.SaveTrackSubjectCandidates(ctx, trackIDs[2], []storage.TrackSubjectCandidate{{
		ExternalID: "910009", Title: "Batch Track Empty", MatchKind: "multi_title",
		Tieups: []storage.BangumiTieup{{SubjectID: 9309, Title: "Batch Track Anime 9", Type: "anime", Role: "op"}},
	}}); err != nil {
		t.Fatal(err)
	}
	cands, _ = app.store.PendingTrackSubjectCandidates(ctx, 0, 10)
	empty := batchItem("track", cands[0].TrackID, cands[0].ID)
	rec = postForm(handler, cookie, "/admin/work-review/batch", url.Values{"csrfToken": {csrf}, "decision": {"accept"}, "item": {empty}, "state-" + empty: {""}})
	if _, notice = batchNotice(t, rec); notice != "已接受 0 条，1 条未处理（请至少选择一部作品）" {
		t.Fatalf("empty selection notice=%q", notice)
	}
}

// 系列建议：批量接受各自按卡片命名；双方都改过名且没有选择时只这一条失败。
func TestWorkReviewBatchSeriesSuggestions(t *testing.T) {
	app, store, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	ctx := context.Background()
	newA := batch6Work(t, store, "Batch New A", "anime", 2019)
	newB := batch6Work(t, store, "Batch New B", "game", 2021)
	newID := batch6Suggestion(t, store, newA, newB, 11, 22, "游戏", "动画", "cross")
	mergeA := batch6Work(t, store, "Batch Merge A", "anime", 2015)
	mergeB := batch6Work(t, store, "Batch Merge B", "anime", 2017)
	if _, err := store.CreateWorkSeries(ctx, "批量甲", []int64{mergeA.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateWorkSeries(ctx, "批量乙", []int64{mergeB.ID}); err != nil {
		t.Fatal(err)
	}
	mergeID := batch6Suggestion(t, store, mergeA, mergeB, 55, 66, "续集", "前传", "sequel")
	rejA := batch6Work(t, store, "Batch Reject A", "anime", 2005)
	rejB := batch6Work(t, store, "Batch Reject B", "anime", 2007)
	rejID := batch6Suggestion(t, store, rejA, rejB, 111, 122, "不同演绎", "不同演绎", "cross")

	body := getAdmin(t, handler, cookie, "/admin/work-review?tab=series")
	if !strings.Contains(body, `value="series:`+strconv.FormatInt(newID, 10)+`"`) || !strings.Contains(body, "已选 0 / 3 条") || !strings.Contains(body, ">批量拒绝<") {
		t.Fatal("series tab missing batch selection")
	}

	newItem, mergeItem := batchItem("series", newID), batchItem("series", mergeID)
	rec := postSecurity(t, app, handler, cookie, "/admin/work-review/batch", url.Values{
		"decision":           {"accept"},
		"returnTo":           {"/admin/work-review?tab=series"},
		"item":               {newItem, mergeItem},
		"state-" + mergeItem: {url.Values{"seriesTitleA": {"批量甲"}, "seriesTitleB": {"批量乙"}}.Encode()},
	})
	if _, notice := batchNotice(t, rec); notice != "已接受 1 条，1 条未处理（两个系列都改过名，请选择合并后的名字）" {
		t.Fatalf("series accept notice=%q", notice)
	}
	if members := seriesIDsOfWork(t, store, newA.ID); members[newB.ID] == "" {
		t.Fatalf("new suggestion not applied: %v", members)
	}
	rec = postSecurity(t, app, handler, cookie, "/admin/work-review/batch", url.Values{
		"decision":           {"accept"},
		"item":               {mergeItem},
		"state-" + mergeItem: {url.Values{"titleChoice": {"b"}, "seriesTitleA": {"批量甲"}, "seriesTitleB": {"批量乙"}}.Encode()},
	})
	if loc, notice := batchNotice(t, rec); notice != "已接受 1 条" || !strings.HasPrefix(loc, "/admin/work-review?tab=series") {
		t.Fatalf("merge accept location=%q notice=%q", loc, notice)
	}
	rec = postSecurity(t, app, handler, cookie, "/admin/work-review/batch", url.Values{"decision": {"reject"}, "item": {batchItem("series", rejID)}})
	if _, notice := batchNotice(t, rec); notice != "已拒绝 1 条" {
		t.Fatalf("series reject notice=%q", notice)
	}
	if got := batch6PendingCount(t, store); got != 0 {
		t.Fatalf("pending suggestions=%d, want 0", got)
	}
}

func TestWorkReviewBatchRejectsMalformedInput(t *testing.T) {
	app, store, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	_ = store
	for _, tc := range []struct {
		form url.Values
		want string
	}{
		{url.Values{"decision": {"accept"}}, "请先勾选要审核的候选"},
		{url.Values{"decision": {"merge"}, "item": {"series:1"}}, "未知的批量操作"},
		{url.Values{"decision": {"accept"}, "item": {"album:1"}}, "批量审核参数无效"},
		{url.Values{"decision": {"accept"}, "item": {"work:1:2"}}, "批量审核参数无效"},
		{url.Values{"decision": {"accept"}, "item": {"series:-3"}}, "批量审核参数无效"},
		{url.Values{"decision": {"reject"}, "item": {"series:999999"}}, "已拒绝 0 条，1 条未处理（候选已不存在）"},
		{url.Values{"decision": {"reject"}, "item": {"series:1"}, "returnTo": {"https://evil.example/x"}}, ""},
	} {
		rec := postSecurity(t, app, handler, cookie, "/admin/work-review/batch", tc.form)
		loc, notice := batchNotice(t, rec)
		if !strings.HasPrefix(loc, "/admin/work-review") {
			t.Fatalf("form %v redirected to %q", tc.form, loc)
		}
		if tc.want != "" && notice != tc.want {
			t.Fatalf("form %v notice=%q, want %q", tc.form, notice, tc.want)
		}
	}
	items := url.Values{"decision": {"reject"}}
	for i := 1; i <= maxWorkReviewBatch+1; i++ {
		items.Add("item", "series:"+strconv.Itoa(i))
	}
	rec := postSecurity(t, app, handler, cookie, "/admin/work-review/batch", items)
	if _, notice := batchNotice(t, rec); notice != "一次最多审核 200 条" {
		t.Fatalf("over-limit notice=%q", notice)
	}
}

// 作品对齐 Tab 不提供批量（每部作品只能确认一个条目，需要逐个判断）。
func TestWorkReviewBatchBarHiddenOnWorksTab(t *testing.T) {
	app, cookie, _, _, _, _ := setupBatch3HTTPFixture(t)
	body := getAdmin(t, app.Handler(), cookie, "/admin/work-review?tab=works")
	if strings.Contains(body, "work-review-batch") {
		t.Fatal("works tab must not render the batch bar")
	}
	body = getAdmin(t, app.Handler(), cookie, "/admin/work-review?tab=albums")
	if strings.Contains(body, "work-review-batch") {
		t.Fatal("batch bar must be hidden when there is nothing to review")
	}
}
