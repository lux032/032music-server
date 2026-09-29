package enrichment

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

// 批次 8 A3：cachedJSON 对瞬时传输错误自动重试一次。

// batch8Store 打开一个迁移好的临时库（cachedJSON 需要读写 http_response_cache）。
func batch8Store(t *testing.T) *storage.Store {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "batch8.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err = store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return store
}

func batch8Manager(t *testing.T, store *storage.Store) *Manager {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := New(context.Background(), store, logger, t.TempDir())
	manager.bangumiInterval = 0
	manager.sleep = func(ctx context.Context, d time.Duration) error { return nil }
	return manager
}

// truncatedJSONServer 第一次请求返回被截断的响应体（客户端读到
// io.ErrUnexpectedEOF），之后返回合法 JSON。
func truncatedJSONServer(t *testing.T, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Error("ResponseWriter does not support hijack")
				return
			}
			conn, _, err := hijacker.Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			// 声明 Content-Length 20 但只写一半就断开。
			_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 20\r\n\r\n{\"ok\":tr"))
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestCachedJSONRetriesTransientTransportError(t *testing.T) {
	store := batch8Store(t)
	manager := batch8Manager(t, store)
	var calls atomic.Int32
	server := truncatedJSONServer(t, &calls)

	slept := 0
	manager.sleep = func(ctx context.Context, d time.Duration) error { slept++; return nil }

	var target struct {
		OK bool `json:"ok"`
	}
	setting := storage.MetadataSourceSetting{Source: "bangumi", Enabled: true, CacheDays: 30}
	status, err := manager.cachedJSON(context.Background(), "bangumi", "retry-key", server.URL+"/subjects", setting, false, nil, &target)
	if err != nil || status != http.StatusOK || !target.OK {
		t.Fatalf("status=%d err=%v target=%+v", status, err, target)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("requests=%d, want 2 (first truncated, second succeeds)", got)
	}
	if slept != 1 {
		t.Fatalf("sleep calls=%d, want 1 (retry waits one bangumi interval)", slept)
	}
}

func TestCachedJSONDoesNotRetryRateLimit(t *testing.T) {
	store := batch8Store(t)
	manager := batch8Manager(t, store)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(server.Close)

	var target struct {
		OK bool `json:"ok"`
	}
	setting := storage.MetadataSourceSetting{Source: "bangumi", Enabled: true, CacheDays: 30}
	_, err := manager.cachedJSON(context.Background(), "bangumi", "rl-key", server.URL+"/subjects", setting, false, nil, &target)
	if asRateLimited(err) == nil {
		t.Fatalf("err=%v, want rate limit error", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("requests=%d, want 1 (429 stops immediately, never retried)", got)
	}
}

// eofBody 在 Read 时立即返回 io.ErrUnexpectedEOF，模拟被截断的响应体。
type eofBody struct{}

func (eofBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (eofBody) Close() error             { return nil }

// L4：ctx 在传输途中被取消且错误属于瞬时类型（响应体读到 EOF）时也不重试。
// cachedJSONAttempt 的 body 读取分支不做 ctx 转换，EOF 会原样传到重试判定，
// 全靠 cachedJSON 的 ctx.Err()==nil 守卫拦截（变异验证：删掉守卫本测试失败）。
func TestCachedJSONDoesNotRetryCancelledContext(t *testing.T) {
	store := batch8Store(t)
	manager := batch8Manager(t, store)
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	manager.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		cancel() // 模拟 ctx 在响应体读取前被取消
		return &http.Response{Status: "200 OK", StatusCode: http.StatusOK, Header: make(http.Header), Body: eofBody{}, Request: r}, nil
	})}

	var target struct {
		OK bool `json:"ok"`
	}
	// 用没有限流等待的来源（lastfm）：bangumi/musicbrainz 的 waitXRateLimit
	// 会先拦下重试，那样 ctx 守卫就起不到判定作用，变异验证会漏报。
	setting := storage.MetadataSourceSetting{Source: "lastfm", Enabled: true, CacheDays: 30}
	_, err := manager.cachedJSON(ctx, "lastfm", "cancel-key", "http://lastfm.local/subjects", setting, false, nil, &target)
	if err == nil {
		t.Fatal("cancelled context must fail")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("requests=%d, want 1 (cancelled context never retries)", got)
	}
}

