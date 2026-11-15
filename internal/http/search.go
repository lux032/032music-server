package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/lux032/032music-server/internal/storage"
)

type searchPageData struct {
	Chrome
	Result             storage.SearchResults
	HasResults         bool
	CategoryLabel      string
	BestURL, BestLabel string
	AllURL             string
	More               map[string]string
	PrevURL, NextURL   string
	Page               int
}

func searchURL(query, category string, page int) string {
	q := url.Values{"q": {query}}
	if category != "" {
		q.Set("category", category)
	}
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	return "/admin/search?" + q.Encode()
}

func (a *App) handleSearchPage(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	category := r.URL.Query().Get("category")
	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 1000000 {
			http.Error(w, "无效页码", http.StatusBadRequest)
			return
		}
		page = n
	}
	result, err := a.store.Search(r.Context(), storage.SearchOptions{Query: query, Category: category, Limit: 24, Offset: (page - 1) * 24})
	if err != nil {
		if errors.Is(err, storage.ErrInvalidSearch) {
			http.Error(w, "搜索词最多 200 个字符，请选择有效的结果类别", http.StatusBadRequest)
			return
		}
		a.renderAdminFeatureError(w, "search", err)
		return
	}
	session, _ := a.sessions.get(r)
	data := searchPageData{Chrome: a.chromeFor(r.Context(), session, "search"), Result: result, Page: page, More: map[string]string{}, AllURL: searchURL(query, "", 1)}
	labels := map[string]string{"artists": "歌手", "albums": "专辑", "tracks": "歌曲", "composers": "作曲家"}
	data.CategoryLabel = labels[category]
	for key := range labels {
		data.More[key] = searchURL(query, key, 1)
	}
	data.HasResults = result.Artists.Total+result.Albums.Total+result.Tracks.Total+result.Composers.Total > 0
	if category != "" {
		total := map[string]int64{"artists": result.Artists.Total, "albums": result.Albums.Total, "tracks": result.Tracks.Total, "composers": result.Composers.Total}[category]
		if page > 1 && int64((page-1)*24) >= total {
			lastPage := 1
			if total > 0 {
				lastPage = int((total-1)/24) + 1
			}
			http.Redirect(w, r, searchURL(query, category, lastPage), http.StatusSeeOther)
			return
		}
		if page > 1 {
			data.PrevURL = searchURL(query, category, page-1)
		}
		if int64(page)*24 < total {
			data.NextURL = searchURL(query, category, page+1)
		}
	}
	if best := result.BestMatch; best != nil {
		data.BestLabel = labels[best.Type]
		id := strconv.FormatInt(best.ID, 10)
		switch best.Type {
		case "artists":
			data.BestURL = "/admin/artists/" + id
		case "composers":
			data.BestURL = "/admin/credits/" + id + "?role=composer"
		case "albums":
			data.BestURL = "/admin/albums/" + id
		case "tracks":
			data.BestURL = "/admin/albums/" + strconv.FormatInt(best.AlbumID, 10) + "#track-" + id
		}
	}
	a.render(w, http.StatusOK, "search.html", data)
}

func (a *App) handleAPISearch(w http.ResponseWriter, r *http.Request) {
	limit, offset := pageValues(r)
	for _, key := range []string{"limit", "offset"} {
		if raw := r.URL.Query().Get(key); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 0 || (key == "limit" && n == 0) {
				writeAPIError(w, 400, "invalid_filter", "invalid "+key)
				return
			}
		}
	}
	result, err := a.store.Search(r.Context(), storage.SearchOptions{Query: r.URL.Query().Get("q"), Category: r.URL.Query().Get("category"), Limit: limit, Offset: offset})
	if err != nil {
		if errors.Is(err, storage.ErrInvalidSearch) {
			writeAPIError(w, 400, "invalid_search", err.Error())
			return
		}
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	if err = a.store.AttachAlbumArtistRefs(r.Context(), result.Albums.Items); err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	if err = a.store.AttachTrackAlbumArtistRefs(r.Context(), result.Tracks.Items); err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *App) handleArtistSearchAliases(w http.ResponseWriter, r *http.Request) {
	admin := strings.HasPrefix(r.URL.Path, "/admin/")
	if admin && !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id, err := a.store.CanonicalArtistID(r.Context(), parseInt64(r.PathValue("id")))
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	if r.Method == http.MethodGet {
		items, err := a.store.ArtistSearchAliases(r.Context(), id)
		if err != nil {
			a.writeFeatureError(w, r, err, "query_failed")
			return
		}
		writeJSON(w, 200, map[string]any{"items": items})
		return
	}
	deleting := r.PathValue("aliasId") != ""
	if deleting {
		err = a.store.DeleteArtistSearchAlias(r.Context(), id, parseInt64(r.PathValue("aliasId")))
	} else {
		var value string
		if admin {
			value = r.FormValue("value")
		} else {
			var body struct {
				Value string `json:"value"`
			}
			if !decode(w, r, &body) {
				return
			}
			value = body.Value
		}
		err = a.store.AddArtistSearchAlias(r.Context(), id, value)
	}
	if err != nil {
		if errors.Is(err, storage.ErrInvalidSearchAlias) {
			if admin {
				redirectWithNotice(w, r, artistProfilePath(id), "搜索别名须为 1 至 200 个字符")
			} else {
				writeAPIError(w, 400, "invalid_alias", err.Error())
			}
			return
		}
		a.writeFeatureError(w, r, err, "update_failed")
		return
	}
	if admin {
		notice := "已添加搜索别名"
		if deleting {
			notice = "已删除搜索别名"
		}
		redirectWithNotice(w, r, artistProfilePath(id)+"#search-aliases", notice)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
