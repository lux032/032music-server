package httpapi

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/config"
)

func TestUntrustedForwarderDetection(t *testing.T) {
	trusted, err := config.ParseTrustedProxies("10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	limiter := newLoginLimiter()
	limiter.trustedProxies = trusted
	req := func(remote, header string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		if header != "" {
			r.Header.Set(header, "203.0.113.9")
		}
		return r
	}
	for _, tc := range []struct {
		r        *http.Request
		wantPeer string
		want     bool
	}{
		{req("172.18.0.5:5000", "X-Forwarded-For"), "172.18.0.5", true},
		{req("[::ffff:172.18.0.5]:5000", "X-Real-IP"), "172.18.0.5", true},
		{req("127.0.0.1:5000", "Forwarded"), "127.0.0.1", true},
		{req("10.0.0.1:5000", "X-Forwarded-For"), "", false}, // trusted proxy
		{req("172.18.0.5:5000", ""), "", false},              // direct client
	} {
		if peer, ok := limiter.untrustedForwarder(tc.r); ok != tc.want || peer != tc.wantPeer {
			t.Fatalf("%s: got %q %v", tc.r.RemoteAddr, peer, ok)
		}
	}

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	limiter.warnUntrustedForwarder(req("172.18.0.5:1", "X-Forwarded-For"), logger)
	limiter.warnUntrustedForwarder(req("172.18.0.6:1", "X-Forwarded-For"), logger)
	if got := strings.Count(logs.String(), "MUSIC_SERVER_TRUSTED_PROXIES"); got != 1 {
		t.Fatalf("warning logged %d times, want once: %s", got, logs.String())
	}
}

func TestSecurityPageShowsUntrustedProxyWarning(t *testing.T) {
	_, _, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	body := getWithCookie(handler, securityPagePath, cookie).Body.String()
	if strings.Contains(body, "data-proxy-untrusted") || !strings.Contains(body, "受信任的代理：未设置") {
		t.Fatal("direct access must not warn and must show the unset proxy list")
	}
	r := httptest.NewRequest(http.MethodGet, securityPagePath, nil)
	r.AddCookie(cookie)
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, r)
	body = rec.Body.String()
	if !strings.Contains(body, `data-proxy-untrusted="192.0.2.1"`) || !strings.Contains(body, "MUSIC_SERVER_TRUSTED_PROXIES=192.0.2.1") {
		t.Fatal("an untrusted proxy must be shown with a suggested value")
	}

	cfg := credentialTestConfig(t)
	cfg.TrustedProxies = "192.0.2.1"
	app := newCredentialTestApp(t, cfg, credentialTestStore(t))
	handler = app.Handler()
	cookie = mustLogin(t, handler, "admin", testAdminPassword)
	r = httptest.NewRequest(http.MethodGet, securityPagePath, nil)
	r.AddCookie(cookie)
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, r)
	body = rec.Body.String()
	if strings.Contains(body, "data-proxy-untrusted") || !strings.Contains(body, "data-login-client-ip>203.0.113.9<") || !strings.Contains(body, "<code>192.0.2.1/32</code>") {
		t.Fatal("a trusted proxy must resolve the real client and not warn")
	}
}
