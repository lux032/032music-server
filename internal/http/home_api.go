package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

// home_api.go serves /api/v1/home/*: the read-only JSON twin of the web home
// page (/admin/home). Every endpoint maps the very same view data the HTML
// routes render (homeData / homeRandomPane / homeWorksPanel /
// homeSongsPanel / homeChronicle), so both read one frozen daily rotation.
// Responses carry data only: no HTML, no preformatted labels; timestamps are
// ISO 8601 and media URLs follow the existing API rules (clients append
// mediaToken / size themselves). See docs/api/home.md.

// homeAPIPlayRef tells a client how to expand a card into tracks:
// album → /albums/{id}, work → /works/{id}/tracks, series →
// /series/{id}/tracks, playlist → /playlists/{id}, track → the track itself.
type homeAPIPlayRef struct {
	Type string `json:"type"`
	ID   int64  `json:"id"`
}

type homeAPIRoleCount struct {
	Role  string `json:"role"`
	Count int    `json:"count"`
}

// homeAPIAlbumPane is one album-shaped spotlight tab (recent / listening /
// random), also the payload of spotlight/random.
type homeAPIAlbumPane struct {
	Focus                 string                  `json:"focus"`
	Play                  homeAPIPlayRef          `json:"play"`
	Album                 storage.Album           `json:"album"`
	Tracks                []storage.Track         `json:"tracks"`
	PreviewDurationMillis int64                   `json:"previewDurationMillis"`
	NoteTime              string                  `json:"noteTime"`
	Resume                *storage.PlaybackRecord `json:"resume"`
}

type homeAPISpotlightWork struct {
	Work       storage.Work       `json:"work"`
	RoleCounts []homeAPIRoleCount `json:"roleCounts"`
}

type homeAPISeriesAlbum struct {
	WorkID    int64             `json:"workId"`
	WorkTitle string            `json:"workTitle"`
	Album     storage.AlbumWork `json:"album"`
	Play      homeAPIPlayRef    `json:"play"`
}

// homeAPISeriesPane is the 作品聚焦 tab.
type homeAPISeriesPane struct {
	IsSeries   bool                   `json:"isSeries"`
	SeriesID   int64                  `json:"seriesId"`
	Title      string                 `json:"title"`
	Play       homeAPIPlayRef         `json:"play"`
	WorkIDs    []int64                `json:"workIds"`
	Works      []homeAPISpotlightWork `json:"works"`
	Albums     []homeAPISeriesAlbum   `json:"albums"`
	AlbumCount int                    `json:"albumCount"`
	TrackTotal int64                  `json:"trackTotal"`
	YearFrom   int                    `json:"yearFrom"`
	YearTo     int                    `json:"yearTo"`
}

type homeAPISpotlight struct {
	Tabs       []string           `json:"tabs"`
	DefaultTab string             `json:"defaultTab"`
	Recent     *homeAPIAlbumPane  `json:"recent"`
	Listening  *homeAPIAlbumPane  `json:"listening"`
	Series     *homeAPISeriesPane `json:"series"`
	Random     *homeAPIAlbumPane  `json:"random"`
}

type homeAPISeriesRef struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

// homeAPIWorkCard is one card of the works rotation. WorkType is the work's
// type (anime / game / …), renamed so it never clashes with a play type.
type homeAPIWorkCard struct {
	ID         int64              `json:"id"`
	Title      string             `json:"title"`
	WorkType   string             `json:"workType"`
	Year       int                `json:"year"`
	TrackCount int64              `json:"trackCount"`
	PosterURL  string             `json:"posterUrl"`
	CoverURL   string             `json:"coverUrl"`
	ArtworkURL string             `json:"artworkUrl"`
	Series     *homeAPISeriesRef  `json:"series"`
	RoleCounts []homeAPIRoleCount `json:"roleCounts"`
	Play       homeAPIPlayRef     `json:"play"`
}

type homeAPISongWork struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Year  int    `json:"year"`
}

type homeAPISongRow struct {
	Track storage.Track   `json:"track"`
	Role  string          `json:"role"`
	Work  homeAPISongWork `json:"work"`
	Play  homeAPIPlayRef  `json:"play"`
}

// homeAPIRotationPage is one page of a daily rotation. Type is only set for
// the works rotation.
type homeAPIRotationPage struct {
	Type       string `json:"type,omitempty"`
	Generation string `json:"generation"`
	Batch      int    `json:"batch"`
	Pages      int    `json:"pages"`
	Count      int    `json:"count"`
	Renewed    bool   `json:"renewed"`
	Items      any    `json:"items"`
}

