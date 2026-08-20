// Package api implements the JSON API under /api/v1 and the OPDS catalog.
//
// Handlers assume the router has already authenticated the request: every
// route registered by Register except the /auth/ endpoints runs behind the
// server's auth middleware. Authorization - admin-only writes, per-library
// visibility, token scopes - is enforced here, per handler.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rake-pro/go-bookshelf/internal/auth"
	"github.com/rake-pro/go-bookshelf/internal/config"
	"github.com/rake-pro/go-bookshelf/internal/images"
	"github.com/rake-pro/go-bookshelf/internal/importer"
	"github.com/rake-pro/go-bookshelf/internal/library"
	"github.com/rake-pro/go-bookshelf/internal/settings"
	"github.com/rake-pro/go-bookshelf/internal/store"
	"github.com/rake-pro/go-bookshelf/internal/upload"
	"github.com/rs/zerolog/log"
)

// API holds everything the handlers need.
type API struct {
	cfg      config.Config
	settings *settings.Service
	db       *store.DB
	cat      *library.Catalog
	auth     *auth.Manager
	scanner  *library.Scanner
	covers   *images.Store
	version  string

	// Adding books. The upload service is the only thing in the server that
	// writes into a library; the job queue and its worker are how a URL import
	// outlives the request that asked for it.
	uploads       *upload.Service
	jobs          *importer.Jobs
	importWorker  *importer.Worker
	uploadLimiter *auth.Limiter
	uploading     *inflight
}

// New builds the API handler set.
func New(cfg config.Config, set *settings.Service, db *store.DB, cat *library.Catalog,
	authMgr *auth.Manager, scanner *library.Scanner, covers *images.Store, version string,
	uploads *upload.Service, jobs *importer.Jobs, worker *importer.Worker) *API {
	return &API{
		cfg: cfg, settings: set, db: db, cat: cat, auth: authMgr,
		scanner: scanner, covers: covers, version: version,
		uploads: uploads, jobs: jobs, importWorker: worker,
		// Thirty uploads of burst, then one more every two seconds: enough to
		// drop a folder of chapters in at once, not enough to be a way of
		// filling a disk.
		uploadLimiter: auth.NewLimiter(30, 2*time.Second),
		uploading:     newInflight(),
	}
}

// baseURL is the external URL of this deployment, as configured in the app.
func (a *API) baseURL() string { return a.settings.Get().General.BaseURL }

// maxJSONBody bounds a request body the API will parse.
const maxJSONBody = 1 << 20

// errorBody is the documented error envelope.
type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// listBody is the documented list envelope.
type listBody[T any] struct {
	Items []T `json:"items"`
	Total int `json:"total"`
}

// Error codes used across the API.
const (
	codeBadRequest   = "bad_request"
	codeUnauthorized = "unauthorized"
	codeForbidden    = "forbidden"
	codeNotFound     = "not_found"
	codeConflict     = "conflict"
	codeRateLimited  = "rate_limited"
	codeInternal     = "internal"
	codeSetupPending = "setup_required"
)

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Debug().Err(err).Msg("writing JSON response failed")
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	var body errorBody
	body.Error.Code = code
	body.Error.Message = message
	writeJSON(w, status, body)
}

// fail maps an internal error onto a response, without leaking its text.
func fail(w http.ResponseWriter, err error, what string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
	default:
		log.Error().Err(err).Str("op", what).Msg("request failed")
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxJSONBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "request body is not valid JSON for this endpoint")
		return false
	}
	return true
}

// identity returns the authenticated principal, or nil.
func identity(r *http.Request) *auth.Identity { return auth.FromContext(r.Context()) }

// requireUser returns the identity, writing 401 when there is none.
func requireUser(w http.ResponseWriter, r *http.Request) *auth.Identity {
	id := identity(r)
	if id == nil || id.User == nil {
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "authentication required")
		return nil
	}
	return id
}

