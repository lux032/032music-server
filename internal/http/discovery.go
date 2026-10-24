package httpapi

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/lux032/032music-server/internal/discovery"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

type discoveryAlbumDTO struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	Year       int    `json:"year"`
	TrackCount int    `json:"trackCount"`
	CurrentRG  string `json:"currentRg"`
}
type discoveryItemDTO struct {
	RGID           string              `json:"rgid"`
	Title          string              `json:"title"`
	Artist         string              `json:"artist"`
	Kind           string              `json:"kind"`
	Year           int                 `json:"year"`
	CoverAvailable bool                `json:"coverAvailable"`
	Reasons        []string            `json:"reasons"`
	Possible       []discoveryAlbumDTO `json:"possible"`
	Ignored        bool                `json:"ignored"`
}
type discoveryCountsDTO struct {
	Recommended int `json:"recommended"`
	Possible    int `json:"possible"`
	Acquiring   int `json:"acquiring"`
	Ignored     int `json:"ignored"`
}
type discoveryStatusDTO struct {
	Status       string `json:"status"`
	Generation   int64  `json:"generation"`
	Total        int    `json:"total"`
	Processed    int    `json:"processed"`
	ErrorMessage string `json:"errorMessage"`
	StartedAt    string `json:"startedAt"`
	FinishedAt   string `json:"finishedAt"`
}
type discoveryPageData struct {
	Chrome
	Blocked string
	Rebuild storage.ReleaseGroupRebuild
	Items   []discoveryItemDTO
	Counts  discoveryCountsDTO
	Status  discoveryStatusDTO
}

