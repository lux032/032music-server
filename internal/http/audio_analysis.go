package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lux032/032music-server/internal/analysis"
	"github.com/lux032/032music-server/internal/storage"
)

const audioAnalysisPath = "/admin/audio-analysis"

// SetAudioAnalysis attaches the acoustic analysis worker (nil hides the feature).
func (a *App) SetAudioAnalysis(service *analysis.Service) { a.audioAnalysis = service }

// TranscodesActive returns the number of running ffmpeg transcodes (live and cache);
// background analysis yields while it is non-zero.
func (a *App) TranscodesActive() int {
	if a.transcoder == nil {
		return 0
	}
	return int(a.transcoder.active.Load())
}

// audioAnalysisView is the human-readable status shared by the page and its poller.
type audioAnalysisView struct {
	StateLabel     string  `json:"stateLabel"`
	StateDetail    string  `json:"stateDetail"`
	Tone           string  `json:"tone"` // ok | busy | warn | off
	Percent        float64 `json:"percent"`
	ProgressText   string  `json:"progressText"`
	CurrentText    string  `json:"currentText"`
	ETAText        string  `json:"etaText"`
	FailureText    string  `json:"failureText"`
	CalibrationTxt string  `json:"calibrationText"`
	AnalyzerText   string  `json:"analyzerText"`
	LastErrorText  string  `json:"lastErrorText"`
	SummaryText    string  `json:"summaryText"` // one line for the dashboard card
}

type audioAnalysisPageData struct {
	Chrome
	Notice        string
	Status        analysis.Snapshot
	View          audioAnalysisView
	Failures      []storage.AudioAnalysisFailure
	WindowEnabled bool
	WindowStart   string
	WindowEnd     string
}

func audioStateLabel(s analysis.Snapshot) (string, string) {
	switch s.State {
	case analysis.StateNotConfigured:
		return "未部署分析服务", "off"
	case analysis.StateDisabled:
		return "未开启", "off"
	case analysis.StatePaused:
		return "已暂停", "warn"
	case analysis.StateWaitingWindow:
		return "等待运行时间窗", "warn"
	case analysis.StateYielding:
		return "暂时让路", "warn"
	case analysis.StateAnalyzerUnavailable:
		return "分析服务不可用", "warn"
	case analysis.StateAnalyzerLoading:
		return "分析服务正在加载模型", "busy"
	case analysis.StateCalibrating:
		return "正在校准", "busy"
	case analysis.StateFitting:
		return "正在拟合唤醒度/效价模型", "busy"
	case analysis.StateRunning:
		return "分析中", "busy"
	case analysis.StateIdle:
		return "已完成，新歌入库后自动分析", "ok"
	case analysis.StateError:
		return "出错，稍后自动重试", "warn"
	}
	return string(s.State), "off"
}

func formatETA(seconds int64) string {
	switch {
	case seconds <= 0:
		return ""
	case seconds < 3600:
		return fmt.Sprintf("预计还需约 %d 分钟", max(seconds/60, 1))
	case seconds < 48*3600:
		return fmt.Sprintf("预计还需约 %.1f 小时", float64(seconds)/3600)
	default:
		return fmt.Sprintf("预计还需约 %.1f 天", float64(seconds)/86400)
	}
}

