package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSimilarityConcurrencyAndCancelledRequests(t *testing.T) {
	app, _, token := setupTestApp(t)
	handler := app.Handler()
	for i := 0; i < 4; i++ {
		app.similaritySlots <- struct{}{}
	}
	req := httptest.NewRequest("GET", "/api/v1/tracks/1/similar", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("full capacity: %d retry=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
	req = httptest.NewRequest("GET", "/api/v1/tracks/path?from=1&to=2", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("path full capacity: %d", rec.Code)
	}
	for i := 0; i < 4; i++ {
		<-app.similaritySlots
	}
	for _, path := range []string{"/api/v1/tracks/1/similar", "/api/v1/tracks/path?from=1&to=2"} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		req = httptest.NewRequest("GET", path, nil).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer "+token)
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code >= 500 || rec.Body.Len() != 0 {
			t.Fatalf("cancelled %s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	other, _, _ := setupTestApp(t)
	if len(other.similaritySlots) != 0 || cap(other.similaritySlots) != 4 {
		t.Fatal("slot capacity is not per-app")
	}
}
