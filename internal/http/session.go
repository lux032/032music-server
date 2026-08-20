package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
}

func newSessionManager(cookieSecure bool, store *storage.Store) *sessionManager {
	return &sessionManager{
		sessions:     make(map[string]adminSession),
		store:        store,
		cookieSecure: cookieSecure,
		lifetime:     8 * time.Hour,
	}
}

func sessionTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (m *sessionManager) create(w http.ResponseWriter, username string) (adminSession, error) {
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
	if err = m.store.CreateAdminSession(ctx, storage.AdminSession{TokenHash: sessionTokenHash(sessionToken), Username: session.Username, CSRFToken: session.CSRFToken, ExpiresAt: session.ExpiresAt}); err != nil {
		return adminSession{}, err
	}
	_ = m.store.DeleteExpiredAdminSessions(ctx, time.Now())

	m.mu.Lock()
	m.removeExpiredLocked(time.Now())
	m.sessions[sessionToken] = session
	m.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookie,
		Value:    sessionToken,
		Path:     "/",
		MaxAge:   int(m.lifetime.Seconds()),
		HttpOnly: true,
		Secure:   m.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})

	return session, nil
}

func (m *sessionManager) get(r *http.Request) (adminSession, bool) {
	cookie, err := r.Cookie(adminSessionCookie)
	if err != nil {
		return adminSession{}, false
	}

	now := time.Now()
	m.mu.Lock()
	m.removeExpiredLocked(now)
	session, ok := m.sessions[cookie.Value]
	m.mu.Unlock()
	if ok {
		return session, true
	}

	// Cache miss: fall back to the persisted session (post-restart).
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	persisted, err := m.store.AdminSessionByTokenHash(ctx, sessionTokenHash(cookie.Value))
	if err != nil || !persisted.ExpiresAt.After(now) {
		return adminSession{}, false
	}
	session = adminSession{Username: persisted.Username, CSRFToken: persisted.CSRFToken, ExpiresAt: persisted.ExpiresAt}
	m.mu.Lock()
	m.sessions[cookie.Value] = session
	m.mu.Unlock()
	return session, true
}

func (m *sessionManager) delete(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(adminSessionCookie); err == nil {
		m.mu.Lock()
		delete(m.sessions, cookie.Value)
		m.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = m.store.DeleteAdminSession(ctx, sessionTokenHash(cookie.Value))
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
}

func (m *sessionManager) removeExpiredLocked(now time.Time) {
	for token, session := range m.sessions {
		if !session.ExpiresAt.After(now) {
			delete(m.sessions, token)
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
