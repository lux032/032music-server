package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/lux032/032music-server/internal/enrichment"
	"github.com/lux032/032music-server/internal/storage"
)

type artistRunView struct {
	ID               int64  `json:"id"`
	Status           string `json:"status"`
	Total            int    `json:"total"`
	Processed        int    `json:"processed"`
	Matched          int    `json:"matched"`
	Review           int    `json:"review"`
	NoResult         int    `json:"noResult"`
	Skipped          int    `json:"skipped"`
	Failed           int    `json:"failed"`
	Current          string `json:"current"`
	ErrorMessage     string `json:"errorMessage"`
	PauseReason      string `json:"pauseReason"`
	WaitSource       string `json:"waitSource"`
	WaitingUntil     string `json:"waitingUntil"`
	WaitTotalMS      int64  `json:"waitTotalMs"`
	BudgetBaselineMS int64  `json:"budgetBaselineMs"`
	Resumable        bool   `json:"resumable"`
}

func artistRunDTO(r storage.DurableArtistRun) artistRunView {
	return artistRunView{r.ID, r.Status, r.Total, r.Processed, r.Matched, r.Review, r.NoResult, r.Skipped, r.Failed, r.Current, r.ErrorMessage, r.PauseReason, r.WaitSource, r.WaitingUntil, r.WaitTotalMS, r.BudgetBaselineMS, r.DurableVersion == 1 && r.Status == "paused"}
}
func writeArtistJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(value)
}
func (a *App) artistRunError(w http.ResponseWriter, err error) {
	if errors.Is(err, storage.ErrArtistRunState) || errors.Is(err, enrichment.ErrRunNotActive) {
		http.Error(w, "任务状态已变化或有其他活动任务", 409)
	} else if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "任务不存在", 404)
	} else {
		a.logger.Error("artist run request", "error", err)
		http.Error(w, "任务操作失败，请稍后重试", 500)
	}
}
func (a *App) handlePauseArtistRun(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	if a.enrichment == nil {
		http.Error(w, "任务服务不可用", 503)
		return
	}
	if err := a.enrichment.PauseArtistMatching(parseInt64(r.PathValue("id"))); err != nil {
		a.artistRunError(w, err)
		return
	}
	redirectWithNotice(w, r, "/admin/matches", "任务已暂停，可手动继续")
}
func (a *App) handleResumeArtistRun(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	if a.enrichment == nil {
		http.Error(w, "任务服务不可用", 503)
		return
	}
	if err := a.enrichment.ResumeArtistMatching(r.Context(), parseInt64(r.PathValue("id"))); err != nil {
		a.artistRunError(w, err)
		return
	}
	redirectWithNotice(w, r, "/admin/matches", "任务已继续，已处理对象不会重复")
}
func (a *App) handleActiveArtistRun(w http.ResponseWriter, r *http.Request) {
	run, err := a.store.UnfinishedDurableArtistRun(r.Context())
	if errors.Is(err, sql.ErrNoRows) {
		writeArtistJSON(w, map[string]any{"run": nil})
		return
	}
	if err != nil {
		a.artistRunError(w, err)
		return
	}
	writeArtistJSON(w, map[string]any{"run": artistRunDTO(run)})
}
func (a *App) handleArtistRunDetail(w http.ResponseWriter, r *http.Request) {
	run, err := a.store.DurableArtistRun(r.Context(), parseInt64(r.PathValue("id")))
	if err != nil {
		a.artistRunError(w, err)
		return
	}
	writeArtistJSON(w, artistRunDTO(run))
}
func (a *App) handleArtistRunList(w http.ResponseWriter, r *http.Request) {
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
	runs, count, err := a.store.ListDurableArtistRuns(r.Context(), limit, offset)
	if err != nil {
		a.artistRunError(w, err)
		return
	}
	views := []artistRunView{}
	for _, run := range runs {
		views = append(views, artistRunDTO(run))
	}
	writeArtistJSON(w, map[string]any{"runs": views, "count": count, "limit": limit, "offset": offset})
}
