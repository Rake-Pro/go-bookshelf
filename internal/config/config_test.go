package config_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rake-pro/go-bookshelf/internal/config"
	"github.com/rake-pro/go-bookshelf/internal/settings"
)

func TestDefaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("GOBOOKSHELF_SECRETS_KEY", testKey())

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Listen != ":8080" {
		t.Errorf("listen = %q", cfg.Listen)
	}
	if cfg.DBDriver != "sqlite" {
		t.Errorf("db_driver = %q, want sqlite", cfg.DBDriver)
	}
	if cfg.DBPath != "/data/go-bookshelf.db" {
		t.Errorf("db_path = %q", cfg.DBPath)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("log_level = %q", cfg.LogLevel)
	}
	// The data directory is optional and off by default: without one the
	// server writes nothing to local disk.
	if cfg.DataDir != "" {
		t.Errorf("data_dir = %q, want empty by default", cfg.DataDir)
	}
	if cfg.CoversDir() != "" {
		t.Errorf("covers dir = %q, want empty when there is no data directory", cfg.CoversDir())
	}
	if len(cfg.SecretsKey) != settings.KeySize {
		t.Errorf("secrets key = %d bytes, want %d", len(cfg.SecretsKey), settings.KeySize)
	}
	if cfg.InsecureKey {
		t.Error("a supplied key must not be reported as the development fallback")
	}
	if cfg.AdminRecovery {
		t.Error("admin recovery must be off unless asked for")
	}
}

