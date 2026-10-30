package httpapi

import (
	"errors"
	"html/template"
	"net/http"
	"sync"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

// Two-factor admin login.
//
// POST /admin/login checks username + password as before. When a TOTP
// secret is configured it does NOT create a session; it creates a
// short-lived login challenge (random token in an HttpOnly cookie scoped
// to /admin/login) and redirects to /admin/login/2fa, where the code is
// entered. Only a correct, unused code turns the challenge into a session.
//
// Brute-force bounds: a correct password does not reset the login limiter
// while 2FA is on (only a full login does), every wrong code counts as a
// failed login for the same IP+username / IP keys, and each challenge
// allows at most loginChallengeMaxAttempts codes.
const (
	adminMFACookie            = "music_server_admin_mfa"
	adminMFACookiePath        = "/admin/login"
	totpLoginPath             = "/admin/login/2fa"
	loginChallengeTTL         = 5 * time.Minute
	loginChallengeMaxAttempts = 5
	// loginChallengeCapacity bounds memory; challenges only exist after a
	// correct password, so this is never reached in normal use.
	loginChallengeCapacity = 256
	// totpSetupTTL is how long an enrollment secret waits for its
	// confirmation code.
	totpSetupTTL = 10 * time.Minute
)

type loginChallenge struct {
	username   string
	totpSecret string
	generation uint64
	next       string
	expiresAt  time.Time
	attempts   int
}

// loginChallengeStore is zero-value usable. Keys are SHA-256 hashes of the
// cookie token.
type loginChallengeStore struct {
	mu         sync.Mutex
	challenges map[string]*loginChallenge
}

var errLoginChallengesFull = errors.New("too many pending two-factor logins")

func (s *loginChallengeStore) pruneLocked(now time.Time) {
	for key, challenge := range s.challenges {
		if !challenge.expiresAt.After(now) {
			delete(s.challenges, key)
		}
	}
}

func (s *loginChallengeStore) create(challenge loginChallenge) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	challenge.expiresAt = time.Now().Add(loginChallengeTTL)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.challenges == nil {
		s.challenges = make(map[string]*loginChallenge)
	}
	s.pruneLocked(time.Now())
	if len(s.challenges) >= loginChallengeCapacity {
		return "", errLoginChallengesFull
	}
	s.challenges[sessionTokenHash(token)] = &challenge
	return token, nil
}

// peek returns the challenge without consuming an attempt.
func (s *loginChallengeStore) peek(token string) (loginChallenge, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	challenge, ok := s.challenges[sessionTokenHash(token)]
	if !ok || !challenge.expiresAt.After(time.Now()) {
		return loginChallenge{}, false
	}
	return *challenge, true
}

// beginAttempt reserves one code attempt (under the lock, so parallel
// requests cannot exceed the limit) and reports the attempts left after
// this one. An exhausted or expired challenge is deleted.
func (s *loginChallengeStore) beginAttempt(token string) (loginChallenge, int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sessionTokenHash(token)
	challenge, ok := s.challenges[key]
	if !ok {
		return loginChallenge{}, 0, false
	}
	if !challenge.expiresAt.After(time.Now()) || challenge.attempts >= loginChallengeMaxAttempts {
		delete(s.challenges, key)
		return loginChallenge{}, 0, false
	}
	challenge.attempts++
	return *challenge, loginChallengeMaxAttempts - challenge.attempts, true
}

// take consumes the challenge; only one caller can succeed.
func (s *loginChallengeStore) take(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sessionTokenHash(token)
	_, ok := s.challenges[key]
	delete(s.challenges, key)
	return ok
}

func (s *loginChallengeStore) discard(token string) { s.take(token) }

