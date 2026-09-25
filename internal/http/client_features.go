package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/lux032/032music-server/internal/storage"
)

func (a *App) handleCapabilities(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"apiVersion": "v1", "apiRevision": 2,
		"features": map[string]bool{
			"albums": true, "artists": true, "tracks": true, "search": true,
			"favorites": true, "playlists": true, "playbackProgress": true,
			"playbackHistory": true, "scrobble": true, "rangeStreaming": true,
			"syncAlbums": true, "syncTracks": true,
			"lyrics": true, "instrumentalFilter": true,
			"works": true, "multilingualIndex": true,
			"artistDetail": true, "artistFavorites": true,
			"audioProperties": true, "lyricsText": true, "playlistCreateWithItems": true,
			"transcode": a.transcoder.available["mp3"] || a.transcoder.available["ogg"] || a.transcoder.available["flac"], "artworkThumbnails": true, "similarTracks": false, "trackPath": false, "skipInference": false,
		},
		"transcode": map[string]any{
			"available":   a.transcoder.available["mp3"] || a.transcoder.available["ogg"] || a.transcoder.available["flac"],
			"urlTemplate": "/api/v1/tracks/{id}/transcode.{format}",
			"formats": map[string]any{
				"mp3":  map[string]any{"available": a.transcoder.available["mp3"], "live": true, "offset": true, "bitrates": []int{128, 192, 256, 320}, "default": 320},
				"ogg":  map[string]any{"available": a.transcoder.available["ogg"], "codec": "opus", "live": true, "offset": true, "bitrates": []int{64, 96, 128, 160, 192, 256}, "default": 128},
				"flac": map[string]any{"available": a.transcoder.available["flac"], "live": false, "ranges": true, "offset": false, "maxSampleRates": []int{48000}},
			},
		},
		"artwork": map[string]any{"parameter": "size", "sizes": []int{256, 512, 768, 1024, 1536}},
		"media": map[string]any{
			"streaming": "original", "supportsRange": true,
			"authentication":      []string{"bearer", "query"},
			"mediaAuthentication": []string{"mediaToken", "session"},
			"queryParameter":      "mediaToken",
		},
		"limits": map[string]int{"maxPageSize": 500, "maxPlaylistTracks": 5000},
	})
}