func TestIsTransientTransportError(t *testing.T) {
	if !isTransientTransportError(io.ErrUnexpectedEOF) {
		t.Fatal("unexpected EOF must be transient")
	}
	if !isTransientTransportError(fmt.Errorf("read: %w", syscall.ECONNRESET)) {
		t.Fatal("connection reset must be transient")
	}
	if !isTransientTransportError(fmt.Errorf("wrap: %w", &net.DNSError{IsTimeout: true})) {
		t.Fatal("timeout must be transient")
	}
	if isTransientTransportError(context.Canceled) {
		t.Fatal("context.Canceled is never transient")
	}
	// context.DeadlineExceeded 实现了 net.Error（Timeout()=true）：分类器判为
	// 超时，但重试由 cachedJSON 的 ctx.Err()==nil 守卫兜底——调用方 ctx 到期
	// 时不会重试，只有 http.Client 自身的 20s 超时会重试一次。
	if !isTransientTransportError(context.DeadlineExceeded) {
		t.Fatal("deadline exceeded is a timeout (retried only when caller ctx is alive)")
	}
	if isTransientTransportError(sql429Error()) {
		t.Fatal("rate limit error must not be transient")
	}
	if isTransientTransportError(errors.New("bangumi returned HTTP 502")) {
		t.Fatal("HTTP status errors must not be transient")
	}
}

func sql429Error() error {
	return &RateLimitError{Source: "bangumi", StatusCode: 429, RetryAfter: time.Minute}
}