// requireWrite returns the identity for a mutating request, enforcing the
// write scope on API tokens.
func requireWrite(w http.ResponseWriter, r *http.Request) *auth.Identity {
	id := requireUser(w, r)
	if id == nil {
		return nil
	}
	if !id.HasScope(auth.ScopeWrite) {
		writeError(w, http.StatusForbidden, codeForbidden, "this token does not carry the write scope")
		return nil
	}
	return id
}

// requireAdmin returns the identity for an admin-only request.
func requireAdmin(w http.ResponseWriter, r *http.Request) *auth.Identity {
	id := requireWrite(w, r)
	if id == nil {
		return nil
	}
	if !id.User.IsAdmin() {
		writeError(w, http.StatusForbidden, codeForbidden, "administrator role required")
		return nil
	}
	return id
}

// allowedLibraries returns the library ids the identity may read, or nil for
// an administrator (meaning "no restriction").
func (a *API) allowedLibraries(r *http.Request, id *auth.Identity) ([]int64, error) {
	if id.User.IsAdmin() {
		return nil, nil
	}
	return a.auth.LibraryIDs(r.Context(), id.User)
}

// itemVisible reports whether the identity may see an item. A user without
// access to the item's library is told the item does not exist, so the API
// never confirms the existence of items outside a user's libraries.
func (a *API) itemVisible(r *http.Request, id *auth.Identity, itemID int64) (bool, error) {
	libID, err := a.cat.ItemLibrary(r.Context(), itemID)
	if err != nil {
		return false, err
	}
	return a.auth.CanAccessLibrary(r.Context(), id.User, libID)
}

