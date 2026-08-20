package auth

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/rake-pro/go-bookshelf/internal/config"
	"github.com/rake-pro/go-bookshelf/internal/store"
	"golang.org/x/oauth2"
)

// oidcStateCookie carries the CSRF state and nonce between the authorization
// request and the callback.
const oidcStateCookie = "gbs_oidc"

// ErrOIDCDisabled is returned when an OIDC endpoint is called but no provider
// is configured or discovery failed.
var ErrOIDCDisabled = errors.New("auth: OIDC login is not enabled")

// ErrOIDCState is returned when the callback's state does not match the one
// issued at the start of the flow.
var ErrOIDCState = errors.New("auth: OIDC state mismatch")

type oidcClient struct {
	verifier    *oidc.IDTokenVerifier
	oauth       oauth2.Config
	groupsClaim string
	adminGroup  string
}

func newOIDCClient(ctx context.Context, cfg config.Config) (*oidcClient, error) {
	provider, err := oidc.NewProvider(ctx, cfg.OIDC.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	scopes := []string{oidc.ScopeOpenID, "profile", "email"}
	if cfg.OIDC.Scopes != "" {
		scopes = strings.FieldsFunc(cfg.OIDC.Scopes, func(r rune) bool { return r == ',' || r == ' ' })
	}
	if cfg.OIDC.AdminGroup != "" {
		scopes = appendUnique(scopes, "groups")
	}
	return &oidcClient{
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.OIDC.ClientID}),
		oauth: oauth2.Config{
			ClientID:     cfg.OIDC.ClientID,
			ClientSecret: cfg.OIDC.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  cfg.OIDCRedirectURL(),
			Scopes:       scopes,
		},
		groupsClaim: cfg.GroupsClaim(),
		adminGroup:  cfg.OIDC.AdminGroup,
	}, nil
}

// StartOIDC sets the state cookie and returns the provider URL to redirect to.
func (m *Manager) StartOIDC(w http.ResponseWriter) (string, error) {
	if m.oidc == nil {
		return "", ErrOIDCDisabled
	}
	state, err := randomHex(16)
	if err != nil {
		return "", err
	}
	nonce, err := randomHex(16)
	if err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     oidcStateCookie,
		Value:    state + ":" + nonce,
		Path:     "/",
		HttpOnly: true,
		Secure:   m.cfg.SecureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600,
	})
	return m.oidc.oauth.AuthCodeURL(state, oidc.Nonce(nonce)), nil
}

// CompleteOIDC validates the callback, maps the claims onto a local account,
// and returns the user with a fresh session id.
func (m *Manager) CompleteOIDC(ctx context.Context, w http.ResponseWriter, r *http.Request) (*User, string, error) {
	if m.oidc == nil {
		return nil, "", ErrOIDCDisabled
	}
	cookie, err := r.Cookie(oidcStateCookie)
	if err != nil || cookie.Value == "" {
		return nil, "", ErrOIDCState
	}
	// Consume the cookie whatever the outcome.
	http.SetCookie(w, &http.Cookie{Name: oidcStateCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})

	wantState, wantNonce, ok := strings.Cut(cookie.Value, ":")
	if !ok || subtle.ConstantTimeCompare([]byte(wantState), []byte(r.URL.Query().Get("state"))) != 1 {
		return nil, "", ErrOIDCState
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		return nil, "", fmt.Errorf("auth: OIDC callback without a code")
	}

	tok, err := m.oidc.oauth.Exchange(ctx, code)
	if err != nil {
		return nil, "", fmt.Errorf("auth: OIDC token exchange: %w", err)
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok {
		return nil, "", errors.New("auth: OIDC response had no id_token")
	}
	idToken, err := m.oidc.verifier.Verify(ctx, rawID)
	if err != nil {
		return nil, "", fmt.Errorf("auth: id_token verification: %w", err)
	}
	if idToken.Nonce != wantNonce {
		return nil, "", ErrOIDCState
	}

	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return nil, "", err
	}
	username := firstString(claims, "preferred_username", "email", "sub")
	if username == "" {
		return nil, "", errors.New("auth: OIDC claims carried no usable username")
	}
	displayName := firstString(claims, "name", "preferred_username")
	role := RoleUser
	if m.oidc.adminGroup != "" && hasGroup(claims[m.oidc.groupsClaim], m.oidc.adminGroup) {
		role = RoleAdmin
	}

	u, err := m.userForSubject(ctx, idToken.Subject, username, displayName, role)
	if err != nil {
		return nil, "", err
	}
	if u.Disabled {
		return nil, "", ErrDisabled
	}
	sid, err := m.CreateSession(ctx, u.ID, r.UserAgent(), ClientIP(r))
	if err != nil {
		return nil, "", err
	}
	return u, sid, nil
}

// userForSubject finds the account bound to an OIDC subject, linking or
// creating one as needed.
func (m *Manager) userForSubject(ctx context.Context, subject, username, displayName, role string) (*User, error) {
	row := m.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE oidc_subject = ?`, subject)
	u, err := scanUser(row)
	if err == nil {
		if m.oidc.adminGroup != "" && u.Role != role && u.Role != RoleRestricted {
			if err := m.SetRole(ctx, u.ID, role); err != nil {
				return nil, err
			}
			u.Role = role
		}
		return u, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	// A local account with the same username is adopted, so an operator can
	// pre-create accounts and grant library access before first login.
	existing, err := m.UserByUsername(ctx, username)
	switch {
	case err == nil:
		if _, err := m.db.ExecContext(ctx, `UPDATE users SET oidc_subject = ? WHERE id = ?`, subject, existing.ID); err != nil {
			return nil, err
		}
		return m.UserByID(ctx, existing.ID)
	case errors.Is(err, store.ErrNotFound):
	default:
		return nil, err
	}

	created, err := m.CreateUser(ctx, username, "", displayName, role)
	if err != nil {
		return nil, err
	}
	if _, err := m.db.ExecContext(ctx, `UPDATE users SET oidc_subject = ? WHERE id = ?`, subject, created.ID); err != nil {
		return nil, err
	}
	return m.UserByID(ctx, created.ID)
}

func firstString(claims map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := claims[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func hasGroup(claim any, want string) bool {
	switch v := claim.(type) {
	case []any:
		for _, g := range v {
			if s, ok := g.(string); ok && s == want {
				return true
			}
		}
	case []string:
		for _, g := range v {
			if g == want {
				return true
			}
		}
	case string:
		for _, g := range strings.Split(v, ",") {
			if strings.TrimSpace(g) == want {
				return true
			}
		}
	}
	return false
}

func appendUnique(list []string, v string) []string {
	for _, s := range list {
		if s == v {
			return list
		}
	}
	return append(list, v)
}
