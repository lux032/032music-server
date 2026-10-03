package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/lux032/032music-server/internal/enrichment"
	"github.com/lux032/032music-server/internal/storage"
)

type enrichmentRunView struct {
	ID               int64  `json:"id"`
	Status           string `json:"status"`
	Scope            string `json:"scope"`
	TargetID         int64  `json:"targetId"`
	Force            bool   `json:"force"`
	Total            int    `json:"total"`
	Processed        int    `json:"processed"`
	Succeeded        int    `json:"succeeded"`
	Skipped          int    `json:"skipped"`
	Review           int    `json:"review"`
	Failed           int    `json:"failed"`
	Stage            string `json:"stage"`
	StageAlbums      int    `json:"stageAlbums"`
	StageTracks      int    `json:"stageTracks"`
	StageWorks       int    `json:"stageWorks"`
	Current          string `json:"current"`
	ErrorMessage     string `json:"errorMessage"`
	PauseReason      string `json:"pauseReason"`
	WaitSource       string `json:"waitSource"`
	WaitingUntil     string `json:"waitingUntil"`
	WaitTotalMS      int64  `json:"waitTotalMs"`
	BudgetBaselineMS int64  `json:"budgetBaselineMs"`
	Resumable        bool   `json:"resumable"`
}

func enrichmentRunDTO(r storage.DurableEnrichmentRun) enrichmentRunView {
	return enrichmentRunView{r.ID, r.Status, r.Scope, r.TargetID, r.Force, r.Total, r.Processed, r.Succeeded, r.Skipped, r.Review, r.Failed, r.Stage, r.StageAlbums, r.StageTracks, r.StageWorks, r.Current, r.ErrorMessage, r.PauseReason, r.WaitSource, r.WaitingUntil, r.WaitTotalMS, r.BudgetBaselineMS, r.Version == 1 && r.Status == "paused"}
}
func (a *App) enrichmentRunError(w http.ResponseWriter, err error) {
	if errors.Is(err, storage.ErrEnrichmentRunState) || errors.Is(err, enrichment.ErrRunNotActive) {
		http.Error(w, "任务状态已变化或存在其他活动任务", 409)
	} else if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "任务不存在", 404)
	} else {
		a.logger.Error("enrichment run request", "error", err)
		http.Error(w, "任务操作失败，请稍后重试", 500)
	}
}
func (a *App) handlePauseEnrichmentRun(w http.ResponseWriter, r *http.Request) {
	a.handleEnrichmentControl(w, r, false)
}
func (a *App) handleResumeEnrichmentRun(w http.ResponseWriter, r *http.Request) {
	a.handleEnrichmentControl(w, r, true)
}
func (a *App) handleEnrichmentControl(w http.ResponseWriter, r *http.Request, resume bool) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	if a.enrichment == nil {
		http.Error(w, "任务服务不可用", 503)
		return
	}
	id := parseInt64(r.PathValue("id"))
	var err error
	if resume {
		err = a.enrichment.ResumeRun(r.Context(), id)
	} else {
		err = a.enrichment.PauseRun(id)
	}
	if err != nil {
		// ErrRunNotActive covers both a state conflict and a missing run;
		// distinguish them so callers get 404 for an unknown id.
		if errors.Is(err, enrichment.ErrRunNotActive) {
			if _, lookupErr := a.store.DurableEnrichmentRun(r.Context(), id); errors.Is(lookupErr, sql.ErrNoRows) {
				a.enrichmentRunError(w, lookupErr)
				return
			}
		}
		a.enrichmentRunError(w, err)
		return
	}
	redirectWithNotice(w, r, "/admin/enrichment", "任务状态已更新，已处理对象不会重复")
}
func (a *App) handleActiveEnrichmentRun(w http.ResponseWriter, r *http.Request) {
	run, err := a.store.UnfinishedDurableEnrichmentRun(r.Context())
	if errors.Is(err, sql.ErrNoRows) {
		writeArtistJSON(w, map[string]any{"run": nil})
		return
	}
	if err != nil {
		a.enrichmentRunError(w, err)
		return
	}
	writeArtistJSON(w, map[string]any{"run": enrichmentRunDTO(run)})
}
func (a *App) handleEnrichmentRunDetail(w http.ResponseWriter, r *http.Request) {
	run, err := a.store.DurableEnrichmentRun(r.Context(), parseInt64(r.PathValue("id")))
	if err != nil {
		a.enrichmentRunError(w, err)
		return
	}
	writeArtistJSON(w, enrichmentRunDTO(run))
}
func (a *App) handleEnrichmentRunList(w http.ResponseWriter, r *http.Request) {
	limit, offset := 30, 0
	var err error
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			http.Error(w, "invalid limit", 400)
			return
		}
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil {
			http.Error(w, "invalid offset", 400)
			return
		}
	}
	if limit < 1 || limit > 200 || offset < 0 {
		http.Error(w, "invalid pagination", 400)
		return
	}
	rows, err := a.store.ListEnrichmentRuns(r.Context(), limit, offset)
	if err != nil {
		a.enrichmentRunError(w, err)
		return
	}
	count, err := a.store.CountEnrichmentRuns(r.Context())
	if err != nil {
		a.enrichmentRunError(w, err)
		return
	}
	views := []enrichmentRunView{}
	for _, row := range rows {
		run, e := a.store.DurableEnrichmentRun(r.Context(), row.ID)
		if e != nil {
			a.enrichmentRunError(w, e)
			return
		}
		views = append(views, enrichmentRunDTO(run))
	}
	writeArtistJSON(w, map[string]any{"runs": views, "count": count, "limit": limit, "offset": offset})
}
