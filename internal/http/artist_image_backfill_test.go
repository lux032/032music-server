package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

// 页面区块：统计、按钮、活动卡片（暂停表单）、限流倒计时文案、最近任务。
func TestMatchReviewPageImageBackfillUI(t *testing.T) {
	_, s, handler, _, cookie, _ := rateLimitTestApp(t)
	ctx := context.Background()
	artists, err := s.ArtistsForMatching(ctx)
	if err != nil || len(artists) == 0 {
		t.Fatalf("artists=%v err=%v", artists, err)
	}
	if err = s.UpsertExternalArtistProfile(ctx, artists[0].ID, storage.ExternalArtistProfile{Source: "lastfm", ExternalID: "lf-1", DisplayName: "Artist"}); err != nil {
		t.Fatal(err)
	}
	run, err := s.CreateArtistImageBackfillRun(ctx, []storage.ArtistImageBackfillCandidate{{ID: artists[0].ID, Name: artists[0].Name}})
	if err != nil {
		t.Fatal(err)
	}
	item, err := s.ClaimArtistImageBackfillItem(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	cp := storage.ArtistImageBackfillCheckpoint{RunID: run, ItemID: item.ID, ClaimToken: item.ClaimToken}
	deadline := time.Now().Add(3 * time.Minute).UTC()
	for i := 0; i < 3; i++ {
		if e := s.RecordArtistImageBackfillWait(ctx, cp, "artist image", deadline, 0, true); e != nil && i < 2 {
			t.Fatal(e)
		}
	}
	body := adminGetBody(t, handler, cookie, "/admin/matches")
	for _, want := range []string{
		"已匹配艺术家头像补全",
		"已确认身份的艺术家 1 位",
		"待补 1",
		`data-active-url="/admin/matches/images/runs/active.json"`,
		`data-run-kind="artistImage"`,
		"头像补全任务 #" + strconv.FormatInt(run, 10),
		"已暂停",
		"暂停原因：同一对象连续限流",
		"自动继续",
		"处理 0 / 1 · 已缓存 0 · 无地址 0 · 跳过 0 · 失败 0",
		"/admin/matches/images/runs/" + strconv.FormatInt(run, 10) + "/resume",
		"/admin/matches/images/runs/" + strconv.FormatInt(run, 10) + "/cancel",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q", want)
		}
	}
	// 有活动（暂停）任务时不再显示启动按钮。
	if strings.Contains(body, `action="/admin/matches/images/backfill"`) {
		t.Fatal("start button must hide while a run is unfinished")
	}

	// 取消后：启动按钮回归，最近任务行展示终态。
	if err = s.TransitionArtistImageBackfillRun(ctx, run, "cancel", "manual"); err != nil {
		t.Fatal(err)
	}
	body = adminGetBody(t, handler, cookie, "/admin/matches")
	if !strings.Contains(body, `action="/admin/matches/images/backfill"`) {
		t.Fatal("start button must return after the run reaches a terminal state")
	}
	if !strings.Contains(body, "最近补全：#"+strconv.FormatInt(run, 10)) || !strings.Contains(body, "已停止") {
		t.Fatalf("last run line missing: %s", body)
	}
}

// 全部管理路由都有 CSRF 保护。
func TestArtistImageBackfillRoutesRequireCSRF(t *testing.T) {
	_, _, handler, _, cookie, _ := rateLimitTestApp(t)
	for _, path := range []string{
		"/admin/matches/images/backfill",
		"/admin/matches/images/runs/1/pause",
		"/admin/matches/images/runs/1/resume",
		"/admin/matches/images/runs/1/cancel",
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(url.Values{}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("POST %s without CSRF: status=%d", path, rec.Code)
		}
	}
}

