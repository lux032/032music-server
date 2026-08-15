package httpapi

import (
	"net/http"

	"github.com/lux032/032music-server/internal/storage"
)

func (a *App) handleAPISyncAlbums(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	result, err := a.store.SyncAlbums(r.Context(), storage.SyncAlbumsParams{
		Cursor: parseInt64(q.Get("cursor")),
		Limit:  int(parseInt64(q.Get("limit"))),
	})
	if err != nil {
		a.logger.Error("sync albums failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *App) handleAPISyncTracks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cursor := parseInt64(q.Get("cursor"))
	limit := int(parseInt64(q.Get("limit")))
	albumID := parseInt64(q.Get("albumId"))
	if albumID == 0 {
		albumID = parseInt64(q.Get("album_id"))
	}

	result, err := a.store.SyncTracks(r.Context(), storage.SyncTracksParams{
		Cursor:  cursor,
		Limit:   limit,
		AlbumID: albumID,
	})
	if err != nil {
		a.logger.Error("sync tracks failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}
