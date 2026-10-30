package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

// Persistent, escalating login bans.
//
// Every time a client trips a lockout (10 failures for IP+username or 30
// for the IP alone, see login_limit.go) it earns a strike. A strike bans
// the client's address for loginBanSchedule[strikes-1]; the bans survive
// restarts. Strikes are forgotten loginStrikeDecay after the last one, and
// a fully successful login from the address forgives them immediately.
//
// IPv6 clients are tracked per /64, the smallest block a single customer
// usually controls, so rotating addresses inside it does not evade bans.
//
// Allowlisted addresses (MUSIC_SERVER_LOGIN_ALLOWLIST plus the list on the
// security page) never earn strikes and skip the IP-only limit; the
// in-memory 15-minute IP+username lock still applies to them.
var loginBanSchedule = []time.Duration{
	15 * time.Minute,
	time.Hour,
	6 * time.Hour,
	24 * time.Hour,
	7 * 24 * time.Hour,
}

const (
	loginStrikeDecay = 30 * 24 * time.Hour
	// maxLoginAllowlist bounds the page-managed allowlist.
	maxLoginAllowlist   = 64
	maxLoginAllowNote   = 100
	minAllowPrefixBits4 = 8
	minAllowPrefixBits6 = 16
)

func loginBanDuration(strikes int) time.Duration {
	if strikes < 1 {
		strikes = 1
	}
	return loginBanSchedule[min(strikes, len(loginBanSchedule))-1]
}

// loginBanScheduleText renders the schedule for the security page.
func loginBanScheduleText() string {
	parts := make([]string, len(loginBanSchedule))
	for i, d := range loginBanSchedule {
		parts[i] = humanDuration(d)
	}
	return strings.Join(parts, " → ")
}

// humanDuration renders a lock duration in whole, rounded-up units.
func humanDuration(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟", int((d+time.Minute-1)/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d 小时", int((d+time.Hour-1)/time.Hour))
	default:
		return fmt.Sprintf("%d 天", int((d+24*time.Hour-1)/(24*time.Hour)))
	}
}

// banKeyFor maps a client address to its ban key: the IPv4 address, or
// the IPv6 /64 prefix. Unparseable hosts are used verbatim.
func banKeyFor(host string) string {
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	addr = addr.Unmap()
	if addr.Is4() {
		return addr.String()
	}
	prefix, err := addr.WithZone("").Prefix(64)
	if err != nil {
		return host
	}
	return prefix.String()
}

// allowEntry is a parsed page allowlist row.
type allowEntry struct {
	prefix netip.Prefix
	entry  storage.LoginAllowEntry
}

// parseAllowPrefix normalizes an allowlist value (IP or CIDR) and returns
// a security notice code when it is invalid.
func parseAllowPrefix(raw string) (netip.Prefix, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return netip.Prefix{}, "allow_invalid"
	}
	var prefix netip.Prefix
	if strings.Contains(raw, "/") {
		p, err := netip.ParsePrefix(raw)
		if err != nil {
			return netip.Prefix{}, "allow_invalid"
		}
		if p.Addr().Is4In6() {
			if p.Bits() < 96 {
				return netip.Prefix{}, "allow_invalid"
			}
			p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
		}
		prefix = p.Masked()
	} else {
		addr, err := netip.ParseAddr(raw)
		if err != nil || addr.Zone() != "" {
			return netip.Prefix{}, "allow_invalid"
		}
		addr = addr.Unmap()
		prefix = netip.PrefixFrom(addr, addr.BitLen())
	}
	if (prefix.Addr().Is4() && prefix.Bits() < minAllowPrefixBits4) || (prefix.Addr().Is6() && prefix.Bits() < minAllowPrefixBits6) {
		return netip.Prefix{}, "allow_too_broad"
	}
	return prefix, ""
}

