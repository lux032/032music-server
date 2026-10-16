package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/analysis"
	"github.com/lux032/032music-server/internal/storage"
)

type stubAnalyzer struct{}

func (stubAnalyzer) Health(context.Context) (analysis.Health, error) {
	return analysis.Health{Status: "ready", AnalysisVersion: "v1", Backend: "onnxruntime test", FullModeAvailable: true}, nil
}
func (stubAnalyzer) Labels(context.Context) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
func (stubAnalyzer) Analyze(context.Context, analysis.AnalyzeRequest) (analysis.AnalyzeResult, error) {
	return analysis.AnalyzeResult{}, &analysis.APIError{Status: 503, Code: "busy"}
}
func (stubAnalyzer) Fit(context.Context, []analysis.FitSample) (analysis.FitResult, error) {
	return analysis.FitResult{}, &analysis.APIError{Status: 422, Code: "fit_failed"}
}

func audioAnalysisTestApp(t *testing.T, configured bool) (*App, *storage.Store, http.Handler, *http.Cookie) {
	t.Helper()
	store := credentialTestStore(t)
	app := newCredentialTestApp(t, credentialTestConfig(t), store)
	opts := analysis.Options{}
	if configured {
		opts.Client = stubAnalyzer{}
	}
	app.SetAudioAnalysis(analysis.New(store, slog.New(slog.NewTextHandler(io.Discard, nil)), opts))
	handler := app.Handler()
	return app, store, handler, mustLogin(t, handler, "admin", testAdminPassword)
}

