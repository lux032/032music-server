package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/lux032/032music-server/internal/enrichment"
	"github.com/lux032/032music-server/internal/storage"
)

// 已匹配艺术家头像补全（B+2）的页面视图与管理路由。复用匹配任务 run-card
// 标记与 admin.js 轮询约定（data-run-kind="artistImage"）。

type artistImageRunView struct {
	ID           int64  `json:"id"`
	Status       string `json:"status"`
	Mode         string `json:"mode"`
	Total        int    `json:"total"`
	Processed    int    `json:"processed"`
	Cached       int    `json:"cached"`
	Candidate    int    `json:"candidate"`
	NoURL        int    `json:"noUrl"`
	Skipped      int    `json:"skipped"`
	Failed       int    `json:"failed"`
	Current      string `json:"current"`
	ErrorMessage string `json:"errorMessage"`
	PauseReason  string `json:"pauseReason"`
	WaitSource   string `json:"waitSource"`
	WaitingUntil string `json:"waitingUntil"`
	WaitTotalMS  int64  `json:"waitTotalMs"`
	Resumable    bool   `json:"resumable"`
}

func artistImageRunDTO(r storage.ArtistImageBackfillRun) artistImageRunView {
	return artistImageRunView{ID: r.ID, Status: r.Status, Mode: r.Mode, Total: r.Total, Processed: r.Processed, Cached: r.Cached, Candidate: r.Candidate, NoURL: r.NoURL, Skipped: r.Skipped, Failed: r.Failed, Current: r.Current, ErrorMessage: r.ErrorMessage, PauseReason: r.PauseReason, WaitSource: r.WaitSource, WaitingUntil: r.WaitingUntil, WaitTotalMS: r.WaitTotalMS, Resumable: r.Status == "paused"}
}

// artistImageBackfillView 是 /admin/matches 页“已匹配艺术家头像补全”区块
// 的数据：统计（只读 DB+磁盘）与活动/最近任务。
type artistImageBackfillView struct {
	Cached, Total     int
	Missing           int
	PendingCount      int
	PendingCandidates []storage.ArtistImageCandidate
	Settings          *storage.ArtistImageSettings
	Active            *artistImageRunView
	Last              *artistImageRunView
}

func (a *App) artistImageRunError(w http.ResponseWriter, err error) {
	if errors.Is(err, storage.ErrArtistImageBackfillState) || errors.Is(err, enrichment.ErrRunNotActive) {
		http.Error(w, "任务状态已变化或有其他活动任务", 409)
	} else if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "任务不存在", 404)
	} else {
		a.logger.Error("artist image backfill request", "error", err)
		http.Error(w, "任务操作失败，请稍后重试", 500)
	}
}

// handleStartArtistImageBackfill 启动一轮已匹配艺术家头像补全。已有活动或
// 暂停任务、没有候选时都以 notice 反馈，不报错页。
func (a *App) handleStartArtistImageBackfill(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if a.enrichment == nil {
		redirectWithNotice(w, r, "/admin/matches", "任务服务不可用")
		return
	}
	mode := storage.NormalizeArtistImageBackfillMode(r.FormValue("mode"))
	if _, err := a.enrichment.StartArtistImageBackfillWithMode(r.Context(), mode); err != nil {
		message := "启动补全失败，请稍后重试"
		switch {
		case errors.Is(err, enrichment.ErrNoArtistImageBackfillCandidates):
			message = "没有需要补全头像的艺术家"
		case errors.Is(err, storage.ErrArtistImageBackfillState) || errors.Is(err, enrichment.ErrRunNotActive):
			message = "已有活动或暂停中的补全任务，请查看任务并手动继续"
			if run, e := a.store.UnfinishedArtistImageBackfillRun(r.Context()); e == nil && storage.ArtistImageBackfillAutoResumeEligible(run) {
				message = autoResumeNotice(run.WaitingUntil)
			}
		default:
			a.logger.Error("start artist image backfill", "error", err)
		}
		redirectWithNotice(w, r, "/admin/matches", message)
		return
	}
	message := "艺术家头像补全任务已启动"
	if mode == "matched" {
		message = "已匹配" + message
	}
	redirectWithNotice(w, r, "/admin/matches", message)
}

