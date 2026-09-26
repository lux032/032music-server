package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/config"
	"github.com/lux032/032music-server/internal/storage"
)

// rawCredentialDB opens a second connection to the test database for
// fault injection and row counts.
func rawCredentialDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func countAdminSessions(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM admin_sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func cachedSessionCount(app *App) int {
	app.sessions.mu.Lock()
	defer app.sessions.mu.Unlock()
	return len(app.sessions.sessions)
}

// TestLoginRacingPasswordChangeLeavesNoSession (H-1): a login that already
// read the old credentials when a password change (with revocation of all
// other sessions) completes must not create a surviving session.
func TestLoginRacingPasswordChangeLeavesNoSession(t *testing.T) {
	store, path := credentialTestStoreAt(t)
	app := newCredentialTestApp(t, credentialTestConfig(t), store)
	handler := app.Handler()
	db := rawCredentialDB(t, path)
	admin := mustLogin(t, handler, "admin", testAdminPassword)
	newPassword := "changed-during-login-123"

	var rotated *http.Cookie
	var once sync.Once
	app.afterLoginCredentialsRead = func() {
		once.Do(func() {
			rec := postSecurity(t, app, handler, admin, securityPagePath+"/password", url.Values{"current_password": {testAdminPassword}, "new_password": {newPassword}, "confirm_password": {newPassword}})
			if notice := securityNotice(t, rec); !strings.Contains(notice, "密码已修改") {
				t.Errorf("injected change notice = %q", notice)
			}
			rotated = sessionCookieFrom(rec)
		})
	}

	form := url.Values{"username": {"admin"}, "password": {testAdminPassword}}
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	app.afterLoginCredentialsRead = nil

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login?notice=retry" {
		t.Fatalf("racing login = %d %q, want 303 to /admin/login?notice=retry", rec.Code, rec.Header().Get("Location"))
	}
	if sessionCookieFrom(rec) != nil {
		t.Fatal("racing login must not set a session cookie")
	}
	if rotated == nil {
		t.Fatal("injected password change did not rotate the admin session")
	}
	if got := cachedSessionCount(app); got != 1 {
		t.Fatalf("cached sessions = %d, want only the rotated one", got)
	}
	if got := countAdminSessions(t, db); got != 1 {
		t.Fatalf("persisted sessions = %d, want only the rotated one", got)
	}
	if got := getWithCookie(handler, "/admin", rotated); got.Code != http.StatusOK {
		t.Fatalf("rotated session = %d, want 200", got.Code)
	}
	page := getWithCookie(handler, "/admin/login?notice=retry", nil)
	if !strings.Contains(page.Body.String(), "登录状态已变更，请重试") {
		t.Fatal("login page does not show the retry notice")
	}
	if _, code := loginAs(t, handler, "admin", testAdminPassword); code != http.StatusUnauthorized {
		t.Fatalf("old password after change = %d, want 401", code)
	}
	mustLogin(t, handler, "admin", newPassword)
}

