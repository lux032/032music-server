package httpapi

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

const adminFeaturePageSize = 50

type favoritesPageData struct {
	Chrome
	Notice                 string
	Albums                 []storage.Album
	Tracks                 []storage.Track
	AlbumTotal, TrackTotal int64
}

type playlistsPageData struct {
	Chrome
	Notice    string
	Playlists []storage.Playlist
	Total     int64
}

type playlistPageData struct {
	Chrome
	Notice, Query string
	Detail        storage.PlaylistDetail
	Candidates    []storage.Track
}

type playbackPageData struct {
	Chrome
	Notice           string
	History          []storage.PlaybackRecord
	Total            int64
	Page, PageCount  int
	PrevURL, NextURL string
	Pages            []pageLink
}

func (a *App) handleAdminFavorites(w http.ResponseWriter, r *http.Request) {
	session, _ := a.sessions.get(r)
	albums, albumTotal, err := a.store.FavoriteAlbums(r.Context(), 500, 0)
	if err != nil {
		a.renderAdminFeatureError(w, "favorites", err)
		return
	}
	tracks, trackTotal, err := a.store.FavoriteTracks(r.Context(), 500, 0)
	if err != nil {
		a.renderAdminFeatureError(w, "favorites", err)
		return
	}
	a.render(w, http.StatusOK, "favorites.html", favoritesPageData{
		Chrome: a.chromeFor(r.Context(), session, "favorites"), Notice: r.URL.Query().Get("notice"),
		Albums: albums, Tracks: tracks, AlbumTotal: albumTotal, TrackTotal: trackTotal,
	})
}

func (a *App) handleAdminAlbumFavorite(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	favorite := r.FormValue("favorite") == "1"
	if err := a.store.SetAlbumFavorite(r.Context(), parseInt64(r.PathValue("id")), favorite); err != nil {
		a.redirectFeatureError(w, r, safeAdminReturnTo(r.FormValue("returnTo"), "/admin/favorites"), err)
		return
	}
	redirectWithNotice(w, r, safeAdminReturnTo(r.FormValue("returnTo"), "/admin/favorites"), favoriteNotice(favorite, "专辑"))
}

func (a *App) handleAdminTrackFavorite(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	favorite := r.FormValue("favorite") == "1"
	if err := a.store.SetTrackFavorite(r.Context(), parseInt64(r.PathValue("id")), favorite); err != nil {
		a.redirectFeatureError(w, r, safeAdminReturnTo(r.FormValue("returnTo"), "/admin/favorites"), err)
		return
	}
	redirectWithNotice(w, r, safeAdminReturnTo(r.FormValue("returnTo"), "/admin/favorites"), favoriteNotice(favorite, "歌曲"))
}

func (a *App) handleAdminPlaylists(w http.ResponseWriter, r *http.Request) {
	session, _ := a.sessions.get(r)
	playlists, total, err := a.store.ListPlaylists(r.Context(), 500, 0)
	if err != nil {
		a.renderAdminFeatureError(w, "playlists", err)
		return
	}
	a.render(w, http.StatusOK, "playlists.html", playlistsPageData{
		Chrome: a.chromeFor(r.Context(), session, "playlists"), Notice: r.URL.Query().Get("notice"),
		Playlists: playlists, Total: total,
	})
}

func (a *App) handleAdminCreatePlaylist(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	playlist, err := a.store.CreatePlaylist(r.Context(), r.FormValue("name"), r.FormValue("description"))
	if err != nil {
		a.redirectFeatureError(w, r, "/admin/playlists", err)
		return
	}
	redirectWithNotice(w, r, "/admin/playlists/"+strconv.FormatInt(playlist.ID, 10), "歌单已创建")
}