func (a *App) setMFACookie(w http.ResponseWriter, token string) {
	maxAge := int(loginChallengeTTL.Seconds())
	if token == "" {
		maxAge = -1
	}
	http.SetCookie(w, &http.Cookie{
		Name:     adminMFACookie,
		Value:    token,
		Path:     adminMFACookiePath,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   a.sessions.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func mfaToken(r *http.Request) string {
	cookie, err := r.Cookie(adminMFACookie)
	if err != nil {
		return ""
	}
	return cookie.Value
}

// startTOTPChallenge is called by handleLogin after a correct password when
// two-factor login is enabled.
func (a *App) startTOTPChallenge(w http.ResponseWriter, r *http.Request, creds *credentials, generation uint64, next string) {
	if old := mfaToken(r); old != "" {
		a.loginChallenges.discard(old)
	}
	token, err := a.loginChallenges.create(loginChallenge{username: creds.username, totpSecret: creds.totpSecret, generation: generation, next: next})
	if err != nil {
		if !errors.Is(err, errLoginChallengesFull) {
			a.logger.Error("create two-factor login challenge", "error", err)
		}
		http.Redirect(w, r, loginPath(next, "busy"), http.StatusSeeOther)
		return
	}
	a.setMFACookie(w, token)
	http.Redirect(w, r, totpLoginPath, http.StatusSeeOther)
}

func (a *App) renderTOTPLogin(w http.ResponseWriter, status int, message string) {
	setNoStore(w)
	a.render(w, status, "login.html", loginPageData{TOTP: true, Error: message})
}

func (a *App) handleTOTPLoginPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.sessions.get(r); ok {
		http.Redirect(w, r, afterLoginPath(""), http.StatusSeeOther)
		return
	}
	if _, ok := a.loginChallenges.peek(mfaToken(r)); !ok {
		a.setMFACookie(w, "")
		http.Redirect(w, r, loginPath("", "mfa_expired"), http.StatusSeeOther)
		return
	}
	a.renderTOTPLogin(w, http.StatusOK, "")
}

func (a *App) handleTOTPLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		a.renderTOTPLogin(w, http.StatusBadRequest, "无法读取验证码。")
		return
	}
	token := mfaToken(r)
	pending, ok := a.loginChallenges.peek(token)
	if !ok {
		a.setMFACookie(w, "")
		http.Redirect(w, r, loginPath("", "mfa_expired"), http.StatusSeeOther)
		return
	}
	if locked, _ := a.loginLimiter.lockedFor(r, pending.username); locked {
		a.loginChallenges.discard(token)
		a.setMFACookie(w, "")
		a.logger.Warn("two-factor login attempt while locked out", "usernameHash", loginUsernameLogID(pending.username), "remoteAddr", r.RemoteAddr)
		http.Redirect(w, r, loginPath(pending.next, "locked"), http.StatusSeeOther)
		return
	}
	challenge, remaining, ok := a.loginChallenges.beginAttempt(token)
	if !ok {
		a.setMFACookie(w, "")
		http.Redirect(w, r, loginPath(pending.next, "mfa_expired"), http.StatusSeeOther)
		return
	}
	// 2FA was disabled or re-enrolled since the password step: start over
	// (createAt would refuse the old generation anyway).
	if a.currentCredentials().totpSecret != challenge.totpSecret {
		a.loginChallenges.discard(token)
		a.setMFACookie(w, "")
		http.Redirect(w, r, loginPath(challenge.next, "retry"), http.StatusSeeOther)
		return
	}
	if !a.totpReplay.verify(challenge.totpSecret, r.PostFormValue("code"), time.Now()) {
		a.loginLimiter.recordFailureFor(r, challenge.username)
		time.Sleep(loginFailureDelay)
		a.logger.Warn("two-factor login rejected: wrong code", "usernameHash", loginUsernameLogID(challenge.username), "remoteAddr", r.RemoteAddr)
		if remaining == 0 {
			a.loginChallenges.discard(token)
			a.setMFACookie(w, "")
			http.Redirect(w, r, loginPath(challenge.next, "mfa_expired"), http.StatusSeeOther)
			return
		}
		a.renderTOTPLogin(w, http.StatusUnauthorized, "验证码不正确或已使用，请等待下一个验证码后重试。")
		return
	}
	a.setMFACookie(w, "")
	if !a.loginChallenges.take(token) {
		http.Redirect(w, r, loginPath(challenge.next, "retry"), http.StatusSeeOther)
		return
	}
	a.loginLimiter.recordSuccessFor(r, challenge.username)
	a.finishLogin(w, r, challenge.username, challenge.generation, challenge.next)
}

// totpSetup is a secret being enrolled from the security page.
type totpSetup struct {
	secret string
	// expected is the credential snapshot the current password was
	// verified against when the setup started.
	expected  *credentials
	expiresAt time.Time
}

