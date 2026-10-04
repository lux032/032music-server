package httpapi

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/lux032/032music-server/internal/enrichment"
	"github.com/lux032/032music-server/internal/storage"
)

// 已匹配艺术家头像补全（B+2）的页面视图与管理路由。复用匹配任务 run-card
// 标记与 admin.js 轮询约定（data-run-kind="artistImage"）。

type artistImageRunView struct {
	ID           int64  `json:"id"`
	Status       string `json:"status"`
	Total        int    `json:"total"`
	Processed    int    `json:"processed"`
	Cached       int    `json:"cached"`
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
	return artistImageRunView{ID: r.ID, Status: r.Status, Total: r.Total, Processed: r.Processed, Cached: r.Cached, NoURL: r.NoURL, Skipped: r.Skipped, Failed: r.Failed, Current: r.Current, ErrorMessage: r.ErrorMessage, PauseReason: r.PauseReason, WaitSource: r.WaitSource, WaitingUntil: r.WaitingUntil, WaitTotalMS: r.WaitTotalMS, Resumable: r.Status == "paused"}
}

// artistImageBackfillView 是 /admin/matches 页“已匹配艺术家头像补全”区块
// 的数据：统计（只读 DB+磁盘）与活动/最近任务。
type artistImageBackfillView struct {
	Cached, Total int
	Missing       int
	Active        *artistImageRunView
	Last          *artistImageRunView
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
	if _, err := a.enrichment.StartArtistImageBackfill(r.Context()); err != nil {
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
	redirectWithNotice(w, r, "/admin/matches", "已匹配艺术家头像补全任务已启动")
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
