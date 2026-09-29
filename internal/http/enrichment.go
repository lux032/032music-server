package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
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

var errAlbumCandidateConflict = errors.New("没有可写入的作品关联：该条目可能已被拒绝、抑制，或存在作品身份冲突")
var errTrackCandidateConflict = errors.New("track subject candidate conflict")

type enrichmentPageData struct {
	Chrome
	Notice             string
	Runs               []storage.EnrichmentRun
	Artists            []enrichmentArtistReview
	PendingArtistCount int
	TotalPendingCount  int
	AlbumCount         int
	TrackCount         int
	WorkCount          int
	// 批次 8 C1：作品海报缓存状态；PosterTotal<0 表示统计不可用（无管理器）。
	PosterCached          int
	PosterTotal           int
	PosterBackfillRunning bool
	PosterLast            *enrichment.PosterBackfillResult
}

// workReviewCtx 是候选卡片子模板需要的页面级上下文（CSRF、当前分组与过滤）。
type workReviewCtx struct {
	CSRFToken string
	Group     string
	AlbumID   int64
}

type albumCandCard struct {
	Ctx  workReviewCtx
	Cand storage.AlbumSubjectCandidate
}

type trackCandCard struct {
	Ctx  workReviewCtx
	Cand storage.TrackSubjectCandidate
}

type workReviewAlbumGroup struct {
	AlbumID    int64
	AlbumTitle string
	Artist     string
	Year       int
	TrackCount int
	ArtworkURL string
	AlbumCards []albumCandCard
	TrackCards []trackCandCard
}

type workReviewWorkGroup struct {
	WorkTitle  string
	WorkType   string
	AlbumCards []albumCandCard
	TrackCards []trackCandCard
}

type workReviewPageData struct {
	Chrome
	ActiveTab             string // "albums" | "tracks" | "works" | "series"
	Group                 string // "album" | "work"
	AlbumID               int64  // optional filter
	Notice                string
	ConflictWork          *storage.Work
	AlbumSubjectCount     int
	TrackSubjectCount     int
	WorkCandidateCount    int
	SeriesSuggestionCount int
	TotalPendingCount     int
	AlbumSubjects         []storage.AlbumSubjectCandidate
	TrackSubjects         []storage.TrackSubjectCandidate
	WorkReviews           []enrichmentWorkReview
	AlbumGroups           []workReviewAlbumGroup
	WorkGroups            []workReviewWorkGroup
	// 系列建议 Tab（D54）：与作品对齐 Tab 相同，不支持分组切换与 albumId 过滤。
	SeriesGroups    []seriesSuggestionGroup
	SeriesCards     []seriesSuggestionCard
	SeriesTruncated bool // 建议超过 200 条：仅显示前 200 条
}

