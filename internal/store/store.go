// Package store owns the database: connection setup for either supported
// backend, the dialect abstraction, the embedded migration set, and small
// shared helpers. Query construction lives with the feature packages that need
// it; this package deliberately stays thin.
//
// Two backends are supported. SQLite (pure Go, no cgo) is the default and
// keeps a single-box installation to one file. Postgres is opened through the
// pgx stdlib driver and is what lets the server run with no local state at all,
// so the process can be rescheduled onto any node.
//
// Feature packages write their SQL once, with "?" placeholders, and the DB and
// Tx wrappers below rebind it for the active dialect. The remaining dialect
// differences are kept out of Go entirely: no query in this repository uses
// COLLATE NOCASE, INSERT OR IGNORE, datetime() or LastInsertId, and a lint test
// in this package fails the build if one appears.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx"
	_ "modernc.org/sqlite"             // pure-Go driver, keeps CGO_ENABLED=0 builds static
)

// ErrNotFound is returned by helpers that look up a single row.
var ErrNotFound = errors.New("not found")

// Postgres connection pool bounds. A library server is a low-concurrency
// workload in front of a database that is usually shared, so the pool is kept
// small and connections are recycled often enough that a rolling restart of
// the database is not felt as a wall of dead-connection errors.
const (
	pgMaxOpenConns    = 16
	pgMaxIdleConns    = 4
	pgConnMaxLifetime = 30 * time.Minute
	pgConnMaxIdleTime = 5 * time.Minute
)

// DB is the application database handle. It embeds *sql.DB, so connection-level
// methods (Close, PingContext, Stats) remain available; the query methods below
// shadow the embedded ones so every statement is rebound for the dialect first.
type DB struct {
	*sql.DB
	dialect Dialect
}

// Dialect returns the backend this handle is talking to.
func (db *DB) Dialect() Dialect { return db.dialect }

// Open connects to the database described by driver and target, applies
// backend-appropriate connection settings, and runs any pending migrations.
//
// For "sqlite", target is a filesystem path (":memory:" and "file::memory:"
// DSNs are supported for tests). For "postgres", target is a
// postgres:// connection string.
func Open(ctx context.Context, driver, target string) (*DB, error) {
	switch driver {
	case DriverSQLite:
		return openSQLite(ctx, target)
	case DriverPostgres:
		return openPostgres(ctx, target)
	default:
		return nil, fmt.Errorf("store: unknown database driver %q (want %q or %q)",
			driver, DriverSQLite, DriverPostgres)
	}
}

func openSQLite(ctx context.Context, path string) (*DB, error) {
	if path == "" {
		return nil, errors.New("store: sqlite needs a database path")
	}
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
	return finish(ctx, sqlDB, sqliteDialect{})
}

func openPostgres(ctx context.Context, dsn string) (*DB, error) {
	if dsn == "" {
		return nil, errors.New("store: postgres needs a connection string")
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	sqlDB.SetMaxOpenConns(pgMaxOpenConns)
	sqlDB.SetMaxIdleConns(pgMaxIdleConns)
	sqlDB.SetConnMaxLifetime(pgConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(pgConnMaxIdleTime)

	if err := sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return finish(ctx, sqlDB, postgresDialect{})
}

func finish(ctx context.Context, sqlDB *sql.DB, d Dialect) (*DB, error) {
	db := &DB{DB: sqlDB, dialect: d}
	if err := db.Migrate(ctx); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return db, nil
}

func isMemory(path string) bool {
	return path == ":memory:" || strings.HasPrefix(path, "file::memory:")
}

// RedactDSN removes the password from a connection string so it can be logged.
// Anything it cannot parse is reported as redacted in full rather than echoed,
// because a DSN that does not match the expected shape is exactly the one most
// likely to be carrying a credential somewhere unexpected.
func RedactDSN(dsn string) string {
	if dsn == "" {
		return ""
	}
	scheme, rest, ok := strings.Cut(dsn, "://")
	if !ok {
		return "(redacted)"
	}
	authority, tail, hasTail := strings.Cut(rest, "/")
	userinfo, host, hasUser := strings.Cut(authority, "@")
	if hasUser {
		user, _, hasPassword := strings.Cut(userinfo, ":")
		if hasPassword {
			userinfo = user + ":***"
		}
		authority = userinfo + "@" + host
	}
	out := scheme + "://" + authority
	if hasTail {
		// The path carries the database name; the query string can carry a
		// password too, so it is dropped rather than parsed.
		if db, _, ok := strings.Cut(tail, "?"); ok {
			return out + "/" + db + "?..."
		}
		out += "/" + tail
	}
	return out
}

// Tx wraps *sql.Tx with the same rebinding as DB.
type Tx struct {
	*sql.Tx
	dialect Dialect
}

// BeginTx starts a transaction that rebinds statements for this dialect.
func (db *DB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*Tx, error) {
	tx, err := db.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &Tx{Tx: tx, dialect: db.dialect}, nil
}

// ExecContext rebinds query and executes it.
func (db *DB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return db.DB.ExecContext(ctx, db.dialect.Rebind(query), args...)
}

// QueryContext rebinds query and runs it.
func (db *DB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return db.DB.QueryContext(ctx, db.dialect.Rebind(query), args...)
}

// QueryRowContext rebinds query and runs it.
func (db *DB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return db.DB.QueryRowContext(ctx, db.dialect.Rebind(query), args...)
}

// InsertReturningID runs an INSERT written with "?" placeholders and no
// RETURNING clause, and reports the generated id.
func (db *DB) InsertReturningID(ctx context.Context, query string, args ...any) (int64, error) {
	return insertReturningID(ctx, db.dialect, db.DB, query, args...)
}

// ExecContext rebinds query and executes it.
func (tx *Tx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return tx.Tx.ExecContext(ctx, tx.dialect.Rebind(query), args...)
}

// QueryContext rebinds query and runs it.
func (tx *Tx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return tx.Tx.QueryContext(ctx, tx.dialect.Rebind(query), args...)
}

// QueryRowContext rebinds query and runs it.
func (tx *Tx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return tx.Tx.QueryRowContext(ctx, tx.dialect.Rebind(query), args...)
}

// InsertReturningID mirrors DB.InsertReturningID inside a transaction.
func (tx *Tx) InsertReturningID(ctx context.Context, query string, args ...any) (int64, error) {
	return insertReturningID(ctx, tx.dialect, tx.Tx, query, args...)
}

// execQuerier is the subset of *sql.DB and *sql.Tx that insertReturningID uses.
// Statements handed to it have already been rebound by the caller.
type execQuerier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// insertReturningID hides the one place the two backends genuinely disagree on
// an ordinary write: SQLite hands back the rowid through the driver, while the
// postgres wire protocol has no such channel and the id has to be selected.
func insertReturningID(ctx context.Context, d Dialect, ex execQuerier, query string, args ...any) (int64, error) {
	if d.usesReturning() {
		var id int64
		err := ex.QueryRowContext(ctx, d.Rebind(query+" RETURNING id"), args...).Scan(&id)
		return id, err
	}
	res, err := ex.ExecContext(ctx, d.Rebind(query), args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Querier is satisfied by both *DB and *Tx, so a helper can be handed either a
// connection or an open transaction.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	InsertReturningID(ctx context.Context, query string, args ...any) (int64, error)
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