func discoveryReason(r storage.DiscoveryReason) string {
	switch r.Code {
	case "favorite_artist":
		return "你收藏了 " + r.Artist
	case "plays_90d":
		return fmt.Sprintf("近 90 天你播放了 %s %d 次", r.Artist, r.N)
	case "owned_albums":
		return fmt.Sprintf("你已有 %s 的 %d 张专辑", r.Artist, r.N)
	case "new_release":
		return fmt.Sprintf("%d 年新发行", r.Year)
	case "explore":
		return "探索：你曲库里较少听的 " + r.Artist
	}
	return "来自你曲库中的艺人"
}
func (a *App) discoveryItems(r *http.Request, tab string, limit, offset int) ([]discoveryItemDTO, discoveryCountsDTO, error) {
	rows, c, e := a.discovery.Items(r.Context(), tab, limit, offset)
	items := make([]discoveryItemDTO, 0, len(rows))
	for _, v := range rows {
		artist := v.ArtistCredit
		if artist == "" {
			artist = v.Artist
		}
		d := discoveryItemDTO{RGID: v.RGID, Title: v.Title, Artist: artist, Kind: v.Kind, Year: v.Year, CoverAvailable: v.CoverAvailable, Reasons: []string{}, Possible: []discoveryAlbumDTO{}, Ignored: tab == "ignored"}
		for _, reason := range v.Reasons {
			d.Reasons = append(d.Reasons, discoveryReason(reason))
		}
		for _, p := range v.Possible {
			d.Possible = append(d.Possible, discoveryAlbumDTO{p.ID, p.Title, p.Year, p.TrackCount, p.CurrentRG})
		}
		items = append(items, d)
	}
	return items, discoveryCountsDTO{c.Recommended, c.Possible, 0, c.Ignored}, e
}
func (a *App) handleDiscover(w http.ResponseWriter, r *http.Request) {
	session, _ := a.sessions.get(r)
	d := discoveryPageData{Chrome: a.chromeFor(r.Context(), session, "discover")}
	if e := a.store.IdentityReady(r.Context()); e != nil {
		if !errors.Is(e, storage.ErrIdentityNotReady) {
			a.discoveryError(w, e)
			return
		}
		d.Blocked = "发行组身份尚未就绪，请先重建身份后使用发现功能。"
		var err error
		d.Rebuild, err = a.store.ReleaseGroupRebuildState(r.Context())
		if err != nil {
			a.discoveryError(w, err)
			return
		}
	} else if a.discovery == nil {
		d.Blocked = "发现服务暂不可用。"
	} else {
		var e error
		d.Items, d.Counts, e = a.discoveryItems(r, "recommended", 60, 0)
		if e != nil {
			a.discoveryError(w, e)
			return
		}
		s, e := a.discovery.Status(r.Context())
		if e != nil {
			a.discoveryError(w, e)
			return
		}
		d.Status = discoveryStatus(s)
	}
	a.render(w, 200, "discover.html", d)
}
func discoveryStatus(s storage.DiscoveryStatus) discoveryStatusDTO {
	return discoveryStatusDTO{s.Status, s.Generation, s.Total, s.Processed, s.ErrorMessage, s.StartedAt, s.FinishedAt}
}
func (a *App) discoveryError(w http.ResponseWriter, e error) {
	var replace *storage.ErrReplacesReleaseGroup
	switch {
	case errors.Is(e, storage.ErrIdentityNotReady):
		writeAPIError(w, 409, "identity_not_ready", "请先重建发行组身份")
	case errors.Is(e, discovery.ErrRunning):
		writeAPIError(w, 409, "running", "目录正在刷新")
	case errors.As(e, &replace):
		writeJSON(w, 409, map[string]any{"error": map[string]string{"code": "replaces_rg", "message": "该专辑已关联其他发行组，请确认替换"}, "currentRg": replace.RGID})
	case errors.Is(e, sql.ErrNoRows):
		writeAPIError(w, 404, "not_found", "发行组或本地专辑不存在")
	default:
		a.logger.Error("discovery request", "error", e)
		writeAPIError(w, 500, "discovery_failed", "发现请求失败，请稍后重试")
	}
}
func (a *App) handleDiscoveryJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && !a.validCSRFHeader(r) {
		writeAPIError(w, 403, "csrf_invalid", "CSRF 校验失败")
		return
	}
	if e := a.store.IdentityReady(r.Context()); e != nil {
		a.discoveryError(w, e)
		return
	}
	if a.discovery == nil {
		writeAPIError(w, 503, "unavailable", "发现服务暂不可用")
		return
	}
	switch r.URL.Path {
	case "/admin/discover/status.json":
		s, e := a.discovery.Status(r.Context())
		if e != nil {
			a.discoveryError(w, e)
			return
		}
		writeJSON(w, 200, discoveryStatus(s))
		return
	case "/admin/discover/items.json":
		tab := r.URL.Query().Get("tab")
		if tab == "" {
			tab = "recommended"
		}
		if tab != "recommended" && tab != "possible" && tab != "ignored" {
			writeAPIError(w, 400, "invalid_tab", "无效分类")
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		items, counts, e := a.discoveryItems(r, tab, limit, offset)
		if e != nil {
			a.discoveryError(w, e)
			return
		}
		var html bytes.Buffer
		if e = a.templates.ExecuteTemplate(&html, "discovery-cards", items); e != nil {
			a.discoveryError(w, e)
			return
		}
		writeJSON(w, 200, map[string]any{"items": items, "counts": counts, "html": html.String()})
		return
	case "/admin/discover/refresh":
		g, e := a.discovery.Refresh(r.Context(), true)
		if e != nil {
			a.discoveryError(w, e)
			return
		}
		writeJSON(w, 202, map[string]any{"generation": g})
		return
	case "/admin/discover/refresh/cancel":
		if e := a.discovery.Cancel(r.Context()); e != nil {
			a.discoveryError(w, e)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	id, ok := metadata.NormalizeMusicBrainzID(r.PathValue("rgid"))
	if !ok {
		writeAPIError(w, 400, "invalid_rgid", "无效发行组 ID")
		return
	}
	var e error
	switch r.PathValue("action") {
	case "ignore":
		e = a.store.DiscoveryIgnore(r.Context(), id, r.Method != "DELETE")
	case "owned", "not-same":
		var body struct {
			AlbumID        int64 `json:"albumId"`
			ConfirmReplace bool  `json:"confirmReplace"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) != nil || body.AlbumID <= 0 {
			writeAPIError(w, 400, "invalid_body", "请选择本地专辑")
			return
		}
		if r.PathValue("action") == "owned" {
			e = a.store.DiscoveryOwned(r.Context(), id, body.AlbumID, body.ConfirmReplace)
		} else {
			e = a.store.DiscoveryNotSame(r.Context(), id, body.AlbumID)
		}
	default:
		http.NotFound(w, r)
		return
	}
	if e != nil {
		a.discoveryError(w, e)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (a *App) handleDiscoveryCover(w http.ResponseWriter, r *http.Request) {
	id, ok := metadata.NormalizeMusicBrainzID(r.PathValue("rgid"))
	if !ok {
		writeAPIError(w, 400, "invalid_rgid", "无效发行组 ID")
		return
	}
	if a.discovery == nil {
		http.NotFound(w, r)
		return
	}
	path, mime, e := a.discovery.CachedCover(id)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("Content-Type", mime)
	http.ServeFile(w, r, path)
}
