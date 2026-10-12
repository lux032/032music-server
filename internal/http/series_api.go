package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/lux032/032music-server/internal/storage"
)

// seriesTracks returns every visible track linked to the member works of a
// series: member works in SeriesMembers order (earliest first), each work's
// tracks in TracksForWork order, a track linked to several members kept once
// at its first occurrence (the same rule as the web home's series play).
// Returns sql.ErrNoRows for an unknown series.
func (a *App) seriesTracks(ctx context.Context, seriesID int64) ([]storage.WorkTrack, error) {
	_, members, err := a.seriesWithMembers(ctx, seriesID)
	if err != nil {
		return nil, err
	}
	return a.seriesMemberTracks(ctx, members)
}

// seriesMemberTracks merges the member works' tracks (see seriesTracks).
func (a *App) seriesMemberTracks(ctx context.Context, members []storage.WorkSeriesMember) ([]storage.WorkTrack, error) {
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

// apiSeries is the series object of the series JSON API (list items and the
// detail's `series`).
type apiSeries struct {
	ID                   int64             `json:"id"`
	Title                string            `json:"title"`
	TitleSource          string            `json:"titleSource"`
	MemberCount          int               `json:"memberCount"`
	TypeCounts           []seriesTypeCount `json:"typeCounts"`
	RepresentativeWorkID int64             `json:"representativeWorkId"`
	PosterURL            string            `json:"posterUrl"`
	CreatedAt            string            `json:"createdAt"`
	UpdatedAt            string            `json:"updatedAt"`
}

// apiSeriesMember is one member work card, field-aligned with the home
// works-rail card (posterUrl is the media-token poster route).
type apiSeriesMember struct {
	ID              int64          `json:"id"`
	Title           string         `json:"title"`
	ReadingTitle    string         `json:"readingTitle"`
	TranslatedTitle string         `json:"translatedTitle"`
	WorkType        string         `json:"workType"`
	Year            int            `json:"year"`
	TrackCount      int64          `json:"trackCount"`
	PosterURL       string         `json:"posterUrl"`
	Source          string         `json:"source"`
	Play            homeAPIPlayRef `json:"play"`
}

type apiSeriesStats struct {
	MemberCount int `json:"memberCount"`
	YearFrom    int `json:"yearFrom"`
	YearTo      int `json:"yearTo"`
	AlbumCount  int `json:"albumCount"`
	TrackCount  int `json:"trackCount"`
}

type apiSeriesDetail struct {
	Series  apiSeries         `json:"series"`
	Members []apiSeriesMember `json:"members"`
	Stats   apiSeriesStats    `json:"stats"`
	Play    homeAPIPlayRef    `json:"play"`
}

func (a *App) apiSeriesFrom(series storage.WorkSeries, typeCounts map[string]int, representative *storage.Work) apiSeries {
	out := apiSeries{
		ID: series.ID, Title: series.Title, TitleSource: series.TitleSource, MemberCount: series.MemberCount,
		TypeCounts: sortedSeriesTypeCounts(typeCounts), RepresentativeWorkID: series.RepresentativeWorkID,
		CreatedAt: series.CreatedAt, UpdatedAt: series.UpdatedAt,
	}
	if representative != nil {
		out.PosterURL = workPosterAPIURL(a.enrichment, *representative, 0)
	}
	return out
}

// seriesAPIListLimits mirrors storage.ListSeriesPage's clamp so the response
// echoes the page size actually applied (default 50, max 200).
func seriesAPIListLimits(r *http.Request) (int, int) {
	limit := int(parseInt64(r.URL.Query().Get("limit")))
	offset := int(parseInt64(r.URL.Query().Get("offset")))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// handleAPISeriesList serves GET /api/v1/series: the JSON version of the
// /admin/series list (same search and ordering).
func (a *App) handleAPISeriesList(w http.ResponseWriter, r *http.Request) {
	limit, offset := seriesAPIListLimits(r)
	entries, total, err := a.seriesListEntries(r.Context(), strings.TrimSpace(r.URL.Query().Get("q")), limit, offset)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	items := make([]apiSeries, 0, len(entries))
	for _, entry := range entries {
		items = append(items, a.apiSeriesFrom(entry.Series, entry.TypeCounts, entry.Representative))
	}
	writePage(w, items, int64(total), limit, offset)
}

// seriesAlbums lists the visible albums linked to the member works, in member
// order, each album kept once (appendSeriesAlbums).
func (a *App) seriesAlbums(ctx context.Context, members []storage.WorkSeriesMember) ([]homeSeriesAlbumView, error) {
	out := make([]homeSeriesAlbumView, 0)
	seen := map[int64]bool{}
	for _, member := range members {
		albums, err := a.store.AlbumsForWork(ctx, member.Work.ID)
		if err != nil {
			return nil, err
		}
		out = appendSeriesAlbums(out, seen, member.Work.Title, albums)
	}
	return out, nil
}

// handleAPISeriesDetail serves GET /api/v1/series/{id}: the series, its member
// works in /admin/series/{id} order and summary stats.
func (a *App) handleAPISeriesDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	series, members, err := a.seriesWithMembers(ctx, parseInt64(r.PathValue("id")))
	if errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, http.StatusNotFound, "not_found", "Series not found.")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	albums, err := a.seriesAlbums(ctx, members)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	tracks, err := a.seriesMemberTracks(ctx, members)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	typeCounts := map[string]int{}
	var representative *storage.Work
	out := apiSeriesDetail{
		Members: make([]apiSeriesMember, 0, len(members)),
		Stats:   apiSeriesStats{MemberCount: len(members), AlbumCount: len(albums), TrackCount: len(tracks)},
		Play:    homeAPIPlayRef{Type: "series", ID: series.ID},
	}
	for i := range members {
		work := members[i].Work
		typeCounts[work.Type]++
		if work.ID == series.RepresentativeWorkID {
			representative = &members[i].Work
		}
		if work.Year > 0 {
			if out.Stats.YearFrom == 0 || work.Year < out.Stats.YearFrom {
				out.Stats.YearFrom = work.Year
			}
			out.Stats.YearTo = max(out.Stats.YearTo, work.Year)
		}
		out.Members = append(out.Members, apiSeriesMember{
			ID: work.ID, Title: work.Title, ReadingTitle: work.ReadingTitle, TranslatedTitle: work.TranslatedTitle,
			WorkType: work.Type, Year: work.Year, TrackCount: work.TrackCount,
			PosterURL: workPosterAPIURL(a.enrichment, work, 0), Source: members[i].Source,
			Play: homeAPIPlayRef{Type: "work", ID: work.ID},
		})
	}
	out.Series = a.apiSeriesFrom(series, typeCounts, representative)
	writeJSON(w, http.StatusOK, out)
}

// handleAPISeriesAlbums serves GET /api/v1/series/{id}/albums with the same
// element shape as the home spotlight's series albums.
func (a *App) handleAPISeriesAlbums(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, members, err := a.seriesWithMembers(ctx, parseInt64(r.PathValue("id")))
	if errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, http.StatusNotFound, "not_found", "Series not found.")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	albums, err := a.seriesAlbums(ctx, members)
	items := make([]homeAPISeriesAlbum, 0, len(albums))
	for _, album := range albums {
		items = append(items, homeAPISeriesAlbum{WorkID: album.Album.WorkID, WorkTitle: album.WorkTitle, Album: album.Album, Play: homeAPIPlayRef{Type: "album", ID: album.Album.AlbumID}})
	}
	apiResult(w, items, err)
}
