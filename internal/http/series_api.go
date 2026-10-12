package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/lux032/032music-server/internal/storage"
)

// seriesTracks returns every visible track linked to the member works of a
// series: member works in SeriesMembers order (earliest first), each work's
// tracks in TracksForWork order, a track linked to several members kept once
// at its first occurrence (the same rule as the web home's series play).
// Returns sql.ErrNoRows for an unknown series.
func (a *App) seriesTracks(ctx context.Context, seriesID int64) ([]storage.WorkTrack, error) {
	if _, err := a.store.WorkSeriesByID(ctx, seriesID); err != nil {
		return nil, err
	}
	members, err := a.store.SeriesMembers(ctx, seriesID)
	if err != nil {
		return nil, err
	}
	values := make([]storage.WorkTrack, 0)
	seen := map[int64]bool{}
	for _, member := range members {
		tracks, err := a.store.TracksForWork(ctx, member.Work.ID)
		if err != nil {
			return nil, err
		}
		for _, track := range tracks {
			if seen[track.ID] {
				continue
			}
			seen[track.ID] = true
			values = append(values, track)
		}
	}
	return values, nil
}

// handleAPISeriesTracks serves GET /api/v1/series/{id}/tracks with the same
// {items: WorkTrack[]} shape as /api/v1/works/{id}/tracks.
func (a *App) handleAPISeriesTracks(w http.ResponseWriter, r *http.Request) {
	values, err := a.seriesTracks(r.Context(), parseInt64(r.PathValue("id")))
	if errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, http.StatusNotFound, "not_found", "Series not found.")
		return
	}
	apiResult(w, values, err)
}
