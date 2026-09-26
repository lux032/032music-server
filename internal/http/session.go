package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

const adminSessionCookie = "music_server_admin_session"

type adminSession struct {
	Username  string
	CSRFToken string
	ExpiresAt time.Time
}

// sessionManager persists sessions in SQLite (M11) so restarts do not log
// everyone out. Only the SHA-256 hash of the cookie token is stored; the
// in-memory map is a write-through cache keyed by the raw token.
type sessionManager struct {
	mu           sync.Mutex
	sessions     map[string]adminSession
	store        *storage.Store
	cookieSecure bool
	lifetime     time.Duration
	// generation increases whenever sessions are revoked in bulk (a
	// username/password change); logins created against an older
	// generation are refused (see createAt).
	generation uint64
	// revocations increases on every revocation, bulk or single (logout).
	// get() reads the database outside mu, so it only caches a persisted
	// session when no revocation happened in between; otherwise a
	// concurrent lookup could resurrect a session that was just deleted.
	revocations uint64
	// afterPersistedLookup is a test hook run by get() right after a
	// cache-miss database read; nil in production.
	afterPersistedLookup func()
	// flashes holds one-shot security notices (e.g. a freshly generated
	// token) keyed by raw session token. They live only in memory and are
	// never put into URLs or logs.
	flashes map[string]securityFlash
}

// securityFlash is shown once on the next GET of the security page and
// deleted immediately after being read.
type securityFlash struct {
	Label string
	Value string
}

func newSessionManager(cookieSecure bool, store *storage.Store) *sessionManager {
	return &sessionManager{
		sessions:     make(map[string]adminSession),
		store:        store,
		cookieSecure: cookieSecure,
		lifetime:     8 * time.Hour,
		flashes:      make(map[string]securityFlash),
	}
}

func sessionTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// errSessionsRevoked is returned by createAt when sessions were revoked in
// bulk (a username/password change) after the caller read the credentials
// it authenticated against. The login must be retried.
var errSessionsRevoked = errors.New("admin sessions were revoked during login")

// currentGeneration returns the bulk-revocation counter. A login must read
// it before reading the credentials it checks, and pass it to createAt.
func (m *sessionManager) currentGeneration() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.generation
}

func (m *sessionManager) create(w http.ResponseWriter, username string) (adminSession, error) {
	return m.createAt(w, username, m.currentGeneration())
}

