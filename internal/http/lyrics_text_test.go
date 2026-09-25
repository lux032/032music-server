package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeLyricsBOM(t *testing.T) {
	for _, tc := range []struct {
		in  []byte
		out string
	}{{[]byte{0xef, 0xbb, 0xbf, 'a'}, "a"}, {[]byte{0xff, 0xfe, 'a', 0}, "a"}, {[]byte{0xfe, 0xff, 0, 'a'}, "a"}, {[]byte{'a'}, "a"}} {
		if got := string(normalizeLyricsBOM(tc.in)); got != tc.out {
			t.Fatalf("%x: %q", tc.in, got)
		}
	}
}
func TestPlaylistCreateWithItemsRollback(t *testing.T) {
	app, _, token := setupTestApp(t)
	request := func(payload string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/playlists", bytes.NewBufferString(payload))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, r)
		return w
	}
	bad := request(`{"name":"rolled back","trackIds":[999999]}`)
	if bad.Code != 400 {
		t.Fatalf("invalid id: %d %s", bad.Code, bad.Body.String())
	}
	list := httptest.NewRequest(http.MethodGet, "/api/v1/playlists", nil)
	list.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, list)
	var body struct{ Total int }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Total != 0 {
		t.Fatalf("playlist rollback: %s %v", w.Body.String(), err)
	}
}

func TestLyricsTextMediaTokenAndFallback(t *testing.T) {
	app, store, token := setupTestApp(t)
	ctx := context.Background()
	root := t.TempDir()
	if err := store.EnsureLibrary(ctx, "Test", root); err != nil {
		t.Fatal(err)
	}
	lib, err := store.LibraryByRoot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "song.flac")
	if err = os.WriteFile(path, []byte("audio"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: "song.flac", FileSize: 5, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album", Artists: []string{"Singer"}, DiscNumber: 1, DurationMillis: 1000, Lyrics: "embedded"}}); err != nil {
		t.Fatal(err)
	}
	sync, err := store.SyncTracks(ctx, storage.SyncTracksParams{})
	if err != nil {
		t.Fatal(err)
	}
	url := fmt.Sprintf("/api/v1/tracks/%d/lyrics.lrc", sync.Items[0].ID)
	request := func(u string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, u, nil))
		return w
	}
	if w := request(url); w.Code != 401 {
		t.Fatalf("unauthenticated: %d", w.Code)
	}
	if w := request(url + "?mediaToken=" + token); w.Code != 200 || w.Body.String() != "embedded" {
		t.Fatalf("embedded: %d %s", w.Code, w.Body.String())
	}
	if err = os.WriteFile(filepath.Join(root, "song.lrc"), []byte{0xff, 0xfe, 'L', 0, 'R', 0, 'C', 0}, 0600); err != nil {
		t.Fatal(err)
	}
	if w := request(url + "?mediaToken=" + token); w.Code != 200 || w.Body.String() != "LRC" {
		t.Fatalf("external: %d %s", w.Code, w.Body.String())
	}
	if w := request("/api/v1/tracks/99999/lyrics.lrc?mediaToken=" + token); w.Code != 404 {
		t.Fatalf("missing: %d", w.Code)
	}
}

func TestPlaylistCreateWithItemsSuccess(t *testing.T) {
	app, store, token := setupTestApp(t)
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: "one.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album", Artists: []string{"Singer"}, DiscNumber: 1, DurationMillis: 1000}}); err != nil {
		t.Fatal(err)
	}
	tracks, err := store.SyncTracks(ctx, storage.SyncTracksParams{})
	if err != nil {
		t.Fatal(err)
	}
	id := tracks.Items[0].ID
	body := fmt.Sprintf(`{"name":"New","trackIds":[%d,%d]}`, id, id)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/playlists", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, req)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var playlist storage.Playlist
	if err = json.Unmarshal(w.Body.Bytes(), &playlist); err != nil || playlist.ItemCount != 1 {
		t.Fatalf("playlist: %+v %v", playlist, err)
	}
}

func TestLyricsTextRejectsOversizedSidecar(t *testing.T) {
	app, store, token := setupTestApp(t)
	ctx := context.Background()
	root := t.TempDir()
	if err := store.EnsureLibrary(ctx, "Test", root); err != nil {
		t.Fatal(err)
	}
	lib, err := store.LibraryByRoot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: "large.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album", DiscNumber: 1, DurationMillis: 1}}); err != nil {
		t.Fatal(err)
	}
	tracks, err := store.SyncTracks(ctx, storage.SyncTracksParams{})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "large.lrc"), bytes.Repeat([]byte("a"), maxExternalLRCBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/tracks/%d/lyrics.lrc?mediaToken=%s", tracks.Items[0].ID, token), nil)
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}

func TestLyricsTextEmptySidecarFallsBackToEmbedded(t *testing.T) {
	app, store, token := setupTestApp(t)
	ctx := context.Background()
	root := t.TempDir()
	if err := store.EnsureLibrary(ctx, "Test", root); err != nil {
		t.Fatal(err)
	}
	lib, err := store.LibraryByRoot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: "song.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album", DiscNumber: 1, DurationMillis: 1, Lyrics: "[00:01]embedded"}}); err != nil {
		t.Fatal(err)
	}
	tracks, err := store.SyncTracks(ctx, storage.SyncTracksParams{})
	if err != nil {
		t.Fatal(err)
	}
	url := fmt.Sprintf("/api/v1/tracks/%d/lyrics.lrc?mediaToken=%s", tracks.Items[0].ID, token)
	for _, sidecar := range [][]byte{nil, {0xef, 0xbb, 0xbf}, {0xff, 0xfe}, {0xef, 0xbb, 0xbf, ' ', '\n'}} {
		if err = os.WriteFile(filepath.Join(root, "song.lrc"), sidecar, 0600); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
		if w.Code != 200 || w.Body.String() != "[00:01]embedded" {
			t.Fatalf("empty sidecar %x: %d %s", sidecar, w.Code, w.Body.String())
		}
	}
}
