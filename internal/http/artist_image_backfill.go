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

// 已匹配艺术家头像补全（B+2）与全量历史头像 AI 审查的页面视图与管理路由。
// 复用匹配任务 run-card 标记与 admin.js 轮询约定（data-run-kind="artistImage"）。

type artistImageRunView struct {
	ID           int64  `json:"id"`
	Status       string `json:"status"`
	Mode         string `json:"mode"`
	Total        int    `json:"total"`
	Processed    int    `json:"processed"`
	Cached       int    `json:"cached"`
	Kept         int    `json:"kept"`
	Replaced     int    `json:"replaced"`
	Cleared      int    `json:"cleared"`
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
	return artistImageRunView{
		ID: r.ID, Status: r.Status, Mode: r.Mode,
		Total: r.Total, Processed: r.Processed,
		Cached: r.Cached, Kept: r.Kept, Replaced: r.Replaced, Cleared: r.Cleared,
		Candidate: r.Candidate, NoURL: r.NoURL, Skipped: r.Skipped, Failed: r.Failed,
		Current: r.Current, ErrorMessage: r.ErrorMessage,
		PauseReason: r.PauseReason, WaitSource: r.WaitSource, WaitingUntil: r.WaitingUntil,
		WaitTotalMS: r.WaitTotalMS, Resumable: r.Status == "paused",
	}
}

// artistImageBackfillView 是 /admin/matches 页“艺术家头像补全与 AI 审查”区块
// 的数据：统计（只读 DB+磁盘）、活动/最近任务、待审候选与可撤销替换记录。
type artistImageBackfillView struct {
	Cached, Total      int
	Missing            int
	PendingCount       int
	PendingCandidates  []storage.ArtistImageCandidate
	RecentAuditActions []storage.ArtistImageAuditAction
	WebSearchReady     bool
	WebSearchSummary   string
	Settings           *storage.ArtistImageSettings
	Active             *artistImageRunView
	Last               *artistImageRunView
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

// handleStartArtistImageBackfill 启动一轮头像补全或全量 AI 审查替换任务。
func (a *App) handleStartArtistImageBackfill(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	returnTo := safeAdminReturnTo(r.FormValue("returnTo"), "/admin/matches")
	if a.enrichment == nil {
		redirectWithNotice(w, r, returnTo, "任务服务不可用")
		return
	}
	mode := storage.NormalizeArtistImageBackfillMode(r.FormValue("mode"))
	if _, err := a.enrichment.StartArtistImageBackfillWithMode(r.Context(), mode); err != nil {
		message := "启动补全失败，请稍后重试"
		switch {
		case errors.Is(err, enrichment.ErrNoArtistImageBackfillCandidates):
			message = "没有需要补全或审查头像的艺术家"
		case errors.Is(err, storage.ErrArtistImageBackfillState) || errors.Is(err, enrichment.ErrRunNotActive):
			message = "已有活动或暂停中的补全任务，请查看任务并手动继续"
			if run, e := a.store.UnfinishedArtistImageBackfillRun(r.Context()); e == nil && storage.ArtistImageBackfillAutoResumeEligible(run) {
				message = autoResumeNotice(run.WaitingUntil)
			}
		default:
			a.logger.Error("start artist image backfill", "error", err)
		}
		redirectWithNotice(w, r, returnTo, message)
		return
	}
	message := "艺术家头像补全任务已启动"
	switch mode {
	case "matched":
		message = "已匹配艺术家头像补全任务已启动"
	case "audit_all":
		message = "全量补全与历史头像 AI 审查任务已启动"
	case "audit_force":
		message = "全量历史头像强制重审任务已启动"
	}
	redirectWithNotice(w, r, returnTo, message)
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
	redirectWithNotice(w, r, safeAdminReturnTo(r.FormValue("returnTo"), "/admin/matches"), "补全任务已暂停，可手动继续")
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
	redirectWithNotice(w, r, safeAdminReturnTo(r.FormValue("returnTo"), "/admin/matches"), "补全任务已继续，已处理对象不会重复")
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
	redirectWithNotice(w, r, safeAdminReturnTo(r.FormValue("returnTo"), "/admin/matches"), "补全任务已停止")
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
	w.Header().Set("Cache-Control", "private, max-age=300")
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
	returnTo := safeAdminReturnTo(r.FormValue("returnTo"), "/admin/matches")
	if err := a.enrichment.ApproveArtistImageCandidate(r.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "候选头像不存在", http.StatusNotFound)
			return
		}
		if errors.Is(err, storage.ErrArtistImageBackfillState) {
			redirectWithNotice(w, r, returnTo, "艺术家状态已变化或已有自定义头像，候选头像未写入")
			return
		}
		a.logger.Error("approve artist image candidate", "candidateId", id, "error", err)
		http.Error(w, "采纳候选头像失败", http.StatusInternalServerError)
		return
	}
	redirectWithNotice(w, r, returnTo, "已采纳候选头像并缓存到本地")
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