// totpSetupView is the template contract for a pending enrollment.
type totpSetupView struct {
	Secret string
	// QRImage is a PNG data URI produced by totpQRDataURI (trusted, so it
	// is typed to pass html/template's URL sanitizer).
	QRImage template.URL
}

func (m *sessionManager) setTOTPSetup(r *http.Request, setup totpSetup) bool {
	cookie, err := r.Cookie(adminSessionCookie)
	if err != nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[cookie.Value]; !ok {
		return false
	}
	m.totpSetups[cookie.Value] = setup
	return true
}

func (m *sessionManager) totpSetupFor(r *http.Request) (totpSetup, bool) {
	cookie, err := r.Cookie(adminSessionCookie)
	if err != nil {
		return totpSetup{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	setup, ok := m.totpSetups[cookie.Value]
	if ok && !setup.expiresAt.After(time.Now()) {
		delete(m.totpSetups, cookie.Value)
		return totpSetup{}, false
	}
	return setup, ok
}

func (m *sessionManager) clearTOTPSetup(r *http.Request) {
	if cookie, err := r.Cookie(adminSessionCookie); err == nil {
		m.mu.Lock()
		delete(m.totpSetups, cookie.Value)
		m.mu.Unlock()
	}
}

// handleSecurityTOTPSetup starts enrollment: after the current password is
// verified a new secret is kept for this session only and shown (QR code +
// manual key) on the security page until confirmed or expired.
func (a *App) handleSecurityTOTPSetup(w http.ResponseWriter, r *http.Request) {
	creds, ok := a.beginSensitiveOperation(w, r, "totp_setup")
	if !ok {
		return
	}
	if creds.totpEnabled() {
		securityRedirect(w, r, "totp_already")
		return
	}
	secret, err := generateTOTPSecret()
	if err != nil {
		a.logger.Error("generate two-factor secret", "error", err)
		securityRedirect(w, r, "save_failed")
		return
	}
	if !a.sessions.setTOTPSetup(r, totpSetup{secret: secret, expected: creds, expiresAt: time.Now().Add(totpSetupTTL)}) {
		securityRedirect(w, r, "save_failed")
		return
	}
	securityRedirect(w, r, "totp_setup_started")
}

// handleSecurityTOTPEnable confirms enrollment with a code from the new
// secret, then saves it like a password change (other sessions revoked).
func (a *App) handleSecurityTOTPEnable(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	setup, ok := a.sessions.totpSetupFor(r)
	if !ok {
		securityRedirect(w, r, "totp_setup_expired")
		return
	}
	if !a.totpReplay.verify(setup.secret, r.PostFormValue("code"), time.Now()) {
		time.Sleep(loginFailureDelay)
		securityRedirect(w, r, "totp_code_wrong")
		return
	}
	a.sessions.clearTOTPSetup(r)
	a.saveLoginCredentials(w, r, "totp_enable", setup.expected, map[string]string{storage.CredentialAdminTOTPSecret: setup.secret}, nil, "totp_enabled")
}

func (a *App) handleSecurityTOTPCancel(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	a.sessions.clearTOTPSetup(r)
	securityRedirect(w, r, "totp_setup_cancelled")
}

// handleSecurityTOTPDisable requires the current password AND a current
// code. The limiter is cleared only when both are correct, so a stolen
// session plus password cannot brute-force the code.
func (a *App) handleSecurityTOTPDisable(w http.ResponseWriter, r *http.Request) {
	creds, ok := a.beginSensitiveOperationOpts(w, r, "totp_disable", securityPagePath, false)
	if !ok {
		return
	}
	if !creds.totpEnabled() {
		securityRedirect(w, r, "totp_not_enabled")
		return
	}
	if !a.totpReplay.verify(creds.totpSecret, r.PostFormValue("code"), time.Now()) {
		a.loginLimiter.recordFailureFor(r, creds.username)
		time.Sleep(loginFailureDelay)
		a.logger.Warn("two-factor disable rejected: wrong code", "session", sessionLogID(r), "remoteAddr", r.RemoteAddr)
		securityRedirect(w, r, "totp_code_wrong")
		return
	}
	a.loginLimiter.recordSuccessFor(r, creds.username)
	a.saveLoginCredentials(w, r, "totp_disable", creds, nil, []string{storage.CredentialAdminTOTPSecret}, "totp_disabled")
}
