package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func httpCount(t *testing.T, store *storage.Store, query string, args ...any) int {
	t.Helper()
	db, err := sql.Open("sqlite", httpTestDBPath[store]+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err = db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func importTrackSubject(t *testing.T, store *storage.Store, title string) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Music", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, _ := store.LibraryByRoot(ctx, "/music")
	if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: title + "/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: title, Album: title + " Album", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	tracks, err := store.ListTracks(ctx, storage.Filters{Query: title, Limit: 10})
	if err != nil || len(tracks) != 1 {
		t.Fatalf("tracks=%+v err=%v", tracks, err)
	}
	candidate := storage.TrackSubjectCandidate{ExternalID: "305621", Title: title, Artist: "Singer", MatchKind: "exact", Tieups: []storage.BangumiTieup{{SubjectID: 100, Title: "Work", Type: "anime", Role: "op"}, {SubjectID: 200, Title: "Other Work", Type: "anime", Role: "ed"}}}
	if err = store.SaveTrackSubjectCandidates(ctx, tracks[0].ID, []storage.TrackSubjectCandidate{candidate}); err != nil {
		t.Fatal(err)
	}
	stored, err := store.TrackSubjectCandidates(ctx, tracks[0].ID)
	if err != nil || len(stored) != 1 {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	return tracks[0].ID, stored[0].ID
}

func TestTrackSubjectAPIRejectsAnonymousAndNonAdmin(t *testing.T) {
	app, store, _ := setupTestApp(t)
	trackID, candidateID := importTrackSubject(t, store, "Opening")
	path := "/api/v1/enrichment/tracks/" + jsonNumber(trackID) + "/subjects"
	for _, method := range []struct{ method, path string }{
		{http.MethodGet, path},
		{http.MethodPost, path + "/" + jsonNumber(candidateID) + "/accept"},
		{http.MethodPost, path + "/" + jsonNumber(candidateID) + "/reject"},
	} {
		req := httptest.NewRequest(method.method, method.path, nil)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s anonymous=%d", method.method, method.path, rec.Code)
		}
	}
}

func TestTrackSubjectAPIAcceptRoleOverrideAndReject(t *testing.T) {
	app, store, token := setupTestApp(t)
	ctx := context.Background()
	acceptTrack, acceptCandidate := importTrackSubject(t, store, "Accept Song")
	body := strings.NewReader(`{"workSubjectIds":[100,200],"roles":{"200":"insert"}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/enrichment/tracks/"+jsonNumber(acceptTrack)+"/subjects/"+jsonNumber(acceptCandidate)+"/accept", body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("accept=%d %s", rec.Code, rec.Body.String())
	}
	works, err := store.ListWorks(ctx, storage.WorkFilters{})
	if err != nil || len(works) != 2 {
		t.Fatalf("works=%+v err=%v", works, err)
	}
	sources := map[string]string{}
	for _, work := range works {
		linked, err := store.TracksForWork(ctx, work.ID)
		if err != nil || len(linked) != 1 {
			t.Fatalf("linked=%+v err=%v", linked, err)
		}
		sources[linked[0].Role] = linked[0].Source
	}
	if sources["op"] != "bangumi" || sources["insert"] != "manual" {
		t.Fatalf("sources=%v", sources)
	}

	rejectTrack, rejectCandidate := importTrackSubject(t, store, "Reject Song")
	req = httptest.NewRequest(http.MethodPost, "/api/v1/enrichment/tracks/"+jsonNumber(rejectTrack)+"/subjects/"+jsonNumber(rejectCandidate)+"/reject", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("reject=%d %s", rec.Code, rec.Body.String())
	}
	n := httpCount(t, store, `SELECT COUNT(*) FROM track_work_suppressions WHERE track_id=? AND inferred_key=?`, rejectTrack, "bangumi:305621")
	if n != 1 {
		t.Fatalf("suppressions=%d", n)
	}
}

func TestTrackSubjectAdminFormCSRFAndPage(t *testing.T) {
	app, store, _ := setupTestApp(t)
	trackID, candidateID := importTrackSubject(t, store, "Review Song")
	login := httptest.NewRecorder()
	if _, err := app.sessions.create(login, "admin"); err != nil {
		t.Fatal(err)
	}
	cookie := login.Result().Cookies()[0]
	path := "/admin/enrichment/tracks/" + jsonNumber(trackID) + "/subjects/" + jsonNumber(candidateID) + "/reject"
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing csrf=%d", rec.Code)
	}
	page := httptest.NewRequest(http.MethodGet, "/admin/enrichment", nil)
	page.AddCookie(cookie)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, page)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte("曲目候选")) || !bytes.Contains(rec.Body.Bytes(), []byte("Review Song")) || !bytes.Contains(rec.Body.Bytes(), []byte("/subject/305621")) {
		t.Fatalf("page=%d %s", rec.Code, rec.Body.String())
	}
	values := url.Values{"csrfToken": {csrfOf(t, app, cookie)}}
	req = httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("reject form=%d", rec.Code)
	}
}

func TestTrackSubjectDecisionValidation(t *testing.T) {
	app, store, token := setupTestApp(t)
	trackID, candidateID := importTrackSubject(t, store, "Validate Song")
	otherTrack, _ := importTrackSubject(t, store, "Other Song")
	base := "/api/v1/enrichment/tracks/"
	auth := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		return rec
	}
	if rec := auth(base+jsonNumber(otherTrack)+"/subjects/"+jsonNumber(candidateID)+"/accept", `{"workSubjectIds":[100]}`); rec.Code != http.StatusNotFound {
		t.Fatalf("mismatched candidate=%d %s", rec.Code, rec.Body.String())
	}
	if got := auth(base+jsonNumber(trackID)+"/subjects/"+jsonNumber(candidateID)+"/accept", `{"workSubjectIds":[100],"roles":{"100":"not-a-role"}}`); got.Code != http.StatusBadRequest {
		t.Fatalf("bad role=%d %s", got.Code, got.Body.String())
	}
	if got := auth(base+jsonNumber(trackID)+"/subjects/"+jsonNumber(candidateID)+"/accept", `{"workSubjectIds":[999]}`); got.Code != http.StatusBadRequest {
		t.Fatalf("foreign work=%d %s", got.Code, got.Body.String())
	}
	// A foreign id mixed into an otherwise valid selection is still a 400, and
	// the valid part must not be written either.
	if got := auth(base+jsonNumber(trackID)+"/subjects/"+jsonNumber(candidateID)+"/accept", `{"workSubjectIds":[100,999]}`); got.Code != http.StatusBadRequest {
		t.Fatalf("mixed selection=%d %s", got.Code, got.Body.String())
	}
	if n := httpCount(t, store, `SELECT COUNT(*) FROM work_tracks WHERE track_id=?`, trackID); n != 0 {
		t.Fatalf("mixed selection wrote %d links", n)
	}
	if n := httpCount(t, store, `SELECT COUNT(*) FROM track_subject_candidates WHERE id=? AND status='candidate'`, candidateID); n != 1 {
		t.Fatalf("candidate no longer pending=%d", n)
	}
	if got := auth(base+jsonNumber(trackID)+"/subjects/"+jsonNumber(candidateID)+"/accept", `{"workSubjectIds":[]}`); got.Code != http.StatusBadRequest {
		t.Fatalf("empty selection=%d %s", got.Code, got.Body.String())
	}
	login := httptest.NewRecorder()
	if _, err := app.sessions.create(login, "admin"); err != nil {
		t.Fatal(err)
	}
	cookie := login.Result().Cookies()[0]
	form := url.Values{"csrfToken": {csrfOf(t, app, cookie)}}
	req := httptest.NewRequest(http.MethodPost, "/admin/enrichment/tracks/"+jsonNumber(trackID)+"/subjects/"+jsonNumber(candidateID)+"/accept", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "%E8%AF%B7%E8%87%B3%E5%B0%91%E9%80%89%E6%8B%A9%E4%B8%80%E9%83%A8%E4%BD%9C%E5%93%81") && !strings.Contains(rec.Header().Get("Location"), "请至少选择一部作品") {
		t.Fatalf("empty form=%d %s", rec.Code, rec.Header().Get("Location"))
	}
}
