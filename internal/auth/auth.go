// Package auth owns identities: local accounts hashed with argon2id, login
// sessions, API tokens, the one-time first-run setup token, optional OIDC
// login, and the reverse-proxy header mode.
//
// Every request arrives with at most one credential. Credentials are looked up
// in the table that issues them - a session id is only ever matched against
// sessions, a bearer token only against api_tokens - so a value taken from one
// channel cannot be replayed through the other.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rake-pro/go-bookshelf/internal/config"
	"github.com/rake-pro/go-bookshelf/internal/settings"
	"github.com/rake-pro/go-bookshelf/internal/store"
	"github.com/rs/zerolog/log"
)

// Roles a user may hold.
const (
	RoleAdmin      = "admin"
	RoleUser       = "user"
	RoleRestricted = "restricted"
)

// Scopes an API token may carry.
const (
	ScopeRead  = "read"
	ScopeWrite = "write"
)

// SessionCookie is the name of the login session cookie.
const SessionCookie = "gbs_session"

// TokenPrefix marks a value as an API token. It is only a readability aid;
// tokens are still validated by hash lookup.
const TokenPrefix = "gbsk_"

// Errors surfaced to callers.
var (
	ErrUnauthenticated = errors.New("auth: not authenticated")
	ErrBadCredentials  = errors.New("auth: invalid username or password")
	ErrDisabled        = errors.New("auth: account disabled")
	ErrSetupDone       = errors.New("auth: setup has already been completed")
	ErrSetupToken      = errors.New("auth: invalid setup token")
	ErrRateLimited     = errors.New("auth: too many attempts")
	ErrLocalLoginOff   = errors.New("auth: password sign-in is disabled")
	ErrWeakPassword    = fmt.Errorf("auth: password must be at least %d characters", MinPasswordLength)
)

// User is an account record.
type User struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	CreatedAt   string `json:"created_at"`
	Disabled    bool   `json:"disabled"`
	HasPassword bool   `json:"has_password"`
	OIDCSubject string `json:"-"`
}

// IsAdmin reports whether the user holds the admin role.
func (u *User) IsAdmin() bool { return u != nil && u.Role == RoleAdmin }

// Identity is an authenticated request's principal plus how it proved itself.
type Identity struct {
	User      *User
	Method    string // "session", "token", "proxy" or "oidc"
	Scopes    []string
	SessionID string
	TokenID   int64
}

