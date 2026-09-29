package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/enrichment"
	"github.com/lux032/032music-server/internal/storage"
)

func TestBatch95ManualBangumiLookupUsesFriendlyErrors(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		wantNotice string
	}{
		{"missing", http.StatusNotFound, "Bangumi 上找不到该条目（可能已删除或需要登录才能查看）"},
		{"provider failure", http.StatusInternalServerError, "对齐失败，请稍后重试"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := credentialTestStore(t)
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			manager := enrichment.New(context.Background(), store, logger, t.TempDir())
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "provider-secret", tc.status) }))
			defer server.Close()
			manager.SetBangumiBaseURL(server.URL)
			app, err := NewApp(credentialTestConfig(t), store, nil, manager, logger, "test")
			if err != nil {
				t.Fatal(err)
			}
			work, _ := store.CreateWork(context.Background(), storage.WorkInput{Title: "Target", Type: "anime"})
			cookie := adminCookie(t, app)
			csrf := csrfOf(t, app, cookie)
			form := url.Values{"csrfToken": {csrf}, "bangumiSubject": {"55770"}}
			req := httptest.NewRequest(http.MethodPost, "/admin/works/"+strconv.FormatInt(work.ID, 10)+"/bangumi", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, req)
			location, _ := url.Parse(rec.Header().Get("Location"))
			if rec.Code != http.StatusSeeOther || location.Query().Get("notice") != tc.wantNotice || strings.Contains(location.Query().Get("notice"), "provider-secret") {
				t.Fatalf("status=%d location=%q", rec.Code, rec.Header().Get("Location"))
			}
		})
	}
}

func TestBatch95ManualBangumiConflictRedirectAndClickableLink(t *testing.T) {
	store := credentialTestStore(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := enrichment.New(context.Background(), store, logger, t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 55770, "type": 2, "name": "Occupied", "name_cn": "已占用", "date": "2024-01-01", "platform": "TV"})
	}))
	defer server.Close()
	manager.SetBangumiBaseURL(server.URL)
	app, err := NewApp(credentialTestConfig(t), store, nil, manager, logger, "test")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := store.CreateWork(context.Background(), storage.WorkInput{Title: `Owner <script>alert(1)</script>`, Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	candidateID, err := store.AddWorkMatchCandidate(context.Background(), owner.ID, storage.WorkMatchCandidate{Source: "bangumi", ExternalID: "55770", Title: "Occupied", Type: "anime", Payload: json.RawMessage(`{"id":55770,"type":2}`)})
	if err != nil || store.ConfirmWorkMatchCandidate(context.Background(), owner.ID, candidateID, 0) != nil {
		t.Fatalf("seed owner: candidate=%d err=%v", candidateID, err)
	}
	target, err := store.CreateWork(context.Background(), storage.WorkInput{Title: "Target", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	cookie := adminCookie(t, app)
	csrf := csrfOf(t, app, cookie)
	for _, returnTo := range []string{
		"/admin/works/" + strconv.FormatInt(target.ID, 10),
		"/admin/work-review?tab=works",
	} {
		form := url.Values{"csrfToken": {csrf}, "returnTo": {returnTo}, "bangumiSubject": {"55770"}}
		req := httptest.NewRequest(http.MethodPost, "/admin/works/"+strconv.FormatInt(target.ID, 10)+"/bangumi", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		location, parseErr := url.Parse(rec.Header().Get("Location"))
		if rec.Code != http.StatusSeeOther || parseErr != nil || location.Query().Get("conflictWorkId") != strconv.FormatInt(owner.ID, 10) {
			t.Fatalf("returnTo=%s code=%d location=%q err=%v", returnTo, rec.Code, rec.Header().Get("Location"), parseErr)
		}
		get := httptest.NewRequest(http.MethodGet, location.String(), nil)
		get.AddCookie(cookie)
		page := httptest.NewRecorder()
		app.Handler().ServeHTTP(page, get)
		body := page.Body.String()
		if page.Code != http.StatusOK || !strings.Contains(body, `href="/admin/works/`+strconv.FormatInt(owner.ID, 10)+`"`) || !strings.Contains(body, "该条目已对齐到作品") {
			t.Fatalf("returnTo=%s page=%d body=%s", returnTo, page.Code, body)
		}
		if strings.Contains(body, "<script>alert(1)</script>") {
			t.Fatalf("owner title was not escaped: %s", body)
		}
	}
}
