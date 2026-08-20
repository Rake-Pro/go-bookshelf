package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Cookie security modes for General.SecureCookies.
const (
	CookiesAuto = "auto"
	CookiesOn   = "on"
	CookiesOff  = "off"
)

// Metadata providers. ProviderNone is the default and means no outbound
// request is ever made.
const (
	ProviderNone        = "none"
	ProviderOpenLibrary = "openlibrary"
)

// DefaultMetricsAllow is the /metrics allow list used when none is configured.
var DefaultMetricsAllow = []string{
	"127.0.0.0/8", "::1/128",
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
	"fc00::/7",
}

// ErrInvalid wraps every validation failure so the API layer can answer 400
// without inspecting the message.
var ErrInvalid = errors.New("settings")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Duration is a time.Duration that marshals as the string a human typed
// ("720h", "6h"), so the stored document stays readable and an admin form can
// round-trip it without unit guessing.
type Duration time.Duration

// D returns the wrapped time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// String renders the duration in Go's own notation.
func (d Duration) String() string { return time.Duration(d).String() }

// MarshalJSON writes the duration as a string.
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

// UnmarshalJSON accepts a duration string; a number is refused rather than
// guessed at, because "3600" is equally defensible as seconds or milliseconds.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return errors.New("duration must be a string such as \"720h\"")
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return fmt.Errorf("%q is not a duration such as \"720h\" or \"90m\"", s)
	}
	*d = Duration(parsed)
	return nil
}

// General is the hosting-facing behaviour that is not bootstrap.
type General struct {
	// BaseURL is the URL users reach the server on. It decides the OIDC
	// redirect URI, the identifiers in OPDS feeds, and - under the "auto"
	// cookie mode - whether the session cookie is marked Secure. Stored
	// without a trailing slash.
	BaseURL string `json:"base_url"`

	// SecureCookies is "auto", "on" or "off". Auto follows BaseURL's scheme.
	SecureCookies string `json:"secure_cookies"`

	SessionTTL   Duration `json:"session_ttl"`
	ScanInterval Duration `json:"scan_interval"`
}

// OIDC is the OpenID Connect client configuration.
type OIDC struct {
	Enabled bool `json:"enabled"`

	// Issuer is stored verbatim apart from surrounding whitespace. go-oidc
	// compares a token's iss claim, and the discovery document's own issuer
	// field, against this value byte for byte, and some providers legitimately
	// publish an issuer that ends in "/". Trimming it here would turn a valid
	// configuration into a verification failure at sign-in time.
	Issuer   string `json:"issuer"`
	ClientID string `json:"client_id"`

	// ClientSecret is encrypted at rest and never leaves the process.
	ClientSecret string `json:"client_secret"`

	// AdminGroup, when set, grants the admin role to members of that group on
	// every sign-in.
	AdminGroup string `json:"admin_group"`

	// UserGroup, when set, is the membership requirement for signing in at
	// all: an identity in neither AdminGroup nor UserGroup is refused and no
	// account is created for it. Left empty, any identity the provider
	// authenticates may sign in and gets the ordinary user role.
	UserGroup string `json:"user_group"`

	// GroupsClaim is the claim the two group names are matched against.
	GroupsClaim string `json:"groups_claim"`

	// Scopes is a comma or space separated list; empty means openid, profile
	// and email.
	Scopes string `json:"scopes"`

	// AutoRegister creates an account the first time an unknown identity signs
	// in. With it off, an account must already exist (by username) for the
	// sign-in to be accepted.
	AutoRegister bool `json:"auto_register"`

	// LocalLoginEnabled keeps the username and password form available. It can
	// only be turned off while OIDC is on, and GOBOOKSHELF_ADMIN_RECOVERY
	// overrides it for the duration of a restart.
	LocalLoginEnabled bool `json:"local_login_enabled"`
}

// Configured reports whether enough is set to attempt discovery.
func (o OIDC) Configured() bool {
	return o.Enabled && o.Issuer != "" && o.ClientID != "" && o.ClientSecret != ""
}

// ProxyAuth is authentication delegated to a reverse proxy.
type ProxyAuth struct {
	Enabled bool   `json:"enabled"`
	Header  string `json:"header"`

	// TrustedProxies are the CIDRs (or bare addresses) whose requests may
	// carry Header. It must be non-empty while Enabled, because the header is
	// otherwise a way for anyone who can reach the port to name any user.
	TrustedProxies []string `json:"trusted_proxies"`
}

// Metadata is the online metadata and cover lookup configuration.
type Metadata struct {
	Provider string `json:"provider"`

	// AllowPrivate lets the fetcher reach private and loopback addresses. Only
	// useful when the provider runs on the same network.
	AllowPrivate bool `json:"allow_private"`
}

