package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/lux032/032music-server/internal/enrichment"
	"github.com/lux032/032music-server/internal/storage"
)

type worksPageData struct {
	Chrome
	Query, Type, Index, Sort, Notice string
	KanaIndex                        bool
	Year, Page, PageCount, PageSize  int
	Total                            int64
	Rows                             []storage.WorkListRow
	Unreferenced                     []storage.Work
	UnreferencedTotal                int64
	Years                            []int
	PrevURL, NextURL                 string
	Pages                            []pageLink
}

type workPageData struct {
	Chrome
	Notice              string
	Work                storage.Work
	Tracks              []storage.WorkTrack
	Albums              []storage.AlbumWork
	TrackUsages         map[int64][]storage.WorkTrack // D46：专辑 ID → 该专辑中本作品的曲目级关联
	Candidates          []storage.Track
	Query               string
	Series              *storage.WorkSeries
	SeriesMembers       []storage.WorkSeriesMember
	CurrentMemberIndex  int
	CurrentMemberSource string
	SeriesLocked        bool
	AllSeries           []storage.WorkSeries
}

func workFilters(r *http.Request) storage.WorkFilters {
	q := r.URL.Query()
	return storage.WorkFilters{Query: strings.TrimSpace(q.Get("q")), Type: strings.TrimSpace(q.Get("type")), Index: strings.TrimSpace(q.Get("index")), Sort: strings.TrimSpace(q.Get("sort")), Year: int(parseInt64(q.Get("year"))), Limit: int(parseInt64(q.Get("limit"))), Offset: int(parseInt64(q.Get("offset")))}
}

func (a *App) handleAPIWorks(w http.ResponseWriter, r *http.Request) {
	filter := workFilters(r)
	limit, offset := pageValues(r)
	filter.Limit, filter.Offset = limit, offset
	values, err := a.store.ListWorks(r.Context(), filter)
	if err != nil {
		apiResult(w, values, err)
		return
	}
	total, err := a.store.CountWorks(r.Context(), filter)
	if err != nil {
		apiResult(w, values, err)
		return
	}
	writePage(w, values, total, limit, offset)
}

func (a *App) handleAPICreateWork(w http.ResponseWriter, r *http.Request) {
	var input storage.WorkInput
	if !decode(w, r, &input) {
		return
	}
	value, err := a.store.CreateWork(r.Context(), input)
	writeWorkResult(w, http.StatusCreated, value, err)
}

func (a *App) handleAPIWork(w http.ResponseWriter, r *http.Request) {
	value, err := a.store.WorkByID(r.Context(), parseInt64(r.PathValue("id")))
	writeWorkResult(w, http.StatusOK, value, err)
}

func (a *App) handleAPIUpdateWork(w http.ResponseWriter, r *http.Request) {
	var input storage.WorkInput
	if !decode(w, r, &input) {
		return
	}
	value, err := a.store.UpdateWork(r.Context(), parseInt64(r.PathValue("id")), input)
	writeWorkResult(w, http.StatusOK, value, err)
}

func (a *App) handleAPIDeleteWork(w http.ResponseWriter, r *http.Request) {
	err := a.store.DeleteWork(r.Context(), parseInt64(r.PathValue("id")))
	if errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, http.StatusNotFound, "not_found", "Work not found.")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "delete_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleAPIWorkTracks(w http.ResponseWriter, r *http.Request) {
	workID := parseInt64(r.PathValue("id"))
	if _, err := a.store.WorkByID(r.Context(), workID); err != nil {
		writeWorkResult(w, http.StatusOK, storage.Work{}, err)
		return
	}
	values, err := a.store.TracksForWork(r.Context(), workID)
	apiResult(w, values, err)
}

