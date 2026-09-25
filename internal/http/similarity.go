package httpapi

import (
	"net/http"
	"strconv"
	"time"
)

func similarityLimit(r *http.Request, def, min int) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		if _, ok := r.URL.Query()["limit"]; ok {
			return 0, false
		}
		return def, true
	}
	n, e := strconv.Atoi(raw)
	return n, e == nil && n >= min && n <= 100
}
func positiveID(raw string) (int64, bool) {
	n, e := strconv.ParseInt(raw, 10, 64)
	return n, e == nil && n > 0
}
func (a *App) handleSimilarTracks(w http.ResponseWriter, r *http.Request) {
	id, ok := positiveID(r.PathValue("id"))
	limit, valid := similarityLimit(r, 30, 1)
	if !ok || !valid {
		writeAPIError(w, 400, "invalid_request", "Invalid track id or limit.")
		return
	}
	items, err := a.store.SimilarTracks(r.Context(), id, limit)
	if r.Context().Err() != nil {
		return
	}
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (a *App) handleTrackPath(w http.ResponseWriter, r *http.Request) {
	from, ok := positiveID(r.URL.Query().Get("from"))
	to, ok2 := positiveID(r.URL.Query().Get("to"))
	limit, valid := similarityLimit(r, 25, 2)
	if !ok || !ok2 || !valid {
		writeAPIError(w, 400, "invalid_request", "Invalid from, to or limit.")
		return
	}
	items, complete, err := a.store.TrackPath(r.Context(), from, to, limit, 2*time.Second)
	if r.Context().Err() != nil {
		return
	}
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "complete": complete})
}
