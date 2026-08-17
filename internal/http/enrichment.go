package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/lux032/032music-server/internal/enrichment"
	"github.com/lux032/032music-server/internal/storage"
)

type enrichmentRunRequest struct {
	Scope    string `json:"scope"`
	TargetID int64  `json:"targetId"`
	Force    bool   `json:"force"`
}

type enrichmentWorkReview struct {
	Work       storage.Work
	Candidates []storage.WorkMatchCandidate
}

type enrichmentArtistReview struct {
	Artist     storage.Artist
	Candidates []storage.ArtistRelationCandidate
}

type enrichmentPageData struct {
	Username, CSRFToken, Notice string
	Runs                        []storage.EnrichmentRun
	Works                       []enrichmentWorkReview
	Artists                     []enrichmentArtistReview
}

func normalizeEnrichmentRequest(value enrichmentRunRequest) (enrichmentRunRequest, error) {
	value.Scope = strings.ToLower(strings.TrimSpace(value.Scope))
	if value.Scope == "" {
		value.Scope = "all"
	}
	switch value.Scope {
	case "all":
		value.TargetID = 0
	case "album", "work", "artist":
		if value.TargetID <= 0 {
			return value, fmt.Errorf("targetId is required for %s scope", value.Scope)
		}
	default:
		return value, fmt.Errorf("unsupported enrichment scope %q", value.Scope)
	}
	return value, nil
}

func (a *App) startEnrichmentRunRequest(ctx context.Context, request enrichmentRunRequest) (storage.EnrichmentRun, error) {
	if a.startEnrichment != nil {
		return a.startEnrichment(ctx, request)
	}
	if a.enrichment == nil {
		return storage.EnrichmentRun{}, errors.New("enrichment manager is unavailable")
	}
	return a.enrichment.StartRun(ctx, enrichment.RunRequest{Scope: request.Scope, TargetID: request.TargetID, Force: request.Force})
}

