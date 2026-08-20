// Package config loads go-bookshelf's bootstrap configuration from an optional
// YAML file, overlaid by GOBOOKSHELF_* environment variables. Environment wins
// so a deployment's injected secrets stay authoritative over a checked-in file.
//
// Only hosting/bootstrap settings live here: listener, database path, data
// directory, external base URL, log level, and the identity-provider wiring
// that must be known before the database is reachable. Libraries (name, kind,
// paths) are runtime data and live in SQLite, not in this file.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// OIDC holds the OpenID Connect client configuration. Login through an
// external identity provider is enabled only when Issuer, ClientID and
// ClientSecret are all set.
type OIDC struct {
	Issuer       string `yaml:"issuer"`
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
	AdminGroup   string `yaml:"admin_group"`
	GroupsClaim  string `yaml:"groups_claim"`
	Scopes       string `yaml:"scopes"`
}

// Enabled reports whether enough OIDC configuration is present to attempt
// discovery at startup.
func (o OIDC) Enabled() bool {
	return o.Issuer != "" && o.ClientID != "" && o.ClientSecret != ""
}

// Config is the fully resolved application configuration.
type Config struct {
	Listen   string `yaml:"listen"`
	DBPath   string `yaml:"db_path"`
	DataDir  string `yaml:"data_dir"`
	BaseURL  string `yaml:"base_url"`
	LogLevel string `yaml:"log_level"`

	OIDC OIDC `yaml:"oidc"`

	// ProxyAuthHeader names a header (for example "Remote-User") that an
	// authenticating reverse proxy sets. It is honored only for requests whose
	// immediate peer address falls inside TrustedProxies.
	ProxyAuthHeader string   `yaml:"proxy_auth_header"`
	TrustedProxies  []string `yaml:"trusted_proxies"`

	// MetricsAllow limits which peers may read /metrics. Empty means the
	// built-in default: loopback plus the private ranges.
	MetricsAllow []string `yaml:"metrics_allow"`

	// SecureCookies marks the session cookie Secure. Defaults to true when
	// BaseURL is https.
	SecureCookies bool `yaml:"secure_cookies"`

	// ScanInterval is how often every library is rescanned in the background.
	ScanInterval time.Duration `yaml:"scan_interval"`

	// SessionTTL is how long a login session stays valid.
	SessionTTL time.Duration `yaml:"session_ttl"`

	// MetadataProvider names an online metadata provider. Empty (the default)
	// disables all outbound network calls for metadata and cover lookup.
	MetadataProvider string `yaml:"metadata_provider"`
	// MetadataAllowPrivate permits the metadata fetcher to reach private and
	// loopback addresses. Only useful when pointing at a provider on the same
	// network as the server, and off by default.
	MetadataAllowPrivate bool `yaml:"metadata_allow_private"`

	trustedNets []*net.IPNet
	metricsNets []*net.IPNet
}

// DefaultMetricsAllow is used when metrics_allow is not configured.
var DefaultMetricsAllow = []string{
	"127.0.0.0/8", "::1/128",
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
	"fc00::/7",
}

// Default returns the configuration used when nothing is set.
func Default() Config {
	return Config{
		Listen:       ":8080",
		DBPath:       "/data/go-bookshelf.db",
		DataDir:      "/data",
		BaseURL:      "http://localhost:8080",
		LogLevel:     "info",
		ScanInterval: 6 * time.Hour,
		SessionTTL:   30 * 24 * time.Hour,
	}
}

