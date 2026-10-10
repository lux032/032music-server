package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

// home.go renders /admin/home — the landing page (方案 A · 聚焦与编年).
// All data comes from read-only storage queries; playback actions are wired
// client-side onto the existing queue/player delegation (queue.js).

type homeFocusTab struct {
	Key     string
	Label   string
	Current bool
}

// homeHeroAlbumView is the album-shaped spotlight (最新入库 / 最近在听 / 随机重温).
type homeHeroAlbumView struct {
	Tabs          []homeFocusTab
	Focus         string
	CSRFToken     string
	Album         storage.Album
	Tracks        []storage.Track
	Spec          string
	Note          string
	DurationLabel string
	// Listening-only fields: the unfinished track to resume.
	Listening     bool
	ResumeTrackID int64
	ResumeMS      int64
	ResumeLabel   string
}

type homeSeriesAlbumView struct {
	WorkTitle string
	Album     storage.AlbumWork
}

type homeSeriesView struct {
	Tabs         []homeFocusTab
	SeriesID     int64
	IsSeries     bool
	Title        string
	Works        []storage.HomeSpotlightWork
	Albums       []homeSeriesAlbumView
	AlbumCount   int
	TrackTotal   int64
	YearRange    string
	WorkIDsParam string
}

type homeHeroView struct {
	Tabs      []homeFocusTab
	Recent    *homeHeroAlbumView
	Listening *homeHeroAlbumView
	Series    *homeSeriesView
	Random    *homeHeroAlbumView
}

type homeResumeRow struct {
	Record  storage.PlaybackRecord
	Percent int
}

// homeAlbumView is one card of 最近添加 / 编年货架：album + precomputed meta line.
type homeAlbumView struct {
	Album storage.Album
	Meta  string
}

// homeChronicleView is the per-year result block under the chronicle chart;
// also the payload of the /admin/home/chronicle fragment.
type homeChronicleView struct {
	Year   int
	Albums []homeAlbumView
	Total  int64
}

type homeYearBar struct {
	Year      int
	Count     int64
	Height    int
	Selected  bool
	AxisClass string
}

type homeWorkView struct {
	Work  storage.HomeWork
	Cover string
}

type homePageData struct {
	Chrome
	Empty         bool
	Today         string
	Hero          homeHeroView
	Resume        []homeResumeRow
	RecentAlbums  []homeAlbumView
	Works         []homeWorkView
	WorksTotal    int64
	RoleTracks    []storage.HomeRoleTrack
	Years         []homeYearBar
	Chronicle     homeChronicleView
	Playlists     []storage.Playlist
	Stats         storage.Statistics
	WorksCount    int64
	LastScan      string
}

// homeToday renders the header date, e.g. "10月11日 星期五".
func homeToday() string {
	now := time.Now()
	weekdays := []string{"日", "一", "二", "三", "四", "五", "六"}
	return fmt.Sprintf("%d月%d日 星期%s", int(now.Month()), now.Day(), weekdays[now.Weekday()])
}

// homeSpecLabel renders the small format tag of the spotlight card
// ("FLAC / 24bit / 96kHz"); empty when nothing was probed.
func homeSpecLabel(track storage.Track) string {
	parts := make([]string, 0, 3)
	if track.Codec != "" {
		parts = append(parts, strings.ToUpper(track.Codec))
	}
	if track.BitDepth > 0 {
		parts = append(parts, fmt.Sprintf("%dbit", track.BitDepth))
	}
	if track.SampleRate > 0 {
		parts = append(parts, fmt.Sprintf("%gkHz", float64(track.SampleRate)/1000))
	}
	return strings.Join(parts, " / ")
}

func homeFocusTabs(current string, listening, series bool) []homeFocusTab {
	tabs := []homeFocusTab{{Key: "recent", Label: "最新入库"}}
	if listening {
		tabs = append(tabs, homeFocusTab{Key: "listening", Label: "最近在听"})
	}
	if series {
		tabs = append(tabs, homeFocusTab{Key: "series", Label: "作品聚焦"})
	}
	tabs = append(tabs, homeFocusTab{Key: "random", Label: "随机重温"})
	for i := range tabs {
		tabs[i].Current = tabs[i].Key == current
	}
	return tabs
}

