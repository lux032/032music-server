package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	for _, scope := range []string{"artist", "retired-vgmdb"} {
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

func TestAlbumBangumiCandidateAPIRejectAndAccept(t *testing.T) {
	app, store, token := setupTestApp(t)
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Music", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, _ := store.LibraryByRoot(ctx, "/music")
	if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: "Amore/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Amore", Artists: []string{"ReoNa"}, AlbumArtists: []string{"ReoNa"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	albums, _ := store.ListAlbums(ctx, storage.Filters{Limit: 10})
	id := albums[0].ID
	candidates := []storage.AlbumSubjectCandidate{{ExternalID: "660542", Title: "Amore", Score: 95, Tieups: []storage.BangumiTieup{{SubjectID: 541285, Title: "Anime", Type: "anime", Role: "op"}}}}
	if err := store.SaveAlbumSubjectCandidates(ctx, id, candidates); err != nil {
		t.Fatal(err)
	}
	stored, err := store.AlbumSubjectCandidates(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/enrichment/albums/" + jsonNumber(id) + "/subjects"
	request := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		return rec
	}
	if rec := request(http.MethodGet, path); rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte("660542")) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if rec := request(http.MethodPost, path+"/"+jsonNumber(stored[0].ID)+"/accept"); rec.Code != http.StatusNoContent {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
	}
	works, err := store.ListWorks(ctx, storage.WorkFilters{})
	if err != nil || len(works) != 1 {
		t.Fatalf("works %+v: %v", works, err)
	}
	if err := store.SaveAlbumSubjectCandidates(ctx, id, []storage.AlbumSubjectCandidate{{ExternalID: "507031", Title: "amore"}}); err != nil {
		t.Fatal(err)
	}
	stored, err = store.AlbumSubjectCandidates(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var rejectedID int64
	for _, v := range stored {
		if v.ExternalID == "507031" {
			rejectedID = v.ID
		}
	}
	if rec := request(http.MethodPost, path+"/"+jsonNumber(rejectedID)+"/reject"); rec.Code != http.StatusNoContent {
		t.Fatalf("reject: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRVL1AlbumCandidateHTTPStatusAndIDValidation(t *testing.T) {
	app, store, token := setupTestApp(t)
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Music", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, _ := store.LibraryByRoot(ctx, "/music")
	if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: "A/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	albums, _ := store.ListAlbums(ctx, storage.Filters{Limit: 1})
	albumID := albums[0].ID
	if err := store.SaveAlbumSubjectCandidates(ctx, albumID, []storage.AlbumSubjectCandidate{{ExternalID: "1", Title: "Music", Tieups: []storage.BangumiTieup{{SubjectID: 2, Title: "Work", Type: "invalid", Role: "op"}}}}); err != nil {
		t.Fatal(err)
	}
	candidates, _ := store.AlbumSubjectCandidates(ctx, albumID)
	request := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		return rec
	}
	base := "/api/v1/enrichment/albums/" + jsonNumber(albumID) + "/subjects/" + jsonNumber(candidates[0].ID)
	if rec := request(http.MethodPost, base+"/accept"); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid work=%d %s", rec.Code, rec.Body.String())
	}
	if rec := request(http.MethodGet, "/api/v1/enrichment/albums/nope/subjects"); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid album id=%d", rec.Code)
	}
	if rec := request(http.MethodGet, "/api/v1/enrichment/albums/999999/subjects"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing album=%d", rec.Code)
	}
	if rec := request(http.MethodPost, base+"/reject"); rec.Code != http.StatusNoContent {
		t.Fatalf("first reject=%d %s", rec.Code, rec.Body.String())
	}
	if rec := request(http.MethodPost, base+"/reject"); rec.Code != http.StatusConflict {
		t.Fatalf("repeat reject=%d %s", rec.Code, rec.Body.String())
	}
}

func TestRVL2PendingAlbumReviewLimitedAndPayloadTrimmed(t *testing.T) {
	app, store, _ := setupTestApp(t)
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Music", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, _ := store.LibraryByRoot(ctx, "/music")
	for i := 0; i < 205; i++ {
		title := fmt.Sprintf("Album %03d", i)
		if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: fmt.Sprintf("%03d/01.flac", i), FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: title, Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	albums, _ := store.ListAlbums(ctx, storage.Filters{Limit: 500})
	for _, album := range albums {
		if err := store.SaveAlbumSubjectCandidates(ctx, album.ID, []storage.AlbumSubjectCandidate{{ExternalID: jsonNumber(album.ID), Title: "Music", Payload: json.RawMessage(`{"large":"payload"}`)}}); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := store.PendingAlbumSubjectCandidates(ctx, 200)
	if err != nil || len(pending) != 200 {
		t.Fatalf("pending=%d err=%v", len(pending), err)
	}
	for _, candidate := range pending {
		if string(candidate.Payload) != "{}" || candidate.Status != "candidate" {
			t.Fatalf("candidate=%+v", candidate)
		}
	}
	_ = app
}

func TestAlbumBangumiAdminDecisionRequiresCSRF(t *testing.T) {
	app, _, _ := setupTestApp(t)
	login := httptest.NewRecorder()
	if _, err := app.sessions.create(login, "admin"); err != nil {
		t.Fatal(err)
	}
	cookie := login.Result().Cookies()[0]
	for _, suffix := range []string{"accept", "reject"} {
		path := "/admin/enrichment/albums/1/subjects/1/" + suffix
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s missing CSRF %d", suffix, rec.Code)
		}
		req = httptest.NewRequest(http.MethodPost, path, strings.NewReader(url.Values{"csrfToken": {csrfOf(t, app, cookie)}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		rec = httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("%s with CSRF %d", suffix, rec.Code)
		}
	}
}

// F4: admin decisions exercise the actual forms, CSRF guard, PRG and persisted state.
func TestF4AdminAlbumBangumiAcceptRejectFormsAndList(t *testing.T) {
	app, store, _ := setupTestApp(t)
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Music", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, _ := store.LibraryByRoot(ctx, "/music")
	importAlbum := func(title string) int64 {
		t.Helper()
		if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: title + "/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: title, Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
			t.Fatal(err)
		}
		albums, err := store.ListAlbums(ctx, storage.Filters{Query: title, Limit: 10})
		if err != nil || len(albums) != 1 {
			t.Fatalf("albums=%+v %v", albums, err)
		}
		return albums[0].ID
	}
	acceptedAlbum, rejectedAlbum := importAlbum("Accept Music"), importAlbum("Reject Music")
	if err := store.SaveAlbumSubjectCandidates(ctx, acceptedAlbum, []storage.AlbumSubjectCandidate{{ExternalID: "660542", Title: "Amore", Score: 95, Tieups: []storage.BangumiTieup{{SubjectID: 541285, Title: "Anime", Type: "anime", Role: "op"}}}, {ExternalID: "507031", Title: "amore", Score: 40}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAlbumSubjectCandidates(ctx, rejectedAlbum, []storage.AlbumSubjectCandidate{{ExternalID: "328609", Title: "Other"}}); err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	if _, err := app.sessions.create(login, "admin"); err != nil {
		t.Fatal(err)
	}
	cookie := login.Result().Cookies()[0]
	form := func(path string, csrf bool) *httptest.ResponseRecorder {
		t.Helper()
		values := url.Values{}
		if csrf {
			values.Set("csrfToken", csrfOf(t, app, cookie))
		}
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		return rec
	}
	items, _ := store.AlbumSubjectCandidates(ctx, acceptedAlbum)
	var acceptID int64
	for _, item := range items {
		if item.ExternalID == "660542" {
			acceptID = item.ID
		}
	}
	acceptPath := "/admin/enrichment/albums/" + jsonNumber(acceptedAlbum) + "/subjects/" + jsonNumber(acceptID) + "/accept"
	if rec := form(acceptPath, false); rec.Code != http.StatusForbidden {
		t.Fatalf("no csrf %d", rec.Code)
	}
	if rec := form(acceptPath, true); rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "/admin/enrichment") {
		t.Fatalf("accept %d %s", rec.Code, rec.Header().Get("Location"))
	}
	items, _ = store.AlbumSubjectCandidates(ctx, acceptedAlbum)
	for _, item := range items {
		expected := "rejected"
		if item.ExternalID == "660542" {
			expected = "confirmed"
		}
		if item.Status != expected {
			t.Fatalf("item %+v", item)
		}
	}
	works, err := store.ListWorks(ctx, storage.WorkFilters{})
	if err != nil || len(works) != 1 {
		t.Fatalf("works %+v: %v", works, err)
	}
	links, err := store.AlbumsForWork(ctx, works[0].ID)
	if err != nil || len(links) != 1 || links[0].Source != "bangumi" {
		t.Fatalf("links %+v: %v", links, err)
	}
	pending, _ := store.AlbumSubjectCandidates(ctx, rejectedAlbum)
	rejectPath := "/admin/enrichment/albums/" + jsonNumber(rejectedAlbum) + "/subjects/" + jsonNumber(pending[0].ID) + "/reject"
	if rec := form(rejectPath, false); rec.Code != http.StatusForbidden {
		t.Fatalf("no csrf reject %d", rec.Code)
	}
	if rec := form(rejectPath, true); rec.Code != http.StatusSeeOther {
		t.Fatalf("reject %d", rec.Code)
	}
	pending, _ = store.AlbumSubjectCandidates(ctx, rejectedAlbum)
	if pending[0].Status != "rejected" {
		t.Fatalf("status %+v", pending[0])
	}
	targets, err := store.AlbumsForBangumiTieup(ctx, true, rejectedAlbum)
	if err != nil || len(targets) != 0 {
		t.Fatalf("suppression targets %+v err %v", targets, err)
	}
	// Review page displays candidates from a third, still-pending album.
	reviewAlbum := importAlbum("Review Music")
	if err := store.SaveAlbumSubjectCandidates(ctx, reviewAlbum, []storage.AlbumSubjectCandidate{{ExternalID: "198229", Title: "人間開花", Score: 72}}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/enrichment", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte("/subject/198229")) || !bytes.Contains(rec.Body.Bytes(), []byte("Review Music")) {
		t.Fatalf("review page %d %s", rec.Code, rec.Body.String())
	}
}

func TestRVL3F4AdminAlbumBangumiSearchButtonAndRunningNotice(t *testing.T) {
	app, store, _ := setupTestApp(t)
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Music", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, _ := store.LibraryByRoot(ctx, "/music")
	for _, name := range []string{"Empty", "Linked", "TVアニメ『Auto Work』 Original Soundtrack"} {
		if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: name + "/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: name, Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	albums, err := store.ListAlbums(ctx, storage.Filters{Limit: 10})
	if err != nil || len(albums) != 3 {
		t.Fatalf("albums %+v: %v", albums, err)
	}
	var empty, linked, auto int64
	for _, album := range albums {
		switch album.Title {
		case "Empty":
			empty = album.ID
		case "Linked":
			linked = album.ID
		default:
			auto = album.ID
		}
	}
	work, err := store.CreateWork(ctx, storage.WorkInput{Title: "Existing", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.AddWorkAlbum(ctx, work.ID, linked, "op"); err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	if _, err = app.sessions.create(login, "admin"); err != nil {
		t.Fatal(err)
	}
	cookie := login.Result().Cookies()[0]
	page := func(id int64) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/admin/albums/"+jsonNumber(id), nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		return rec
	}
	if rec := page(empty); rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte("从 Bangumi 查找")) {
		t.Fatalf("empty %d %s", rec.Code, rec.Body.String())
	}
	if rec := page(linked); rec.Code != http.StatusOK || bytes.Contains(rec.Body.Bytes(), []byte("从 Bangumi 查找")) {
		t.Fatalf("manual linked %d %s", rec.Code, rec.Body.String())
	}
	if _, err = store.RefreshAlbumWorks(ctx, true); err != nil {
		t.Fatal(err)
	}
	if rec := page(auto); rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte("从 Bangumi 查找")) {
		t.Fatalf("auto linked %d %s", rec.Code, rec.Body.String())
	}
	if rec := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/admin/albums/"+jsonNumber(empty), nil)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		return rec
	}(); rec.Code != http.StatusSeeOther || bytes.Contains(rec.Body.Bytes(), []byte("从 Bangumi 查找")) {
		t.Fatalf("anonymous %d %s", rec.Code, rec.Body.String())
	}
	var calls int
	app.startEnrichment = func(ctx context.Context, req enrichmentRunRequest) (storage.EnrichmentRun, error) {
		calls++
		if req.Scope != "album" || req.TargetID != empty {
			t.Errorf("request %+v", req)
		}
		if calls == 2 {
			return storage.EnrichmentRun{}, errors.New("metadata enrichment is already running")
		}
		return store.CreateEnrichmentRun(ctx, req.Scope, req.TargetID, false, 0)
	}
	path := "/admin/enrichment/albums/" + jsonNumber(empty) + "/subjects/search"
	submit := func() *httptest.ResponseRecorder {
		values := url.Values{"csrfToken": {csrfOf(t, app, cookie)}}
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		return rec
	}
	if rec := submit(); rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "/admin/albums/") {
		t.Fatalf("start %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec := submit(); rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "%E4%BB%BB%E5%8A%A1") {
		t.Fatalf("running notice %d %s", rec.Code, rec.Header().Get("Location"))
	}
}
