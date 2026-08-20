package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/rake-pro/go-bookshelf/internal/auth"
	"github.com/rs/zerolog/log"
)

// meResponse is the shape of GET /auth/me.
type meResponse struct {
	ID          int64   `json:"id"`
	Username    string  `json:"username"`
	DisplayName string  `json:"display_name"`
	Role        string  `json:"role"`
	Libraries   []int64 `json:"libraries"`
	AuthMethod  string  `json:"auth_method"`
	// CanUpload is the answer to "may this account add books", with the role
	// already folded in, so the frontend never has to reimplement the rule.
	CanUpload bool `json:"can_upload"`
}

// authStatus tells the login page and the wizard what they may offer. It is
// deliberately public and reveals nothing beyond which sign-in methods exist
// and whether the server has been set up.
func (a *API) authStatus(w http.ResponseWriter, r *http.Request) {
	complete := a.settings.SetupComplete()
	writeJSON(w, http.StatusOK, map[string]any{
		// setup_required stays the flag the client redirects on; setup_complete
		// is its positive form, which is what the wizard's own steps read.
		"setup_required": !complete,
		"setup_complete": complete,
		"oidc_enabled":   a.auth.OIDCEnabled(),
		"oidc_start_url": "/api/v1/auth/oidc/start",
		"local_login":    a.auth.LocalLoginEnabled(),
		"version":        a.version,
	})
}

func (a *API) authLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	user, sid, err := a.auth.Login(r.Context(), body.Username, body.Password, r.UserAgent(), auth.ClientIP(r))
	switch {
	case errors.Is(err, auth.ErrLocalLoginOff):
		writeError(w, http.StatusForbidden, codeForbidden,
			"password sign-in is turned off on this server; use the single sign-on button")
		return
	case errors.Is(err, auth.ErrRateLimited):
		writeError(w, http.StatusTooManyRequests, codeRateLimited, "too many login attempts; try again later")
		return
	case errors.Is(err, auth.ErrDisabled):
		writeError(w, http.StatusForbidden, codeForbidden, "this account is disabled")
		return
	case errors.Is(err, auth.ErrBadCredentials):
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "invalid username or password")
		return
	case err != nil:
		fail(w, err, "login")
		return
	}
	http.SetCookie(w, a.auth.SessionCookieFor(sid))
	a.writeMe(w, r, &auth.Identity{User: user, Method: "session"})
}

func (a *API) authLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.SessionCookie); err == nil && c.Value != "" {
		if err := a.auth.DeleteSession(r.Context(), c.Value); err != nil {
			fail(w, err, "logout")
			return
		}
	}
	http.SetCookie(w, a.auth.ClearSessionCookie())
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
}

func (a *API) authMe(w http.ResponseWriter, r *http.Request) {
	// /auth/* is exempt from the router's auth middleware, so this endpoint
	// resolves the credential itself and answers 401 when there is none.
	id, err := a.auth.Authenticate(r.Context(), r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "authentication required")
		return
	}
	a.writeMe(w, r, id)
}

func (a *API) writeMe(w http.ResponseWriter, r *http.Request, id *auth.Identity) {
	libs, err := a.auth.LibraryIDs(r.Context(), id.User)
	if err != nil {
		fail(w, err, "library access")
		return
	}
	if libs == nil {
		libs = []int64{}
	}
	writeJSON(w, http.StatusOK, meResponse{
		ID:          id.User.ID,
		Username:    id.User.Username,
		DisplayName: id.User.DisplayName,
		Role:        id.User.Role,
		Libraries:   libs,
		AuthMethod:  id.Method,
		CanUpload:   id.User.MayUpload(),
	})
}

func (a *API) oidcStart(w http.ResponseWriter, r *http.Request) {
	url, err := a.auth.StartOIDC(w)
	if errors.Is(err, auth.ErrOIDCDisabled) {
		writeError(w, http.StatusNotFound, codeNotFound, "OIDC login is not configured")
		return
	}
	if err != nil {
		fail(w, err, "oidc start")
		return
	}
	http.Redirect(w, r, url, http.StatusFound)
}

func (a *API) oidcCallback(w http.ResponseWriter, r *http.Request) {
	user, sid, err := a.auth.CompleteOIDC(r.Context(), w, r)
	switch {
	case errors.Is(err, auth.ErrOIDCDisabled):
		writeError(w, http.StatusNotFound, codeNotFound, "OIDC login is not configured")
		return
	case errors.Is(err, auth.ErrOIDCState):
		writeError(w, http.StatusBadRequest, codeBadRequest, "the login attempt expired; start again")
		return
	case errors.Is(err, auth.ErrDisabled):
		writeError(w, http.StatusForbidden, codeForbidden, "this account is disabled")
		return
	case errors.Is(err, auth.ErrNotAuthorized):
		writeError(w, http.StatusForbidden, codeForbidden,
			"your account is not authorized for this application")
		return
	case errors.Is(err, auth.ErrNoAccount):
		writeError(w, http.StatusForbidden, codeForbidden,
			"this server does not create accounts automatically; ask an administrator for one")
		return
	case err != nil:
		log.Warn().Err(err).Msg("OIDC callback failed")
		writeError(w, http.StatusBadRequest, codeBadRequest, "the login attempt could not be completed")
		return
	}
	http.SetCookie(w, a.auth.SessionCookieFor(sid))
	log.Info().Str("username", user.Username).Msg("OIDC login")
	http.Redirect(w, r, "/", http.StatusFound)
}

func (a *API) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": a.version})
}

func (a *API) readyz(w http.ResponseWriter, r *http.Request) {
	if err := a.db.PingContext(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// metrics serves a small Prometheus exposition. It is limited to the
// configured CIDRs because it exposes catalog and user counts.
func (a *API) metrics(w http.ResponseWriter, r *http.Request) {
	peer := auth.PeerIP(r)
	if !configIPAllowed(a, peer) {
		writeError(w, http.StatusForbidden, codeForbidden, "metrics are not exposed to this address")
		return
	}
	counts, err := a.cat.ItemCounts(r.Context())
	if err != nil {
		fail(w, err, "metrics")
		return
	}
	users, err := a.auth.UserCount(r.Context())
	if err != nil {
		fail(w, err, "metrics")
		return
	}
	var libs int
	if err := a.db.QueryRowContext(r.Context(), `SELECT count(*) FROM libraries`).Scan(&libs); err != nil {
		fail(w, err, "metrics")
		return
	}

	var b strings.Builder
	b.WriteString("# HELP gobookshelf_build_info Build information.\n")
	b.WriteString("# TYPE gobookshelf_build_info gauge\n")
	fmt.Fprintf(&b, "gobookshelf_build_info{version=%q} 1\n", a.version)
	b.WriteString("# HELP gobookshelf_items Number of catalog items by kind.\n")
	b.WriteString("# TYPE gobookshelf_items gauge\n")
	for _, kind := range []string{"ebook", "audiobook"} {
		fmt.Fprintf(&b, "gobookshelf_items{kind=%q} %d\n", kind, counts[kind])
	}
	b.WriteString("# HELP gobookshelf_libraries Number of configured libraries.\n")
	b.WriteString("# TYPE gobookshelf_libraries gauge\n")
	fmt.Fprintf(&b, "gobookshelf_libraries %d\n", libs)
	b.WriteString("# HELP gobookshelf_users Number of accounts.\n")
	b.WriteString("# TYPE gobookshelf_users gauge\n")
	fmt.Fprintf(&b, "gobookshelf_users %d\n", users)

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(b.String()))
}