// Enabled reports whether outbound metadata lookup may happen at all.
func (m Metadata) Enabled() bool { return m.Provider != "" && m.Provider != ProviderNone }

// Metrics limits who may read /metrics.
type Metrics struct {
	Allow []string `json:"allow"`
}

// Settings is the whole application configuration document.
type Settings struct {
	General   General   `json:"general"`
	OIDC      OIDC      `json:"oidc"`
	ProxyAuth ProxyAuth `json:"proxy_auth"`
	Metadata  Metadata  `json:"metadata"`
	Metrics   Metrics   `json:"metrics"`

	// SetupComplete is the wizard's finish line. While it is false the server
	// answers every non-setup API route with 403 setup_required.
	SetupComplete bool `json:"setup_complete"`

	UpdatedAt string `json:"updated_at"`
}

// Default returns the configuration a fresh database starts with.
func Default() Settings {
	return Settings{
		General: General{
			BaseURL:       "http://localhost:8080",
			SecureCookies: CookiesAuto,
			SessionTTL:    Duration(30 * 24 * time.Hour),
			ScanInterval:  Duration(6 * time.Hour),
		},
		OIDC: OIDC{
			GroupsClaim:       "groups",
			AutoRegister:      true,
			LocalLoginEnabled: true,
		},
		Metadata: Metadata{Provider: ProviderNone},
		Metrics:  Metrics{Allow: append([]string(nil), DefaultMetricsAllow...)},
	}
}

// OIDCRedirectURL is the callback URI to register with the provider.
func (s Settings) OIDCRedirectURL() string {
	return s.General.BaseURL + "/api/v1/auth/oidc/callback"
}

// SecureCookies resolves the cookie mode against the base URL.
func (s Settings) SecureCookies() bool {
	switch s.General.SecureCookies {
	case CookiesOn:
		return true
	case CookiesOff:
		return false
	default:
		return strings.HasPrefix(s.General.BaseURL, "https://")
	}
}

// Normalize fills in defaults and canonicalises the values that have more than
// one spelling, so validation and storage see one form.
func (s *Settings) Normalize() {
	s.General.BaseURL = strings.TrimRight(strings.TrimSpace(s.General.BaseURL), "/")
	switch s.General.SecureCookies {
	case CookiesOn, CookiesOff:
	default:
		s.General.SecureCookies = CookiesAuto
	}
	if s.General.SessionTTL <= 0 {
		s.General.SessionTTL = Default().General.SessionTTL
	}

	// Only the surrounding whitespace: see the Issuer field comment.
	s.OIDC.Issuer = strings.TrimSpace(s.OIDC.Issuer)
	s.OIDC.ClientID = strings.TrimSpace(s.OIDC.ClientID)
	s.OIDC.AdminGroup = strings.TrimSpace(s.OIDC.AdminGroup)
	s.OIDC.UserGroup = strings.TrimSpace(s.OIDC.UserGroup)
	s.OIDC.GroupsClaim = strings.TrimSpace(s.OIDC.GroupsClaim)
	if s.OIDC.GroupsClaim == "" {
		s.OIDC.GroupsClaim = "groups"
	}
	s.OIDC.Scopes = strings.TrimSpace(s.OIDC.Scopes)

	s.ProxyAuth.Header = strings.TrimSpace(s.ProxyAuth.Header)
	s.ProxyAuth.TrustedProxies = cleanList(s.ProxyAuth.TrustedProxies)

	s.Metadata.Provider = strings.ToLower(strings.TrimSpace(s.Metadata.Provider))
	if s.Metadata.Provider == "" {
		s.Metadata.Provider = ProviderNone
	}

	s.Metrics.Allow = cleanList(s.Metrics.Allow)
	if len(s.Metrics.Allow) == 0 {
		s.Metrics.Allow = append([]string(nil), DefaultMetricsAllow...)
	}
}

