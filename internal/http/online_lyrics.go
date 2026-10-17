package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/lux032/032music-server/internal/lyrics"
	"github.com/lux032/032music-server/internal/lyricsonline"
	"github.com/lux032/032music-server/internal/storage"
)

const (
	onlineLyricsPath    = "/admin/online-lyrics"
	onlineLyricsPerPage = 50
)

func (a *App) requireOnlineLyrics(w http.ResponseWriter) bool {
	if a.onlineLyrics == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "online_lyrics_unavailable", "Online lyrics are not available on this server.")
		return false
	}
	return true
}

// handleAPILyricsSearch: GET /api/v1/lyrics/search?name=&artist=&durationMs=
// returns candidates from every online source, best match first.
func (a *App) handleAPILyricsSearch(w http.ResponseWriter, r *http.Request) {
	if !a.requireOnlineLyrics(w) {
		return
	}
	q := r.URL.Query()
	query := lyricsonline.Query{Title: strings.TrimSpace(q.Get("name")), Artist: strings.TrimSpace(q.Get("artist"))}
	query.DurationMs, _ = strconv.ParseInt(q.Get("durationMs"), 10, 64)
	if query.Title == "" && query.Artist == "" {
		writeJSON(w, http.StatusOK, map[string]any{"results": []lyricsonline.Candidate{}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), onlineLyricsTimeout)
	defer cancel()
	results, err := a.onlineLyrics.Search(ctx, query)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "online_lyrics_failed", err.Error())
		return
	}
	if results == nil {
		results = []lyricsonline.Candidate{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func (a *App) getRemoteLyrics(w http.ResponseWriter, r *http.Request, source, id string) (lyricsonline.Lyrics, bool) {
	if !a.requireOnlineLyrics(w) {
		return lyricsonline.Lyrics{}, false
	}
	if !a.onlineLyrics.ValidSource(source) || id == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid_source", "Unknown lyrics source or id.")
		return lyricsonline.Lyrics{}, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), onlineLyricsTimeout)
	defer cancel()
	l, err := a.onlineLyrics.Get(ctx, source, id)
	if errors.Is(err, lyricsonline.ErrNoLyrics) {
		writeAPIError(w, http.StatusNotFound, "no_lyrics", "This song has no lyrics.")
		return l, false
	}
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "online_lyrics_failed", err.Error())
		return l, false
	}
	return l, true
}

// handleAPIRemoteLyrics: GET /api/v1/lyrics/remote/{source}/{id} previews one search result.
func (a *App) handleAPIRemoteLyrics(w http.ResponseWriter, r *http.Request) {
	source, id := r.PathValue("source"), r.PathValue("id")
	l, ok := a.getRemoteLyrics(w, r, source, id)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, lyricsResponse(resolvedLyrics{Result: parseLyricsText(l.LRC), Raw: l.LRC, Source: source}))
}

type bindLyricsRequest struct {
	Source string `json:"source"`
	ID     string `json:"id"`
	// Title / Artist of the chosen search result (shown in the admin list).
	Name   string `json:"name"`
	Artist string `json:"artist"`
}

// handleAPIBindLyrics: PUT /api/v1/tracks/{id}/lyrics/source uses one remote
// song's lyrics for this track (overrides local lyrics, for every client).
func (a *App) handleAPIBindLyrics(w http.ResponseWriter, r *http.Request) {
	trackID := parseInt64(r.PathValue("id"))
	var body bindLyricsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "Request body must be JSON {source, id}.")
		return
	}
	if _, err := a.store.LyricsTrackInfo(r.Context(), trackID); a.writeLyricsError(w, trackID, err) {
		return
	}
	l, ok := a.getRemoteLyrics(w, r, body.Source, strings.TrimSpace(body.ID))
	if !ok {
		return
	}
	title, artist := firstNonEmpty(l.Title, body.Name), firstNonEmpty(l.Artist, body.Artist)
	row := storage.OnlineLyrics{TrackID: trackID, Manual: true, Source: l.Source, RemoteID: l.ID, RemoteTitle: title, RemoteArtist: artist,
		Lyrics: l.LRC, TranslationLyrics: l.TranslationLRC}
	if err := a.store.SaveOnlineLyricsFound(r.Context(), row); a.writeLyricsError(w, trackID, err) {
		return
	}
	writeJSON(w, http.StatusOK, lyricsResponse(fromOnlineRow(row)))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// handleAPIClearLyrics: DELETE /api/v1/tracks/{id}/lyrics/source drops the