func buildAudioAnalysisView(s analysis.Snapshot) audioAnalysisView {
	var v audioAnalysisView
	v.StateLabel, v.Tone = audioStateLabel(s)
	switch s.State {
	case analysis.StateWaitingWindow:
		v.StateDetail = "将在 " + s.Detail + "（北京时间）开始"
	case analysis.StateYielding:
		v.StateDetail = s.Detail + "，结束后自动继续"
	case analysis.StateFitting:
		v.StateDetail = s.Detail
	default:
		if v.Tone == "warn" {
			v.StateDetail = s.Detail
		}
	}
	if st := s.Stats; st != nil {
		if st.Eligible > 0 {
			v.Percent = math.Round(float64(st.Analyzed)/float64(st.Eligible)*1000) / 10
		}
		v.ProgressText = fmt.Sprintf("已分析 %d / %d 首（%.1f%%）· 待分析 %d", st.Analyzed, st.Eligible, v.Percent, s.Pending)
		if st.MissingAV > 0 {
			v.ProgressText += fmt.Sprintf(" · %d 首等待唤醒度/效价", st.MissingAV)
		}
		if st.RetryPending > 0 || st.PermanentFailures > 0 {
			v.FailureText = fmt.Sprintf("%d 首无法分析（文件变化前不再重试）· %d 首稍后自动重试", st.PermanentFailures, st.RetryPending)
		}
		if st.RecentAvgMs > 0 {
			v.ETAText = fmt.Sprintf("最近平均每首 %.1f 秒", float64(st.RecentAvgMs)/1000)
		}
	}
	if eta := formatETA(s.ETASeconds); eta != "" && (s.State == analysis.StateRunning || s.State == analysis.StateCalibrating) {
		if v.ETAText != "" {
			v.ETAText += " · "
		}
		v.ETAText += eta
	}
	if s.Current != nil {
		mode := "分析"
		if s.Current.Mode == "full" {
			mode = "校准分析"
		}
		v.CurrentText = fmt.Sprintf("%s：%s（%s）", mode, s.Current.Title, s.Current.Path)
	}
	if c := s.Calibration; c != nil {
		switch {
		case c.ModelKind != "":
			v.CalibrationTxt = fmt.Sprintf("唤醒度/效价模型已就绪（%s，%d 个样本）%s", map[string]string{"embedding": "声学向量回归", "heads": "分类头回归"}[c.ModelKind], c.ModelSamples, metricsSummary(c.Metrics, c.ModelKind))
		case !c.FullModeAvailable:
			v.CalibrationTxt = "分析服务缺少 musicnn/DEAM 模型，暂不计算唤醒度/效价"
		case c.Target > 0:
			v.CalibrationTxt = fmt.Sprintf("校准进度 %d / %d（先用完整模式分析这些曲目，再拟合唤醒度/效价模型）", min(c.Done, int64(c.Target)), c.Target)
		}
	}
	if s.Analyzer != nil {
		v.AnalyzerText = fmt.Sprintf("%s · 分析版本 %s", s.Analyzer.Backend, s.Analyzer.AnalysisVersion)
	} else if s.Version != "" {
		v.AnalyzerText = "分析版本 " + s.Version
	}
	if s.LastError != "" && s.LastErrorAt != nil {
		v.LastErrorText = fmt.Sprintf("最近错误（%s）：%s", s.LastErrorAt.In(homeZone).Format("01-02 15:04"), s.LastError)
	}
	switch {
	case !s.Configured:
		v.SummaryText = "未部署分析服务"
	case !s.Enabled:
		v.SummaryText = "未开启 · 开启后分析曲库的风格、情绪与声学相似度"
	case s.Stats != nil:
		v.SummaryText = fmt.Sprintf("%s · 已分析 %d / %d", v.StateLabel, s.Stats.Analyzed, s.Stats.Eligible)
	default:
		v.SummaryText = v.StateLabel
	}
	return v
}

func metricsSummary(raw json.RawMessage, kind string) string {
	var metrics map[string]struct {
		ValenceR float64 `json:"valenceR"`
		ArousalR float64 `json:"arousalR"`
	}
	if json.Unmarshal(raw, &metrics) != nil {
		return ""
	}
	m, ok := metrics[kind]
	if !ok {
		return ""
	}
	return fmt.Sprintf("，交叉验证 唤醒度 r=%.2f / 效价 r=%.2f", m.ArousalR, m.ValenceR)
}

func (a *App) audioAnalysisSnapshot(ctx context.Context) (analysis.Snapshot, error) {
	if a.audioAnalysis == nil {
		return analysis.Snapshot{State: analysis.StateNotConfigured}, nil
	}
	return a.audioAnalysis.Snapshot(ctx)
}

func minuteText(m *int) string {
	if m == nil {
		return ""
	}
	return analysis.FormatMinute(*m)
}

func (a *App) handleAudioAnalysisPage(w http.ResponseWriter, r *http.Request) {
	session, _ := a.sessions.get(r)
	snap, err := a.audioAnalysisSnapshot(r.Context())
	if err != nil {
		a.renderAdminFeatureError(w, "console", err)
		return
	}
	data := audioAnalysisPageData{Chrome: a.chromeFor(r.Context(), session, "console"), Notice: r.URL.Query().Get("notice"),
		Status: snap, View: buildAudioAnalysisView(snap), WindowEnabled: snap.WindowStart != nil,
		WindowStart: minuteText(snap.WindowStart), WindowEnd: minuteText(snap.WindowEnd)}
	if data.WindowStart == "" {
		data.WindowStart, data.WindowEnd = "01:00", "08:00"
	}
	if a.audioAnalysis != nil {
		if data.Failures, err = a.audioAnalysis.Failures(r.Context(), 50); err != nil {
			a.logger.Error("list analysis failures", "error", err)
		}
	}
	a.render(w, http.StatusOK, "audio-analysis.html", data)
}

