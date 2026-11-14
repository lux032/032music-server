package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/storage"
)

func TestAdminArtistCorrectionPreviewCSRFAndConflict(t *testing.T) {
	_, store, handler, _, cookie, csrf := rateLimitTestApp(t)
	tracks, err := store.ListTracks(context.Background(), storage.Filters{Limit: 1})
	if err != nil || len(tracks) != 1 {
		t.Fatalf("%+v %v", tracks, err)
	}
	id := tracks[0].ID
	path := fmt.Sprintf("/admin/tracks/%d/artists", id)
	get := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	page := get()
	if dump := os.Getenv("ARTIST_CORRECTION_HTML"); dump != "" {
		if err := os.WriteFile(dump, page.Body.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if page.Code != 200 || !strings.Contains(page.Body.String(), "修正演唱者") {
		t.Fatalf("%d %s", page.Code, page.Body.String())
	}
	state, err := store.PrimaryArtistCorrection(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"action": {"reset"}, "token": {state.Token}}
	bad := postAdminForm(t, handler, cookie, "invalid", path, form)
	if bad.Code != 403 {
		t.Fatalf("CSRF=%d", bad.Code)
	}
	preview := postAdminForm(t, handler, cookie, csrf, path, form)
	if preview.Code != 200 || !strings.Contains(preview.Body.String(), "确认修改") {
		t.Fatalf("%d %s", preview.Code, preview.Body.String())
	}
	form.Set("confirm", "1")
	saved := postAdminForm(t, handler, cookie, csrf, path, form)
	if saved.Code != http.StatusSeeOther {
		t.Fatalf("%d %s", saved.Code, saved.Body.String())
	}
	stale := postAdminForm(t, handler, cookie, csrf, path, form)
	if stale.Code != 409 {
		t.Fatalf("stale=%d", stale.Code)
	}
}