func (a *App) handleAdminPlaylist(w http.ResponseWriter, r *http.Request) {
	session, _ := a.sessions.get(r)
	detail, err := a.store.PlaylistDetail(r.Context(), parseInt64(r.PathValue("id")))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		a.renderAdminFeatureError(w, "playlist", err)
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	candidates := make([]storage.Track, 0)
	if query != "" {
		candidates, err = a.store.ListTracks(r.Context(), storage.Filters{Query: query, Limit: 30})
		if err != nil {
			a.renderAdminFeatureError(w, "playlist search", err)
			return
		}
		existing := make(map[int64]struct{}, len(detail.Tracks))
		for _, track := range detail.Tracks {
			existing[track.ID] = struct{}{}
		}
		filtered := candidates[:0]
		for _, track := range candidates {
			if _, ok := existing[track.ID]; !ok {
				filtered = append(filtered, track)
			}
		}
		candidates = filtered
	}
	a.render(w, http.StatusOK, "playlist.html", playlistPageData{
		Chrome: a.chromeFor(r.Context(), session, "playlists"), Notice: r.URL.Query().Get("notice"),
		Query: query, Detail: detail, Candidates: candidates,
	})
}

func (a *App) handleAdminUpdatePlaylist(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id := parseInt64(r.PathValue("id"))
	if _, err := a.store.UpdatePlaylist(r.Context(), id, r.FormValue("name"), r.FormValue("description")); err != nil {
		a.redirectFeatureError(w, r, playlistAdminPath(id), err)
		return
	}
	redirectWithNotice(w, r, playlistAdminPath(id), "歌单信息已保存")
}

func (a *App) handleAdminDeletePlaylist(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if r.FormValue("confirm") != "delete" {
		redirectWithNotice(w, r, playlistAdminPath(parseInt64(r.PathValue("id"))), "删除确认无效")
		return
	}
	if err := a.store.DeletePlaylist(r.Context(), parseInt64(r.PathValue("id"))); err != nil {
		a.redirectFeatureError(w, r, "/admin/playlists", err)
		return
	}
	redirectWithNotice(w, r, "/admin/playlists", "歌单已删除")
}

func (a *App) handleAdminAddPlaylistTrack(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id := parseInt64(r.PathValue("id"))
	detail, err := a.store.PlaylistDetail(r.Context(), id)
	if err != nil {
		a.redirectFeatureError(w, r, playlistAdminPath(id), err)
		return
	}
	trackID := parseInt64(r.FormValue("trackId"))
	trackIDs := playlistTrackIDs(detail.Tracks)
	if !containsTrackID(trackIDs, trackID) {
		trackIDs = append(trackIDs, trackID)
	}
	if err := a.store.ReplacePlaylistItems(r.Context(), id, trackIDs); err != nil {
		a.redirectFeatureError(w, r, playlistAdminPath(id), err)
		return
	}
	redirectWithNotice(w, r, playlistAdminPath(id), "歌曲已加入歌单")
}

func (a *App) handleAdminRemovePlaylistTrack(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id := parseInt64(r.PathValue("id"))
	trackID := parseInt64(r.PathValue("track"))
	detail, err := a.store.PlaylistDetail(r.Context(), id)
	if err != nil {
		a.redirectFeatureError(w, r, playlistAdminPath(id), err)
		return
	}
	trackIDs := make([]int64, 0, len(detail.Tracks))
	for _, track := range detail.Tracks {
		if track.ID != trackID {
			trackIDs = append(trackIDs, track.ID)
		}
	}
	if err := a.store.ReplacePlaylistItems(r.Context(), id, trackIDs); err != nil {
		a.redirectFeatureError(w, r, playlistAdminPath(id), err)
		return
	}
	redirectWithNotice(w, r, playlistAdminPath(id), "歌曲已从歌单移除")
}

