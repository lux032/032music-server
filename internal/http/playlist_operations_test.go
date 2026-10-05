package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPlaylistOperationsRouter(t *testing.T) {
	app, s, token := setupTestApp(t)
	ctx := context.Background()
	root := t.TempDir()
	if err := s.EnsureLibrary(ctx, "test", root); err != nil {
		t.Fatal(err)
	}
	lib, err := s.LibraryByRoot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		err = s.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: fmt.Sprintf("%d.flac", i), FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: fmt.Sprint(i), Album: "a", Artists: []string{"s"}, DiscNumber: 1, TrackNumber: i}})
		if err != nil {
			t.Fatal(err)
		}
	}
	tracks, err := s.ListTracks(ctx, storage.Filters{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	ids := []int64{tracks[0].ID, tracks[1].ID, tracks[2].ID}
	p, err := s.CreatePlaylist(ctx, "p", "")
	if err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("/api/v1/playlists/%d", p.ID)
	handler := app.Handler()
	request := func(method, path, payload string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(payload))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	appendBody := fmt.Sprintf(`{"trackIds":[%d,%d,%d,99999]}`, ids[0], ids[1], ids[0])
	w := request("POST", base+"/items/append", appendBody)
	var stats storage.PlaylistItemResult
	if err = json.Unmarshal(w.Body.Bytes(), &stats); err != nil || w.Code != 200 || stats.Added != 2 || stats.SkippedDuplicate != 1 || stats.SkippedInvalid != 1 {
		t.Fatalf("append %d %s", w.Code, w.Body)
	}
	for _, path := range []string{"/order", "/items/remove"} {
		method := "POST"
		if path == "/order" {
			method = "PUT"
		}
		w = request(method, base+path, `{"trackIds":[],"expectedRevision":0}`)
		if w.Code != 409 {
			t.Fatalf("conflict %d %s", w.Code, w.Body)
		}
	}
	w = request("POST", base+"/items/append", `{"trackIds":[],"albumId":1}`)
	if w.Code != 400 {
		t.Fatalf("ambiguous %d", w.Code)
	}
	w = request("POST", base+"/items/insert", fmt.Sprintf(`{"trackId":%d,"index":0}`, ids[2]))
	if w.Code != 200 {
		t.Fatalf("insert %d %s", w.Code, w.Body)
	}
	w = request("PUT", base+"/items", fmt.Sprintf(`{"trackIds":[%d]}`, ids[0]))
	if w.Code != 200 {
		t.Fatalf("legacy %d %s", w.Code, w.Body)
	}
	w = request("POST", "/api/v1/playlists", fmt.Sprintf(`{"name":"new","invalidTracks":"skip","trackIds":[%d,%d,99999]}`, ids[0], ids[0]))
	if w.Code != 201 || !strings.Contains(w.Body.String(), `"skippedInvalid":1`) {
		t.Fatalf("skip create %d %s", w.Code, w.Body)
	}
	cookie := adminCookie(t, app)
	r := httptest.NewRequest("POST", base+"/items/append", strings.NewReader(appendBody))
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("csrf %d", w.Code)
	}
	w = request("OPTIONS", base+"/items/append", "")
	if w.Code != 204 {
		t.Fatalf("options %d", w.Code)
	}
}
func TestPlaylistArtworkRouter(t *testing.T) {
	app, s, token := setupTestApp(t)
	ctx := context.Background()
	p, err := s.CreatePlaylist(ctx, "p", "")
	if err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("/api/v1/playlists/%d", p.ID)
	handler := app.Handler()
	upload := func(data []byte) *httptest.ResponseRecorder {
		r := uploadRequest(t, base+"/artwork", "", "image.png", data)
		r.Method = "PUT"
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	request := func(method, path string, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		if auth {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	w := upload(pngBytes(t, 512, 512))
	if w.Code != 200 {
		t.Fatalf("upload %d %s", w.Code, w.Body)
	}
	var result struct {
		ArtworkURL       string `json:"artworkUrl"`
		Revision         int64  `json:"revision"`
		HasCustomArtwork bool   `json:"hasCustomArtwork"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil || !result.HasCustomArtwork || result.Revision != 1 {
		t.Fatalf("response %s %v", w.Body, err)
	}
	oldURL := result.ArtworkURL
	img, err := s.PlaylistImage(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(app.customImagesDir(), img.FileName)
	aged := time.Now().Add(-2 * time.Hour)
	if err = os.Chtimes(path, aged, aged); err != nil {
		t.Fatal(err)
	}
	app.GCCustomImages(ctx)
	if _, err = os.Stat(path); err != nil {
		t.Fatal("GC removed referenced image", err)
	}
	if w = request("GET", oldURL, false); w.Code != 401 {
		t.Fatalf("media auth %d", w.Code)
	}
	if w = request("GET", oldURL, true); w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("version %d %s", w.Code, w.Header())
	}
	if w = request("GET", base+"/artwork", true); w.Code != 200 || w.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("unversioned %d %s", w.Code, w.Header())
	}
	if w = request("GET", oldURL+"&size=256", true); w.Code != 200 || w.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("thumbnail %d %s", w.Code, w.Body)
	}
	// Share content with another playlist, then reset the original.
	other, err := s.CreatePlaylist(ctx, "other", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveCustomPlaylistImage(ctx, other.ID, img); err != nil {
		t.Fatal(err)
	}
	if w = request("DELETE", base+"/artwork", true); w.Code != 200 {
		t.Fatalf("reset %d", w.Code)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal("shared file removed", err)
	}
	w = upload(jpegBytes(t, 32, 32))
	if w.Code != 200 {
		t.Fatalf("replace %d", w.Code)
	}
	if w = request("GET", oldURL, true); w.Code != 404 || strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("stale %d %s", w.Code, w.Header())
	}
	if w = upload([]byte("invalid")); w.Code != 400 || !strings.Contains(w.Header().Get("Content-Type"), "json") {
		t.Fatalf("format %d %s", w.Code, w.Body)
	}
	if w = upload(bytes.Repeat([]byte{'a'}, customImageMaxBytes+1)); w.Code != 413 || !strings.Contains(w.Header().Get("Content-Type"), "json") {
		t.Fatalf("oversize %d %s", w.Code, w.Body)
	}
	if w = request("DELETE", fmt.Sprintf("/api/v1/playlists/%d", other.ID), true); w.Code != 204 {
		t.Fatalf("delete %d", w.Code)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("orphan survived %v", err)
	}
	if w = request("DELETE", base, true); w.Code != 204 {
		t.Fatalf("delete %d", w.Code)
	}
}

func TestPlaylistBusyRouter(t *testing.T) {
	app, s, token := setupTestApp(t)
	ctx := context.Background()
	p, err := s.CreatePlaylist(ctx, "busy", "")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", httpTestDBPath[s]+"?_pragma=busy_timeout(1)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE playlists SET id=id WHERE 0`); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/playlists/%d/items/append", p.ID), strings.NewReader(`{"trackIds":[]}`))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, r)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "database_busy") {
		t.Fatalf("busy %d %s", w.Code, w.Body)
	}
}
