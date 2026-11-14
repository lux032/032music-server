package httpapi

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestLegacyCreatorConversionRequiresConfirmationAndCSRF(t *testing.T) {
	_, _, handler, _, cookie, csrf := rateLimitTestApp(t)
	form := url.Values{"legacyCreators": {"1"}, "type": {"full"}}
	badCSRF := postAdminForm(t, handler, cookie, "invalid", "/admin/scan", form)
	if badCSRF.Code != http.StatusForbidden {
		t.Fatalf("CSRF=%d", badCSRF.Code)
	}
	unconfirmed := postAdminForm(t, handler, cookie, csrf, "/admin/scan", form)
	if unconfirmed.Code != http.StatusBadRequest || !strings.Contains(unconfirmed.Body.String(), "确认") {
		t.Fatalf("unconfirmed=%d %s", unconfirmed.Code, unconfirmed.Body.String())
	}
}
