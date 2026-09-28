package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

var errAlbumCandidateConflict = errors.New("album subject candidate conflict")
var errTrackCandidateConflict = errors.New("track subject candidate conflict")

type enrichmentPageData struct {
	Chrome
	Notice             string
	Runs               []storage.EnrichmentRun
	Works              []enrichmentWorkReview
	Artists            []enrichmentArtistReview
	AlbumSubjects      []storage.AlbumSubjectCandidate
	TrackSubjects      []storage.TrackSubjectCandidate
	TrackArtists       map[int64]string
	PendingWorkCount   int
	PendingArtistCount int
}

func normalizeEnrichmentRequest(value enrichmentRunRequest) (enrichmentRunRequest, error) {
	value.Scope = strings.ToLower(strings.TrimSpace(value.Scope))
	if value.Scope == "" {
		value.Scope = "all"
	}
	switch value.Scope {
	case "all", "albums", "tracks", "works":
		value.TargetID = 0
	case "work", "album":
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

func (a *App) handleAPICancelEnrichment(w http.ResponseWriter, r *http.Request) {
	id := parseInt64(r.PathValue("id"))
	run, err := a.store.EnrichmentRun(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, http.StatusNotFound, "not_found", "Enrichment run not found.")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	if run.Status != "running" || a.enrichment == nil {
		writeAPIError(w, http.StatusConflict, "run_not_active", "Enrichment run is not active.")
		return
	}
	if err = a.enrichment.CancelRun(id); err != nil {
		writeAPIError(w, http.StatusConflict, "run_not_active", "Enrichment run is not active.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
		if err == nil {
			a.queueWorkPoster(workID)
		}
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
	var conflict *storage.WorkExternalIDConflictError
	if errors.As(err, &conflict) {
		writeAPIError(w, http.StatusConflict, "external_id_conflict", conflict.Error())
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "decision_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) enrichmentReviews(ctx context.Context) ([]enrichmentWorkReview, []enrichmentArtistReview, int, int) {
	workIDs, workCount, _ := a.store.PendingWorkReviewIDs(ctx)
	var pendingWorks map[int64][]storage.WorkMatchCandidate
	if len(workIDs) > 0 {
		pendingWorks, _ = a.store.PendingWorkMatchCandidates(ctx, workIDs...)
	}
	works, _ := a.store.WorksByIDs(ctx, workIDs)
	workReviews := make([]enrichmentWorkReview, 0, len(works))
	for _, work := range works {
		workReviews = append(workReviews, enrichmentWorkReview{Work: work, Candidates: pendingWorks[work.ID]})
	}
	artistIDs, artistCount, _ := a.store.PendingArtistReviewIDs(ctx)
	var pendingRelations map[int64][]storage.ArtistRelationCandidate
	if len(artistIDs) > 0 {
		pendingRelations, _ = a.store.PendingArtistRelationCandidates(ctx, artistIDs...)
	}
	artists, _ := a.store.ArtistsByIDs(ctx, artistIDs)
	artistReviews := make([]enrichmentArtistReview, 0, len(artists))
	for _, artist := range artists {
		artistReviews = append(artistReviews, enrichmentArtistReview{Artist: artist, Candidates: pendingRelations[artist.ID]})
	}
	return workReviews, artistReviews, workCount, artistCount
}

func (a *App) handleAdminEnrichment(w http.ResponseWriter, r *http.Request) {
	session, _ := a.sessions.get(r)
	data := enrichmentPageData{Chrome: chromeFor(session, "enrichment"), Notice: r.URL.Query().Get("notice")}
	data.Runs, _ = a.store.ListEnrichmentRuns(r.Context(), 30, 0)
	data.Works, data.Artists, data.PendingWorkCount, data.PendingArtistCount = a.enrichmentReviews(r.Context())
	data.AlbumSubjects, _ = a.store.PendingAlbumSubjectCandidates(r.Context(), 200)
	data.TrackSubjects, _ = a.store.PendingTrackSubjectCandidates(r.Context(), 200)
	data.TrackArtists = map[int64]string{}
	for _, candidate := range data.TrackSubjects {
		if _, seen := data.TrackArtists[candidate.TrackID]; seen {
			continue
		}
		track, err := a.store.TrackByID(r.Context(), candidate.TrackID)
		if err == nil {
			data.TrackArtists[candidate.TrackID] = track.Artist
		}
	}
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

func (a *App) handleAdminCancelEnrichment(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	message := "任务已停止"
	if a.enrichment == nil {
		message = "任务已结束或不存在"
	} else if err := a.enrichment.CancelRun(parseInt64(r.PathValue("id"))); errors.Is(err, enrichment.ErrRunNotActive) {
		message = "任务已结束或不存在"
	} else if err != nil {
		message = "停止任务失败：" + err.Error()
	}
	redirectWithNotice(w, r, "/admin/enrichment", message)
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
		if err == nil {
			a.queueWorkPoster(workID)
		}
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

func positivePathID(value string) (int64, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("path id must be a positive integer")
	}
	return id, nil
}

func (a *App) handleAPIAlbumSubjectCandidates(w http.ResponseWriter, r *http.Request) {
	albumID, err := positivePathID(r.PathValue("albumId"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", err.Error())
		return
	}
	values, err := a.store.AlbumSubjectCandidates(r.Context(), albumID)
	if errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, http.StatusNotFound, "not_found", "Album not found.")
		return
	}
	apiResult(w, values, err)
}
func (a *App) decideAlbumSubject(ctx context.Context, albumID, candidateID int64, accept bool, selected []int64) error {
	if !accept {
		return a.store.RejectAlbumSubjectCandidate(ctx, albumID, candidateID)
	}
	outcome, ids, err := a.store.ConfirmAlbumSubjectCandidate(ctx, albumID, candidateID, 0, "", false, selected)
	if err == nil && outcome != "succeeded" {
		return errAlbumCandidateConflict
	}
	if err == nil {
		for _, id := range ids {
			a.queueWorkPoster(id)
		}
	}
	return err
}
func (a *App) handleAPIAlbumSubjectDecision(w http.ResponseWriter, r *http.Request, accept bool) {
	var selected []int64
	if accept && r.Body != nil {
		var body struct {
			WorkSubjectIDs []int64 `json:"workSubjectIds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			selected = body.WorkSubjectIDs
		} else if !errors.Is(err, io.EOF) {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	}
	albumID, err := positivePathID(r.PathValue("albumId"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", err.Error())
		return
	}
	candidateID, err := positivePathID(r.PathValue("candidateId"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", err.Error())
		return
	}
	err = a.decideAlbumSubject(r.Context(), albumID, candidateID, accept, selected)
	if errors.Is(err, errAlbumCandidateConflict) || errors.Is(err, storage.ErrCandidateNotPending) {
		writeAPIError(w, http.StatusConflict, "candidate_conflict", err.Error())
		return
	}
	if errors.Is(err, storage.ErrInvalidWork) {
		writeAPIError(w, http.StatusBadRequest, "invalid_work", err.Error())
		return
	}
	writeDecisionResult(w, err)
}
func (a *App) handleAdminAlbumSubjectDecision(w http.ResponseWriter, r *http.Request, accept bool) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	err := a.decideAlbumSubject(r.Context(), parseInt64(r.PathValue("albumId")), parseInt64(r.PathValue("candidateId")), accept, nil)
	message := "专辑候选已拒绝"
	if accept {
		message = "专辑候选已确认"
	}
	if err != nil {
		message = err.Error()
	}
	redirectWithNotice(w, r, "/admin/enrichment", message)
}
func (a *App) handleAPITrackSubjectCandidates(w http.ResponseWriter, r *http.Request) {
	trackID, err := positivePathID(r.PathValue("trackId"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", err.Error())
		return
	}
	values, err := a.store.TrackSubjectCandidates(r.Context(), trackID)
	if errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, http.StatusNotFound, "not_found", "Track not found.")
		return
	}
	apiResult(w, values, err)
}

func (a *App) decideTrackSubject(ctx context.Context, trackID, candidateID int64, accept bool, selected []int64, roles map[int64]string) error {
	if !accept {
		return a.store.RejectTrackSubjectCandidate(ctx, trackID, candidateID)
	}
	if len(selected) == 0 {
		return fmt.Errorf("%w: select at least one work", storage.ErrInvalidWork)
	}
	outcome, ids, err := a.store.ConfirmTrackSubjectCandidate(ctx, trackID, candidateID, 0, "", false, selected, roles)
	if err == nil && outcome != "succeeded" {
		return errTrackCandidateConflict
	}
	if err == nil {
		for _, id := range ids {
			a.queueWorkPoster(id)
		}
	}
	return err
}

func (a *App) handleAPITrackSubjectDecision(w http.ResponseWriter, r *http.Request, accept bool) {
	var selected []int64
	var roles map[int64]string
	if accept && r.Body != nil {
		var body struct {
			WorkSubjectIDs []int64           `json:"workSubjectIds"`
			Roles          map[string]string `json:"roles"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			selected = body.WorkSubjectIDs
			roles = map[int64]string{}
			for key, role := range body.Roles {
				id, e := strconv.ParseInt(key, 10, 64)
				if e != nil || id <= 0 {
					writeAPIError(w, http.StatusBadRequest, "invalid_request", "roles keys must be work subject ids")
					return
				}
				roles[id] = role
			}
		} else if !errors.Is(err, io.EOF) {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	}
	trackID, err := positivePathID(r.PathValue("trackId"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", err.Error())
		return
	}
	candidateID, err := positivePathID(r.PathValue("candidateId"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", err.Error())
		return
	}
	err = a.decideTrackSubject(r.Context(), trackID, candidateID, accept, selected, roles)
	if errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, http.StatusNotFound, "not_found", "Candidate not found.")
		return
	}
	if errors.Is(err, errTrackCandidateConflict) || errors.Is(err, storage.ErrCandidateNotPending) {
		writeAPIError(w, http.StatusConflict, "candidate_conflict", err.Error())
		return
	}
	if errors.Is(err, storage.ErrInvalidWork) {
		writeAPIError(w, http.StatusBadRequest, "invalid_work", err.Error())
		return
	}
	writeDecisionResult(w, err)
}

func (a *App) handleAdminTrackSubjectDecision(w http.ResponseWriter, r *http.Request, accept bool) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	var selected []int64
	var roles map[int64]string
	if accept {
		for _, value := range r.Form["workSubjectIds"] {
			id := parseInt64(value)
			if id > 0 {
				selected = append(selected, id)
			}
		}
		if len(selected) == 0 {
			redirectWithNotice(w, r, "/admin/enrichment", "请至少选择一部作品")
			return
		}
		roles = map[int64]string{}
		for key, values := range r.Form {
			if !strings.HasPrefix(key, "role-") || len(values) == 0 {
				continue
			}
			id := parseInt64(strings.TrimPrefix(key, "role-"))
			if id > 0 {
				roles[id] = values[0]
			}
		}
	}
	err := a.decideTrackSubject(r.Context(), parseInt64(r.PathValue("trackId")), parseInt64(r.PathValue("candidateId")), accept, selected, roles)
	message := "\u66f2\u76ee\u5019\u9009\u5df2\u62d2\u7edd"
	if accept {
		message = "\u66f2\u76ee\u5019\u9009\u5df2\u786e\u8ba4"
	}
	if err != nil {
		message = err.Error()
	}
	redirectWithNotice(w, r, "/admin/enrichment", message)
}

func (a *App) handleAdminAlbumSubjectSearch(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	albumID := parseInt64(r.PathValue("albumId"))
	_, err := a.startEnrichmentRunRequest(r.Context(), enrichmentRunRequest{Scope: "album", TargetID: albumID})
	message := "Bangumi 专辑查询任务已启动"
	if err != nil {
		message = "任务正在运行或无法启动：" + err.Error()
	}
	redirectWithNotice(w, r, "/admin/albums/"+strconv.FormatInt(albumID, 10), message)
}
