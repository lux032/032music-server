package httpapi

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/lux032/032music-server/internal/storage"
)

type artistCorrectionPage struct {
	Chrome
	Track    storage.Track
	State    storage.PrimaryArtistCorrection
	Proposed []storage.ArtistRef
	IDs      []int64
	Action   string
	Preview  bool
	Notice   string
}

func (a *App) handleArtistCorrection(w http.ResponseWriter, r *http.Request) {
	session, _ := a.sessions.get(r)
	id := parseInt64(r.PathValue("id"))
	// Use the existing detail loader so visibility and metadata match the API.
	track, err := a.store.TrackByID(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		a.renderAdminFeatureError(w, "library", err)
		return
	}
	state, err := a.store.PrimaryArtistCorrection(r.Context(), id)
	if err != nil {
		a.renderAdminFeatureError(w, "library", err)
		return
	}
	data := artistCorrectionPage{Chrome: a.chromeFor(r.Context(), session, "library"), Track: track, State: state}
	if r.Method == http.MethodPost {
		if !a.validCSRF(r) {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}
		if err = r.ParseForm(); err != nil {
			http.Error(w, "invalid form", 400)
			return
		}
		data.Action = r.FormValue("action")
		if data.Action != "set" && data.Action != "reset" && data.Action != "undo" {
			http.Error(w, "invalid action", 400)
			return
		}
		if r.FormValue("token") != state.Token {
			data.Notice = storage.ErrArtistCorrectionConflict.Error()
			a.render(w, 409, "artist-correction.html", data)
			return
		}
		if data.Action == "set" {
			for _, value := range r.Form["artist"] {
				if value == "" {
					continue
				}
				v, e := strconv.ParseInt(value, 10, 64)
				if e != nil || v <= 0 {
					http.Error(w, "请选择有效艺术家", 400)
					return
				}
				data.IDs = append(data.IDs, v)
			}
			if len(data.IDs) == 0 || len(data.IDs) > 32 {
				http.Error(w, "请选择 1 至 32 位艺术家", 400)
				return
			}
			seen := map[int64]bool{}
			for i, v := range data.IDs {
				canonical, e := a.store.CanonicalArtistID(r.Context(), v)
				if e != nil || seen[canonical] {
					http.Error(w, "艺术家无效或重复", 400)
					return
				}
				if r.FormValue("confirm") == "1" && canonical != v {
					http.Error(w, "目标艺术家已合并，请重新预览", http.StatusConflict)
					return
				}
				data.IDs[i] = canonical
				seen[canonical] = true
				name, e := a.store.ArtistNameByID(r.Context(), canonical)
				if e != nil {
					http.Error(w, "艺术家不存在", 400)
					return
				}
				data.Proposed = append(data.Proposed, storage.ArtistRef{ID: canonical, Name: name})
			}
		}
		if r.FormValue("confirm") == "1" {
			err = a.store.SetPrimaryArtistCorrection(r.Context(), id, state.Token, data.IDs, data.Action == "undo")
			if err != nil {
				data.Notice = err.Error()
				a.render(w, 409, "artist-correction.html", data)
				return
			}
			http.Redirect(w, r, fmt.Sprintf("/admin/tracks/%d/artists", id), http.StatusSeeOther)
			return
		}
		data.Preview = true
	}
	a.render(w, 200, "artist-correction.html", data)
}
