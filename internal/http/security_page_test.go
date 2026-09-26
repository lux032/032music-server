package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/lux032/032music-server/internal/config"
)

// TestSecurityNoticeCodesOnly: the page renders fixed messages for known
// codes and nothing for unknown codes or free text.
func TestSecurityNoticeCodesOnly(t *testing.T) {
	_, _, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)

	body := getWithCookie(handler, securityPagePath+"?notice=wrong_password", cookie).Body.String()
	if !strings.Contains(body, `data-security-notice>当前密码不正确。</div>`) {
		t.Fatal("known code must render its fixed message")
	}
	if !strings.Contains(body, `class="form-error security-notice" role="alert"`) {
		t.Fatal("error codes must render as an alert")
	}
	body = getWithCookie(handler, securityPagePath+"?notice=api_token_reset", cookie).Body.String()
	if !strings.Contains(body, `class="toast security-notice" role="status"`) {
		t.Fatal("success codes must render as a status toast")
	}
	for _, injected := range []string{"injected-text", url.QueryEscape("当前密码不正确。"), "__proto__"} {
		body = getWithCookie(handler, securityPagePath+"?notice="+injected, cookie).Body.String()
		if strings.Contains(body, "data-security-notice") {
			t.Fatalf("unknown notice %q must not render", injected)
		}
	}
}

// TestSecurityHandlersRedirectWithKnownCodes: every redirect notice is a
// code from securityNotices (securityNoticeText fails otherwise), and the
// value never contains non-ASCII free text.
func TestSecurityHandlersRedirectWithKnownCodes(t *testing.T) {
	app, _, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	for _, tc := range []struct {
		path string
		form url.Values
	}{
		{"/api-token", url.Values{"current_password": {"wrong"}}},
		{"/username", url.Values{"current_password": {testAdminPassword}, "new_username": {""}}},
		{"/password", url.Values{"current_password": {testAdminPassword}, "new_password": {"x"}, "confirm_password": {"x"}}},
		{"/media-token", url.Values{"current_password": {testAdminPassword}, "custom_token": {"short"}}},
		{"/reset/api-token", url.Values{"current_password": {testAdminPassword}}},
		{"/reset/nope", url.Values{"current_password": {testAdminPassword}}},
		{"/media-token/reveal", url.Values{"current_password": {testAdminPassword}}},
	} {
		rec := postSecurity(t, app, handler, cookie, securityPagePath+tc.path, tc.form)
		securityNoticeText(t, rec)
		location := rec.Header().Get("Location")
		for _, r := range location {
			if r > 127 {
				t.Fatalf("%s: redirect %q carries non-ASCII text", tc.path, location)
			}
		}
	}
}

// TestSecurityPageShowsSourcesAndResetControls: env sources show the env
// badge and no reset control; overrides show the admin-page badge and the
// password-protected "恢复为环境变量" disclosure.
func TestSecurityPageShowsSourcesAndResetControls(t *testing.T) {
	app, _, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	body := getWithCookie(handler, securityPagePath, cookie).Body.String()
	if strings.Count(body, `data-source="env"`) != 4 || strings.Contains(body, "/admin/settings/security/reset/") {
		t.Fatal("all-env page must show four env badges and no reset forms")
	}
	for _, field := range []string{`name="current_password" autocomplete="current-password"`, `name="new_password" autocomplete="new-password"`, `name="confirm_password" autocomplete="new-password"`, `name="custom_token" autocomplete="new-password"`, `name="new_username" autocomplete="username"`} {
		if !strings.Contains(body, field) {
			t.Fatalf("missing field %s", field)
		}
	}
	securityNoticeText(t, postSecurity(t, app, handler, cookie, securityPagePath+"/media-token", url.Values{"current_password": {testAdminPassword}, "custom_token": {"page-media-token-0123456789"}}))
	body = getWithCookie(handler, securityPagePath, cookie).Body.String()
	if !strings.Contains(body, `data-source="override"`) || !strings.Contains(body, `action="/admin/settings/security/reset/media-token"`) {
		t.Fatal("override must show the admin-page badge and its reset form")
	}
	if strings.Contains(body, "page-media-token-0123456789") {
		t.Fatal("the media token must not be rendered without a reveal")
	}
}

