package httpapi

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/config"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func TestThumbnailSize(t *testing.T) {
	for query, want := range map[string]int{"": 0, "?size=1": 256, "?size=256": 256, "?size=257": 512, "?size=600": 768, "?size=900": 1024, "?size=1025": 1536, "?size=99999": 1536} {
		r := httptest.NewRequest("GET", "/x"+query, nil)
		got, err := thumbnailSize(r)
		if err != nil || got != want {
			t.Errorf("%s: %d %v", query, got, err)
		}
	}
	for _, q := range []string{"?size=0", "?size=-1", "?size=x", "?size=", "?size=1&size=2", "?size=18446744073709551616"} {
		if _, err := thumbnailSize(httptest.NewRequest("GET", "/x"+q, nil)); err == nil {
			t.Errorf("accepted %s", q)
		}
	}
}

func TestThumbnailPipeline(t *testing.T) {
	app, _, token := setupTestApp(t)
	root := t.TempDir()
	path := filepath.Join(root, "source.jpg")
	img := image.NewRGBA(image.Rect(0, 0, 2000, 1000))
	for y := 0; y < 1000; y++ {
		for x := 0; x < 2000; x++ {
			img.Set(x, y, color.RGBA{200, 80, 40, 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(f, img, nil); err != nil {
		t.Fatal(err)
	}
	f.Close()
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	app.thumbnails.decode = func(f *os.File) (image.Image, string, error) { calls.Add(1); return image.Decode(f) }
	request := func(method, query string, etag string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1/artwork/1"+query, nil)
		if etag != "" {
			r.Header.Set("If-None-Match", etag)
		}
		w := httptest.NewRecorder()
		app.serveImageForTest(w, r, path, "image/jpeg", 512)
		return w
	}
	_ = token
	if w := request("GET", "", ""); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), original) {
		t.Fatal("original changed")
	}
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := request("GET", "?size=512", "")
			if w.Code != 200 || w.Header().Get("Content-Type") != "image/jpeg" {
				t.Errorf("thumbnail: %d", w.Code)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("decodes: %d", calls.Load())
	}
	w := request("GET", "?size=512", "")
	dimensions, err := jpeg.DecodeConfig(bytes.NewReader(w.Body.Bytes()))
	if err != nil || dimensions.Width != 512 || dimensions.Height != 256 {
		t.Fatalf("dimensions: %v %v", dimensions, err)
	}
	etag := w.Header().Get("ETag")
	if etag == "" || request("GET", "?size=512", etag).Code != 304 {
		t.Fatal("ETag conditional failed")
	}
	if request("HEAD", "?size=512", "").Header().Get("ETag") != etag {
		t.Fatal("HEAD cached headers")
	}
	if calls.Load() != 1 {
		t.Fatal("cache hit decoded")
	}
	now := time.Now().Add(time.Second)
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatal(err)
	}
	if w := request("GET", "?size=512", ""); w.Header().Get("ETag") == etag || calls.Load() != 2 {
		t.Fatal("mtime did not invalidate")
	}
	for _, q := range []string{"?size=0", "?size=bad", "?size=3&size=4"} {
		if w := request("GET", q, ""); w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_request") {
			t.Errorf("invalid %s: %d", q, w.Code)
		}
	}
}