// HasScope reports whether the identity may perform an operation needing scope.
// Session logins carry full authority; API tokens carry only what they were
// issued with.
func (i *Identity) HasScope(scope string) bool {
	if i == nil {
		return false
	}
	if i.Method != "token" {
		return true
	}
	for _, s := range i.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// live is everything the manager reads out of the stored settings. It is
// replaced wholesale on a settings save, so a request either sees the old
// configuration or the new one and never a half-applied mixture.
type live struct {
	sessionTTL    time.Duration
	secureCookies bool

	proxyHeader string
	trustedNets []*net.IPNet

	localLogin   bool
	autoRegister bool

	oidc *oidcClient
}

// Manager is the auth service.
type Manager struct {
	db  *store.DB
	cfg config.Config

	loginLimiter *Limiter
	setupLimiter *Limiter

	mu  sync.RWMutex
	cur live
}

// New builds a Manager. It performs no network calls: the identity provider is
// configured by Prepare, from the stored settings, and can be reconfigured at
// runtime without a restart.
func New(db *store.DB, cfg config.Config) *Manager {
	return &Manager{
		db:  db,
		cfg: cfg,
		// Ten attempts of burst, then one attempt per six seconds.
		loginLimiter: NewLimiter(10, 6*time.Second),
		setupLimiter: NewLimiter(5, 30*time.Second),
		cur: live{
			sessionTTL:   settings.Default().General.SessionTTL.D(),
			localLogin:   true,
			autoRegister: true,
		},
	}
}

// Prepare implements settings.Applier. Discovery against the configured issuer
// happens here, before anything is stored, so a wrong or unreachable issuer is
// reported as a rejected save. The returned apply is always usable: on a
// discovery failure it installs everything except OIDC, which is what startup
// wants (a provider that is briefly down must not stop local sign-in) and what
// a save must not accept (the admin gets the error instead).
func (m *Manager) Prepare(ctx context.Context, s settings.Settings) (func(), error) {
	next := live{
		sessionTTL:    s.General.SessionTTL.D(),
		secureCookies: s.SecureCookies(),
		localLogin:    s.OIDC.LocalLoginEnabled,
		autoRegister:  s.OIDC.AutoRegister,
	}
	if s.ProxyAuth.Enabled {
		nets, err := settings.ParseCIDRs(s.ProxyAuth.TrustedProxies)
		if err != nil {
			return nil, fmt.Errorf("trusted proxies: %w", err)
		}
		next.proxyHeader, next.trustedNets = s.ProxyAuth.Header, nets
	}

	var discoveryErr error
	if s.OIDC.Configured() {
		client, err := newOIDCClient(ctx, s)
		if err != nil {
			discoveryErr = err
		} else {
			next.oidc = client
		}
	}

	return func() {
		m.mu.Lock()
		m.cur = next
		m.mu.Unlock()
		switch {
		case next.oidc != nil:
			log.Info().Str("issuer", s.OIDC.Issuer).Msg("OIDC login enabled")
		case s.OIDC.Enabled:
			log.Warn().Str("issuer", s.OIDC.Issuer).Msg("OIDC is configured but unavailable; local sign-in only")
		}
	}, discoveryErr
}

// snapshot returns the live configuration.
func (m *Manager) snapshot() live {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cur
}

// Config returns the bootstrap configuration the manager was built with.
func (m *Manager) Config() config.Config { return m.cfg }

// OIDCEnabled reports whether OIDC login is available right now.
func (m *Manager) OIDCEnabled() bool { return m.snapshot().oidc != nil }

// SessionTTL is how long a new session will last.
func (m *Manager) SessionTTL() time.Duration { return m.snapshot().sessionTTL }

// LocalLoginEnabled reports whether the password form may be offered.
// GOBOOKSHELF_ADMIN_RECOVERY forces it on: it is the way back in when an
// identity provider that is the only door stops opening.
func (m *Manager) LocalLoginEnabled() bool {
	return m.snapshot().localLogin || m.cfg.AdminRecovery
}

// ---------------------------------------------------------------- users ----

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var (
		u           User
		display     sql.NullString
		passwd      sql.NullString
		subject     sql.NullString
		disabledAt  sql.NullString
		createdAt   string
		usernameRaw string
	)
	if err := row.Scan(&u.ID, &usernameRaw, &display, &passwd, &subject, &u.Role, &createdAt, &disabledAt); err != nil {
		return nil, err
	}
	u.Username = usernameRaw
	u.DisplayName = display.String
	u.CreatedAt = createdAt
	u.Disabled = disabledAt.Valid
	u.HasPassword = passwd.Valid && passwd.String != ""
	u.OIDCSubject = subject.String
	return &u, nil
}

const userColumns = `id, username, display_name, password_hash, oidc_subject, role, created_at, disabled_at`

// UserByID looks up a user by id.
func (m *Manager) UserByID(ctx context.Context, id int64) (*User, error) {
	row := m.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return u, err
}