func TestSecurityPageResetBannerAndMediaNotes(t *testing.T) {
	store := credentialTestStore(t)
	cfg := credentialTestConfig(t)
	cfg.ResetCredentials = config.ResetCredentialsTokens
	cfg.DevMode = true
	cfg.MediaToken = cfg.APIToken
	app := newCredentialTestApp(t, cfg, store)
	handler := app.Handler()
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	body := getWithCookie(handler, securityPagePath, cookie).Body.String()
	for _, want := range []string{`data-security-reset-mode="tokens"`, "MUSIC_SERVER_RESET_CREDENTIALS=tokens", "API Token 与媒体 Token", "从 .env 中删除", "媒体 Token 当前与环境变量中的 API Token 相同", "开发模式：不能为空"} {
		if !strings.Contains(body, want) {
			t.Fatalf("page missing %q", want)
		}
	}

	cfg = credentialTestConfig(t)
	cfg.MediaTokenGenerated = true
	app = newCredentialTestApp(t, cfg, credentialTestStore(t))
	handler = app.Handler()
	cookie = mustLogin(t, handler, "admin", testAdminPassword)
	body = getWithCookie(handler, securityPagePath, cookie).Body.String()
	if !strings.Contains(body, "本次启动随机生成") || strings.Contains(body, "data-security-reset-mode") {
		t.Fatal("ephemeral media token note missing or unexpected reset banner")
	}
	if !strings.Contains(body, `minlength="12"`) {
		t.Fatal("production password field must declare the 12-character rule")
	}
}

// TestLogoutRacingCacheMissDoesNotResurrectSession: a cache-miss lookup
// that already read the persisted row before a concurrent logout must not
// cache (resurrect) the logged-out session.
func TestLogoutRacingCacheMissDoesNotResurrectSession(t *testing.T) {
	store := credentialTestStore(t)
	creator := newSessionManager(false, store)
	rec := httptest.NewRecorder()
	if _, err := creator.create(rec, "admin"); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookieFrom(rec)

	// A second manager on the same database starts with an empty cache,
	// like a process after restart.
	manager := newSessionManager(false, store)
	request := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/admin", nil)
		r.AddCookie(cookie)
		return r
	}
	var once sync.Once
	manager.afterPersistedLookup = func() {
		once.Do(func() { manager.delete(httptest.NewRecorder(), request()) })
	}
	if _, ok := manager.get(request()); ok {
		t.Fatal("lookup racing a logout must not report the session as valid")
	}
	manager.afterPersistedLookup = nil
	manager.mu.Lock()
	_, cached := manager.sessions[cookie.Value]
	manager.mu.Unlock()
	if cached {
		t.Fatal("logged-out session was resurrected into the cache")
	}
	if _, ok := manager.get(request()); ok {
		t.Fatal("logged-out session is still valid")
	}
}

// TestCacheMissRetriesAfterUnrelatedLogout: a logout of another session
// during a cache-miss lookup forces a re-read but does not reject a
// session that is still valid.
func TestCacheMissRetriesAfterUnrelatedLogout(t *testing.T) {
	store := credentialTestStore(t)
	creator := newSessionManager(false, store)
	keepRec, otherRec := httptest.NewRecorder(), httptest.NewRecorder()
	if _, err := creator.create(keepRec, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := creator.create(otherRec, "admin"); err != nil {
		t.Fatal(err)
	}
	keep, other := sessionCookieFrom(keepRec), sessionCookieFrom(otherRec)
	manager := newSessionManager(false, store)
	var once sync.Once
	manager.afterPersistedLookup = func() {
		once.Do(func() {
			r := httptest.NewRequest(http.MethodPost, "/admin/logout", nil)
			r.AddCookie(other)
			manager.delete(httptest.NewRecorder(), r)
		})
	}
	r := httptest.NewRequest(http.MethodGet, "/admin", nil)
	r.AddCookie(keep)
	if _, ok := manager.get(r); !ok {
		t.Fatal("a still-valid session must survive an unrelated concurrent logout")
	}
}

// TestLogoutDeleteFailureKeepsPersistentSession verifies failed DB deletion
// does not report a successful logout or invalidate the in-memory session.
func TestLogoutDeleteFailureKeepsPersistentSession(t *testing.T) {
	store, path := credentialTestStoreAt(t)
	app := newCredentialTestApp(t, credentialTestConfig(t), store)
	handler := app.Handler()
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	db := rawCredentialDB(t, path)
	if _, err := db.Exec(`CREATE TRIGGER fail_session_delete BEFORE DELETE ON admin_sessions BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/logout", strings.NewReader(url.Values{"csrfToken": {csrfOf(t, app, cookie)}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || rec.Header().Get("Location") != "" {
		t.Fatalf("failed delete: status %d redirect %q", rec.Code, rec.Header().Get("Location"))
	}
	if _, ok := app.sessions.get(getRequestWithCookie(cookie)); !ok {
		t.Fatal("failed logout invalidated cached session")
	}
	if _, err := store.AdminSessionByTokenHash(context.Background(), sessionTokenHash(cookie.Value)); err != nil {
		t.Fatalf("persistent session lost: %v", err)
	}
}

func getRequestWithCookie(cookie *http.Cookie) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(cookie)
	return req
}