// Validate reports the first problem that would make the configuration unsafe
// or unusable. It runs on the wizard's writes as well as the admin page's, so
// there is one definition of a valid document.
func (s Settings) Validate() error {
	u, err := url.Parse(s.General.BaseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return invalid("base URL %q must be absolute, for example https://books.example.com", s.General.BaseURL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return invalid("base URL must use http or https")
	}
	if s.General.SessionTTL.D() < time.Minute {
		return invalid("session lifetime must be at least one minute")
	}
	if s.General.ScanInterval < 0 {
		return invalid("scan interval cannot be negative")
	}
	if s.General.ScanInterval > 0 && s.General.ScanInterval.D() < time.Minute {
		return invalid("scan interval must be zero (off) or at least one minute")
	}

	if s.OIDC.Enabled {
		iss, err := url.Parse(s.OIDC.Issuer)
		if err != nil || iss.Scheme == "" || iss.Host == "" {
			return invalid("OIDC issuer %q must be an absolute http or https URL", s.OIDC.Issuer)
		}
		if iss.Scheme != "http" && iss.Scheme != "https" {
			return invalid("OIDC issuer must use http or https")
		}
		if base, err := url.Parse(s.General.BaseURL); err == nil && base.Host != "" &&
			strings.EqualFold(base.Host, iss.Host) {
			return invalid("OIDC issuer %q points at this application; it must be your identity provider's issuer URL (not the redirect URI)", s.OIDC.Issuer)
		}
		if s.OIDC.ClientID == "" {
			return invalid("an OIDC client id is required")
		}
		if s.OIDC.ClientSecret == "" {
			return invalid("an OIDC client secret is required")
		}
	}
	// Turning the password form off while OIDC is off would leave no way in at
	// all, so it is refused rather than recovered from.
	if !s.OIDC.LocalLoginEnabled && !s.OIDC.Enabled {
		return invalid("password sign-in can only be turned off while OIDC sign-in is on")
	}

	if s.ProxyAuth.Enabled {
		if s.ProxyAuth.Header == "" {
			return invalid("a header name is required for reverse-proxy authentication")
		}
		if len(s.ProxyAuth.TrustedProxies) == 0 {
			return invalid("reverse-proxy authentication needs at least one trusted proxy CIDR, " +
				"otherwise the header would be honored from any source address")
		}
		if _, err := ParseCIDRs(s.ProxyAuth.TrustedProxies); err != nil {
			return invalid("trusted proxies: %s", err)
		}
	}

	switch s.Metadata.Provider {
	case ProviderNone, ProviderOpenLibrary:
	default:
		return invalid("unknown metadata provider %q", s.Metadata.Provider)
	}

	if _, err := ParseCIDRs(s.Metrics.Allow); err != nil {
		return invalid("metrics allow list: %s", err)
	}
	return nil
}

// ---------------------------------------------------------------- storage ---

// DB is the slice of the database handle this package needs.
type DB interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Applier is a subsystem that reconfigures itself when the settings change.
//
// Prepare does the fallible part - OIDC discovery, CIDR parsing - and returns
// the function that swaps the new configuration in. Splitting it in two is
// what lets a save be rejected whole: nothing is applied and nothing is
// persisted unless every applier could prepare.
type Applier interface {
	Prepare(ctx context.Context, s Settings) (apply func(), err error)
}

// Service owns the stored settings document and the live reconfiguration of
// whatever depends on it.
type Service struct {
	db  DB
	key []byte

	mu          sync.RWMutex
	cur         Settings
	trustedNets []*net.IPNet
	metricsNets []*net.IPNet

	appliers []Applier
}

// New loads the settings row, creating it from the defaults on a fresh
// database.
//
// usersExist marks an upgrade rather than a first run: a database that already
// has accounts was configured before the wizard existed, so it starts with
// setup already complete and the admin edits the rest at /admin/settings.
func New(ctx context.Context, db DB, key []byte, usersExist bool) (*Service, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("settings: %w (got %d)", ErrKeyLength, len(key))
	}
	s := &Service{db: db, key: key}

	var raw string
	err := db.QueryRowContext(ctx, `SELECT data FROM settings WHERE id = 1`).Scan(&raw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		seed := Default()
		seed.SetupComplete = usersExist
		if err := s.write(ctx, seed); err != nil {
			return nil, err
		}
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("read settings: %w", err)
	}

	loaded, err := s.decode(raw)
	if err != nil {
		return nil, err
	}
	if err := s.cache(loaded); err != nil {
		return nil, err
	}
	return s, nil
}

// Register adds an applier. Appliers are prepared in registration order.
func (s *Service) Register(a Applier) { s.appliers = append(s.appliers, a) }

// Get returns a copy of the current settings, secrets included. Callers that
// serialise it to a client must blank the secret fields first; Redacted does
// that for them.
func (s *Service) Get() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := s.cur
	out.Metrics.Allow = append([]string(nil), s.cur.Metrics.Allow...)
	out.ProxyAuth.TrustedProxies = append([]string(nil), s.cur.ProxyAuth.TrustedProxies...)
	return out
}

// TrustedProxyNets returns the parsed reverse-proxy allow list.
func (s *Service) TrustedProxyNets() []*net.IPNet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.trustedNets
}

// MetricsAllowNets returns the parsed /metrics allow list.
func (s *Service) MetricsAllowNets() []*net.IPNet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.metricsNets
}

