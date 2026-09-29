package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	_ "image/gif" // registered so uploads of GIF fail with a clear decode/format error
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	_ "golang.org/x/image/webp" // WebP decode for uploads and thumbnails (D28)

	"github.com/lux032/032music-server/internal/storage"
)

// 4.5.7 custom album covers and artist images. Only admins with a valid CSRF
// token may upload; music files are never modified — files live under
// <data>/custom-images/<sha256>.<ext>, deduplicated by content hash.
const (
	// customImageMaxBytes is the accepted image size (10MB); the request body
	// limit leaves ~1MB of headroom for multipart framing so a slightly
	// oversized file still produces the friendly notice instead of a 413.
	customImageMaxBytes  = 10 << 20
	customImageBodyLimit = 11 << 20
	// D29: per-side and total-pixel limits, enforced from DecodeConfig before
	// any full decode so decompression bombs are rejected cheaply.
	customImageMaxDimension = 8192
	customImageMaxPixels    = 40_000_000
	// Orphaned files younger than this are left alone so a concurrent upload
	// that just wrote its file but has not committed its row yet is safe.
	customImageOrphanAge = time.Hour
)

var (
	errCustomImageTooLarge   = errors.New("图片不能超过 10MB")
	errCustomImageMissing    = errors.New("请选择要上传的图片文件")
	errCustomImageFormat     = errors.New("仅支持 JPEG、PNG 或 WebP 格式的图片")
	errCustomImageDimensions = errors.New("图片尺寸超限：每边不超过 8192 像素，总像素不超过 4000 万")
	errCustomImageDecode     = errors.New("图片文件损坏或无法解码")
)

type customImageUpload struct {
	data          []byte
	mimeType      string
	extension     string
	width, height int
}

func (a *App) customImagesDir() string {
	return filepath.Join(a.config.DataDirectory, "custom-images")
}

// sniffImageFormat identifies JPEG/PNG/WebP from magic bytes; Content-Type
// headers and file extensions are never trusted.
func sniffImageFormat(data []byte) string {
	switch {
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "jpeg"
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return "png"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "webp"
	}
	return ""
}

// validateCustomImage checks size, format (by content) and the D29 pixel
// limits, then fully decodes once to prove the file is intact. The pixel
// limit check runs on DecodeConfig output before the full decode.
func validateCustomImage(data []byte) (*customImageUpload, error) {
	if len(data) == 0 {
		return nil, errCustomImageMissing
	}
	if len(data) > customImageMaxBytes {
		return nil, errCustomImageTooLarge
	}
	if sniffImageFormat(data) == "" {
		return nil, errCustomImageFormat
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, errCustomImageDecode
	}
	switch format {
	case "jpeg", "png", "webp":
	default:
		return nil, errCustomImageFormat
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > customImageMaxDimension || config.Height > customImageMaxDimension || int64(config.Width)*int64(config.Height) > customImageMaxPixels {
		return nil, errCustomImageDimensions
	}
	if _, _, err = image.Decode(bytes.NewReader(data)); err != nil {
		return nil, errCustomImageDecode
	}
	extension := map[string]string{"jpeg": ".jpg", "png": ".png", "webp": ".webp"}[format]
	return &customImageUpload{data: data, mimeType: "image/" + format, extension: extension, width: config.Width, height: config.Height}, nil
}

// readCustomImageUpload caps the request body, parses the multipart form,
// verifies CSRF and validates the "image" file field. It returns ok=false
// after writing the response. backPath is the validated returnTo target for
// redirects.
func (a *App) readCustomImageUpload(w http.ResponseWriter, r *http.Request, fallback string) (*customImageUpload, string, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, customImageBodyLimit)
	if err := r.ParseMultipartForm(customImageBodyLimit); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, errCustomImageTooLarge.Error(), http.StatusRequestEntityTooLarge)
			return nil, "", false
		}
		redirectWithNotice(w, r, fallback, "无法解析上传内容，请重试")
		return nil, "", false
	}
	back := safeAdminReturnTo(r.FormValue("returnTo"), fallback)
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return nil, "", false
	}
	file, _, err := r.FormFile("image")
	if err != nil {
		redirectWithNotice(w, r, back, errCustomImageMissing.Error())
		return nil, "", false
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, customImageMaxBytes+1))
	if err != nil {
		redirectWithNotice(w, r, back, "读取上传文件失败，请重试")
		return nil, "", false
	}
	// The full decode inside validation shares the thumbnail concurrency
	// slots (L7): uploads cannot exhaust memory alongside thumbnail work.
	a.thumbnails.slots <- struct{}{}
	upload, err := validateCustomImage(data)
	<-a.thumbnails.slots
	if err != nil {
		redirectWithNotice(w, r, back, err.Error())
		return nil, "", false
	}
	return upload, back, true
}