func (a *App) handleAPIAddWorkTrack(w http.ResponseWriter, r *http.Request) {
	var input storage.WorkTrackInput
	if !decode(w, r, &input) {
		return
	}
	if err := a.store.AddWorkTrack(r.Context(), parseInt64(r.PathValue("id")), input); err != nil {
		status := http.StatusInternalServerError
		code := "association_failed"
		if errors.Is(err, storage.ErrInvalidWork) {
			status, code = http.StatusBadRequest, "invalid_association"
		}
		writeAPIError(w, status, code, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleAPIRemoveWorkTrack(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	err := a.store.RemoveWorkTrack(r.Context(), parseInt64(r.PathValue("id")), parseInt64(r.PathValue("trackId")), q.Get("role"), int(parseInt64(q.Get("season"))), int(parseInt64(q.Get("sequence"))))
	if errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, http.StatusNotFound, "not_found", "Work track association not found.")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "delete_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeWorkResult(w http.ResponseWriter, status int, value storage.Work, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, http.StatusNotFound, "not_found", "Work not found.")
		return
	}
	if errors.Is(err, storage.ErrInvalidWork) {
		writeAPIError(w, http.StatusBadRequest, "invalid_work", err.Error())
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "work_failed", err.Error())
		return
	}
	writeJSON(w, status, value)
}

func (a *App) handleWorksPage(w http.ResponseWriter, r *http.Request) {
	rememberSort(w, r, "works")
	session, _ := a.sessions.get(r)
	filter := workFilters(r)
	page := int(parseInt64(r.URL.Query().Get("page")))
	if page < 1 {
		page = 1
	}
	filter.Limit, filter.Offset = 36, (page-1)*36
	rows, err := a.store.ListWorksFolded(r.Context(), filter)
	if err != nil {
		http.Error(w, "works unavailable", http.StatusInternalServerError)
		return
	}
	total, err := a.store.CountWorksFolded(r.Context(), filter)
	if err != nil {
		http.Error(w, "works unavailable", http.StatusInternalServerError)
		return
	}
	data := worksPageData{Chrome: a.chromeFor(r.Context(), session, "works"), Query: filter.Query, Type: filter.Type, Index: filter.Index, KanaIndex: isKanaIndex(filter.Index), Sort: filter.Sort, Year: filter.Year, Rows: rows, Total: total, Page: page, PageSize: 36, Notice: r.URL.Query().Get("notice")}
	data.Unreferenced, data.UnreferencedTotal, err = a.store.UnreferencedProtectedWorks(r.Context())
	if err != nil {
		http.Error(w, "works unavailable", http.StatusInternalServerError)
		return
	}
	data.PageCount = int((total + 35) / 36)
	if data.PageCount < 1 {
		data.PageCount = 1
	}
	setWorksPagination(r, &data)
	a.render(w, http.StatusOK, "works.html", data)
}

func (a *App) handleCreateWork(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	input := workInputFromForm(r)
	value, err := a.store.CreateWork(r.Context(), input)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a.queueWorkPoster(value.ID)
	http.Redirect(w, r, "/admin/works/"+strconv.FormatInt(value.ID, 10)+"?notice=作品已创建", http.StatusSeeOther)
}