// Save validates, prepares, persists and applies a new document, in that
// order. A rejected preparation - an issuer that does not answer discovery, a
// CIDR that does not parse - leaves both the stored document and the running
// configuration untouched.
func (s *Service) Save(ctx context.Context, next Settings) error {
	next.Normalize()
	if err := next.Validate(); err != nil {
		return err
	}
	next.SetupComplete = s.Get().SetupComplete
	next.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	applies := make([]func(), 0, len(s.appliers))
	for _, a := range s.appliers {
		apply, err := a.Prepare(ctx, next)
		if err != nil {
			return err
		}
		applies = append(applies, apply)
	}
	if err := s.write(ctx, next); err != nil {
		return err
	}
	for _, apply := range applies {
		apply()
	}
	return nil
}

// Apply prepares and applies the current settings without writing anything. It
// is how startup pushes the stored document into the subsystems, and it
// reports a preparation failure (an unreachable identity provider) without
// treating it as fatal - the caller decides.
func (s *Service) Apply(ctx context.Context) error {
	cur := s.Get()
	var firstErr error
	for _, a := range s.appliers {
		apply, err := a.Prepare(ctx, cur)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if apply != nil {
			apply()
		}
	}
	return firstErr
}

// MarkSetupComplete closes the wizard. There is no API path back to false:
// reopening it means editing the settings row directly, which is what makes
// the gate meaningful.
func (s *Service) MarkSetupComplete(ctx context.Context) error {
	next := s.Get()
	if next.SetupComplete {
		return nil
	}
	next.SetupComplete = true
	next.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return s.write(ctx, next)
}

// SetupComplete reports whether the wizard has been finished.
func (s *Service) SetupComplete() bool { return s.Get().SetupComplete }

// write persists the document (encrypting the secret fields) and caches it.
func (s *Service) write(ctx context.Context, next Settings) error {
	stored := next
	sealed, err := Encrypt(s.key, next.OIDC.ClientSecret)
	if err != nil {
		return err
	}
	stored.OIDC.ClientSecret = sealed

	blob, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	if next.UpdatedAt == "" {
		next.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (id, data, updated_at) VALUES (1, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		string(blob), next.UpdatedAt); err != nil {
		return fmt.Errorf("store settings: %w", err)
	}
	return s.cache(next)
}

// decode parses a stored row and decrypts its secret fields.
func (s *Service) decode(raw string) (Settings, error) {
	out := Default()
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return out, fmt.Errorf("parse stored settings: %w", err)
	}
	secret, err := Decrypt(s.key, out.OIDC.ClientSecret)
	if err != nil {
		return out, fmt.Errorf("oidc client secret: %w", err)
	}
	out.OIDC.ClientSecret = secret
	return out, nil
}

// cache normalises and stores the document plus its parsed CIDR sets.
func (s *Service) cache(next Settings) error {
	next.Normalize()
	trusted, err := ParseCIDRs(next.ProxyAuth.TrustedProxies)
	if err != nil {
		return invalid("trusted proxies: %s", err)
	}
	metrics, err := ParseCIDRs(next.Metrics.Allow)
	if err != nil {
		return invalid("metrics allow list: %s", err)
	}
	s.mu.Lock()
	s.cur, s.trustedNets, s.metricsNets = next, trusted, metrics
	s.mu.Unlock()
	return nil
}

// ----------------------------------------------------------- CIDR helpers ---

// ParseCIDRs parses a list of CIDRs. A bare address is accepted and treated as
// a single-host network, because that is what an operator means when they
// write one proxy's address.
func ParseCIDRs(in []string) ([]*net.IPNet, error) {
	out := make([]*net.IPNet, 0, len(in))
	for _, raw := range in {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if !strings.Contains(raw, "/") {
			ip := net.ParseIP(raw)
			if ip == nil {
				return nil, fmt.Errorf("%q is not an IP address or CIDR", raw)
			}
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		_, n, err := net.ParseCIDR(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// IPInNets reports whether ip falls inside any of nets.
func IPInNets(ip net.IP, nets []*net.IPNet) bool {
	if ip == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// SplitList turns a comma or newline separated field into a clean list.
func SplitList(v string) []string {
	return cleanList(strings.FieldsFunc(v, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	}))
}

func cleanList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Redacted returns a copy with every secret field blanked, for anything that
// leaves the process. HasClientSecret survives so a form can show "stored"
// without ever seeing the value.
func (s Settings) Redacted() Settings {
	s.OIDC.ClientSecret = ""
	return s
}

// HasClientSecret reports whether an OIDC client secret is stored.
func (s Settings) HasClientSecret() bool { return s.OIDC.ClientSecret != "" }
