package httpapi

import (
	"bufio"
	"compress/gzip"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
)

// gzipResponseMiddleware compresses textual responses (text/html and
// application/json) when the client accepts gzip. Binary and media
// endpoints are never touched: static assets are pre-compressed by the
// asset registry, and streams/transcodes carry Range support that must not
// be disturbed. The writer decision is deferred until the first body write
// so handlers keep full control of Content-Type and status codes.
var gzipWriterPool = sync.Pool{
	New: func() any {
		writer, _ := gzip.NewWriterLevel(nil, gzip.BestSpeed)
		return writer
	},
}

func (a *App) gzipResponse(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !gzipAllowed(r) {
			next.ServeHTTP(w, r)
			return
		}
		wrapped := &gzipResponseWriter{ResponseWriter: w}
		defer wrapped.finish()
		next.ServeHTTP(wrapped, r)
	})
}

func gzipAllowed(r *http.Request) bool {
	if !acceptsGzip(r.Header.Get("Accept-Encoding")) {
		return false
	}
	if r.Header.Get("Range") != "" {
		return false
	}
	path := r.URL.Path
	for _, marker := range []string{"/admin/assets/", "/stream", "/transcode.", "/artwork", "/image"} {
		if strings.Contains(path, marker) {
			return false
		}
	}
	return true
}

type gzipResponseWriter struct {
	http.ResponseWriter
	gzipWriter *gzip.Writer
	decided    bool
	compress   bool
	wroteHead  bool
	statusCode int
}

func (w *gzipResponseWriter) WriteHeader(status int) {
	// 1xx informational responses (e.g. 103 Early Hints) may be sent
	// multiple times before the final status; forward them without
	// committing the gzip decision.
	if status >= 100 && status < 200 {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if w.wroteHead {
		return
	}
	w.wroteHead = true
	w.statusCode = status
	// Bodyless statuses can be forwarded immediately; no gzip decision is
	// needed and Content-Encoding must not be attached.
	if status == http.StatusNoContent || status == http.StatusNotModified {
		w.decided = true
		w.ResponseWriter.WriteHeader(status)
	}
}

// decide inspects the headers accumulated so far, chooses between gzip and
// passthrough, and forwards the status line exactly once.
func (w *gzipResponseWriter) decide() {
	if w.decided {
		return
	}
	w.decided = true
	header := w.ResponseWriter.Header()
	contentType := header.Get("Content-Type")
	compressible := strings.HasPrefix(contentType, "text/html") || strings.HasPrefix(contentType, "application/json")
	if w.statusCode == http.StatusPartialContent || header.Get("Content-Encoding") != "" {
		compressible = false
	}
	if compressible {
		w.compress = true
		header.Del("Content-Length")
		header.Set("Content-Encoding", "gzip")
		header.Add("Vary", "Accept-Encoding")
		w.gzipWriter = gzipWriterPool.Get().(*gzip.Writer)
		w.gzipWriter.Reset(w.ResponseWriter)
	}
	w.ResponseWriter.WriteHeader(w.statusCode)
}

func (w *gzipResponseWriter) Write(body []byte) (int, error) {
	if !w.wroteHead {
		w.WriteHeader(http.StatusOK)
	}
	w.decide()
	if !w.compress {
		return w.ResponseWriter.Write(body)
	}
	return w.gzipWriter.Write(body)
}

func (w *gzipResponseWriter) finish() {
	if !w.wroteHead {
		return
	}
	// Handlers that only wrote headers still need the status line forwarded.
	w.decide()
	if w.compress && w.gzipWriter != nil {
		_ = w.gzipWriter.Close()
		// Reset to a discard sink before pooling so the writer never
		// retains a reference to the previous response's connection.
		w.gzipWriter.Reset(io.Discard)
		gzipWriterPool.Put(w.gzipWriter)
		w.gzipWriter = nil
	}
}

// Flush commits the pending decision, forwards headers and flushes the
// underlying connection so SSE-style or polling handlers keep working.
func (w *gzipResponseWriter) Flush() {
	if !w.wroteHead {
		w.WriteHeader(http.StatusOK)
	}
	w.decide()
	if w.compress && w.gzipWriter != nil {
		_ = w.gzipWriter.Flush()
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap exposes the original writer so http.ResponseController can reach
// optional interfaces (hijack, read timeouts) implemented by the server.
func (w *gzipResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *gzipResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hijacker.Hijack()
}
