package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

const (
	loginMaxFailures  = 10
	loginLockDuration = 15 * time.Minute
	loginAttemptTTL   = time.Hour
	// loginIPMaxFailures bounds failures per client IP regardless of the
	// submitted username, so rotating usernames cannot bypass the per-user
	// lock. It is higher than loginMaxFailures to tolerate a shared NAT.
	loginIPMaxFailures = 30
	// loginFailureDelay slows every failed password check (login and
	// sensitive admin operations alike).
	loginFailureDelay = 250 * time.Millisecond
)

// passwordHashSlots caps concurrent PBKDF2 computations process-wide. A
// single 600k-iteration check costs a noticeable amount of CPU, so floods
// of login attempts queue here instead of saturating every core.
var passwordHashSlots = make(chan struct{}, 2)

// passwordHashWait bounds how long a request queues for a hash slot before
// it is answered with "busy". A variable so tests can shorten it.
var passwordHashWait = 3 * time.Second

// errPasswordBusy means a password hash could not be computed right now:
// no global slot became free within passwordHashWait, or the same client
// IP already has a hash computation in flight.
var errPasswordBusy = errors.New("password hashing is busy")

// acquirePasswordHashSlot waits for a hash slot for at most
// passwordHashWait (or until ctx is done) and returns errPasswordBusy on
// timeout.
func acquirePasswordHashSlot(ctx context.Context) (release func(), err error) {
	ctx, cancel := context.WithTimeout(ctx, passwordHashWait)
	defer cancel()
	select {
	case passwordHashSlots <- struct{}{}:
		return func() { <-passwordHashSlots }, nil
	case <-ctx.Done():
		return nil, errPasswordBusy
	}
}

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
	// hashing marks client IPs with a password hash computation in flight;
	// each IP may run at most one at a time.
	hashing map[string]struct{}
	// trustedProxies (MUSIC_SERVER_TRUSTED_PROXIES) are the only peers
	// whose X-Forwarded-For header is believed. Empty means RemoteAddr is
	// always the client.
	trustedProxies []netip.Prefix

	// Persistent bans (login_bans.go), keyed by banKeyFor. banWriteMu
	// serializes ban/allowlist writes (memory + database) without holding
	// mu across database calls.
	banWriteMu sync.Mutex
	bans       map[string]storage.LoginBan
	envAllow   []netip.Prefix
	pageAllow  []allowEntry
	store      *storage.Store
	logger     *slog.Logger
	// proxyWarned makes warnUntrustedForwarder log only once.
	proxyWarned atomic.Bool
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{attempts: make(map[string]*loginAttempt), hashing: make(map[string]struct{}), bans: make(map[string]storage.LoginBan)}
}

// clientKey is the client's ban key (IPv4 address or IPv6 /64).
func (l *loginLimiter) clientKey(r *http.Request) string {
	return banKeyFor(l.clientIP(r))
}

func (l *loginLimiter) loginKey(r *http.Request, username string) string {
	return l.clientKey(r) + "|" + strings.ToLower(strings.TrimSpace(username))
}

// loginIPKey is the username-independent key. Hosts never contain "|",
// so it cannot collide with a loginKey.
func (l *loginLimiter) loginIPKey(r *http.Request) string {
	return l.clientKey(r)
}

func (l *loginLimiter) trusted(addr netip.Addr) bool {
	for _, prefix := range l.trustedProxies {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// clientIP returns the address used for rate limiting. X-Forwarded-For is
// consulted only when the direct peer is a trusted proxy; the client is
// then the rightmost entry that is not itself a trusted proxy (entries to
// its left are client-controlled and may be forged). Without trusted
// proxies the behavior is the RemoteAddr host, as before.
func (l *loginLimiter) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if len(l.trustedProxies) == 0 {
		return host
	}
	peer, err := netip.ParseAddr(host)
	if err != nil || !l.trusted(peer.Unmap()) {
		return host
	}
	var hops []string
	for _, header := range r.Header.Values("X-Forwarded-For") {
		for _, hop := range strings.Split(header, ",") {
			hops = append(hops, strings.TrimSpace(hop))
		}
	}
	client := host
	for i := len(hops) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(hops[i])
		if err != nil {
			// Unparseable hop: stop at the last address a trusted proxy
			// vouched for.
			return client
		}
		addr = addr.Unmap()
		client = addr.String()
		if !l.trusted(addr) {
			return client
		}
	}
	return client
}