func (a *App) serveImageForTest(w http.ResponseWriter, r *http.Request, path, mime string, bucket int) {
	size, err := thumbnailSize(r)
	if err != nil {
		writeAPIError(w, 400, "invalid_request", "Invalid size.")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, _ := f.Stat()
	if size != 0 && a.serveThumbnail(w, r, f, info, "artwork", 1, size, "public, max-age=31536000, immutable") {
		return
	}
	w.Header().Set("Content-Type", mime)
	if size != 0 {
		w.Header().Set("Cache-Control", "no-cache")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

func TestThumbnailEndpoints(t *testing.T) {
	app, store, token := setupTestApp(t)
	root := t.TempDir()
	path := filepath.Join(root, "cover.png")
	img := image.NewRGBA(image.Rect(0, 0, 600, 300))
	f, _ := os.Create(path)
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	f.Close()
	data, _ := os.ReadFile(path)
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Test", root); err != nil {
		t.Fatal(err)
	}
	lib, err := store.LibraryByRoot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: "song.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 1}, Artwork: &storage.ArtworkInput{Hash: "test-cover", MIMEType: "image/png", CachePath: path, SourceType: "embedded", ByteSize: int64(len(data))}})
	if err != nil {
		t.Fatal(err)
	}
	albums, err := store.ListAlbums(ctx, storage.Filters{})
	if err != nil || len(albums) != 1 {
		t.Fatalf("albums: %v %v", albums, err)
	}
	artists, err := store.ListArtists(ctx, storage.Filters{})
	if err != nil || len(artists) == 0 {
		t.Fatalf("artists: %v %v", artists, err)
	}
	if err := store.SaveArtistImage(ctx, storage.ArtistImageInput{ArtistID: artists[0].ID, ByteSize: int64(len(data)), Source: "test", RemoteURL: "https://example.invalid/image", Hash: "artist-image", MIMEType: "image/png", CachePath: path}); err != nil {
		t.Fatal(err)
	}
	handler := app.Handler()
	for _, endpoint := range []string{albums[0].ArtworkURL, "/api/v1/artists/" + strconv.FormatInt(artists[0].ID, 10) + "/image"} {
		send := func(query string, authenticated bool) *httptest.ResponseRecorder {
			r := httptest.NewRequest("GET", endpoint+query, nil)
			if authenticated {
				r.Header.Set("Authorization", "Bearer "+token)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			return w
		}
		if w := send("?size=512", false); w.Code != 401 {
			t.Fatalf("%s unauthenticated: %d", endpoint, w.Code)
		}
		if w := send("?mediaToken="+token, false); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), data) {
			t.Fatalf("%s original: %d", endpoint, w.Code)
		} else {
			want := "public, max-age=86400"
			if endpoint == albums[0].ArtworkURL {
				want = "public, max-age=31536000, immutable"
			}
			if w.Header().Get("Content-Type") != "image/png" || w.Header().Get("Cache-Control") != want || w.Header().Get("Access-Control-Allow-Origin") != "*" || w.Header().Get("Last-Modified") == "" {
				t.Fatalf("%s original headers: %v", endpoint, w.Header())
			}
		}
		if w := send("?size=256&mediaToken="+token, false); w.Code != 200 || w.Header().Get("Content-Type") != "image/jpeg" {
			t.Fatalf("%s thumbnail: %d %s", endpoint, w.Code, w.Body.String())
		} else {
			want := "public, max-age=86400"
			if endpoint == albums[0].ArtworkURL {
				want = "public, max-age=31536000, immutable"
			}
			if got := w.Header().Get("Cache-Control"); got != want {
				t.Fatalf("%s cache: %s", endpoint, got)
			}
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if w := send("?size=256&mediaToken="+token, false); w.Code != 404 {
			t.Fatalf("%s removed source: %d", endpoint, w.Code)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if w := send("?size=0", true); w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_request") {
			t.Fatalf("%s bad size: %d", endpoint, w.Code)
		}
	}
}

func TestThumbnailFallbackAndEviction(t *testing.T) {
	root := t.TempDir()
	m := newThumbnailManager(config.Config{DataDirectory: root, ThumbCacheMB: 1})
	img := image.NewNRGBA(image.Rect(0, 0, 600, 300))
	img.Set(0, 0, color.NRGBA{255, 0, 0, 0})
	path := filepath.Join(root, "alpha.png")
	f, _ := os.Create(path)
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	f.Close()
	info, _ := os.Stat(path)
	_, dest, ok, err := m.thumbnail(context.Background(), "artist", 1, mustOpenThumbnail(t, path), info, 256)
	if err != nil || !ok {
		t.Fatalf("png: %v", err)
	}
	data, _ := os.ReadFile(dest)
	decoded, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := decoded.At(0, 0).RGBA()
	if r < 62000 || g < 62000 || b < 62000 {
		t.Fatalf("transparent background: %d %d %d", r, g, b)
	}
	small := filepath.Join(root, "small.png")
	os.WriteFile(small, data, 0600)
	info, _ = os.Stat(small)
	_, _, ok, _ = m.thumbnail(context.Background(), "artwork", 2, mustOpenThumbnail(t, small), info, 512)
	if ok {
		t.Fatal("small enlarged")
	}
	bad := filepath.Join(root, "bad.png")
	os.WriteFile(bad, []byte("broken"), 0600)
	info, _ = os.Stat(bad)
	_, _, ok, err = m.thumbnail(context.Background(), "artwork", 3, mustOpenThumbnail(t, bad), info, 256)
	if ok || err == nil {
		t.Fatal("corrupt did not fall back")
	}
	fallbackApp, _, _ := setupTestApp(t)
	badResponse := httptest.NewRecorder()
	fallbackApp.serveImageForTest(badResponse, httptest.NewRequest("GET", "/api/v1/artwork/1?size=256", nil), bad, "image/png", 256)
	if badResponse.Code != 200 || badResponse.Header().Get("Cache-Control") != "no-cache" || !bytes.Equal(badResponse.Body.Bytes(), []byte("broken")) {
		t.Fatal("corrupt image did not return original with 200")
	}
	huge := make([]byte, 33)
	copy(huge, []byte("\x89PNG\r\n\x1a\n"))
	binary.BigEndian.PutUint32(huge[8:12], 13)
	copy(huge[12:16], "IHDR")
	binary.BigEndian.PutUint32(huge[16:20], 20000)
	binary.BigEndian.PutUint32(huge[20:24], 20000)
	huge[24] = 8
	huge[25] = 2
	// Valid PNG header CRC, without pixel data: DecodeConfig succeeds, Decode must never run.
	binary.BigEndian.PutUint32(huge[29:33], crc32.ChecksumIEEE(huge[12:29]))
	os.WriteFile(bad, huge, 0600)
	info, _ = os.Stat(bad)
	_, _, ok, err = m.thumbnail(context.Background(), "artwork", 4, mustOpenThumbnail(t, bad), info, 256)
	if ok || err != nil {
		t.Fatalf("oversized: %v %v", ok, err)
	}
	oversizeResponse := httptest.NewRecorder()
	fallbackApp.serveImageForTest(oversizeResponse, httptest.NewRequest("GET", "/api/v1/artwork/1?size=256", nil), bad, "image/png", 256)
	if oversizeResponse.Code != 200 || oversizeResponse.Header().Get("Cache-Control") != "no-cache" || !bytes.Equal(oversizeResponse.Body.Bytes(), huge) {
		t.Fatal("oversized image did not return original with 200")
	}
	part := filepath.Join(m.dir, "orphan.part")
	os.WriteFile(part, []byte("x"), 0600)
	newThumbnailManager(config.Config{DataDirectory: root})
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Fatal("part retained")
	}
	old := filepath.Join(m.dir, "old.jpg")
	os.WriteFile(old, make([]byte, 700000), 0600)
	fresh := filepath.Join(m.dir, "fresh.jpg")
	os.WriteFile(fresh, make([]byte, 700000), 0600)
	m.mu.Lock()
	m.files["old.jpg"] = thumbnailFile{700000, time.Now().Add(-time.Hour)}
	m.files["fresh.jpg"] = thumbnailFile{700000, time.Now()}
	m.total += 1400000
	m.evictLocked("fresh.jpg")
	m.mu.Unlock()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("old not evicted")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("fresh evicted")
	}
}

