package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestServiceWorkerVersionStamp(t *testing.T) {
	dist := fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><title>x</title>")},
		"sw.js":      {Data: []byte("/* w */\nconst VERSION = 'v2';\nconst X = 1;\n")},
	}
	for _, tc := range []struct{ version, want string }{
		{"1.2.3", "const VERSION = '1.2.3';"},
		{"dev", "const VERSION = 'v2';"},
		{"", "const VERSION = 'v2';"},
	} {
		mux := http.NewServeMux()
		registerStatic(mux, dist, tc.version)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sw.js", nil))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("version %q: code %d body %q, want %q", tc.version, rec.Code, rec.Body.String(), tc.want)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Fatalf("sw.js Cache-Control = %q", cc)
		}
	}
}
