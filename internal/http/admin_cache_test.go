package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAdminHTMLIsNoStoreAssetsStayImmutable: every rendered admin page
// (and the login page) is Cache-Control: no-store, while content-addressed
// assets keep their long-lived immutable caching and JSON API responses
// are not affected.
func TestAdminHTMLIsNoStoreAssetsStayImmutable(t *testing.T) {
	app, _, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)

	for _, path := range []string{"/admin", "/admin/albums", "/admin/playlists", "/admin/settings/metadata", securityPagePath} {
		rec := getWithCookie(handler, path, cookie)
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("%s = %d %q", path, rec.Code, rec.Header().Get("Content-Type"))
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s Cache-Control = %q, want no-store", path, got)
		}
	}
	login := getWithCookie(handler, "/admin/login", nil)
	if got := login.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("login page Cache-Control = %q, want no-store", got)
	}

	asset := getWithCookie(handler, app.assets.assetURL("admin.js"), nil)
	if asset.Code != http.StatusOK {
		t.Fatalf("asset status = %d", asset.Code)
	}
	if got := asset.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("asset Cache-Control = %q, want immutable", got)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if got := rec.Header().Get("Cache-Control"); got == "no-store" {
		t.Fatal("JSON API responses must keep their own caching headers")
	}
}

// TestSecurityNoticeTextsFollowLimits: limit numbers in notices come from
// the validation constants.
func TestSecurityNoticeTextsFollowLimits(t *testing.T) {
	for code, want := range map[string]string{
		"username_too_long":  fmt.Sprint(maxAdminUsernameLength),
		"password_too_short": fmt.Sprint(minAdminPasswordLength),
		"token_too_short":    fmt.Sprint(minCredentialTokenLen),
		"token_too_long":     fmt.Sprint(maxCredentialTokenLen),
	} {
		if !strings.Contains(securityNotices[code].Text, want) {
			t.Fatalf("%s = %q, want it to mention %s", code, securityNotices[code].Text, want)
		}
	}
}

// TestCustomTokenFieldsAreMaskedPassword: custom tokens stay masked without CSS.
func TestCustomTokenFieldsAreMaskedPassword(t *testing.T) {
	_, _, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	body := getWithCookie(handler, securityPagePath, cookie).Body.String()
	for _, id := range []string{"api-custom-token", "media-custom-token"} {
		field := fmt.Sprintf(`<input id="%s" type="password" name="custom_token" autocomplete="new-password"`, id)
		if !strings.Contains(body, field) {
			t.Fatalf("missing masked text field %s", id)
		}
		if !strings.Contains(body, fmt.Sprintf(`data-mask-toggle="%s"`, id)) {
			t.Fatalf("missing show toggle for %s", id)
		}
	}
	if strings.Count(body, "#icon-shield") != 2 {
		t.Fatal("security navigation entries must use the shield icon")
	}
}
