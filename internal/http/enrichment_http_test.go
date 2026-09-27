package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func TestEnrichmentRunAPIRequiresTokenAndReturnsAccepted(t *testing.T) {
	app, store, token := setupTestApp(t)
	app.startEnrichment = func(ctx context.Context, request enrichmentRunRequest) (storage.EnrichmentRun, error) {
		return store.CreateEnrichmentRun(ctx, request.Scope, request.TargetID, request.Force, 4)
	}
	handler := app.Handler()
	body := []byte(`{"scope":"all","force":true}`)

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/api/v1/enrichment/run", bytes.NewReader(body)))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/enrichment/run", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start status=%d body=%s", rec.Code, rec.Body.String())
	}
	var run storage.EnrichmentRun
	if err := json.NewDecoder(rec.Body).Decode(&run); err != nil || run.ID == 0 || run.Status != "running" || !run.Force {
		t.Fatalf("run=%#v err=%v", run, err)
	}

	statusReq := httptest.NewRequest(http.MethodGet, "/api/v1/enrichment/jobs/"+jsonNumber(run.ID), nil)
	statusReq.Header.Set("Authorization", "Bearer "+token)
	statusRec := httptest.NewRecorder()
	handler.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK || !bytes.Contains(statusRec.Body.Bytes(), []byte(`"id":`+jsonNumber(run.ID))) {
		t.Fatalf("status=%d body=%s", statusRec.Code, statusRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/enrichment/jobs", nil)
	listReq.Header.Set("Authorization", "Bearer "+token)
	listRec := httptest.NewRecorder()
	handler.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK || !bytes.Contains(listRec.Body.Bytes(), []byte(`"items"`)) {
		t.Fatalf("list status=%d body=%s", listRec.Code, listRec.Body.String())
	}
}

func TestEnrichmentCandidateAPIDecisions(t *testing.T) {
	app, store, token := setupTestApp(t)
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	work, err := store.CreateWork(ctx, storage.WorkInput{Title: "Test Anime", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceWorkMatchCandidates(ctx, work.ID, []storage.WorkMatchCandidate{{Source: "bangumi", ExternalID: "123", Title: "Test Anime", Type: "anime", Year: 2024, Score: 90}}); err != nil {
		t.Fatal(err)
	}
	workCandidates, _ := store.WorkMatchCandidates(ctx, work.ID)

	artistList, err := store.ListArtists(ctx, storage.Filters{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(artistList) == 0 {
		library, _ := store.LibraryByRoot(ctx, "/music")
		if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "Artist/Album/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Track", Album: "Album", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
			t.Fatal(err)
		}
		artistList, _ = store.ListArtists(ctx, storage.Filters{Limit: 1})
	}
	artistID := artistList[0].ID
	if err := store.ReplaceArtistRelationCandidates(ctx, artistID, []storage.ArtistRelationCandidate{{Source: "musicbrainz", ExternalID: "main", RelatedExternalID: "alias", RelatedName: "Alias", RelationType: "alias_of", Score: 85}}); err != nil {
		t.Fatal(err)
	}
	relations, _ := store.ArtistRelationCandidates(ctx, artistID)

	handler := app.Handler()
	post := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	workRec := post("/api/v1/enrichment/works/" + jsonNumber(work.ID) + "/candidates/" + jsonNumber(workCandidates[0].ID) + "/accept")
	if workRec.Code != http.StatusNoContent {
		t.Fatalf("work accept status=%d body=%s", workRec.Code, workRec.Body.String())
	}
	relationRec := post("/api/v1/enrichment/artists/" + jsonNumber(artistID) + "/relations/" + jsonNumber(relations[0].ID) + "/reject")
	if relationRec.Code != http.StatusNoContent {
		t.Fatalf("relation reject status=%d body=%s", relationRec.Code, relationRec.Body.String())
	}
	updated, _ := store.ArtistRelationCandidates(ctx, artistID)
	if updated[0].Status != "rejected" {
		t.Fatalf("relation status=%q", updated[0].Status)
	}
}

func TestAdminEnrichmentRouteIsRegistered(t *testing.T) {
	app, _, _ := setupTestApp(t)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/enrichment", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
		t.Fatalf("admin enrichment route status=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestEnrichmentAPIRejectsRetiredScopes(t *testing.T) {
	app, _, token := setupTestApp(t)
	for _, scope := range []string{"artist", "album"} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/enrichment/run", bytes.NewBufferString(`{"scope":"`+scope+`","targetId":1}`))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", scope, rec.Code, rec.Body.String())
		}
	}
}

func TestEnrichmentReviewsBeyondFirstHundredWorks(t *testing.T) {
	app, store, _ := setupTestApp(t)
	ctx := context.Background()
	var last int64
	for i := 0; i < 150; i++ {
		work, err := store.CreateWork(ctx, storage.WorkInput{Title: jsonNumber(int64(i)), Type: "anime"})
		if err != nil {
			t.Fatal(err)
		}
		last = work.ID
		if err = store.ReplaceWorkMatchCandidates(ctx, work.ID, []storage.WorkMatchCandidate{{Source: "bangumi", ExternalID: "1", Title: "Candidate"}}); err != nil {
			t.Fatal(err)
		}
	}
	works, _, count, _ := app.enrichmentReviews(ctx)
	if count != 150 || len(works) != 150 || works[149].Work.ID != last {
		t.Fatalf("count=%d displayed=%d last=%d", count, len(works), last)
	}
}

func TestEnrichmentCancelRoutesAndCSRF(t *testing.T) {
	app, _, token := setupTestApp(t)
	handler := app.Handler()
	api := httptest.NewRequest(http.MethodPost, "/api/v1/enrichment/jobs/999/cancel", nil)
	api.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, api)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("API cancel missing=%d %s", rec.Code, rec.Body.String())
	}
	finished, err := app.store.CreateEnrichmentRun(context.Background(), "all", 0, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = app.store.FinishEnrichmentRun(context.Background(), finished.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	api = httptest.NewRequest(http.MethodPost, "/api/v1/enrichment/jobs/"+jsonNumber(finished.ID)+"/cancel", nil)
	api.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, api)
	if rec.Code != http.StatusConflict {
		t.Fatalf("API cancel completed=%d %s", rec.Code, rec.Body.String())
	}
	login := httptest.NewRecorder()
	if _, err := app.sessions.create(login, "admin"); err != nil {
		t.Fatal(err)
	}
	cookie := login.Result().Cookies()[0]
	for _, path := range []string{"/admin/enrichment/runs/999/cancel", "/admin/matches/runs/999/cancel"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s missing CSRF: %d", path, rec.Code)
		}
		req = httptest.NewRequest(http.MethodPost, path, strings.NewReader(url.Values{"csrfToken": {csrfOf(t, app, cookie)}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Errorf("%s CSRF: %d", path, rec.Code)
		}
	}
}

func TestEnrichmentReviewsCapAtTwoHundredAndCountAll(t *testing.T) {
	app, store, _ := setupTestApp(t)
	ctx := context.Background()
	var lastShown int64
	for i := 0; i < 210; i++ {
		work, err := store.CreateWork(ctx, storage.WorkInput{Title: "Pending " + jsonNumber(int64(i)), Type: "anime"})
		if err != nil {
			t.Fatal(err)
		}
		if i == 199 {
			lastShown = work.ID
		}
		if err = store.ReplaceWorkMatchCandidates(ctx, work.ID, []storage.WorkMatchCandidate{{Source: "bangumi", ExternalID: "1", Title: "Candidate"}}); err != nil {
			t.Fatal(err)
		}
	}
	works, _, count, _ := app.enrichmentReviews(ctx)
	if count != 210 || len(works) != 200 || works[199].Work.ID != lastShown {
		t.Fatalf("count=%d shown=%d last=%d want=%d", count, len(works), works[len(works)-1].Work.ID, lastShown)
	}
}