func (a *App) handleKeepArtistImageCandidate(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id := parseInt64(r.PathValue("id"))
	returnTo := safeAdminReturnTo(r.FormValue("returnTo"), "/admin/matches?tab=avatars")
	if err := a.store.KeepArtistImageCandidate(r.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "待审项不存在", http.StatusNotFound)
			return
		}
		a.logger.Error("keep artist image candidate", "candidateId", id, "error", err)
		http.Error(w, "保存保留决定失败", http.StatusInternalServerError)
		return
	}
	redirectWithNotice(w, r, returnTo, "已确认保留当前头像，后续普通全量审查将跳过")
}

func (a *App) handleClearArtistImageCandidate(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id := parseInt64(r.PathValue("id"))
	returnTo := safeAdminReturnTo(r.FormValue("returnTo"), "/admin/matches?tab=avatars")
	if err := a.store.ClearArtistImageCandidate(r.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "待审项不存在", http.StatusNotFound)
			return
		}
		if errors.Is(err, storage.ErrArtistImageBackfillState) {
			redirectWithNotice(w, r, returnTo, "该艺人已有自定义头像，未执行清空")
			return
		}
		a.logger.Error("clear artist image candidate", "candidateId", id, "error", err)
		http.Error(w, "清空头像失败", http.StatusInternalServerError)
		return
	}
	redirectWithNotice(w, r, returnTo, "已清空该艺人缓存头像（可在任务页撤销恢复）")
}

func (a *App) handleSearchForAuditCandidate(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	returnTo := safeAdminReturnTo(r.FormValue("returnTo"), "/admin/matches?tab=avatars")
	if a.enrichment == nil {
		redirectWithNotice(w, r, returnTo, "任务服务不可用")
		return
	}
	id := parseInt64(r.PathValue("id"))
	cand, err := a.store.ArtistImageCandidateByID(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "待审项不存在", http.StatusNotFound)
		return
	}
	if err != nil {
		a.logger.Error("load audit candidate for search", "candidateId", id, "error", err)
		http.Error(w, "读取待审项失败", http.StatusInternalServerError)
		return
	}
	found, ok, searchErr := a.enrichment.SearchArtistImageCandidateNow(r.Context(), cand.ArtistID)
	if searchErr != nil {
		redirectWithNotice(w, r, returnTo, "联网找新图失败："+enrichment.RedactSourceError(searchErr).Error())
		return
	}
	if !ok || found.ID == 0 {
		redirectWithNotice(w, r, returnTo, "联网未找到更合适的候选头像")
		return
	}
	redirectWithNotice(w, r, returnTo, "已联网搜到新候选头像，请在列表顶部对比并采纳")
}

func (a *App) handleRevertArtistImageAuditAction(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id := parseInt64(r.PathValue("id"))
	returnTo := safeAdminReturnTo(r.FormValue("returnTo"), "/admin/matches?tab=tasks")
	if err := a.store.RevertArtistImageAuditAction(r.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			redirectWithNotice(w, r, returnTo, "未找到该条头像处理记录")
			return
		}
		if errors.Is(err, storage.ErrArtistImageBackfillState) {
			redirectWithNotice(w, r, returnTo, "无法撤销：该记录已撤销，或艺人已合并/已上传自定义头像")
			return
		}
		redirectWithNotice(w, r, returnTo, "撤销失败："+err.Error())
		return
	}
	redirectWithNotice(w, r, returnTo, "已撤销该次 AI 头像变更并恢复原头像")
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
		"auditAutoConfidence":  st.AuditAutoConfidence,
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
	if n, err := strconv.Atoi(strings.TrimSpace(r.FormValue("audit_auto_confidence"))); err == nil && n >= 50 && n <= 100 {
		cur.AuditAutoConfidence = n
	}
	if err = a.store.SaveArtistImageSettings(r.Context(), cur); err != nil {
		a.logger.Error("save artist image settings", "error", err)
		http.Error(w, "保存头像补全设置失败", http.StatusInternalServerError)
		return
	}
	redirectWithNotice(w, r, safeAdminReturnTo(r.FormValue("returnTo"), "/admin/matches"), "艺术家头像补全设置已保存")
}
