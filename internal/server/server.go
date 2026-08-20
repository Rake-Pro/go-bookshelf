// Package server wires the HTTP routes and the middleware chain: panic
// recovery, request logging, security headers, gzip for JSON, authentication,
// and the single-page-application fallback that serves the embedded frontend.
package server

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/rake-pro/go-bookshelf/internal/api"
	"github.com/rake-pro/go-bookshelf/internal/auth"
	"github.com/rake-pro/go-bookshelf/internal/config"
)

// Middleware is a standard net/http decorator.
type Middleware func(http.Handler) http.Handler

// chain applies middleware so the first listed runs outermost.
func chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// New builds the fully wrapped HTTP handler.
func New(cfg config.Config, a *api.API, authMgr *auth.Manager, dist fs.FS) http.Handler {
	mux := http.NewServeMux()
	a.Register(mux)
	a.RegisterRoot(mux)
	registerStatic(mux, dist)

	return chain(mux,
		recoverPanic,
		requestLogger,
		securityHeaders,
		gzipJSON,
		authenticate(authMgr),
	)
}

// publicAPIPaths are the only /api/v1 routes reachable without a credential.
// Matching is exact (or an exact directory prefix for the auth endpoints) so a
// path like /api/v1/itemsX cannot masquerade as a public route.
var publicAPIPaths = map[string]bool{
	"/api/v1/healthz": true,
	"/api/v1/readyz":  true,
}

const authPrefix = "/api/v1/auth/"

func isPublicAPIPath(path string) bool {
	if publicAPIPaths[path] {
		return true
	}
	return strings.HasPrefix(path, authPrefix) && len(path) > len(authPrefix)
}

// authenticate resolves any credential on the request and attaches the
// identity to the context. Requests to a non-public /api/v1 route without a
// valid credential are refused here, before any handler runs.
func authenticate(mgr *auth.Manager) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := mgr.Authenticate(r.Context(), r)
			if err == nil && id != nil {
				r = r.WithContext(auth.WithIdentity(r.Context(), id))
			}
			if strings.HasPrefix(r.URL.Path, "/api/v1") && !isPublicAPIPath(r.URL.Path) {
				if id == nil {
					w.Header().Set("Content-Type", "application/json; charset=utf-8")
					w.Header().Set("X-Content-Type-Options", "nosniff")
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"authentication required"}}` + "\n"))
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
