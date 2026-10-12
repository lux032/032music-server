package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/storage"
)

type workAPITestDetail struct {
	storage.Work
	PosterAPIURL     *string           `json:"posterApiUrl"`
	BangumiSubjectID *string           `json:"bangumiSubjectId"`
	Series           *apiWorkSeriesRef `json:"series"`
	SeriesLocked     *bool             `json:"seriesLocked"`
}

func TestAPIWorkDetailSeriesInfo(t *testing.T) {
	ctx := context.Background()
	app, store, _ := setupTestApp(t)
	sagaID, _, works, _, _ := seedSeriesAPILibrary(t, app, store)
	late := works[0] // 3rd of 3 by member order
	path := "/api/v1/works/" + itoa(late.ID)
	assertAPIAuth(t, app, path)

	bind := storage.WorkMatchCandidate{Source: "bangumi", ExternalID: "10380", Title: "Saga Late", Type: "anime", Score: 40}
	if err := store.ManualBindWorkBangumi(ctx, late.ID, bind); err != nil {
		t.Fatal(err)
	}
	var body workAPITestDetail
	rec := apiGet(t, app, path, testAppAPIToken, nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	// The existing Work fields are unchanged.
	if body.ID != late.ID || body.Title != "Saga Late" || body.Type != "anime" || body.Year != 2015 {
		t.Fatalf("work fields = %+v", body.Work)
	}
	if body.PosterAPIURL == nil || *body.PosterAPIURL != "" || body.BangumiSubjectID == nil || *body.BangumiSubjectID != "10380" || body.SeriesLocked == nil || *body.SeriesLocked {
		t.Fatalf("extension fields = %+v", body)
	}
	saga, err := store.WorkSeriesByID(ctx, sagaID)
	if err != nil {
		t.Fatal(err)
	}
	if body.Series == nil || *body.Series != (apiWorkSeriesRef{ID: sagaID, Title: saga.Title, TitleSource: "manual", MemberCount: 3, Position: 3, Source: "manual"}) {
		t.Fatalf("series = %+v", body.Series)
	}
	// Same position as the web work page.
	html := getAdmin(t, app.Handler(), loginCookie(t, app), "/admin/works/"+itoa(late.ID))
	if !strings.Contains(html, "当前：第 3 部") {
		t.Fatal("web page position changed")
	}

	// Detached: no series, locked.
	if err := store.DetachWorkFromSeries(ctx, late.ID); err != nil {
		t.Fatal(err)
	}
	rec = apiGet(t, app, path, "", loginCookie(t, app))
	if !strings.Contains(rec.Body.String(), `"series":null`) || !strings.Contains(rec.Body.String(), `"seriesLocked":true`) {
		t.Fatalf("detached work: %s", rec.Body.String())
	}

	// A work outside any series: series null, not locked, no Bangumi binding.
	plain, err := store.CreateWork(ctx, storage.WorkInput{Title: "Plain", Type: "other"})
	if err != nil {
		t.Fatal(err)
	}
	rec = apiGet(t, app, "/api/v1/works/"+itoa(plain.ID), testAppAPIToken, nil)
	for _, fragment := range []string{`"series":null`, `"seriesLocked":false`, `"bangumiSubjectId":""`, `"posterApiUrl":""`} {
		if !strings.Contains(rec.Body.String(), fragment) {
			t.Fatalf("missing %s in %s", fragment, rec.Body.String())
		}
	}
	if rec := apiGet(t, app, "/api/v1/works/999999", testAppAPIToken, nil); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"not_found"`) {
		t.Fatalf("unknown work: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAPIWorkDetailPosterAPIURL(t *testing.T) {
	_, store, handler, _, _, _, posterDir := batch8PosterApp(t)
	work := seedCachedWorkPoster(t, store, posterDir, "Detail Poster")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/works/"+itoa(work.ID), nil)
	req.Header.Set("Authorization", "Bearer "+testEnvAPIToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var body workAPITestDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.PosterURL != "https://example.com/Detail Poster.jpg" {
		t.Fatalf("posterUrl must keep the stored value: %q", body.PosterURL)
	}
	if body.PosterAPIURL == nil || !strings.HasPrefix(*body.PosterAPIURL, "/api/v1/works/"+itoa(work.ID)+"/poster?v=") {
		t.Fatalf("posterApiUrl = %v", body.PosterAPIURL)
	}
}

func TestCapabilitiesAdvertiseWorkSeriesInfo(t *testing.T) {
	app, _, _ := setupTestApp(t)
	rec := apiGet(t, app, "/api/v1/capabilities", testAppAPIToken, nil)
	var body struct {
		Features map[string]bool `json:"features"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Features["workSeriesInfo"] {
		t.Fatalf("workSeriesInfo missing: %v", body.Features)
	}
}
