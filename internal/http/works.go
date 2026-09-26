package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/lux032/032music-server/internal/storage"
)

type worksPageData struct {
	Chrome
	Query, Type, Index, Sort, Notice string
	KanaIndex                        bool
	Year, Page, PageCount, PageSize  int
	Total                            int64
	Works                            []storage.Work
	Years                            []int
	PrevURL, NextURL                 string
	Pages                            []pageLink
}

type workPageData struct {
	Chrome
	Notice     string
	Work       storage.Work
	Tracks     []storage.WorkTrack
	Candidates []storage.Track
	Query      string
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
	session, _ := a.sessions.get(r)
	filter := workFilters(r)
	page := int(parseInt64(r.URL.Query().Get("page")))
	if page < 1 {
		page = 1
	}
	filter.Limit, filter.Offset = 36, (page-1)*36
	values, err := a.store.ListWorks(r.Context(), filter)
	if err != nil {
		http.Error(w, "works unavailable", http.StatusInternalServerError)
		return
	}
	total, err := a.store.CountWorks(r.Context(), filter)
	if err != nil {
		http.Error(w, "works unavailable", http.StatusInternalServerError)
		return
	}
	data := worksPageData{Chrome: chromeFor(session, "works"), Query: filter.Query, Type: filter.Type, Index: filter.Index, KanaIndex: isKanaIndex(filter.Index), Sort: filter.Sort, Year: filter.Year, Works: values, Total: total, Page: page, PageSize: 36, Notice: r.URL.Query().Get("notice")}
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
	query := strings.TrimSpace(r.URL.Query().Get("trackQ"))
	candidates, _ := a.store.ListTracks(r.Context(), storage.Filters{Query: query, Limit: 30})
	a.render(w, http.StatusOK, "work.html", workPageData{Chrome: chromeFor(session, "works"), Notice: r.URL.Query().Get("notice"), Work: value, Tracks: tracks, Candidates: candidates, Query: query})
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
	http.Redirect(w, r, "/admin/works/"+strconv.FormatInt(id, 10)+"?notice=曲目关联已移除", http.StatusSeeOther)
}

func workInputFromForm(r *http.Request) storage.WorkInput {
	_ = r.ParseForm()
	return storage.WorkInput{Title: r.FormValue("title"), ReadingTitle: r.FormValue("readingTitle"), TranslatedTitle: r.FormValue("translatedTitle"), Type: r.FormValue("type"), Year: int(parseInt64(r.FormValue("year"))), PosterURL: r.FormValue("posterUrl"), ExternalID: r.FormValue("externalId")}
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