type homeAPIChronicleResult struct {
	Year   int             `json:"year"`
	Total  int64           `json:"total"`
	Albums []storage.Album `json:"albums"`
}

type homeAPIYear struct {
	Year  int   `json:"year"`
	Count int64 `json:"count"`
}

type homeAPIChronicle struct {
	Years        []homeAPIYear          `json:"years"`
	SelectedYear int                    `json:"selectedYear"`
	Result       homeAPIChronicleResult `json:"result"`
}

type homeAPIStats struct {
	Albums             int64  `json:"albums"`
	Tracks             int64  `json:"tracks"`
	Works              int64  `json:"works"`
	LastScanAt         string `json:"lastScanAt"`
	PendingReviewCount int    `json:"pendingReviewCount"`
}

type homeAPIResponse struct {
	Empty        bool                     `json:"empty"`
	GeneratedAt  string                   `json:"generatedAt"`
	RotationDay  string                   `json:"rotationDay"`
	Spotlight    *homeAPISpotlight        `json:"spotlight"`
	Resume       []storage.PlaybackRecord `json:"resume"`
	RecentAlbums []storage.Album          `json:"recentAlbums"`
	Works        homeAPIRotationPage      `json:"works"`
	Songs        homeAPIRotationPage      `json:"songs"`
	Chronicle    *homeAPIChronicle        `json:"chronicle"`
	Playlists    []storage.Playlist       `json:"playlists"`
	Stats        homeAPIStats             `json:"stats"`
}

// homeAPIAlbums returns a copy of albums with the API artist refs attached
// (the same `artists` field /api/v1/albums/{id} returns). A failure only
// drops the refs.
func (a *App) homeAPIAlbums(ctx context.Context, albums []storage.Album) []storage.Album {
	out := make([]storage.Album, len(albums))
	copy(out, albums)
	if len(out) == 0 {
		return out
	}
	if err := a.store.AttachAlbumArtistRefs(ctx, out); err != nil {
		a.logger.Warn("home api album artists", "error", err)
	}
	return out
}

func homeAPIRoleCounts(counts []storage.HomeRoleCount) []homeAPIRoleCount {
	out := make([]homeAPIRoleCount, 0, len(counts))
	for _, c := range counts {
		out = append(out, homeAPIRoleCount{Role: c.Role, Count: c.Count})
	}
	return out
}

func (a *App) homeAPIAlbumPaneFrom(ctx context.Context, view *homeHeroAlbumView) *homeAPIAlbumPane {
	if view == nil {
		return nil
	}
	tracks := view.Tracks
	if tracks == nil {
		tracks = []storage.Track{}
	}
	return &homeAPIAlbumPane{
		Focus:                 view.Focus,
		Play:                  homeAPIPlayRef{Type: "album", ID: view.Album.ID},
		Album:                 a.homeAPIAlbums(ctx, []storage.Album{view.Album})[0],
		Tracks:                tracks,
		PreviewDurationMillis: tracksDuration(tracks),
		NoteTime:              view.NoteTime,
		Resume:                view.Resume,
	}
}

func homeAPISeriesPaneFrom(view *homeSeriesView) *homeAPISeriesPane {
	if view == nil {
		return nil
	}
	pane := &homeAPISeriesPane{IsSeries: view.IsSeries, SeriesID: view.SeriesID, Title: view.Title, AlbumCount: view.AlbumCount, TrackTotal: view.TrackTotal, WorkIDs: []int64{}, Works: []homeAPISpotlightWork{}, Albums: []homeAPISeriesAlbum{}}
	for _, work := range view.Works {
		pane.WorkIDs = append(pane.WorkIDs, work.Work.ID)
		pane.Works = append(pane.Works, homeAPISpotlightWork{Work: work.Work, RoleCounts: homeAPIRoleCounts(work.RoleCounts)})
		if year := work.Work.Year; year > 0 {
			if pane.YearFrom == 0 || year < pane.YearFrom {
				pane.YearFrom = year
			}
			pane.YearTo = max(pane.YearTo, year)
		}
	}
	for _, album := range view.Albums {
		pane.Albums = append(pane.Albums, homeAPISeriesAlbum{WorkID: album.Album.WorkID, WorkTitle: album.WorkTitle, Album: album.Album, Play: homeAPIPlayRef{Type: "album", ID: album.Album.AlbumID}})
	}
	switch {
	case view.IsSeries:
		pane.Play = homeAPIPlayRef{Type: "series", ID: view.SeriesID}
	case len(view.Works) > 0:
		pane.Play = homeAPIPlayRef{Type: "work", ID: view.Works[0].Work.ID}
	}
	return pane
}