// GCCustomImages runs the custom-image orphan GC; called at startup and after
// each completed scan (L2) so files orphaned by merges and cascading deletes
// are collected even without upload activity.
func (a *App) GCCustomImages(ctx context.Context) {
	a.customImageMu.Lock()
	defer a.customImageMu.Unlock()
	a.gcCustomImages(ctx)
}

// storeCustomImage writes the file under custom-images/<sha256>.<ext>,
// deduplicating by content hash, and returns the hash and the bare file name
// (<hash>.<ext>); only the name is ever stored in the database (M3) so the
// data directory can move. The hash is namespaced ("custom:" written before
// the content) so it can never collide with scanner-cached artwork hashes in
// the artworks content index. Writes go through a temp file + rename so a
// concurrent reader never sees a partial file. A dedup hit refreshes the
// modification time (M4) so the GC never collects a still-referenced file as
// a stale orphan.
func (a *App) storeCustomImage(data []byte, extension string) (string, string, error) {
	digest := sha256.New()
	digest.Write([]byte("custom:"))
	digest.Write(data)
	hash := hex.EncodeToString(digest.Sum(nil))
	dir := a.customImagesDir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", "", err
	}
	name := hash + extension
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		now := time.Now()
		if err := os.Chtimes(path, now, now); err != nil {
			a.logger.Warn("refresh custom image mtime failed", "path", path, "error", err)
		}
		return hash, name, nil
	}
	tmp, err := os.CreateTemp(dir, "upload-*.tmp")
	if err != nil {
		return "", "", err
	}
	tmpName := tmp.Name()
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", "", err
	}
	if err = tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", "", err
	}
	if err = os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return "", "", err
	}
	return hash, name, nil
}

// removeUnreferenced deletes files that are no longer referenced by any
// custom-image row (the same content may be shared by another album/artist).
// names are bare file names under custom-images/.
func (a *App) removeUnreferenced(ctx context.Context, names []string) {
	if len(names) == 0 {
		return
	}
	refs, err := a.store.CustomImageFiles(ctx)
	if err != nil {
		a.logger.Warn("custom image reference check failed", "error", err)
		return
	}
	for _, name := range names {
		if name == "" || refs[name] {
			continue
		}
		path := filepath.Join(a.customImagesDir(), name)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			a.logger.Warn("remove replaced custom image failed", "path", path, "error", err)
		}
	}
}

// gcCustomImages removes files under custom-images/ that no row references.
// Only files older than customImageOrphanAge are removed so files written by
// an in-flight upload (row not yet committed) survive.
func (a *App) gcCustomImages(ctx context.Context) {
	dir := a.customImagesDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	refs, err := a.store.CustomImageFiles(ctx)
	if err != nil {
		a.logger.Warn("custom image GC reference check failed", "error", err)
		return
	}
	cutoff := time.Now().Add(-customImageOrphanAge)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if refs[entry.Name()] {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			a.logger.Warn("custom image GC remove failed", "path", path, "error", err)
		}
	}
}

func (a *App) handleUploadAlbumArtwork(w http.ResponseWriter, r *http.Request) {
	id := parseInt64(r.PathValue("id"))
	fallback := "/admin/albums/" + strconv.FormatInt(id, 10)
	upload, back, ok := a.readCustomImageUpload(w, r, fallback)
	if !ok {
		return
	}
	// M4: store file -> commit row -> cleanup runs fully serialized.
	a.customImageMu.Lock()
	defer a.customImageMu.Unlock()
	// L1: reject unknown targets before touching the disk.
	exists, err := a.store.AlbumExists(r.Context(), id)
	if err != nil {
		a.logger.Error("check album failed", "albumId", id, "error", err)
		redirectWithNotice(w, r, back, "保存封面失败，请重试")
		return
	}
	if !exists {
		redirectWithNotice(w, r, back, "专辑不存在")
		return
	}
	hash, name, err := a.storeCustomImage(upload.data, upload.extension)
	if err != nil {
		a.logger.Error("store custom album artwork failed", "albumId", id, "error", err)
		redirectWithNotice(w, r, back, "保存图片文件失败，请重试")
		return
	}
	_, oldNames, err := a.store.SaveCustomAlbumArtwork(r.Context(), id, storage.CustomImageInput{Hash: hash, MIMEType: upload.mimeType, FileName: name, Width: upload.width, Height: upload.height, ByteSize: int64(len(upload.data))})
	if err != nil {
		// L1: do not leave the just-written file behind.
		a.removeUnreferenced(r.Context(), []string{name})
		if errors.Is(err, storage.ErrCustomImageTarget) {
			redirectWithNotice(w, r, back, "专辑不存在")
			return
		}
		a.logger.Error("save custom album artwork failed", "albumId", id, "error", err)
		redirectWithNotice(w, r, back, "保存封面失败，请重试")
		return
	}
	a.removeUnreferenced(r.Context(), oldNames)
	a.gcCustomImages(r.Context())
	redirectWithNotice(w, r, back, "封面已更新")
}

