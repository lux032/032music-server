package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestLoginNextValidation(t *testing.T) {
	for raw, want := range map[string]string{
		"/admin/albums/12":                "/admin/albums/12",
		"/admin/favorites?kind=tracks":    "/admin/favorites?kind=tracks",
		"/admin/albums/12?notice=已保存":     "/admin/albums/12",
		"/admin":                          "",
		"/admin/login":                    "",
		"/admin/login?next=/admin/albums": "",
		"/admin/logout":                   "",
		"https://evil.example/admin":      "",
		"//evil.example/admin":            "",
		"/api/v1/albums":                  "",
		"":                                "",
	} {
		if got := loginNext(raw); got != want {
			t.Errorf("loginNext(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestLoginReturnsToRequestedPage(t *testing.T) {
	_, _, handler := credentialTestApp(t)

	// A page request without a session carries its URL to the login page.
	req := httptest.NewRequest(http.MethodGet, "/admin/favorites?kind=tracks", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	location := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || location != "/admin/login?next=%2Fadmin%2Ffavorites%3Fkind%3Dtracks" {
		t.Fatalf("GET page without session = %d %q", rec.Code, location)
	}

	// JSON polls and posts keep the plain login redirect.
	poll := httptest.NewRequest(http.MethodGet, "/admin/matches/runs/active.json", nil)
	poll.Header.Set("Accept", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, poll)
	if got := rec.Header().Get("Location"); got != "/admin/login" {
		t.Fatalf("JSON poll login redirect = %q, want /admin/login", got)
	}

	// The login form keeps the target as a hidden field.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, location, nil))
	if body := rec.Body.String(); !strings.Contains(body, `name="next" value="/admin/favorites?kind=tracks"`) {
		t.Fatalf("login page does not carry next: %s", body)
	}

	// Wrong password re-renders with the target; success lands on it.
	for password, wantCode := range map[string]int{"wrong-password": http.StatusUnauthorized, testAdminPassword: http.StatusSeeOther} {
		form := url.Values{"username": {"admin"}, "password": {password}, "next": {"/admin/favorites?kind=tracks"}}
		post := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
		post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, post)
		if rec.Code != wantCode {
			t.Fatalf("login with %q = %d, want %d", password, rec.Code, wantCode)
		}
		if wantCode == http.StatusSeeOther {
			if got := rec.Header().Get("Location"); got != "/admin/favorites?kind=tracks" {
				t.Fatalf("login success Location = %q", got)
			}
		} else if !strings.Contains(rec.Body.String(), `name="next" value="/admin/favorites?kind=tracks"`) {
			t.Fatal("failed login dropped next")
		}
	}

	// An unsafe target falls back to the dashboard.
	form := url.Values{"username": {"admin"}, "password": {testAdminPassword}, "next": {"https://evil.example/"}}
	post := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, post)
	if got := rec.Header().Get("Location"); got != "/admin" {
		t.Fatalf("unsafe next Location = %q, want /admin", got)
	}
}
