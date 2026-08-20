package server

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
)

// startTime is the modification time reported for embedded assets; the binary
// is immutable, so one timestamp per process is correct and gives clients a
// stable validator.
var startTime = time.Now()

// registerStatic serves the embedded single-page application. Anything that is
// not an API route, a known file, or an asset request falls back to index.html
// so client-side routing works on a hard refresh.
func registerStatic(mux *http.ServeMux, dist fs.FS, version string) {
	sw := stampedServiceWorker(dist, version)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if sw != nil && path.Clean("/"+r.URL.Path) == "/sw.js" {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeContent(w, r, "sw.js", startTime, bytes.NewReader(sw))
			return
		}
		serveSPA(w, r, dist)
	})
}

var swVersionRe = regexp.MustCompile(`(?m)^const VERSION = '[^']*';`)

// stampedServiceWorker rewrites the service worker's cache VERSION to the build
// version, so every release invalidates the cached shell without anyone having
// to remember to bump a constant. A dev build keeps the file's own value. Nil
// when the worker is absent (tests with a stub frontend).
func stampedServiceWorker(dist fs.FS, version string) []byte {
	src, err := fs.ReadFile(dist, "sw.js")
	if err != nil {
		return nil
	}
	if version == "" || version == "dev" || strings.ContainsAny(version, "'\\\n") {
		return src
	}
	return swVersionRe.ReplaceAll(src, []byte("const VERSION = '"+version+"';"))
}

func serveSPA(w http.ResponseWriter, r *http.Request, dist fs.FS) {
	upath := path.Clean("/" + r.URL.Path)

	// An unmatched API path is a 404, never the application shell: returning
	// HTML to an API client hides real routing mistakes.
	if upath == "/api" || strings.HasPrefix(upath, "/api/") || strings.HasPrefix(upath, "/opds") {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"not found"}}` + "\n"))
		return
	}

	name := strings.TrimPrefix(upath, "/")
	if name == "" {
		name = "index.html"
	}

	if serveFile(w, r, dist, name) {
		return
	}
	// A missing file with an extension is a genuine 404; a missing route is
	// handled by the application shell.
	if path.Ext(name) != "" {
		http.NotFound(w, r)
		return
	}
	if !serveFile(w, r, dist, "index.html") {
		http.Error(w, "frontend assets are missing from this build", http.StatusInternalServerError)
	}
}

func serveFile(w http.ResponseWriter, r *http.Request, dist fs.FS, name string) bool {
	f, err := dist.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return false
	}
	seeker, ok := f.(interface {
		Read([]byte) (int, error)
		Seek(int64, int) (int64, error)
	})
	if !ok {
		return false
	}

	switch {
	case name == "index.html", name == "sw.js":
		// The shell and the service worker must never be served stale, or a
		// deployed update would not reach an installed client.
		w.Header().Set("Cache-Control", "no-cache")
	case strings.HasPrefix(name, "assets/"), strings.HasPrefix(name, "app/"), strings.HasPrefix(name, "vendor/"):
		w.Header().Set("Cache-Control", "public, max-age=3600")
	default:
		w.Header().Set("Cache-Control", "public, max-age=300")
	}
	http.ServeContent(w, r, path.Base(name), startTime, seeker)
	return true
}
