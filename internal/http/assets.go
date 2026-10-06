package httpapi

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
)

// assetRegistry serves the embedded admin assets. At startup every file is
// read once, a content-addressed build hash is computed over the whole
// directory, and both the plain and gzip-compressed bodies are kept in
// memory. Requests to /admin/assets/{hash}/{file} are immutable and may be
// cached forever; the legacy /admin/assets/{file} path stays available with
// no-cache + ETag so HTML referencing it always revalidates cheaply.
type assetRegistry struct {
	hash  string
	files map[string]*assetFile
}

type assetFile struct {
	contentType string
	etag        string
	body        []byte
	gzipBody    []byte
}

func newAssetRegistry(fsys fs.FS, dir string) (*assetRegistry, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("read embedded assets: %w", err)
	}
	registry := &assetRegistry{files: make(map[string]*assetFile)}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	digest := sha256.New()
	for _, name := range names {
		data, err := fs.ReadFile(fsys, dir+"/"+name)
		if err != nil {
			return nil, fmt.Errorf("read embedded asset %s: %w", name, err)
		}
		digest.Write([]byte(name))
		digest.Write([]byte{0})
		digest.Write(data)
		digest.Write([]byte{0})

		var compressed bytes.Buffer
		gzipWriter := gzip.NewWriter(&compressed)
		if _, err := gzipWriter.Write(data); err != nil {
			return nil, fmt.Errorf("gzip asset %s: %w", name, err)
		}
		if err := gzipWriter.Close(); err != nil {
			return nil, fmt.Errorf("gzip asset %s: %w", name, err)
		}

		contentType := mime.TypeByExtension(path.Ext(name))
		if contentType == "" {
			contentType = http.DetectContentType(data)
		}
		if strings.HasPrefix(contentType, "text/") && !strings.Contains(contentType, "charset") {
			contentType += "; charset=utf-8"
		}
		file := &assetFile{contentType: contentType, body: data}
		// Already-compressed files (PNG, ICO, WOFF2) gain nothing from gzip;
		// keep the gzip body only when it actually saves bytes.
		if compressed.Len() < len(data)*9/10 {
			file.gzipBody = compressed.Bytes()
		}
		registry.files[name] = file
	}
	registry.hash = hex.EncodeToString(digest.Sum(nil))[:12]
	for _, file := range registry.files {
		file.etag = `"` + registry.hash + `"`
	}
	return registry, nil
}

// assetURL returns the immutable, content-addressed URL for an asset name.
func (r *assetRegistry) assetURL(name string) string {
	return "/admin/assets/" + r.hash + "/" + name
}

// serveAssets handles the whole /admin/assets/ subtree. The versioned
// form /admin/assets/{hash}/{file} is immutable and may be cached forever;
// a stale or unknown hash redirects to the current content-addressed URL so
// outdated HTML always converges on fresh content. Anything else is treated
// as the legacy form /admin/assets/{file}: always revalidate, but a matching
// ETag answers with a cheap 304. Dispatch lives in the handler (instead of
// separate ServeMux patterns) because a "{file...}" wildcard makes the mux
// 307-redirect single-segment legacy requests to a trailing-slash URL.
func (r *assetRegistry) serveAssets(w http.ResponseWriter, req *http.Request) {
	rest := strings.TrimPrefix(req.URL.Path, "/admin/assets/")
	if first, remainder, found := strings.Cut(rest, "/"); found {
		if first == r.hash {
			r.serveNamed(w, req, remainder, "public, max-age=31536000, immutable")
			return
		}
		if looksLikeAssetHash(first) {
			if _, ok := r.files[path.Clean("/" + remainder)[1:]]; !ok {
				http.NotFound(w, req)
				return
			}
			target := r.assetURL(remainder)
			if req.URL.RawQuery != "" {
				target += "?" + req.URL.RawQuery
			}
			http.Redirect(w, req, target, http.StatusFound)
			return
		}
	}
	r.serveNamed(w, req, rest, "no-cache")
}

// looksLikeAssetHash reports whether the path segment has the shape of a
// build hash, so typos like /admin/assets/admin.css/extra fall through to a
// plain 404 instead of a confusing redirect loop.
func looksLikeAssetHash(segment string) bool {
	if len(segment) != 12 {
		return false
	}
	for _, c := range segment {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (r *assetRegistry) serveNamed(w http.ResponseWriter, req *http.Request, name, cacheControl string) {
	name = path.Clean("/" + name)[1:]
	file, ok := r.files[name]
	if !ok || name == "" {
		http.NotFound(w, req)
		return
	}
	r.serve(w, req, file, cacheControl)
}

func (r *assetRegistry) serve(w http.ResponseWriter, req *http.Request, file *assetFile, cacheControl string) {
	header := w.Header()
	header.Set("Content-Type", file.contentType)
	header.Set("Cache-Control", cacheControl)
	header.Set("ETag", file.etag)
	header.Add("Vary", "Accept-Encoding")
	if etagMatches(req.Header.Get("If-None-Match"), file.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body := file.body
	if file.gzipBody != nil && acceptsGzip(req.Header.Get("Accept-Encoding")) {
		header.Set("Content-Encoding", "gzip")
		body = file.gzipBody
	}
	header.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if req.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

func etagMatches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		// Weak validators (W/...) are sufficient for cache revalidation.
		candidate = strings.TrimPrefix(candidate, "W/")
		if candidate == etag || candidate == "*" {
			return true
		}
	}
	return false
}

func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
			continue
		}
		// An explicit q=0 means the client refuses gzip.
		for _, param := range strings.Split(params, ";") {
			key, value, ok := strings.Cut(strings.TrimSpace(param), "=")
			if !ok || !strings.EqualFold(strings.TrimSpace(key), "q") {
				continue
			}
			if q, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil && q <= 0 {
				return false
			}
		}
		return true
	}
	return false
}
