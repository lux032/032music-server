package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/storage"
)

func TestWorkIdentityReviewRejectSnapshot(t *testing.T) {
	app, store, _ := setupTestApp(t)
	ctx := context.Background()
	work, err := store.CreateWork(ctx, storage.WorkInput{Title: "Review target", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.CreateWork(ctx, storage.WorkInput{Title: "Other target", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.AddWorkMatchCandidate(ctx, work.ID, storage.WorkMatchCandidate{Source: "bangumi", ExternalID: "1001", Title: "First", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	later, err := store.AddWorkMatchCandidate(ctx, work.ID, storage.WorkMatchCandidate{Source: "bangumi", ExternalID: "1002", Title: "Later", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := store.AddWorkMatchCandidate(ctx, other.ID, storage.WorkMatchCandidate{Source: "bangumi", ExternalID: "1003", Title: "Foreign", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	cookie := adminCookie(t, app)
	path := "/admin/enrichment/works/" + strconv.FormatInt(work.ID, 10) + "/candidates/reject-all"
	post := func(csrf string) *httptest.ResponseRecorder {
		form := url.Values{"csrfToken": {csrf}, "candidateIds": {strconv.FormatInt(first, 10), strconv.FormatInt(foreign, 10)}, "returnTo": {"https://evil.example/"}}
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		return rec
	}
	if rec := post("wrong"); rec.Code != http.StatusForbidden {
		t.Fatalf("csrf: %d", rec.Code)
	}
	rec := post(csrfOf(t, app, cookie))
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/admin/work-review?") {
		t.Fatalf("redirect: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	candidates, err := store.WorkMatchCandidates(ctx, work.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range candidates {
		if c.ID == first && c.Status != "rejected" || c.ID == later && c.Status != "candidate" {
			t.Fatalf("snapshot changed: %+v", c)
		}
	}
	candidates, err = store.WorkMatchCandidates(ctx, other.ID)
	if err != nil || candidates[0].Status != "candidate" {
		t.Fatalf("foreign candidate changed: %+v %v", candidates, err)
	}
	// Ordinary regeneration retains the rejection.
	if err = store.ReplaceWorkMatchCandidates(ctx, work.ID, []storage.WorkMatchCandidate{{Source: "bangumi", ExternalID: "1001", Title: "First", Type: "anime"}}); err != nil {
		t.Fatal(err)
	}
	candidates, _ = store.WorkMatchCandidates(ctx, work.ID)
	if len(candidates) != 1 || candidates[0].Status != "rejected" {
		t.Fatalf("rejection lost: %+v", candidates)
	}
}

func TestWorkIdentityReviewOccupiedCandidate(t *testing.T) {
	app, store, _ := setupTestApp(t)
	ctx := context.Background()
	owner, err := store.CreateWork(ctx, storage.WorkInput{Title: "Existing animation", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := store.CreateWork(ctx, storage.WorkInput{Title: "Piano Volume 2", Type: "other"})
	if err != nil {
		t.Fatal(err)
	}
	c := storage.WorkMatchCandidate{Source: "bangumi", ExternalID: "515759", Title: "Animation season 2", Type: "anime", Score: 40, Evidence: []string{"中文标题精确匹配"}}
	if err = store.ManualBindWorkBangumi(ctx, owner.ID, c); err != nil {
		t.Fatal(err)
	}
	id, err := store.AddWorkMatchCandidate(ctx, target.ID, c)
	if err != nil {
		t.Fatal(err)
	}
	cookie := adminCookie(t, app)
	req := httptest.NewRequest(http.MethodGet, "/admin/work-review?tab=works", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	for _, text := range []string{"以上都不是", "中文标题精确匹配", "Existing animation", "Volume 2 不等于动画第二季", "匹配分 40"} {
		if !strings.Contains(body, text) {
			t.Fatalf("missing %q: %d %s", text, rec.Code, body)
		}
	}
	if strings.Contains(body, "/candidates/"+strconv.FormatInt(id, 10)+"/accept") {
		t.Fatal("occupied candidate can be accepted")
	}
}
