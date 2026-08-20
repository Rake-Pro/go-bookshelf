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
	"github.com/rake-pro/go-bookshelf/internal/settings"
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
func New(a *api.API, authMgr *auth.Manager, set *settings.Service, dist fs.FS) http.Handler {
	mux := http.NewServeMux()
	a.Register(mux)
	a.RegisterRoot(mux)
	registerStatic(mux, dist)

	return chain(mux,
		recoverPanic,
		requestLogger,
		securityHeaders,
		gzipJSON,
		authenticate(authMgr, set),
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

// setupPrefix is the wizard. Its first two steps run before any account
// exists, so they are public in the same way /auth/ is; the later steps do
// their own administrator check.
const setupPrefix = "/api/v1/setup/"

func isPublicAPIPath(path string) bool {
	if publicAPIPaths[path] {
		return true
	}
	if strings.HasPrefix(path, setupPrefix) && len(path) > len(setupPrefix) {
		return true
	}
	return strings.HasPrefix(path, authPrefix) && len(path) > len(authPrefix)
}

// setupAllowedPaths are the routes that still answer normally while first-run
// setup is unfinished. Everything else under /api/v1 is refused, so a
// half-configured server cannot be driven through its ordinary API.
func isSetupAllowedPath(path string) bool {
	if isPublicAPIPath(path) {
		return true
	}
	// The wizard's OIDC step reuses the admin page's test endpoint rather than
	// growing a second copy of it.
	return path == "/api/v1/admin/settings/oidc/test"
}

// authenticate resolves any credential on the request and attaches the
// identity to the context. Requests to a non-public /api/v1 route without a
// valid credential are refused here, before any handler runs, and so are
// requests to anything but the wizard while first-run setup is unfinished.
//
// The order matters: the 401 comes first, so an anonymous caller learns only
// that it needs to authenticate, never that this server has not been set up.
func authenticate(mgr *auth.Manager, set *settings.Service) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := mgr.Authenticate(r.Context(), r)
			if err == nil && id != nil {
				r = r.WithContext(auth.WithIdentity(r.Context(), id))
			}
			if strings.HasPrefix(r.URL.Path, "/api/v1") && !isPublicAPIPath(r.URL.Path) {
				if id == nil {
					writeRefusal(w, http.StatusUnauthorized, "unauthorized", "authentication required")
					return
				}
			}
			if strings.HasPrefix(r.URL.Path, "/api/v1") && !set.SetupComplete() && !isSetupAllowedPath(r.URL.Path) {
				writeRefusal(w, http.StatusForbidden, "setup_required",
					"this server has not finished first-run setup; open /setup")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// writeRefusal emits the standard error envelope without going through the API
// package, which is not reachable from a middleware.
func writeRefusal(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":{"code":"` + code + `","message":"` + message + `"}}` + "\n"))
}
