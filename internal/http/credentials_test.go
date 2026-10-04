package httpapi

import (
	"context"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/config"
	"github.com/lux032/032music-server/internal/storage"
)

const (
	testAdminPassword = "initial-password-123"
	testEnvAPIToken   = "env-api-token-at-least-24-characters"
	testEnvMediaToken = "env-media-token-at-least-24-characters"
)

// credentialTestConfig returns an environment config rooted in its own
// temporary data directory.
func credentialTestConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		AdminUsername: "admin",
		AdminPassword: testAdminPassword,
		APIToken:      testEnvAPIToken,
		MediaToken:    testEnvMediaToken,
		DataDirectory: t.TempDir(),
	}
}

func credentialTestStore(t *testing.T) *storage.Store {
	t.Helper()
	store, path := credentialTestStoreAt(t)
	// 注册原始库路径，需要直写 DB 的用例（如 httpExec）可用。
	httpTestDBPath[store] = path
	t.Cleanup(func() { delete(httpTestDBPath, store) })
	return store
}

// credentialTestStoreAt also returns the database path so tests can open
// a raw connection (fault injection, row counts).
func credentialTestStoreAt(t *testing.T) (*storage.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials.db")
	store, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return store, path
}

func newCredentialTestApp(t *testing.T, cfg config.Config, store *storage.Store) *App {
	t.Helper()
	app, err := NewApp(cfg, store, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	if err != nil {
		t.Fatal(err)
	}
	return app
}

// credentialTestApp builds an isolated App (own database and data dir).
func credentialTestApp(t *testing.T) (*App, *storage.Store, http.Handler) {
	t.Helper()
	store := credentialTestStore(t)
	app := newCredentialTestApp(t, credentialTestConfig(t), store)
	return app, store, app.Handler()
}

func sessionCookieFrom(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == adminSessionCookie && cookie.Value != "" {
			return cookie
		}
	}
	return nil
}

func loginAs(t *testing.T, handler http.Handler, username, password string) (*http.Cookie, int) {
	t.Helper()
	form := url.Values{"username": {username}, "password": {password}}
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return sessionCookieFrom(rec), rec.Code
}

func mustLogin(t *testing.T, handler http.Handler, username, password string) *http.Cookie {
	t.Helper()
	cookie, code := loginAs(t, handler, username, password)
	if code != http.StatusSeeOther || cookie == nil {
		t.Fatalf("login(%q) status = %d, cookie = %v", username, code, cookie)
	}
	return cookie
}

func csrfOf(t *testing.T, app *App, cookie *http.Cookie) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(cookie)
	session, ok := app.sessions.get(req)
	if !ok {
		t.Fatal("session not found")
	}
	return session.CSRFToken
}