func capabilityAudioAnalysis(t *testing.T, handler http.Handler) bool {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
	req.Header.Set("Authorization", "Bearer "+testEnvAPIToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var body struct {
		Features map[string]bool `json:"features"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != 200 {
		t.Fatalf("capabilities status=%d err=%v", rec.Code, err)
	}
	return body.Features["audioAnalysis"]
}

func TestAudioAnalysisNotConfigured(t *testing.T) {
	app, store, handler, cookie := audioAnalysisTestApp(t, false)
	body := getWithCookie(handler, "/admin/audio-analysis", cookie).Body.String()
	for _, want := range []string{"未部署分析服务", "MUSIC_SERVER_ANALYZER_URL", "--profile analysis", `name="enabled"  disabled`} {
		if !strings.Contains(body, want) {
			t.Fatalf("page missing %q", want)
		}
	}
	dash := getWithCookie(handler, "/admin", cookie).Body.String()
	if !strings.Contains(dash, `href="/admin/audio-analysis"`) || !strings.Contains(dash, "未部署分析服务") {
		t.Fatal("dashboard must link to the analysis page with its summary")
	}
	rec := postSecurity(t, app, handler, cookie, "/admin/audio-analysis/settings", url.Values{"enabled": {"on"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", rec.Code)
	}
	if v, _ := store.AudioAnalysisSettings(context.Background()); v.Enabled {
		t.Fatal("must not enable without a deployed analyzer")
	}
	if capabilityAudioAnalysis(t, handler) {
		t.Fatal("capability must be false when not configured")
	}
}

func TestAudioAnalysisEnableWindowPauseAndStatus(t *testing.T) {
	app, store, handler, cookie := audioAnalysisTestApp(t, true)
	ctx := context.Background()
	if body := getWithCookie(handler, "/admin/audio-analysis", cookie).Body.String(); !strings.Contains(body, "未开启") || strings.Contains(body, "需要先启动分析服务") {
		t.Fatal("configured page must show the disabled state without setup instructions")
	}
	if capabilityAudioAnalysis(t, handler) {
		t.Fatal("capability must be false until enabled")
	}

	// CSRF is required.
	req := httptest.NewRequest(http.MethodPost, "/admin/audio-analysis/settings", strings.NewReader("enabled=on"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d", rec.Code)
	}

	rec = postSecurity(t, app, handler, cookie, "/admin/audio-analysis/settings", url.Values{"enabled": {"on"}, "window": {"all"}})
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, url.QueryEscape("开始首次全量分析")) {
		t.Fatalf("first enable notice missing: %s", loc)
	}
	v, _ := store.AudioAnalysisSettings(ctx)
	if !v.Enabled || v.Paused || v.WindowStart != nil || v.FirstEnabledAt == "" {
		t.Fatalf("settings = %+v", v)
	}
	if !capabilityAudioAnalysis(t, handler) {
		t.Fatal("capability must be true once enabled")
	}

	// Invalid and valid windows.
	rec = postSecurity(t, app, handler, cookie, "/admin/audio-analysis/settings", url.Values{"enabled": {"on"}, "window": {"custom"}, "window_start": {"01:00"}, "window_end": {"01:00"}})
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, url.QueryEscape("运行时间窗无效")) {
		t.Fatalf("invalid window accepted: %s", loc)
	}
	postSecurity(t, app, handler, cookie, "/admin/audio-analysis/settings", url.Values{"enabled": {"on"}, "window": {"custom"}, "window_start": {"23:30"}, "window_end": {"07:00"}})
	if v, _ = store.AudioAnalysisSettings(ctx); v.WindowStart == nil || *v.WindowStart != 23*60+30 || *v.WindowEnd != 7*60 {
		t.Fatalf("window = %+v", v)
	}
	if body := getWithCookie(handler, "/admin/audio-analysis", cookie).Body.String(); !strings.Contains(body, `value="23:30"`) || !strings.Contains(body, `value="custom" checked`) {
		t.Fatal("page must show the saved window")
	}

	// Pause/resume; saving settings while enabled keeps the pause.
	postSecurity(t, app, handler, cookie, "/admin/audio-analysis/pause", nil)
	postSecurity(t, app, handler, cookie, "/admin/audio-analysis/settings", url.Values{"enabled": {"on"}, "window": {"all"}})
	if v, _ = store.AudioAnalysisSettings(ctx); !v.Paused {
		t.Fatal("saving settings must keep a pause")
	}
	status := getWithCookie(handler, "/admin/audio-analysis/status", cookie)
	var payload struct {
		Status analysis.Snapshot `json:"status"`
		View   audioAnalysisView `json:"view"`
	}
	if err := json.Unmarshal(status.Body.Bytes(), &payload); err != nil || payload.Status.State != analysis.StatePaused || payload.View.StateLabel != "已暂停" {
		t.Fatalf("status = %s err=%v", status.Body.String(), err)
	}
	if body := getWithCookie(handler, "/admin/audio-analysis", cookie).Body.String(); !strings.Contains(body, `action="/admin/audio-analysis/resume"`) {
		t.Fatal("paused page must offer resume")
	}
	postSecurity(t, app, handler, cookie, "/admin/audio-analysis/resume", nil)
	if v, _ = store.AudioAnalysisSettings(ctx); v.Paused {
		t.Fatal("resume must clear the pause")
	}

	// Disable keeps the first-enabled timestamp; re-enable says "continue".
	first := v.FirstEnabledAt
	postSecurity(t, app, handler, cookie, "/admin/audio-analysis/settings", url.Values{"window": {"all"}})
	rec = postSecurity(t, app, handler, cookie, "/admin/audio-analysis/settings", url.Values{"enabled": {"on"}, "window": {"all"}})
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, url.QueryEscape("继续分析")) {
		t.Fatalf("re-enable notice: %s", loc)
	}
	if v, _ = store.AudioAnalysisSettings(ctx); v.FirstEnabledAt != first {
		t.Fatal("first enabled timestamp must be kept")
	}

	// API token access; admin JSON endpoint requires a session.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/audio-analysis", nil)
	req.Header.Set("Authorization", "Bearer "+testEnvAPIToken)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"enabled":true`) {
		t.Fatalf("api status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec = getWithCookie(handler, "/admin/audio-analysis/status", nil); rec.Code == 200 {
		t.Fatal("admin status must require a session")
	}
}

func TestBuildAudioAnalysisView(t *testing.T) {
	stats := &storage.AudioAnalysisStats{Eligible: 200, Analyzed: 50, MissingAV: 3, PermanentFailures: 2, RetryPending: 1, RecentAvgMs: 2900}
	v := buildAudioAnalysisView(analysis.Snapshot{Configured: true, Enabled: true, State: analysis.StateRunning, Stats: stats, Pending: 148, ETASeconds: 7200,
		Current:     &analysis.Current{Title: "曲名", Path: "a/b.flac", Mode: "full"},
		Calibration: &analysis.Calibration{Target: 200, Done: 50, FullModeAvailable: true}})
	for _, check := range []struct{ got, want string }{
		{v.StateLabel, "分析中"}, {v.Tone, "busy"},
		{v.ProgressText, "已分析 50 / 200 首（25.0%）· 待分析 148 · 3 首等待唤醒度/效价"},
		{v.CurrentText, "校准分析：曲名（a/b.flac）"},
		{v.ETAText, "最近平均每首 2.9 秒 · 预计还需约 2.0 小时"},
		{v.FailureText, "2 首无法分析（文件变化前不再重试）· 1 首稍后自动重试"},
		{v.CalibrationTxt, "校准进度 50 / 200（先用完整模式分析这些曲目，再拟合唤醒度/效价模型）"},
		{v.SummaryText, "分析中 · 已分析 50 / 200"},
	} {
		if check.got != check.want {
			t.Fatalf("got %q, want %q", check.got, check.want)
		}
	}
	model := buildAudioAnalysisView(analysis.Snapshot{Configured: true, Enabled: true, State: analysis.StateIdle,
		Calibration: &analysis.Calibration{ModelKind: "embedding", ModelSamples: 300, Metrics: json.RawMessage(`{"embedding":{"arousalR":0.85,"valenceR":0.91}}`)}})
	if model.CalibrationTxt != "唤醒度/效价模型已就绪（声学向量回归，300 个样本），交叉验证 唤醒度 r=0.85 / 效价 r=0.91" || model.Tone != "ok" {
		t.Fatalf("model view = %+v", model)
	}
	waiting := buildAudioAnalysisView(analysis.Snapshot{Configured: true, Enabled: true, State: analysis.StateWaitingWindow, Detail: "01:00"})
	if waiting.StateDetail != "将在 01:00（北京时间）开始" {
		t.Fatalf("waiting detail = %q", waiting.StateDetail)
	}
}