func (a *App) handlePauseArtistImageBackfill(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if a.enrichment == nil {
		http.Error(w, "任务服务不可用", 503)
		return
	}
	if err := a.enrichment.PauseArtistImageBackfill(parseInt64(r.PathValue("id"))); err != nil {
		a.artistImageRunError(w, err)
		return
	}
	redirectWithNotice(w, r, "/admin/matches", "补全任务已暂停，可手动继续")
}

func (a *App) handleResumeArtistImageBackfill(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if a.enrichment == nil {
		http.Error(w, "任务服务不可用", 503)
		return
	}
	if err := a.enrichment.ResumeArtistImageBackfill(r.Context(), parseInt64(r.PathValue("id"))); err != nil {
		a.artistImageRunError(w, err)
		return
	}
	redirectWithNotice(w, r, "/admin/matches", "补全任务已继续，已处理对象不会重复")
}

func (a *App) handleCancelArtistImageBackfill(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if a.enrichment == nil {
		http.Error(w, "任务服务不可用", 503)
		return
	}
	if err := a.enrichment.CancelArtistImageBackfill(parseInt64(r.PathValue("id"))); err != nil {
		a.artistImageRunError(w, err)
		return
	}
	redirectWithNotice(w, r, "/admin/matches", "补全任务已停止")
}

// handleActiveArtistImageBackfill 是 run-card 轮询端点：返回当前未完成
// （running/queued/paused）的补全任务，没有时 run 为 null。
func (a *App) handleActiveArtistImageBackfill(w http.ResponseWriter, r *http.Request) {
	run, err := a.store.UnfinishedArtistImageBackfillRun(r.Context())
	if errors.Is(err, sql.ErrNoRows) {
		writeArtistJSON(w, map[string]any{"run": nil})
		return
	}
	if err != nil {
		a.artistImageRunError(w, err)
		return
	}
	writeArtistJSON(w, map[string]any{"run": artistImageRunDTO(run)})
}

func (a *App) handleListArtistImageCandidates(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	items, err := a.store.PendingArtistImageCandidates(r.Context(), limit)
	if err != nil {
		a.logger.Error("list artist image candidates", "error", err)
		http.Error(w, "加载候选头像失败", http.StatusInternalServerError)
		return
	}
	writeArtistJSON(w, map[string]any{"candidates": items})
}

func (a *App) handleArtistImageCandidatePreview(w http.ResponseWriter, r *http.Request) {
	if a.enrichment == nil {
		http.Error(w, "任务服务不可用", http.StatusServiceUnavailable)
		return
	}
	data, mimeType, err := a.enrichment.ArtistImageCandidatePreview(r.Context(), parseInt64(r.PathValue("id")))
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "候选头像预览暂不可用", http.StatusBadGateway)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

func (a *App) handleApproveArtistImageCandidate(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if a.enrichment == nil {
		http.Error(w, "任务服务不可用", http.StatusServiceUnavailable)
		return
	}
	id := parseInt64(r.PathValue("id"))
	if err := a.enrichment.ApproveArtistImageCandidate(r.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "候选头像不存在", http.StatusNotFound)
			return
		}
		a.logger.Error("approve artist image candidate", "candidateId", id, "error", err)
		http.Error(w, "采纳候选头像失败", http.StatusInternalServerError)
		return
	}
	redirectWithNotice(w, r, safeAdminReturnTo(r.FormValue("returnTo"), "/admin/matches"), "已采纳候选头像并缓存到本地")
}

func (a *App) handleRejectArtistImageCandidate(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id := parseInt64(r.PathValue("id"))
	if err := a.store.ReviewArtistImageCandidate(r.Context(), id, "rejected"); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "候选头像不存在", http.StatusNotFound)
			return
		}
		a.logger.Error("reject artist image candidate", "candidateId", id, "error", err)
		http.Error(w, "忽略候选头像失败", http.StatusInternalServerError)
		return
	}
	redirectWithNotice(w, r, safeAdminReturnTo(r.FormValue("returnTo"), "/admin/matches"), "已忽略该候选头像")
}