func postSecurity(t *testing.T, app *App, handler http.Handler, cookie *http.Cookie, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	if form == nil {
		form = url.Values{}
	}
	if !form.Has("csrfToken") {
		form.Set("csrfToken", csrfOf(t, app, cookie))
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// securityNotice asserts a 303 back to the security page and returns the
// notice text.
func securityNoticeText(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303, body = %s", rec.Code, rec.Body.String())
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil || location.Path != securityPagePath {
		t.Fatalf("Location = %q", rec.Header().Get("Location"))
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	code := location.Query().Get("notice")
	notice, ok := securityNotices[code]
	if !ok {
		t.Fatalf("unknown security notice code %q", code)
	}
	return notice.Text
}

func getWithCookie(handler http.Handler, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func apiStatus(handler http.Handler, token string) int {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/playlists", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code
}

// mediaAuthorized reports whether the media token passed authentication
// (the artwork itself does not exist, so success is a 404).
func mediaAuthorized(handler http.Handler, token string) bool {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/artwork/999999?mediaToken="+url.QueryEscape(token), nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code != http.StatusUnauthorized
}

var flashPattern = regexp.MustCompile(`<input id="security-flash-value"[^>]* value="([^"]*)"`)

// takeSecurityFlash loads the security page and returns the flash value.
func takeSecurityFlash(t *testing.T, handler http.Handler, cookie *http.Cookie) (string, bool) {
	t.Helper()
	rec := getWithCookie(handler, securityPagePath, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("security page status = %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("security page Cache-Control = %q, want no-store", got)
	}
	match := flashPattern.FindStringSubmatch(rec.Body.String())
	if match == nil {
		return "", false
	}
	return match[1], true
}

func credentialOverrideRows(t *testing.T, store *storage.Store) map[string]string {
	t.Helper()
	rows, err := store.CredentialOverrides(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestCredentialPasswordChangeTakesEffectAndResets(t *testing.T) {
	app, store, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	newPassword := "brand-new-password-456"

	rec := postSecurity(t, app, handler, cookie, securityPagePath+"/password", url.Values{"current_password": {testAdminPassword}, "new_password": {newPassword}, "confirm_password": {newPassword}})
	if notice := securityNoticeText(t, rec); !strings.Contains(notice, "密码已修改") {
		t.Fatalf("notice = %q", notice)
	}
	if strings.Contains(rec.Header().Get("Location"), newPassword) {
		t.Fatal("new password leaked into the redirect URL")
	}
	if _, code := loginAs(t, handler, "admin", testAdminPassword); code != http.StatusUnauthorized {
		t.Fatalf("old password login status = %d, want 401", code)
	}
	cookie = mustLogin(t, handler, "admin", newPassword)
	if got := app.CredentialSources().Password; got != CredentialSourceOverride {
		t.Fatalf("password source = %q, want override", got)
	}

	rows := credentialOverrideRows(t, store)
	if !strings.HasPrefix(rows[storage.CredentialAdminPasswordHash], "pbkdf2-sha256$600000$") {
		t.Fatalf("stored password hash has unexpected format")
	}
	for key, value := range rows {
		if strings.Contains(value, newPassword) {
			t.Fatalf("override %s contains the plaintext password", key)
		}
	}

	// Restore the environment password.
	rec = postSecurity(t, app, handler, cookie, securityPagePath+"/reset/password", url.Values{"current_password": {newPassword}})
	if notice := securityNoticeText(t, rec); !strings.Contains(notice, "已恢复为环境变量") {
		t.Fatalf("notice = %q", notice)
	}
	if got := app.CredentialSources().Password; got != CredentialSourceEnv {
		t.Fatalf("password source = %q, want env", got)
	}
	if _, code := loginAs(t, handler, "admin", newPassword); code != http.StatusUnauthorized {
		t.Fatalf("override password still accepted after reset: %d", code)
	}
	mustLogin(t, handler, "admin", testAdminPassword)
}

func TestCredentialPasswordRules(t *testing.T) {
	app, _, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	cases := []struct {
		name, newPassword, confirm, want string
	}{
		{"too short", "short", "short", "至少 12 个字符"},
		{"mismatch", "another-password-1", "another-password-2", "不一致"},
		{"same as current", testAdminPassword, testAdminPassword, "不能与当前密码相同"},
	}
	for _, tc := range cases {
		rec := postSecurity(t, app, handler, cookie, securityPagePath+"/password", url.Values{"current_password": {testAdminPassword}, "new_password": {tc.newPassword}, "confirm_password": {tc.confirm}})
		if notice := securityNoticeText(t, rec); !strings.Contains(notice, tc.want) {
			t.Fatalf("%s: notice = %q, want %q", tc.name, notice, tc.want)
		}
	}
	if got := app.CredentialSources().Password; got != CredentialSourceEnv {
		t.Fatalf("rejected changes must not create an override, source = %q", got)
	}
}

func TestCredentialPasswordChangeRevokesOtherSessionsAndRotatesCurrent(t *testing.T) {
	app, store, handler := credentialTestApp(t)
	current := mustLogin(t, handler, "admin", testAdminPassword)
	other := mustLogin(t, handler, "admin", testAdminPassword)
	oldCSRF := csrfOf(t, app, current)
	newPassword := "rotated-password-789"

	rec := postSecurity(t, app, handler, current, securityPagePath+"/password", url.Values{"current_password": {testAdminPassword}, "new_password": {newPassword}, "confirm_password": {newPassword}})
	securityNoticeText(t, rec)
	rotated := sessionCookieFrom(rec)
	if rotated == nil || rotated.Value == current.Value {
		t.Fatal("current session cookie was not rotated")
	}

	if got := getWithCookie(handler, "/admin", rotated); got.Code != http.StatusOK {
		t.Fatalf("rotated session GET /admin = %d, want 200", got.Code)
	}
	if newCSRF := csrfOf(t, app, rotated); newCSRF == oldCSRF {
		t.Fatal("CSRF token was not rotated")
	}
	for name, cookie := range map[string]*http.Cookie{"other": other, "pre-rotation": current} {
		got := getWithCookie(handler, "/admin", cookie)
		if got.Code != http.StatusSeeOther || got.Header().Get("Location") != "/admin/login" {
			t.Fatalf("%s session GET /admin = %d %q, want redirect to login", name, got.Code, got.Header().Get("Location"))
		}
	}

	// The database agrees: a fresh manager (as after a restart) only knows
	// the rotated session.
	fresh := newSessionManager(false, store)
	for name, cookie := range map[string]*http.Cookie{"other": other, "pre-rotation": current, "rotated": rotated} {
		req := httptest.NewRequest(http.MethodGet, "/admin", nil)
		req.AddCookie(cookie)
		_, ok := fresh.get(req)
		if want := name == "rotated"; ok != want {
			t.Fatalf("persisted session %s valid = %v, want %v", name, ok, want)
		}
	}
}

func TestCredentialUsernameChangeUpdatesSessionAndLogin(t *testing.T) {
	app, _, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	other := mustLogin(t, handler, "admin", testAdminPassword)

	for _, tc := range []struct{ username, want string }{
		{"   ", "不能为空"},
		{strings.Repeat("u", 65), "不能超过 64"},
		{"bad\x07name", "控制字符"},
		{"admin", "相同"},
	} {
		rec := postSecurity(t, app, handler, cookie, securityPagePath+"/username", url.Values{"current_password": {testAdminPassword}, "new_username": {tc.username}})
		if notice := securityNoticeText(t, rec); !strings.Contains(notice, tc.want) {
			t.Fatalf("username %q: notice = %q, want %q", tc.username, notice, tc.want)
		}
	}

	rec := postSecurity(t, app, handler, cookie, securityPagePath+"/username", url.Values{"current_password": {testAdminPassword}, "new_username": {"  curator  "}})
	if notice := securityNoticeText(t, rec); !strings.Contains(notice, "用户名已修改") {
		t.Fatalf("notice = %q", notice)
	}
	rotated := sessionCookieFrom(rec)
	if rotated == nil {
		t.Fatal("username change did not rotate the session")
	}
	page := getWithCookie(handler, securityPagePath, rotated)
	if !strings.Contains(page.Body.String(), "<span>curator</span>") {
		t.Fatal("session username was not updated to the new username")
	}
	if got := getWithCookie(handler, "/admin", other); got.Code != http.StatusSeeOther {
		t.Fatalf("other session after username change = %d, want redirect", got.Code)
	}
	if _, code := loginAs(t, handler, "admin", testAdminPassword); code != http.StatusUnauthorized {
		t.Fatalf("old username login = %d, want 401", code)
	}
	cookie = mustLogin(t, handler, "curator", testAdminPassword)

	rec = postSecurity(t, app, handler, cookie, securityPagePath+"/reset/username", url.Values{"current_password": {testAdminPassword}})
	securityNoticeText(t, rec)
	if got := app.CredentialSources().Username; got != CredentialSourceEnv {
		t.Fatalf("username source = %q, want env", got)
	}
	mustLogin(t, handler, "admin", testAdminPassword)
}

func TestCredentialAPITokenRegenerateFlashAndNoStore(t *testing.T) {
	app, store, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	if apiStatus(handler, testEnvAPIToken) != http.StatusOK {
		t.Fatal("env API token should work initially")
	}

	rec := postSecurity(t, app, handler, cookie, securityPagePath+"/api-token", url.Values{"current_password": {testAdminPassword}})
	notice := securityNoticeText(t, rec)
	if !strings.Contains(notice, "旧 Token 已失效") {
		t.Fatalf("notice = %q", notice)
	}
	if sessionCookieFrom(rec) != nil {
		t.Fatal("regenerating a token must not rotate the admin session")
	}

	token, ok := takeSecurityFlash(t, handler, cookie)
	if !ok || len(token) < 32 {
		t.Fatalf("flash token = %q, %v", token, ok)
	}
	if strings.Contains(rec.Header().Get("Location"), token) {
		t.Fatal("new token leaked into the redirect URL")
	}
	if _, again := takeSecurityFlash(t, handler, cookie); again {
		t.Fatal("flash must be shown only once")
	}

	if got := apiStatus(handler, testEnvAPIToken); got != http.StatusUnauthorized {
		t.Fatalf("old API token status = %d, want 401", got)
	}
	if got := apiStatus(handler, token); got != http.StatusOK {
		t.Fatalf("new API token status = %d, want 200", got)
	}
	if got := getWithCookie(handler, "/admin", cookie); got.Code != http.StatusOK {
		t.Fatalf("admin session after token change = %d, want 200", got.Code)
	}

	rows := credentialOverrideRows(t, store)
	sum := sha256.Sum256([]byte(token))
	if rows[storage.CredentialAPITokenHash] != fmt.Sprintf("%x", sum) {
		t.Fatal("stored API token is not its SHA-256 hex digest")
	}
	for key, value := range rows {
		if strings.Contains(value, token) {
			t.Fatalf("override %s contains the plaintext API token", key)
		}
	}

	rec = postSecurity(t, app, handler, cookie, securityPagePath+"/reset/api-token", url.Values{"current_password": {testAdminPassword}})
	securityNoticeText(t, rec)
	if apiStatus(handler, testEnvAPIToken) != http.StatusOK || apiStatus(handler, token) != http.StatusUnauthorized {
		t.Fatal("reset did not restore the environment API token")
	}
}

func TestCredentialCustomTokenValidation(t *testing.T) {
	app, _, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	cases := []struct{ path, token, want string }{
		{"/api-token", "too-short-token", "至少 24 位"},
		{"/media-token", "  short  ", "至少 24 位"},
		{"/api-token", "custom-token-with-a-tab\tinside-it", "只能包含"},
		{"/api-token", "custom token with spaces 0123456789", "只能包含"},
		{"/media-token", "custom-media-token-中文-0123456789", "只能包含"},
		{"/media-token", "custom-media-token/with+slash=0123", "只能包含"},
		{"/api-token", testEnvMediaToken, "不能相同"},
		{"/media-token", testEnvAPIToken, "不能相同"},
	}
	for _, tc := range cases {
		rec := postSecurity(t, app, handler, cookie, securityPagePath+tc.path, url.Values{"current_password": {testAdminPassword}, "custom_token": {tc.token}})
		if notice := securityNoticeText(t, rec); !strings.Contains(notice, tc.want) {
			t.Fatalf("%s %q: notice = %q, want %q", tc.path, tc.token, notice, tc.want)
		}
	}
	if sources := app.CredentialSources(); sources.APIToken != CredentialSourceEnv || sources.MediaToken != CredentialSourceEnv {
		t.Fatalf("rejected tokens must not create overrides: %+v", sources)
	}

	custom := "  custom-api-token-0123456789-abcdef  "
	rec := postSecurity(t, app, handler, cookie, securityPagePath+"/api-token", url.Values{"current_password": {testAdminPassword}, "custom_token": {custom}})
	securityNoticeText(t, rec)
	if got := apiStatus(handler, strings.TrimSpace(custom)); got != http.StatusOK {
		t.Fatalf("custom API token (trimmed) status = %d, want 200", got)
	}
	if _, ok := takeSecurityFlash(t, handler, cookie); ok {
		t.Fatal("a custom token must not be echoed back in a flash")
	}
}

func TestCredentialMediaTokenChangeRevealAndReset(t *testing.T) {
	app, store, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	if !mediaAuthorized(handler, testEnvMediaToken) {
		t.Fatal("env media token should work initially")
	}

	custom := "custom-media-token-0123456789"
	securityNoticeText(t, postSecurity(t, app, handler, cookie, securityPagePath+"/media-token", url.Values{"current_password": {testAdminPassword}, "custom_token": {custom}}))
	if mediaAuthorized(handler, testEnvMediaToken) {
		t.Fatal("old media token must be rejected after the change")
	}
	if !mediaAuthorized(handler, custom) {
		t.Fatal("new media token must be accepted")
	}
	if credentialOverrideRows(t, store)[storage.CredentialMediaToken] != custom {
		t.Fatal("media token override should be stored in plaintext for reveal")
	}

	// Reveal requires the current password.
	notice := securityNoticeText(t, postSecurity(t, app, handler, cookie, securityPagePath+"/media-token/reveal", url.Values{"current_password": {"wrong-password"}}))
	if !strings.Contains(notice, "当前密码不正确") {
		t.Fatalf("notice = %q", notice)
	}
	if _, ok := takeSecurityFlash(t, handler, cookie); ok {
		t.Fatal("wrong password must not reveal the media token")
	}
	rec := postSecurity(t, app, handler, cookie, securityPagePath+"/media-token/reveal", url.Values{"current_password": {testAdminPassword}})
	securityNoticeText(t, rec)
	if strings.Contains(rec.Header().Get("Location"), custom) {
		t.Fatal("media token leaked into the redirect URL")
	}
	if value, ok := takeSecurityFlash(t, handler, cookie); !ok || value != custom {
		t.Fatalf("revealed flash = %q, %v", value, ok)
	}

	// Generate: the new token is flashed once and the custom one dies.
	securityNoticeText(t, postSecurity(t, app, handler, cookie, securityPagePath+"/media-token", url.Values{"current_password": {testAdminPassword}}))
	generated, ok := takeSecurityFlash(t, handler, cookie)
	if !ok || !mediaAuthorized(handler, generated) || mediaAuthorized(handler, custom) {
		t.Fatal("generated media token did not replace the custom one")
	}

	securityNoticeText(t, postSecurity(t, app, handler, cookie, securityPagePath+"/reset/media-token", url.Values{"current_password": {testAdminPassword}}))
	if !mediaAuthorized(handler, testEnvMediaToken) || mediaAuthorized(handler, generated) {
		t.Fatal("reset did not restore the environment media token")
	}
	notice = securityNoticeText(t, postSecurity(t, app, handler, cookie, securityPagePath+"/reset/media-token", url.Values{"current_password": {testAdminPassword}}))
	if !strings.Contains(notice, "已在使用环境变量") {
		t.Fatalf("second reset notice = %q", notice)
	}
}

func TestCredentialSensitiveOperationsRequireCSRF(t *testing.T) {
	app, _, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	for _, path := range []string{"/username", "/password", "/api-token", "/media-token", "/media-token/reveal", "/reset/api-token"} {
		rec := postSecurity(t, app, handler, cookie, securityPagePath+path, url.Values{"csrfToken": {"forged"}, "current_password": {testAdminPassword}})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s without valid CSRF = %d, want 403", path, rec.Code)
		}
	}
	if got := app.CredentialSources(); got != (CredentialSources{Username: "env", Password: "env", APIToken: "env", MediaToken: "env"}) {
		t.Fatalf("sources = %+v", got)
	}
}

func TestCredentialWrongCurrentPasswordIsRateLimited(t *testing.T) {
	app, _, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	started := time.Now()
	for i := 0; i < loginMaxFailures; i++ {
		rec := postSecurity(t, app, handler, cookie, securityPagePath+"/api-token", url.Values{"current_password": {"not-the-password"}})
		if notice := securityNoticeText(t, rec); !strings.Contains(notice, "当前密码不正确") {
			t.Fatalf("attempt %d notice = %q", i, notice)
		}
	}
	if elapsed := time.Since(started); elapsed < time.Duration(loginMaxFailures)*loginFailureDelay {
		t.Fatalf("failures were not delayed: %s", elapsed)
	}
	// Locked: even the correct password is refused, like the login form.
	rec := postSecurity(t, app, handler, cookie, securityPagePath+"/api-token", url.Values{"current_password": {testAdminPassword}})
	if notice := securityNoticeText(t, rec); !strings.Contains(notice, "失败次数过多") {
		t.Fatalf("locked notice = %q", notice)
	}
	if _, code := loginAs(t, handler, "admin", testAdminPassword); code != http.StatusTooManyRequests {
		t.Fatalf("login while locked = %d, want 429", code)
	}
	if got := app.CredentialSources().APIToken; got != CredentialSourceEnv {
		t.Fatalf("API token changed despite failures: %q", got)
	}
}

func TestLoginLimiterIPKeyIgnoresUsername(t *testing.T) {
	limiter := newLoginLimiter()
	req := httptest.NewRequest(http.MethodPost, "/admin/login", nil)
	for i := 0; i < loginIPMaxFailures; i++ {
		limiter.recordFailureFor(req, fmt.Sprintf("user-%d", i))
	}
	if locked, _ := limiter.lockedFor(req, "admin"); !locked {
		t.Fatal("rotating usernames must still hit the per-IP limit")
	}
	other := httptest.NewRequest(http.MethodPost, "/admin/login", nil)
	other.RemoteAddr = "198.51.100.7:4000"
	if locked, _ := limiter.lockedFor(other, "admin"); locked {
		t.Fatal("another IP must not be locked")
	}
}

func TestCredentialOverridesSurviveRestartAndBeatEnvironment(t *testing.T) {
	app, store, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	apiToken := "persisted-api-token-0123456789"
	mediaToken := "persisted-media-token-0123456789"
	securityNoticeText(t, postSecurity(t, app, handler, cookie, securityPagePath+"/api-token", url.Values{"current_password": {testAdminPassword}, "custom_token": {apiToken}}))
	securityNoticeText(t, postSecurity(t, app, handler, cookie, securityPagePath+"/media-token", url.Values{"current_password": {testAdminPassword}, "custom_token": {mediaToken}}))

	restarted := newCredentialTestApp(t, credentialTestConfig(t), store)
	restartedHandler := restarted.Handler()
	if got := restarted.CredentialSources(); got.APIToken != CredentialSourceOverride || got.MediaToken != CredentialSourceOverride {
		t.Fatalf("sources after restart = %+v", got)
	}
	if apiStatus(restartedHandler, testEnvAPIToken) != http.StatusUnauthorized || apiStatus(restartedHandler, apiToken) != http.StatusOK {
		t.Fatal("API token override must win over the environment after restart")
	}
	if mediaAuthorized(restartedHandler, testEnvMediaToken) || !mediaAuthorized(restartedHandler, mediaToken) {
		t.Fatal("media token override must win over the environment after restart")
	}
	// The config still carries the raw environment values.
	if restarted.config.APIToken != testEnvAPIToken || restarted.config.MediaToken != testEnvMediaToken {
		t.Fatal("a.config must keep the environment values")
	}
}

func TestCredentialResetEnvironmentModes(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want CredentialSources
	}{
		{config.ResetCredentialsPassword, CredentialSources{Username: "env", Password: "env", APIToken: "override", MediaToken: "override"}},
		{config.ResetCredentialsTokens, CredentialSources{Username: "override", Password: "override", APIToken: "env", MediaToken: "env"}},
		{config.ResetCredentialsAll, CredentialSources{Username: "env", Password: "env", APIToken: "env", MediaToken: "env"}},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			store := credentialTestStore(t)
			ctx := context.Background()
			hash, err := hashPassword(ctx, "override-password-123")
			if err != nil {
				t.Fatal(err)
			}
			if err := store.UpdateCredentialOverrides(ctx, map[string]string{
				storage.CredentialAdminUsername:     "override-user",
				storage.CredentialAdminPasswordHash: hash,
				storage.CredentialAPITokenHash:      hashAPIToken("override-api-token-0123456789"),
				storage.CredentialMediaToken:        "override-media-token-0123456789",
			}, nil, nil); err != nil {
				t.Fatal(err)
			}
			if err := store.CreateAdminSession(ctx, storage.AdminSession{TokenHash: sessionTokenHash("old-session"), Username: "override-user", CSRFToken: "csrf", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}

			cfg := credentialTestConfig(t)
			cfg.ResetCredentials = tc.mode
			app := newCredentialTestApp(t, cfg, store)
			if got := app.CredentialSources(); got != tc.want {
				t.Fatalf("sources = %+v, want %+v", got, tc.want)
			}
			if _, err := store.AdminSessionByTokenHash(ctx, sessionTokenHash("old-session")); err == nil {
				t.Fatal("reset mode must delete every admin session")
			}
			handler := app.Handler()
			wantUser, wantPassword := "override-user", "override-password-123"
			if tc.want.Password == "env" {
				wantUser, wantPassword = "admin", testAdminPassword
			}
			cookie := mustLogin(t, handler, wantUser, wantPassword)
			page := getWithCookie(handler, securityPagePath, cookie)
			if !strings.Contains(page.Body.String(), `data-security-reset-mode="`+tc.mode+`"`) {
				t.Fatal("security page must expose the active reset mode")
			}
		})
	}
}

func TestPasswordHashFormatAndTunableIterations(t *testing.T) {
	ctx := context.Background()
	encoded, err := hashPassword(ctx, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" || parts[1] != "600000" {
		t.Fatalf("encoded = %q", encoded)
	}
	if salt, _ := base64.RawStdEncoding.DecodeString(parts[2]); len(salt) != 16 {
		t.Fatalf("salt length = %d", len(salt))
	}
	if key, _ := base64.RawStdEncoding.DecodeString(parts[3]); len(key) != 32 {
		t.Fatalf("key length = %d", len(key))
	}
	if ok, err := verifyPasswordHash(ctx, encoded, "correct horse battery"); !ok || err != nil {
		t.Fatalf("verify correct = %v, %v", ok, err)
	}
	if ok, _ := verifyPasswordHash(ctx, encoded, "wrong"); ok {
		t.Fatal("wrong password verified")
	}

	// Parameters are read from the encoding, so a different iteration
	// count (future tuning) still verifies.
	salt := []byte("0123456789abcdef")
	key, err := pbkdf2.Key(sha256.New, "tuned", salt, 1000, 32)
	if err != nil {
		t.Fatal(err)
	}
	tuned := "pbkdf2-sha256$1000$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(key)
	if ok, err := verifyPasswordHash(ctx, tuned, "tuned"); !ok || err != nil {
		t.Fatalf("tuned verify = %v, %v", ok, err)
	}
	for _, bad := range []string{"", "plain", "bcrypt$1000$abc$def", "pbkdf2-sha256$0$" + parts[2] + "$" + parts[3], "pbkdf2-sha256$99999999$" + parts[2] + "$" + parts[3], "pbkdf2-sha256$2000001$" + parts[2] + "$" + parts[3], "pbkdf2-sha256$1000$" + base64.RawStdEncoding.EncodeToString(make([]byte, 65)) + "$" + parts[3], "pbkdf2-sha256$1000$!!$" + parts[3]} {
		if _, err := verifyPasswordHash(ctx, bad, "x"); err == nil {
			t.Fatalf("malformed hash %q accepted", bad)
		}
	}
}

func TestPasswordHashSlotsBoundConcurrency(t *testing.T) {
	var releases []func()
	for i := 0; i < cap(passwordHashSlots); i++ {
		release, err := acquirePasswordHashSlot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := acquirePasswordHashSlot(ctx); err == nil {
		t.Fatal("a third concurrent hash must wait for a slot")
	}
	for _, release := range releases {
		release()
	}
}

// TestCredentialStoreConcurrentReadWrite exercises the atomic snapshot
// under `go test -race`: API/media requests authenticate while tokens are
// rotated concurrently. Every request must observe either an old or a new
// complete snapshot.
func TestCredentialStoreConcurrentReadWrite(t *testing.T) {
	app, _, handler := credentialTestApp(t)
	tokens := []string{"concurrent-api-token-000000000", "concurrent-api-token-111111111", "concurrent-api-token-222222222"}
	ctx := context.Background()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for reader := 0; reader < 4; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, token := range tokens {
					status := apiStatus(handler, token)
					if status != http.StatusOK && status != http.StatusUnauthorized {
						t.Errorf("unexpected status %d", status)
						return
					}
				}
				mediaAuthorized(handler, testEnvMediaToken)
				_ = app.CredentialSources()
			}
		}()
	}
	for writer := 0; writer < 2; writer++ {
		wg.Add(1)
		go func(writer int) {
			defer wg.Done()
			for i := 0; i < 15; i++ {
				token := tokens[(i+writer)%len(tokens)]
				if _, err := app.credentials.update(ctx, nil, map[string]string{storage.CredentialAPITokenHash: hashAPIToken(token)}, nil); err != nil {
					t.Errorf("update: %v", err)
					return
				}
			}
		}(writer)
	}
	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()

	// After the writers finish, exactly the last published token works.
	accepted := 0
	for _, token := range tokens {
		if apiStatus(handler, token) == http.StatusOK {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted tokens = %d, want exactly 1", accepted)
	}
}

// TestCredentialUpdateFailureKeepsSnapshot: when the database write fails
// the published credentials must not change.
func TestCredentialUpdateFailureKeepsSnapshot(t *testing.T) {
	app, store, _ := credentialTestApp(t)
	store.Close()
	if _, err := app.credentials.update(context.Background(), nil, map[string]string{storage.CredentialAPITokenHash: hashAPIToken("never-published-token-0123456789")}, nil); err == nil {
		t.Fatal("update on a closed database should fail")
	}
	if got := app.CredentialSources().APIToken; got != CredentialSourceEnv {
		t.Fatalf("API token source = %q after failed write, want env", got)
	}
	if !app.currentCredentials().apiTokenMatches(testEnvAPIToken) {
		t.Fatal("environment API token must still be effective")
	}
}