func (a *App) homeAPIWorksPage(panel homeWorksPanel, renewed bool) homeAPIRotationPage {
	items := make([]homeAPIWorkCard, 0, len(panel.Works))
	for _, view := range panel.Works {
		work := view.Work
		card := homeAPIWorkCard{
			ID: work.ID, Title: work.Title, WorkType: work.Type, Year: work.Year, TrackCount: work.TrackCount,
			PosterURL:  workPosterAPIURL(a.enrichment, storage.Work{ID: work.ID, PosterURL: work.PosterURL, UpdatedAt: work.UpdatedAt}, 0),
			CoverURL:   work.CoverURL,
			RoleCounts: homeAPIRoleCounts(work.RoleCounts),
			Play:       homeAPIPlayRef{Type: "work", ID: work.ID},
		}
		card.ArtworkURL = card.PosterURL
		if card.ArtworkURL == "" {
			card.ArtworkURL = card.CoverURL
		}
		if work.SeriesID > 0 {
			card.Series = &homeAPISeriesRef{ID: work.SeriesID, Title: work.SeriesTitle}
		}
		items = append(items, card)
	}
	filter := panel.Filter
	if filter == "" {
		filter = "all"
	}
	return homeAPIRotationPage{Type: filter, Generation: panel.Generation, Batch: panel.Batch, Pages: panel.Pages, Count: panel.Count, Renewed: renewed, Items: items}
}

func homeAPISongsPage(panel homeSongsPanel, renewed bool) homeAPIRotationPage {
	items := make([]homeAPISongRow, 0, len(panel.Tracks))
	for _, row := range panel.Tracks {
		items = append(items, homeAPISongRow{Track: row.Track, Role: row.Role, Work: homeAPISongWork{ID: row.WorkID, Title: row.WorkTitle, Year: row.WorkYear}, Play: homeAPIPlayRef{Type: "track", ID: row.Track.ID}})
	}
	return homeAPIRotationPage{Generation: panel.Generation, Batch: panel.Batch, Pages: panel.Pages, Count: panel.Count, Renewed: renewed, Items: items}
}

func (a *App) homeAPIChronicleResult(ctx context.Context, view homeChronicleView) homeAPIChronicleResult {
	albums := make([]storage.Album, 0, len(view.Albums))
	for _, card := range view.Albums {
		albums = append(albums, card.Album)
	}
	return homeAPIChronicleResult{Year: view.Year, Total: view.Total, Albums: a.homeAPIAlbums(ctx, albums)}
}

// homeAPIFrom maps the shared home view data onto the JSON payload.
func (a *App) homeAPIFrom(ctx context.Context, data homePageData) homeAPIResponse {
	now := a.homeRotation.now()
	out := homeAPIResponse{
		Empty:        data.Empty,
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		RotationDay:  homeDay(now),
		Resume:       []storage.PlaybackRecord{},
		RecentAlbums: []storage.Album{},
		Works:        homeAPIRotationPage{Type: "all", Items: []homeAPIWorkCard{}},
		Songs:        homeAPIRotationPage{Items: []homeAPISongRow{}},
		Playlists:    []storage.Playlist{},
		Stats:        homeAPIStats{Albums: data.Stats.Albums, Tracks: data.Stats.Tracks, Works: data.WorksCount, LastScanAt: data.LastScan},
	}
	if total, err := a.store.PendingWorkReviewTotal(ctx); err == nil {
		out.Stats.PendingReviewCount = total
	} else {
		a.logger.Warn("home api pending review count", "error", err)
	}
	if data.Empty {
		return out
	}
	if hero := data.Hero; hero.Recent != nil {
		spot := &homeAPISpotlight{Tabs: make([]string, 0, len(hero.Tabs)), DefaultTab: "recent"}
		for _, tab := range hero.Tabs {
			spot.Tabs = append(spot.Tabs, tab.Key)
		}
		spot.Recent = a.homeAPIAlbumPaneFrom(ctx, hero.Recent)
		spot.Listening = a.homeAPIAlbumPaneFrom(ctx, hero.Listening)
		spot.Series = homeAPISeriesPaneFrom(hero.Series)
		spot.Random = a.homeAPIAlbumPaneFrom(ctx, hero.Random)
		out.Spotlight = spot
	}
	for _, row := range data.Resume {
		out.Resume = append(out.Resume, row.Record)
	}
	recent := make([]storage.Album, 0, len(data.RecentAlbums))
	for _, card := range data.RecentAlbums {
		recent = append(recent, card.Album)
	}
	out.RecentAlbums = a.homeAPIAlbums(ctx, recent)
	out.Works = a.homeAPIWorksPage(data.WorksPanel, false)
	out.Songs = homeAPISongsPage(data.SongsPanel, false)
	if len(data.Years) > 0 {
		chronicle := &homeAPIChronicle{Years: make([]homeAPIYear, 0, len(data.Years)), SelectedYear: data.Chronicle.Year}
		for _, bar := range data.Years {
			chronicle.Years = append(chronicle.Years, homeAPIYear{Year: bar.Year, Count: bar.Count})
		}
		chronicle.Result = a.homeAPIChronicleResult(ctx, data.Chronicle)
		out.Chronicle = chronicle
	}
	if data.Playlists != nil {
		out.Playlists = data.Playlists
	}
	return out
}

