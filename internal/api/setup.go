package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/rake-pro/go-bookshelf/internal/auth"
	"github.com/rake-pro/go-bookshelf/internal/library"
	"github.com/rake-pro/go-bookshelf/internal/settings"
	"github.com/rs/zerolog/log"
)

// The wizard's steps, as the last path segment of POST /api/v1/setup/{step}.
//
// Each step is a separate route rather than one endpoint with a step field so
// that a half-finished wizard is resumable: the client knows which calls have
// succeeded from their own status codes, and a reload picks up from there
// instead of replaying the whole document.
const (
	stepToken    = "token"    // check the one-time token, before anything is created
	stepAdmin    = "admin"    // spend the token, create the first administrator
	stepBaseURL  = "base-url" // store the external URL
	stepOIDC     = "oidc"     // store (or skip) the identity provider
	stepLibrary  = "library"  // create (or skip) the first library
	stepComplete = "complete" // close the wizard
)

// setupStep dispatches POST /api/v1/setup/{step}.
//
// The token and admin steps are public - there is nobody to authenticate as
// yet, and the one-time token is the gate. Every later step requires the
// administrator the admin step just signed in, so the rest of the wizard is
// exactly as protected as the admin surface it is configuring.
func (a *API) setupStep(w http.ResponseWriter, r *http.Request) {
	if a.settings.SetupComplete() {
		writeError(w, http.StatusConflict, codeConflict, "setup has already been completed")
		return
	}
	switch r.PathValue("step") {
	case stepToken:
		a.setupCheckToken(w, r)
	case stepAdmin:
		a.setupCreateAdmin(w, r)
	case stepBaseURL:
		a.setupBaseURL(w, r)
	case stepOIDC:
		a.setupOIDC(w, r)
	case stepLibrary:
		a.setupLibrary(w, r)
	case stepComplete:
		a.setupComplete(w, r)
	default:
		writeError(w, http.StatusNotFound, codeNotFound, "unknown setup step")
	}
}

// requireSetupAdmin gates the steps that run after the account exists.
func (a *API) requireSetupAdmin(w http.ResponseWriter, r *http.Request) *auth.Identity {
	id := identity(r)
	if id == nil || id.User == nil {
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "sign in as the administrator you just created")
		return nil
	}
	if !id.User.IsAdmin() {
		writeError(w, http.StatusForbidden, codeForbidden, "administrator role required")
		return nil
	}
	return id
}

// setupCheckToken validates the one-time token without spending it, so the
// wizard can tell the operator they pasted the wrong thing on step one rather
// than after they have typed a password. It also answers with the base URL the
// request itself suggests, which is what step three prefills.
func (a *API) setupCheckToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	err := a.auth.CheckSetupToken(r.Context(), body.Token, auth.ClientIP(r))
	switch {
	case errors.Is(err, auth.ErrRateLimited):
		writeError(w, http.StatusTooManyRequests, codeRateLimited, "too many setup attempts; try again later")
		return
	case errors.Is(err, auth.ErrSetupDone):
		writeError(w, http.StatusConflict, codeConflict, "setup has already been completed")
		return
	case errors.Is(err, auth.ErrSetupToken):
		writeError(w, http.StatusForbidden, codeForbidden, "invalid setup token")
		return
	case err != nil:
		fail(w, err, "setup token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                 true,
		"suggested_base_url": suggestedBaseURL(r),
	})
}

// setupCreateAdmin spends the token and creates the first administrator, then
// signs them in so the remaining steps run as that account.
func (a *API) setupCreateAdmin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token       string `json:"token"`
		Username    string `json:"username"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	user, err := a.auth.Setup(r.Context(), body.Token, body.Username, body.Password, body.DisplayName, auth.ClientIP(r))
	switch {
	case errors.Is(err, auth.ErrRateLimited):
		writeError(w, http.StatusTooManyRequests, codeRateLimited, "too many setup attempts; try again later")
		return
	case errors.Is(err, auth.ErrSetupDone):
		writeError(w, http.StatusConflict, codeConflict, "setup has already been completed")
		return
	case errors.Is(err, auth.ErrSetupToken):
		writeError(w, http.StatusForbidden, codeForbidden, "invalid setup token")
		return
	case errors.Is(err, auth.ErrWeakPassword):
		writeError(w, http.StatusBadRequest, codeBadRequest, auth.ErrWeakPassword.Error())
		return
	case err != nil:
		fail(w, err, "setup")
		return
	}

	sid, err := a.auth.CreateSession(r.Context(), user.ID, r.UserAgent(), auth.ClientIP(r))
	if err != nil {
		fail(w, err, "setup session")
		return
	}
	http.SetCookie(w, a.auth.SessionCookieFor(sid))
	log.Info().Str("username", user.Username).Msg("first-run setup: administrator created")
	a.writeMe(w, r, &auth.Identity{User: user, Method: "session"})
}

// setupBaseURL stores the external URL. It is its own step because everything
// after it - the OIDC redirect URI the operator has to register, the cookie
// Secure flag - is derived from it.
func (a *API) setupBaseURL(w http.ResponseWriter, r *http.Request) {
	if a.requireSetupAdmin(w, r) == nil {
		return
	}
	var body struct {
		BaseURL string `json:"base_url"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	next := a.settings.Get()
	next.General.BaseURL = body.BaseURL
	if err := a.saveSettings(w, r, next); err != nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"base_url":     a.settings.Get().General.BaseURL,
		"redirect_url": a.settings.Get().OIDCRedirectURL(),
	})
}

