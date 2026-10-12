package httpapi

import (
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/lux032/032music-server/internal/storage"
)

// seedCachedWorkPoster creates a work whose poster is already in the local
// cache and returns it.
func seedCachedWorkPoster(t *testing.T, store *storage.Store, posterDir, title string) storage.Work {
	t.Helper()
	posterURL := "https://example.com/" + title + ".jpg"
	work, err := store.CreateWork(context.Background(), storage.WorkInput{Title: title, Type: "anime", PosterURL: posterURL})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(posterDir, 0o750); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 600, 400))
	for y := 0; y < 400; y++ {
		for x := 0; x < 600; x++ {
			img.Set(x, y, color.RGBA{R: 20, G: 120, B: 200, A: 255})
		}
	}
	file, err := os.Create(filepath.Join(posterDir, fmt.Sprintf("%x", sha256.Sum256([]byte(posterURL)))+".jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if err = jpeg.Encode(file, img, nil); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	return work
}

func TestAPIWorkPosterMediaAccess(t *testing.T) {
	app, store, handler, _, cookie, _, posterDir := batch8PosterApp(t)
	work := seedCachedWorkPoster(t, store, posterDir, "poster-api")
	path := "/api/v1/works/" + strconv.FormatInt(work.ID, 10) + "/poster"
	get := func(target string, mutate func(*http.Request)) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		if mutate != nil {
			mutate(req)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	if rec := get(path, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no credentials: %d", rec.Code)
	}
	if rec := get(path+"?mediaToken="+testEnvMediaToken, nil); rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("media token: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	rec := get(path+"?size=256", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+testEnvMediaToken) })
	if rec.Code != http.StatusOK {
		t.Fatalf("bearer media token thumbnail: %d", rec.Code)
	}
	if cfg, _, err := image.DecodeConfig(rec.Body); err != nil || cfg.Width != 256 {
		t.Fatalf("thumbnail width=%d err=%v", cfg.Width, err)
	}
	// The full-access API token is never accepted on media routes.
	if rec := get(path, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+testEnvAPIToken) }); rec.Code == http.StatusOK {
		t.Fatal("API token must not open a media route")
	}
	if rec := get(path, func(r *http.Request) { r.AddCookie(cookie) }); rec.Code != http.StatusOK {
		t.Fatalf("admin session: %d", rec.Code)
	}
	if rec := get("/api/v1/works/999999/poster?mediaToken="+testEnvMediaToken, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown work: %d", rec.Code)
	}

	if got := workPosterAPIURL(app.enrichment, work, 360); got == "" || got[:len(path)] != path {
		t.Fatalf("api poster url = %q", got)
	}
	if got := workPosterURL(app.enrichment, work, 360); got[:len("/admin/works/")] != "/admin/works/" {
		t.Fatalf("web poster url must stay on /admin: %q", got)
	}
}