func mustOpenThumbnail(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestThumbnailNegativeAndHead(t *testing.T) {
	app, _, _ := setupTestApp(t)
	path := filepath.Join(t.TempDir(), "small.png")
	f, _ := os.Create(path)
	png.Encode(f, image.NewRGBA(image.Rect(0, 0, 32, 32)))
	f.Close()
	var calls atomic.Int32
	app.thumbnails.decodeConfig = func(f *os.File) (image.Config, string, error) { calls.Add(1); return image.DecodeConfig(f) }
	for range 2 {
		w := httptest.NewRecorder()
		app.serveImageForTest(w, httptest.NewRequest("GET", "/api/v1/artwork/1?size=256", nil), path, "image/png", 256)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-cache" {
			t.Fatal("fallback headers")
		}
	}
	if calls.Load() != 1 || len(app.thumbnails.slots) != 0 {
		t.Fatalf("negative cache: %d", calls.Load())
	}
	other := filepath.Join(t.TempDir(), "large.png")
	f, _ = os.Create(other)
	png.Encode(f, image.NewRGBA(image.Rect(0, 0, 600, 300)))
	f.Close()
	w := httptest.NewRecorder()
	app.serveImageForTest(w, httptest.NewRequest("HEAD", "/api/v1/artwork/1?size=256", nil), other, "image/png", 256)
	if len(app.thumbnails.files) != 0 || w.Header().Get("Cache-Control") != "no-cache" {
		t.Fatal("HEAD generated")
	}
}

