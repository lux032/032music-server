package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lux032/032music-server/internal/config"
)

func TestMediaAccessAcceptsQueryToken(t *testing.T) {
	app := &App{
		config:   config.Config{APIToken: "api-token-at-least-24-characters", MediaToken: "media-token-at-least-24-characters"},
		sessions: newSessionManager(false),
	}
	handler := app.requireMediaAccess(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	request := httptest.NewRequest(http.MethodGet, "/stream?mediaToken=media-token-at-least-24-characters", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
}

func TestMediaAccessRejectsInvalidToken(t *testing.T) {
	app := &App{
		config:   config.Config{APIToken: "api-token-at-least-24-characters", MediaToken: "media-token-at-least-24-characters"},
		sessions: newSessionManager(false),
	}
	handler := app.requireMediaAccess(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	request := httptest.NewRequest(http.MethodGet, "/stream?mediaToken=wrong", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}