func (a *App) handleAdminMovePlaylistTrack(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id := parseInt64(r.PathValue("id"))
	trackID := parseInt64(r.PathValue("track"))
	detail, err := a.store.PlaylistDetail(r.Context(), id)
	if err != nil {
		a.redirectFeatureError(w, r, playlistAdminPath(id), err)
		return
	}
	trackIDs := playlistTrackIDs(detail.Tracks)
	index := indexOfTrackID(trackIDs, trackID)
	target := index
	if r.FormValue("direction") == "up" {
		target--
	} else if r.FormValue("direction") == "down" {
		target++
	}
	if index >= 0 && target >= 0 && target < len(trackIDs) {
		trackIDs[index], trackIDs[target] = trackIDs[target], trackIDs[index]
		if err := a.store.ReplacePlaylistItems(r.Context(), id, trackIDs); err != nil {
			a.redirectFeatureError(w, r, playlistAdminPath(id), err)
			return
		}
	}
	redirectWithNotice(w, r, playlistAdminPath(id), "歌曲顺序已更新")
}

func (a *App) handleAdminPlayback(w http.ResponseWriter, r *http.Request) {
	session, _ := a.sessions.get(r)
	page := int(parseInt64(r.URL.Query().Get("page")))
	if page < 1 {
		page = 1
	}
	history, total, err := a.store.PlaybackHistory(r.Context(), adminFeaturePageSize, (page-1)*adminFeaturePageSize)
	if err != nil {
		a.renderAdminFeatureError(w, "playback", err)
		return
	}
	data := playbackPageData{
		Chrome: a.chromeFor(r.Context(), session, "playback"), Notice: r.URL.Query().Get("notice"),
		History: history, Total: total, Page: page,
	}
	data.PageCount = int((total + adminFeaturePageSize - 1) / adminFeaturePageSize)
	if page > 1 {
		data.PrevURL = "/admin/playback?page=" + strconv.Itoa(page-1)
	}
	if page < data.PageCount {
		data.NextURL = "/admin/playback?page=" + strconv.Itoa(page+1)
	}
	for number := 1; number <= data.PageCount; number++ {
		if number == 1 || number == data.PageCount || (number >= page-2 && number <= page+2) {
			data.Pages = append(data.Pages, pageLink{Number: number, URL: "/admin/playback?page=" + strconv.Itoa(number), Current: number == page})
		}
	}
	a.render(w, http.StatusOK, "playback.html", data)
}

func (a *App) handleAdminClearPlayback(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if r.FormValue("confirm") != "clear" {
		redirectWithNotice(w, r, "/admin/playback", "清空确认无效")
		return
	}
	if err := a.store.ClearPlaybackHistory(r.Context()); err != nil {
		a.redirectFeatureError(w, r, "/admin/playback", err)
		return
	}
	redirectWithNotice(w, r, "/admin/playback", "播放历史和断点位置已清空")
}

func favoriteNotice(favorite bool, mediaType string) string {
	if favorite {
		return mediaType + "已加入收藏"
	}
	return mediaType + "已取消收藏"
}

// safeAdminReturnTo validates a returnTo form value: same-origin /admin
// paths only, with any stale notice parameter stripped — redirectWithNotice
// appends a fresh one. Fragments are preserved. This is the single
// implementation for every admin handler (L6; it merges the old
// adminReturnPath).
//
// M1: percent-encoding is not a smuggling channel. The decoded, cleaned
// path is re-validated (encoded backslashes like %5C, control characters
// like %0d, and ".." traversal that would escape /admin are all rejected),
// and the output is built from EscapedPath so legal encoded characters
// (e.g. %3F in a path segment) survive byte-for-byte instead of turning
// into real delimiters.
func safeAdminReturnTo(raw, fallback string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.HasPrefix(raw, "/\\") || strings.ContainsAny(raw, "\r\n\\") {
		return fallback
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" {
		return fallback
	}
	decoded := parsed.Path
	if decoded == "" {
		decoded = "/"
	}
	if strings.ContainsRune(decoded, '\\') || strings.ContainsFunc(decoded, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return fallback
	}
	if cleaned := path.Clean(decoded); cleaned != "/admin" && !strings.HasPrefix(cleaned, "/admin/") {
		return fallback
	}
	out := parsed.EscapedPath()
	if out == "" {
		out = "/"
	}
	if strings.Contains(parsed.RawQuery, "notice=") {
		query := parsed.Query()
		query.Del("notice")
		if encoded := query.Encode(); encoded != "" {
			out += "?" + encoded
		}
	} else if parsed.RawQuery != "" {
		out += "?" + parsed.RawQuery
	}
	if parsed.Fragment != "" {
		out += "#" + parsed.Fragment
	}
	return out
}