func TestThumbnailCanceledLeaderAndPanic(t *testing.T) {
	m := newThumbnailManager(config.Config{DataDirectory: t.TempDir()})
	path := filepath.Join(t.TempDir(), "img.png")
	f, _ := os.Create(path)
	png.Encode(f, image.NewRGBA(image.Rect(0, 0, 700, 300)))
	f.Close()
	info, _ := os.Stat(path)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	m.decodeConfig = func(f *os.File) (image.Config, string, error) {
		once.Do(func() { close(entered); <-release })
		return image.DecodeConfig(f)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		file, _ := os.Open(path)
		defer file.Close()
		m.thumbnail(ctx, "artwork", 1, file, info, 256)
	}()
	<-entered
	follower := make(chan bool, 1)
	go func() {
		file, _ := os.Open(path)
		defer file.Close()
		_, _, ok, _ := m.thumbnail(context.Background(), "artwork", 1, file, info, 256)
		follower <- ok
	}()
	cancel()
	close(release)
	<-done
	if !<-follower {
		t.Fatal("follower did not regenerate")
	}
	path2 := filepath.Join(t.TempDir(), "other.png")
	data, _ := os.ReadFile(path)
	os.WriteFile(path2, data, 0600)
	info2, _ := os.Stat(path2)
	m.decode = func(*os.File) (image.Image, string, error) { panic("injected") }
	file := mustOpenThumbnail(t, path2)
	_, _, ok, err := m.thumbnail(context.Background(), "artwork", 2, file, info2, 256)
	if ok || err == nil || len(m.jobs) != 0 || len(m.slots) != 0 {
		t.Fatal("panic left job or slot")
	}
	m.decode = func(f *os.File) (image.Image, string, error) { return image.Decode(f) }
	file = mustOpenThumbnail(t, path2)
	_, _, ok, err = m.thumbnail(context.Background(), "artwork", 2, file, info2, 256)
	if ok || err != nil {
		t.Fatalf("panic not negative cached: %v", err)
	}
	// A different source key can still be generated after the panic.
	file = mustOpenThumbnail(t, path2)
	_, _, ok, err = m.thumbnail(context.Background(), "artwork", 3, file, info2, 256)
	if !ok || err != nil {
		t.Fatalf("post-panic: %v", err)
	}
}

func TestThumbnailDeletedBeforeOpen(t *testing.T) {
	app, _, _ := setupTestApp(t)
	path := filepath.Join(t.TempDir(), "img.png")
	f, _ := os.Create(path)
	png.Encode(f, image.NewRGBA(image.Rect(0, 0, 700, 350)))
	f.Close()
	var decoded atomic.Int32
	app.thumbnails.decode = func(f *os.File) (image.Image, string, error) { decoded.Add(1); return image.Decode(f) }
	originalOpen := app.thumbnails.openCache
	var tries atomic.Int32
	app.thumbnails.openCache = func(path string) (*os.File, error) {
		if tries.Add(1) == 2 {
			os.Remove(path)
			return nil, os.ErrNotExist
		}
		return originalOpen(path)
	}
	w := httptest.NewRecorder()
	app.serveImageForTest(w, httptest.NewRequest("GET", "/api/v1/artwork/1?size=256", nil), path, "image/png", 256)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/jpeg" || decoded.Load() != 2 {
		t.Fatalf("deleted cache: %d decodes %d", w.Code, decoded.Load())
	}
}

func TestThumbnailCheckerboard(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1024, 1024))
	for y := 0; y < 1024; y++ {
		for x := 0; x < 1024; x++ {
			v := uint8(0)
			if (x+y)%2 == 0 {
				v = 255
			}
			img.SetRGBA(x, y, color.RGBA{v, v, v, 255})
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, 256, 256))
	boxThumbnail(context.Background(), dst, img)
	for i := 0; i < len(dst.Pix); i += 4 {
		if dst.Pix[i] < 126 || dst.Pix[i] > 129 {
			t.Fatalf("alias at %d: %d", i, dst.Pix[i])
		}
	}
}