// UserByUsername looks up a user by username (case-insensitive).
func (m *Manager) UserByUsername(ctx context.Context, username string) (*User, error) {
	// Folded on both sides rather than with COLLATE NOCASE, which only SQLite
	// understands. The parameter is cast because Postgres also has a
	// range-valued lower(), and a bare placeholder inside the call would be
	// ambiguous between the two overloads.
	row := m.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE lower(username) = lower(CAST(? AS TEXT))`, username)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return u, err
}

// ListUsers returns every account, oldest first.
func (m *Manager) ListUsers(ctx context.Context) ([]*User, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CreateUser inserts a local account. An empty password creates an account
// that can only sign in through OIDC or a trusted proxy.
func (m *Manager) CreateUser(ctx context.Context, username, password, displayName, role string) (*User, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, errors.New("auth: username is required")
	}
	switch role {
	case RoleAdmin, RoleUser, RoleRestricted:
	default:
		return nil, fmt.Errorf("auth: unknown role %q", role)
	}
	var hash any
	if password != "" {
		if len([]rune(password)) < MinPasswordLength {
			return nil, ErrWeakPassword
		}
		h, err := HashPassword(password, DefaultArgonParams)
		if err != nil {
			return nil, err
		}
		hash = h
	}
	if displayName == "" {
		displayName = username
	}
	id, err := m.db.InsertReturningID(ctx,
		`INSERT INTO users (username, display_name, password_hash, role, created_at) VALUES (?, ?, ?, ?, ?)`,
		username, displayName, hash, role, store.Now())
	if err != nil {
		return nil, err
	}
	return m.UserByID(ctx, id)
}

// SetPassword replaces a user's password.
func (m *Manager) SetPassword(ctx context.Context, userID int64, password string) error {
	if len([]rune(password)) < MinPasswordLength {
		return ErrWeakPassword
	}
	h, err := HashPassword(password, DefaultArgonParams)
	if err != nil {
		return err
	}
	_, err = m.db.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, h, userID)
	return err
}

// SetDisabled enables or disables an account, dropping its sessions when
// disabling.
func (m *Manager) SetDisabled(ctx context.Context, userID int64, disabled bool) error {
	var at any
	if disabled {
		at = store.Now()
	}
	if _, err := m.db.ExecContext(ctx, `UPDATE users SET disabled_at = ? WHERE id = ?`, at, userID); err != nil {
		return err
	}
	if disabled {
		if _, err := m.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
			return err
		}
	}
	return nil
}

// SetRole changes a user's role.
func (m *Manager) SetRole(ctx context.Context, userID int64, role string) error {
	switch role {
	case RoleAdmin, RoleUser, RoleRestricted:
	default:
		return fmt.Errorf("auth: unknown role %q", role)
	}
	_, err := m.db.ExecContext(ctx, `UPDATE users SET role = ? WHERE id = ?`, role, userID)
	return err
}

// DeleteUser removes an account and everything owned by it.
func (m *Manager) DeleteUser(ctx context.Context, userID int64) error {
	_, err := m.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, userID)
	return err
}

// AdminsWithOIDC counts enabled administrators already linked to an identity
// at the provider. It is what tells the settings handler whether turning the
// password form off would leave a way back in.
func (m *Manager) AdminsWithOIDC(ctx context.Context) (int, error) {
	var n int
	err := m.db.QueryRowContext(ctx,
		`SELECT count(*) FROM users
		 WHERE role = ? AND disabled_at IS NULL AND oidc_subject IS NOT NULL AND oidc_subject <> ''`,
		RoleAdmin).Scan(&n)
	return n, err
}

// UserCount returns the number of accounts.
func (m *Manager) UserCount(ctx context.Context) (int, error) {
	var n int
	err := m.db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// ------------------------------------------------------- library access ----

// LibraryIDs returns the library ids a user may see. Admins see everything.
func (m *Manager) LibraryIDs(ctx context.Context, u *User) ([]int64, error) {
	query := `SELECT library_id FROM user_library_access WHERE user_id = ? ORDER BY library_id`
	args := []any{u.ID}
	if u.IsAdmin() {
		query, args = `SELECT id FROM libraries ORDER BY id`, nil
	}
	rows, err := m.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SetLibraryAccess replaces a user's library grants.
func (m *Manager) SetLibraryAccess(ctx context.Context, userID int64, libraryIDs []int64) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_library_access WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, id := range libraryIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO user_library_access (user_id, library_id) VALUES (?, ?) ON CONFLICT DO NOTHING`, userID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CanAccessLibrary reports whether u may read from libraryID.
func (m *Manager) CanAccessLibrary(ctx context.Context, u *User, libraryID int64) (bool, error) {
	if u == nil {
		return false, nil
	}
	if u.IsAdmin() {
		return true, nil
	}
	var n int
	err := m.db.QueryRowContext(ctx,
		`SELECT count(*) FROM user_library_access WHERE user_id = ? AND library_id = ?`, u.ID, libraryID).Scan(&n)
	return n > 0, err
}

// ------------------------------------------------------------- sessions ----

// Login verifies a username and password and creates a session.
func (m *Manager) Login(ctx context.Context, username, password, userAgent, ip string) (*User, string, error) {
	if !m.LocalLoginEnabled() {
		return nil, "", ErrLocalLoginOff
	}
	if !m.loginLimiter.Allow(ip) {
		return nil, "", ErrRateLimited
	}
	u, err := m.UserByUsername(ctx, username)
	if errors.Is(err, store.ErrNotFound) {
		// Spend comparable time on an unknown user so the response does not
		// reveal which accounts exist.
		_, _ = VerifyPassword(password, dummyHash)
		return nil, "", ErrBadCredentials
	}
	if err != nil {
		return nil, "", err
	}
	var stored sql.NullString
	if err := m.db.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = ?`, u.ID).Scan(&stored); err != nil {
		return nil, "", err
	}
	if !stored.Valid || stored.String == "" {
		_, _ = VerifyPassword(password, dummyHash)
		return nil, "", ErrBadCredentials
	}
	ok, err := VerifyPassword(password, stored.String)
	if err != nil || !ok {
		return nil, "", ErrBadCredentials
	}
	if u.Disabled {
		return nil, "", ErrDisabled
	}
	sid, err := m.CreateSession(ctx, u.ID, userAgent, ip)
	if err != nil {
		return nil, "", err
	}
	return u, sid, nil
}

// CreateSession issues a new session id for a user.
func (m *Manager) CreateSession(ctx context.Context, userID int64, userAgent, ip string) (string, error) {
	sid, err := randomHex(32)
	if err != nil {
		return "", err
	}
	expires := time.Now().Add(m.SessionTTL())
	_, err = m.db.ExecContext(ctx,
		`INSERT INTO sessions (id, user_id, created_at, expires_at, user_agent, ip) VALUES (?, ?, ?, ?, ?, ?)`,
		sid, userID, store.Now(), store.FormatTime(expires), truncate(userAgent, 512), ip)
	if err != nil {
		return "", err
	}
	return sid, nil
}

// DeleteSession invalidates one session.
func (m *Manager) DeleteSession(ctx context.Context, sid string) error {
	_, err := m.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, sid)
	return err
}

// PruneSessions removes expired sessions.
func (m *Manager) PruneSessions(ctx context.Context) error {
	_, err := m.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, store.Now())
	return err
}

// SessionCookieFor builds the response cookie carrying a session id.
func (m *Manager) SessionCookieFor(sid string) *http.Cookie {
	cur := m.snapshot()
	return &http.Cookie{
		Name:     SessionCookie,
		Value:    sid,
		Path:     "/",
		HttpOnly: true,
		Secure:   cur.secureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(cur.sessionTTL / time.Second),
	}
}

// ClearSessionCookie builds the cookie that removes a session cookie.
func (m *Manager) ClearSessionCookie() *http.Cookie {
	return &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   m.snapshot().secureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	}
}

// ----------------------------------------------------------- api tokens ----

// APIToken is an issued token, without its secret.
type APIToken struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Scopes     string `json:"scopes"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at,omitempty"`
}

// CreateToken issues an API token and returns the record plus the one-time
// secret. The secret is never stored and cannot be shown again.
func (m *Manager) CreateToken(ctx context.Context, userID int64, name string, scopes []string) (APIToken, string, error) {
	clean := make([]string, 0, len(scopes))
	for _, s := range scopes {
		switch s {
		case ScopeRead, ScopeWrite:
			clean = append(clean, s)
		}
	}
	if len(clean) == 0 {
		clean = []string{ScopeRead}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return APIToken{}, "", err
	}
	secret := TokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	id, err := m.db.InsertReturningID(ctx,
		`INSERT INTO api_tokens (user_id, name, token_hash, scopes, created_at) VALUES (?, ?, ?, ?, ?)`,
		userID, truncate(name, 128), hashToken(secret), strings.Join(clean, ","), store.Now())
	if err != nil {
		return APIToken{}, "", err
	}
	return APIToken{ID: id, Name: name, Scopes: strings.Join(clean, ","), CreatedAt: store.Now()}, secret, nil
}

// ListTokens returns a user's tokens.
func (m *Manager) ListTokens(ctx context.Context, userID int64) ([]APIToken, error) {
	rows, err := m.db.QueryContext(ctx,
		`SELECT id, name, scopes, created_at, coalesce(last_used_at, '') FROM api_tokens WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.Name, &t.Scopes, &t.CreatedAt, &t.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteToken revokes one of a user's tokens.
func (m *Manager) DeleteToken(ctx context.Context, userID, tokenID int64) error {
	_, err := m.db.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ? AND user_id = ?`, tokenID, userID)
	return err
}

func hashToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// --------------------------------------------------------------- setup ----

// SetupRequired reports whether first-run setup is still pending.
func (m *Manager) SetupRequired(ctx context.Context) (bool, error) {
	n, err := m.UserCount(ctx)
	if err != nil {
		return false, err
	}
	return n == 0, nil
}

// EnsureSetupToken issues (or reissues) the one-time first-run token when no
// account exists yet. The returned string is the only copy; the database keeps
// a hash. It returns an empty string once setup is complete.
func (m *Manager) EnsureSetupToken(ctx context.Context) (string, error) {
	required, err := m.SetupRequired(ctx)
	if err != nil {
		return "", err
	}
	if !required {
		return "", nil
	}
	token, err := randomHex(24)
	if err != nil {
		return "", err
	}
	if _, err := m.db.ExecContext(ctx,
		`INSERT INTO setup_state (id, token_hash, created_at) VALUES (1, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET token_hash = excluded.token_hash, created_at = excluded.created_at, used_at = NULL`,
		hashToken(token), store.Now()); err != nil {
		return "", err
	}
	return token, nil
}

// CheckSetupToken reports whether the one-time token is the right one, without
// spending it. The wizard uses it to fail its first step immediately instead of
// waiting until the operator has also filled in an account.
func (m *Manager) CheckSetupToken(ctx context.Context, token, ip string) error {
	if !m.setupLimiter.Allow(ip) {
		return ErrRateLimited
	}
	required, err := m.SetupRequired(ctx)
	if err != nil {
		return err
	}
	if !required {
		return ErrSetupDone
	}
	return m.matchSetupToken(ctx, token)
}

// matchSetupToken compares token against the stored hash. It does no rate
// limiting of its own; both callers have already spent an attempt.
func (m *Manager) matchSetupToken(ctx context.Context, token string) error {
	var storedHash string
	err := m.db.QueryRowContext(ctx,
		`SELECT token_hash FROM setup_state WHERE id = 1 AND used_at IS NULL`).Scan(&storedHash)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSetupToken
	}
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(hashToken(token)), []byte(storedHash)) != 1 {
		return ErrSetupToken
	}
	return nil
}