func (data *workReviewPageData) buildGroups() {
	ctx := workReviewCtx{CSRFToken: data.CSRFToken, Group: data.Group, AlbumID: data.AlbumID}
	wrapAlbum := func(c storage.AlbumSubjectCandidate) albumCandCard {
		return albumCandCard{Ctx: ctx, Cand: c}
	}
	wrapTrack := func(c storage.TrackSubjectCandidate) trackCandCard {
		return trackCandCard{Ctx: ctx, Cand: c}
	}
	if data.Group == "work" {
		workMap := map[string]*workReviewWorkGroup{}
		var order []string
		for _, c := range data.AlbumSubjects {
			title := c.AlbumTitle
			typ := "album"
			if len(c.Tieups) > 0 {
				title = c.Tieups[0].Title
				typ = c.Tieups[0].Type
			}
			g, ok := workMap[title]
			if !ok {
				g = &workReviewWorkGroup{WorkTitle: title, WorkType: typ}
				workMap[title] = g
				order = append(order, title)
			}
			g.AlbumCards = append(g.AlbumCards, wrapAlbum(c))
		}
		for _, c := range data.TrackSubjects {
			title := c.TrackTitle
			typ := "track"
			if len(c.Tieups) > 0 {
				title = c.Tieups[0].Title
				typ = c.Tieups[0].Type
			}
			g, ok := workMap[title]
			if !ok {
				g = &workReviewWorkGroup{WorkTitle: title, WorkType: typ}
				workMap[title] = g
				order = append(order, title)
			}
			g.TrackCards = append(g.TrackCards, wrapTrack(c))
		}
		data.WorkGroups = make([]workReviewWorkGroup, 0, len(order))
		for _, name := range order {
			data.WorkGroups = append(data.WorkGroups, *workMap[name])
		}
		// 不原地修改排序用的 slice：先复制再排序。
		sortedAlbums := append([]storage.AlbumSubjectCandidate(nil), data.AlbumSubjects...)
		sort.Slice(sortedAlbums, func(i, j int) bool {
			ti, tj := sortedAlbums[i].AlbumTitle, sortedAlbums[j].AlbumTitle
			if len(sortedAlbums[i].Tieups) > 0 {
				ti = sortedAlbums[i].Tieups[0].Title
			}
			if len(sortedAlbums[j].Tieups) > 0 {
				tj = sortedAlbums[j].Tieups[0].Title
			}
			return ti < tj
		})
		data.AlbumSubjects = sortedAlbums
		sortedTracks := append([]storage.TrackSubjectCandidate(nil), data.TrackSubjects...)
		sort.Slice(sortedTracks, func(i, j int) bool {
			ti, tj := sortedTracks[i].TrackTitle, sortedTracks[j].TrackTitle
			if len(sortedTracks[i].Tieups) > 0 {
				ti = sortedTracks[i].Tieups[0].Title
			}
			if len(sortedTracks[j].Tieups) > 0 {
				tj = sortedTracks[j].Tieups[0].Title
			}
			return ti < tj
		})
		data.TrackSubjects = sortedTracks
		return
	}

	albumMap := map[int64]*workReviewAlbumGroup{}
	var albumOrder []int64
	for _, c := range data.AlbumSubjects {
		g, ok := albumMap[c.AlbumID]
		if !ok {
			g = &workReviewAlbumGroup{
				AlbumID:    c.AlbumID,
				AlbumTitle: c.AlbumTitle,
				Artist:     c.AlbumArtist,
				Year:       c.AlbumYear,
				TrackCount: c.AlbumTrackCount,
				ArtworkURL: c.AlbumArtworkURL,
			}
			albumMap[c.AlbumID] = g
			albumOrder = append(albumOrder, c.AlbumID)
		}
		g.AlbumCards = append(g.AlbumCards, wrapAlbum(c))
	}
	for _, c := range data.TrackSubjects {
		g, ok := albumMap[c.AlbumID]
		if !ok {
			g = &workReviewAlbumGroup{
				AlbumID:    c.AlbumID,
				AlbumTitle: c.AlbumTitle,
				Artist:     c.TrackArtist,
				ArtworkURL: c.AlbumArtworkURL,
			}
			albumMap[c.AlbumID] = g
			albumOrder = append(albumOrder, c.AlbumID)
		}
		g.TrackCards = append(g.TrackCards, wrapTrack(c))
	}
	data.AlbumGroups = make([]workReviewAlbumGroup, 0, len(albumOrder))
	for _, id := range albumOrder {
		data.AlbumGroups = append(data.AlbumGroups, *albumMap[id])
	}
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

func (a *App) enrichmentReviews(ctx context.Context) ([]enrichmentWorkReview, []enrichmentArtistReview, int, int, error) {
	workIDs, workCount, err := a.store.PendingWorkReviewIDs(ctx)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	var pendingWorks map[int64][]storage.WorkMatchCandidate
	if len(workIDs) > 0 {
		if pendingWorks, err = a.store.PendingWorkMatchCandidates(ctx, workIDs...); err != nil {
			return nil, nil, 0, 0, err
		}
	}
	works, err := a.store.WorksByIDs(ctx, workIDs)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	workReviews := make([]enrichmentWorkReview, 0, len(works))
	for _, work := range works {
		workReviews = append(workReviews, enrichmentWorkReview{Work: work, Candidates: pendingWorks[work.ID]})
	}
	artistIDs, artistCount, err := a.store.PendingArtistReviewIDs(ctx)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	var pendingRelations map[int64][]storage.ArtistRelationCandidate
	if len(artistIDs) > 0 {
		if pendingRelations, err = a.store.PendingArtistRelationCandidates(ctx, artistIDs...); err != nil {
			return nil, nil, 0, 0, err
		}
	}
	artists, err := a.store.ArtistsByIDs(ctx, artistIDs)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	artistReviews := make([]enrichmentArtistReview, 0, len(artists))
	for _, artist := range artists {
		artistReviews = append(artistReviews, enrichmentArtistReview{Artist: artist, Candidates: pendingRelations[artist.ID]})
	}
	return workReviews, artistReviews, workCount, artistCount, nil
}

func (a *App) handleAdminEnrichment(w http.ResponseWriter, r *http.Request) {
	session, _ := a.sessions.get(r)
	data := enrichmentPageData{Chrome: a.chromeFor(r.Context(), session, "enrichment"), Notice: r.URL.Query().Get("notice"), PosterTotal: -1}
	data.Runs, _ = a.store.ListEnrichmentRuns(r.Context(), 30, 0)
	albumCount, trackCount, workCount, seriesCount, _ := a.store.PendingWorkReviewCounts(r.Context())
	data.AlbumCount = albumCount
	data.TrackCount = trackCount
	data.WorkCount = workCount
	data.TotalPendingCount = albumCount + trackCount + workCount + seriesCount
	// 批次 8 C1：海报缓存状态只读本地文件，不访问网络。
	if a.enrichment != nil {
		if cached, total, err := a.enrichment.WorkPosterCacheStats(r.Context()); err == nil {
			data.PosterCached, data.PosterTotal = cached, total
		}
		data.PosterBackfillRunning = a.enrichment.PosterBackfillRunning()
		data.PosterLast = a.enrichment.LastPosterBackfill()
	}
	// D44：艺术家关系候选的审核保留在 /admin/enrichment，不迁到作品关联审核页。
	var reviewErr error
	_, data.Artists, _, data.PendingArtistCount, reviewErr = a.enrichmentReviews(r.Context())
	if reviewErr != nil {
		// 艺术家关系候选是该页的附属区块：查询失败记日志，页面其余部分照常渲染。
		a.logger.Error("enrichment artist reviews", "error", reviewErr)
	}
	a.render(w, http.StatusOK, "enrichment-jobs.html", data)
}

// handleWorkPosterBackfill 手动触发一次缺失海报补全（批次 8 C1）。
func (a *App) handleWorkPosterBackfill(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if a.enrichment == nil {
		redirectWithNotice(w, r, "/admin/enrichment", "增强管理器不可用")
		return
	}
	// L3：手动按钮忽略失败记录，强制重试全部缺失海报。
	if a.enrichment.RetryWorkPosterBackfill() {
		redirectWithNotice(w, r, "/admin/enrichment", "已开始补全缺失的作品海报")
	} else {
		redirectWithNotice(w, r, "/admin/enrichment", "正在补全作品海报，无需重复启动")
	}
}

func (a *App) handleAdminWorkReview(w http.ResponseWriter, r *http.Request) {
	session, _ := a.sessions.get(r)
	tab := r.URL.Query().Get("tab")
	if tab != "tracks" && tab != "works" && tab != "series" {
		tab = "albums"
	}
	group := r.URL.Query().Get("group")
	if group != "work" {
		group = "album"
	}
	albumID := parseInt64(r.URL.Query().Get("albumId"))

	data := workReviewPageData{
		Chrome:    a.chromeFor(r.Context(), session, "work-review"),
		ActiveTab: tab,
		Group:     group,
		AlbumID:   albumID,
		Notice:    r.URL.Query().Get("notice"),
	}
	if conflictID := parseInt64(r.URL.Query().Get("conflictWorkId")); conflictID > 0 {
		if conflict, conflictErr := a.store.WorkByID(r.Context(), conflictID); conflictErr == nil {
			data.ConflictWork = &conflict
		}
	}

	var err error
	var seriesCount int
	data.AlbumSubjectCount, data.TrackSubjectCount, data.WorkCandidateCount, seriesCount, err = a.store.PendingWorkReviewCounts(r.Context())
	if err != nil {
		a.logger.Error("work review counts", "error", err)
		http.Error(w, "work review unavailable", http.StatusInternalServerError)
		return
	}
	data.SeriesSuggestionCount = seriesCount
	// L5：TotalPendingCount 与导航角标同口径，始终是全局总数；带 albumId
	// 过滤时只把专辑/曲目两个 Tab 的计数覆盖为过滤后的数量（M3），总数
	// 不随之缩小（该字段当前不渲染在页面上，导航角标由 chromeFor 单独按
	// 全局口径计算）。
	data.TotalPendingCount = data.AlbumSubjectCount + data.TrackSubjectCount + data.WorkCandidateCount + data.SeriesSuggestionCount
	if albumID > 0 {
		// M3：过滤状态下 Tab 计数显示过滤后的数量（专辑/曲目两个 Tab）。
		data.AlbumSubjectCount, data.TrackSubjectCount, err = a.store.PendingAlbumReviewCounts(r.Context(), albumID)
		if err != nil {
			a.logger.Error("work review album counts", "error", err)
			http.Error(w, "work review unavailable", http.StatusInternalServerError)
			return
		}
	}

	switch tab {
	case "albums":
		cands, err := a.store.PendingAlbumSubjectCandidates(r.Context(), albumID, 200)
		if err != nil {
			a.logger.Error("work review album candidates", "error", err)
			http.Error(w, "work review unavailable", http.StatusInternalServerError)
			return
		}
		data.AlbumSubjects = cands
		data.buildGroups()
	case "tracks":
		cands, err := a.store.PendingTrackSubjectCandidates(r.Context(), albumID, 200)
		if err != nil {
			a.logger.Error("work review track candidates", "error", err)
			http.Error(w, "work review unavailable", http.StatusInternalServerError)
			return
		}
		data.TrackSubjects = cands
		data.buildGroups()
	case "works":
		// 作品对齐候选与专辑无关，按专辑分组/过滤没有意义：忽略 group 与 albumId。
		var err error
		data.WorkReviews, _, _, _, err = a.enrichmentReviews(r.Context())
		if err != nil {
			a.logger.Error("work review work candidates", "error", err)
			http.Error(w, "work review unavailable", http.StatusInternalServerError)
			return
		}
	case "series":
		// 系列建议（D54）：与作品对齐 Tab 相同，忽略 group 与 albumId。
		suggestions, err := a.store.PendingSeriesSuggestions(r.Context(), 200, 0)
		if err != nil {
			a.logger.Error("work review series suggestions", "error", err)
			http.Error(w, "work review unavailable", http.StatusInternalServerError)
			return
		}
		// L2：超过 200 条时提示“仅显示前 200 条”（角标计数与列表同口径）。
		data.SeriesTruncated = data.SeriesSuggestionCount > len(suggestions)
		data.SeriesGroups, data.SeriesCards = a.seriesSuggestionCards(data.CSRFToken, suggestions)
	}

	a.render(w, http.StatusOK, "work-review.html", data)
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
	returnTo := safeAdminReturnTo(r.FormValue("returnTo"), "/admin/work-review?tab=works")
	redirectWithNotice(w, r, returnTo, message)
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

// 批次 8：增强任务的分阶段进度展示。阶段集合由范围推导；计数为 -1 的阶段
// 显示“待统计”（曲目阶段可能有上万首，任务开始时预算太贵，见 phase4.go）。
var enrichmentStageOrder = []struct {
	key, label string
}{
	{"albums", "专辑"},
	{"tracks", "曲目"},
	{"works", "作品"},
	{"series", "系列"},
}

func enrichmentRunStages(run storage.EnrichmentRun) []string {
	switch run.Scope {
	case "all":
		return []string{"albums", "tracks", "works", "series"}
	case "albums":
		return []string{"albums"}
	case "tracks":
		return []string{"tracks"}
	case "album":
		return []string{"albums", "tracks", "works"}
	case "works":
		return []string{"works", "series"}
	case "work":
		return []string{"works"}
	default:
		return nil
	}
}

func enrichmentStageCount(run storage.EnrichmentRun, stage string) int {
	switch stage {
	case "albums":
		return run.StageAlbums
	case "tracks":
		return run.StageTracks
	case "works":
		return run.StageWorks
	case "series":
		// 系列阶段没有单独计数列：进入过该阶段即为 1 个处理单元。
		if run.Stage == "series" {
			return 1
		}
		return -1
	default:
		return -1
	}
}

// enrichmentStageLine 返回类似“阶段：曲目（2/4）· 专辑 145 · 曲目 3,210 ·
// 作品 待统计 · 系列 待统计”的一行说明；旧行（迁移前无阶段数据）返回空串。
func enrichmentStageLine(run storage.EnrichmentRun) string {
	stages := enrichmentRunStages(run)
	if len(stages) == 0 {
		return ""
	}
	hasData := false
	for _, stage := range stages {
		if enrichmentStageCount(run, stage) >= 0 {
			hasData = true
			break
		}
	}
	if !hasData {
		return ""
	}
	labels := map[string]string{}
	for _, item := range enrichmentStageOrder {
		labels[item.key] = item.label
	}
	var b strings.Builder
	if run.Status == "running" && run.Stage != "" {
		position := 0
		for i, stage := range stages {
			if stage == run.Stage {
				position = i + 1
				break
			}
		}
		if position > 0 {
			fmt.Fprintf(&b, "阶段：%s（%d/%d）· ", labels[run.Stage], position, len(stages))
		}
	} else {
		b.WriteString("分阶段：")
	}
	for i, stage := range stages {
		if i > 0 {
			b.WriteString(" · ")
		}
		b.WriteString(labels[stage])
		b.WriteString(" ")
		if count := enrichmentStageCount(run, stage); count >= 0 {
			b.WriteString(formatIntGroup(count))
		} else {
			b.WriteString("待统计")
		}
	}
	return b.String()
}

// formatIntGroup 以千分位渲染计数（曲目阶段可能上万）。
func formatIntGroup(n int) string {
	digits := strconv.Itoa(n)
	if len(digits) <= 3 {
		return digits
	}
	var b strings.Builder
	for i, c := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
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
	albumID := parseInt64(r.PathValue("albumId"))
	candidateID := parseInt64(r.PathValue("candidateId"))
	returnTo := safeAdminReturnTo(r.FormValue("returnTo"), "/admin/work-review?tab=albums")
	var selected []int64
	if accept && r.FormValue("onlySelected") == "1" {
		for _, val := range r.Form["workSubjectIds"] {
			id := parseInt64(val)
			if id > 0 {
				selected = append(selected, id)
			}
		}
		if len(selected) == 0 {
			redirectWithNotice(w, r, returnTo, "请至少选择一部作品")
			return
		}
	}
	err := a.decideAlbumSubject(r.Context(), albumID, candidateID, accept, selected)
	message := "专辑候选已拒绝"
	if accept {
		message = "专辑候选已确认"
	}
	if err != nil {
		message = err.Error()
	}
	redirectWithNotice(w, r, returnTo, message)
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
	returnTo := safeAdminReturnTo(r.FormValue("returnTo"), "/admin/work-review?tab=tracks")
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
			redirectWithNotice(w, r, returnTo, "请至少选择一部作品")
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
	message := "曲目候选已拒绝"
	if accept {
		message = "曲目候选已确认"
	}
	if err != nil {
		message = err.Error()
	}
	redirectWithNotice(w, r, returnTo, message)
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
	returnTo := safeAdminReturnTo(r.FormValue("returnTo"), "/admin/albums/"+strconv.FormatInt(albumID, 10))
	redirectWithNotice(w, r, returnTo, message)
}