// 批次 8 A1/A 追加：一轮跑完后阶段计数持久化，且每个阶段统计前先更新
// “正在统计 X 阶段…”。
func TestPhase4RunPersistsStageProgressAndCollectingNotice(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[]}`)
	})
	manager, store, _, _ := phase4TestManager(t, handler)
	ctx := context.Background()

	run, err := store.CreateEnrichmentRun(ctx, "all", 0, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	// 在曲目阶段统计开始前观察任务状态：必须已经持久化“正在统计曲目阶段…”。
	var observed []string
	manager.testStageCollectHook = func(stage string) {
		current, readErr := store.EnrichmentRun(ctx, run.ID)
		if readErr != nil {
			t.Errorf("read run in hook: %v", readErr)
			return
		}
		if current.Current != "正在统计"+phase4StageLabel(stage)+"阶段…" {
			t.Errorf("stage %s: current=%q, want collecting notice", stage, current.Current)
		}
		if current.Stage != stage {
			t.Errorf("stage field=%q, want %q", current.Stage, stage)
		}
		observed = append(observed, stage)
	}
	manager.executePhase4Run(ctx, run.ID, RunRequest{Scope: "all"})

	finished, err := store.EnrichmentRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != "completed" {
		t.Fatalf("run=%+v", finished)
	}
	// scope=all 的四个阶段：专辑（0，fixture 专辑已关联）→ 曲目（1）→ 作品
	// （1）→ 系列。阶段计数持久化，结束时 stage 停在 series。
	if finished.StageAlbums != 0 || finished.StageTracks != 1 || finished.StageWorks != 1 {
		t.Fatalf("stage totals albums=%d tracks=%d works=%d", finished.StageAlbums, finished.StageTracks, finished.StageWorks)
	}
	if finished.Stage != phase4StageSeries {
		t.Fatalf("stage=%q, want series", finished.Stage)
	}
	wantStages := []string{phase4StageAlbums, phase4StageTracks, phase4StageWorks}
	if strings.Join(observed, ",") != strings.Join(wantStages, ",") {
		t.Fatalf("hook stages=%v, want %v", observed, wantStages)
	}
}

// 批次 8 C1：海报缓存统计与补全结果记忆。
func TestWorkPosterBackfillStatsAndResult(t *testing.T) {
	store := batch8Store(t)
	ctx := context.Background()
	cachedWork, err := store.CreateWork(ctx, storage.WorkInput{Title: "Cached", Type: "anime", PosterURL: "https://example.com/cached.jpg"})
	if err != nil {
		t.Fatal(err)
	}
	// 127.0.0.1 会被 SSRF 校验拒绝（不解析 DNS、不发请求），确定性地失败。
	if _, err = store.CreateWork(ctx, storage.WorkInput{Title: "Failing", Type: "anime", PosterURL: "http://127.0.0.1/poster.jpg"}); err != nil {
		t.Fatal(err)
	}

	manager := batch8Manager(t, store)
	// 把 cachedWork 的海报直接放进缓存目录。
	key := fmt.Sprintf("%x", sha256.Sum256([]byte("https://example.com/cached.jpg")))
	if err = os.MkdirAll(manager.workPosterDirectory(), 0o750); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(manager.workPosterDirectory(), key+".jpg"), []byte("jpeg"), 0o640); err != nil {
		t.Fatal(err)
	}

	cached, total, err := manager.WorkPosterCacheStats(ctx)
	if err != nil || cached != 1 || total != 2 {
		t.Fatalf("stats cached=%d total=%d err=%v, want 1/2", cached, total, err)
	}

	// L4：用 hook 把补全 goroutine 阻塞住，消除“第二次启动”的竞态。
	blocked := make(chan struct{})
	entered := make(chan struct{})
	manager.testPosterBackfillHook = func() { close(entered); <-blocked }
	if !manager.StartWorkPosterBackfill() {
		t.Fatal("first backfill must start")
	}
	<-entered
	if manager.StartWorkPosterBackfill() {
		t.Fatal("second backfill must report already running")
	}
	if !manager.PosterBackfillRunning() {
		t.Fatal("backfill must be running while the hook blocks")
	}
	close(blocked)
	manager.testPosterBackfillHook = nil
	waitBackfillResult(t, manager, nil)
	result := manager.LastPosterBackfill()
	// 已缓存的不重复下载（不计入成功）；失败的计入失败。
	if result.Cached != 0 || result.Failed != 1 || result.RateLimited {
		t.Fatalf("result=%+v, want cached=0 failed=1 rateLimited=false", result)
	}
	if manager.PosterBackfillRunning() {
		t.Fatal("backfill must be finished")
	}
	_ = cachedWork
}

// waitBackfillResult 等待一轮补全结束（FinishedAt 晚于 prev；注意
// LastPosterBackfill 返回的是副本指针，不能用指针比较）。
func waitBackfillResult(t *testing.T, manager *Manager, prev *PosterBackfillResult) *PosterBackfillResult {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if result := manager.LastPosterBackfill(); result != nil && (prev == nil || result.FinishedAt.After(prev.FinishedAt)) {
			return result
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("backfill result never recorded")
	return nil
}

// 批次 8.5 L3：自动补全跳过 6 小时内失败的 URL；手动按钮强制重试。
func TestWorkPosterBackfillSkipsRecentlyFailed(t *testing.T) {
	store := batch8Store(t)
	ctx := context.Background()
	if _, err := store.CreateWork(ctx, storage.WorkInput{Title: "Failing", Type: "anime", PosterURL: "http://127.0.0.1/poster.jpg"}); err != nil {
		t.Fatal(err)
	}
	manager := batch8Manager(t, store)

	// 第一轮：失败并记录。
	manager.StartWorkPosterBackfill()
	first := waitBackfillResult(t, manager, nil)
	if first.Failed != 1 {
		t.Fatalf("first=%+v, want failed=1", first)
	}
	// 第二轮（自动）：TTL 内跳过，不再失败。
	manager.StartWorkPosterBackfill()
	second := waitBackfillResult(t, manager, first)
	if second.Failed != 0 || second.Cached != 0 {
		t.Fatalf("second=%+v, want failed=0 (skipped by failure memory)", second)
	}
	// 手动按钮：忽略失败记录，强制重试。
	manager.RetryWorkPosterBackfill()
	third := waitBackfillResult(t, manager, second)
	if third.Failed != 1 {
		t.Fatalf("third=%+v, want failed=1 (manual retry)", third)
	}
	// TTL 过期后自动补全重新尝试。
	manager.posterMu.Lock()
	manager.posterFailed["http://127.0.0.1/poster.jpg"] = time.Now().Add(-7 * time.Hour)
	manager.posterMu.Unlock()
	manager.StartWorkPosterBackfill()
	fourth := waitBackfillResult(t, manager, third)
	if fourth.Failed != 1 {
		t.Fatalf("fourth=%+v, want failed=1 (TTL expired)", fourth)
	}
}

// 批次 8.5 L1：scope=tracks 时没有空的专辑阶段，也不会显示“正在统计专辑
// 阶段…”。
func TestPhase4TracksScopeSkipsEmptyAlbumStage(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[]}`)
	})
	manager, store, _, _ := phase4TestManager(t, handler)
	ctx := context.Background()

	run, err := store.CreateEnrichmentRun(ctx, "tracks", 0, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	var observed []string
	manager.testStageCollectHook = func(stage string) {
		current, readErr := store.EnrichmentRun(ctx, run.ID)
		if readErr != nil {
			t.Errorf("read run in hook: %v", readErr)
			return
		}
		if strings.Contains(current.Current, "专辑") {
			t.Errorf("tracks scope must never mention the album stage: %q", current.Current)
		}
		observed = append(observed, stage)
	}
	manager.executePhase4Run(ctx, run.ID, RunRequest{Scope: "tracks"})

	finished, err := store.EnrichmentRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != "completed" {
		t.Fatalf("run=%+v", finished)
	}
	if len(observed) != 1 || observed[0] != phase4StageTracks {
		t.Fatalf("stages=%v, want [tracks] only", observed)
	}
	if finished.StageAlbums != -1 {
		t.Fatalf("StageAlbums=%d, want -1 (album stage not part of this run)", finished.StageAlbums)
	}
	if finished.StageTracks != 1 {
		t.Fatalf("StageTracks=%d, want 1 (fixture track)", finished.StageTracks)
	}
}

