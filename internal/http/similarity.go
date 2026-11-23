package httpapi

import (
	"github.com/lux032/032music-server/internal/storage"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxSimilarExcludeIDs = 200

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
func parseSimilarOptions(r *http.Request) (storage.SimilarTracksOptions, bool) {
	var opts storage.SimilarTracksOptions
	if rawExclude := strings.TrimSpace(r.URL.Query().Get("exclude")); rawExclude != "" {
		parts := strings.Split(rawExclude, ",")
		if len(parts) > maxSimilarExcludeIDs {
			return opts, false
		}
		exclude := make(map[int64]bool, len(parts))
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			id, ok := positiveID(part)
			if !ok {
				return opts, false
			}
			exclude[id] = true
		}
		opts.Exclude = exclude
	}
	if rawDiverse, hasDiverse := r.URL.Query()["diverse"]; hasDiverse {
		val := strings.ToLower(strings.TrimSpace(rawDiverse[0]))
		switch val {
		case "1", "true":
			opts.MaxPerArtist = 2
		case "0", "false", "":
		default:
			return opts, false
		}
	}
	if rawCap := strings.TrimSpace(r.URL.Query().Get("maxPerArtist")); rawCap != "" {
		n, err := strconv.Atoi(rawCap)
		if err != nil || n < 1 || n > 10 {
			return opts, false
		}
		opts.MaxPerArtist = n
	}
	if rawHide, hasHide := r.URL.Query()["hideInstrumental"]; hasHide {
		val := strings.ToLower(strings.TrimSpace(rawHide[0]))
		switch val {
		case "1", "true":
			opts.HideInstrumental = true
		case "0", "false", "":
		default:
			return opts, false
		}
	}
	return opts, true
}
func (a *App) handleSimilarTracks(w http.ResponseWriter, r *http.Request) {
	id, ok := positiveID(r.PathValue("id"))
	limit, valid := similarityLimit(r, 30, 1)
	opts, optsValid := parseSimilarOptions(r)
	if !ok || !valid || !optsValid {
		writeAPIError(w, 400, "invalid_request", "Invalid track id, limit, exclude, or diversity parameter.")
		return
	}
	items, err := a.store.SimilarTracksWithOptions(r.Context(), id, limit, opts)
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