func (a *App) handleWorkPage(w http.ResponseWriter, r *http.Request) {
	session, _ := a.sessions.get(r)
	id := parseInt64(r.PathValue("id"))
	value, err := a.store.WorkByID(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "work unavailable", http.StatusInternalServerError)
		return
	}
	tracks, err := a.store.TracksForWork(r.Context(), id)
	if err != nil {
		http.Error(w, "work unavailable", http.StatusInternalServerError)
		return
	}
	albums, err := a.store.AlbumsForWork(r.Context(), id)
	if err != nil {
		http.Error(w, "work unavailable", http.StatusInternalServerError)
		return
	}
	albumSet := map[int64]bool{}
	for _, album := range albums {
		albumSet[album.AlbumID] = true
	}
	// "收录于精选集 / 原创专辑": ONLY tracks where the album itself does NOT have an album-level association!
	standalone := make([]storage.WorkTrack, 0, len(tracks))
	// D46：整张关联专辑下的曲目级用途（只取真实曲目级行，排除专辑级摊下来的 source='album' 行）。
	trackUsages := make(map[int64][]storage.WorkTrack)
	for _, track := range tracks {
		if !albumSet[track.AlbumID] {
			standalone = append(standalone, track)
		} else if track.Source != "album" {
			trackUsages[track.AlbumID] = append(trackUsages[track.AlbumID], track)
		}
	}
	query := strings.TrimSpace(r.URL.Query().Get("trackQ"))
	candidates, _ := a.store.ListTracks(r.Context(), storage.Filters{Query: query, Limit: 30})
	data := workPageData{Chrome: a.chromeFor(r.Context(), session, "works"), Notice: r.URL.Query().Get("notice"), Work: value, Tracks: standalone, Albums: albums, TrackUsages: trackUsages, Candidates: candidates, Query: query}
	if series, members, seriesErr := a.store.SeriesForWork(r.Context(), id); seriesErr == nil {
		seriesCopy := series
		data.Series = &seriesCopy
		data.SeriesMembers = members
		for idx, m := range members {
			if m.Work.ID == id {
				data.CurrentMemberIndex = idx + 1
				data.CurrentMemberSource = m.Source
				break
			}
		}
	} else if !errors.Is(seriesErr, sql.ErrNoRows) {
		http.Error(w, "work unavailable", http.StatusInternalServerError)
		return
	}
	data.SeriesLocked, _ = a.store.WorkSeriesLocked(r.Context(), id)
	if data.Series == nil {
		data.AllSeries, _ = a.store.ListSeries(r.Context())
	}
	a.render(w, http.StatusOK, "work.html", data)
}

func (a *App) handleUpdateWork(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id := parseInt64(r.PathValue("id"))
	if _, err := a.store.UpdateWork(r.Context(), id, workInputFromForm(r)); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a.queueWorkPoster(id)
	http.Redirect(w, r, "/admin/works/"+strconv.FormatInt(id, 10)+"?notice=作品信息已保存", http.StatusSeeOther)
}

func (a *App) handleDeleteWork(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if err := a.store.DeleteWork(r.Context(), parseInt64(r.PathValue("id"))); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/works?notice=作品已删除", http.StatusSeeOther)
}

func (a *App) handleAddWorkTrack(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id := parseInt64(r.PathValue("id"))
	input := storage.WorkTrackInput{TrackID: parseInt64(r.FormValue("trackId")), Role: r.FormValue("role"), Season: int(parseInt64(r.FormValue("season"))), Sequence: int(parseInt64(r.FormValue("sequence")))}
	if err := a.store.AddWorkTrack(r.Context(), id, input); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin/works/"+strconv.FormatInt(id, 10)+"?notice=曲目关联已添加", http.StatusSeeOther)
}

func (a *App) handleRemoveWorkTrack(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id := parseInt64(r.PathValue("id"))
	err := a.store.RemoveWorkTrack(r.Context(), id, parseInt64(r.PathValue("trackId")), r.FormValue("role"), int(parseInt64(r.FormValue("season"))), int(parseInt64(r.FormValue("sequence"))))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// L5：支持站内 returnTo；默认回到作品页的关联专辑锚点。
	target := safeAdminReturnTo(r.FormValue("returnTo"), "/admin/works/"+strconv.FormatInt(id, 10)+"#albums")
	redirectWithNotice(w, r, target, "曲目关联已移除")
}

