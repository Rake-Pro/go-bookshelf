// Package config loads the bootstrap configuration: the handful of values that
// must be known before a database connection exists, and that therefore cannot
// live in the database.
//
// Everything else - base URL, session and cookie behaviour, scan interval,
// OIDC, reverse-proxy authentication, metadata provider, metrics allow list -
// is application configuration. It is entered in the setup wizard or on the
// admin settings page and stored in the database; see internal/settings.
//
// Values come from an optional YAML file, overlaid by GOBOOKSHELF_*
// environment variables. The environment wins so an orchestrator's injected
// values stay authoritative over a checked-in file. The secrets key is
// deliberately environment-only: it is the one value that must not be sitting
// in a config file next to the database it decrypts.
package config

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/rake-pro/go-bookshelf/internal/settings"
	"github.com/rake-pro/go-bookshelf/internal/store"
	"gopkg.in/yaml.v3"
)

// devInsecureSeed derives the fixed key used when GOBOOKSHELF_DEV_INSECURE_KEY
// is set. It is a constant on purpose: the whole point is that a developer's
// throwaway database survives a restart without them managing a key. It is
// also why using it in production would leave every stored secret readable by
// anyone holding a copy of this source.
const devInsecureSeed = "go-bookshelf development key - not for production use"

// defaultDBPath is where a SQLite installation keeps its database when nothing
// says otherwise. It is only a default for the SQLite driver: selecting
// Postgres clears it rather than refusing to start over a value nobody set.
const defaultDBPath = "/data/go-bookshelf.db"

// ErrNoSecretsKey is returned when neither a secrets key nor the development
// fallback is set. Its message is the operator-facing instruction.
var ErrNoSecretsKey = errors.New(
	"GOBOOKSHELF_SECRETS_KEY is not set. It encrypts the credentials stored in the " +
		"database, so the server will not start without it.\n" +
		"Generate one with:  openssl rand -base64 32\n" +
		"For local development only, set GOBOOKSHELF_DEV_INSECURE_KEY=true instead")

// Config is the resolved bootstrap configuration.
type Config struct {
	Listen string `yaml:"listen"`

	// DBDriver selects the backend: "sqlite" (default) or "postgres".
	DBDriver string `yaml:"db_driver"`
	// DBPath is the SQLite database file. It is meaningless, and refused, when
	// the driver is Postgres.
	DBPath string `yaml:"db_path"`
	// DBDSN is the Postgres connection string. It may carry a password, so it
	// is never logged except through store.RedactDSN.
	DBDSN string `yaml:"db_dsn"`

	// DataDir is an optional local scratch directory used as a cover cache.
	// Empty means the server writes nothing to local disk, which is what lets
	// a Postgres deployment run with no volume and be rescheduled anywhere.
	DataDir string `yaml:"data_dir"`

	LogLevel string `yaml:"log_level"`

	// SecretsKey is the decoded GOBOOKSHELF_SECRETS_KEY, settings.KeySize
	// bytes, used to encrypt the secret settings values at rest.
	SecretsKey []byte `yaml:"-"`

	// InsecureKey records that SecretsKey was derived from the development
	// fallback rather than supplied. main logs the warning once the logger
	// exists.
	InsecureKey bool `yaml:"-"`

	// AdminRecovery keeps the local password form available even when the
	// stored settings have turned it off. It is deliberately an environment
	// variable and nothing else: turning it on requires a restart, so an
	// attacker who reaches the admin UI cannot re-enable the password path
	// they would otherwise have to get past the identity provider for.
	AdminRecovery bool `yaml:"-"`
}

// Default returns the configuration used when nothing is set.
func Default() Config {
	return Config{
		Listen:   ":8080",
		DBDriver: store.DriverSQLite,
		DBPath:   defaultDBPath,
		LogLevel: "info",
	}
}