func TestEnvOverridesFile(t *testing.T) {
	clearEnv(t)
	t.Setenv("GOBOOKSHELF_SECRETS_KEY", testKey())
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := `
listen: ":9999"
db_path: "/srv/books.db"
data_dir: "/srv/data"
log_level: "debug"
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
	if cfg.DBPath != "/srv/books.db" || cfg.DataDir != "/srv/data" {
		t.Errorf("db/data = %q %q", cfg.DBPath, cfg.DataDir)
	}
	if cfg.CoversDir() != filepath.Join("/srv/data", "covers") {
		t.Errorf("covers dir = %q", cfg.CoversDir())
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("log_level = %q", cfg.LogLevel)
	}
}

// The application configuration moved into the database, so the keys that used
// to live here must no longer be readable from the file: a stale config.yaml
// carrying an OIDC block should be a parse error, not a silently honored one.
func TestRetiredKeysAreRejected(t *testing.T) {
	clearEnv(t)
	t.Setenv("GOBOOKSHELF_SECRETS_KEY", testKey())
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("listen: \":8080\"\nbase_url: \"https://books.example.com\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil {
		t.Fatal("a config file carrying a retired key must be refused")
	}
}

func TestSecretsKeyIsRequired(t *testing.T) {
	clearEnv(t)
	_, err := config.Load("")
	if !errors.Is(err, config.ErrNoSecretsKey) {
		t.Fatalf("load without a key = %v, want ErrNoSecretsKey", err)
	}
	// The message has to tell the operator how to fix it, because it is the
	// only thing they will see.
	if !bytes.Contains([]byte(err.Error()), []byte("openssl rand -base64 32")) {
		t.Errorf("the missing-key error does not say how to generate one: %v", err)
	}
}

func TestSecretsKeyWrongLengthIsFatal(t *testing.T) {
	clearEnv(t)
	t.Setenv("GOBOOKSHELF_SECRETS_KEY", base64.StdEncoding.EncodeToString([]byte("too short")))
	if _, err := config.Load(""); err == nil {
		t.Fatal("a key that does not decode to 32 bytes must be refused")
	}
}

func TestDevInsecureKeyFallback(t *testing.T) {
	clearEnv(t)
	t.Setenv("GOBOOKSHELF_DEV_INSECURE_KEY", "true")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.InsecureKey {
		t.Error("the development fallback must announce itself so main can warn")
	}
	if !bytes.Equal(cfg.SecretsKey, config.DevInsecureKey()) {
		t.Error("the development fallback must derive the same key every time")
	}
}

func TestAdminRecovery(t *testing.T) {
	clearEnv(t)
	t.Setenv("GOBOOKSHELF_SECRETS_KEY", testKey())
	t.Setenv("GOBOOKSHELF_ADMIN_RECOVERY", "true")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.AdminRecovery {
		t.Error("GOBOOKSHELF_ADMIN_RECOVERY=true must turn recovery on")
	}
}

// The two backends are selected by one variable, and the combinations that
// cannot mean anything are refused with a message rather than half-applied.
func TestPostgresDriver(t *testing.T) {
	clearEnv(t)
	t.Setenv("GOBOOKSHELF_SECRETS_KEY", testKey())
	t.Setenv("GOBOOKSHELF_DB_DRIVER", "postgres")
	t.Setenv("GOBOOKSHELF_DB_DSN", "postgres://books:hunter2@db.example.com:5432/books?sslmode=require")

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// The SQLite default must not survive into a Postgres configuration.
	if cfg.DBPath != "" {
		t.Errorf("db_path = %q, want empty on postgres", cfg.DBPath)
	}
	if cfg.DSN() != "postgres://books:hunter2@db.example.com:5432/books?sslmode=require" {
		t.Errorf("DSN() = %q", cfg.DSN())
	}
	if strings.Contains(cfg.SafeDSN(), "hunter2") {
		t.Errorf("SafeDSN leaks the password: %q", cfg.SafeDSN())
	}
}

func TestPostgresRequiresDSN(t *testing.T) {
	clearEnv(t)
	t.Setenv("GOBOOKSHELF_SECRETS_KEY", testKey())
	t.Setenv("GOBOOKSHELF_DB_DRIVER", "postgres")

	_, err := config.Load("")
	if err == nil {
		t.Fatal("postgres without a connection string must be refused")
	}
	if !strings.Contains(err.Error(), "GOBOOKSHELF_DB_DSN") {
		t.Errorf("the error does not name the missing variable: %v", err)
	}
}

func TestPostgresRefusesDBPath(t *testing.T) {
	clearEnv(t)
	t.Setenv("GOBOOKSHELF_SECRETS_KEY", testKey())
	t.Setenv("GOBOOKSHELF_DB_DRIVER", "postgres")
	t.Setenv("GOBOOKSHELF_DB_DSN", "postgres://books@db.example.com:5432/books")
	t.Setenv("GOBOOKSHELF_DB_PATH", "/data/go-bookshelf.db.custom")

	if _, err := config.Load(""); err == nil {
		t.Fatal("a db_path alongside driver=postgres must be refused")
	}
}

func TestSQLiteRefusesEmptyPathAndStrayDSN(t *testing.T) {
	clearEnv(t)
	t.Setenv("GOBOOKSHELF_SECRETS_KEY", testKey())
	t.Setenv("GOBOOKSHELF_DB_PATH", "")
	if _, err := config.Load(""); err == nil {
		t.Fatal("sqlite without a database file must be refused")
	}

	clearEnv(t)
	t.Setenv("GOBOOKSHELF_SECRETS_KEY", testKey())
	t.Setenv("GOBOOKSHELF_DB_DSN", "postgres://books@db.example.com:5432/books")
	if _, err := config.Load(""); err == nil {
		t.Fatal("a db_dsn alongside driver=sqlite must be refused")
	}
}

func TestUnknownDriver(t *testing.T) {
	clearEnv(t)
	t.Setenv("GOBOOKSHELF_SECRETS_KEY", testKey())
	t.Setenv("GOBOOKSHELF_DB_DRIVER", "mysql")
	if _, err := config.Load(""); err == nil {
		t.Fatal("an unknown driver must be refused")
	}
}

func testKey() string {
	return base64.StdEncoding.EncodeToString(make([]byte, settings.KeySize))
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"GOBOOKSHELF_CONFIG", "GOBOOKSHELF_LISTEN", "GOBOOKSHELF_DB_DRIVER", "GOBOOKSHELF_DB_PATH",
		"GOBOOKSHELF_DB_DSN", "GOBOOKSHELF_DATA_DIR", "GOBOOKSHELF_LOG_LEVEL",
		"GOBOOKSHELF_SECRETS_KEY", "GOBOOKSHELF_DEV_INSECURE_KEY", "GOBOOKSHELF_ADMIN_RECOVERY",
	} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
}
