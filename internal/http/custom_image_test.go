package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/config"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func solidImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 7), uint8(y * 5), 120, 255})
		}
	}
	return img
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, solidImage(w, h)); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func jpegBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, solidImage(w, h), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func gifBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := gif.Encode(&buf, solidImage(4, 4), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func webpBytes(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/tiny.webp")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// fakePNGHeader builds a structurally valid PNG header (correct IHDR CRC)
// claiming w×h pixels without any IDAT data. DecodeConfig succeeds; a full
// decode would fail — exactly the decompression-bomb guard shape.
func fakePNGHeader(t *testing.T, w, h uint32) []byte {
	t.Helper()
	chunk := func(typ string, data []byte) []byte {
		var out bytes.Buffer
		_ = binary.Write(&out, binary.BigEndian, uint32(len(data)))
		out.WriteString(typ)
		out.Write(data)
		crc := crc32.ChecksumIEEE(append([]byte(typ), data...))
		_ = binary.Write(&out, binary.BigEndian, crc)
		return out.Bytes()
	}
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], w)
	binary.BigEndian.PutUint32(ihdr[4:8], h)
	ihdr[8] = 8 // bit depth
	ihdr[9] = 6 // RGBA
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A})
	buf.Write(chunk("IHDR", ihdr))
	buf.Write(chunk("IEND", nil))
	return buf.Bytes()
}

