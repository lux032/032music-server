package httpapi

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/lux032/032music-server/internal/loudness"
	"github.com/lux032/032music-server/internal/storage"
)

const loudnessPath = "/admin/loudness"

// SetLoudness attaches the background EBU R128 loudness worker.
func (a *App) SetLoudness(service *loudness.Service) { a.loudness = service }

type loudnessView struct {
	StateLabel    string  `json:"stateLabel"`
	StateDetail   string  `json:"stateDetail"`
	Tone          string  `json:"tone"` // ok | busy | warn | off
	Percent       float64 `json:"percent"`
	ProgressText  string  `json:"progressText"`
	MedianText    string  `json:"medianText"`
	CurrentText   string  `json:"currentText"`
	ETAText       string  `json:"etaText"`
	FailureText   string  `json:"failureText"`
	LastErrorText string  `json:"lastErrorText"`
	WorkerText    string  `json:"workerText"`
	SummaryText   string  `json:"summaryText"`
}

type loudnessPageData struct {
	Chrome
	Notice   string
	Status   loudness.Snapshot
	View     loudnessView
	Failures []storage.AudioLoudnessFailure
}

func loudnessStateLabel(s loudness.Snapshot) (string, string) {
	switch s.State {
	case loudness.StateUnavailable:
		return "不可用（缺少 ffmpeg ebur128）", "off"
	case loudness.StatePaused:
		return "已暂停", "warn"
	case loudness.StateYielding:
		return "暂时让路", "warn"
	case loudness.StateRunning:
		return "分析中", "busy"
	case loudness.StateIdle:
		return "已完成，新歌入库后自动分析", "ok"
	case loudness.StateError:
		return "出错，稍后自动重试", "warn"
	default:
		return string(s.State), "off"
	}
}

func buildLoudnessView(s loudness.Snapshot) loudnessView {
	var v loudnessView
	v.StateLabel, v.Tone = loudnessStateLabel(s)
	switch s.State {
	case loudness.StateYielding:
		v.StateDetail = s.Detail + "，结束后自动继续"
	default:
		if v.Tone == "warn" || v.Tone == "off" {
			v.StateDetail = s.Detail
		}
	}
	if st := s.Stats; st != nil {
		if st.Eligible > 0 {
			v.Percent = math.Round(float64(st.Analyzed)/float64(st.Eligible)*1000) / 10
		}
		v.ProgressText = fmt.Sprintf("已分析 %d / %d 首（%.1f%%）· 待分析 %d", st.Analyzed, st.Eligible, v.Percent, s.Pending)
		if st.LibraryMedianLUFS != nil {
			v.MedianText = fmt.Sprintf("当前曲库响度中位数：%.1f LUFS", *st.LibraryMedianLUFS)
		}
		if st.RetryPending > 0 || st.PermanentFailures > 0 {
			v.FailureText = fmt.Sprintf("%d 首无法分析（静音或损坏，文件变化前不再重试）· %d 首稍后自动重试", st.PermanentFailures, st.RetryPending)
		}
		if st.RecentAvgMs > 0 {
			v.ETAText = fmt.Sprintf("最近平均每首 %.1f 秒", float64(st.RecentAvgMs)/1000)
		}
	}
	if eta := formatETA(s.ETASeconds); eta != "" && s.State == loudness.StateRunning {
		if v.ETAText != "" {
			v.ETAText += " · "
		}
		v.ETAText += eta
	}
	if len(s.Current) > 0 {
		parts := make([]string, 0, len(s.Current))
		for _, cur := range s.Current {
			parts = append(parts, fmt.Sprintf("%s（%s）", cur.Title, cur.Path))
		}
		v.CurrentText = "正在测量：" + strings.Join(parts, " / ")
	}
	if s.Workers > 0 {
		v.WorkerText = fmt.Sprintf("并发 %d 个 worker（每进程单线程 ffmpeg ebur128=peak=true）", s.Workers)
	}
	if s.LastError != "" && s.LastErrorAt != nil {
		v.LastErrorText = fmt.Sprintf("最近错误（%s）：%s", s.LastErrorAt.In(homeZone).Format("01-02 15:04"), s.LastError)
	}
	switch {
	case !s.Available && (s.Stats == nil || s.Stats.Analyzed == 0):
		v.SummaryText = "不可用（缺少 ffmpeg ebur128）"
	case s.Stats != nil && s.Stats.LibraryMedianLUFS != nil:
		v.SummaryText = fmt.Sprintf("%s · 已分析 %d / %d · 中位数 %.1f LUFS", v.StateLabel, s.Stats.Analyzed, s.Stats.Eligible, *s.Stats.LibraryMedianLUFS)
	case s.Stats != nil:
		v.SummaryText = fmt.Sprintf("%s · 已分析 %d / %d", v.StateLabel, s.Stats.Analyzed, s.Stats.Eligible)
	default:
		v.SummaryText = v.StateLabel
	}
	return v
}

