package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/storage"
)

type seriesAPITestSeries struct {
	ID                   int64             `json:"id"`
	Title                string            `json:"title"`
	TitleSource          string            `json:"titleSource"`
	MemberCount          int               `json:"memberCount"`
	TypeCounts           []seriesTypeCount `json:"typeCounts"`
	RepresentativeWorkID int64             `json:"representativeWorkId"`
	PosterURL            *string           `json:"posterUrl"`
	CreatedAt            string            `json:"createdAt"`
	UpdatedAt            string            `json:"updatedAt"`
}

type seriesAPITestDetail struct {
	Series  seriesAPITestSeries `json:"series"`
	Members []struct {
		ID              int64          `json:"id"`
		Title           string         `json:"title"`
		ReadingTitle    *string        `json:"readingTitle"`
		TranslatedTitle string         `json:"translatedTitle"`
		WorkType        string         `json:"workType"`
		Year            int            `json:"year"`
		TrackCount      int64          `json:"trackCount"`
		PosterURL       *string        `json:"posterUrl"`
		Source          string         `json:"source"`
		Play            homeAPIPlayRef `json:"play"`
	} `json:"members"`
	Stats apiSeriesStats `json:"stats"`
	Play  homeAPIPlayRef `json:"play"`
}

func decodeAPI(t *testing.T, body []byte, out any) {
	t.Helper()
	if err := json.Unmarshal(body, out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
}

// seedSeriesAPILibrary builds a series whose member order (year first) differs
// from creation order, plus a second series, and links the album to two
// members so album dedupe is visible.
func seedSeriesAPILibrary(t *testing.T, app *App, store *storage.Store) (sagaID, otherID int64, works []storage.Work, albumID int64, tracks []int64) {
	t.Helper()
	ctx := context.Background()
	albumID, tracks = seedHomeLibrary(t, app, store)
	for _, input := range []storage.WorkInput{
		{Title: "Saga Late", Type: "anime", Year: 2015, TranslatedTitle: "后作"},
		{Title: "Saga Game", Type: "game", Year: 2005},
		{Title: "Saga Mid", Type: "anime", Year: 2010},
	} {
		work, err := store.CreateWork(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		works = append(works, work)
	}
	if err := store.AddWorkTrack(ctx, works[0].ID, storage.WorkTrackInput{TrackID: tracks[0], Role: "op"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddWorkTrack(ctx, works[2].ID, storage.WorkTrackInput{TrackID: tracks[1], Role: "ed"}); err != nil {
		t.Fatal(err)
	}
	for _, work := range []storage.Work{works[0], works[2]} {
		if err := store.AddWorkAlbum(ctx, work.ID, albumID, "ost"); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	if sagaID, err = store.CreateWorkSeries(ctx, "Saga", []int64{works[0].ID, works[1].ID, works[2].ID}); err != nil {
		t.Fatal(err)
	}
	other, err := store.CreateWork(ctx, storage.WorkInput{Title: "Lonely Movie", Type: "movie", Year: 1999})
	if err != nil {
		t.Fatal(err)
	}
	if otherID, err = store.CreateWorkSeries(ctx, "", []int64{other.ID}); err != nil {
		t.Fatal(err)
	}
	return sagaID, otherID, works, albumID, tracks
}

func TestAPISeriesList(t *testing.T) {
	app, store, _ := setupTestApp(t)
	sagaID, otherID, _, _, _ := seedSeriesAPILibrary(t, app, store)
	assertAPIAuth(t, app, "/api/v1/series")

	var page struct {
		Items  []seriesAPITestSeries `json:"items"`
		Total  int64                 `json:"total"`
		Limit  int                   `json:"limit"`
		Offset int                   `json:"offset"`
	}
	rec := apiGet(t, app, "/api/v1/series", testAppAPIToken, nil)
	decodeAPI(t, rec.Body.Bytes(), &page)
	if page.Total != 2 || page.Limit != 50 || page.Offset != 0 || len(page.Items) != 2 {
		t.Fatalf("page = %+v", page)
	}
	// Same ordering as /admin/series: title (case-insensitive), then id.
	if page.Items[0].ID != otherID || page.Items[1].ID != sagaID {
		t.Fatalf("order = %+v", page.Items)
	}
	saga := page.Items[1]
	if saga.Title != "Saga" || saga.TitleSource != "manual" || saga.MemberCount != 3 || saga.RepresentativeWorkID == 0 || saga.CreatedAt == "" || saga.UpdatedAt == "" {
		t.Fatalf("saga = %+v", saga)
	}
	if len(saga.TypeCounts) != 2 || saga.TypeCounts[0] != (seriesTypeCount{"anime", 2}) || saga.TypeCounts[1] != (seriesTypeCount{"game", 1}) {
		t.Fatalf("typeCounts = %+v", saga.TypeCounts)
	}
	if saga.PosterURL == nil || *saga.PosterURL != "" {
		t.Fatalf("posterUrl must be present and empty without a cached poster: %v", saga.PosterURL)
	}
	if page.Items[0].TitleSource != "auto" {
		t.Fatalf("untitled series follows its representative: %+v", page.Items[0])
	}

	// q matches member titles like the web search.
	rec = apiGet(t, app, "/api/v1/series?q="+url.QueryEscape("Saga Game"), testAppAPIToken, nil)
	decodeAPI(t, rec.Body.Bytes(), &page)
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != sagaID {
		t.Fatalf("search = %+v", page)
	}
	rec = apiGet(t, app, "/api/v1/series?limit=1&offset=1", testAppAPIToken, nil)
	decodeAPI(t, rec.Body.Bytes(), &page)
	if page.Total != 2 || page.Limit != 1 || page.Offset != 1 || len(page.Items) != 1 || page.Items[0].ID != sagaID {
		t.Fatalf("paged = %+v", page)
	}
	rec = apiGet(t, app, "/api/v1/series?limit=999", testAppAPIToken, nil)
	decodeAPI(t, rec.Body.Bytes(), &page)
	if page.Limit != 50 {
		t.Fatalf("limit above 200 falls back to the default: %d", page.Limit)
	}
	rec = apiGet(t, app, "/api/v1/series?q=nothing-matches", testAppAPIToken, nil)
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("empty result must be an empty array: %s", rec.Body.String())
	}
}

func TestAPISeriesDetail(t *testing.T) {
	app, store, _ := setupTestApp(t)
	sagaID, _, works, _, tracks := seedSeriesAPILibrary(t, app, store)
	path := "/api/v1/series/" + itoa(sagaID)
	assertAPIAuth(t, app, path)

	var body seriesAPITestDetail
	rec := apiGet(t, app, path, "", loginCookie(t, app))
	decodeAPI(t, rec.Body.Bytes(), &body)
	if body.Series.ID != sagaID || body.Series.Title != "Saga" || body.Series.MemberCount != 3 || len(body.Series.TypeCounts) != 2 {
		t.Fatalf("series = %+v", body.Series)
	}
	if body.Play != (homeAPIPlayRef{Type: "series", ID: sagaID}) {
		t.Fatalf("play = %+v", body.Play)
	}
	// Members follow SeriesMembers (earliest first), not creation order.
	want := []int64{works[1].ID, works[2].ID, works[0].ID}
	if len(body.Members) != 3 {
		t.Fatalf("members = %+v", body.Members)
	}
	for i, id := range want {
		if body.Members[i].ID != id {
			t.Fatalf("member %d = %d, want %d", i, body.Members[i].ID, id)
		}
	}
	late := body.Members[2]
	if late.Title != "Saga Late" || late.TranslatedTitle != "后作" || late.ReadingTitle == nil || late.WorkType != "anime" || late.Year != 2015 || late.TrackCount != 3 || late.Source != "manual" || late.PosterURL == nil || late.Play != (homeAPIPlayRef{Type: "work", ID: works[0].ID}) {
		t.Fatalf("member card = %+v", late)
	}
	// The album is linked to two members and counted once; all three album
	// tracks reach the series through it.
	if body.Stats != (apiSeriesStats{MemberCount: 3, YearFrom: 2005, YearTo: 2015, AlbumCount: 1, TrackCount: len(tracks)}) {
		t.Fatalf("stats = %+v", body.Stats)
	}

	// Same order as the web detail page.
	html := getAdmin(t, app.Handler(), loginCookie(t, app), "/admin/series/"+itoa(sagaID))
	last := -1
	for _, member := range body.Members {
		at := strings.Index(html, "<strong>"+member.Title+"</strong>")
		if at <= last {
			t.Fatalf("web order differs at %q", member.Title)
		}
		last = at
	}

	for _, missing := range []string{"/api/v1/series/999999", "/api/v1/series/abc"} {
		rec := apiGet(t, app, missing, testAppAPIToken, nil)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"not_found"`) {
			t.Fatalf("%s: %d %s", missing, rec.Code, rec.Body.String())
		}
	}
}

func TestAPISeriesDetailEmptySeries(t *testing.T) {
	ctx := context.Background()
	app, store, _ := setupTestApp(t)
	work, err := store.CreateWork(ctx, storage.WorkInput{Title: "Solo", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	seriesID, err := store.CreateWorkSeries(ctx, "Empty Shell", []int64{work.ID})
	if err != nil {
		t.Fatal(err)
	}
	httpExec(t, store, `DELETE FROM work_series_members WHERE series_id=?`, seriesID)

	rec := apiGet(t, app, "/api/v1/series/"+itoa(seriesID), testAppAPIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty series: %d %s", rec.Code, rec.Body.String())
	}
	for _, fragment := range []string{`"members":[]`, `"typeCounts":[]`, `"posterUrl":""`, `"stats":{"memberCount":0,"yearFrom":0,"yearTo":0,"albumCount":0,"trackCount":0}`} {
		if !strings.Contains(rec.Body.String(), fragment) {
			t.Fatalf("missing %s in %s", fragment, rec.Body.String())
		}
	}
	rec = apiGet(t, app, "/api/v1/series/"+itoa(seriesID)+"/albums", testAppAPIToken, nil)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"items":[]}` {
		t.Fatalf("empty series albums: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAPISeriesAlbums(t *testing.T) {
	ctx := context.Background()
	app, store, _ := setupTestApp(t)
	sagaID, _, works, albumID, _ := seedSeriesAPILibrary(t, app, store)
	path := "/api/v1/series/" + itoa(sagaID) + "/albums"
	assertAPIAuth(t, app, path)

	var body struct {
		Items []struct {
			WorkID    int64             `json:"workId"`
			WorkTitle string            `json:"workTitle"`
			Album     storage.AlbumWork `json:"album"`
			Play      homeAPIPlayRef    `json:"play"`
		} `json:"items"`
	}
	rec := apiGet(t, app, path, testAppAPIToken, nil)
	decodeAPI(t, rec.Body.Bytes(), &body)
	// Linked to "Saga Mid" (2010) and "Saga Late" (2015): kept once, under the
	// earlier member.
	if len(body.Items) != 1 {
		t.Fatalf("items = %+v", body.Items)
	}
	item := body.Items[0]
	if item.WorkID != works[2].ID || item.WorkTitle != "Saga Mid" || item.Album.AlbumID != albumID || item.Album.WorkID != works[2].ID || item.Album.AlbumRole != "ost" || item.Play != (homeAPIPlayRef{Type: "album", ID: albumID}) {
		t.Fatalf("item = %+v", item)
	}
	if want, err := store.AlbumsForWork(ctx, works[2].ID); err != nil || len(want) != 1 || want[0].Title != item.Album.Title {
		t.Fatalf("album must reuse the AlbumWork object: %+v %v", want, err)
	}
	if rec := apiGet(t, app, "/api/v1/series/999999/albums", testAppAPIToken, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown series albums: %d", rec.Code)
	}
}

func TestAPISeriesPosterURLs(t *testing.T) {
	ctx := context.Background()
	_, store, handler, _, _, _, posterDir := batch8PosterApp(t)
	cached := seedCachedWorkPoster(t, store, posterDir, "Poster Work")
	plain, err := store.CreateWork(ctx, storage.WorkInput{Title: "Plain Work", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	seriesID, err := store.CreateWorkSeries(ctx, "", []int64{cached.ID, plain.ID})
	if err != nil {
		t.Fatal(err)
	}
	series, err := store.WorkSeriesByID(ctx, seriesID)
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string, out any) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+testEnvAPIToken)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		decodeAPI(t, rec.Body.Bytes(), out)
	}
	var detail seriesAPITestDetail
	get("/api/v1/series/"+itoa(seriesID), &detail)
	posterOf := map[int64]string{}
	for _, member := range detail.Members {
		posterOf[member.ID] = *member.PosterURL
	}
	if !strings.HasPrefix(posterOf[cached.ID], "/api/v1/works/"+itoa(cached.ID)+"/poster?v=") || posterOf[plain.ID] != "" {
		t.Fatalf("member posters = %v", posterOf)
	}
	// Neither member has a year, so the earlier id (the cached one) represents.
	if series.RepresentativeWorkID != cached.ID {
		t.Fatalf("representative = %d, want %d", series.RepresentativeWorkID, cached.ID)
	}
	wantSeriesPoster := posterOf[cached.ID]
	if *detail.Series.PosterURL != wantSeriesPoster {
		t.Fatalf("series poster = %q, want representative's %q", *detail.Series.PosterURL, wantSeriesPoster)
	}
	var page struct {
		Items []seriesAPITestSeries `json:"items"`
	}
	get("/api/v1/series", &page)
	if len(page.Items) != 1 || *page.Items[0].PosterURL != wantSeriesPoster {
		t.Fatalf("list poster = %+v", page.Items)
	}
}

func TestCapabilitiesAdvertiseSeries(t *testing.T) {
	app, _, _ := setupTestApp(t)
	rec := apiGet(t, app, "/api/v1/capabilities", testAppAPIToken, nil)
	var body struct {
		Features map[string]bool `json:"features"`
	}
	decodeAPI(t, rec.Body.Bytes(), &body)
	if !body.Features["series"] || !body.Features["seriesTracks"] {
		t.Fatalf("series features missing: %v", body.Features)
	}
}