// createAt creates a session only if no bulk revocation happened since
// generation was read. The generation check, the database insert and the
// cache insert all happen under mu, so a concurrent credential change
// either runs entirely before (and createAt fails) or entirely after (and
// its revocation deletes the new session).
func (m *sessionManager) createAt(w http.ResponseWriter, username string, generation uint64) (adminSession, error) {
	sessionToken, err := randomToken()
	if err != nil {
		return adminSession{}, err
	}
	csrfToken, err := randomToken()
	if err != nil {
		return adminSession{}, err
	}

	session := adminSession{
		Username:  username,
		CSRFToken: csrfToken,
		ExpiresAt: time.Now().Add(m.lifetime),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m.mu.Lock()
	if m.generation != generation {
		m.mu.Unlock()
		return adminSession{}, errSessionsRevoked
	}
	if err = m.store.CreateAdminSession(ctx, storage.AdminSession{TokenHash: sessionTokenHash(sessionToken), Username: session.Username, CSRFToken: session.CSRFToken, ExpiresAt: session.ExpiresAt}); err != nil {
		m.mu.Unlock()
		return adminSession{}, err
	}
	_ = m.store.DeleteExpiredAdminSessions(ctx, time.Now())
	m.removeExpiredLocked(time.Now())
	m.sessions[sessionToken] = session
	m.mu.Unlock()

	m.setCookie(w, sessionToken)
	return session, nil
}

func (m *sessionManager) setCookie(w http.ResponseWriter, sessionToken string) {
	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookie,
		Value:    sessionToken,
		Path:     "/",
		MaxAge:   int(m.lifetime.Seconds()),
		HttpOnly: true,
		Secure:   m.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

// replaceAll keeps only the caller's session alive after a username or
// password change. Under mu it:
//  1. calls commit with the rotated session; commit must persist the
//     credential change AND replace all persisted sessions with keep in one
//     database transaction;
//  2. on success calls publish (swap the credential pointer) — before the
//     generation bump, so a login that read the old credentials always
//     holds the old generation and fails in createAt;
//  3. bumps the generation, clears the cache and flashes, caches the new
//     session and sets the new cookie (new token + new CSRF token, which
//     prevents session fixation).
//
// If commit fails nothing changes: credentials, sessions and the cookie
// stay as they were.
func (m *sessionManager) replaceAll(w http.ResponseWriter, username string, commit func(keep storage.AdminSession) error, publish func()) error {
	sessionToken, err := randomToken()
	if err != nil {
		return err
	}
	csrfToken, err := randomToken()
	if err != nil {
		return err
	}
	session := adminSession{Username: username, CSRFToken: csrfToken, ExpiresAt: time.Now().Add(m.lifetime)}

	m.mu.Lock()
	defer m.mu.Unlock()
	if err := commit(storage.AdminSession{TokenHash: sessionTokenHash(sessionToken), Username: session.Username, CSRFToken: session.CSRFToken, ExpiresAt: session.ExpiresAt}); err != nil {
		return err
	}
	publish()
	m.generation++
	m.revocations++
	clear(m.sessions)
	clear(m.flashes)
	m.sessions[sessionToken] = session
	m.setCookie(w, sessionToken)
	return nil
}

// setFlash stores a one-shot notice on the request's session. It reports
// false when the session is no longer cached (e.g. revoked meanwhile).
func (m *sessionManager) setFlash(r *http.Request, flash securityFlash) bool {
	cookie, err := r.Cookie(adminSessionCookie)
	if err != nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[cookie.Value]; !ok {
		return false
	}
	m.flashes[cookie.Value] = flash
	return true
}

// takeFlash returns and deletes the request session's one-shot notice.
func (m *sessionManager) takeFlash(r *http.Request) (securityFlash, bool) {
	cookie, err := r.Cookie(adminSessionCookie)
	if err != nil {
		return securityFlash{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	flash, ok := m.flashes[cookie.Value]
	delete(m.flashes, cookie.Value)
	return flash, ok
}

func (m *sessionManager) get(r *http.Request) (adminSession, bool) {
	cookie, err := r.Cookie(adminSessionCookie)
	if err != nil {
		return adminSession{}, false
	}

	// A cache miss reads the database outside mu. If any session was
	// revoked meanwhile (logout or bulk revocation, both of which delete
	// the row under mu before bumping revocations), the read may be stale:
	// read again rather than caching a session that no longer exists.
	for attempt := 0; attempt < 3; attempt++ {
		now := time.Now()
		m.mu.Lock()
		m.removeExpiredLocked(now)
		session, ok := m.sessions[cookie.Value]
		revocations := m.revocations
		m.mu.Unlock()
		if ok {
			return session, true
		}

		// Cache miss: fall back to the persisted session (post-restart).
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		persisted, err := m.store.AdminSessionByTokenHash(ctx, sessionTokenHash(cookie.Value))
		cancel()
		if m.afterPersistedLookup != nil {
			m.afterPersistedLookup()
		}
		if err != nil || !persisted.ExpiresAt.After(now) {
			return adminSession{}, false
		}
		session = adminSession{Username: persisted.Username, CSRFToken: persisted.CSRFToken, ExpiresAt: persisted.ExpiresAt}
		m.mu.Lock()
		if m.revocations == revocations {
			m.sessions[cookie.Value] = session
			m.mu.Unlock()
			return session, true
		}
		m.mu.Unlock()
	}
	return adminSession{}, false
}

// delete logs the request's session out. The database row and the cache
// entry are removed under mu and revocations is bumped, so a concurrent
// cache-miss lookup that already read the row cannot resurrect it.
func (m *sessionManager) delete(w http.ResponseWriter, r *http.Request) error {
	if cookie, err := r.Cookie(adminSessionCookie); err == nil {
		m.mu.Lock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = m.store.DeleteAdminSession(ctx, sessionTokenHash(cookie.Value))
		cancel()
		if err != nil {
			m.mu.Unlock()
			slog.Warn("delete admin session failed", "error", err)
			return err
		}
		delete(m.sessions, cookie.Value)
		delete(m.flashes, cookie.Value)
		m.revocations++
		m.mu.Unlock()
	}

	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   m.cookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
	return nil
}

func (m *sessionManager) removeExpiredLocked(now time.Time) {
	for token, session := range m.sessions {
		if !session.ExpiresAt.After(now) {
			delete(m.sessions, token)
			delete(m.flashes, token)
		}
	}
}

func randomToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}