// Setup consumes the first-run token and creates the initial admin account.
//
// The token is claimed before the account is created, with a conditional
// UPDATE whose row count is the claim: two requests arriving together both
// pass the "no accounts yet" check, but only one of them can move used_at away
// from NULL, so only one of them can go on to create an administrator. The
// claim is released again if the account itself is refused - a password the
// server rejects must not burn the only token the operator has.
func (m *Manager) Setup(ctx context.Context, token, username, password, displayName, ip string) (*User, error) {
	if !m.setupLimiter.Allow(ip) {
		return nil, ErrRateLimited
	}
	required, err := m.SetupRequired(ctx)
	if err != nil {
		return nil, err
	}
	if !required {
		return nil, ErrSetupDone
	}
	// Compared in constant time first, so the value is never matched by the
	// database's own byte-at-a-time comparison.
	if err := m.matchSetupToken(ctx, token); err != nil {
		return nil, err
	}
	res, err := m.db.ExecContext(ctx,
		`UPDATE setup_state SET used_at = ? WHERE id = 1 AND used_at IS NULL`, store.Now())
	if err != nil {
		return nil, err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		if err != nil {
			return nil, err
		}
		return nil, ErrSetupToken
	}
	u, err := m.CreateUser(ctx, username, password, displayName, RoleAdmin)
	if err != nil {
		if _, relErr := m.db.ExecContext(ctx,
			`UPDATE setup_state SET used_at = NULL WHERE id = 1`); relErr != nil {
			log.Error().Err(relErr).Msg("releasing the setup token after a failed account creation")
		}
		return nil, err
	}
	return u, nil
}