// 启动→完成（无 URL 计 no_url，全程零网络）；暂停中的任务阻止重复启动；
// 恢复/暂停/停止路由与 active.json 轮询端点。
func TestArtistImageBackfillRoutesFlow(t *testing.T) {
	_, s, handler, _, cookie, csrf := rateLimitTestApp(t)
	ctx := context.Background()
	artists, err := s.ArtistsForMatching(ctx)
	if err != nil || len(artists) == 0 {
		t.Fatalf("artists=%v err=%v", artists, err)
	}
	// 空 URL + Last.fm 默认禁用：补全不查询任何来源，直接计 no_url。
	if err = s.UpsertExternalArtistProfile(ctx, artists[0].ID, storage.ExternalArtistProfile{Source: "lastfm", ExternalID: "lf-1", DisplayName: "Artist"}); err != nil {
		t.Fatal(err)
	}

	rec := postAdminForm(t, handler, cookie, csrf, "/admin/matches/images/backfill", nil)
	if notice := notice303Of(t, rec); !strings.Contains(notice, "已匹配艺术家头像补全任务已启动") {
		t.Fatalf("notice=%q", notice)
	}
	deadline := time.Now().Add(10 * time.Second)
	var run storage.ArtistImageBackfillRun
	for time.Now().Before(deadline) {
		run, err = s.LatestArtistImageBackfillRun(ctx)
		if err == nil && run.Status != "running" && run.Status != "queued" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if run.Status != "completed" || run.NoURL != 1 || run.Cached != 0 {
		t.Fatalf("run=%+v err=%v", run, err)
	}

	// 手动制造暂停任务 → 重复启动被拒绝并给出明确提示（重复点击安全）。
	run2, err := s.CreateArtistImageBackfillRun(ctx, []storage.ArtistImageBackfillCandidate{{ID: artists[0].ID, Name: artists[0].Name}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.TransitionArtistImageBackfillRun(ctx, run2, "pause", "manual"); err != nil {
		t.Fatal(err)
	}
	rec = postAdminForm(t, handler, cookie, csrf, "/admin/matches/images/backfill", nil)
	if notice := notice303Of(t, rec); !strings.Contains(notice, "已有活动或暂停中的补全任务") {
		t.Fatalf("notice=%q", notice)
	}

	// active.json 返回暂停中的任务。
	req := httptest.NewRequest(http.MethodGet, "/admin/matches/images/runs/active.json", nil)
	req.AddCookie(cookie)
	activeRec := httptest.NewRecorder()
	handler.ServeHTTP(activeRec, req)
	if activeRec.Code != 200 {
		t.Fatalf("active.json status=%d", activeRec.Code)
	}
	var payload struct {
		Run *artistImageRunView `json:"run"`
	}
	if err = json.Unmarshal(activeRec.Body.Bytes(), &payload); err != nil || payload.Run == nil {
		t.Fatalf("active.json=%s err=%v", activeRec.Body.String(), err)
	}
	if payload.Run.ID != run2 || payload.Run.Status != "paused" || !payload.Run.Resumable {
		t.Fatalf("run=%+v", payload.Run)
	}

	// 对已暂停任务再次暂停 → 409；继续 → 303 并跑完。
	rec = postAdminForm(t, handler, cookie, csrf, "/admin/matches/images/runs/"+strconv.FormatInt(run2, 10)+"/pause", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("double pause status=%d", rec.Code)
	}
	rec = postAdminForm(t, handler, cookie, csrf, "/admin/matches/images/runs/"+strconv.FormatInt(run2, 10)+"/resume", nil)
	if notice := notice303Of(t, rec); !strings.Contains(notice, "补全任务已继续") {
		t.Fatalf("notice=%q", notice)
	}
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		run, err = s.DurableArtistImageBackfillRun(ctx, run2)
		if err == nil && run.Status != "running" && run.Status != "queued" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if run.Status != "completed" || run.NoURL != 1 {
		t.Fatalf("run=%+v", run)
	}

	// 终态任务可以取消（幂等提示）→ 实际已完成任务取消应 409。
	rec = postAdminForm(t, handler, cookie, csrf, "/admin/matches/images/runs/"+strconv.FormatInt(run2, 10)+"/cancel", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("cancel completed run status=%d", rec.Code)
	}

	// 不存在的任务 → 404。
	rec = postAdminForm(t, handler, cookie, csrf, "/admin/matches/images/runs/999999/cancel", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing run cancel status=%d", rec.Code)
	}

	// 全部结束后 active.json 返回 run:null。
	req = httptest.NewRequest(http.MethodGet, "/admin/matches/images/runs/active.json", nil)
	req.AddCookie(cookie)
	activeRec = httptest.NewRecorder()
	handler.ServeHTTP(activeRec, req)
	if !strings.Contains(activeRec.Body.String(), `"run":null`) {
		t.Fatalf("active.json=%s", activeRec.Body.String())
	}
}