func playlistAdminPath(id int64) string { return "/admin/playlists/" + strconv.FormatInt(id, 10) }

func playlistTrackIDs(tracks []storage.Track) []int64 {
	result := make([]int64, 0, len(tracks))
	for _, track := range tracks {
		result = append(result, track.ID)
	}
	return result
}

func containsTrackID(values []int64, target int64) bool { return indexOfTrackID(values, target) >= 0 }

func indexOfTrackID(values []int64, target int64) int {
	for index, value := range values {
		if value == target {
			return index
		}
	}
	return -1
}

func (a *App) redirectFeatureError(w http.ResponseWriter, r *http.Request, path string, err error) {
	a.logger.Error("admin client feature operation", "path", r.URL.Path, "error", err)
	redirectWithNotice(w, r, path, adminFeatureErrorMessage(err))
}

func (a *App) renderAdminFeatureError(w http.ResponseWriter, feature string, err error) {
	a.logger.Error("load admin client feature", "feature", feature, "error", err)
	http.Error(w, "admin page unavailable", http.StatusInternalServerError)
}

func adminFeatureErrorMessage(err error) string {
	if errors.Is(err, sql.ErrNoRows) {
		return "操作失败：内容不存在或已经被删除"
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "playlist name must not be empty"):
		return "操作失败：歌单名称不能为空"
	case strings.Contains(message, "playlist cannot contain more than"):
		return "操作失败：单个歌单最多保存 5000 首歌曲"
	case strings.Contains(message, "invalid track id"):
		return "操作失败：歌曲不存在"
	default:
		return "操作失败，请稍后重试"
	}
}

func formatDurationMillis(value int64) string {
	if value <= 0 {
		return "0:00"
	}
	totalSeconds := value / 1000
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	seconds := totalSeconds % 60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%d:%02d", minutes, seconds)
}

func formatAdminTime(value string) string {
	return formatAdminTimeIn(value, time.Local)
}

func formatAdminTimeIn(value string, zone *time.Location) string {
	if value == "" {
		return "—"
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return value
	}
	return parsed.In(zone).Format("2006-01-02 15:04")
}

func albumTypeLabel(value string) string {
	switch value {
	case "album":
		return "专辑"
	case "single":
		return "单曲"
	case "ep":
		return "EP"
	case "compilation":
		return "合辑"
	case "soundtrack":
		return "原声"
	case "live":
		return "现场"
	case "bootleg":
		return "非官方发行"
	case "other":
		return "其他"
	default:
		return value
	}
}

func workTypeLabel(value string) string {
	switch value {
	case "anime":
		return "动画"
	case "drama":
		return "电视剧"
	case "movie":
		return "电影"
	case "game":
		return "游戏"
	case "commercial":
		return "广告"
	case "other":
		return "其他"
	default:
		return value
	}
}

// scanStatusLabel renders the scanner job status in Chinese for the
// dashboard first paint, matching the client-side statusMap in admin.js.
func scanStatusLabel(value string) string {
	switch value {
	case "never":
		return "尚未扫描"
	case "running":
		return "正在扫描"
	case "completed":
		return "扫描完成"
	case "failed":
		return "扫描失败"
	default:
		return value
	}
}

func playbackStateLabel(value string) string {
	switch value {
	case "playing":
		return "播放中"
	case "paused":
		return "已暂停"
	case "buffering":
		return "缓冲中"
	case "stopped":
		return "已停止"
	default:
		return value
	}
}