func TestThumbnailGIFOffsetAndColorModels(t *testing.T) {
	m := newThumbnailManager(config.Config{DataDirectory: t.TempDir()})
	path := filepath.Join(t.TempDir(), "offset.gif")
	palette := color.Palette{color.RGBA{0, 0, 0, 0}, color.RGBA{255, 0, 0, 255}}
	frame := image.NewPaletted(image.Rect(150, 50, 450, 250), palette)
	for y := 50; y < 250; y++ {
		for x := 150; x < 450; x++ {
			frame.SetColorIndex(x, y, 1)
		}
	}
	// EncodeAll preserves logical screen size and the frame's nonzero origin.
	f, _ := os.Create(path)
	err := gif.EncodeAll(f, &gif.GIF{Image: []*image.Paletted{frame}, Delay: []int{0}, Config: image.Config{Width: 600, Height: 300, ColorModel: palette}})
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	_, dest, ok, err := m.thumbnail(context.Background(), "artist", 8, mustOpenThumbnail(t, path), info, 256)
	if !ok || err != nil {
		t.Fatalf("GIF: %v", err)
	}
	out, _ := os.Open(dest)
	result, _ := jpeg.Decode(out)
	out.Close()
	r, g, b, _ := result.At(0, 0).RGBA()
	if r < 60000 || g < 60000 || b < 60000 {
		t.Fatal("GIF canvas not white")
	}
	r, g, b, _ = result.At(128, 64).RGBA()
	if r < 50000 || g > 10000 || b > 10000 {
		t.Fatal("GIF offset lost")
	}
	for _, test := range []struct {
		name string
		img  image.Image
	}{{"gray16", func() image.Image {
		v := image.NewGray16(image.Rect(0, 0, 600, 300))
		for i := range v.Pix {
			v.Pix[i] = 180
		}
		return v
	}()}, {"cmyk", func() image.Image {
		v := image.NewCMYK(image.Rect(0, 0, 600, 300))
		for y := 0; y < 300; y++ {
			for x := 0; x < 600; x++ {
				v.SetCMYK(x, y, color.CMYK{0, 255, 255, 0})
			}
		}
		return v
	}()}, {"ycbcr", func() image.Image {
		v := image.NewYCbCr(image.Rect(0, 0, 600, 300), image.YCbCrSubsampleRatio420)
		for y := 0; y < 300; y++ {
			for x := 0; x < 600; x++ {
				v.Y[v.YOffset(x, y)] = 120
				v.Cb[v.COffset(x, y)] = 128
				v.Cr[v.COffset(x, y)] = 128
			}
		}
		return v
	}()}} {
		p := filepath.Join(t.TempDir(), test.name+".png")
		f, _ := os.Create(p)
		if test.name == "cmyk" { // PNG cannot encode CMYK; supply matching config and decoded model via hooks.
			png.Encode(f, image.NewRGBA(image.Rect(0, 0, 600, 300)))
		} else {
			png.Encode(f, test.img)
		}
		f.Close()
		info, _ := os.Stat(p)
		m2 := newThumbnailManager(config.Config{DataDirectory: t.TempDir()})
		if test.name == "cmyk" || test.name == "ycbcr" {
			m2.decode = func(*os.File) (image.Image, string, error) { return test.img, "png", nil }
		}
		_, name, ok, err := m2.thumbnail(context.Background(), "artwork", 1, mustOpenThumbnail(t, p), info, 256)
		if !ok || err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		f, _ = os.Open(name)
		cfg, _ := jpeg.DecodeConfig(f)
		f.Close()
		if cfg.Width != 256 || cfg.Height != 128 {
			t.Fatalf("%s dimensions: %v", test.name, cfg)
		}
	}
}

func TestThumbnailGeneratedLRU(t *testing.T) {
	root := t.TempDir()
	m := newThumbnailManager(config.Config{DataDirectory: root, ThumbCacheMB: 1})
	makeSource := func(label string) string {
		t.Helper()
		path := filepath.Join(root, label+".png")
		img := image.NewRGBA(image.Rect(0, 0, 700, 400))
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err = png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
		f.Close()
		return path
	}
	first := makeSource("first")
	info, _ := os.Stat(first)
	_, old, ok, err := m.thumbnail(context.Background(), "artwork", 1, mustOpenThumbnail(t, first), info, 256)
	if !ok || err != nil {
		t.Fatalf("first: %v", err)
	}
	second := makeSource("second")
	info, _ = os.Stat(second)
	// Force capacity to one thumbnail: generation must protect the newly written entry.
	m.mu.Lock()
	m.limit = m.total
	m.mu.Unlock()
	_, newName, ok, err := m.thumbnail(context.Background(), "artwork", 2, mustOpenThumbnail(t, second), info, 256)
	if !ok || err != nil {
		t.Fatalf("second: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old cache retained: %v", err)
	}
	if _, err := os.Stat(newName); err != nil {
		t.Fatalf("new cache removed: %v", err)
	}
}

