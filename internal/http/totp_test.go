package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/config"
	"github.com/lux032/032music-server/internal/storage"
)

// RFC 6238 appendix B (SHA-1 seed), truncated to the last six digits.
func TestTOTPRFC6238Vectors(t *testing.T) {
	key := []byte("12345678901234567890")
	for _, tc := range []struct {
		unix int64
		want string
	}{{59, "287082"}, {1111111109, "081804"}, {1234567890, "005924"}, {2000000000, "279037"}} {
		if got := totpCodeAt(key, totpStep(time.Unix(tc.unix, 0))); got != tc.want {
			t.Fatalf("t=%d code = %s, want %s", tc.unix, got, tc.want)
		}
	}
}

func TestTOTPMatchSkewFormatAndReplay(t *testing.T) {
	secret, err := generateTOTPSecret()
	if err != nil || !validTOTPSecret(secret) {
		t.Fatalf("secret %q err %v", secret, err)
	}
	now := time.Unix(1_800_000_000, 0)
	key, _ := decodeTOTPSecret(secret)
	step := totpStep(now)
	for offset, want := range map[int64]bool{-2: false, -1: true, 0: true, 1: true, 2: false} {
		if _, ok := matchTOTP(secret, totpCodeAt(key, step+offset), now); ok != want {
			t.Fatalf("offset %d accepted = %v", offset, ok)
		}
	}
	code := totpCodeAt(key, step)
	if _, ok := matchTOTP(secret, code[:3]+" "+code[3:], now); !ok {
		t.Fatal("a space inside the code must be tolerated")
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef", "１２３４５６"} {
		if _, ok := matchTOTP(secret, bad, now); ok {
			t.Fatalf("malformed code %q accepted", bad)
		}
	}
	var guard totpReplayGuard
	if !guard.verify(secret, code, now) || guard.verify(secret, code, now) {
		t.Fatal("a code must be accepted exactly once")
	}
	if guard.verify(secret, totpCodeAt(key, step-1), now) {
		t.Fatal("an earlier step must not be accepted after a later one")
	}
	if !guard.verify(secret, totpCodeAt(key, step+1), now) {
		t.Fatal("the next step must still be accepted")
	}
	for _, bad := range []string{"", "abc", "AAAA", strings.ToLower(secret)} {
		if validTOTPSecret(bad) {
			t.Fatalf("invalid secret %q accepted", bad)
		}
	}
	if uri := totpURI("ad min", secret); !strings.HasPrefix(uri, "otpauth://totp/032%20Music%20Server:ad%20min?") || !strings.Contains(uri, "secret="+secret) {
		t.Fatalf("uri = %s", uri)
	}
	if qr := totpQRDataURI(totpURI("admin", secret)); !strings.HasPrefix(qr, "data:image/png;base64,") {
		t.Fatal("QR image must be a PNG data URI")
	}
}

var totpSecretPattern = regexp.MustCompile(`value="([A-Z2-7 ]+)"[^>]*data-totp-secret`)

func totpCodeFor(t *testing.T, secret string, offset int64) string {
	t.Helper()
	key, ok := decodeTOTPSecret(secret)
	if !ok {
		t.Fatalf("bad secret %q", secret)
	}
	return totpCodeAt(key, totpStep(time.Now())+offset)
}

// enableTOTP enrolls 2FA through the security page and returns the secret.
// The enrollment consumes the current step's code.
func enableTOTP(t *testing.T, app *App, handler http.Handler, cookie *http.Cookie) (string, *http.Cookie) {
	t.Helper()
	if got := securityNoticeText(t, postSecurity(t, app, handler, cookie, securityPagePath+"/totp/setup", url.Values{"current_password": {testAdminPassword}})); got != securityNotices["totp_setup_started"].Text {
		t.Fatalf("setup notice = %q", got)
	}
	page := getWithCookie(handler, securityPagePath, cookie)
	if page.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("page with a pending secret must be no-store")
	}
	body := page.Body.String()
	match := totpSecretPattern.FindStringSubmatch(body)
	if match == nil || !strings.Contains(body, `src="data:image/png;base64,`) {
		t.Fatal("pending setup must show the secret and the QR code")
	}
	secret := strings.ReplaceAll(match[1], " ", "")
	if got := securityNoticeText(t, postSecurity(t, app, handler, cookie, securityPagePath+"/totp/enable", url.Values{"code": {"000000"}})); got != securityNotices["totp_code_wrong"].Text && totpCodeFor(t, secret, 0) != "000000" {
		t.Fatalf("wrong confirmation code notice = %q", got)
	}
	rec := postSecurity(t, app, handler, cookie, securityPagePath+"/totp/enable", url.Values{"code": {totpCodeFor(t, secret, 0)}})
	if got := securityNoticeText(t, rec); got != securityNotices["totp_enabled"].Text {
		t.Fatalf("enable notice = %q", got)
	}
	rotated := sessionCookieFrom(rec)
	if rotated == nil {
		t.Fatal("enabling 2FA must rotate the current session")
	}
	return secret, rotated
}