// uploadRequest builds a multipart POST with csrfToken first (like the
// browser forms) followed by the file part.
func uploadRequest(t *testing.T, target, csrfToken, fileName string, data []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("csrfToken", csrfToken); err != nil {
		t.Fatal(err)
	}
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="image"; filename="%s"`, fileName))
	header.Set("Content-Type", "application/octet-stream")
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, target, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func noticeOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	location := rec.Header().Get("Location")
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("Location %q: %v", location, err)
	}
	return parsed.Query().Get("notice")
}

// uploadAlbumArtwork performs the upload and returns the recorder.
func uploadAlbumArtwork(t *testing.T, app *App, cookie *http.Cookie, albumID int64, csrf, fileName string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := uploadRequest(t, "/admin/albums/"+strconv.FormatInt(albumID, 10)+"/artwork", csrf, fileName, data)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	return rec
}

func firstAlbumID(t *testing.T, app *App) int64 {
	t.Helper()
	albums, err := app.store.ListAlbums(context.Background(), storage.Filters{Limit: 10})
	if err != nil || len(albums) == 0 {
		t.Fatalf("ListAlbums = %v, %v", albums, err)
	}
	return albums[0].ID
}

func firstArtistID(t *testing.T, app *App) int64 {
	t.Helper()
	artists, err := app.store.ListArtists(context.Background(), storage.Filters{Limit: 10})
	if err != nil || len(artists) == 0 {
		t.Fatalf("ListArtists = %v, %v", artists, err)
	}
	return artists[0].ID
}

func TestCustomImageUploadRequiresLogin(t *testing.T) {
	app, _, _ := setupTestApp(t)
	albumID := func() int64 { importTestTrack(t, app); return firstAlbumID(t, app) }()
	req := uploadRequest(t, "/admin/albums/"+strconv.FormatInt(albumID, 10)+"/artwork", "whatever", "cover.png", pngBytes(t, 8, 8))
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/admin/login") {
		t.Fatalf("status = %d location = %q, want login redirect", rec.Code, rec.Header().Get("Location"))
	}
}

func TestCustomImageUploadRequiresCSRF(t *testing.T) {
	app, cookie, _ := csrfSessionApp(t)
	importTestTrack(t, app)
	albumID := firstAlbumID(t, app)
	rec := uploadAlbumArtwork(t, app, cookie, albumID, "wrong-token", "cover.png", pngBytes(t, 8, 8))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}
	// The reset forms are urlencoded and must also enforce CSRF.
	form := strings.NewReader("csrfToken=wrong-token")
	req := httptest.NewRequest(http.MethodPost, "/admin/albums/"+strconv.FormatInt(albumID, 10)+"/artwork/reset", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	reset := httptest.NewRecorder()
	app.Handler().ServeHTTP(reset, req)
	if reset.Code != http.StatusForbidden {
		t.Fatalf("reset status = %d, want 403", reset.Code)
	}
}

func TestUploadAlbumArtworkFormats(t *testing.T) {
	formats := []struct {
		name     string
		fileName string
		data     func(t *testing.T) []byte
		mime     string
	}{
		{"PNG", "cover.png", func(t *testing.T) []byte { return pngBytes(t, 16, 16) }, "image/png"},
		{"JPEG", "cover.jpg", func(t *testing.T) []byte { return jpegBytes(t, 16, 16) }, "image/jpeg"},
		{"WebP", "cover.webp", webpBytes, "image/webp"},
		{"JPEGWithWrongExtension", "cover.png", func(t *testing.T) []byte { return jpegBytes(t, 16, 16) }, "image/jpeg"},
	}
	for _, format := range formats {
		t.Run(format.name, func(t *testing.T) {
			app, cookie, csrf := csrfSessionApp(t)
			importTestTrack(t, app)
			albumID := firstAlbumID(t, app)
			rec := uploadAlbumArtwork(t, app, cookie, albumID, csrf, format.fileName, format.data(t))
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if notice := noticeOf(t, rec); notice != "封面已更新" {
				t.Fatalf("notice = %q", notice)
			}
			album, err := app.store.AlbumByID(context.Background(), albumID)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(album.ArtworkURL, "/api/v1/artwork/") || !album.HasCustomArtwork {
				t.Fatalf("artwork URL = %q custom=%v", album.ArtworkURL, album.HasCustomArtwork)
			}
			// Only the bare file name is stored (M3); the file itself lives
			// under custom-images/, never the music dir.
			refs, err := app.store.CustomImageFiles(context.Background())
			if err != nil || len(refs) != 1 {
				t.Fatalf("refs = %v, %v", refs, err)
			}
			for name := range refs {
				if filepath.IsAbs(name) || strings.ContainsRune(name, os.PathSeparator) {
					t.Fatalf("stored reference must be a bare file name, got %q", name)
				}
				if _, err := os.Stat(filepath.Join(app.config.DataDirectory, "custom-images", name)); err != nil {
					t.Fatalf("custom image file missing: %v", err)
				}
			}
			// Serving the artwork returns the right mime and long-lived cache.
			req := httptest.NewRequest(http.MethodGet, album.ArtworkURL, nil)
			req.AddCookie(cookie)
			served := httptest.NewRecorder()
			app.Handler().ServeHTTP(served, req)
			if served.Code != http.StatusOK {
				t.Fatalf("GET artwork = %d", served.Code)
			}
			if ct := served.Header().Get("Content-Type"); ct != format.mime {
				t.Fatalf("Content-Type = %q, want %q", ct, format.mime)
			}
			if cc := served.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
				t.Fatalf("Cache-Control = %q", cc)
			}
			// Thumbnails go through the same pipeline (WebP included).
			thumb := httptest.NewRecorder()
			thumbReq := httptest.NewRequest(http.MethodGet, album.ArtworkURL+"?size=256", nil)
			thumbReq.AddCookie(cookie)
			app.Handler().ServeHTTP(thumb, thumbReq)
			if thumb.Code != http.StatusOK {
				t.Fatalf("GET thumbnail = %d", thumb.Code)
			}
		})
	}
}

func TestUploadAlbumArtworkRejectsBadContent(t *testing.T) {
	cases := []struct {
		name       string
		fileName   string
		data       func(t *testing.T) []byte
		wantNotice string
	}{
		{"TextFileWithPngExtension", "cover.png", func(t *testing.T) []byte {
			return []byte("this is definitely not an image, just plain text pretending")
		}, "仅支持 JPEG、PNG 或 WebP 格式的图片"},
		{"GIF", "cover.gif", gifBytes, "仅支持 JPEG、PNG 或 WebP 格式的图片"},
		{"TruncatedPNG", "cover.png", func(t *testing.T) []byte { return pngBytes(t, 16, 16)[:40] }, "图片文件损坏或无法解码"},
		{"HugeSideClaim", "cover.png", func(t *testing.T) []byte { return fakePNGHeader(t, 9000, 100) }, "图片尺寸超限：每边不超过 8192 像素，总像素不超过 4000 万"},
		{"HugeTotalPixels", "cover.png", func(t *testing.T) []byte { return fakePNGHeader(t, 7000, 7000) }, "图片尺寸超限：每边不超过 8192 像素，总像素不超过 4000 万"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, cookie, csrf := csrfSessionApp(t)
			importTestTrack(t, app)
			albumID := firstAlbumID(t, app)
			rec := uploadAlbumArtwork(t, app, cookie, albumID, csrf, tc.fileName, tc.data(t))
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if notice := noticeOf(t, rec); notice != tc.wantNotice {
				t.Fatalf("notice = %q, want %q", notice, tc.wantNotice)
			}
			album, err := app.store.AlbumByID(context.Background(), albumID)
			if err != nil {
				t.Fatal(err)
			}
			if album.HasCustomArtwork {
				t.Fatal("rejected upload must not create a custom cover")
			}
		})
	}
}

func TestUploadAlbumArtworkOversized(t *testing.T) {
	t.Run("file above 10MB gets friendly notice", func(t *testing.T) {
		app, cookie, csrf := csrfSessionApp(t)
		importTestTrack(t, app)
		albumID := firstAlbumID(t, app)
		// Valid PNG header followed by padding: 10.5MB, sniffed as PNG, but
		// over the 10MB image cap while under the 11MB body cap.
		data := append(pngBytes(t, 8, 8), make([]byte, 10<<20+512*1024)...)
		rec := uploadAlbumArtwork(t, app, cookie, albumID, csrf, "cover.png", data)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if notice := noticeOf(t, rec); notice != "图片不能超过 10MB" {
			t.Fatalf("notice = %q", notice)
		}
	})
	t.Run("body above 11MB gets 413", func(t *testing.T) {
		app, cookie, csrf := csrfSessionApp(t)
		importTestTrack(t, app)
		albumID := firstAlbumID(t, app)
		data := append(pngBytes(t, 8, 8), make([]byte, 12<<20)...)
		rec := uploadAlbumArtwork(t, app, cookie, albumID, csrf, "cover.png", data)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413, body = %s", rec.Code, rec.Body.String())
		}
	})
}

func TestUploadArtistImageVersionedAndReset(t *testing.T) {
	app, cookie, csrf := csrfSessionApp(t)
	importTestTrack(t, app)
	artistID := firstArtistID(t, app)
	detail, err := app.store.ArtistDetail(context.Background(), artistID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.ImageURL != "" {
		t.Fatalf("expected no image before upload, got %q", detail.ImageURL)
	}

	rec := uploadAlbumArtwork2(t, app, cookie, "/admin/artists/"+strconv.FormatInt(artistID, 10)+"/image", csrf, "artist.webp", webpBytes(t))
	if rec.Code != http.StatusSeeOther || noticeOf(t, rec) != "歌手图片已更新" {
		t.Fatalf("status = %d notice = %q", rec.Code, noticeOf(t, rec))
	}
	detail, err = app.store.ArtistDetail(context.Background(), artistID)
	if err != nil {
		t.Fatal(err)
	}
	versionPrefix := "/api/v1/artists/" + strconv.FormatInt(artistID, 10) + "/image?v="
	if !strings.HasPrefix(detail.ImageURL, versionPrefix) || !detail.HasCustomImage {
		t.Fatalf("image URL = %q custom=%v", detail.ImageURL, detail.HasCustomImage)
	}
	req := httptest.NewRequest(http.MethodGet, detail.ImageURL, nil)
	req.AddCookie(cookie)
	served := httptest.NewRecorder()
	app.Handler().ServeHTTP(served, req)
	if served.Code != http.StatusOK || served.Header().Get("Content-Type") != "image/webp" {
		t.Fatalf("GET image = %d %q", served.Code, served.Header().Get("Content-Type"))
	}
	if cc := served.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age=86400") {
		t.Fatalf("Cache-Control = %q", cc)
	}

	// Reset via urlencoded form restores the empty (default) image.
	form := strings.NewReader("csrfToken=" + url.QueryEscape(csrf) + "&returnTo=" + url.QueryEscape("/admin/artists/"+strconv.FormatInt(artistID, 10)))
	resetReq := httptest.NewRequest(http.MethodPost, "/admin/artists/"+strconv.FormatInt(artistID, 10)+"/image/reset", form)
	resetReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resetReq.AddCookie(cookie)
	reset := httptest.NewRecorder()
	app.Handler().ServeHTTP(reset, resetReq)
	if reset.Code != http.StatusSeeOther || noticeOf(t, reset) != "已恢复默认歌手图片" {
		t.Fatalf("reset status = %d notice = %q", reset.Code, noticeOf(t, reset))
	}
	detail, err = app.store.ArtistDetail(context.Background(), artistID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.ImageURL != "" || detail.HasCustomImage {
		t.Fatalf("after reset image = %q custom=%v", detail.ImageURL, detail.HasCustomImage)
	}
	// The uploaded file must be gone (nothing references it anymore).
	entries, err := os.ReadDir(filepath.Join(app.config.DataDirectory, "custom-images"))
	if err == nil {
		for _, entry := range entries {
			t.Fatalf("leftover custom image file: %s", entry.Name())
		}
	}
}

// uploadAlbumArtwork2 generalizes uploadAlbumArtwork to arbitrary targets.
func uploadAlbumArtwork2(t *testing.T, app *App, cookie *http.Cookie, target, csrf, fileName string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := uploadRequest(t, target, csrf, fileName, data)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	return rec
}

func TestUploadReplaceDeletesOldFileAndDedups(t *testing.T) {
	app, cookie, csrf := csrfSessionApp(t)
	importTestTrack(t, app)
	albumID := firstAlbumID(t, app)
	dir := filepath.Join(app.config.DataDirectory, "custom-images")

	pngData := pngBytes(t, 16, 16)
	if rec := uploadAlbumArtwork(t, app, cookie, albumID, csrf, "a.png", pngData); rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", rec.Code)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %v, %v", entries, err)
	}
	firstFile := entries[0].Name()
	if !strings.HasSuffix(firstFile, ".png") {
		t.Fatalf("first file = %q", firstFile)
	}
	// Replacing with a JPEG removes the old PNG file and stores a .jpg.
	if rec := uploadAlbumArtwork(t, app, cookie, albumID, csrf, "b.jpg", jpegBytes(t, 16, 16)); rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", rec.Code)
	}
	entries, err = os.ReadDir(dir)
	if err != nil || len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".jpg") {
		t.Fatalf("entries after replace = %v, %v", entries, err)
	}
	// Re-uploading the same JPEG dedups: same path, no extra file.
	if rec := uploadAlbumArtwork(t, app, cookie, albumID, csrf, "b2.jpg", jpegBytes(t, 16, 16)); rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", rec.Code)
	}
	entries, err = os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries after dedup upload = %v, %v", entries, err)
	}
}

func TestCustomImageGC(t *testing.T) {
	app, cookie, csrf := csrfSessionApp(t)
	importTestTrack(t, app)
	albumID := firstAlbumID(t, app)
	dir := filepath.Join(app.config.DataDirectory, "custom-images")
	if rec := uploadAlbumArtwork(t, app, cookie, albumID, csrf, "a.png", pngBytes(t, 16, 16)); rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", rec.Code)
	}
	// One referenced file; add an old orphan and a fresh orphan.
	old := time.Now().Add(-2 * time.Hour)
	oldOrphan := filepath.Join(dir, "deadbeef.png")
	freshOrphan := filepath.Join(dir, "cafef00d.png")
	if err := os.WriteFile(oldOrphan, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(oldOrphan, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(freshOrphan, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	app.gcCustomImages(context.Background())
	if _, err := os.Stat(oldOrphan); !os.IsNotExist(err) {
		t.Fatal("old orphan should be collected")
	}
	if _, err := os.Stat(freshOrphan); err != nil {
		t.Fatal("fresh orphan must survive (concurrent upload protection)")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var referenced int
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".png") && entry.Name() != "cafef00d.png" {
			referenced++
		}
	}
	if referenced != 1 {
		t.Fatalf("referenced files = %d, want 1 (entries %v)", referenced, entries)
	}
}

func TestUploadForMissingAlbumRejected(t *testing.T) {
	app, cookie, csrf := csrfSessionApp(t)
	importTestTrack(t, app)
	rec := uploadAlbumArtwork(t, app, cookie, 424242, csrf, "cover.png", pngBytes(t, 8, 8))
	if rec.Code != http.StatusSeeOther || noticeOf(t, rec) != "专辑不存在" {
		t.Fatalf("status = %d notice = %q", rec.Code, noticeOf(t, rec))
	}
	// L1: the target check runs before any file is written.
	entries, err := os.ReadDir(filepath.Join(app.config.DataDirectory, "custom-images"))
	if err == nil && len(entries) != 0 {
		t.Fatalf("no file should be written for a missing album, got %v", entries)
	}
}

func largeWebpBytes(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/cover-large.webp")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestWebPCoverThumbnailGenerated (M2): a >256px WebP custom cover must get a
// real JPEG thumbnail, not the original-file fallback.
func TestWebPCoverThumbnailGenerated(t *testing.T) {
	app, cookie, csrf := csrfSessionApp(t)
	importTestTrack(t, app)
	albumID := firstAlbumID(t, app)
	rec := uploadAlbumArtwork(t, app, cookie, albumID, csrf, "cover.webp", largeWebpBytes(t))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", rec.Code)
	}
	album, err := app.store.AlbumByID(context.Background(), albumID)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, album.ArtworkURL+"?size=256", nil)
	req.AddCookie(cookie)
	served := httptest.NewRecorder()
	app.Handler().ServeHTTP(served, req)
	if served.Code != http.StatusOK {
		t.Fatalf("GET thumbnail = %d", served.Code)
	}
	// The thumbnail pipeline always emits JPEG; the WebP original would come
	// back as image/webp.
	if ct := served.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("Content-Type = %q, want image/jpeg (generated thumbnail)", ct)
	}
	img, err := jpeg.Decode(served.Body)
	if err != nil {
		t.Fatalf("decode thumbnail: %v", err)
	}
	if bounds := img.Bounds(); bounds.Dx() > 256 || bounds.Dy() > 256 {
		t.Fatalf("thumbnail bounds = %v, want <= 256px", bounds)
	}
}

// TestCustomImageSurvivesDataDirRespelling (M3): because the database stores
// only file names, re-spelling the data directory (different drive-letter
// case on Windows) neither breaks reads nor makes the GC eat referenced files.
func TestCustomImageSurvivesDataDirRespelling(t *testing.T) {
	app, cookie, csrf := csrfSessionApp(t)
	importTestTrack(t, app)
	albumID := firstAlbumID(t, app)
	if rec := uploadAlbumArtwork(t, app, cookie, albumID, csrf, "a.png", pngBytes(t, 16, 16)); rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", rec.Code)
	}
	original := app.config.DataDirectory
	t.Cleanup(func() { app.config.DataDirectory = original })
	// Make every referenced file look old: without name-only storage the GC
	// would compare differently-spelled absolute paths and delete them.
	dir := filepath.Join(app.config.DataDirectory, "custom-images")
	old := time.Now().Add(-2 * time.Hour)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %v, %v", entries, err)
	}
	if err := os.Chtimes(filepath.Join(dir, entries[0].Name()), old, old); err != nil {
		t.Fatal(err)
	}
	// Re-spell the directory: drive-letter case flip on Windows; elsewhere a
	// no-op keeps the test meaningful but weaker.
	if len(original) >= 2 && original[1] == ':' {
		c := original[0]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		} else {
			c -= 'a' - 'A'
		}
		app.config.DataDirectory = string(c) + original[1:]
	}
	app.gcCustomImages(context.Background())
	if _, err := os.Stat(filepath.Join(dir, entries[0].Name())); err != nil {
		t.Fatalf("referenced file collected after dir respelling: %v", err)
	}
	album, err := app.store.AlbumByID(context.Background(), albumID)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, album.ArtworkURL, nil)
	req.AddCookie(cookie)
	served := httptest.NewRecorder()
	app.Handler().ServeHTTP(served, req)
	if served.Code != http.StatusOK {
		t.Fatalf("GET artwork after respelling = %d", served.Code)
	}
}

// TestCustomImageDedupRefreshesMtime (M4): a dedup hit on an old,
// currently-unreferenced file must refresh its mtime so the orphan GC cannot
// delete it in the store->commit window.
func TestCustomImageDedupRefreshesMtime(t *testing.T) {
	app, cookie, csrf := csrfSessionApp(t)
	importTestTrack(t, app)
	albumID := firstAlbumID(t, app)
	data := pngBytes(t, 16, 16)
	// Pre-place the exact dedup target as an old orphan.
	digest := sha256.New()
	digest.Write([]byte("custom:"))
	digest.Write(data)
	name := hex.EncodeToString(digest.Sum(nil)) + ".png"
	dir := filepath.Join(app.config.DataDirectory, "custom-images")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o640); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if rec := uploadAlbumArtwork(t, app, cookie, albumID, csrf, "a.png", data); rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", rec.Code)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(info.ModTime()) > time.Minute {
		t.Fatalf("dedup hit did not refresh mtime: %v", info.ModTime())
	}
}

// TestCustomImageConcurrentUploads (M4): parallel uploads of the same and of
// different content all succeed and leave a consistent store.
func TestCustomImageConcurrentUploads(t *testing.T) {
	app, cookie, csrf := csrfSessionApp(t)
	importTestTrack(t, app)
	albumID := firstAlbumID(t, app)
	shared := pngBytes(t, 16, 16)
	var wg sync.WaitGroup
	errs := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			data := shared
			if i%2 == 1 {
				data = jpegBytes(t, 8+i, 8+i)
			}
			if rec := uploadAlbumArtwork(t, app, cookie, albumID, csrf, fmt.Sprintf("c%d.png", i), data); rec.Code != http.StatusSeeOther {
				errs <- fmt.Sprintf("goroutine %d status = %d", i, rec.Code)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Fatal(msg)
	}
	album, err := app.store.AlbumByID(context.Background(), albumID)
	if err != nil || !album.HasCustomArtwork {
		t.Fatalf("final album = %v, %v", album, err)
	}
	refs, err := app.store.CustomImageFiles(context.Background())
	if err != nil || len(refs) != 1 {
		t.Fatalf("refs = %v, %v, want exactly one referenced file", refs, err)
	}
	for name := range refs {
		if _, err := os.Stat(filepath.Join(app.config.DataDirectory, "custom-images", name)); err != nil {
			t.Fatalf("referenced file missing after concurrent uploads: %v", err)
		}
	}
}

// setupRelativeDataApp builds an app whose data directory is RELATIVE (like
// scripts/dev.ps1's ./.local/data), after chdir-ing into a throwaway dir.
func setupRelativeDataApp(t *testing.T) (*App, *http.Cookie, string) {
	t.Helper()
	t.Chdir(t.TempDir())
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "rel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	token := "valid-test-token-at-least-24-chars"
	cfg := config.Config{APIToken: token, MediaToken: token, CookieSecure: false, DataDirectory: "data"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app, err := NewApp(cfg, store, nil, nil, logger, "1.0.0-test")
	if err != nil {
		t.Fatal(err)
	}
	loginRec := httptest.NewRecorder()
	session, err := app.sessions.create(loginRec, "admin")
	if err != nil {
		t.Fatal(err)
	}
	cookies := loginRec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("login did not set a session cookie")
	}
	return app, cookies[0], session.CSRFToken
}

// TestRelativeDataDirServesAllImageKinds (B1): with a relative data dir,
// scanner artwork rows and automatic artist cache rows also carry relative
// paths — they must be served as-is, never resolved against custom-images/.
// Custom covers (bare file names) must still resolve correctly. All three
// kinds return 200.
func TestRelativeDataDirServesAllImageKinds(t *testing.T) {
	app, cookie, csrf := setupRelativeDataApp(t)
	importTestTrack(t, app)
	albumID := firstAlbumID(t, app)
	artistID := firstArtistID(t, app)
	ctx := context.Background()

	get := func(target string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		return rec
	}

	// 1) Scanner-style embedded artwork with a relative source_path (the
	// scanner stores its cache path verbatim, which is relative when the
	// data dir is relative).
	jpegData := jpegBytes(t, 8, 8)
	if err := os.MkdirAll("artwork", 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("artwork/embedded.jpg", jpegData, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := app.store.ImportTrack(ctx, storage.ImportInput{LibraryID: 1, RelativePath: "Artist/Album/01.flac", FileSize: 1024, ModifiedAtNS: 2, Metadata: metadata.AudioMetadata{Title: "Track", Album: "Album", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 1}, Artwork: &storage.ArtworkInput{Hash: "rel-embedded-hash", MIMEType: "image/jpeg", CachePath: "artwork/embedded.jpg", SourceType: "embedded", ByteSize: int64(len(jpegData))}}); err != nil {
		t.Fatal(err)
	}
	album, err := app.store.AlbumByID(ctx, albumID)
	if err != nil {
		t.Fatal(err)
	}
	if rec := get(album.ArtworkURL); rec.Code != http.StatusOK {
		t.Fatalf("scanner artwork with relative path = %d (%s), want 200", rec.Code, album.ArtworkURL)
	}

	// 2) Automatic artist image (Last.fm/MusicBrainz cache) with a relative
	// cache_path.
	if err := os.MkdirAll("artist-cache", 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("artist-cache/img.jpg", jpegData, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := app.store.SaveArtistImage(ctx, storage.ArtistImageInput{ArtistID: artistID, Source: "lastfm", RemoteURL: "https://img", Hash: "h", MIMEType: "image/jpeg", CachePath: "artist-cache/img.jpg", ByteSize: int64(len(jpegData))}); err != nil {
		t.Fatal(err)
	}
	if rec := get("/api/v1/artists/" + strconv.FormatInt(artistID, 10) + "/image"); rec.Code != http.StatusOK {
		t.Fatalf("artist cache image with relative path = %d, want 200", rec.Code)
	}

	// 3) Custom cover upload into the relative data dir.
	if rec := uploadAlbumArtwork(t, app, cookie, albumID, csrf, "cover.png", pngBytes(t, 8, 8)); rec.Code != http.StatusSeeOther {
		t.Fatalf("upload status = %d", rec.Code)
	}
	album, err = app.store.AlbumByID(ctx, albumID)
	if err != nil {
		t.Fatal(err)
	}
	if rec := get(album.ArtworkURL); rec.Code != http.StatusOK {
		t.Fatalf("custom cover with relative data dir = %d, want 200", rec.Code)
	}
}