// handleDetachWorkSeries removes the work from its series and locks it
// against automatic grouping (R3).
func (a *App) handleDetachWorkSeries(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id := parseInt64(r.PathValue("id"))
	// M4：支持站内 returnTo，默认回到作品页。
	fallback := "/admin/works/" + strconv.FormatInt(id, 10)
	if err := a.store.DetachWorkFromSeries(r.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			redirectWithNotice(w, r, safeAdminReturnTo(r.FormValue("returnTo"), fallback), "作品不在任何系列中")
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	redirectWithNotice(w, r, safeAdminReturnTo(r.FormValue("returnTo"), fallback), "已从系列中拆出，自动归组不会再把它加回来")
}

// handleAddWorkSeries manually places the work into an existing series,
// clearing any detach lock.
func (a *App) handleAddWorkSeries(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id := parseInt64(r.PathValue("id"))
	seriesID := parseInt64(r.FormValue("seriesId"))
	if err := a.store.AddWorkToSeries(r.Context(), id, seriesID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "series or work not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	target := safeAdminReturnTo(r.FormValue("returnTo"), "/admin/works/"+strconv.FormatInt(id, 10))
	redirectWithNotice(w, r, target, "已手动加入系列")
}

// handleRenameWorkSeries records a user-chosen series title; the automatic
// pass keeps manual titles (D25).
func (a *App) handleRenameWorkSeries(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	seriesID := parseInt64(r.PathValue("id"))
	if err := a.store.RenameWorkSeries(r.Context(), seriesID, r.FormValue("title")); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	redirect := "/admin/works"
	if series, err := a.store.WorkSeriesByID(r.Context(), seriesID); err == nil && series.RepresentativeWorkID != 0 {
		redirect = "/admin/works/" + strconv.FormatInt(series.RepresentativeWorkID, 10)
	}
	redirectWithNotice(w, r, safeAdminReturnTo(r.FormValue("returnTo"), redirect), "系列已重命名")
}

// handleDissolveWorkSeries removes the series and locks every member so the
// automatic pass never rebuilds it.
func (a *App) handleDissolveWorkSeries(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	seriesID := parseInt64(r.PathValue("id"))
	if err := a.store.DissolveWorkSeries(r.Context(), seriesID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	target := safeAdminReturnTo(r.FormValue("returnTo"), "/admin/works")
	redirectWithNotice(w, r, target, "系列已解散，成员不会再被自动归组")
}

func workInputFromForm(r *http.Request) storage.WorkInput {
	_ = r.ParseForm()
	return storage.WorkInput{Title: r.FormValue("title"), ReadingTitle: r.FormValue("readingTitle"), TranslatedTitle: r.FormValue("translatedTitle"), Type: r.FormValue("type"), OriginalType: r.FormValue("originalType"), Year: int(parseInt64(r.FormValue("year"))), PosterURL: r.FormValue("posterUrl"), ExternalID: r.FormValue("externalId")}
}

func setWorksPagination(r *http.Request, data *worksPageData) {
	makeURL := func(page int) string {
		q := r.URL.Query()
		q.Set("page", strconv.Itoa(page))
		return r.URL.Path + "?" + q.Encode()
	}
	if data.Page > 1 {
		data.PrevURL = makeURL(data.Page - 1)
	}
	if data.Page < data.PageCount {
		data.NextURL = makeURL(data.Page + 1)
	}
	start, end := data.Page-2, data.Page+2
	if start < 1 {
		start = 1
	}
	if end > data.PageCount {
		end = data.PageCount
	}
	for i := start; i <= end; i++ {
		data.Pages = append(data.Pages, pageLink{Number: i, URL: makeURL(i), Current: i == data.Page})
	}
}

// handleWorkPoster serves the locally cached copy of the work's poster so it
// renders under the img-src 'self' CSP.
func (a *App) handleWorkPoster(w http.ResponseWriter, r *http.Request) {
	value, err := a.store.WorkByID(r.Context(), parseInt64(r.PathValue("id")))
	if err != nil || value.PosterURL == "" || a.enrichment == nil {
		http.NotFound(w, r)
		return
	}
	// Only serve the local cache; never proxy to Bangumi on page views. A
	// missing cache entry (e.g. a failed earlier download) is re-queued.
	path, mimeType, err := a.enrichment.CachedWorkPoster(value.PosterURL)
	if err != nil {
		a.enrichment.QueueWorkPoster(value.ID)
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

func (a *App) queueWorkPoster(workID int64) {
	if a.enrichment != nil {
		a.enrichment.QueueWorkPoster(workID)
	}
}

// workPosterURL returns the same-origin poster URL for a work, or "" when the
// poster has not been cached locally yet (the placeholder icon is shown).
func workPosterURL(manager *enrichment.Manager, work storage.Work) string {
	if work.PosterURL == "" || manager == nil {
		return ""
	}
	if _, _, err := manager.CachedWorkPoster(work.PosterURL); err != nil {
		return ""
	}
	return "/admin/works/" + strconv.FormatInt(work.ID, 10) + "/poster?v=" + url.QueryEscape(work.UpdatedAt)
}
