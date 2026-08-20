// Package store owns the SQLite database: connection setup, the embedded
// migration set, and small shared helpers. Query construction lives with the
// feature packages that need it; this package deliberately stays thin.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/rs/zerolog/log"

	_ "modernc.org/sqlite" // pure-Go driver, keeps CGO_ENABLED=0 builds static
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// ErrNotFound is returned by helpers that look up a single row.
var ErrNotFound = errors.New("not found")

// DB is the application database handle.
type DB struct {
	*sql.DB
}

// Open opens (creating if needed) the SQLite database at path and applies all
// pending migrations. ":memory:" and "file::memory:" DSNs are supported for
// tests.
func Open(ctx context.Context, path string) (*DB, error) {
	dsn := path
	if !isMemory(path) {
		if dir := filepath.Dir(path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("create db directory %s: %w", dir, err)
			}
		}
		dsn = "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	}

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite serialises writers anyway; a small pool avoids "database is
	// locked" churn while still allowing concurrent readers under WAL.
	sqlDB.SetMaxOpenConns(8)
	sqlDB.SetMaxIdleConns(8)
	sqlDB.SetConnMaxLifetime(time.Hour)

	if err := sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	for _, pragma := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
	} {
		if isMemory(path) && pragma == "PRAGMA journal_mode = WAL" {
			continue
		}
		if _, err := sqlDB.ExecContext(ctx, pragma); err != nil {
			sqlDB.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}

	db := &DB{DB: sqlDB}
	if err := db.Migrate(ctx); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return db, nil
}

func isMemory(path string) bool {
	return path == ":memory:" || len(path) >= 13 && path[:13] == "file::memory:"
}

// Migrate applies every embedded migration not yet recorded in
// schema_migrations, in filename order, each inside its own transaction.
func (db *DB) Migrate(ctx context.Context) error {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		var applied int
		if err := db.QueryRowContext(ctx,
			`SELECT count(*) FROM schema_migrations WHERE version = ?`, name).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if applied > 0 {
			continue
		}

		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx for %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			name, Now()); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
		log.Info().Str("migration", name).Msg("applied migration")
	}
	return nil
}

// Now is the timestamp format used for every stored time value.
func Now() string { return time.Now().UTC().Format(time.RFC3339) }

// FormatTime renders t in the stored timestamp format.
func FormatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// ParseTime parses a stored timestamp; the zero time is returned for input
// that does not parse.
func ParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// NullString converts an empty string to SQL NULL.
func NullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