func (a *App) handleAPITrack(w http.ResponseWriter, r *http.Request) {
	value, err := a.store.TrackByID(r.Context(), parseInt64(r.PathValue("id")))
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (a *App) handleSetAlbumFavorite(w http.ResponseWriter, r *http.Request) {
	a.favoriteResult(w, r, a.store.SetAlbumFavorite(r.Context(), parseInt64(r.PathValue("id")), true))
}

func (a *App) handleUnsetAlbumFavorite(w http.ResponseWriter, r *http.Request) {
	a.favoriteResult(w, r, a.store.SetAlbumFavorite(r.Context(), parseInt64(r.PathValue("id")), false))
}

func (a *App) handleSetTrackFavorite(w http.ResponseWriter, r *http.Request) {
	a.favoriteResult(w, r, a.store.SetTrackFavorite(r.Context(), parseInt64(r.PathValue("id")), true))
}

func (a *App) handleUnsetTrackFavorite(w http.ResponseWriter, r *http.Request) {
	a.favoriteResult(w, r, a.store.SetTrackFavorite(r.Context(), parseInt64(r.PathValue("id")), false))
}

func (a *App) favoriteResult(w http.ResponseWriter, r *http.Request, err error) {
	if err != nil {
		a.writeFeatureError(w, r, err, "favorite_failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleFavoriteAlbums(w http.ResponseWriter, r *http.Request) {
	limit, offset := pageValues(r)
	values, total, err := a.store.FavoriteAlbums(r.Context(), limit, offset)
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	writePage(w, values, total, limit, offset)
}

func (a *App) handleFavoriteTracks(w http.ResponseWriter, r *http.Request) {
	limit, offset := pageValues(r)
	values, total, err := a.store.FavoriteTracks(r.Context(), limit, offset)
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	writePage(w, values, total, limit, offset)
}

func (a *App) handlePlaylists(w http.ResponseWriter, r *http.Request) {
	limit, offset := pageValues(r)
	values, total, err := a.store.ListPlaylists(r.Context(), limit, offset)
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	writePage(w, values, total, limit, offset)
}

func (a *App) handleCreatePlaylist(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name        string  `json:"name"`
		Description string  `json:"description"`
		TrackIDs    []int64 `json:"trackIds"`
	}
	if !decode(w, r, &input) {
		return
	}
	value, err := a.store.CreatePlaylistWithItems(r.Context(), input.Name, input.Description, input.TrackIDs)
	if err != nil {
		a.writeFeatureError(w, r, err, "create_failed")
		return
	}
	writeJSON(w, http.StatusCreated, value)
}

func (a *App) handlePlaylist(w http.ResponseWriter, r *http.Request) {
	value, err := a.store.PlaylistDetail(r.Context(), parseInt64(r.PathValue("id")))
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (a *App) handleUpdatePlaylist(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !decode(w, r, &input) {
		return
	}
	value, err := a.store.UpdatePlaylist(r.Context(), parseInt64(r.PathValue("id")), input.Name, input.Description)
	if err != nil {
		a.writeFeatureError(w, r, err, "update_failed")
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (a *App) handleDeletePlaylist(w http.ResponseWriter, r *http.Request) {
	if err := a.store.DeletePlaylist(r.Context(), parseInt64(r.PathValue("id"))); err != nil {
		a.writeFeatureError(w, r, err, "delete_failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleReplacePlaylistItems(w http.ResponseWriter, r *http.Request) {
	var input struct {
		TrackIDs []int64 `json:"trackIds"`
	}
	if !decode(w, r, &input) {
		return
	}
	id := parseInt64(r.PathValue("id"))
	if err := a.store.ReplacePlaylistItems(r.Context(), id, input.TrackIDs); err != nil {
		a.writeFeatureError(w, r, err, "replace_items_failed")
		return
	}
	value, err := a.store.PlaylistDetail(r.Context(), id)
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (a *App) handlePlaybackTimeline(w http.ResponseWriter, r *http.Request) {
	var input storage.PlaybackUpdate
	if !decode(w, r, &input) {
		return
	}
	if err := a.store.UpdatePlayback(r.Context(), input); err != nil {
		a.writeFeatureError(w, r, err, "timeline_failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handlePlaybackScrobble(w http.ResponseWriter, r *http.Request) {
	var input struct {
		TrackID        int64  `json:"trackId"`
		PositionMillis int64  `json:"positionMillis"`
		DurationMillis int64  `json:"durationMillis"`
		Timestamp      string `json:"timestamp"`
	}
	if !decode(w, r, &input) {
		return
	}
	if err := a.store.Scrobble(r.Context(), input.TrackID, input.PositionMillis, input.DurationMillis); err != nil {
		a.writeFeatureError(w, r, err, "scrobble_failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handlePlaybackHistory(w http.ResponseWriter, r *http.Request) {
	limit, offset := pageValues(r)
	values, total, err := a.store.PlaybackHistory(r.Context(), limit, offset)
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	writePage(w, values, total, limit, offset)
}

func (a *App) handleClearPlaybackHistory(w http.ResponseWriter, r *http.Request) {
	if err := a.store.ClearPlaybackHistory(r.Context()); err != nil {
		a.writeFeatureError(w, r, err, "delete_failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func pageValues(r *http.Request) (int, int) {
	limit := int(parseInt64(r.URL.Query().Get("limit")))
	offset := int(parseInt64(r.URL.Query().Get("offset")))
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func writePage(w http.ResponseWriter, items any, total int64, limit, offset int) {
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "limit": limit, "offset": offset,
	})
}

func (a *App) writeFeatureError(w http.ResponseWriter, r *http.Request, err error, code string) {
	status := http.StatusInternalServerError
	message := "The request could not be completed."
	if errors.Is(err, sql.ErrNoRows) {
		status = http.StatusNotFound
		code = "not_found"
		message = "The requested resource was not found."
	} else if strings.Contains(err.Error(), "must") || strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "cannot contain") || strings.Contains(err.Error(), "FOREIGN KEY") {
		status = http.StatusBadRequest
		code = "invalid_request"
		message = err.Error()
	}
	if status >= 500 {
		a.logger.Error("client feature request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	}
	writeAPIError(w, status, code, message)
}