// homeAlbumSpotlight loads the track list and spec tag for one spotlighted
// album. A track-list failure degrades to no side preview, never a 500.
func (a *App) homeAlbumSpotlight(ctx context.Context, album storage.Album, note string) *homeHeroAlbumView {
	view := &homeHeroAlbumView{Album: album, Note: note}
	tracks, err := a.store.ListTracks(ctx, storage.Filters{AlbumID: album.ID, Limit: 60})
	if err != nil {
		a.logger.Warn("home spotlight tracks", "album", album.ID, "error", err)
		return view
	}
	view.Tracks = tracks
	view.DurationLabel = formatDurationMillis(tracksDuration(tracks))
	for _, track := range tracks {
		if spec := homeSpecLabel(track); spec != "" {
			view.Spec = spec
			break
		}
	}
	return view
}

func homeSeriesViewFrom(spot *storage.HomeSpotlight) *homeSeriesView {
	view := &homeSeriesView{SeriesID: spot.SeriesID, IsSeries: spot.SeriesID > 0, Title: spot.SeriesTitle, Works: spot.Works}
	if view.Title == "" && len(spot.Works) > 0 {
		view.Title = spot.Works[0].Work.Title
	}
	seen := map[int64]bool{}
	workIDs := make([]string, 0, len(spot.Works))
	minYear, maxYear := 0, 0
	for _, work := range spot.Works {
		workIDs = append(workIDs, strconv.FormatInt(work.Work.ID, 10))
		view.TrackTotal += work.Work.TrackCount
		if work.Work.Year > 0 && (minYear == 0 || work.Work.Year < minYear) {
			minYear = work.Work.Year
		}
		if work.Work.Year > maxYear {
			maxYear = work.Work.Year
		}
		for _, album := range work.Albums {
			if seen[album.AlbumID] {
				continue
			}
			seen[album.AlbumID] = true
			view.Albums = append(view.Albums, homeSeriesAlbumView{WorkTitle: work.Work.Title, Album: album})
		}
	}
	view.AlbumCount = len(view.Albums)
	if len(view.Albums) > 8 {
		view.Albums = view.Albums[:8]
	}
	view.WorkIDsParam = strings.Join(workIDs, ",")
	if minYear > 0 {
		if minYear == maxYear {
			view.YearRange = strconv.Itoa(minYear)
		} else {
			view.YearRange = fmt.Sprintf("%d–%d", minYear, maxYear)
		}
	}
	return view
}

// homeHero assembles every focus pane in one pass so tab switching is pure
// front-end (no refetch).
func (a *App) homeHero(ctx context.Context, csrfToken string) homeHeroView {
	hero := homeHeroView{}
	if albums, err := a.store.ListAlbums(ctx, storage.Filters{Sort: "added", Limit: 1}); err == nil && len(albums) > 0 {
		album := albums[0]
		hero.Recent = a.homeAlbumSpotlight(ctx, album, "入库于 "+formatAdminTime(album.AddedAt))
		hero.Recent.Focus = "recent"
	} else if err != nil {
		a.logger.Warn("home recent album", "error", err)
	}
	if unfinished, err := a.store.UnfinishedPlayback(ctx, 1); err == nil && len(unfinished) > 0 {
		record := unfinished[0]
		if album, err := a.store.AlbumByID(ctx, record.Track.AlbumID); err == nil {
			view := a.homeAlbumSpotlight(ctx, album, fmt.Sprintf("上次播放 %s · 听到 %s %s", formatAdminTime(record.LastPlayedAt), record.Track.Title, formatDurationMillis(record.PositionMillis)))
			view.Listening = true
			view.Focus = "listening"
			view.ResumeTrackID = record.TrackID
			view.ResumeMS = record.PositionMillis
			view.ResumeLabel = "从 " + formatDurationMillis(record.PositionMillis) + " 继续"
			hero.Listening = view
		}
	} else if err != nil {
		a.logger.Warn("home listening spotlight", "error", err)
	}
	if spot, err := a.store.HomeSeriesSpotlight(ctx); err == nil && spot != nil {
		hero.Series = homeSeriesViewFrom(spot)
	} else if err != nil {
		a.logger.Warn("home series spotlight", "error", err)
	}
	if album, err := a.store.RandomAlbum(ctx, 0); err == nil {
		note := "从未播放过"
		if album.LastPlayedAt != "" {
			note = "上次播放 " + formatAdminTime(album.LastPlayedAt)
		}
		hero.Random = a.homeAlbumSpotlight(ctx, album, note)
		hero.Random.Focus = "random"
	} else if !errors.Is(err, sql.ErrNoRows) {
		a.logger.Warn("home random album", "error", err)
	}
	hero.Tabs = homeFocusTabs("recent", hero.Listening != nil, hero.Series != nil)
	for _, pane := range []*homeHeroAlbumView{hero.Recent, hero.Listening, hero.Random} {
		if pane != nil {
			pane.Tabs = hero.Tabs
			pane.CSRFToken = csrfToken
		}
	}
	if hero.Series != nil {
		hero.Series.Tabs = hero.Tabs
	}
	return hero
}

