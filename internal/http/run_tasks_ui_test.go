package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/storage"
)

func adminGetBody(t *testing.T, handler http.Handler, cookie *http.Cookie, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("GET %s status=%d body=%s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// 匹配页：最终命名、扫描语义说明、状态中文映射、活动任务动作、历史折叠分页。
func TestMatchReviewPageRunTasksUI(t *testing.T) {
	_, s, handler, _, cookie, _ := rateLimitTestApp(t)
	ctx := context.Background()
	artists, _ := s.ArtistsForMatching(ctx)
	var lastFailed int64
	for i := 0; i < 11; i++ {
		finished, err := s.CreateDurableArtistRun(ctx, []storage.ArtistRunItemInput{{Artist: artists[0]}})
		if err != nil {
			t.Fatal(err)
		}
		if err = s.TransitionArtistRun(ctx, finished, "fail", "连续五次来源失败，任务已结束"); err != nil {
			t.Fatal(err)
		}
		lastFailed = finished
	}
	run, err := s.CreateDurableArtistRun(ctx, []storage.ArtistRunItemInput{{Artist: artists[0]}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.TransitionArtistRun(ctx, run, "pause", "manual"); err != nil {
		t.Fatal(err)
	}
	body := adminGetBody(t, handler, cookie, "/admin/matches")
	for _, want := range []string{
		"艺术家匹配与审核",
		"扫描未匹配与需检查的艺术家",
		"元数据匹配，不是文件扫描",
		"当前任务",
		"已暂停",
		`data-run-status="paused"`,
		`data-run-action="pause" hidden`,
		`data-run-action="resume">`,
		">继续</button>",
		">停止</button>",
		"任务记录（共 12 条）",
		"已失败",
		"runsPage=2",
		"data-active-url=\"/admin/matches/runs/active.json\"",
		"待审核候选",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.Contains(body, ">failed<") {
		t.Fatal("raw english status leaked")
	}
	page2 := adminGetBody(t, handler, cookie, "/admin/matches?runsPage=2")
	if !strings.Contains(page2, "第 2 页") || !strings.Contains(page2, "runsPage=1") {
		t.Fatalf("page2=%s", page2)
	}
	// 第 2 页的“最近任务”仍是最新终态任务，不是页内旧任务。
	if !strings.Contains(page2, "最近任务：#"+strconv.FormatInt(lastFailed, 10)) {
		t.Fatalf("page2 last run stale: %s", page2)
	}
	// 待审核筛选控件保留 q/source，历史分页不吞筛选参数。
	if !strings.Contains(body, `name="q"`) || !strings.Contains(body, `name="source"`) || !strings.Contains(body, `name="runsPage"`) {
		t.Fatal("review filter controls missing")
	}
}

// 增强页：活动卡片（scope/force 中文、限流行、动作）、最近任务、历史折叠分页。
func TestEnrichmentPageRunTasksUI(t *testing.T) {
	_, s, handler, _, cookie, _ := rateLimitTestApp(t)
	ctx := context.Background()
	var lastFailed int64
	for i := 0; i < 11; i++ {
		finished, err := s.CreateDurableEnrichmentRun(ctx, "tracks", 0, false, []string{"tracks"})
		if err != nil {
			t.Fatal(err)
		}
		if err = s.TransitionEnrichmentWorker(ctx, finished.ID, finished.Epoch, "fail", "连续五次来源失败，任务已结束"); err != nil {
			t.Fatal(err)
		}
		lastFailed = finished.ID
	}
	run, err := s.CreateDurableEnrichmentRun(ctx, "works", 0, true, []string{"works", "series"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.TransitionEnrichmentRun(ctx, run.ID, "pause", "rate_limit_wait_budget"); err != nil {
		t.Fatal(err)
	}
	body := adminGetBody(t, handler, cookie, "/admin/enrichment")
	for _, want := range []string{
		"当前任务",
		"作品对齐与系列",
		"强制刷新",
		"已暂停",
		"暂停原因：限流等待超出 30 分钟预算",
		`data-run-status="paused"`,
		`data-run-action="pause" hidden`,
		`data-run-action="resume">`,
		">继续</button>",
		">停止</button>",
		"任务记录（共 12 条）",
		"已失败",
		"runsPage=2",
		"data-active-url=\"/admin/enrichment/runs/active.json\"",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q", want)
		}
	}
	page2 := adminGetBody(t, handler, cookie, "/admin/enrichment?runsPage=2")
	if !strings.Contains(page2, "最近任务：#"+strconv.FormatInt(lastFailed, 10)) {
		t.Fatalf("page2 last run stale: %s", page2)
	}
}

// DTO 白名单：errorMessage 明示暴露，且不携带 SQL/快照。
func TestEnrichmentRunDTOErrorMessageWhitelisted(t *testing.T) {
	_, s, handler, _, cookie, _ := rateLimitTestApp(t)
	ctx := context.Background()
	run, err := s.CreateDurableEnrichmentRun(ctx, "tracks", 0, false, []string{"tracks"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.TransitionEnrichmentWorker(ctx, run.ID, run.Epoch, "fail", "连续五次来源失败，任务已结束"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/enrichment/runs/"+strconv.FormatInt(run.ID, 10), nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `"errorMessage":"连续五次来源失败，任务已结束"`) {
		t.Fatal(rec.Code, body)
	}
	if strings.Contains(body, "parameters_json") || strings.Contains(body, "api_key") {
		t.Fatal(body)
	}
}

// running 卡片渲染（dataset 状态、暂停可见/继续隐藏）、历史链接双向保留
// q/source/page/runsPage，以及无障碍属性（无静态 aria-expanded、状态徽标
// aria-live、等待行 aria-live=off）。
func TestMatchReviewPageRunningCardAndQueryPreservation(t *testing.T) {
	_, s, handler, _, cookie, _ := rateLimitTestApp(t)
	ctx := context.Background()
	artists, _ := s.ArtistsForMatching(ctx)
	for i := 0; i < 11; i++ {
		finished, err := s.CreateDurableArtistRun(ctx, []storage.ArtistRunItemInput{{Artist: artists[0]}})
		if err != nil {
			t.Fatal(err)
		}
		if err = s.TransitionArtistRun(ctx, finished, "fail", "连续五次来源失败，任务已结束"); err != nil {
			t.Fatal(err)
		}
	}
	run, err := s.CreateDurableArtistRun(ctx, []storage.ArtistRunItemInput{{Artist: artists[0]}})
	if err != nil {
		t.Fatal(err)
	}
	body := adminGetBody(t, handler, cookie, "/admin/matches?q=x&source=lastfm")
	for _, want := range []string{
		`data-run-status="running"`,
		`data-run-action="pause">`,
		`data-run-action="resume" hidden`,
		`data-run-action="cancel">`,
		`data-run-field="status" aria-live="polite"`,
		`data-run-field="wait" aria-live="off"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.Contains(body, `<summary aria-expanded`) {
		t.Fatal("static aria-expanded on summary")
	}
	// 历史分页链接保留筛选参数。
	if !strings.Contains(body, "q=x&source=lastfm&page=1&runsPage=2") {
		t.Fatalf("history link dropped filter params: %s", body)
	}
	// 反向：筛选表单携带 runsPage 隐藏域，且值跟随当前历史页。
	if !strings.Contains(body, `name="runsPage"`) {
		t.Fatal("filter form dropped runsPage")
	}
	body2 := adminGetBody(t, handler, cookie, "/admin/matches?q=x&source=lastfm&runsPage=2")
	if !strings.Contains(body2, `name="runsPage" value="2"`) {
		t.Fatal("filter form runsPage value not preserved")
	}
	_ = run
}

// 增强页 running 卡片的 dataset 与动作可见性。
func TestEnrichmentPageRunningCardRender(t *testing.T) {
	_, s, handler, _, cookie, _ := rateLimitTestApp(t)
	ctx := context.Background()
	run, err := s.CreateDurableEnrichmentRun(ctx, "tracks", 0, false, []string{"tracks"})
	if err != nil {
		t.Fatal(err)
	}
	body := adminGetBody(t, handler, cookie, "/admin/enrichment")
	for _, want := range []string{
		`data-run-status="running"`,
		`data-run-action="pause">`,
		`data-run-action="resume" hidden`,
		`>暂停</button>`,
		`>停止</button>`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q", want)
		}
	}
	_ = run
}
