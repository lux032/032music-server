package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

// homeAPIWire mirrors the documented /api/v1/home payload (docs/api/home.md)
// strictly enough for the shape assertions below.
type homeAPIWire struct {
	Empty       bool   `json:"empty"`
	GeneratedAt string `json:"generatedAt"`
	RotationDay string `json:"rotationDay"`
	Spotlight   *struct {
		Tabs       []string              `json:"tabs"`
		DefaultTab string                `json:"defaultTab"`
		Recent     *homeAPIAlbumPaneWire `json:"recent"`
		Listening  *homeAPIAlbumPaneWire `json:"listening"`
		Random     *homeAPIAlbumPaneWire `json:"random"`
		Series     *struct {
			IsSeries bool           `json:"isSeries"`
			SeriesID int64          `json:"seriesId"`
			Title    string         `json:"title"`
			Play     homeAPIPlayRef `json:"play"`
			WorkIDs  []int64        `json:"workIds"`
			Works    []struct {
				Work       storage.Work       `json:"work"`
				RoleCounts []homeAPIRoleCount `json:"roleCounts"`
			} `json:"works"`
			Albums []struct {
				WorkID    int64             `json:"workId"`
				WorkTitle string            `json:"workTitle"`
				Album     storage.AlbumWork `json:"album"`
				Play      homeAPIPlayRef    `json:"play"`
			} `json:"albums"`
			AlbumCount int   `json:"albumCount"`
			TrackTotal int64 `json:"trackTotal"`
			YearFrom   int   `json:"yearFrom"`
			YearTo     int   `json:"yearTo"`
		} `json:"series"`
	} `json:"spotlight"`
	Resume       []storage.PlaybackRecord `json:"resume"`
	RecentAlbums []storage.Album          `json:"recentAlbums"`
	Works        homeWorksPageWire        `json:"works"`
	Songs        homeSongsPageWire        `json:"songs"`
	Chronicle    *struct {
		Years        []homeAPIYear     `json:"years"`
		SelectedYear int               `json:"selectedYear"`
		Result       homeChronicleWire `json:"result"`
	} `json:"chronicle"`
	Playlists []storage.Playlist `json:"playlists"`
	Stats     homeAPIStats       `json:"stats"`
}

type homeAPIAlbumPaneWire struct {
	Focus                 string                  `json:"focus"`
	Play                  homeAPIPlayRef          `json:"play"`
	Album                 storage.Album           `json:"album"`
	Tracks                []storage.Track         `json:"tracks"`
	PreviewDurationMillis int64                   `json:"previewDurationMillis"`
	NoteTime              string                  `json:"noteTime"`
	Resume                *storage.PlaybackRecord `json:"resume"`
}

type homeRotationWire struct {
	Type       string `json:"type"`
	Generation string `json:"generation"`
	Batch      int    `json:"batch"`
	Pages      int    `json:"pages"`
	Count      int    `json:"count"`
	Renewed    bool   `json:"renewed"`
}

type homeWorksPageWire struct {
	homeRotationWire
	Items []homeAPIWorkCard `json:"items"`
}

type homeSongsPageWire struct {
	homeRotationWire
	Items []struct {
		Track storage.Track   `json:"track"`
		Role  string          `json:"role"`
		Work  homeAPISongWork `json:"work"`
		Play  homeAPIPlayRef  `json:"play"`
	} `json:"items"`
}

type homeChronicleWire struct {
	Year   int             `json:"year"`
	Total  int64           `json:"total"`
	Albums []storage.Album `json:"albums"`
}

func decodeHomeJSON(t *testing.T, rec *httptest.ResponseRecorder, into any) {
	t.Helper()
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("content type = %q", rec.Header().Get("Content-Type"))
	}
	decoder := json.NewDecoder(strings.NewReader(rec.Body.String()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		t.Fatalf("decode %T: %v\n%s", into, err, rec.Body.String())
	}
}