func pathID(r *http.Request, name string) (int64, bool) {
	v := r.PathValue(name)
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

func queryInt(r *http.Request, name string, fallback int) int {
	v := strings.TrimSpace(r.URL.Query().Get(name))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func queryID(r *http.Request, name string) int64 {
	v := strings.TrimSpace(r.URL.Query().Get(name))
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// Register attaches every /api/v1 route to mux. Routes are registered with
// explicit method+path patterns so no path prefix accidentally matches.
func (a *API) Register(mux *http.ServeMux) {
	const p = "/api/v1"

	// Public: these are the only /api/v1 routes the auth middleware exempts.
	mux.HandleFunc("GET "+p+"/auth/status", a.authStatus)
	mux.HandleFunc("POST "+p+"/auth/login", a.authLogin)
	mux.HandleFunc("POST "+p+"/auth/logout", a.authLogout)
	mux.HandleFunc("GET "+p+"/auth/oidc/start", a.oidcStart)
	mux.HandleFunc("GET "+p+"/auth/oidc/callback", a.oidcCallback)
	mux.HandleFunc("GET "+p+"/auth/me", a.authMe)
	mux.HandleFunc("GET "+p+"/healthz", a.healthz)
	mux.HandleFunc("GET "+p+"/readyz", a.readyz)

	// Libraries.
	mux.HandleFunc("GET "+p+"/libraries", a.listLibraries)
	mux.HandleFunc("POST "+p+"/libraries", a.createLibrary)
	mux.HandleFunc("PATCH "+p+"/libraries/{id}", a.patchLibrary)
	mux.HandleFunc("DELETE "+p+"/libraries/{id}", a.deleteLibrary)
	mux.HandleFunc("POST "+p+"/libraries/{id}/scan", a.scanLibrary)
	// Adding books. Both routes need the upload permission rather than the
	// admin role, and both are checked against the caller's library access.
	mux.HandleFunc("POST "+p+"/libraries/{id}/upload", a.uploadFiles)
	mux.HandleFunc("POST "+p+"/libraries/{id}/import", a.createImport)
	mux.HandleFunc("GET "+p+"/imports/{id}", a.getImport)
	mux.HandleFunc("DELETE "+p+"/imports/{id}", a.deleteImport)
	mux.HandleFunc("GET "+p+"/libraries/{id}/scans", a.listScans)

	// Items.
	mux.HandleFunc("GET "+p+"/items", a.listItems)
	mux.HandleFunc("GET "+p+"/items/{id}", a.getItem)
	mux.HandleFunc("PATCH "+p+"/items/{id}", a.patchItem)
	mux.HandleFunc("GET "+p+"/items/{id}/cover", a.itemCover)
	mux.HandleFunc("GET "+p+"/items/{id}/epub", a.itemEPUBManifest)
	mux.HandleFunc("GET "+p+"/items/{id}/epub/{path...}", a.itemEPUBResource)
	mux.HandleFunc("GET "+p+"/items/{id}/files/{file_id}/stream", a.itemStream)
	mux.HandleFunc("GET "+p+"/items/{id}/download", a.itemDownload)

	// Discovery.
	mux.HandleFunc("GET "+p+"/home", a.home)
	mux.HandleFunc("GET "+p+"/authors", a.listAuthors)
	mux.HandleFunc("GET "+p+"/authors/{id}", a.getAuthor)
	mux.HandleFunc("GET "+p+"/series", a.listSeries)
	mux.HandleFunc("GET "+p+"/series/{id}", a.getSeries)
	mux.HandleFunc("GET "+p+"/tags", a.listTags)
	mux.HandleFunc("GET "+p+"/search", a.search)

	// User state.
	mux.HandleFunc("GET "+p+"/me/settings", a.getSettings)
	mux.HandleFunc("PUT "+p+"/me/settings", a.putSettings)
	mux.HandleFunc("GET "+p+"/me/progress", a.getProgress)
	mux.HandleFunc("PUT "+p+"/me/progress/{item_id}", a.putProgress)
	mux.HandleFunc("GET "+p+"/me/bookmarks", a.listBookmarks)
	mux.HandleFunc("POST "+p+"/me/bookmarks", a.createBookmark)
	mux.HandleFunc("DELETE "+p+"/me/bookmarks/{id}", a.deleteBookmark)
	mux.HandleFunc("GET "+p+"/me/imports", a.listImports)
	mux.HandleFunc("GET "+p+"/me/tokens", a.listTokens)
	mux.HandleFunc("POST "+p+"/me/tokens", a.createToken)
	mux.HandleFunc("DELETE "+p+"/me/tokens/{id}", a.deleteToken)
	mux.HandleFunc("GET "+p+"/me/collections", a.listCollections)
	mux.HandleFunc("GET "+p+"/collections", a.listCollections)
	mux.HandleFunc("POST "+p+"/collections", a.createCollection)

	// First-run wizard. Every step is its own route so the client can resume
	// at the step it got to; see docs/DESIGN.md.
	mux.HandleFunc("POST "+p+"/setup/{step}", a.setupStep)

	// Admin.
	mux.HandleFunc("GET "+p+"/admin/settings", a.getAdminSettings)
	mux.HandleFunc("PUT "+p+"/admin/settings", a.putAdminSettings)
	mux.HandleFunc("POST "+p+"/admin/settings/oidc/test", a.testAdminOIDC)
	mux.HandleFunc("GET "+p+"/users", a.listUsers)
	mux.HandleFunc("POST "+p+"/users", a.createUser)
	mux.HandleFunc("PATCH "+p+"/users/{id}", a.patchUser)
	mux.HandleFunc("DELETE "+p+"/users/{id}", a.deleteUser)
	mux.HandleFunc("PUT "+p+"/users/{id}/libraries", a.putUserLibraries)
	mux.HandleFunc("GET "+p+"/system/status", a.systemStatus)
}

// RegisterRoot attaches the routes that live outside /api/v1.
func (a *API) RegisterRoot(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", a.healthz)
	mux.HandleFunc("GET /readyz", a.readyz)
	mux.HandleFunc("GET /metrics", a.metrics)
	mux.HandleFunc("GET /opds", a.opdsRoot)
	mux.HandleFunc("GET /opds/search", a.opdsSearch)
	mux.HandleFunc("GET /opds/{library}", a.opdsLibrary)
}

// configIPAllowed reports whether an address may read /metrics.
func configIPAllowed(a *API, ip net.IP) bool {
	return settings.IPInNets(ip, a.settings.MetricsAllowNets())
}

// Version is the build version stamped into the binary.
func (a *API) Version() string { return a.version }
