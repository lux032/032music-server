package httpapi

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	loginMaxFailures  = 10
	loginLockDuration = 15 * time.Minute
	loginAttemptTTL   = time.Hour
)

type loginAttempt struct {
	failures    int
	lockedUntil time.Time
	lastSeen    time.Time
}

// loginLimiter throttles admin login attempts per client IP + username.
// After loginMaxFailures consecutive failures the key is locked for
// loginLockDuration. State is in-memory only; a restart resets it, which is
// acceptable for a single-admin panel.
type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string]*loginAttempt
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{attempts: make(map[string]*loginAttempt)}
}

func loginKey(r *http.Request, username string) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return host + "|" + strings.ToLower(strings.TrimSpace(username))
}

// locked reports whether the key is currently locked out, and the remaining
// lock duration.
func (l *loginLimiter) locked(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	attempt, ok := l.attempts[key]
	if !ok {
		return false, 0
	}
	now := time.Now()
	if now.Before(attempt.lockedUntil) {
		return true, attempt.lockedUntil.Sub(now)
	}
	return false, 0
}

func (l *loginLimiter) recordFailure(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	attempt, ok := l.attempts[key]
	if !ok {
		attempt = &loginAttempt{}
		l.attempts[key] = attempt
	}
	attempt.failures++
	attempt.lastSeen = now
	if attempt.failures >= loginMaxFailures {
		attempt.lockedUntil = now.Add(loginLockDuration)
		attempt.failures = 0
	}
	l.prune(now)
}

func (l *loginLimiter) recordSuccess(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
}

// prune drops stale entries so the map cannot grow unbounded under
// password-spraying from many IPs. Called with mu held.
func (l *loginLimiter) prune(now time.Time) {
	if len(l.attempts) < 1024 {
		return
	}
	for key, attempt := range l.attempts {
		if now.After(attempt.lockedUntil) && now.Sub(attempt.lastSeen) > loginAttemptTTL {
			delete(l.attempts, key)
		}
	}
}
