package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/storage"
)

func TestWorkFormOriginalTypePreservesConcurrentCorrectionAndLocksChange(t *testing.T) {
	app, cookie, token := csrfSessionApp(t)
	ctx := context.Background()
	work, err := app.store.CreateWork(ctx, storage.WorkInput{Title: "Show", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	if changed, correctionErr := app.store.CorrectBangumiWorkType(ctx, work.ID, "anime", "movie", "5", 0); correctionErr != nil || !changed {
		t.Fatalf("automatic correction changed=%v err=%v", changed, correctionErr)
	}
	post := func(values url.Values) *httptest.ResponseRecorder {
		values.Set("csrfToken", token)
		req := httptest.NewRequest(http.MethodPost, "/admin/works/"+strconv.FormatInt(work.ID, 10), strings.NewReader(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", strconv.FormatInt(work.ID, 10))
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		return rec
	}
	if rec := post(url.Values{"title": {"Renamed"}, "type": {"anime"}, "originalType": {"anime"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("rename status=%d body=%s", rec.Code, rec.Body.String())
	}
	got, _ := app.store.WorkByID(ctx, work.ID)
	if got.Type != "movie" {
		t.Fatalf("stale form overwrote type=%s", got.Type)
	}
	if rec := post(url.Values{"title": {"Renamed"}, "type": {"game"}, "originalType": {"movie"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("type change status=%d body=%s", rec.Code, rec.Body.String())
	}
	got, _ = app.store.WorkByID(ctx, work.ID)
	if got.Type != "game" {
		t.Fatalf("type change not applied=%s", got.Type)
	}
}

func TestAPIWorkTypeChangeUsesCurrentDatabaseType(t *testing.T) {
	app, _, apiToken := csrfSessionAppWithToken(t)
	ctx := context.Background()
	work, err := app.store.CreateWork(ctx, storage.WorkInput{Title: "API", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(storage.WorkInput{Title: "API renamed", Type: "anime"})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/works/"+strconv.FormatInt(work.ID, 10), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", strconv.FormatInt(work.ID, 10))
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("same type status=%d body=%s", rec.Code, rec.Body.String())
	}
	body, _ = json.Marshal(storage.WorkInput{Title: "API renamed", Type: "movie"})
	req = httptest.NewRequest(http.MethodPatch, "/api/v1/works/"+strconv.FormatInt(work.ID, 10), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", strconv.FormatInt(work.ID, 10))
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("changed type status=%d body=%s", rec.Code, rec.Body.String())
	}
	got, _ := app.store.WorkByID(ctx, work.ID)
	if got.Type != "movie" {
		t.Fatalf("API type=%s", got.Type)
	}
}