// online lyrics of a track and stops automatic lookups for it. Local lyrics
// are unaffected; choosing a search result or deleting the row in the admin
// page re-enables online lyrics.
func (a *App) handleAPIClearLyrics(w http.ResponseWriter, r *http.Request) {
	trackID := parseInt64(r.PathValue("id"))
	if err := a.store.SuppressOnlineLyrics(r.Context(), trackID); a.writeLyricsError(w, trackID, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- admin

var onlineLyricsStatusLabels = map[string]string{
	storage.OnlineLyricsFound:      "已找到",
	storage.OnlineLyricsNotFound:   "未找到",
	storage.OnlineLyricsSuppressed: "已清除",
}

type onlineLyricsRowView struct {
	storage.OnlineLyrics
	StatusLabel string
	SourceName  string
	Lines       int
	Synced      bool
}

type onlineLyricsPageData struct {
	Chrome
	Notice    string
	Enabled   bool
	Query     string
	Status    string
	Counts    map[string]int64
	Items     []onlineLyricsRowView
	Total     int64
	Page      int
	Pages     int
	PrevURL   string
	NextURL   string
	ReturnURL string
}

func (a *App) handleOnlineLyricsPage(w http.ResponseWriter, r *http.Request) {
	session, _ := a.sessions.get(r)
	ctx := r.Context()
	q := r.URL.Query()
	data := onlineLyricsPageData{Chrome: a.chromeFor(ctx, session, "console"), Notice: q.Get("notice"), Enabled: a.onlineLyrics != nil,
		Query: strings.TrimSpace(q.Get("q")), Status: q.Get("status")}
	data.Page, _ = strconv.Atoi(q.Get("page"))
	if data.Page < 1 {
		data.Page = 1
	}
	var err error
	if data.Counts, err = a.store.OnlineLyricsCounts(ctx); err != nil {
		a.renderAdminFeatureError(w, "console", err)
		return
	}
	items, total, err := a.store.ListOnlineLyrics(ctx, storage.OnlineLyricsFilter{Query: data.Query, Status: data.Status,
		Limit: onlineLyricsPerPage, Offset: (data.Page - 1) * onlineLyricsPerPage})
	if err != nil {
		a.renderAdminFeatureError(w, "console", err)
		return
	}
	data.Total = total
	for _, it := range items {
		v := onlineLyricsRowView{OnlineLyrics: it, StatusLabel: onlineLyricsStatusLabels[it.Status], SourceName: lyricsonline.SourceName(it.Source)}
		if it.Status == storage.OnlineLyricsFound {
			res := lyrics.ParseLRC(it.Lyrics)
			v.Lines, v.Synced = len(res.Lines), res.Synced
		}
		data.Items = append(data.Items, v)
	}
	data.Pages = int((total + onlineLyricsPerPage - 1) / onlineLyricsPerPage)
	pageURL := func(p int) string {
		v := url.Values{}
		if data.Query != "" {
			v.Set("q", data.Query)
		}
		if data.Status != "" {
			v.Set("status", data.Status)
		}
		v.Set("page", strconv.Itoa(p))
		return onlineLyricsPath + "?" + v.Encode()
	}
	data.ReturnURL = pageURL(data.Page)
	if data.Page > 1 {
		data.PrevURL = pageURL(data.Page - 1)
	}
	if data.Page < data.Pages {
		data.NextURL = pageURL(data.Page + 1)
	}
	a.render(w, http.StatusOK, "online-lyrics.html", data)
}

func onlineLyricsReturn(raw string) string {
	if strings.HasPrefix(raw, onlineLyricsPath) && !strings.Contains(raw, "//") {
		return raw
	}
	return onlineLyricsPath
}

func (a *App) handleDeleteOnlineLyrics(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	ids := formIDs(r)
	back := onlineLyricsReturn(r.FormValue("return"))
	if len(ids) == 0 {
		redirectWithNotice(w, r, back, "没有选择要删除的歌词")
		return
	}
	n, err := a.store.DeleteOnlineLyrics(r.Context(), ids)
	if err != nil {
		redirectWithNotice(w, r, back, "删除失败："+err.Error())
		return
	}
	redirectWithNotice(w, r, back, fmt.Sprintf("已删除 %d 条；这些歌曲下次播放时会重新在线匹配", n))
}

func (a *App) handleClearOnlineLyricsMisses(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	n, err := a.store.ClearOnlineLyricsNotFound(r.Context())
	if err != nil {
		redirectWithNotice(w, r, onlineLyricsPath, "清除失败："+err.Error())
		return
	}
	redirectWithNotice(w, r, onlineLyricsPath, fmt.Sprintf("已清除 %d 条“未找到”记录；这些歌曲下次播放时会立即重新搜索", n))
}

func (a *App) dashboardOnlineLyricsSummary(ctx context.Context) string {
	if a.onlineLyrics == nil {
		return "未启用 · 只使用本地歌词"
	}
	counts, err := a.store.OnlineLyricsCounts(ctx)
	if err != nil {
		return "状态暂不可用"
	}
	return fmt.Sprintf("已缓存 %d 首 · 未找到 %d 首", counts[storage.OnlineLyricsFound], counts[storage.OnlineLyricsNotFound])
}
