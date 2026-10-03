package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// The durable enrichment run endpoints must be admin-session only, CSRF
// guarded for POST, and expose a safe DTO (no raw bodies, keys, or SQL).
func TestEnrichmentRunEndpointsSafeAndProtected(t *testing.T) {
	_, s, handler, manager, cookie, csrf := rateLimitTestApp(t)
	ctx := context.Background()
	run, err := s.CreateDurableEnrichmentRun(ctx, "tracks", 0, false, []string{"tracks"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/admin/enrichment/runs/" + strconv.FormatInt(run.ID, 10)
	for _, endpoint := range []string{"/admin/enrichment/runs", "/admin/enrichment/runs/active.json", path} {
		req := httptest.NewRequest(http.MethodGet, endpoint, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		body := rec.Body.String()
		if rec.Code != 200 || strings.Contains(body, "api_key") || strings.Contains(body, "parameters_json") || strings.Contains(body, "SQL") {
			t.Fatal(endpoint, rec.Code, body)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/enrichment/runs?limit=201", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatal(rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/admin/enrichment/runs?offset=-1", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatal(rec.Code)
	}
	// POST without CSRF is rejected before touching the run.
	req = httptest.NewRequest(http.MethodPost, path+"/pause", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatal(rec.Code)
	}
	if r, _ := s.DurableEnrichmentRun(ctx, run.ID); r.Status != "running" {
		t.Fatal("CSRF-less pause mutated the run", r.Status)
	}
	rec = postAdminForm(t, handler, cookie, csrf, path+"/pause", nil)
	if rec.Code != 303 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if r, _ := s.DurableEnrichmentRun(ctx, run.ID); r.Status != "paused" {
		t.Fatal(r.Status)
	}
	// Pausing a paused run is a state conflict, not an error page.
	rec = postAdminForm(t, handler, cookie, csrf, path+"/pause", nil)
	if rec.Code != 409 || strings.Contains(rec.Body.String(), "SQL") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	// Resume relaunches the worker; with no eligible items it completes.
	rec = postAdminForm(t, handler, cookie, csrf, path+"/resume", nil)
	if rec.Code != 303 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	manager.Wait()
	if r, _ := s.DurableEnrichmentRun(ctx, run.ID); r.Status != "completed" {
		t.Fatal("resumed run did not complete", r.Status)
	}
	// A missing run is 404 on detail and on control actions.
	req = httptest.NewRequest(http.MethodGet, "/admin/enrichment/runs/999999", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	rec = postAdminForm(t, handler, cookie, csrf, "/admin/enrichment/runs/999999/pause", nil)
	if rec.Code != 404 {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

// Without an admin session every enrichment run endpoint redirects to login
// and leaks no run data; a nil enrichment service reports 503 in Chinese.
func TestEnrichmentRunHTTPNoSessionAndNilService(t *testing.T) {
	app, s, handler, _, cookie, csrf := rateLimitTestApp(t)
	ctx := context.Background()
	run, err := s.CreateDurableEnrichmentRun(ctx, "tracks", 0, false, []string{"tracks"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/admin/enrichment/runs/" + strconv.FormatInt(run.ID, 10)
	for _, endpoint := range []string{path, "/admin/enrichment/runs", "/admin/enrichment/runs/active.json", path + "/pause", path + "/resume"} {
		method := http.MethodGet
		if strings.HasSuffix(endpoint, "pause") || strings.HasSuffix(endpoint, "resume") {
			method = http.MethodPost
		}
		req := httptest.NewRequest(method, endpoint, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 303 || rec.Header().Get("Location") != "/admin/login" || strings.Contains(rec.Body.String(), `"processed"`) {
			t.Fatal(endpoint, rec.Code, rec.Body.String())
		}
	}
	if r, _ := s.DurableEnrichmentRun(ctx, run.ID); r.Status != "running" {
		t.Fatal("anonymous request mutated the run", r.Status)
	}
	app.enrichment = nil
	for _, action := range []string{"pause", "resume"} {
		rec := postAdminForm(t, handler, cookie, csrf, path+"/"+action, nil)
		if rec.Code != 503 || strings.Contains(rec.Body.String(), "SQL") {
			t.Fatal(action, rec.Code, rec.Body.String())
		}
	}
}

// The old start/cancel flow keeps safe fixed Chinese notices for conflicts.
func TestEnrichmentStartConflictAndCancelNoticesSafe(t *testing.T) {
	_, s, handler, _, cookie, csrf := rateLimitTestApp(t)
	ctx := context.Background()
	run, err := s.CreateDurableEnrichmentRun(ctx, "tracks", 0, false, []string{"tracks"})
	if err != nil {
		t.Fatal(err)
	}
	// A second start while one is active: distinct paused/active notice.
	rec := postAdminForm(t, handler, cookie, csrf, "/admin/enrichment/run", map[string][]string{"scope": {"all"}})
	notice := notice303Of(t, rec)
	if !strings.Contains(notice, "恢复或取消") || strings.Contains(notice, "conflict") || strings.Contains(notice, "SQL") {
		t.Fatal(notice)
	}
	// Missing target for an album/work scope: distinct validation notice.
	rec = postAdminForm(t, handler, cookie, csrf, "/admin/enrichment/run", map[string][]string{"scope": {"album"}})
	if notice = notice303Of(t, rec); !strings.Contains(notice, "启动参数无效") {
		t.Fatal(notice)
	}
	// Cancelling a finished run keeps the safe fixed notice.
	if err = s.TransitionEnrichmentRun(ctx, run.ID, "cancel", "manual"); err != nil {
		t.Fatal(err)
	}
	rec = postAdminForm(t, handler, cookie, csrf, "/admin/enrichment/runs/"+strconv.FormatInt(run.ID, 10)+"/cancel", nil)
	if notice = notice303Of(t, rec); !strings.Contains(notice, "任务已结束或不存在") {
		t.Fatal(notice)
	}
	rec = postAdminForm(t, handler, cookie, csrf, "/admin/enrichment/runs/999999/cancel", nil)
	if notice = notice303Of(t, rec); !strings.Contains(notice, "任务已结束或不存在") {
		t.Fatal(notice)
	}
}
