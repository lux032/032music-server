package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSimilarityHTTPValidationAndAuth(t *testing.T) {
	app, _, token := setupTestApp(t)
	h := app.Handler()
	for _, tc := range []struct {
		url    string
		auth   bool
		status int
	}{{"/api/v1/tracks/1/similar?limit=0", true, 400}, {"/api/v1/tracks/1/similar?limit=101", true, 400}, {"/api/v1/tracks/abc/similar", true, 400}, {"/api/v1/tracks/999999/similar", true, 404}, {"/api/v1/tracks/path?from=1&to=2&limit=1", true, 400}, {"/api/v1/tracks/path?from=no&to=2", true, 400}, {"/api/v1/tracks/path?from=999999&to=1", true, 404}, {"/api/v1/tracks/999999/similar?mediaToken=bad", false, 401}, {"/api/v1/capabilities", true, 200}} {
		req := httptest.NewRequest("GET", tc.url, nil)
		if tc.auth {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Errorf("%s status=%d want=%d body=%s", tc.url, w.Code, tc.status, w.Body.String())
		}
		if tc.url == "/api/v1/capabilities" && !strings.Contains(w.Body.String(), `"method":"metadata"`) {
			t.Error("missing metadata capability")
		}
	}
	_ = http.StatusOK
}