// handleAPIHome serves GET /api/v1/home: every first-screen section.
func (a *App) handleAPIHome(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	data, err := a.homeData(ctx, r, "")
	if err != nil {
		a.logger.Error("home api", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "query_failed", "Home data could not be loaded.")
		return
	}
	writeJSON(w, http.StatusOK, a.homeAPIFrom(ctx, data))
}

// handleAPIHomeRandomSpotlight serves GET /api/v1/home/spotlight/random (换一张).
func (a *App) handleAPIHomeRandomSpotlight(w http.ResponseWriter, r *http.Request) {
	view, err := a.homeRandomPane(r.Context(), parseInt64(r.URL.Query().Get("exclude")))
	if errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, http.StatusNotFound, "not_found", "The library has no album.")
		return
	}
	if err != nil {
		a.logger.Error("home api random spotlight", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "query_failed", "Home data could not be loaded.")
		return
	}
	writeJSON(w, http.StatusOK, a.homeAPIAlbumPaneFrom(r.Context(), view))
}

// homeAPIRotationParams parses ?batch=&generation= (gen is accepted as an
// alias, matching the web fragment routes).
func homeAPIRotationParams(r *http.Request) (int, string, bool) {
	batch, generation, ok := homeRotationParams(r)
	if value := r.URL.Query().Get("generation"); value != "" {
		generation = value
	}
	return batch, generation, ok
}

// handleAPIHomeWorks serves GET /api/v1/home/works (类型筛选 / 换一批).
func (a *App) handleAPIHomeWorks(w http.ResponseWriter, r *http.Request) {
	filter := r.URL.Query().Get("type")
	if filter == "" {
		filter = "all"
	}
	batch, generation, ok := homeAPIRotationParams(r)
	if !ok || !homeWorkFilterValid(filter) {
		writeAPIError(w, http.StatusBadRequest, "invalid_parameters", "type must be all, anime, game or movie and batch a non-negative integer.")
		return
	}
	panel, renewed, err := a.homeWorksPanel(r.Context(), filter, batch, generation)
	if err != nil {
		a.logger.Error("home api works", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "query_failed", "Home data could not be loaded.")
		return
	}
	if renewed {
		w.Header().Set("X-Home-Rotation", "renewed")
	}
	writeJSON(w, http.StatusOK, a.homeAPIWorksPage(panel, renewed))
}

// handleAPIHomeSongs serves GET /api/v1/home/songs (换一批).
func (a *App) handleAPIHomeSongs(w http.ResponseWriter, r *http.Request) {
	batch, generation, ok := homeAPIRotationParams(r)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "invalid_parameters", "batch must be a non-negative integer.")
		return
	}
	panel, renewed, err := a.homeSongsPanel(r.Context(), batch, generation)
	if err != nil {
		a.logger.Error("home api songs", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "query_failed", "Home data could not be loaded.")
		return
	}
	if renewed {
		w.Header().Set("X-Home-Rotation", "renewed")
	}
	writeJSON(w, http.StatusOK, homeAPISongsPage(panel, renewed))
}

// handleAPIHomeChronicle serves GET /api/v1/home/chronicle?year= (切年份).
func (a *App) handleAPIHomeChronicle(w http.ResponseWriter, r *http.Request) {
	year := int(parseInt64(r.URL.Query().Get("year")))
	if year <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid_year", "year must be a positive integer.")
		return
	}
	view, err := a.homeChronicle(r.Context(), year)
	if err != nil {
		a.logger.Error("home api chronicle", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "query_failed", "Home data could not be loaded.")
		return
	}
	writeJSON(w, http.StatusOK, a.homeAPIChronicleResult(r.Context(), view))
}
