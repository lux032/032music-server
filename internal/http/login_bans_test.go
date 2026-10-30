package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

func requestFrom(remote string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/admin/login", nil)
	r.RemoteAddr = remote
	return r
}

func tripLock(l *loginLimiter, remote string) {
	for i := 0; i < loginMaxFailures; i++ {
		l.recordFailureFor(requestFrom(remote), "admin")
	}
}

// expireLocks ends the in-memory locks and the current ban of key so the
// next lockout counts as a repeat offence.
func expireLocks(l *loginLimiter, key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, attempt := range l.attempts {
		if k == key || strings.HasPrefix(k, key+"|") {
			attempt.lockedUntil = time.Time{}
		}
	}
	ban := l.bans[key]
	ban.BannedUntil = time.Now().Add(-time.Second)
	l.bans[key] = ban
}

func storedBan(t *testing.T, store *storage.Store, key string) (storage.LoginBan, bool) {
	t.Helper()
	bans, err := store.LoginBans(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, ban := range bans {
		if ban.IP == key {
			return ban, true
		}
	}
	return storage.LoginBan{}, false
}

func TestLoginBanEscalatesAndPersists(t *testing.T) {
	app, store, handler := credentialTestApp(t)
	const remote = "203.0.113.50:4000"
	limiter := app.loginLimiter
	for strike := 1; strike <= len(loginBanSchedule)+1; strike++ {
		tripLock(limiter, remote)
		locked, remaining := limiter.lockedFor(requestFrom(remote), "someone-else")
		want := loginBanDuration(strike)
		if !locked || remaining < want-time.Minute || remaining > want {
			t.Fatalf("strike %d: locked=%v remaining=%s, want ~%s", strike, locked, remaining, want)
		}
		ban, ok := storedBan(t, store, "203.0.113.50")
		if !ok || ban.Strikes != strike {
			t.Fatalf("strike %d: stored ban = %+v %v", strike, ban, ok)
		}
		if strike <= len(loginBanSchedule) {
			expireLocks(limiter, "203.0.113.50")
		}
	}
	if loginBanDuration(99) != 7*24*time.Hour {
		t.Fatal("the schedule must cap at its last step")
	}

	// A restart keeps the ban (the in-memory counters are gone).
	restarted := newCredentialTestApp(t, credentialTestConfig(t), store)
	if locked, _ := restarted.loginLimiter.lockedFor(requestFrom(remote), "admin"); !locked {
		t.Fatal("the ban must survive a restart")
	}
	if locked, _ := restarted.loginLimiter.lockedFor(requestFrom("198.51.100.1:1"), "admin"); locked {
		t.Fatal("other addresses must not be affected")
	}
	_ = handler
}

func TestLoginBanLoginPageAndForgiveOnSuccess(t *testing.T) {
	app, store, handler := credentialTestApp(t)
	// httptest requests come from 192.0.2.1.
	tripLock(app.loginLimiter, "192.0.2.1:1234")
	if _, ok := storedBan(t, store, "192.0.2.1"); !ok {
		t.Fatal("ban must be stored")
	}
	if _, status := loginAs(t, handler, "admin", testAdminPassword); status != http.StatusTooManyRequests {
		t.Fatalf("banned login = %d", status)
	}
	expireLocks(app.loginLimiter, "192.0.2.1")
	mustLogin(t, handler, "admin", testAdminPassword)
	if _, ok := storedBan(t, store, "192.0.2.1"); ok {
		t.Fatal("a successful login must forgive the address")
	}
	app.loginLimiter.mu.Lock()
	_, inMemory := app.loginLimiter.bans["192.0.2.1"]
	app.loginLimiter.mu.Unlock()
	if inMemory {
		t.Fatal("forgiven ban must leave memory too")
	}
}

func TestLoginBanStrikesDecay(t *testing.T) {
	limiter := newLoginLimiter()
	limiter.bans["203.0.113.9"] = storage.LoginBan{IP: "203.0.113.9", Strikes: 4, BannedUntil: time.Now().Add(-40 * 24 * time.Hour), LastStrikeAt: time.Now().Add(-31 * 24 * time.Hour)}
	tripLock(limiter, "203.0.113.9:1")
	if got := limiter.bans["203.0.113.9"].Strikes; got != 1 {
		t.Fatalf("strikes after decay = %d, want 1", got)
	}
}

func TestLoginBanIPv6GroupsBy64(t *testing.T) {
	limiter := newLoginLimiter()
	for i := 0; i < loginIPMaxFailures; i++ {
		// Rotating usernames and addresses inside one /64.
		limiter.recordFailureFor(requestFrom("[2001:db8:1:2::"+string(rune('a'+i%6))+"]:443"), "user"+string(rune('a'+i)))
	}
	if locked, _ := limiter.lockedFor(requestFrom("[2001:db8:1:2:ffff::1]:443"), "admin"); !locked {
		t.Fatal("the whole /64 must be banned")
	}
	if locked, _ := limiter.lockedFor(requestFrom("[2001:db8:1:3::1]:443"), "admin"); locked {
		t.Fatal("a neighbouring /64 must not be banned")
	}
	if _, ok := limiter.bans["2001:db8:1:2::/64"]; !ok {
		t.Fatalf("bans = %v", limiter.bans)
	}
	if got := banKeyFor("::ffff:203.0.113.1"); got != "203.0.113.1" {
		t.Fatalf("mapped IPv4 key = %q", got)
	}
}

func TestLoginBanEnvAllowlist(t *testing.T) {
	store := credentialTestStore(t)
	cfg := credentialTestConfig(t)
	cfg.LoginAllowlist = "192.168.1.0/24"
	app := newCredentialTestApp(t, cfg, store)
	limiter := app.loginLimiter
	for i := 0; i < loginIPMaxFailures+5; i++ {
		limiter.recordFailureFor(requestFrom("192.168.1.20:1"), "user"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	if locked, _ := limiter.lockedFor(requestFrom("192.168.1.20:1"), "admin"); locked {
		t.Fatal("an allowlisted address must not be IP-locked or banned")
	}
	tripLock(limiter, "192.168.1.20:1")
	if locked, _ := limiter.lockedFor(requestFrom("192.168.1.20:1"), "admin"); !locked {
		t.Fatal("the in-memory IP+username lock still applies to allowlisted addresses")
	}
	if bans, _ := store.LoginBans(context.Background()); len(bans) != 0 {
		t.Fatalf("allowlisted address got banned: %+v", bans)
	}
}

func TestSecurityPageBansAndAllowlist(t *testing.T) {
	app, store, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	tripLock(app.loginLimiter, "203.0.113.77:1")

	body := getWithCookie(handler, securityPagePath, cookie).Body.String()
	for _, want := range []string{`data-login-ban="203.0.113.77"`, `data-login-client-ip>192.0.2.1<`, "15 分钟 → 1 小时 → 6 小时 → 24 小时 → 7 天", `data-ban-count="1"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("page missing %q", want)
		}
	}
	if got := securityNoticeText(t, postSecurity(t, app, handler, cookie, securityPagePath+"/bans/remove", url.Values{"ip": {"198.51.100.200"}})); got != securityNotices["ban_not_found"].Text {
		t.Fatalf("unknown ban notice = %q", got)
	}
	if rec := postSecurity(t, app, handler, cookie, securityPagePath+"/bans/remove", url.Values{"csrfToken": {"nope"}, "ip": {"203.0.113.77"}}); rec.Code != http.StatusForbidden {
		t.Fatalf("unban without CSRF = %d", rec.Code)
	}
	if got := securityNoticeText(t, postSecurity(t, app, handler, cookie, securityPagePath+"/bans/remove", url.Values{"ip": {"203.0.113.77"}})); got != securityNotices["ban_removed"].Text {
		t.Fatalf("unban notice = %q", got)
	}
	if _, ok := storedBan(t, store, "203.0.113.77"); ok {
		t.Fatal("unban must delete the stored row")
	}
	if locked, _ := app.loginLimiter.lockedFor(requestFrom("203.0.113.77:1"), "admin"); locked {
		t.Fatal("unban must also clear the in-memory locks")
	}

	add := func(prefix, note, password string) string {
		return securityNoticeText(t, postSecurity(t, app, handler, cookie, securityPagePath+"/allowlist", url.Values{"prefix": {prefix}, "note": {note}, "current_password": {password}}))
	}
	for _, tc := range []struct{ prefix, password, code string }{
		{"10.0.0.0/8", "wrong", "wrong_password"},
		{"not-an-ip", testAdminPassword, "allow_invalid"},
		{"0.0.0.0/0", testAdminPassword, "allow_too_broad"},
		{"::/8", testAdminPassword, "allow_too_broad"},
		{"fe80::1%eth0", testAdminPassword, "allow_invalid"},
	} {
		if got := add(tc.prefix, "", tc.password); got != securityNotices[tc.code].Text {
			t.Fatalf("%s: notice = %q, want %s", tc.prefix, got, tc.code)
		}
	}
	if got := add("203.0.113.0/24", strings.Repeat("长", maxLoginAllowNote+1), testAdminPassword); got != securityNotices["allow_note_invalid"].Text {
		t.Fatalf("long note = %q", got)
	}
	if got := add(" 203.0.113.9/24 ", "办公室", testAdminPassword); got != securityNotices["allow_added"].Text {
		t.Fatalf("add = %q", got)
	}
	if got := add("203.0.113.0/24", "", testAdminPassword); got != securityNotices["allow_exists"].Text {
		t.Fatalf("duplicate = %q", got)
	}
	entries, err := store.LoginAllowlist(context.Background())
	if err != nil || len(entries) != 1 || entries[0].Prefix != "203.0.113.0/24" || entries[0].Note != "办公室" {
		t.Fatalf("stored allowlist = %+v %v", entries, err)
	}
	// Allowlisted: no more bans for that range, and it survives a restart.
	tripLock(app.loginLimiter, "203.0.113.77:1")
	if _, ok := storedBan(t, store, "203.0.113.77"); ok {
		t.Fatal("an allowlisted range must not be banned")
	}
	restarted := newCredentialTestApp(t, credentialTestConfig(t), store)
	if !restarted.loginLimiter.allowlisted("203.0.113.5") {
		t.Fatal("the allowlist must survive a restart")
	}
	if body := getWithCookie(handler, securityPagePath, cookie).Body.String(); !strings.Contains(body, `data-login-allow="203.0.113.0/24"`) || !strings.Contains(body, "办公室") {
		t.Fatal("page must list the allowlist entry")
	}
	if got := securityNoticeText(t, postSecurity(t, app, handler, cookie, securityPagePath+"/allowlist/remove", url.Values{"prefix": {"203.0.113.0/24"}})); got != securityNotices["allow_removed"].Text {
		t.Fatalf("remove = %q", got)
	}
	if got := securityNoticeText(t, postSecurity(t, app, handler, cookie, securityPagePath+"/allowlist/remove", url.Values{"prefix": {"203.0.113.0/24"}})); got != securityNotices["allow_not_found"].Text {
		t.Fatalf("remove again = %q", got)
	}
	if app.loginLimiter.allowlisted("203.0.113.5") {
		t.Fatal("removed entry must stop applying")
	}
}

func TestLoginBanStoragePrunesStaleRows(t *testing.T) {
	store := credentialTestStore(t)
	ctx := context.Background()
	now := time.Now()
	old := storage.LoginBan{IP: "198.51.100.1", Strikes: 1, BannedUntil: now.Add(-40 * 24 * time.Hour), LastStrikeAt: now.Add(-40 * 24 * time.Hour)}
	if err := store.SaveLoginBan(ctx, old, now.Add(-50*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	fresh := storage.LoginBan{IP: "198.51.100.2", Strikes: 2, BannedUntil: now.Add(time.Hour), LastStrikeAt: now}
	if err := store.SaveLoginBan(ctx, fresh, now.Add(-loginStrikeDecay)); err != nil {
		t.Fatal(err)
	}
	if _, ok := storedBan(t, store, "198.51.100.1"); ok {
		t.Fatal("stale ban must be pruned")
	}
	if ban, ok := storedBan(t, store, "198.51.100.2"); !ok || ban.Strikes != 2 {
		t.Fatalf("fresh ban = %+v %v", ban, ok)
	}
}