// Load reads the optional YAML file at path (or GOBOOKSHELF_CONFIG when path is
// empty), applies GOBOOKSHELF_* overrides, and validates the result.
func Load(path string) (Config, error) {
	cfg := Default()

	if path == "" {
		path = os.Getenv("GOBOOKSHELF_CONFIG")
	}
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return cfg, fmt.Errorf("read config %s: %w", path, err)
		}
		if err := yaml.Unmarshal(b, &cfg); err != nil {
			return cfg, fmt.Errorf("parse config %s: %w", path, err)
		}
	}

	secureSet := false
	env := func(key string, set func(string)) {
		if v, ok := os.LookupEnv(key); ok {
			set(strings.TrimSpace(v))
		}
	}
	env("GOBOOKSHELF_LISTEN", func(v string) { cfg.Listen = v })
	env("GOBOOKSHELF_DB_PATH", func(v string) { cfg.DBPath = v })
	env("GOBOOKSHELF_DATA_DIR", func(v string) { cfg.DataDir = v })
	env("GOBOOKSHELF_BASE_URL", func(v string) { cfg.BaseURL = v })
	env("GOBOOKSHELF_LOG_LEVEL", func(v string) { cfg.LogLevel = v })
	env("GOBOOKSHELF_OIDC_ISSUER", func(v string) { cfg.OIDC.Issuer = v })
	env("GOBOOKSHELF_OIDC_CLIENT_ID", func(v string) { cfg.OIDC.ClientID = v })
	env("GOBOOKSHELF_OIDC_CLIENT_SECRET", func(v string) { cfg.OIDC.ClientSecret = v })
	env("GOBOOKSHELF_OIDC_ADMIN_GROUP", func(v string) { cfg.OIDC.AdminGroup = v })
	env("GOBOOKSHELF_OIDC_GROUPS_CLAIM", func(v string) { cfg.OIDC.GroupsClaim = v })
	env("GOBOOKSHELF_OIDC_SCOPES", func(v string) { cfg.OIDC.Scopes = v })
	env("GOBOOKSHELF_PROXY_AUTH_HEADER", func(v string) { cfg.ProxyAuthHeader = v })
	env("GOBOOKSHELF_TRUSTED_PROXIES", func(v string) { cfg.TrustedProxies = splitList(v) })
	env("GOBOOKSHELF_METRICS_ALLOW", func(v string) { cfg.MetricsAllow = splitList(v) })
	env("GOBOOKSHELF_METADATA_PROVIDER", func(v string) { cfg.MetadataProvider = v })
	env("GOBOOKSHELF_SECURE_COOKIES", func(v string) {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.SecureCookies, secureSet = b, true
		}
	})
	env("GOBOOKSHELF_METADATA_ALLOW_PRIVATE", func(v string) {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.MetadataAllowPrivate = b
		}
	})
	env("GOBOOKSHELF_SCAN_INTERVAL", func(v string) {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.ScanInterval = d
		}
	})
	env("GOBOOKSHELF_SESSION_TTL", func(v string) {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.SessionTTL = d
		}
	})

	if cfg.GroupsClaim() == "" {
		cfg.OIDC.GroupsClaim = "groups"
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if !secureSet {
		cfg.SecureCookies = strings.HasPrefix(cfg.BaseURL, "https://")
	}
	if cfg.DataDir == "" {
		cfg.DataDir = filepath.Dir(cfg.DBPath)
	}
	if len(cfg.MetricsAllow) == 0 {
		cfg.MetricsAllow = DefaultMetricsAllow
	}

	if err := cfg.parseNets(); err != nil {
		return cfg, err
	}
	if u, err := url.Parse(cfg.BaseURL); err != nil || u.Scheme == "" || u.Host == "" {
		return cfg, fmt.Errorf("base_url %q is not an absolute URL", cfg.BaseURL)
	}
	if cfg.ProxyAuthHeader != "" && len(cfg.trustedNets) == 0 {
		return cfg, fmt.Errorf("proxy_auth_header is set but trusted_proxies is empty; " +
			"the header would be honored from any source address")
	}
	return cfg, nil
}

// GroupsClaim returns the claim inspected for group membership.
func (c Config) GroupsClaim() string { return c.OIDC.GroupsClaim }

// OIDCRedirectURL is the callback URL registered with the identity provider.
func (c Config) OIDCRedirectURL() string { return c.BaseURL + "/api/v1/auth/oidc/callback" }

// CoversDir is where generated cover images are cached.
func (c Config) CoversDir() string { return filepath.Join(c.DataDir, "covers") }

// TrustedProxyNets returns the parsed trusted_proxies CIDRs.
func (c Config) TrustedProxyNets() []*net.IPNet { return c.trustedNets }

// MetricsAllowNets returns the parsed metrics_allow CIDRs.
func (c Config) MetricsAllowNets() []*net.IPNet { return c.metricsNets }

func (c *Config) parseNets() error {
	var err error
	if c.trustedNets, err = parseCIDRs(c.TrustedProxies); err != nil {
		return fmt.Errorf("trusted_proxies: %w", err)
	}
	if c.metricsNets, err = parseCIDRs(c.MetricsAllow); err != nil {
		return fmt.Errorf("metrics_allow: %w", err)
	}
	return nil
}

func parseCIDRs(in []string) ([]*net.IPNet, error) {
	out := make([]*net.IPNet, 0, len(in))
	for _, raw := range in {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		// A bare address is accepted and treated as a single-host network.
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

func splitList(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
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