func (a *App) loudnessSnapshot(ctx context.Context) (loudness.Snapshot, error) {
	if a.loudness == nil {
		return loudness.Snapshot{State: loudness.StateUnavailable, Detail: "未初始化响度分析服务"}, nil
	}
	return a.loudness.Snapshot(ctx)
}

func (a *App) capabilitiesLoudness(ctx context.Context) (bool, map[string]any) {
	if a.loudness == nil {
		return false, map[string]any{
			"available":      false,
			"analyzedTracks": 0,
			"totalTracks":    0,
		}
	}
	avail, st, err := a.loudness.CapabilitiesSummary(ctx)
	if err != nil {
		a.logger.Warn("loudness capabilities summary", "error", err)
		return a.loudness.Available(), map[string]any{
			"available":      a.loudness.Available(),
			"analyzedTracks": 0,
			"totalTracks":    0,
		}
	}
	payload := map[string]any{
		"available":      avail,
		"analyzedTracks": st.Analyzed,
		"totalTracks":    st.Eligible,
	}
	if st.LibraryMedianLUFS != nil {
		payload["libraryMedianLufs"] = *st.LibraryMedianLUFS
	}
	return avail, payload
}

func (a *App) handleLoudnessPage(w http.ResponseWriter, r *http.Request) {
	session, _ := a.sessions.get(r)
	snap, err := a.loudnessSnapshot(r.Context())
	if err != nil {
		a.renderAdminFeatureError(w, "console", err)
		return
	}
	data := loudnessPageData{
		Chrome: a.chromeFor(r.Context(), session, "loudness"),
		Notice: r.URL.Query().Get("notice"),
		Status: snap,
		View:   buildLoudnessView(snap),
	}
	if a.loudness != nil {
		if data.Failures, err = a.loudness.Failures(r.Context(), 50); err != nil {
			a.logger.Error("list loudness failures", "error", err)
		}
	}
	a.render(w, http.StatusOK, "loudness.html", data)
}

func (a *App) handleLoudnessStatus(w http.ResponseWriter, r *http.Request) {
	snap, err := a.loudnessSnapshot(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": snap, "view": buildLoudnessView(snap)})
}

func (a *App) handleLoudnessPause(paused bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.validCSRF(r) {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}
		if a.loudness == nil {
			redirectWithNotice(w, r, loudnessPath, "响度分析服务未启用")
			return
		}
		if err := a.loudness.SetPaused(r.Context(), paused); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if paused {
			redirectWithNotice(w, r, loudnessPath, "已暂停响度分析；当前在途曲目完成后停止")
		} else {
			redirectWithNotice(w, r, loudnessPath, "已继续响度分析")
		}
	}
}

func (a *App) handleLoudnessRetry(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if a.loudness == nil {
		redirectWithNotice(w, r, loudnessPath, "响度分析服务未启用")
		return
	}
	n, err := a.loudness.RetryFailures(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	redirectWithNotice(w, r, loudnessPath, "已清除 "+strconv.FormatInt(n, 10)+" 条失败记录，稍后重新测量")
}

func (a *App) dashboardLoudnessView(ctx context.Context) loudnessView {
	snap, err := a.loudnessSnapshot(ctx)
	if err != nil {
		a.logger.Error("loudness dashboard status", "error", err)
		return loudnessView{SummaryText: "状态暂不可用"}
	}
	return buildLoudnessView(snap)
}