func (a *App) handleAudioAnalysisStatus(w http.ResponseWriter, r *http.Request) {
	snap, err := a.audioAnalysisSnapshot(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": snap, "view": buildAudioAnalysisView(snap)})
}

func parseClock(value string) (int, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(value))
	if err != nil {
		return 0, err
	}
	return t.Hour()*60 + t.Minute(), nil
}

// handleSaveAudioAnalysis saves the switch and run window. Turning analysis on is the
// "start" action: the first full analysis begins immediately (subject to the window).
func (a *App) handleSaveAudioAnalysis(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if a.audioAnalysis == nil || !a.audioAnalysis.Configured() {
		redirectWithNotice(w, r, audioAnalysisPath, "尚未部署分析服务，请先按页面说明配置 MUSIC_SERVER_ANALYZER_URL")
		return
	}
	current, err := a.store.AudioAnalysisSettings(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	next := storage.AudioAnalysisSettings{Enabled: r.FormValue("enabled") != "", Paused: current.Paused}
	if !next.Enabled || !current.Enabled {
		next.Paused = false // enabling starts work; disabling clears a stale pause
	}
	if r.FormValue("window") == "custom" {
		start, err1 := parseClock(r.FormValue("window_start"))
		end, err2 := parseClock(r.FormValue("window_end"))
		if err1 != nil || err2 != nil || start == end {
			redirectWithNotice(w, r, audioAnalysisPath, "运行时间窗无效：开始和结束时间需为 HH:MM 且不能相同")
			return
		}
		next.WindowStart, next.WindowEnd = &start, &end
	}
	if err := a.store.SaveAudioAnalysisSettings(r.Context(), next); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.audioAnalysis.Wake()
	switch {
	case next.Enabled && !current.Enabled && current.FirstEnabledAt == "":
		redirectWithNotice(w, r, audioAnalysisPath, "声学分析已开启，开始首次全量分析；之后新入库的歌曲会自动分析")
	case next.Enabled && !current.Enabled:
		redirectWithNotice(w, r, audioAnalysisPath, "声学分析已开启，继续分析尚未完成的曲目")
	case !next.Enabled && current.Enabled:
		redirectWithNotice(w, r, audioAnalysisPath, "声学分析已关闭，已分析的结果会保留")
	default:
		redirectWithNotice(w, r, audioAnalysisPath, "设置已保存")
	}
}

func (a *App) handleAudioAnalysisPause(paused bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.validCSRF(r) {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}
		current, err := a.store.AudioAnalysisSettings(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !current.Enabled {
			redirectWithNotice(w, r, audioAnalysisPath, "声学分析未开启")
			return
		}
		current.Paused = paused
		if err := a.store.SaveAudioAnalysisSettings(r.Context(), current); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if a.audioAnalysis != nil {
			a.audioAnalysis.Wake()
		}
		if paused {
			redirectWithNotice(w, r, audioAnalysisPath, "已暂停；当前这首完成后停止")
		} else {
			redirectWithNotice(w, r, audioAnalysisPath, "已继续分析")
		}
	}
}

func (a *App) handleAudioAnalysisRetry(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if a.audioAnalysis == nil {
		redirectWithNotice(w, r, audioAnalysisPath, "尚未部署分析服务")
		return
	}
	n, err := a.audioAnalysis.RetryFailures(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	redirectWithNotice(w, r, audioAnalysisPath, "已清除 "+strconv.FormatInt(n, 10)+" 条失败记录，稍后重新分析")
}

// audioAnalysisEnabled is used by /api/v1/capabilities.
func (a *App) audioAnalysisEnabled(ctx context.Context) bool {
	if a.audioAnalysis == nil || !a.audioAnalysis.Configured() {
		return false
	}
	settings, err := a.store.AudioAnalysisSettings(ctx)
	return err == nil && settings.Enabled
}

// dashboardAudioAnalysisView is the one-line summary on the console card.
func (a *App) dashboardAudioAnalysisView(ctx context.Context) audioAnalysisView {
	snap, err := a.audioAnalysisSnapshot(ctx)
	if err != nil {
		a.logger.Error("audio analysis status", "error", err)
		return audioAnalysisView{SummaryText: "状态暂不可用"}
	}
	return buildAudioAnalysisView(snap)
}