// seedHomeAPILibrary builds a library with every home section populated:
// two works in a series (OP + ED), an unfinished play, a playlist.
func seedHomeAPILibrary(t *testing.T, app *App, store *storage.Store) (albumID int64, tracks, workIDs []int64, seriesID int64) {
	t.Helper()
	ctx := context.Background()
	albumID, tracks = seedHomeLibrary(t, app, store)
	for i, title := range []string{"Anime A", "Game B"} {
		work, err := store.CreateWork(ctx, storage.WorkInput{Title: title, Type: []string{"anime", "game"}[i], Year: 2020 + i})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AddWorkAlbum(ctx, work.ID, albumID, "ost"); err != nil {
			t.Fatal(err)
		}
		if err := store.AddWorkTrack(ctx, work.ID, storage.WorkTrackInput{TrackID: tracks[i], Role: []string{"op", "ed"}[i]}); err != nil {
			t.Fatal(err)
		}
		workIDs = append(workIDs, work.ID)
	}
	seriesID, err := store.CreateWorkSeries(ctx, "Saga", workIDs)
	if err != nil {
		t.Fatal(err)
	}
	for seq, event := range []storage.PlaybackEventInput{
		{Type: "start", State: "playing"},
		{Type: "pause", PositionMillis: 64000},
	} {
		event.ClientID, event.ClientKind, event.SessionID, event.Seq = "test", "web", "home-api-session-1", int64(seq+1)
		event.TrackID, event.DurationMillis = tracks[1], 240000
		if _, err := store.RecordPlaybackEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CreatePlaylistWithItems(ctx, "通勤", "", tracks); err != nil {
		t.Fatal(err)
	}
	return albumID, tracks, workIDs, seriesID
}

func fixedHomeDay(app *App) {
	app.homeRotation.now = func() time.Time { return time.Date(2026, 10, 11, 4, 0, 0, 0, time.UTC) }
}

func TestAPIHomeAuth(t *testing.T) {
	app, store, _ := setupTestApp(t)
	seedHomeAPILibrary(t, app, store)
	for _, path := range []string{"/api/v1/home", "/api/v1/home/spotlight/random", "/api/v1/home/works?type=anime", "/api/v1/home/songs", "/api/v1/home/chronicle?year=2024"} {
		assertAPIAuth(t, app, path)
	}
	// Errors stay JSON for API clients.
	rec := apiGet(t, app, "/api/v1/home", "", nil)
	var body struct {
		Error struct{ Code string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error.Code != "unauthorized" {
		t.Fatalf("unauthorized body: %s", rec.Body.String())
	}
}

func TestAPIHomeStructure(t *testing.T) {
	app, store, _ := setupTestApp(t)
	fixedHomeDay(app)
	albumID, tracks, workIDs, seriesID := seedHomeAPILibrary(t, app, store)

	rec := apiGet(t, app, "/api/v1/home", testAppAPIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	raw := rec.Body.String()
	if strings.ContainsAny(raw, "<>") {
		t.Fatalf("JSON must not carry markup: %s", raw)
	}
	for _, label := range []string{"入库于", "上次播放", "继续", "首未听完"} {
		if strings.Contains(raw, label) {
			t.Fatalf("JSON must not carry display copy %q", label)
		}
	}
	var home homeAPIWire
	decodeHomeJSON(t, rec, &home)

	if home.Empty || home.RotationDay != "2026-10-11" {
		t.Fatalf("empty=%v rotationDay=%q", home.Empty, home.RotationDay)
	}
	if _, err := time.Parse(time.RFC3339Nano, home.GeneratedAt); err != nil {
		t.Fatalf("generatedAt %q: %v", home.GeneratedAt, err)
	}
	spot := home.Spotlight
	if spot == nil || strings.Join(spot.Tabs, ",") != "recent,listening,series,random" || spot.DefaultTab != "recent" {
		t.Fatalf("spotlight tabs: %+v", spot)
	}
	recent := spot.Recent
	if recent == nil || recent.Focus != "recent" || recent.Play != (homeAPIPlayRef{Type: "album", ID: albumID}) || recent.Album.ID != albumID {
		t.Fatalf("recent pane: %+v", recent)
	}
	if len(recent.Album.Artists) != 1 || recent.Album.Artists[0].Name != "Singer" {
		t.Fatalf("albums carry the API artist refs: %+v", recent.Album.Artists)
	}
	if len(recent.Tracks) != 3 || recent.Tracks[0].StreamURL == "" || recent.Resume != nil {
		t.Fatalf("recent tracks: %+v", recent.Tracks)
	}
	if _, err := time.Parse(time.RFC3339Nano, recent.NoteTime); err != nil || recent.NoteTime != recent.Album.AddedAt {
		t.Fatalf("recent noteTime %q (addedAt %q): %v", recent.NoteTime, recent.Album.AddedAt, err)
	}
	listening := spot.Listening
	if listening == nil || listening.Focus != "listening" || listening.Resume == nil || listening.Resume.TrackID != tracks[1] || listening.Resume.PositionMillis != 64000 || listening.Resume.Track.ID != tracks[1] {
		t.Fatalf("listening pane: %+v", listening)
	}
	if listening.NoteTime != listening.Resume.LastPlayedAt {
		t.Fatalf("listening noteTime %q vs lastPlayedAt %q", listening.NoteTime, listening.Resume.LastPlayedAt)
	}
	if spot.Random == nil || spot.Random.Focus != "random" || spot.Random.Play.Type != "album" {
		t.Fatalf("random pane: %+v", spot.Random)
	}
	series := spot.Series
	if series == nil || !series.IsSeries || series.SeriesID != seriesID || series.Title != "Saga" || series.Play != (homeAPIPlayRef{Type: "series", ID: seriesID}) {
		t.Fatalf("series pane: %+v", series)
	}
	if len(series.WorkIDs) != 2 || series.YearFrom != 2020 || series.YearTo != 2021 || series.AlbumCount != 1 || series.TrackTotal != 6 {
		t.Fatalf("series facts: %+v", series)
	}
	if len(series.Albums) != 1 || series.Albums[0].Play != (homeAPIPlayRef{Type: "album", ID: albumID}) || series.Albums[0].WorkID != series.Works[0].Work.ID {
		t.Fatalf("series albums: %+v", series.Albums)
	}
	if len(series.Works[0].RoleCounts) != 1 || series.Works[0].RoleCounts[0] != (homeAPIRoleCount{Role: "op", Count: 1}) {
		t.Fatalf("series role counts: %+v", series.Works[0].RoleCounts)
	}

	if len(home.Resume) != 1 || home.Resume[0].TrackID != tracks[1] || home.Resume[0].DurationMillis != 240000 {
		t.Fatalf("resume: %+v", home.Resume)
	}
	if len(home.RecentAlbums) != 1 || len(home.RecentAlbums[0].Artists) != 1 {
		t.Fatalf("recent albums: %+v", home.RecentAlbums)
	}
	works := home.Works
	if works.Type != "all" || works.Generation == "" || !strings.HasPrefix(works.Generation, "2026-10-11.") || works.Count != 2 || works.Pages != 1 || works.Renewed || len(works.Items) != 2 {
		t.Fatalf("works page: %+v", works)
	}
	for _, card := range works.Items {
		if card.Play != (homeAPIPlayRef{Type: "work", ID: card.ID}) || card.Series == nil || card.Series.ID != seriesID || card.Series.Title != "Saga" {
			t.Fatalf("work card: %+v", card)
		}
		// The test albums have no artwork, so the cover fallback is empty too.
		if card.PosterURL != "" || card.ArtworkURL != card.CoverURL {
			t.Fatalf("work card artwork without a cached poster: %+v", card)
		}
		if card.ID == workIDs[1] && (card.WorkType != "game" || card.Year != 2021 || card.TrackCount != 3) {
			t.Fatalf("work card fields: %+v", card)
		}
	}
	songs := home.Songs
	if songs.Type != "" || songs.Count != 2 || len(songs.Items) != 2 {
		t.Fatalf("songs page: %+v", songs)
	}
	for _, row := range songs.Items {
		if row.Play != (homeAPIPlayRef{Type: "track", ID: row.Track.ID}) || row.Work.ID == 0 || row.Work.Title == "" || (row.Role != "op" && row.Role != "ed") {
			t.Fatalf("song row: %+v", row)
		}
	}
	chronicle := home.Chronicle
	if chronicle == nil || len(chronicle.Years) != 1 || chronicle.Years[0] != (homeAPIYear{Year: 2024, Count: 1}) || chronicle.SelectedYear != 2024 {
		t.Fatalf("chronicle: %+v", chronicle)
	}
	if chronicle.Result.Year != 2024 || chronicle.Result.Total != 1 || len(chronicle.Result.Albums) != 1 || len(chronicle.Result.Albums[0].Artists) != 1 {
		t.Fatalf("chronicle result: %+v", chronicle.Result)
	}
	if len(home.Playlists) != 1 || home.Playlists[0].Name != "通勤" || home.Playlists[0].ItemCount != 3 {
		t.Fatalf("playlists: %+v", home.Playlists)
	}
	if home.Stats.Albums != 1 || home.Stats.Tracks != 3 || home.Stats.Works != 2 {
		t.Fatalf("stats: %+v", home.Stats)
	}
}

func TestAPIHomeEmptyLibrary(t *testing.T) {
	app, _, _ := setupTestApp(t)
	rec := apiGet(t, app, "/api/v1/home", testAppAPIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	raw := rec.Body.String()
	for _, want := range []string{`"empty":true`, `"spotlight":null`, `"resume":[]`, `"recentAlbums":[]`, `"chronicle":null`, `"playlists":[]`, `"items":[]`} {
		if !strings.Contains(raw, want) {
			t.Errorf("empty payload missing %s: %s", want, raw)
		}
	}
	var home homeAPIWire
	decodeHomeJSON(t, rec, &home)
	if home.Works.Type != "all" || home.Works.Pages != 0 || home.Songs.Count != 0 || home.Stats != (homeAPIStats{}) {
		t.Fatalf("empty payload: %+v", home)
	}

	rec = apiGet(t, app, "/api/v1/home/spotlight/random", testAppAPIToken, nil)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"not_found"`) {
		t.Fatalf("random on empty library: %d %s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/api/v1/home/works", "/api/v1/home/songs"} {
		rec := apiGet(t, app, path, testAppAPIToken, nil)
		var page homeWorksPageWire
		decodeHomeJSON(t, rec, &page)
		if rec.Code != http.StatusOK || page.Pages != 0 || page.Items == nil || len(page.Items) != 0 {
			t.Fatalf("%s on empty library: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	rec = apiGet(t, app, "/api/v1/home/chronicle?year=2024", testAppAPIToken, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"albums":[]`) {
		t.Fatalf("chronicle on empty library: %d %s", rec.Code, rec.Body.String())
	}
}

func homeAPIGetWorks(t *testing.T, app *App, path string) (homeWorksPageWire, *httptest.ResponseRecorder) {
	t.Helper()
	rec := apiGet(t, app, path, testAppAPIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
	}
	var page homeWorksPageWire
	decodeHomeJSON(t, rec, &page)
	return page, rec
}

func homeAPIWorkIDs(page homeWorksPageWire) []string {
	ids := make([]string, 0, len(page.Items))
	for _, card := range page.Items {
		ids = append(ids, strconv.FormatInt(card.ID, 10))
	}
	return ids
}

var homeGenerationAttr = regexp.MustCompile(`data-generation="([^"]*)"`)

// The HTML fragments and the JSON endpoints read one frozen rotation: the
// same day yields the same pages, in the same order, on both routes.
func TestAPIHomeRotationMatchesHTML(t *testing.T) {
	ctx := context.Background()
	app, store, _ := setupTestApp(t)
	movieID := seedRotationWorks(t, app, store)
	fixedHomeDay(app)
	cookie := loginCookie(t, app)

	first, _ := homeAPIGetWorks(t, app, "/api/v1/home/works")
	if first.Type != "all" || first.Count != 27 || first.Pages != 2 || first.Batch != 0 || len(first.Items) != 24 {
		t.Fatalf("works page 0: %+v", first.homeRotationWire)
	}
	html := getHome(t, app, cookie, "/admin/home/works?type=all&batch=0").Body.String()
	if got, want := strings.Join(homeAPIWorkIDs(first), ","), strings.Join(workIDsIn(html), ","); got != want {
		t.Fatalf("JSON and HTML page 0 differ:\njson %s\nhtml %s", got, want)
	}
	if m := homeGenerationAttr.FindStringSubmatch(html); m == nil || m[1] != first.Generation {
		t.Fatalf("generation differs: html %v json %q", m, first.Generation)
	}
	again, _ := homeAPIGetWorks(t, app, "/api/v1/home/works?type=all&batch=0&generation="+first.Generation)
	if strings.Join(homeAPIWorkIDs(again), ",") != strings.Join(homeAPIWorkIDs(first), ",") || again.Renewed {
		t.Fatal("the same day must return the same page")
	}
	page1, _ := homeAPIGetWorks(t, app, "/api/v1/home/works?type=all&batch=1&generation="+first.Generation)
	html1 := getHome(t, app, cookie, "/admin/home/works?type=all&batch=1&gen="+first.Generation).Body.String()
	if got, want := strings.Join(homeAPIWorkIDs(page1), ","), strings.Join(workIDsIn(html1), ","); got != want || page1.Batch != 1 || len(page1.Items) != 3 {
		t.Fatalf("JSON and HTML page 1 differ: %s vs %s", got, want)
	}
	wrapped, _ := homeAPIGetWorks(t, app, "/api/v1/home/works?batch=2")
	if wrapped.Batch != 0 || strings.Join(homeAPIWorkIDs(wrapped), ",") != strings.Join(homeAPIWorkIDs(first), ",") {
		t.Fatal("a batch past the last page wraps to page 0")
	}
	movie, _ := homeAPIGetWorks(t, app, "/api/v1/home/works?type=movie")
	if movie.Type != "movie" || len(movie.Items) != 1 || movie.Items[0].ID != movieID || movie.Items[0].WorkType != "movie" {
		t.Fatalf("movie filter: %+v", movie)
	}

	// The first screen embeds the same page 0 as the works endpoint.
	var home homeAPIWire
	decodeHomeJSON(t, apiGet(t, app, "/api/v1/home", testAppAPIToken, nil), &home)
	if strings.Join(homeAPIWorkIDs(home.Works), ",") != strings.Join(homeAPIWorkIDs(first), ",") || home.Works.Generation != first.Generation {
		t.Fatal("/api/v1/home must embed page 0 of the frozen works view")
	}
	homeHTML := getHome(t, app, cookie, "/admin/home").Body.String()
	if !strings.Contains(homeHTML, `data-generation="`+first.Generation+`"`) {
		t.Fatal("the HTML page must render the same generation")
	}

	// A stale generation renews to page 0, flagged in body and header.
	stale, rec := homeAPIGetWorks(t, app, "/api/v1/home/works?batch=1&generation=2026-01-01.stale")
	if !stale.Renewed || stale.Batch != 0 || rec.Header().Get("X-Home-Rotation") != "renewed" || stale.Generation != first.Generation {
		t.Fatalf("stale generation: %+v %v", stale.homeRotationWire, rec.Header())
	}
	if alias, _ := homeAPIGetWorks(t, app, "/api/v1/home/works?batch=1&gen=2026-01-01.stale"); !alias.Renewed {
		t.Fatal("gen must be accepted as an alias of generation")
	}

	// Songs: same picks on both routes, frozen against plays during the day.
	songsRec := apiGet(t, app, "/api/v1/home/songs", testAppAPIToken, nil)
	var songs homeSongsPageWire
	decodeHomeJSON(t, songsRec, &songs)
	picks := make([]string, 0, len(songs.Items))
	for _, row := range songs.Items {
		picks = append(picks, strconv.FormatInt(row.Track.ID, 10)+"@"+strconv.FormatInt(row.Work.ID, 10))
	}
	if got, want := strings.Join(picks, ","), songPicksIn(getHome(t, app, cookie, "/admin/home/songs").Body.String()); got != want || songs.Count != 3 {
		t.Fatalf("songs differ: json %s html %s", got, want)
	}
	tracks, err := store.ListTracks(ctx, storage.Filters{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for i, track := range tracks {
		if _, err := store.RecordPlaybackEvent(ctx, storage.PlaybackEventInput{ClientID: "test", ClientKind: "web", SessionID: "home-api-play-" + strconv.Itoa(i), Seq: 1, Type: "start", TrackID: track.ID, State: "playing", DurationMillis: 240000}); err != nil {
			t.Fatal(err)
		}
	}
	var after homeSongsPageWire
	decodeHomeJSON(t, apiGet(t, app, "/api/v1/home/songs?generation="+songs.Generation, testAppAPIToken, nil), &after)
	for i, row := range after.Items {
		if row.Track.ID != songs.Items[i].Track.ID || row.Work.ID != songs.Items[i].Work.ID {
			t.Fatal("listening during the day must not reshuffle the JSON rotation")
		}
	}

	for _, path := range []string{"/api/v1/home/works?type=other", "/api/v1/home/works?batch=-1", "/api/v1/home/works?batch=abc", "/api/v1/home/songs?batch=x"} {
		rec := apiGet(t, app, path, testAppAPIToken, nil)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"invalid_parameters"`) {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestAPIHomeRandomAndChronicle(t *testing.T) {
	app, store, _ := setupTestApp(t)
	albumID, _ := seedHomeLibrary(t, app, store)

	for _, path := range []string{"/api/v1/home/spotlight/random", "/api/v1/home/spotlight/random?exclude=" + itoa(albumID)} {
		rec := apiGet(t, app, path, testAppAPIToken, nil)
		var pane homeAPIAlbumPaneWire
		decodeHomeJSON(t, rec, &pane)
		if pane.Focus != "random" || pane.Album.ID != albumID || pane.Play != (homeAPIPlayRef{Type: "album", ID: albumID}) || len(pane.Tracks) != 3 || pane.NoteTime != "" || pane.Resume != nil {
			t.Fatalf("%s: %+v", path, pane)
		}
		if pane.PreviewDurationMillis != tracksDuration(pane.Tracks) {
			t.Fatalf("previewDurationMillis = %d", pane.PreviewDurationMillis)
		}
	}

	rec := apiGet(t, app, "/api/v1/home/chronicle?year=2024", testAppAPIToken, nil)
	var result homeChronicleWire
	decodeHomeJSON(t, rec, &result)
	if result.Year != 2024 || result.Total != 1 || len(result.Albums) != 1 || result.Albums[0].ID != albumID {
		t.Fatalf("chronicle 2024: %+v", result)
	}
	rec = apiGet(t, app, "/api/v1/home/chronicle?year=1999", testAppAPIToken, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"albums":[]`) || !strings.Contains(rec.Body.String(), `"total":0`) {
		t.Fatalf("chronicle 1999: %s", rec.Body.String())
	}
	for _, path := range []string{"/api/v1/home/chronicle", "/api/v1/home/chronicle?year=abc", "/api/v1/home/chronicle?year=-3"} {
		rec := apiGet(t, app, path, testAppAPIToken, nil)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"invalid_year"`) {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestAPIHomeWorkCardPoster(t *testing.T) {
	app, store, handler, _, cookie, _, posterDir := batch8PosterApp(t)
	albumID, _ := seedHomeLibrary(t, app, store)
	work := seedCachedWorkPoster(t, store, posterDir, "home-card")
	if err := store.AddWorkAlbum(context.Background(), work.ID, albumID, "ost"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/home/works", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var page homeWorksPageWire
	decodeHomeJSON(t, rec, &page)
	if len(page.Items) != 1 {
		t.Fatalf("works: %+v", page)
	}
	card := page.Items[0]
	prefix := "/api/v1/works/" + itoa(work.ID) + "/poster?v="
	if !strings.HasPrefix(card.PosterURL, prefix) || card.ArtworkURL != card.PosterURL || card.Series != nil {
		t.Fatalf("poster card: %+v", card)
	}
	// The web fragment keeps its own /admin poster URL.
	html := getAdmin(t, handler, cookie, "/admin/home/works")
	if !strings.Contains(html, "/admin/works/"+itoa(work.ID)+"/poster?v=") {
		t.Fatalf("web poster url changed: %s", html)
	}
}

func TestCapabilitiesAdvertiseHome(t *testing.T) {
	app, _, _ := setupTestApp(t)
	rec := apiGet(t, app, "/api/v1/capabilities", testAppAPIToken, nil)
	var body struct {
		Features map[string]bool `json:"features"`
		Home     struct {
			Endpoint    string   `json:"endpoint"`
			WorkFilters []string `json:"workFilters"`
			PlayTypes   []string `json:"playTypes"`
		} `json:"home"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"home", "seriesTracks", "workPosters"} {
		if !body.Features[key] {
			t.Errorf("feature %s missing", key)
		}
	}
	if body.Home.Endpoint != "/api/v1/home" || strings.Join(body.Home.WorkFilters, ",") != "all,anime,game,movie" || len(body.Home.PlayTypes) != 5 {
		t.Fatalf("home capability: %+v", body.Home)
	}
}
