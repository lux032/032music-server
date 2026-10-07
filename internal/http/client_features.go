package httpapi

import (
	"database/sql"
	"errors"
	"github.com/lux032/032music-server/internal/storage"
	"net/http"
	"strings"
)

func (a *App) handleCapabilities(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"apiVersion": "v1", "apiRevision": 3,
		"features": map[string]bool{
			"albums": true, "artists": true, "tracks": true, "search": true,
			"playlistItemOps": true, "playlistArtwork": true, "playlistRevision": true,
			"favorites": true, "playlists": true, "playbackProgress": true,
			"playbackHistory": true, "playbackEvents": true, "rangeStreaming": true,
			"syncAlbums": true, "syncTracks": true, "syncArtists": true, "artistRefs": true,
			"lyrics": true, "instrumentalFilter": true,
			"works": true, "multilingualIndex": true,
			"artistDetail": true, "artistFavorites": true,
			"audioProperties": true, "lyricsText": true, "playlistCreateWithItems": true,
			"transcode": a.transcoder.available["mp3"] || a.transcoder.available["ogg"] || a.transcoder.available["flac"], "artworkThumbnails": true, "similarTracks": true, "trackPath": true,
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
		"similarity": map[string]any{"method": "metadata", "distanceRange": []int{0, 1}},
		"playback": map[string]any{
			// Playback sessions (plan C, apiRevision 3). The legacy timeline
			// and scrobble endpoints were removed and answer 410.
			"endpoint":            "/api/v1/playback/events",
			"removedEndpoints":    []string{"/api/v1/playback/timeline", "/api/v1/playback/scrobble"},
			"authentication":      []string{"apiToken", "mediaToken", "session"},
			"queryParameter":      "mediaToken",
			"identityNote":        "single-user server: clientId binds a session to its owner but is not a strong security identity",
			"heartbeatMillis":     map[string]int{"active": 15000, "paused": 60000},
			"leaseMillis":         map[string]int{"active": 90000, "paused": 600000},
			"seqMax":              9007199254740992,
			"states":              []string{"playing", "buffering", "paused"},
			"endReasons":          []string{"completed", "skipped", "stopped", "replaced", "error", "client_closed"},
			"resumableEndReasons": []string{"expired", "client_closed", "error", "stopped"},
			"expiredBehaviour":    "409 session_expired: the session is finalized as interrupted (never counted as skip/completion); start ONE new session with resumedFromSessionId at the current position (serialize: only one resume per expired session), then continue; a late end repeats start(resume at the FINAL position) followed by end(original endReason). Recovery events only: the recovery start uses state playing because the track actually played — never fake playing for a track that was not played",
			"missingBehaviour":    "404 session_not_found (unknown or cleared session): if the play actually happened and already passed the count threshold, start a fresh session with state playing at the final position and end it with the original endReason to recover the play; otherwise drop it and start fresh on the next play",
			"resumeDefinition":    "refresh, restoreState, BFCache restore and process-restart recovery of the SAME track are resumes: persist lastSessionId+trackId and start with resumedFromSessionId. Only an explicit track change or a loop restart is a new play (fresh session without resume). A start retry after a network timeout must reuse the SAME sessionId (idempotent replay).",
			"resumeInvalid":       "409 resume_invalid: unknown/other-client/other-track predecessor, or a predecessor ended completed/skipped/replaced; an active predecessor on the same client+track is instead superseded as replaced and accepted. Do NOT silently retry as a fresh session at a >50% position: that is a new play and counts again",
			"completedPosition":   "end(completed) must carry the true final position (≈duration), never 0; the server resets the stored breakpoint itself",
			"effectiveStates":     []string{"playing", "buffering", "paused", "interrupted", "completed", "skipped", "stopped", "error"},
			"historyPriority":     "playing > buffering > paused, otherwise the most recent end reason; pre-session rows show stopped",
			"scrobble":            map[string]any{"thresholdFraction": 0.5, "slackMillis": 1000, "countsOn": "events with evidence of actual playback: the state before or after the event is playing; a session that only ever reported paused/buffering never counts, whatever endReason it ends with", "deduplicated": "once per resume chain (chain_id), backstopped by a unique partial index", "serverDerived": true, "lastfm": a.lastfm != nil},
			"skip":                map[string]any{"endReason": "skipped", "thresholdMillis": 30000, "thresholdFraction": 0.5, "countedOnce": true},
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
		Name          string  `json:"name"`
		Description   string  `json:"description"`
		TrackIDs      []int64 `json:"trackIds"`
		InvalidTracks string  `json:"invalidTracks"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.InvalidTracks != "" && input.InvalidTracks != "skip" {
		writeAPIError(w, 400, "invalid_request", "invalid invalidTracks policy")
		return
	}
	if input.InvalidTracks == "skip" {
		value, stats, err := a.store.CreatePlaylistSkippingInvalid(r.Context(), input.Name, input.Description, input.TrackIDs)
		if err != nil {
			a.writeFeatureError(w, r, err, "create_failed")
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"playlist": value, "stats": stats})
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
	if err := a.deletePlaylist(r.Context(), parseInt64(r.PathValue("id"))); err != nil {
		a.writeFeatureError(w, r, err, "delete_failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleReplacePlaylistItems(w http.ResponseWriter, r *http.Request) {
	var input struct {
		TrackIDs         []int64 `json:"trackIds"`
		ExpectedRevision *int64  `json:"expectedRevision"`
	}
	if !decode(w, r, &input) {
		return
	}
	id := parseInt64(r.PathValue("id"))
	if err := a.store.ReplacePlaylistItemsAtRevision(r.Context(), id, input.TrackIDs, input.ExpectedRevision); err != nil {
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
	var capacity *storage.PlaylistCapacityError
	if errors.Is(err, storage.ErrPlaylistConflict) {
		writeAPIError(w, 409, "playlist_conflict", err.Error())
		return
	} else if storage.IsBusyError(err) {
		w.Header().Set("Retry-After", "1")
		writeAPIError(w, 503, "database_busy", "Database is busy; retry later.")
		return
	} else if errors.As(err, &capacity) {
		writeJSON(w, 400, map[string]any{"error": map[string]any{"code": "playlist_capacity", "message": err.Error(), "remainingCapacity": capacity.Remaining}})
		return
	} else if errors.Is(err, sql.ErrNoRows) {
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