// setupOIDC stores the identity provider, or records that it was skipped.
func (a *API) setupOIDC(w http.ResponseWriter, r *http.Request) {
	if a.requireSetupAdmin(w, r) == nil {
		return
	}
	var body struct {
		Skip bool `json:"skip"`
		oidcRequest
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	next := a.settings.Get()
	if body.Skip {
		next.OIDC.Enabled = false
	} else {
		body.oidcRequest.applyTo(&next)
	}
	if err := a.saveSettings(w, r, next); err != nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"oidc_enabled": a.auth.OIDCEnabled()})
}

// setupLibrary creates the first library, or records that it was skipped. The
// path is checked here rather than at the first scan so the operator finds out
// about a typo or a missing mount while they are still looking at the field.
func (a *API) setupLibrary(w http.ResponseWriter, r *http.Request) {
	if a.requireSetupAdmin(w, r) == nil {
		return
	}
	var body struct {
		Skip          bool   `json:"skip"`
		Name          string `json:"name"`
		Kind          string `json:"kind"`
		Path          string `json:"path"`
		CreateMissing bool   `json:"create_missing"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Skip {
		writeJSON(w, http.StatusOK, map[string]any{"skipped": true})
		return
	}
	name := strings.TrimSpace(body.Name)
	path := strings.TrimSpace(body.Path)
	if name == "" || path == "" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "a name and a path are required")
		return
	}
	switch body.Kind {
	case library.KindEbook, library.KindAudiobook, library.KindMixed:
	case "":
		body.Kind = library.KindMixed
	default:
		writeError(w, http.StatusBadRequest, codeBadRequest, "kind must be ebook, audiobook or mixed")
		return
	}
	if body.CreateMissing {
		if err := ensureLibraryDir(path); err != nil {
			writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
			return
		}
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		writeError(w, http.StatusBadRequest, codeBadRequest,
			"the server cannot read a directory at "+path)
		return
	}

	lib, err := a.cat.CreateLibrary(r.Context(), name, body.Kind, []string{path})
	if err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "could not create the library")
		return
	}
	log.Info().Str("library", lib.Name).Msg("first-run setup: library created")
	writeJSON(w, http.StatusCreated, lib)
}

// setupComplete closes the wizard. Only after this does the rest of the API
// answer normally.
func (a *API) setupComplete(w http.ResponseWriter, r *http.Request) {
	if a.requireSetupAdmin(w, r) == nil {
		return
	}
	if err := a.settings.MarkSetupComplete(r.Context()); err != nil {
		fail(w, err, "complete setup")
		return
	}
	log.Info().Msg("first-run setup completed")
	writeJSON(w, http.StatusOK, map[string]any{"setup_complete": true})
}

// saveSettings validates and applies a settings document, mapping a rejection
// onto 400. It is shared by the wizard and the admin settings page so both
// enforce exactly the same rules.
func (a *API) saveSettings(w http.ResponseWriter, r *http.Request, next settings.Settings) error {
	if err := a.settings.Save(r.Context(), next); err != nil {
		if errors.Is(err, settings.ErrInvalid) {
			writeError(w, http.StatusBadRequest, codeBadRequest,
				strings.TrimPrefix(err.Error(), "settings: "))
			return err
		}
		// A preparation failure is nearly always the identity provider: an
		// issuer that does not answer discovery. Say so instead of logging a
		// 500, because the operator is the only one who can fix it.
		log.Warn().Err(err).Msg("settings save rejected")
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return err
	}
	return nil
}

// suggestedBaseURL reconstructs the URL the browser used, preferring what a
// reverse proxy says it terminated. It is only a prefill: the operator sees it
// in a field and can correct it, so trusting the forwarded headers here costs
// nothing that the field itself does not already allow.
func suggestedBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if v := firstForwarded(r.Header.Get("X-Forwarded-Proto")); v == "http" || v == "https" {
		scheme = v
	}
	host := r.Host
	if v := firstForwarded(r.Header.Get("X-Forwarded-Host")); v != "" {
		host = v
	}
	if host == "" {
		return ""
	}
	u := url.URL{Scheme: scheme, Host: host}
	return u.String()
}

// firstForwarded takes the leftmost value of a comma-separated forwarding
// header; anything after the first hop is another proxy's opinion.
func firstForwarded(v string) string {
	first, _, _ := strings.Cut(v, ",")
	return strings.ToLower(strings.TrimSpace(first))
}

// ensureLibraryDir creates a library directory on request (an empty media
// share has none yet). The path must be absolute; creation is admin-only.
func ensureLibraryDir(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("library path must be absolute")
	}
	if err := os.MkdirAll(path, 0o775); err != nil {
		return fmt.Errorf("could not create %s: %v", path, err)
	}
	return nil
}
