package httpapi

import (
	"errors"
	"github.com/lux032/032music-server/internal/storage"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func (a *App) playlistArtworkResponse(w http.ResponseWriter, r *http.Request, id int64) {
	p, err := a.store.PlaylistByID(r.Context(), id)
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	writeJSON(w, 200, map[string]any{"artworkUrl": p.ArtworkURL, "revision": p.Revision, "hasCustomArtwork": p.HasCustomArtwork})
}
func (a *App) handleUploadPlaylistArtwork(w http.ResponseWriter, r *http.Request) {
	// The API middleware checks Bearer credentials or session + CSRF header
	// before parsing multipart. No redirect/reload is used on this endpoint.
	r.Body = http.MaxBytesReader(w, r.Body, customImageBodyLimit)
	if err := r.ParseMultipartForm(customImageMaxBytes); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeAPIError(w, 413, "image_too_large", errCustomImageTooLarge.Error())
		} else {
			writeAPIError(w, 400, "invalid_request", "Invalid multipart upload.")
		}
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, _, err := r.FormFile("image")
	if err != nil {
		writeAPIError(w, 400, "invalid_image", errCustomImageMissing.Error())
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, customImageMaxBytes+1))
	if err != nil {
		writeAPIError(w, 400, "invalid_image", "Cannot read image.")
		return
	}
	select {
	case a.thumbnails.slots <- struct{}{}:
	case <-r.Context().Done():
		return
	}
	upload, err := validateCustomImage(data)
	<-a.thumbnails.slots
	if err != nil {
		status := 400
		if errors.Is(err, errCustomImageTooLarge) {
			status = 413
		}
		writeAPIError(w, status, "invalid_image", err.Error())
		return
	}
	id := parseInt64(r.PathValue("id"))
	a.customImageMu.Lock()
	defer a.customImageMu.Unlock()
	if _, err = a.store.PlaylistByID(r.Context(), id); err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	hash, name, err := a.storeCustomImage(upload.data, upload.extension)
	if err != nil {
		a.writeFeatureError(w, r, err, "image_save_failed")
		return
	}
	names, err := a.store.SaveCustomPlaylistImage(r.Context(), id, storage.CustomImageInput{Hash: hash, FileName: name, MIMEType: upload.mimeType, Width: upload.width, Height: upload.height, ByteSize: int64(len(data))})
	if err != nil {
		a.removeUnreferenced(r.Context(), []string{name})
		a.writeFeatureError(w, r, err, "image_save_failed")
		return
	}
	a.removeUnreferenced(r.Context(), names)
	a.gcCustomImages(r.Context())
	a.playlistArtworkResponse(w, r, id)
}
func (a *App) handleResetPlaylistArtwork(w http.ResponseWriter, r *http.Request) {
	id := parseInt64(r.PathValue("id"))
	a.customImageMu.Lock()
	defer a.customImageMu.Unlock()
	names, err := a.store.ResetCustomPlaylistImage(r.Context(), id)
	if err != nil {
		a.writeFeatureError(w, r, err, "image_reset_failed")
		return
	}
	a.removeUnreferenced(r.Context(), names)
	a.playlistArtworkResponse(w, r, id)
}
func (a *App) handlePlaylistArtwork(w http.ResponseWriter, r *http.Request) {
	size, err := thumbnailSize(r)
	if err != nil {
		writeAPIError(w, 400, "invalid_request", "Invalid size.")
		return
	}
	id := parseInt64(r.PathValue("id"))
	img, err := a.store.PlaylistImage(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	version := img.Hash
	if len(version) > 16 {
		version = version[:16]
	}
	requested := r.URL.Query().Get("v")
	cache := "no-cache"
	// Never serve new bytes at an old immutable URL.
	if requested != "" && requested != version {
		http.NotFound(w, r)
		return
	}
	if requested == version {
		cache = "public, max-age=31536000, immutable"
	}
	if filepath.Base(img.FileName) != img.FileName || strings.ContainsAny(img.FileName, "/\\") {
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(filepath.Join(a.customImagesDir(), img.FileName))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if size != 0 && a.serveThumbnail(w, r, file, info, "playlist", id, size, cache) {
		return
	}
	w.Header().Set("Content-Type", img.MIMEType)
	w.Header().Set("Cache-Control", cache)
	w.Header().Set("ETag", "\""+img.Hash+"\"")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}