// loadBans attaches persistence and loads the stored bans and allowlist.
// Without a store (unit tests) bans live in memory only.
func (l *loginLimiter) loadBans(ctx context.Context, store *storage.Store, envAllow []netip.Prefix, logger *slog.Logger) error {
	l.mu.Lock()
	l.store = store
	l.logger = logger
	l.envAllow = envAllow
	l.mu.Unlock()
	if store == nil {
		return nil
	}
	bans, err := store.LoginBans(ctx)
	if err != nil {
		return err
	}
	entries, err := store.LoginAllowlist(ctx)
	if err != nil {
		return err
	}
	var allow []allowEntry
	for _, entry := range entries {
		prefix, problem := parseAllowPrefix(entry.Prefix)
		if problem != "" {
			logger.Warn("stored login allowlist entry is invalid and was ignored", "prefix", entry.Prefix)
			continue
		}
		allow = append(allow, allowEntry{prefix: prefix, entry: entry})
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, ban := range bans {
		l.bans[ban.IP] = ban
	}
	l.pageAllow = allow
	return nil
}

// allowlistedLocked reports whether host is exempt from bans; l.mu held.
func (l *loginLimiter) allowlistedLocked(host string) bool {
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.Unmap().WithZone("")
	for _, prefix := range l.envAllow {
		if prefix.Contains(addr) {
			return true
		}
	}
	for _, entry := range l.pageAllow {
		if entry.prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func (l *loginLimiter) allowlisted(host string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.allowlistedLocked(host)
}

// bannedLocked returns the remaining ban for key; l.mu held.
func (l *loginLimiter) bannedLocked(key string, now time.Time) (bool, time.Duration) {
	ban, ok := l.bans[key]
	if !ok || !now.Before(ban.BannedUntil) {
		return false, 0
	}
	return true, ban.BannedUntil.Sub(now)
}

// strike records one lockout for key and bans it for the escalated
// duration. Writes are serialized by banWriteMu so the database never
// ends up with an older strike count than memory.
func (l *loginLimiter) strike(key string) {
	l.banWriteMu.Lock()
	defer l.banWriteMu.Unlock()
	now := time.Now()
	l.mu.Lock()
	ban, ok := l.bans[key]
	if !ok || now.Sub(ban.LastStrikeAt) > loginStrikeDecay {
		ban = storage.LoginBan{IP: key}
	}
	ban.Strikes++
	ban.LastStrikeAt = now
	ban.BannedUntil = now.Add(loginBanDuration(ban.Strikes))
	l.bans[key] = ban
	forgetBefore := now.Add(-loginStrikeDecay)
	for k, b := range l.bans {
		if b.BannedUntil.Before(forgetBefore) && b.LastStrikeAt.Before(forgetBefore) {
			delete(l.bans, k)
		}
	}
	store, logger := l.store, l.logger
	l.mu.Unlock()

	if logger != nil {
		logger.Warn("login address banned after repeated failures", "ip", key, "strikes", ban.Strikes, "duration", loginBanDuration(ban.Strikes).String(), "until", ban.BannedUntil.UTC().Format(time.RFC3339))
	}
	if store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.SaveLoginBan(ctx, ban, forgetBefore); err != nil && logger != nil {
		// The ban still holds in memory until the next restart.
		logger.Error("persist login ban", "ip", key, "error", err)
	}
}

// forgive drops key's ban and strikes (a full login succeeded, or the
// admin lifted the ban) and clears its in-memory failure counters. It
// reports whether a ban existed.
func (l *loginLimiter) forgive(key string) (bool, error) {
	l.banWriteMu.Lock()
	defer l.banWriteMu.Unlock()
	l.mu.Lock()
	_, existed := l.bans[key]
	delete(l.bans, key)
	delete(l.attempts, key)
	for k := range l.attempts {
		if strings.HasPrefix(k, key+"|") {
			delete(l.attempts, k)
		}
	}
	store := l.store
	l.mu.Unlock()
	if !existed || store == nil {
		return existed, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return true, store.DeleteLoginBan(ctx, key)
}

// activeBans returns the bans still in force, longest remaining first.
func (l *loginLimiter) activeBans(now time.Time) []storage.LoginBan {
	l.mu.Lock()
	defer l.mu.Unlock()
	var bans []storage.LoginBan
	for _, ban := range l.bans {
		if now.Before(ban.BannedUntil) {
			bans = append(bans, ban)
		}
	}
	slices.SortFunc(bans, func(a, b storage.LoginBan) int { return b.BannedUntil.Compare(a.BannedUntil) })
	return bans
}

func (l *loginLimiter) allowlists() (env []netip.Prefix, page []storage.LoginAllowEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	env = slices.Clone(l.envAllow)
	for _, entry := range l.pageAllow {
		page = append(page, entry.entry)
	}
	return env, page
}

// addAllow persists and publishes a page allowlist entry. It returns a
// notice code on refusal.
func (l *loginLimiter) addAllow(ctx context.Context, prefix netip.Prefix, note string) (string, error) {
	l.banWriteMu.Lock()
	defer l.banWriteMu.Unlock()
	l.mu.Lock()
	if len(l.pageAllow) >= maxLoginAllowlist {
		l.mu.Unlock()
		return "allow_full", nil
	}
	for _, entry := range l.pageAllow {
		if entry.prefix == prefix {
			l.mu.Unlock()
			return "allow_exists", nil
		}
	}
	store := l.store
	l.mu.Unlock()
	entry := storage.LoginAllowEntry{Prefix: prefix.String(), Note: note, CreatedAt: time.Now()}
	if store != nil {
		if err := store.AddLoginAllowlist(ctx, entry); err != nil {
			return "", err
		}
	}
	l.mu.Lock()
	l.pageAllow = append(l.pageAllow, allowEntry{prefix: prefix, entry: entry})
	l.mu.Unlock()
	return "", nil
}

// removeAllow deletes a page allowlist entry; false when it did not exist.
func (l *loginLimiter) removeAllow(ctx context.Context, prefix netip.Prefix) (bool, error) {
	l.banWriteMu.Lock()
	defer l.banWriteMu.Unlock()
	l.mu.Lock()
	index := slices.IndexFunc(l.pageAllow, func(e allowEntry) bool { return e.prefix == prefix })
	store := l.store
	l.mu.Unlock()
	if index < 0 {
		return false, nil
	}
	if store != nil {
		if err := store.DeleteLoginAllowlist(ctx, l.pageAllow[index].entry.Prefix); err != nil {
			return false, err
		}
	}
	l.mu.Lock()
	l.pageAllow = slices.DeleteFunc(l.pageAllow, func(e allowEntry) bool { return e.prefix == prefix })
	l.mu.Unlock()
	return true, nil
}