// TestSensitiveSaveRefusesStaleSnapshot (H-1): a change verified against
// credentials that were replaced meanwhile is refused.
func TestSensitiveSaveRefusesStaleSnapshot(t *testing.T) {
	app, _, _ := credentialTestApp(t)
	ctx := context.Background()
	stale := app.currentCredentials()
	if _, err := app.credentials.update(ctx, nil, map[string]string{storage.CredentialAPITokenHash: hashAPIToken("first-concurrent-token-0123456789")}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := app.credentials.update(ctx, stale, map[string]string{storage.CredentialMediaToken: "stale-media-token-0123456789"}, nil); !errors.Is(err, errCredentialsChanged) {
		t.Fatalf("token update with stale snapshot err = %v, want errCredentialsChanged", err)
	}
	rec := httptest.NewRecorder()
	if _, err := app.credentials.updateLogin(ctx, app.sessions, rec, stale, map[string]string{storage.CredentialAdminUsername: "stale-user"}, nil); !errors.Is(err, errCredentialsChanged) {
		t.Fatalf("login update with stale snapshot err = %v, want errCredentialsChanged", err)
	}
	if sessionCookieFrom(rec) != nil {
		t.Fatal("refused login update must not rotate the session")
	}
	if got := app.CredentialSources(); got.MediaToken != CredentialSourceEnv || got.Username != CredentialSourceEnv {
		t.Fatalf("stale updates were applied: %+v", got)
	}
}

// TestPasswordChangeSessionReplacementFailureKeepsCredentials (M-1): the
// override write and the session replacement share one transaction, so a
// failing session write leaves the password (and all sessions) unchanged.
func TestPasswordChangeSessionReplacementFailureKeepsCredentials(t *testing.T) {
	store, path := credentialTestStoreAt(t)
	app := newCredentialTestApp(t, credentialTestConfig(t), store)
	handler := app.Handler()
	db := rawCredentialDB(t, path)
	current := mustLogin(t, handler, "admin", testAdminPassword)
	other := mustLogin(t, handler, "admin", testAdminPassword)
	if _, err := db.Exec(`CREATE TRIGGER fail_session_insert BEFORE INSERT ON admin_sessions BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	newPassword := "never-applied-password-1"
	rec := postSecurity(t, app, handler, current, securityPagePath+"/password", url.Values{"current_password": {testAdminPassword}, "new_password": {newPassword}, "confirm_password": {newPassword}})
	if notice := securityNotice(t, rec); !strings.Contains(notice, "保存失败") {
		t.Fatalf("notice = %q", notice)
	}
	if sessionCookieFrom(rec) != nil {
		t.Fatal("failed change must not rotate the cookie")
	}
	if got := app.CredentialSources().Password; got != CredentialSourceEnv {
		t.Fatalf("password source = %q, want env", got)
	}
	if rows := credentialOverrideRows(t, store); len(rows) != 0 {
		t.Fatalf("override rows = %v, want none", rows)
	}
	if got := countAdminSessions(t, db); got != 2 {
		t.Fatalf("persisted sessions = %d, want both kept", got)
	}
	for name, cookie := range map[string]*http.Cookie{"current": current, "other": other} {
		if got := getWithCookie(handler, "/admin", cookie); got.Code != http.StatusOK {
			t.Fatalf("%s session after failed change = %d, want 200", name, got.Code)
		}
	}
	if _, err := db.Exec(`DROP TRIGGER fail_session_insert`); err != nil {
		t.Fatal(err)
	}
	mustLogin(t, handler, "admin", testAdminPassword)
}

// passwordOverrideApp returns an app whose admin password is a PBKDF2
// override, so password checks go through the hash limits.
func passwordOverrideApp(t *testing.T, password string) (*App, http.Handler) {
	t.Helper()
	app, _, handler := credentialTestApp(t)
	hash, err := hashPassword(context.Background(), password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.credentials.update(context.Background(), nil, map[string]string{storage.CredentialAdminPasswordHash: hash}, nil); err != nil {
		t.Fatal(err)
	}
	return app, handler
}

// TestPasswordHashBusyGlobalTimeout (M-2): when every global hash slot
// stays taken, login and sensitive operations answer "busy" after the
// bounded wait instead of queueing forever, without counting a failure.
func TestPasswordHashBusyGlobalTimeout(t *testing.T) {
	password := "hashed-override-password"
	app, handler := passwordOverrideApp(t, password)
	cookie := mustLogin(t, handler, "admin", password)

	previous := passwordHashWait
	passwordHashWait = 100 * time.Millisecond
	t.Cleanup(func() { passwordHashWait = previous })
	var releases []func()
	for i := 0; i < cap(passwordHashSlots); i++ {
		release, err := acquirePasswordHashSlot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	releaseAll := func() {
		for _, release := range releases {
			release()
		}
		releases = nil
	}
	t.Cleanup(releaseAll)

	started := time.Now()
	form := url.Values{"username": {"admin"}, "password": {password}}
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login?notice=busy" {
		t.Fatalf("busy login = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("busy login waited %s", elapsed)
	}
	if !strings.Contains(getWithCookie(handler, "/admin/login?notice=busy", nil).Body.String(), "系统繁忙") {
		t.Fatal("login page does not show the busy notice")
	}
	notice := securityNotice(t, postSecurity(t, app, handler, cookie, securityPagePath+"/api-token", url.Values{"current_password": {password}}))
	if !strings.Contains(notice, "系统繁忙") {
		t.Fatalf("sensitive op notice = %q", notice)
	}
	if locked, _ := app.loginLimiter.lockedFor(req, "admin"); locked {
		t.Fatal("busy answers must not count as failures")
	}
	releaseAll()
	mustLogin(t, handler, "admin", password)
}

// TestPasswordHashBusyPerIP (M-2): one client IP may run only one hash
// computation at a time; a second one is rejected immediately.
func TestPasswordHashBusyPerIP(t *testing.T) {
	password := "hashed-override-password"
	app, handler := passwordOverrideApp(t, password)
	probe := httptest.NewRequest(http.MethodPost, "/admin/login", nil)
	end, ok := app.loginLimiter.beginHash(probe)
	if !ok {
		t.Fatal("first hash for the IP must be allowed")
	}
	if _, again := app.loginLimiter.beginHash(probe); again {
		t.Fatal("second concurrent hash for the same IP must be refused")
	}
	started := time.Now()
	cookie, code := loginAs(t, handler, "admin", password)
	if code != http.StatusSeeOther || cookie != nil {
		t.Fatalf("login during in-flight hash = %d, cookie %v; want busy redirect", code, cookie)
	}
	if elapsed := time.Since(started); elapsed >= passwordHashWait {
		t.Fatalf("per-IP rejection queued for %s", elapsed)
	}
	other := httptest.NewRequest(http.MethodPost, "/admin/login", nil)
	other.RemoteAddr = "198.51.100.20:5000"
	if endOther, ok := app.loginLimiter.beginHash(other); !ok {
		t.Fatal("another IP must not be blocked")
	} else {
		endOther()
	}
	end()
	mustLogin(t, handler, "admin", password)
}

func TestLoginLimiterTrustedProxies(t *testing.T) {
	trusted, err := config.ParseTrustedProxies("10.0.0.0/8, 192.168.1.1")
	if err != nil {
		t.Fatal(err)
	}
	limiter := newLoginLimiter()
	limiter.trustedProxies = trusted
	request := func(remote string, xff ...string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/admin/login", nil)
		r.RemoteAddr = remote
		for _, value := range xff {
			r.Header.Add("X-Forwarded-For", value)
		}
		return r
	}
	for _, tc := range []struct {
		name string
		r    *http.Request
		want string
	}{
		{"trusted proxy", request("10.0.0.5:1234", "203.0.113.9"), "203.0.113.9"},
		{"proxy chain", request("10.0.0.5:1234", "203.0.113.9, 10.0.0.7"), "203.0.113.9"},
		{"forged left entry ignored", request("192.168.1.1:80", "1.1.1.1, 203.0.113.9"), "203.0.113.9"},
		{"multiple headers", request("10.0.0.5:1234", "1.1.1.1", "203.0.113.9"), "203.0.113.9"},
		{"untrusted peer ignores XFF", request("198.51.100.1:1234", "203.0.113.9"), "198.51.100.1"},
		{"no XFF", request("10.0.0.5:1234"), "10.0.0.5"},
		{"garbage hop stops", request("10.0.0.5:1234", "garbage, 10.0.0.7"), "10.0.0.7"},
		{"ipv4-mapped peer", request("[::ffff:10.0.0.5]:1234", "203.0.113.9"), "203.0.113.9"},
	} {
		if got := limiter.clientIP(tc.r); got != tc.want {
			t.Fatalf("%s: clientIP = %q, want %q", tc.name, got, tc.want)
		}
	}

	// Without MUSIC_SERVER_TRUSTED_PROXIES the header is never trusted.
	plain := newLoginLimiter()
	if got := plain.clientIP(request("10.0.0.5:1234", "203.0.113.9")); got != "10.0.0.5" {
		t.Fatalf("unconfigured clientIP = %q, want RemoteAddr", got)
	}

	// Failures from different clients behind the same proxy are counted
	// separately, so one client cannot lock out the admin.
	for i := 0; i < loginIPMaxFailures; i++ {
		limiter.recordFailureFor(request("10.0.0.5:1234", "203.0.113.66"), "admin")
	}
	if locked, _ := limiter.lockedFor(request("10.0.0.5:1234", "203.0.113.66"), "admin"); !locked {
		t.Fatal("attacker behind the proxy must be locked")
	}
	if locked, _ := limiter.lockedFor(request("10.0.0.5:1234", "203.0.113.9"), "admin"); locked {
		t.Fatal("another client behind the same proxy must not be locked")
	}
}

// TestCorruptOverridesFallBackToEnvironment (Low-2): invalid stored rows are
// ignored with a warning and the environment values stay effective.
func TestCorruptOverridesFallBackToEnvironment(t *testing.T) {
	store := credentialTestStore(t)
	if err := store.UpdateCredentialOverrides(context.Background(), map[string]string{
		storage.CredentialAdminUsername:     " padded ",
		storage.CredentialAdminPasswordHash: "not-a-hash",
		storage.CredentialAPITokenHash:      "zz",
		storage.CredentialMediaToken:        "short",
	}, nil, nil); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	app, err := NewApp(credentialTestConfig(t), store, nil, nil, slog.New(slog.NewTextHandler(&logs, nil)), "test")
	if err != nil {
		t.Fatal(err)
	}
	if got := app.CredentialSources(); got != (CredentialSources{Username: "env", Password: "env", APIToken: "env", MediaToken: "env"}) {
		t.Fatalf("sources = %+v, want all env", got)
	}
	if strings.Count(logs.String(), "stored credential override is invalid") != 4 {
		t.Fatalf("expected 4 invalid-override warnings, logs:\n%s", logs.String())
	}
	handler := app.Handler()
	mustLogin(t, handler, "admin", testAdminPassword)
	if apiStatus(handler, testEnvAPIToken) != http.StatusOK || !mediaAuthorized(handler, testEnvMediaToken) {
		t.Fatal("environment tokens must be effective")
	}
	// Later changes are not blocked by the ignored rows.
	if _, err := app.credentials.update(context.Background(), nil, map[string]string{storage.CredentialMediaToken: "repaired-media-token-0123456789"}, nil); err != nil {
		t.Fatalf("update after corrupt rows: %v", err)
	}
}

func TestMediaAccessRejectsEmptyBearer(t *testing.T) {
	app := &App{
		config:   config.Config{APIToken: "api-token-at-least-24-characters"},
		sessions: testSessionManager(t),
	}
	handler := app.requireMediaAccess(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	req := httptest.NewRequest(http.MethodGet, "/stream", nil)
	req.Header.Set("Authorization", "Bearer ")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("empty Bearer media token = %d, want 401", rec.Code)
	}
}

func TestGeneratedTokensAreURLSafe(t *testing.T) {
	for i := 0; i < 50; i++ {
		token, generated, problem := chooseCredentialToken("")
		if !generated || problem != "" || tokenProblem(token) != "" {
			t.Fatalf("generated token %q rejected: %q", token, tokenProblem(token))
		}
	}
	if problem := tokenProblem("Az09-_.~Az09-_.~Az09-_.~"); problem != "" {
		t.Fatalf("unreserved characters rejected: %q", problem)
	}
}

func TestSetFlashReportsMissingSession(t *testing.T) {
	manager := testSessionManager(t)
	if manager.setFlash(httptest.NewRequest(http.MethodGet, "/", nil), securityFlash{Value: "x"}) {
		t.Fatal("setFlash without a session cookie must report false")
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: adminSessionCookie, Value: "revoked"})
	if manager.setFlash(req, securityFlash{Value: "x"}) {
		t.Fatal("setFlash for an unknown session must report false")
	}
	rec := httptest.NewRecorder()
	if _, err := manager.create(rec, "admin"); err != nil {
		t.Fatal(err)
	}
	live := httptest.NewRequest(http.MethodGet, "/", nil)
	live.AddCookie(sessionCookieFrom(rec))
	if !manager.setFlash(live, securityFlash{Value: "x"}) {
		t.Fatal("setFlash for a live session must report true")
	}
}

func TestSecurityPageNavigationEntry(t *testing.T) {
	_, _, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	body := getWithCookie(handler, securityPagePath, cookie).Body.String()
	if got := strings.Count(body, `href="/admin/settings/security" data-nav="security" class="active" aria-current="page"`); got != 2 {
		t.Fatalf("active security nav entries = %d, want 2 (sidebar + mobile sheet)", got)
	}
	other := getWithCookie(handler, "/admin", cookie).Body.String()
	if !strings.Contains(other, `href="/admin/settings/security" data-nav="security" class=""`) {
		t.Fatal("security entry must be reachable (not highlighted) from other pages")
	}
}

// TestLockedLoginDoesNotLogUsername (Low-6).
func TestLockedLoginDoesNotLogUsername(t *testing.T) {
	app, _, handler := credentialTestApp(t)
	var logs bytes.Buffer
	app.logger = slog.New(slog.NewTextHandler(&logs, nil))
	secretName := "secret-typed-username"
	probe := httptest.NewRequest(http.MethodPost, "/admin/login", nil)
	for i := 0; i < loginMaxFailures; i++ {
		app.loginLimiter.recordFailureFor(probe, secretName)
	}
	if _, code := loginAs(t, handler, secretName, "whatever"); code != http.StatusTooManyRequests {
		t.Fatalf("locked login = %d, want 429", code)
	}
	if !strings.Contains(logs.String(), "login attempt while locked out") {
		t.Fatal("locked login was not logged")
	}
	if strings.Contains(logs.String(), secretName) {
		t.Fatal("submitted username leaked into the log")
	}
}

func TestParseTrustedProxiesValidation(t *testing.T) {
	prefixes, err := config.ParseTrustedProxies(" 10.0.0.0/8 ,127.0.0.1,, ::1 ")
	if err != nil || len(prefixes) != 3 {
		t.Fatalf("prefixes = %v, err = %v", prefixes, err)
	}
	if !prefixes[1].Contains(netip.MustParseAddr("127.0.0.1")) {
		t.Fatal("bare IP must become a single-address prefix")
	}
	for _, bad := range []string{"10.0.0.0/33", "not-an-ip", "10.0.0.1/"} {
		if _, err := config.ParseTrustedProxies(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}
