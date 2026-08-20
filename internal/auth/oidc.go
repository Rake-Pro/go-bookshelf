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
	"github.com/rake-pro/go-bookshelf/internal/settings"
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

// ErrNoAccount is returned when a verified identity has no local account and
// automatic registration is off.
var ErrNoAccount = errors.New("auth: no account exists for this identity")

// ErrNotAuthorized is returned when a verified identity is in neither of the
// configured groups.
var ErrNotAuthorized = errors.New("auth: this identity is not authorized for this application")

type oidcClient struct {
	verifier     *oidc.IDTokenVerifier
	oauth        oauth2.Config
	groupsClaim  string
	adminGroup   string
	userGroup    string
	autoRegister bool
}

// newOIDCClient runs discovery against the stored issuer and builds the
// verifier and OAuth client from it. Every caller reaches it through
// Manager.Prepare, so a failure here is what turns a bad issuer into a rejected
// settings save rather than a broken sign-in later.
func newOIDCClient(ctx context.Context, s settings.Settings) (*oidcClient, error) {
	provider, err := oidc.NewProvider(ctx, s.OIDC.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	scopes := []string{oidc.ScopeOpenID, "profile", "email"}
	if s.OIDC.Scopes != "" {
		scopes = strings.FieldsFunc(s.OIDC.Scopes, func(r rune) bool { return r == ',' || r == ' ' })
	}
	// Either group mapping needs the claim to actually arrive, and most
	// providers only send it when the scope is asked for.
	if s.OIDC.AdminGroup != "" || s.OIDC.UserGroup != "" {
		scopes = appendUnique(scopes, "groups")
	}
	return &oidcClient{
		verifier: provider.Verifier(&oidc.Config{ClientID: s.OIDC.ClientID}),
		oauth: oauth2.Config{
			ClientID:     s.OIDC.ClientID,
			ClientSecret: s.OIDC.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  s.OIDCRedirectURL(),
			Scopes:       scopes,
		},
		groupsClaim:  s.OIDC.GroupsClaim,
		adminGroup:   s.OIDC.AdminGroup,
		userGroup:    s.OIDC.UserGroup,
		autoRegister: s.OIDC.AutoRegister,
	}, nil
}

// Discover probes an issuer without touching the running configuration. It
// backs the admin page's "Test" button, so an operator finds out that a URL is
// wrong before saving it rather than after being locked out by it.
func Discover(ctx context.Context, s settings.Settings) error {
	_, err := newOIDCClient(ctx, s)
	return err
}

// StartOIDC sets the state cookie and returns the provider URL to redirect to.
func (m *Manager) StartOIDC(w http.ResponseWriter) (string, error) {
	cur := m.snapshot()
	if cur.oidc == nil {
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
		Secure:   cur.secureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600,
	})
	return cur.oidc.oauth.AuthCodeURL(state, oidc.Nonce(nonce)), nil
}

// CompleteOIDC validates the callback, maps the claims onto a local account,
// and returns the user with a fresh session id.
func (m *Manager) CompleteOIDC(ctx context.Context, w http.ResponseWriter, r *http.Request) (*User, string, error) {
	client := m.snapshot().oidc
	if client == nil {
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

	tok, err := client.oauth.Exchange(ctx, code)
	if err != nil {
		return nil, "", fmt.Errorf("auth: OIDC token exchange: %w", err)
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok {
		return nil, "", errors.New("auth: OIDC response had no id_token")
	}
	idToken, err := client.verifier.Verify(ctx, rawID)
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
	role, ok := client.roleFor(claims)
	if !ok {
		// Refused before any account exists for the identity: a directory this
		// server does not serve must not accumulate rows in its users table.
		return nil, "", ErrNotAuthorized
	}

	u, err := m.userForSubject(ctx, client, idToken.Subject, username, displayName, role)
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

// roleFor maps the token's group claim onto a role. The second return is
// whether the identity may sign in at all: with a user group configured,
// membership of one of the two groups is the entry requirement, and without
// one any authenticated identity is an ordinary user.
func (c *oidcClient) roleFor(claims map[string]any) (string, bool) {
	groups := claims[c.groupsClaim]
	if c.adminGroup != "" && hasGroup(groups, c.adminGroup) {
		return RoleAdmin, true
	}
	if c.userGroup != "" && !hasGroup(groups, c.userGroup) {
		return "", false
	}
	return RoleUser, true
}

// userForSubject finds the account bound to an OIDC subject, linking or
// creating one as needed.
func (m *Manager) userForSubject(ctx context.Context, client *oidcClient, subject, username, displayName, role string) (*User, error) {
	row := m.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE oidc_subject = ?`, subject)
	u, err := scanUser(row)
	if err == nil {
		if client.shouldApplyRole(u, role) {
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

	// With automatic registration off, an identity the provider vouches for is
	// still refused unless an account was created for it first. That is the
	// difference between "anyone in the directory may read the library" and
	// "these people may".
	if !client.autoRegister {
		return nil, ErrNoAccount
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

// shouldApplyRole decides whether a sign-in may rewrite an existing account's
// role. Group membership is re-evaluated on every sign-in, so a promotion or a
// demotion at the provider follows the user here - with two exceptions.
//
// A restricted account is never touched: that role is a local decision the
// directory knows nothing about. Neither is an administrator who still has a
// local password: that is the break-glass account, and letting a directory
// change demote it would remove the only way back in the moment the directory
// is what went wrong.
func (c *oidcClient) shouldApplyRole(u *User, role string) bool {
	if c.adminGroup == "" && c.userGroup == "" {
		return false
	}
	if u.Role == role || u.Role == RoleRestricted {
		return false
	}
	if u.Role == RoleAdmin && role != RoleAdmin && u.HasPassword {
		return false
	}
	return true
}
