package httpapi

import (
	"net/http"
)

func (a *App) withSimilaritySlot(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case a.similaritySlots <- struct{}{}:
			defer func() { <-a.similaritySlots }()
			next.ServeHTTP(w, r)
		default:
			w.Header().Set("Retry-After", "1")
			writeAPIError(w, http.StatusServiceUnavailable, "busy", "Similarity capacity reached.")
		}
	})
}