// homeAlbumCard wraps an album with its card meta line.
func homeAlbumCard(album storage.Album, meta string) homeAlbumView {
	return homeAlbumView{Album: album, Meta: meta}
}

// homeChronicle loads one year of the chronicle chart result.
func (a *App) homeChronicle(ctx context.Context, year int) (homeChronicleView, error) {
	view := homeChronicleView{Year: year}
	albums, err := a.store.ListAlbums(ctx, storage.Filters{Year: year, Sort: "added", Limit: 12})
	if err != nil {
		return view, err
	}
	for _, album := range albums {
		meta := ""
		if kind := albumKindLabel(album); kind != "" {
			meta = kind
		}
		if album.TrackCount > 0 {
			if meta != "" {
				meta += " · "
			}
			meta += strconv.FormatInt(album.TrackCount, 10) + " 首"
		}
		view.Albums = append(view.Albums, homeAlbumCard(album, meta))
	}
	view.Total, err = a.store.CountAlbums(ctx, storage.Filters{Year: year})
	return view, err
}

func (a *App) handleHome(w http.ResponseWriter, r *http.Request) {
	session, _ := a.sessions.get(r)
	ctx := r.Context()
	stats, err := a.store.Statistics(ctx)
	if err != nil {
		a.logger.Error("query home statistics", "error", err)
		http.Error(w, "home unavailable", http.StatusInternalServerError)
		return
	}
	data := homePageData{
		Chrome: a.chromeFor(ctx, session, "home"),
		Today:  homeToday(),
		Stats:  stats,
	}
	if scan := latestScan(a.store, r); scan.FinishedAt != "" {
		data.LastScan = formatAdminTime(scan.FinishedAt)
	}
	if stats.Albums == 0 {
		data.Empty = true
		a.render(w, http.StatusOK, "home.html", data)
		return
	}

	data.Hero = a.homeHero(ctx, session.CSRFToken)

	if unfinished, err := a.store.UnfinishedPlayback(ctx, 5); err == nil {
		for _, record := range unfinished {
			data.Resume = append(data.Resume, homeResumeRow{Record: record, Percent: percentDone(int(record.PositionMillis), int(record.DurationMillis))})
		}
	} else {
		a.logger.Warn("home resume rows", "error", err)
	}
	if albums, err := a.store.ListAlbums(ctx, storage.Filters{Sort: "added", Limit: 12}); err == nil {
		for _, album := range albums {
			data.RecentAlbums = append(data.RecentAlbums, homeAlbumCard(album, "入库于 "+formatAdminTime(album.AddedAt)))
		}
	} else {
		a.logger.Warn("home recent albums", "error", err)
	}
	if works, err := a.store.HomeWorks(ctx, "", 24); err == nil {
		for _, work := range works {
			cover := workPosterURL(a.enrichment, storage.Work{ID: work.ID, PosterURL: work.PosterURL}, 360)
			if cover == "" {
				cover = thumbURL(work.CoverURL, 360)
			}
			data.Works = append(data.Works, homeWorkView{Work: work, Cover: cover})
		}
	} else {
		a.logger.Warn("home works", "error", err)
	}
	if total, err := a.store.CountWorks(ctx, storage.WorkFilters{}); err == nil {
		data.WorksCount = total
	}
	if tracks, err := a.store.HomeRoleTracks(ctx, 20); err == nil {
		data.RoleTracks = tracks
	} else {
		a.logger.Warn("home role tracks", "error", err)
	}
	if years, err := a.store.AlbumYearFacets(ctx, storage.Filters{}); err == nil && len(years) > 0 {
		var maxCount int64
		for _, year := range years {
			if year.Count > maxCount {
				maxCount = year.Count
			}
		}
		selectedYear := years[0].Year
		for i := len(years) - 1; i >= 0; i-- {
			height := 4
			if maxCount > 0 {
				height = int(years[i].Count * 100 / maxCount)
				if height < 4 {
					height = 4
				}
			}
			selected := years[i].Year == selectedYear
			axisClass := ""
			switch {
			case selected:
				axisClass = "on"
			case years[i].Year%2 != 0:
				axisClass = "dim"
			}
			data.Years = append(data.Years, homeYearBar{Year: years[i].Year, Count: years[i].Count, Height: height, Selected: selected, AxisClass: axisClass})
		}
		if chronicle, err := a.homeChronicle(ctx, selectedYear); err == nil {
			data.Chronicle = chronicle
		} else {
			a.logger.Warn("home chronicle", "error", err)
		}
	} else if err != nil {
		a.logger.Warn("home year facets", "error", err)
	}
	if playlists, _, err := a.store.ListPlaylists(ctx, 20, 0); err == nil {
		data.Playlists = playlists
	} else {
		a.logger.Warn("home playlists", "error", err)
	}
	a.render(w, http.StatusOK, "home.html", data)
}