func (a *App) handleAPIStartEnrichment(w http.ResponseWriter, r *http.Request) {
	var request enrichmentRunRequest
	if !decode(w, r, &request) {
		return
	}
	request, err := normalizeEnrichmentRequest(request)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_enrichment_request", err.Error())
		return
	}
	run, err := a.startEnrichmentRunRequest(r.Context(), request)
	if err != nil {
		writeAPIError(w, http.StatusConflict, "enrichment_not_started", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

func (a *App) handleAPIEnrichmentRuns(w http.ResponseWriter, r *http.Request) {
	limit, offset := pageValues(r)
	values, err := a.store.ListEnrichmentRuns(r.Context(), limit, offset)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": values})
}

func (a *App) handleAPIEnrichmentRun(w http.ResponseWriter, r *http.Request) {
	value, err := a.store.EnrichmentRun(r.Context(), parseInt64(r.PathValue("id")))
	if errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, http.StatusNotFound, "not_found", "Enrichment run not found.")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (a *App) handleAPIWorkEnrichmentCandidates(w http.ResponseWriter, r *http.Request) {
	values, err := a.store.WorkMatchCandidates(r.Context(), parseInt64(r.PathValue("workId")))
	apiResult(w, values, err)
}

func (a *App) handleAPIWorkCandidateDecision(w http.ResponseWriter, r *http.Request, status string) {
	workID := parseInt64(r.PathValue("workId"))
	candidateID := parseInt64(r.PathValue("candidateId"))
	var err error
	if status == "confirmed" {
		err = a.store.ConfirmWorkMatchCandidate(r.Context(), workID, candidateID, 0)
	} else {
		err = a.store.SetWorkMatchCandidateStatus(r.Context(), workID, candidateID, status)
	}
	writeDecisionResult(w, err)
}

func (a *App) handleAPIArtistRelationCandidates(w http.ResponseWriter, r *http.Request) {
	values, err := a.store.ArtistRelationCandidates(r.Context(), parseInt64(r.PathValue("artistId")))
	apiResult(w, values, err)
}

func (a *App) handleAPIArtistRelationDecision(w http.ResponseWriter, r *http.Request, status string) {
	err := a.store.SetArtistRelationCandidateStatus(r.Context(), parseInt64(r.PathValue("artistId")), parseInt64(r.PathValue("candidateId")), status)
	writeDecisionResult(w, err)
}

func writeDecisionResult(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, http.StatusNotFound, "not_found", "Candidate not found.")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "decision_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) enrichmentReviews(ctx context.Context) ([]enrichmentWorkReview, []enrichmentArtistReview) {
	works, _ := a.store.ListWorks(ctx, storage.WorkFilters{Limit: 100})
	workReviews := make([]enrichmentWorkReview, 0)
	for _, work := range works {
		values, _ := a.store.WorkMatchCandidates(ctx, work.ID)
		pending := pendingWorkCandidates(values)
		if len(pending) > 0 {
			workReviews = append(workReviews, enrichmentWorkReview{Work: work, Candidates: pending})
		}
	}
	artists, _ := a.store.ListArtists(ctx, storage.Filters{Limit: 100})
	artistReviews := make([]enrichmentArtistReview, 0)
	for _, artist := range artists {
		values, _ := a.store.ArtistRelationCandidates(ctx, artist.ID)
		pending := pendingArtistRelations(values)
		if len(pending) > 0 {
			artistReviews = append(artistReviews, enrichmentArtistReview{Artist: artist, Candidates: pending})
		}
	}
	return workReviews, artistReviews
}

func pendingWorkCandidates(values []storage.WorkMatchCandidate) []storage.WorkMatchCandidate {
	result := make([]storage.WorkMatchCandidate, 0, len(values))
	for _, value := range values {
		if value.Status == "candidate" {
			result = append(result, value)
		}
	}
	return result
}

func pendingArtistRelations(values []storage.ArtistRelationCandidate) []storage.ArtistRelationCandidate {
	result := make([]storage.ArtistRelationCandidate, 0, len(values))
	for _, value := range values {
		if value.Status == "candidate" {
			result = append(result, value)
		}
	}
	return result
}

func (a *App) handleAdminEnrichment(w http.ResponseWriter, r *http.Request) {
	session, _ := a.sessions.get(r)
	data := enrichmentPageData{Username: session.Username, CSRFToken: session.CSRFToken, Notice: r.URL.Query().Get("notice")}
	data.Runs, _ = a.store.ListEnrichmentRuns(r.Context(), 30, 0)
	data.Works, data.Artists = a.enrichmentReviews(r.Context())
	a.render(w, http.StatusOK, "enrichment-jobs.html", data)
}

func (a *App) handleAdminStartEnrichment(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	request, err := normalizeEnrichmentRequest(enrichmentRunRequest{Scope: r.FormValue("scope"), TargetID: parseInt64(r.FormValue("targetId")), Force: r.FormValue("force") != ""})
	if err == nil {
		_, err = a.startEnrichmentRunRequest(r.Context(), request)
	}
	if err != nil {
		redirectWithNotice(w, r, "/admin/enrichment", err.Error())
		return
	}
	redirectWithNotice(w, r, "/admin/enrichment", "元数据增强任务已启动")
}

func (a *App) handleAdminWorkCandidateDecision(w http.ResponseWriter, r *http.Request, status string) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	workID := parseInt64(r.PathValue("workId"))
	candidateID := parseInt64(r.PathValue("candidateId"))
	var err error
	if status == "confirmed" {
		err = a.store.ConfirmWorkMatchCandidate(r.Context(), workID, candidateID, 0)
	} else {
		err = a.store.SetWorkMatchCandidateStatus(r.Context(), workID, candidateID, status)
	}
	message := "作品候选已" + map[string]string{"confirmed": "确认", "rejected": "拒绝"}[status]
	if err != nil {
		message = err.Error()
	}
	redirectWithNotice(w, r, "/admin/enrichment", message)
}

func (a *App) handleAdminArtistRelationDecision(w http.ResponseWriter, r *http.Request, status string) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	err := a.store.SetArtistRelationCandidateStatus(r.Context(), parseInt64(r.PathValue("artistId")), parseInt64(r.PathValue("candidateId")), status)
	message := "艺术家关系候选已" + map[string]string{"confirmed": "确认", "rejected": "拒绝"}[status]
	if err != nil {
		message = err.Error()
	}
	redirectWithNotice(w, r, "/admin/enrichment", message)
}

func enrichmentRunProgress(run storage.EnrichmentRun) int {
	if run.Total <= 0 {
		return 0
	}
	value := run.Processed * 100 / run.Total
	if value > 100 {
		return 100
	}
	return value
}

func enrichmentTargetLabel(run storage.EnrichmentRun) string {
	if run.TargetID == 0 {
		return run.Scope
	}
	return run.Scope + " #" + strconv.FormatInt(run.TargetID, 10)
}
