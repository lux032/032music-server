package httpapi

import (
	"database/sql"
	"errors"
	"github.com/lux032/032music-server/internal/storage"
	"net/http"
)

func (a *App) handleAPIAlbumWorks(w http.ResponseWriter, r *http.Request) {
	id := parseInt64(r.PathValue("id"))
	if _, err := a.store.AlbumByID(r.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAPIError(w, http.StatusNotFound, "not_found", "Album not found.")
		} else {
			apiResult(w, []storage.AlbumWorkView{}, err)
		}
		return
	}
	values, err := a.store.WorksForAlbum(r.Context(), id)
	apiResult(w, values, err)
}
