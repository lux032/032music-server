package httpapi

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/lux032/032music-server/internal/enrichment"
	"github.com/lux032/032music-server/internal/storage"
)

// 歌手简介补全的页面视图与管理路由。复用匹配任务 run-card 标记与 admin.js
// 轮询约定（data-run-kind="artistBio"）。

type artistBiographyRunView struct {
	ID           int64  `json:"id"`
	Status       string `json:"status"`
	Total        int    `json:"total"`
	Processed    int    `json:"processed"`
	Filled       int    `json:"filled"`
	Missing      int    `json:"missing"`
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

func artistBiographyRunDTO(r storage.ArtistBiographyBackfillRun) artistBiographyRunView {
	return artistBiographyRunView{ID: r.ID, Status: r.Status, Total: r.Total, Processed: r.Processed, Filled: r.Filled, Missing: r.Missing, Skipped: r.Skipped, Failed: r.Failed, Current: r.Current, ErrorMessage: r.ErrorMessage, PauseReason: r.PauseReason, WaitSource: r.WaitSource, WaitingUntil: r.WaitingUntil, WaitTotalMS: r.WaitTotalMS, Resumable: r.Status == "paused"}
}

// artistBiographyBackfillView 是 /admin/matches 页“歌手简介补全”区块的
// 数据：统计（只读 DB）与活动/最近任务。
type artistBiographyBackfillView struct {
	Missing, Total int
	Active         *artistBiographyRunView
	Last           *artistBiographyRunView
}

func (a *App) artistBiographyRunError(w http.ResponseWriter, err error) {
	if errors.Is(err, storage.ErrArtistBiographyBackfillState) || errors.Is(err, enrichment.ErrRunNotActive) {
		http.Error(w, "任务状态已变化或有其他活动任务", 409)
	} else if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "任务不存在", 404)
	} else {
		a.logger.Error("artist biography backfill request", "error", err)
		http.Error(w, "任务操作失败，请稍后重试", 500)
	}
}

// handleStartArtistBiographyBackfill 启动一轮歌手简介补全。已有活动或暂停
// 任务、没有候选时都以 notice 反馈，不报错页。
func (a *App) handleStartArtistBiographyBackfill(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if a.enrichment == nil {
		redirectWithNotice(w, r, "/admin/matches", "任务服务不可用")
		return
	}
	if _, err := a.enrichment.StartArtistBiographyBackfill(r.Context()); err != nil {
		message := "启动补全失败，请稍后重试"
		switch {
		case errors.Is(err, enrichment.ErrNoArtistBiographyBackfillCandidates):
			message = "没有需要补全简介的歌手"
		case errors.Is(err, storage.ErrArtistBiographyBackfillState) || errors.Is(err, enrichment.ErrRunNotActive):
			message = "已有活动或暂停中的补全任务，请查看任务并手动继续"
			if run, e := a.store.UnfinishedArtistBiographyBackfillRun(r.Context()); e == nil && storage.ArtistBiographyBackfillAutoResumeEligible(run) {
				message = autoResumeNotice(run.WaitingUntil)
			}
		default:
			a.logger.Error("start artist biography backfill", "error", err)
		}
		redirectWithNotice(w, r, "/admin/matches", message)
		return
	}
	redirectWithNotice(w, r, "/admin/matches", "歌手简介补全任务已启动")
}

func (a *App) handlePauseArtistBiographyBackfill(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if a.enrichment == nil {
		http.Error(w, "任务服务不可用", 503)
		return
	}
	if err := a.enrichment.PauseArtistBiographyBackfill(parseInt64(r.PathValue("id"))); err != nil {
		a.artistBiographyRunError(w, err)
		return
	}
	redirectWithNotice(w, r, "/admin/matches", "补全任务已暂停，可手动继续")
}

func (a *App) handleResumeArtistBiographyBackfill(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if a.enrichment == nil {
		http.Error(w, "任务服务不可用", 503)
		return
	}
	if err := a.enrichment.ResumeArtistBiographyBackfill(r.Context(), parseInt64(r.PathValue("id"))); err != nil {
		a.artistBiographyRunError(w, err)
		return
	}
	redirectWithNotice(w, r, "/admin/matches", "补全任务已继续，已处理对象不会重复")
}

func (a *App) handleCancelArtistBiographyBackfill(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if a.enrichment == nil {
		http.Error(w, "任务服务不可用", 503)
		return
	}
	if err := a.enrichment.CancelArtistBiographyBackfill(parseInt64(r.PathValue("id"))); err != nil {
		a.artistBiographyRunError(w, err)
		return
	}
	redirectWithNotice(w, r, "/admin/matches", "补全任务已停止")
}

// handleActiveArtistBiographyBackfill 是 run-card 轮询端点：返回当前未完成
// （running/queued/paused）的补全任务，没有时 run 为 null。
func (a *App) handleActiveArtistBiographyBackfill(w http.ResponseWriter, r *http.Request) {
	run, err := a.store.UnfinishedArtistBiographyBackfillRun(r.Context())
	if errors.Is(err, sql.ErrNoRows) {
		writeArtistJSON(w, map[string]any{"run": nil})
		return
	}
	if err != nil {
		a.artistBiographyRunError(w, err)
		return
	}
	writeArtistJSON(w, map[string]any{"run": artistBiographyRunDTO(run)})
}