// untrustedForwarder detects a likely misconfiguration: the request carries
// reverse-proxy forwarding headers, but its direct peer is not in
// MUSIC_SERVER_TRUSTED_PROXIES, so every client behind that proxy shares
// the peer's address for rate limiting and bans. peer is that address.
// (A direct client can fake the headers; that only shows the admin a
// warning on their own security page.)
func (l *loginLimiter) untrustedForwarder(r *http.Request) (peer string, ok bool) {
	if r.Header.Get("X-Forwarded-For") == "" && r.Header.Get("X-Real-IP") == "" && r.Header.Get("Forwarded") == "" {
		return "", false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if l.trusted(addr.Unmap()) {
			return "", false
		}
		host = addr.Unmap().String()
	}
	return host, true
}

// warnUntrustedForwarder logs the misconfiguration once per process.
func (l *loginLimiter) warnUntrustedForwarder(r *http.Request, logger *slog.Logger) {
	if l.proxyWarned.Load() {
		return
	}
	if peer, ok := l.untrustedForwarder(r); ok && l.proxyWarned.CompareAndSwap(false, true) {
		logger.Warn("admin login arrived through a reverse proxy that is not trusted: all clients behind it share one address for login rate limiting and bans; set MUSIC_SERVER_TRUSTED_PROXIES", "proxy", peer)
	}
}

// beginHash claims the per-IP hash slot; false means this client already
// has a hash computation running and the request must be rejected.
func (l *loginLimiter) beginHash(r *http.Request) (end func(), ok bool) {
	ip := l.clientIP(r)
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, busy := l.hashing[ip]; busy {
		return nil, false
	}
	l.hashing[ip] = struct{}{}
	return func() {
		l.mu.Lock()
		delete(l.hashing, ip)
		l.mu.Unlock()
	}, true
}

// lockedFor checks the IP+username key and, unless the client is
// allowlisted, the IP-only key and the persistent ban. It returns the
// longest remaining lock.
func (l *loginLimiter) lockedFor(r *http.Request, username string) (bool, time.Duration) {
	ip := l.clientIP(r)
	key := banKeyFor(ip)
	locked, remaining := l.locked(l.loginKey(r, username))
	if l.allowlisted(ip) {
		return locked, remaining
	}
	if ipLocked, ipRemaining := l.locked(key); ipLocked {
		locked, remaining = true, max(remaining, ipRemaining)
	}
	l.mu.Lock()
	banned, banRemaining := l.bannedLocked(key, time.Now())
	l.mu.Unlock()
	if banned {
		locked, remaining = true, max(remaining, banRemaining)
	}
	return locked, remaining
}

// recordFailureFor counts a failed check against both keys; tripping
// either lock earns the client a strike (an escalating persistent ban).
// Allowlisted clients only count against the IP+username key.
func (l *loginLimiter) recordFailureFor(r *http.Request, username string) {
	ip := l.clientIP(r)
	userLocked := l.recordFailureLimit(l.loginKey(r, username), loginMaxFailures)
	if l.allowlisted(ip) {
		return
	}
	ipLocked := l.recordFailureLimit(banKeyFor(ip), loginIPMaxFailures)
	if userLocked || ipLocked {
		l.strike(banKeyFor(ip))
	}
}

// recordSuccessFor clears both keys after a fully successful check, and
// forgives the client's strikes.
func (l *loginLimiter) recordSuccessFor(r *http.Request, username string) {
	l.recordSuccess(l.loginKey(r, username))
	l.recordSuccess(l.loginIPKey(r))
	key := l.clientKey(r)
	l.mu.Lock()
	_, banned := l.bans[key]
	l.mu.Unlock()
	if banned {
		if _, err := l.forgive(key); err != nil && l.logger != nil {
			l.logger.Error("clear login ban after successful login", "ip", key, "error", err)
		}
	}
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
	l.recordFailureLimit(key, loginMaxFailures)
}

// recordFailureLimit counts one failure and reports whether it tripped the
// lock.
func (l *loginLimiter) recordFailureLimit(key string, maxFailures int) bool {
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
	tripped := false
	if attempt.failures >= maxFailures {
		attempt.lockedUntil = now.Add(loginLockDuration)
		attempt.failures = 0
		tripped = true
	}
	l.prune(now)
	return tripped
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