func mfaCookieFrom(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == adminMFACookie && cookie.Value != "" {
			return cookie
		}
	}
	return nil
}

// passwordStep posts the password form and returns the 2FA challenge cookie.
func passwordStep(t *testing.T, handler http.Handler, next string) *http.Cookie {
	t.Helper()
	form := url.Values{"username": {"admin"}, "password": {testAdminPassword}}
	if next != "" {
		form.Set("next", next)
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != totpLoginPath {
		t.Fatalf("password step = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if sessionCookieFrom(rec) != nil {
		t.Fatal("the password alone must not create a session when 2FA is on")
	}
	challenge := mfaCookieFrom(rec)
	if challenge == nil || !challenge.HttpOnly || challenge.Path != adminMFACookiePath {
		t.Fatalf("challenge cookie = %+v", challenge)
	}
	return challenge
}

func codeStep(handler http.Handler, challenge *http.Cookie, code string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, totpLoginPath, strings.NewReader(url.Values{"code": {code}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if challenge != nil {
		req.AddCookie(challenge)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestTOTPLoginFlow(t *testing.T) {
	app, store, handler := credentialTestApp(t)
	other := mustLogin(t, handler, "admin", testAdminPassword)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	secret, cookie := enableTOTP(t, app, handler, cookie)
	if credentialOverrideRows(t, store)[storage.CredentialAdminTOTPSecret] != secret {
		t.Fatal("secret must be persisted")
	}
	if getWithCookie(handler, "/admin", other).Code != http.StatusSeeOther {
		t.Fatal("enabling 2FA must revoke other sessions")
	}
	if getWithCookie(handler, "/admin", cookie).Code != http.StatusOK {
		t.Fatal("the enrolling session must stay logged in")
	}
	if body := getWithCookie(handler, securityPagePath, cookie).Body.String(); !strings.Contains(body, `data-totp-status="enabled"`) || strings.Contains(body, "data-totp-secret") {
		t.Fatal("enabled page must show the status and never the secret")
	}

	// No challenge: the code page sends back to the login form.
	if rec := getWithCookie(handler, totpLoginPath, nil); rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "notice=mfa_expired") {
		t.Fatalf("code page without challenge = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := codeStep(handler, nil, totpCodeFor(t, secret, 1)); sessionCookieFrom(rec) != nil {
		t.Fatal("a code without a challenge must not log in")
	}

	challenge := passwordStep(t, handler, "/admin/library")
	if rec := getWithCookie(handler, totpLoginPath, challenge); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "data-login-totp") || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("code page = %d", rec.Code)
	}
	if rec := codeStep(handler, challenge, "abcdef"); rec.Code != http.StatusUnauthorized || sessionCookieFrom(rec) != nil {
		t.Fatalf("wrong code = %d", rec.Code)
	}
	// The step consumed by the enrollment cannot be replayed.
	if rec := codeStep(handler, challenge, totpCodeFor(t, secret, 0)); rec.Code != http.StatusUnauthorized && totpStep(time.Now()) == totpStep(time.Now().Add(-time.Second)) {
		t.Fatalf("replayed enrollment code = %d", rec.Code)
	}
	code := totpCodeFor(t, secret, 1)
	rec := codeStep(handler, challenge, code)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/library" {
		t.Fatalf("correct code = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	session := sessionCookieFrom(rec)
	if session == nil || getWithCookie(handler, "/admin", session).Code != http.StatusOK {
		t.Fatal("a correct code must create a working session")
	}
	if rec := codeStep(handler, challenge, code); sessionCookieFrom(rec) != nil {
		t.Fatal("a used challenge must not log in again")
	}
	// Same code on a fresh challenge: replay rejected.
	if rec := codeStep(handler, passwordStep(t, handler, ""), code); rec.Code != http.StatusUnauthorized {
		t.Fatalf("replayed code on a new challenge = %d", rec.Code)
	}
	// A wrong password never reaches the code step.
	if _, status := loginAs(t, handler, "admin", "wrong-password-xyz"); status != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d", status)
	}
}

func TestTOTPChallengeAttemptsAndLimiter(t *testing.T) {
	app, _, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	secret, _ := enableTOTP(t, app, handler, cookie)
	good := map[string]bool{totpCodeFor(t, secret, -1): true, totpCodeFor(t, secret, 0): true, totpCodeFor(t, secret, 1): true, totpCodeFor(t, secret, 2): true}
	wrong := "000000"
	for good[wrong] {
		wrong = "1" + wrong[1:]
	}

	// Two challenges × 5 wrong codes = 10 failures: the correct password
	// in between must not reset the limiter.
	for round := 0; round < 2; round++ {
		challenge := passwordStep(t, handler, "")
		for i := 0; i < loginChallengeMaxAttempts-1; i++ {
			if rec := codeStep(handler, challenge, wrong); rec.Code != http.StatusUnauthorized {
				t.Fatalf("round %d attempt %d = %d", round, i, rec.Code)
			}
		}
		rec := codeStep(handler, challenge, wrong)
		if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "mfa_expired") {
			t.Fatalf("exhausted challenge = %d %q", rec.Code, rec.Header().Get("Location"))
		}
		if rec := codeStep(handler, challenge, totpCodeFor(t, secret, 1)); sessionCookieFrom(rec) != nil {
			t.Fatal("an exhausted challenge must not log in")
		}
	}
	if _, status := loginAs(t, handler, "admin", testAdminPassword); status != http.StatusTooManyRequests {
		t.Fatalf("after 10 wrong codes login = %d, want 429", status)
	}
}

func TestTOTPDisableRequiresCodeAndResetModeClears(t *testing.T) {
	app, store, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	secret, cookie := enableTOTP(t, app, handler, cookie)

	if got := securityNoticeText(t, postSecurity(t, app, handler, cookie, securityPagePath+"/totp/disable", url.Values{"current_password": {testAdminPassword}, "code": {"abcdef"}})); got != securityNotices["totp_code_wrong"].Text {
		t.Fatalf("wrong code notice = %q", got)
	}
	if got := securityNoticeText(t, postSecurity(t, app, handler, cookie, securityPagePath+"/totp/disable", url.Values{"current_password": {"wrong"}, "code": {totpCodeFor(t, secret, 1)}})); got != securityNotices["wrong_password"].Text {
		t.Fatalf("wrong password notice = %q", got)
	}
	if rec := postSecurity(t, app, handler, cookie, securityPagePath+"/totp/disable", url.Values{"csrfToken": {"bad"}, "current_password": {testAdminPassword}, "code": {totpCodeFor(t, secret, 1)}}); rec.Code != http.StatusForbidden {
		t.Fatalf("disable without CSRF = %d", rec.Code)
	}
	rec := postSecurity(t, app, handler, cookie, securityPagePath+"/totp/disable", url.Values{"current_password": {testAdminPassword}, "code": {totpCodeFor(t, secret, 1)}})
	if got := securityNoticeText(t, rec); got != securityNotices["totp_disabled"].Text {
		t.Fatalf("disable notice = %q", got)
	}
	if _, ok := credentialOverrideRows(t, store)[storage.CredentialAdminTOTPSecret]; ok {
		t.Fatal("disabling must delete the stored secret")
	}
	mustLogin(t, handler, "admin", testAdminPassword)

	// Re-enable, then restart with MUSIC_SERVER_RESET_CREDENTIALS=password.
	enableTOTP(t, app, handler, sessionCookieFrom(rec))
	cfg := credentialTestConfig(t)
	cfg.ResetCredentials = config.ResetCredentialsPassword
	restarted := newCredentialTestApp(t, cfg, store)
	if restarted.currentCredentials().totpEnabled() {
		t.Fatal("reset mode password must clear 2FA")
	}
	mustLogin(t, restarted.Handler(), "admin", testAdminPassword)
}

func TestTOTPSetupCancelAndSetupRequiresPassword(t *testing.T) {
	app, _, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	if got := securityNoticeText(t, postSecurity(t, app, handler, cookie, securityPagePath+"/totp/setup", url.Values{"current_password": {"wrong"}})); got != securityNotices["wrong_password"].Text {
		t.Fatalf("setup with wrong password = %q", got)
	}
	if got := securityNoticeText(t, postSecurity(t, app, handler, cookie, securityPagePath+"/totp/enable", url.Values{"code": {"123456"}})); got != securityNotices["totp_setup_expired"].Text {
		t.Fatalf("enable without setup = %q", got)
	}
	securityNoticeText(t, postSecurity(t, app, handler, cookie, securityPagePath+"/totp/setup", url.Values{"current_password": {testAdminPassword}}))
	// The pending secret belongs to this session only.
	otherSession := mustLogin(t, handler, "admin", testAdminPassword)
	if strings.Contains(getWithCookie(handler, securityPagePath, otherSession).Body.String(), "data-totp-secret") {
		t.Fatal("another session must not see the pending secret")
	}
	securityNoticeText(t, postSecurity(t, app, handler, cookie, securityPagePath+"/totp/cancel", nil))
	if strings.Contains(getWithCookie(handler, securityPagePath, cookie).Body.String(), "data-totp-secret") {
		t.Fatal("cancel must drop the pending secret")
	}
	if app.currentCredentials().totpEnabled() {
		t.Fatal("cancelled setup must not enable 2FA")
	}
}

func TestTOTPCorruptSecretFailsClosed(t *testing.T) {
	store := credentialTestStore(t)
	if err := store.UpdateCredentialOverrides(context.Background(), map[string]string{storage.CredentialAdminTOTPSecret: "not-base32!"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := NewApp(credentialTestConfig(t), store, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), "test"); err == nil {
		t.Fatal("a corrupt 2FA secret must refuse to start instead of disabling 2FA")
	}
}