// 批次 8.5 L2：服务关闭（baseCtx 取消）后，增强轮结束不再触发海报补全。
func TestPhase4RunEndBackfillSkippedWhenShuttingDown(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[]}`)
	})
	manager, store, _, _ := phase4TestManager(t, handler)
	manager.SetPosterBackfillEnabled(true)
	run, err := store.CreateEnrichmentRun(context.Background(), "albums", 0, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	// 取消 baseCtx，再跑（会立刻走 cancelled 分支结束）。
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	manager.baseCtx = cancelled
	manager.executePhase4Run(cancelled, run.ID, RunRequest{Scope: "albums"})
	time.Sleep(200 * time.Millisecond)
	if manager.LastPosterBackfill() != nil {
		t.Fatal("poster backfill must not run after shutdown")
	}
}

// 批次 8 C2：增强轮结束后按开关触发海报补全。
func TestPhase4RunEndTriggersPosterBackfillWhenEnabled(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[]}`)
	})
	manager, store, _, _ := phase4TestManager(t, handler)
	manager.SetPosterBackfillEnabled(true)
	ctx := context.Background()
	run, err := store.CreateEnrichmentRun(ctx, "all", 0, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	manager.executePhase4Run(ctx, run.ID, RunRequest{Scope: "all"})
	waitBackfillResult(t, manager, nil)
}

func TestPhase4RunEndDoesNotTriggerPosterBackfillWhenDisabled(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[]}`)
	})
	manager, store, _, _ := phase4TestManager(t, handler)
	// 默认关闭。
	ctx := context.Background()
	run, err := store.CreateEnrichmentRun(ctx, "all", 0, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	manager.executePhase4Run(ctx, run.ID, RunRequest{Scope: "all"})
	time.Sleep(200 * time.Millisecond)
	if manager.LastPosterBackfill() != nil {
		t.Fatal("poster backfill must not run when disabled")
	}
}

// 批次 8 C2：启动延迟补全尊重开关与 ctx 关闭。
func TestScheduleStartupPosterBackfill(t *testing.T) {
	// 关闭开关：不排期。
	store := batch8Store(t)
	manager := batch8Manager(t, store)
	manager.ScheduleStartupPosterBackfill(10 * time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	if manager.LastPosterBackfill() != nil {
		t.Fatal("disabled backfill must not be scheduled")
	}

	// 开启开关：延迟后执行。
	manager2 := batch8Manager(t, store)
	manager2.SetPosterBackfillEnabled(true)
	manager2.ScheduleStartupPosterBackfill(10 * time.Millisecond)
	deadline := time.Now().Add(5 * time.Second)
	for manager2.LastPosterBackfill() == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if manager2.LastPosterBackfill() == nil {
		t.Fatal("enabled startup backfill never ran")
	}

	// ctx 在延迟窗口内关闭：不再启动。
	ctx, cancel := context.WithCancel(context.Background())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager3 := New(ctx, batch8Store(t), logger, t.TempDir())
	manager3.SetPosterBackfillEnabled(true)
	manager3.ScheduleStartupPosterBackfill(50 * time.Millisecond)
	cancel()
	manager3.Wait()
	if manager3.LastPosterBackfill() != nil {
		t.Fatal("cancelled context must prevent the scheduled backfill")
	}
}