func TestThumbnailMemoryBudget(t *testing.T) {
	if thumbnailMaxPixels != 24_000_000 || int64(34_000_000)*thumbnailBytesPerPixel(color.RGBA64Model) <= thumbnailMaxBytes {
		t.Fatal("memory budget inaccurate")
	}
	if thumbnailBytesPerPixel(color.Gray16Model) != 8 || thumbnailBytesPerPixel(color.YCbCrModel) != 3 || thumbnailBytesPerPixel(color.Palette{color.Black}) != 1 {
		t.Fatal("color model estimate")
	}
}

func TestThumbnailEvictionDeleteFailure(t *testing.T) {
	m := newThumbnailManager(config.Config{DataDirectory: t.TempDir()})
	for _, name := range []string{"old.jpg", "new.jpg"} {
		if err := os.WriteFile(filepath.Join(m.dir, name), make([]byte, 100), 0600); err != nil {
			t.Fatal(err)
		}
	}
	m.mu.Lock()
	m.files["old.jpg"] = thumbnailFile{size: 100, at: time.Now().Add(-time.Hour)}
	m.files["new.jpg"] = thumbnailFile{size: 100, at: time.Now()}
	m.total = 200
	m.limit = 100
	calls := 0
	m.removeCache = func(string) error { calls++; return os.ErrPermission }
	m.evictLocked("")
	if calls != 1 || m.total != 200 || len(m.files) != 2 || !m.files["old.jpg"].at.After(time.Now().Add(-time.Minute)) {
		t.Fatalf("failed deletion corrupted index: calls=%d total=%d files=%v", calls, m.total, m.files)
	}
	m.removeCache = os.Remove
	m.evictLocked("new.jpg")
	if m.total != 100 || len(m.files) != 1 {
		t.Fatalf("retry deletion: total=%d files=%v", m.total, m.files)
	}
	m.mu.Unlock()
}

func TestThumbnailYCbCrFastPath(t *testing.T) {
	for _, ratio := range []image.YCbCrSubsampleRatio{image.YCbCrSubsampleRatio444, image.YCbCrSubsampleRatio422, image.YCbCrSubsampleRatio420} {
		img := image.NewYCbCr(image.Rect(3, 5, 12, 13), ratio)
		for y := 5; y < 13; y++ {
			for x := 3; x < 12; x++ {
				img.Y[img.YOffset(x, y)] = uint8(17*x + 9*y)
				i := img.COffset(x, y)
				img.Cb[i] = uint8(11*x + 7*y)
				img.Cr[i] = uint8(3*x + 19*y)
			}
		}
		for y := 5; y < 13; y++ {
			for x := 3; x < 12; x++ {
				r, g, b, a := sourceRGBA(img, x, y)
				er, eg, eb, ea := img.At(x, y).RGBA()
				if r != er || g != eg || b != eb || a != ea {
					t.Fatalf("%v %d,%d: fast=%v generic=%v", ratio, x, y, [4]uint32{r, g, b, a}, [4]uint32{er, eg, eb, ea})
				}
			}
		}
	}
}

func TestThumbnailDecodeConfigIOFailureRetry(t *testing.T) {
	m := newThumbnailManager(config.Config{DataDirectory: t.TempDir()})
	path := filepath.Join(t.TempDir(), "sample.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = png.Encode(f, image.NewRGBA(image.Rect(0, 0, 600, 300))); err != nil {
		t.Fatal(err)
	}
	f.Close()
	info, _ := os.Stat(path)
	calls := 0
	m.decodeConfig = func(f *os.File) (image.Config, string, error) {
		calls++
		if calls == 1 {
			return image.Config{}, "", os.ErrPermission
		}
		return image.DecodeConfig(f)
	}
	_, _, ok, err := m.thumbnail(context.Background(), "artwork", 1, mustOpenThumbnail(t, path), info, 256)
	if ok || !errors.Is(err, os.ErrPermission) || len(m.negative) != 0 {
		t.Fatalf("I/O failure cached: %v", err)
	}
	_, _, ok, err = m.thumbnail(context.Background(), "artwork", 1, mustOpenThumbnail(t, path), info, 256)
	if !ok || err != nil || calls != 2 {
		t.Fatalf("retry failed: %v calls=%d", err, calls)
	}
}

func TestThumbnailGIFCanvasBudget(t *testing.T) {
	pixels := int64(24_000_000)
	if pixels*(thumbnailBytesPerPixel(color.Palette{color.Black}))+thumbnailCanvasBytes("gif", pixels) != pixels*5 || thumbnailCanvasBytes("jpeg", pixels) != 0 {
		t.Fatal("GIF canvas not accounted")
	}
}
