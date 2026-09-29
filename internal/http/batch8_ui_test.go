package httpapi

import (
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/enrichment"
	"github.com/lux032/032music-server/internal/storage"
)

// 批次 8 B：审核页四个 Tab 都不再渲染 http(s) 外链图片（Bangumi 海报被 CSP
// img-src 'self' 拦截后显示裂图），候选作品改为本地类型胶囊；本地专辑封面
// （review-cover-thumb，本站 URL）保留。
func TestWorkReviewTabsHaveNoExternalImages(t *testing.T) {
	app, cookie, _, albumID, _, trackIDs := setupBatch3HTTPFixture(t)
	handler := app.Handler()
	ctx := context.Background()
	const externalPoster = "https://lain.bgm.tv/pic/cover/l/aa/bb/123.jpg"

	if err := app.store.SaveAlbumSubjectCandidates(ctx, albumID, []storage.AlbumSubjectCandidate{{
		ExternalID: "412958", Title: "Amore", Score: 88,
		Tieups: []storage.BangumiTieup{{SubjectID: 501, Title: "Work Anime A", Type: "anime", Role: "op", PosterURL: externalPoster}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := app.store.SaveTrackSubjectCandidates(ctx, trackIDs[0], []storage.TrackSubjectCandidate{{
		ExternalID: "359216", Title: "Track Cand", MatchKind: "multi_title",
		Tieups: []storage.BangumiTieup{{SubjectID: 502, Title: "Work Anime B", Type: "game", Role: "op", PosterURL: externalPoster}},
	}}); err != nil {
		t.Fatal(err)
	}
	work, err := app.store.CreateWork(ctx, storage.WorkInput{Title: "Align Me", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	if err = app.store.ReplaceWorkMatchCandidates(ctx, work.ID, []storage.WorkMatchCandidate{{
		WorkID: work.ID, Source: "bangumi", ExternalID: "326624", Title: "Align Cand",
		Score: 96, Type: "movie", Status: "candidate", PosterURL: externalPoster,
	}}); err != nil {
		t.Fatal(err)
	}
	workB := batch6Work(t, app.store, "Sugg Other", "game", 2021)
	batch6Suggestion(t, app.store, work, workB, 11, 22, "游戏", "动画", "cross")

	for _, tab := range []string{"albums", "tracks", "works", "series"} {
		body := getAdmin(t, handler, cookie, "/admin/work-review?tab="+tab)
		if strings.Contains(body, `src="http`) || strings.Contains(body, `src='http`) {
			t.Fatalf("tab %s still renders an external <img> src", tab)
		}
		if strings.Contains(body, "tieup-mini-poster") || strings.Contains(body, "work-align-poster") || strings.Contains(body, "review-poster-thumb") {
			t.Fatalf("tab %s still uses a removed poster class", tab)
		}
		assertNoEnumWords(t, body)
	}
	// 作品对齐与系列建议 Tab 用类型胶囊替代海报；专辑/曲目 Tab 的关联行保留
	// 文字型 format-pill（专辑封面是本站缩略图，不受影响）。
	for _, tab := range []string{"works", "series"} {
		body := getAdmin(t, handler, cookie, "/admin/work-review?tab="+tab)
		if !strings.Contains(body, "work-type-chip") {
			t.Fatalf("tab %s must render type chips instead of posters", tab)
		}
	}
	// 本地专辑封面保留。
	body := getAdmin(t, handler, cookie, "/admin/work-review?tab=albums")
	if !strings.Contains(body, "review-cover-thumb") {
		t.Fatal("local album cover thumbnail must stay")
	}
}

// 批次 8 C1：增强页的海报缓存统计、补全按钮与上次结果。

// batch8PosterApp 搭一个带增强管理器的应用；返回海报缓存目录（只写本地文件）。
func batch8PosterApp(t *testing.T) (*App, *storage.Store, http.Handler, *enrichment.Manager, *http.Cookie, string, string) {
	t.Helper()
	store := credentialTestStore(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dataDir := t.TempDir()
	manager := enrichment.New(context.Background(), store, logger, dataDir)
	app, err := NewApp(credentialTestConfig(t), store, nil, manager, logger, "test")
	if err != nil {
		t.Fatal(err)
	}
	handler := app.Handler()
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	return app, store, handler, manager, cookie, csrfOf(t, app, cookie), filepath.Join(dataDir, "work-posters")
}

func TestEnrichmentPagePosterStatsAndBackfill(t *testing.T) {
	_, store, handler, manager, cookie, csrf, posterDir := batch8PosterApp(t)
	ctx := context.Background()
	// 两个有海报地址的作品：一个本地已缓存，一个下载必失败（127.0.0.1 被
	// SSRF 校验拒绝，不访问网络）。
	if _, err := store.CreateWork(ctx, storage.WorkInput{Title: "Has Cache", Type: "anime", PosterURL: "https://example.com/a.jpg"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateWork(ctx, storage.WorkInput{Title: "Never Caches", Type: "anime", PosterURL: "http://127.0.0.1/b.jpg"}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(posterDir, 0o750); err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte("https://example.com/a.jpg")))
	if err := os.WriteFile(filepath.Join(posterDir, key+".jpg"), []byte("jpeg"), 0o640); err != nil {
		t.Fatal(err)
	}

	body := getAdmin(t, handler, cookie, "/admin/enrichment")
	if !strings.Contains(body, "作品海报：已缓存 1 / 应有 2 · 缺失 1") {
		t.Fatalf("poster stats line missing: %s", body)
	}
	if !strings.Contains(body, "补全缺失海报") {
		t.Fatal("backfill button missing")
	}

	// CSRF：不带 token 403。
	rec := postForm(handler, cookie, "/admin/enrichment/posters/backfill", url.Values{})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST without CSRF: %d", rec.Code)
	}

	// 触发补全：303 + 提示。
	rec = postForm(handler, cookie, "/admin/enrichment/posters/backfill", url.Values{"csrfToken": {csrf}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST backfill: %d", rec.Code)
	}
	location, _ := url.Parse(rec.Header().Get("Location"))
	if notice := location.Query().Get("notice"); !strings.Contains(notice, "已开始补全") {
		t.Fatalf("notice=%q", notice)
	}
	// 补全还在进行（失败的 127.0.0.1 下载之后有一次间隔等待）：重复触发提示“正在补全”。
	rec = postForm(handler, cookie, "/admin/enrichment/posters/backfill", url.Values{"csrfToken": {csrf}})
	location, _ = url.Parse(rec.Header().Get("Location"))
	if notice := location.Query().Get("notice"); !strings.Contains(notice, "正在补全") {
		t.Fatalf("second POST notice=%q, want 正在补全", notice)
	}

	deadline := time.Now().Add(5 * time.Second)
	for manager.LastPosterBackfill() == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	result := manager.LastPosterBackfill()
	if result == nil || result.Failed != 1 {
		t.Fatalf("backfill result=%+v, want failed=1", result)
	}
	body = getAdmin(t, handler, cookie, "/admin/enrichment")
	if !strings.Contains(body, "上次补全：成功 0 · 失败 1") {
		t.Fatalf("last result line missing: %s", body)
	}
}

// 批次 8 C3：/admin/works/{id}/poster 支持 size 缩略图，不带 size 返回原图。
func TestWorkPosterServesThumbnail(t *testing.T) {
	_, store, handler, _, cookie, _, posterDir := batch8PosterApp(t)
	ctx := context.Background()
	const posterURL = "https://example.com/big.jpg"
	work, err := store.CreateWork(ctx, storage.WorkInput{Title: "Poster Work", Type: "anime", PosterURL: posterURL})
	if err != nil {
		t.Fatal(err)
	}
	// 写一张 1000x600 的 JPEG 作为“已缓存”的海报原图。
	if err = os.MkdirAll(posterDir, 0o750); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 1000, 600))
	for y := 0; y < 600; y++ {
		for x := 0; x < 1000; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 100, B: 50, A: 255})
		}
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(posterURL)))
	cacheFile, err := os.Create(filepath.Join(posterDir, key+".jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if err = jpeg.Encode(cacheFile, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	_ = cacheFile.Close()

	// 不带 size：原图原样返回。
	req := httptest.NewRequest(http.MethodGet, "/admin/works/"+strconv.FormatInt(work.ID, 10)+"/poster", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("original: %d", rec.Code)
	}
	cfg, _, err := image.DecodeConfig(rec.Body)
	if err != nil || cfg.Width != 1000 {
		t.Fatalf("original width=%d err=%v, want 1000", cfg.Width, err)
	}

	// 带 size=360：走缩略图桶（360 → 512 桶），返回缩小后的 JPEG。
	req = httptest.NewRequest(http.MethodGet, "/admin/works/"+strconv.FormatInt(work.ID, 10)+"/poster?size=360", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("thumbnail: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	cfg, _, err = image.DecodeConfig(rec.Body)
	if err != nil || cfg.Width != 512 {
		t.Fatalf("thumbnail width=%d err=%v, want 512 bucket", cfg.Width, err)
	}

	// 非法 size：400。
	req = httptest.NewRequest(http.MethodGet, "/admin/works/"+strconv.FormatInt(work.ID, 10)+"/poster?size=abc", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid size: %d", rec.Code)
	}
}

// 批次 8 A1：分阶段进度行的渲染口径。
func TestEnrichmentStageLine(t *testing.T) {
	// 旧行（迁移前）：四个阶段都未统计，不渲染。
	old := storage.EnrichmentRun{Scope: "all", Status: "completed", StageAlbums: -1, StageTracks: -1, StageWorks: -1}
	if got := enrichmentStageLine(old); got != "" {
		t.Fatalf("old run line=%q, want empty", got)
	}
	// 进行中：当前阶段 + 序号 + 已统计/待统计。
	running := storage.EnrichmentRun{Scope: "all", Status: "running", Stage: "tracks", StageAlbums: 145, StageTracks: 3210, StageWorks: -1}
	want := "阶段：曲目（2/4）· 专辑 145 · 曲目 3,210 · 作品 待统计 · 系列 待统计"
	if got := enrichmentStageLine(running); got != want {
		t.Fatalf("running line=%q, want %q", got, want)
	}
	// 已完成：无“当前阶段”，系列阶段进入过则计 1。
	done := storage.EnrichmentRun{Scope: "all", Status: "completed", Stage: "series", StageAlbums: 145, StageTracks: 3210, StageWorks: 60}
	want = "分阶段：专辑 145 · 曲目 3,210 · 作品 60 · 系列 1"
	if got := enrichmentStageLine(done); got != want {
		t.Fatalf("done line=%q, want %q", got, want)
	}
	// 单阶段范围。
	tracks := storage.EnrichmentRun{Scope: "tracks", Status: "running", Stage: "tracks", StageAlbums: -1, StageTracks: 3, StageWorks: -1}
	want = "阶段：曲目（1/1）· 曲目 3"
	if got := enrichmentStageLine(tracks); got != want {
		t.Fatalf("tracks line=%q, want %q", got, want)
	}
}

// 批次 8 D：作品页每页数量可选（24/36/60/120，cookie 记忆，非法回落 36）。
func TestWorksPageSizeOptions(t *testing.T) {
	app, store, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	ctx := context.Background()
	for i := 1; i <= 70; i++ {
		if _, err := store.CreateWork(ctx, storage.WorkInput{Title: fmt.Sprintf("Size Work %02d", i), Type: "anime"}); err != nil {
			t.Fatal(err)
		}
	}
	countCards := func(body string) int { return strings.Count(body, `class="work-card"`) }

	// 默认 36：70 个作品分两页，分页链接存在。
	body := getAdmin(t, handler, cookie, "/admin/works")
	if n := countCards(body); n != 36 {
		t.Fatalf("default page cards=%d, want 36", n)
	}
	if !strings.Contains(body, `name="size"`) {
		t.Fatal("page-size control missing from the filter bar")
	}

	// size=60：一页 60 个，写 cookie，分页链接带 size。
	req := httptest.NewRequest(http.MethodGet, "/admin/works?size=60", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("size=60: %d", rec.Code)
	}
	if n := countCards(rec.Body.String()); n != 60 {
		t.Fatalf("size=60 cards=%d, want 60", n)
	}
	if next := rec.Body.String(); !strings.Contains(next, "size=60") {
		t.Fatal("pagination links must carry size=60")
	}
	var sizeCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "032_pagesize_works" {
			sizeCookie = c
		}
	}
	if sizeCookie == nil || sizeCookie.Value != "60" {
		t.Fatalf("size cookie=%v", sizeCookie)
	}

	// 记住选择：不带 size 参数时按 cookie 渲染 60 个。
	req = httptest.NewRequest(http.MethodGet, "/admin/works", nil)
	req.AddCookie(cookie)
	req.AddCookie(sizeCookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if n := countCards(rec.Body.String()); n != 60 {
		t.Fatalf("remembered cards=%d, want 60", n)
	}

	// 非法值回落 36，且不覆盖已记住的 cookie。
	req = httptest.NewRequest(http.MethodGet, "/admin/works?size=999", nil)
	req.AddCookie(cookie)
	req.AddCookie(sizeCookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if n := countCards(rec.Body.String()); n != 36 {
		t.Fatalf("invalid size cards=%d, want 36 fallback", n)
	}

	// size=24：24 个，分页链接带 size=24。
	body = getAdmin(t, handler, cookie, "/admin/works?size=24")
	if n := countCards(body); n != 24 {
		t.Fatalf("size=24 cards=%d, want 24", n)
	}
	if !strings.Contains(body, "size=24") {
		t.Fatal("pagination links must carry size=24")
	}
	_ = app
}
