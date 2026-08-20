package config_test

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/rake-pro/go-bookshelf/internal/config"
)

func TestDefaults(t *testing.T) {
	clearEnv(t)
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Listen != ":8080" {
		t.Errorf("listen = %q", cfg.Listen)
	}
	if cfg.DBPath != "/data/go-bookshelf.db" || cfg.DataDir != "/data" {
		t.Errorf("db/data = %q %q", cfg.DBPath, cfg.DataDir)
	}
	if cfg.BaseURL != "http://localhost:8080" {
		t.Errorf("base_url = %q", cfg.BaseURL)
	}
	if cfg.SecureCookies {
		t.Error("secure cookies should default off for an http base URL")
	}
	if cfg.CoversDir() != filepath.Join("/data", "covers") {
		t.Errorf("covers dir = %q", cfg.CoversDir())
	}
	if len(cfg.MetricsAllowNets()) == 0 {
		t.Error("metrics allow list should default to loopback plus private ranges")
	}
	if cfg.OIDC.Enabled() {
		t.Error("OIDC must be off by default")
	}
}

func TestEnvOverridesFile(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := `
listen: ":9999"
db_path: "/srv/books.db"
data_dir: "/srv/data"
base_url: "https://books.example.com"
log_level: "debug"
oidc:
  issuer: "https://id.example.com"
  client_id: "bookshelf"
  client_secret: "s3cret"
  admin_group: "librarians"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOBOOKSHELF_LISTEN", ":7777")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Listen != ":7777" {
		t.Errorf("listen = %q, want the environment to win", cfg.Listen)
	}
	if cfg.DBPath != "/srv/books.db" {
		t.Errorf("db_path = %q", cfg.DBPath)
	}
	if !cfg.SecureCookies {
		t.Error("an https base URL should turn on secure cookies")
	}
	if !cfg.OIDC.Enabled() {
		t.Error("OIDC should be enabled when issuer, id and secret are all set")
	}
	if cfg.OIDCRedirectURL() != "https://books.example.com/api/v1/auth/oidc/callback" {
		t.Errorf("redirect URL = %q", cfg.OIDCRedirectURL())
	}
	if cfg.GroupsClaim() != "groups" {
		t.Errorf("groups claim = %q", cfg.GroupsClaim())
	}
}

func TestProxyAuthRequiresTrustedProxies(t *testing.T) {
	clearEnv(t)
	t.Setenv("GOBOOKSHELF_PROXY_AUTH_HEADER", "Remote-User")
	if _, err := config.Load(""); err == nil {
		t.Fatal("proxy auth without trusted_proxies must be rejected")
	}

	t.Setenv("GOBOOKSHELF_TRUSTED_PROXIES", "10.0.0.0/8, 192.0.2.5")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	nets := cfg.TrustedProxyNets()
	if len(nets) != 2 {
		t.Fatalf("trusted proxies = %v", nets)
	}
	if !config.IPInNets(net.ParseIP("10.1.2.3"), nets) {
		t.Error("10.1.2.3 should be inside 10.0.0.0/8")
	}
	if !config.IPInNets(net.ParseIP("192.0.2.5"), nets) {
		t.Error("a bare address should be treated as a single-host network")
	}
	if config.IPInNets(net.ParseIP("192.0.2.6"), nets) {
		t.Error("192.0.2.6 must not match the single-host entry")
	}
}

func TestInvalidValuesRejected(t *testing.T) {
	clearEnv(t)
	t.Setenv("GOBOOKSHELF_BASE_URL", "not-a-url")
	if _, err := config.Load(""); err == nil {
		t.Error("a base_url without a scheme must be rejected")
	}

	clearEnv(t)
	t.Setenv("GOBOOKSHELF_METRICS_ALLOW", "not-a-cidr")
	if _, err := config.Load(""); err == nil {
		t.Error("an unparseable CIDR must be rejected")
	}
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"GOBOOKSHELF_CONFIG", "GOBOOKSHELF_LISTEN", "GOBOOKSHELF_DB_PATH", "GOBOOKSHELF_DATA_DIR",
		"GOBOOKSHELF_BASE_URL", "GOBOOKSHELF_LOG_LEVEL", "GOBOOKSHELF_OIDC_ISSUER",
		"GOBOOKSHELF_OIDC_CLIENT_ID", "GOBOOKSHELF_OIDC_CLIENT_SECRET", "GOBOOKSHELF_OIDC_ADMIN_GROUP",
		"GOBOOKSHELF_OIDC_GROUPS_CLAIM", "GOBOOKSHELF_OIDC_SCOPES", "GOBOOKSHELF_PROXY_AUTH_HEADER",
		"GOBOOKSHELF_TRUSTED_PROXIES", "GOBOOKSHELF_METRICS_ALLOW", "GOBOOKSHELF_SECURE_COOKIES",
		"GOBOOKSHELF_METADATA_PROVIDER", "GOBOOKSHELF_METADATA_ALLOW_PRIVATE",
		"GOBOOKSHELF_SCAN_INTERVAL", "GOBOOKSHELF_SESSION_TTL",
	} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
}