func (a *App) handleResetAlbumArtwork(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id := parseInt64(r.PathValue("id"))
	a.customImageMu.Lock()
	defer a.customImageMu.Unlock()
	oldNames, err := a.store.ResetCustomAlbumArtwork(r.Context(), id)
	if err != nil {
		a.logger.Error("reset custom album artwork failed", "albumId", id, "error", err)
		redirectWithNotice(w, r, safeAdminReturnTo(r.FormValue("returnTo"), "/admin/albums/"+strconv.FormatInt(id, 10)), "恢复默认封面失败，请重试")
		return
	}
	a.removeUnreferenced(r.Context(), oldNames)
	a.gcCustomImages(r.Context())
	redirectWithNotice(w, r, safeAdminReturnTo(r.FormValue("returnTo"), "/admin/albums/"+strconv.FormatInt(id, 10)), "已恢复默认封面")
}

func (a *App) handleUploadArtistImage(w http.ResponseWriter, r *http.Request) {
	id := parseInt64(r.PathValue("id"))
	fallback := "/admin/artists/" + strconv.FormatInt(id, 10)
	upload, back, ok := a.readCustomImageUpload(w, r, fallback)
	if !ok {
		return
	}
	// M4: store file -> commit row -> cleanup runs fully serialized.
	a.customImageMu.Lock()
	defer a.customImageMu.Unlock()
	// L1: reject unknown targets before touching the disk.
	exists, err := a.store.ArtistExists(r.Context(), id)
	if err != nil {
		a.logger.Error("check artist failed", "artistId", id, "error", err)
		redirectWithNotice(w, r, back, "保存歌手图片失败，请重试")
		return
	}
	if !exists {
		redirectWithNotice(w, r, back, "歌手不存在")
		return
	}
	hash, name, err := a.storeCustomImage(upload.data, upload.extension)
	if err != nil {
		a.logger.Error("store custom artist image failed", "artistId", id, "error", err)
		redirectWithNotice(w, r, back, "保存图片文件失败，请重试")
		return
	}
	oldName, err := a.store.SaveCustomArtistImage(r.Context(), id, storage.CustomImageInput{Hash: hash, MIMEType: upload.mimeType, FileName: name, Width: upload.width, Height: upload.height, ByteSize: int64(len(upload.data))})
	if err != nil {
		// L1: do not leave the just-written file behind.
		a.removeUnreferenced(r.Context(), []string{name})
		if errors.Is(err, storage.ErrCustomImageTarget) {
			redirectWithNotice(w, r, back, "歌手不存在")
			return
		}
		a.logger.Error("save custom artist image failed", "artistId", id, "error", err)
		redirectWithNotice(w, r, back, "保存歌手图片失败，请重试")
		return
	}
	a.removeUnreferenced(r.Context(), []string{oldName})
	a.gcCustomImages(r.Context())
	redirectWithNotice(w, r, back, "歌手图片已更新")
}

func (a *App) handleResetArtistImage(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id := parseInt64(r.PathValue("id"))
	a.customImageMu.Lock()
	defer a.customImageMu.Unlock()
	oldNames, err := a.store.ResetCustomArtistImage(r.Context(), id)
	if err != nil {
		a.logger.Error("reset custom artist image failed", "artistId", id, "error", err)
		redirectWithNotice(w, r, safeAdminReturnTo(r.FormValue("returnTo"), "/admin/artists/"+strconv.FormatInt(id, 10)), "恢复默认歌手图片失败，请重试")
		return
	}
	a.removeUnreferenced(r.Context(), oldNames)
	a.gcCustomImages(r.Context())
	redirectWithNotice(w, r, safeAdminReturnTo(r.FormValue("returnTo"), "/admin/artists/"+strconv.FormatInt(id, 10)), "已恢复默认歌手图片")
}