// ------------------------------------------------------- authentication ----

// Authenticate resolves the credential on a request to an identity. It returns
// ErrUnauthenticated when no valid credential is present.
func (m *Manager) Authenticate(ctx context.Context, r *http.Request) (*Identity, error) {
	if authz := r.Header.Get("Authorization"); authz != "" {
		scheme, value, _ := strings.Cut(authz, " ")
		switch strings.ToLower(scheme) {
		case "bearer":
			return m.identityFromToken(ctx, strings.TrimSpace(value))
		case "basic":
			// OPDS clients authenticate with Basic; the password field carries
			// an API token.
			if user, pass, ok := r.BasicAuth(); ok {
				if id, err := m.identityFromToken(ctx, pass); err == nil {
					return id, nil
				} else if user != "" {
					if id, err := m.identityFromToken(ctx, user); err == nil {
						return id, nil
					}
				}
			}
			return nil, ErrUnauthenticated
		}
	}

	if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
		return m.identityFromSession(ctx, c.Value)
	}

	if id, err := m.identityFromProxy(ctx, r); err == nil {
		return id, nil
	}
	return nil, ErrUnauthenticated
}

func (m *Manager) identityFromSession(ctx context.Context, sid string) (*Identity, error) {
	var (
		userID  int64
		expires string
	)
	err := m.db.QueryRowContext(ctx, `SELECT user_id, expires_at FROM sessions WHERE id = ?`, sid).Scan(&userID, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	if exp := store.ParseTime(expires); exp.IsZero() || time.Now().After(exp) {
		_ = m.DeleteSession(ctx, sid)
		return nil, ErrUnauthenticated
	}
	u, err := m.UserByID(ctx, userID)
	if err != nil {
		return nil, ErrUnauthenticated
	}
	if u.Disabled {
		return nil, ErrDisabled
	}
	return &Identity{User: u, Method: "session", SessionID: sid}, nil
}

func (m *Manager) identityFromToken(ctx context.Context, secret string) (*Identity, error) {
	if secret == "" {
		return nil, ErrUnauthenticated
	}
	var (
		id     int64
		userID int64
		scopes string
	)
	err := m.db.QueryRowContext(ctx,
		`SELECT id, user_id, scopes FROM api_tokens WHERE token_hash = ?`, hashToken(secret)).Scan(&id, &userID, &scopes)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	u, err := m.UserByID(ctx, userID)
	if err != nil {
		return nil, ErrUnauthenticated
	}
	if u.Disabled {
		return nil, ErrDisabled
	}
	if _, err := m.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, store.Now(), id); err != nil {
		return nil, err
	}
	return &Identity{User: u, Method: "token", Scopes: strings.Split(scopes, ","), TokenID: id}, nil
}

