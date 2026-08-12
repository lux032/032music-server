package httpapi

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"sync"
	"time"
)

const adminSessionCookie = "music_server_admin_session"

type adminSession struct {
	Username  string
	CSRFToken string
	ExpiresAt time.Time
}

type sessionManager struct {
	mu           sync.Mutex
	sessions     map[string]adminSession
	cookieSecure bool
	lifetime     time.Duration
}

func newSessionManager(cookieSecure bool) *sessionManager {
	return &sessionManager{
		sessions:     make(map[string]adminSession),
		cookieSecure: cookieSecure,
		lifetime:     8 * time.Hour,
	}
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
		SameSite: http.SameSiteStrictMode,
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
	defer m.mu.Unlock()
	m.removeExpiredLocked(now)
	session, ok := m.sessions[cookie.Value]
	if !ok || !session.ExpiresAt.After(now) {
		return adminSession{}, false
	}
	return session, true
}

func (m *sessionManager) delete(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(adminSessionCookie); err == nil {
		m.mu.Lock()
		delete(m.sessions, cookie.Value)
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