func (a *App) handleSearchArtistImageCandidate(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if a.enrichment == nil {
		http.Error(w, "任务服务不可用", http.StatusServiceUnavailable)
		return
	}
	artistID := parseInt64(r.PathValue("id"))
	cand, ok, err := a.enrichment.SearchArtistImageCandidateNow(r.Context(), artistID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "艺术家不存在或已有自定义头像", http.StatusNotFound)
		return
	}
	if err != nil {
		a.logger.Error("search artist image candidate", "artistId", artistID, "error", err)
		http.Error(w, "联网搜索头像失败", http.StatusInternalServerError)
		return
	}
	writeArtistJSON(w, map[string]any{"found": ok, "candidate": cand})
}

func (a *App) handleGetArtistImageSettings(w http.ResponseWriter, r *http.Request) {
	st, err := a.store.ArtistImageSettings(r.Context())
	if err != nil {
		a.logger.Error("load artist image settings", "error", err)
		http.Error(w, "读取头像补全设置失败", http.StatusInternalServerError)
		return
	}
	writeArtistJSON(w, map[string]any{
		"bangumiEnabled":       st.BangumiEnabled,
		"deezerEnabled":        st.DeezerEnabled,
		"wikipediaEnabled":     st.WikipediaEnabled,
		"fanartEnabled":        st.FanartEnabled,
		"fanartKeySet":         st.FanartAPIKey != "",
		"webFallbackEnabled":   st.WebFallbackEnabled,
		"webAutoApplyHigh":     st.WebAutoApplyHigh,
		"visionEnabled":        st.VisionEnabled,
		"visionModel":          st.VisionModel,
		"aiModel":              st.AIModel,
		"maxWebRequestsPerRun": st.MaxWebRequestsPerRun,
		"negativeCacheDays":    st.NegativeCacheDays,
	})
}

func (a *App) handleSaveArtistImageSettings(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	cur, err := a.store.ArtistImageSettings(r.Context())
	if err != nil {
		http.Error(w, "读取头像补全设置失败", http.StatusInternalServerError)
		return
	}
	parseBool := func(name string) bool {
		v := strings.ToLower(strings.TrimSpace(r.FormValue(name)))
		return v == "1" || v == "true" || v == "on" || v == "yes"
	}
	cur.BangumiEnabled = parseBool("bangumi_enabled")
	cur.DeezerEnabled = parseBool("deezer_enabled")
	cur.WikipediaEnabled = parseBool("wikipedia_enabled")
	cur.FanartEnabled = parseBool("fanart_enabled")
	if key := strings.TrimSpace(r.FormValue("fanart_api_key")); key != "" {
		cur.FanartAPIKey = key
	} else if parseBool("clear_fanart_api_key") {
		cur.FanartAPIKey = ""
	}
	cur.WebFallbackEnabled = parseBool("web_fallback_enabled")
	cur.WebAutoApplyHigh = parseBool("web_auto_apply_high")
	cur.VisionEnabled = parseBool("vision_enabled")
	cur.VisionModel = strings.TrimSpace(r.FormValue("vision_model"))
	cur.AIModel = strings.TrimSpace(r.FormValue("ai_model"))
	if n, err := strconv.Atoi(strings.TrimSpace(r.FormValue("max_web_requests_per_run"))); err == nil && n >= 0 {
		cur.MaxWebRequestsPerRun = n
	}
	if n, err := strconv.Atoi(strings.TrimSpace(r.FormValue("negative_cache_days"))); err == nil && n >= 1 {
		cur.NegativeCacheDays = n
	}
	if err = a.store.SaveArtistImageSettings(r.Context(), cur); err != nil {
		a.logger.Error("save artist image settings", "error", err)
		http.Error(w, "保存头像补全设置失败", http.StatusInternalServerError)
		return
	}
	redirectWithNotice(w, r, safeAdminReturnTo(r.FormValue("returnTo"), "/admin/matches"), "艺术家头像补全设置已保存")
}
