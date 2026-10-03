package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/storage"
)

func TestArtistRunEndpointsSafeAndProtected(t *testing.T) {
	_, s, handler, _, cookie, csrf := rateLimitTestApp(t)
	ctx := context.Background()
	artists, _ := s.ArtistsForMatching(ctx)
	run, err := s.CreateDurableArtistRun(ctx, []storage.ArtistRunItemInput{{Artist: artists[0], Sources: []storage.ArtistRunSource{{Source: "lastfm", HasAPIKey: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	path := "/admin/matches/runs/" + strconv.FormatInt(run, 10)
	for _, endpoint := range []string{"/admin/matches/runs", "/admin/matches/runs/active.json", path} {
		req := httptest.NewRequest("GET", endpoint, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		body := rec.Body.String()
		if rec.Code != 200 || strings.Contains(body, "snapshot") || strings.Contains(body, "HasAPIKey") || strings.Contains(body, "input_json") {
			t.Fatal(rec.Code, body)
		}
	}
	req := httptest.NewRequest("GET", "/admin/matches/runs?limit=201", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatal(rec.Code)
	}
	req = httptest.NewRequest("POST", path+"/pause", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatal(rec.Code)
	}
	rec = postAdminForm(t, handler, cookie, csrf, path+"/pause", nil)
	if rec.Code != 303 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	rec = postAdminForm(t, handler, cookie, csrf, path+"/cancel", nil)
	if rec.Code != 303 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	rec = postAdminForm(t, handler, cookie, csrf, path+"/resume", nil)
	if rec.Code != 409 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, path, nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code == 200 {
		t.Fatal("anonymous run access")
	}
}

func TestArtistRunHTTPStateErrorsAndNoAdminSession(t *testing.T) {
	app, s, handler, _, cookie, csrf := rateLimitTestApp(t)
	ctx := context.Background()
	artists, _ := s.ArtistsForMatching(ctx)
	run, err := s.CreateDurableArtistRun(ctx, []storage.ArtistRunItemInput{{Artist: artists[0]}})
	if err != nil {
		t.Fatal(err)
	}
	path := "/admin/matches/runs/" + strconv.FormatInt(run, 10)
	for _, endpoint := range []string{path, "/admin/matches/runs/active.json", path + "/pause", path + "/resume", path + "/cancel"} {
		method := http.MethodGet
		if strings.HasSuffix(endpoint, "pause") || strings.HasSuffix(endpoint, "resume") || strings.HasSuffix(endpoint, "cancel") {
			method = http.MethodPost
		}
		req := httptest.NewRequest(method, endpoint, nil)
		req.Header.Set("Authorization", "Bearer not-an-admin-session")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 303 || rec.Header().Get("Location") != "/admin/login" || strings.Contains(rec.Body.String(), `"processed"`) {
			t.Fatal(endpoint, rec.Code, rec.Body.String())
		}
	}
	r, _ := s.DurableArtistRun(ctx, run)
	if r.Status != "running" {
		t.Fatal("unauthorized side effect", r)
	}
	rec := postAdminForm(t, handler, cookie, csrf, path+"/pause", nil)
	if rec.Code != 303 {
		t.Fatal(rec.Code)
	}
	rec = postAdminForm(t, handler, cookie, csrf, path+"/pause", nil)
	if rec.Code != 409 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	rec = postAdminForm(t, handler, cookie, csrf, "/admin/matches/run", nil)
	notice := notice303Of(t, rec)
	if !strings.Contains(notice, "暂停任务") || strings.Contains(notice, "conflict") {
		t.Fatal(notice)
	}
	rec = postAdminForm(t, handler, cookie, csrf, "/admin/matches/runs/999999/cancel", nil)
	notice = notice303Of(t, rec)
	if !strings.Contains(notice, "不存在") {
		t.Fatal(notice)
	}
	app.enrichment = nil
	for _, action := range []string{"pause", "resume"} {
		rec = postAdminForm(t, handler, cookie, csrf, path+"/"+action, nil)
		if rec.Code != 503 || strings.Contains(rec.Body.String(), "SQL") {
			t.Fatal(action, rec.Code, rec.Body.String())
		}
	}
}