// handleHomeRandomSpotlight re-renders the “随机重温” pane for 换一张.
// The response is an HTML fragment: the pane's inner markup.
func (a *App) handleHomeRandomSpotlight(w http.ResponseWriter, r *http.Request) {
	exclude := parseInt64(r.URL.Query().Get("exclude"))
	album, err := a.store.RandomAlbum(r.Context(), exclude)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "no album", http.StatusNotFound)
			return
		}
		a.logger.Error("home random spotlight", "error", err)
		http.Error(w, "home unavailable", http.StatusInternalServerError)
		return
	}
	note := "从未播放过"
	if album.LastPlayedAt != "" {
		note = "上次播放 " + formatAdminTime(album.LastPlayedAt)
	}
	view := a.homeAlbumSpotlight(r.Context(), album, note)
	view.Focus = "random"
	// The fragment re-renders the whole pane, tabs included, so tab
	// availability is recomputed here the same way the full page does.
	listening := false
	if unfinished, err := a.store.UnfinishedPlayback(r.Context(), 1); err == nil {
		listening = len(unfinished) > 0
	}
	series := false
	if spot, err := a.store.HomeSeriesSpotlight(r.Context()); err == nil {
		series = spot != nil
	}
	view.Tabs = homeFocusTabs("random", listening, series)
	session, _ := a.sessions.get(r)
	view.CSRFToken = session.CSRFToken
	a.render(w, http.StatusOK, "home-hero-album", view)
}

// handleHomeChronicle re-renders the album shelf of one chronicle year.
func (a *App) handleHomeChronicle(w http.ResponseWriter, r *http.Request) {
	year := int(parseInt64(r.URL.Query().Get("year")))
	if year <= 0 {
		http.Error(w, "invalid year", http.StatusBadRequest)
		return
	}
	chronicle, err := a.homeChronicle(r.Context(), year)
	if err != nil {
		a.logger.Error("home chronicle", "error", err)
		http.Error(w, "home unavailable", http.StatusInternalServerError)
		return
	}
	a.render(w, http.StatusOK, "home-chronicle-result", chronicle)
}