// identityFromProxy honors the configured authentication header, but only when
// the request's immediate peer is inside trusted_proxies. Without that check
// anyone able to reach the port could name any user.
func (m *Manager) identityFromProxy(ctx context.Context, r *http.Request) (*Identity, error) {
	cur := m.snapshot()
	if cur.proxyHeader == "" {
		return nil, ErrUnauthenticated
	}
	username := strings.TrimSpace(r.Header.Get(cur.proxyHeader))
	if username == "" {
		return nil, ErrUnauthenticated
	}
	peer := PeerIP(r)
	if !settings.IPInNets(peer, cur.trustedNets) {
		log.Warn().Str("peer", peer.String()).Str("header", cur.proxyHeader).
			Msg("ignoring proxy auth header from an untrusted source address")
		return nil, ErrUnauthenticated
	}
	u, err := m.UserByUsername(ctx, username)
	if errors.Is(err, store.ErrNotFound) {
		// Provision on first sight so the proxy stays the source of truth.
		u, err = m.CreateUser(ctx, username, "", username, RoleUser)
	}
	if err != nil {
		return nil, ErrUnauthenticated
	}
	if u.Disabled {
		return nil, ErrDisabled
	}
	return &Identity{User: u, Method: "proxy"}, nil
}

// PeerIP returns the address of the immediate TCP peer, ignoring any
// forwarding headers: those are attacker-controlled unless a trusted proxy
// rewrote them, and trust is decided from this value.
func PeerIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(strings.Trim(host, "[]"))
}

// ClientIP returns the peer address as a string, for rate limiting and logs.
func ClientIP(r *http.Request) string {
	if ip := PeerIP(r); ip != nil {
		return ip.String()
	}
	return r.RemoteAddr
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// contextKey is unexported so no other package can plant an identity.
type contextKey struct{}

// WithIdentity returns a context carrying an authenticated identity.
func WithIdentity(ctx context.Context, id *Identity) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// FromContext returns the identity attached to ctx, or nil.
func FromContext(ctx context.Context) *Identity {
	id, _ := ctx.Value(contextKey{}).(*Identity)
	return id
}
