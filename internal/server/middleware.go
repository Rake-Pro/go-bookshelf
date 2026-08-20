package server

import (
	"compress/gzip"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rake-pro/go-bookshelf/internal/auth"
	"github.com/rs/zerolog/log"
)

// appCSP is the policy for the application shell. Book content is served from
// /api/v1/items/{id}/epub/ with its own, far stricter policy.
const appCSP = "default-src 'self'; " +
	"script-src 'self'; " +
	// blob: on styles/fonts/frames - the reader serves chapter documents and
	// their stylesheets and fonts from in-memory blobs (never remote).
	"style-src 'self' 'unsafe-inline' blob:; " +
	"img-src 'self' data: blob:; " +
	"media-src 'self' blob:; " +
	"font-src 'self' data: blob:; " +
	"connect-src 'self'; " +
	"worker-src 'self'; " +
	"manifest-src 'self'; " +
	"object-src 'none'; " +
	"base-uri 'none'; " +
	"form-action 'self'; " +
	// blob: - the reader renders each chapter from an in-memory document in a
	// sandboxed iframe (no scripts), never from a remote frame.
	"frame-src 'self' blob:; " +
	"frame-ancestors 'self'"

// securityHeaders sets defence-in-depth headers on every response. Handlers
// that need a different policy (the EPUB resource endpoint) overwrite the CSP
// before writing their response.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", appCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		// SAMEORIGIN rather than DENY: the reader renders book content in a
		// same-origin sandboxed iframe.
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), interest-cohort=()")
		next.ServeHTTP(w, r)
	})
}

// statusWriter records the status code and byte count for the access log.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer, which
// ServeContent relies on for flushing large media responses.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		event := log.Info()
		if sw.status >= 500 {
			event = log.Error()
		} else if sw.status >= 400 {
			event = log.Warn()
		}
		// The path is logged without its query string: search terms and
		// tokens have no business in the log.
		event.Str("method", r.Method).
			Str("path", r.URL.Path).
			Int("status", sw.status).
			Int("bytes", sw.bytes).
			Dur("duration", time.Since(start)).
			Str("ip", auth.ClientIP(r)).
			Msg("request")
	})
}

func recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Error().Interface("panic", rec).Str("path", r.URL.Path).Msg("recovered from panic")
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"code":"internal","message":"internal error"}}` + "\n"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

var gzipPool = sync.Pool{
	New: func() any { return gzip.NewWriter(nil) },
}

// gzipWriter compresses only textual responses, and only for a full 200. Media
// streaming answers 206 with byte ranges, which must not be re-encoded.
type gzipWriter struct {
	http.ResponseWriter
	gz       *gzip.Writer
	decided  bool
	compress bool
}

func (w *gzipWriter) WriteHeader(status int) {
	if !w.decided {
		w.decided = true
		ct := w.Header().Get("Content-Type")
		if status == http.StatusOK && isCompressible(ct) && w.Header().Get("Content-Range") == "" {
			w.compress = true
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Add("Vary", "Accept-Encoding")
			w.Header().Del("Content-Length")
			w.gz = gzipPool.Get().(*gzip.Writer)
			w.gz.Reset(w.ResponseWriter)
		}
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *gzipWriter) Write(b []byte) (int, error) {
	if !w.decided {
		w.WriteHeader(http.StatusOK)
	}
	if w.compress {
		return w.gz.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

func (w *gzipWriter) Close() {
	if w.compress && w.gz != nil {
		_ = w.gz.Close()
		gzipPool.Put(w.gz)
		w.gz = nil
	}
}

// Unwrap keeps http.ResponseController working through the wrapper.
func (w *gzipWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func isCompressible(contentType string) bool {
	ct := strings.ToLower(contentType)
	switch {
	case strings.HasPrefix(ct, "application/json"),
		strings.HasPrefix(ct, "application/atom+xml"),
		strings.HasPrefix(ct, "application/xhtml+xml"),
		strings.HasPrefix(ct, "application/manifest+json"),
		strings.HasPrefix(ct, "application/javascript"),
		strings.HasPrefix(ct, "text/"):
		return true
	}
	return false
}

func gzipJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// HEAD carries no body to compress, and compressing it would drop the
		// Content-Length the client asked the question for: the reader sizes
		// spine documents with HEAD requests.
		if r.Method == http.MethodHead || !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipWriter{ResponseWriter: w}
		defer gw.Close()
		next.ServeHTTP(gw, r)
	})
}