// Load reads the optional YAML file at path (or GOBOOKSHELF_CONFIG when path
// is empty), applies GOBOOKSHELF_* overrides, and validates the result.
func Load(path string) (Config, error) {
	cfg := Default()

	if path == "" {
		path = os.Getenv("GOBOOKSHELF_CONFIG")
	}
	if path != "" {
		f, err := os.Open(path)
		if err != nil {
			return cfg, fmt.Errorf("read config %s: %w", path, err)
		}
		dec := yaml.NewDecoder(f)
		// Strict: an unknown key is refused rather than ignored. Most of what
		// used to live in this file moved into the database, and silently
		// dropping a stale base_url or oidc block would leave the operator
		// convinced they had configured something they had not.
		dec.KnownFields(true)
		err = dec.Decode(&cfg)
		f.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			return cfg, fmt.Errorf("parse config %s: %w", path, err)
		}
	}

	// dbPathSet distinguishes "the operator asked for this file" from "nobody
	// said anything, so the SQLite default is still in place". Only the first
	// is worth refusing when the driver is Postgres.
	dbPathSet := cfg.DBPath != "" && cfg.DBPath != defaultDBPath

	env := func(key string, set func(string)) {
		if v, ok := os.LookupEnv(key); ok {
			set(strings.TrimSpace(v))
		}
	}
	env("GOBOOKSHELF_LISTEN", func(v string) { cfg.Listen = v })
	env("GOBOOKSHELF_DB_DRIVER", func(v string) { cfg.DBDriver = strings.ToLower(v) })
	env("GOBOOKSHELF_DB_PATH", func(v string) {
		cfg.DBPath = v
		dbPathSet = v != "" && v != defaultDBPath
	})
	env("GOBOOKSHELF_DB_DSN", func(v string) { cfg.DBDSN = v })
	env("GOBOOKSHELF_DATA_DIR", func(v string) { cfg.DataDir = v })
	env("GOBOOKSHELF_LOG_LEVEL", func(v string) { cfg.LogLevel = v })

	cfg.AdminRecovery = truthy(os.Getenv("GOBOOKSHELF_ADMIN_RECOVERY"))

	if cfg.Listen == "" {
		return cfg, errors.New("listen address is empty")
	}

	switch cfg.DBDriver {
	case store.DriverSQLite:
		if cfg.DBPath == "" {
			return cfg, errors.New(
				"db_driver is sqlite but no database file is set. " +
					"Set GOBOOKSHELF_DB_PATH (or db_path) to a writable path, " +
					"or set GOBOOKSHELF_DB_DRIVER=postgres and supply GOBOOKSHELF_DB_DSN")
		}
		if cfg.DBDSN != "" {
			return cfg, errors.New(
				"db_dsn is set but db_driver is sqlite. " +
					"Set GOBOOKSHELF_DB_DRIVER=postgres to use it, or unset GOBOOKSHELF_DB_DSN")
		}
	case store.DriverPostgres:
		if dbPathSet {
			return cfg, errors.New(
				"db_path is set but db_driver is postgres. " +
					"Postgres keeps everything in the database, including cover images, " +
					"so there is no SQLite file to point at: unset GOBOOKSHELF_DB_PATH")
		}
		cfg.DBPath = ""
		if cfg.DBDSN == "" {
			return cfg, errors.New(
				"db_driver is postgres but no connection string is set. " +
					"Set GOBOOKSHELF_DB_DSN, for example " +
					"postgres://bookshelf:PASSWORD@db.example.com:5432/bookshelf?sslmode=require")
		}
	default:
		return cfg, fmt.Errorf("db_driver is %q, want %q or %q",
			cfg.DBDriver, store.DriverSQLite, store.DriverPostgres)
	}

	key, insecure, err := loadSecretsKey()
	if err != nil {
		return cfg, err
	}
	cfg.SecretsKey, cfg.InsecureKey = key, insecure

	return cfg, nil
}

// DSN returns the connection target for the configured driver: the SQLite file
// path, or the Postgres connection string.
func (c Config) DSN() string {
	if c.DBDriver == store.DriverPostgres {
		return c.DBDSN
	}
	return c.DBPath
}

// SafeDSN is DSN with any password removed, for logs and the admin page.
func (c Config) SafeDSN() string {
	if c.DBDriver == store.DriverPostgres {
		return store.RedactDSN(c.DBDSN)
	}
	return c.DBPath
}

// loadSecretsKey resolves the AES key that protects stored secrets. A wrong
// length is fatal rather than padded: silently accepting it would make every
// secret written under it undecryptable by anyone who later supplies the key
// they meant.
func loadSecretsKey() ([]byte, bool, error) {
	raw := strings.TrimSpace(os.Getenv("GOBOOKSHELF_SECRETS_KEY"))
	if raw != "" {
		key, err := settings.ParseKey(raw)
		if err != nil {
			return nil, false, fmt.Errorf("GOBOOKSHELF_SECRETS_KEY: %w", err)
		}
		return key, false, nil
	}
	if truthy(os.Getenv("GOBOOKSHELF_DEV_INSECURE_KEY")) {
		sum := sha256.Sum256([]byte(devInsecureSeed))
		return sum[:], true, nil
	}
	return nil, false, ErrNoSecretsKey
}

// DevInsecureKey returns the key the development fallback derives. Tests use
// it so they exercise the same path a developer does.
func DevInsecureKey() []byte {
	sum := sha256.Sum256([]byte(devInsecureSeed))
	return sum[:]
}

// EncodedDevInsecureKey is the base64 form of DevInsecureKey, for scripts that
// need to pass a key in the environment.
func EncodedDevInsecureKey() string {
	return base64.StdEncoding.EncodeToString(DevInsecureKey())
}

// CoversDir is the local cover cache, or "" when no data directory is
// configured and covers are served straight from the database.
func (c Config) CoversDir() string {
	if c.DataDir == "" {
		return ""
	}
	return filepath.Join(c.DataDir, "covers")
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
