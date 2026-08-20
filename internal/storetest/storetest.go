// Package storetest opens throwaway databases for tests.
//
// Every suite that needs a database calls Open. By default it gets SQLite in a
// temporary directory, which is what `go test ./...` does on a machine with no
// database server. When GOBOOKSHELF_TEST_POSTGRES_DSN points at a Postgres
// instance, the same call returns Postgres instead, so the entire suite - the
// API contract tests included - runs a second time against the other backend
// without a line of test code knowing which one it is talking to.
//
// Isolation on Postgres is per call: each database gets a schema of its own and
// drops it on cleanup, so tests neither see each other's rows nor have to run
// one at a time.
package storetest

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rake-pro/go-bookshelf/internal/store"
)

// DSNEnv names the environment variable that switches the suite to Postgres.
const DSNEnv = "GOBOOKSHELF_TEST_POSTGRES_DSN"

// schemaSeq keeps generated schema names unique within a run.
var schemaSeq atomic.Int64

// PostgresDSN returns the configured Postgres DSN, or "" when the suite should
// run on SQLite.
func PostgresDSN() string { return strings.TrimSpace(os.Getenv(DSNEnv)) }

// Driver reports which backend Open will use, for tests that need to name it.
func Driver() string {
	if PostgresDSN() != "" {
		return store.DriverPostgres
	}
	return store.DriverSQLite
}

// Open returns a migrated, empty database and closes it when the test ends.
func Open(t *testing.T) *store.DB {
	t.Helper()
	driver, target := Target(t)
	db, err := store.Open(context.Background(), driver, target)
	if err != nil {
		t.Fatalf("open %s database: %v", driver, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// Target returns the driver and connection target for a fresh database,
// arranging cleanup. It exists for the few callers that have to hand the same
// values to something other than store.Open - the config the server is built
// from, for instance.
func Target(t *testing.T) (driver, target string) {
	t.Helper()
	dsn := PostgresDSN()
	if dsn == "" {
		return store.DriverSQLite, filepath.Join(t.TempDir(), "test.db")
	}

	schema := schemaName(t)
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres for schema setup: %v", err)
	}
	defer admin.Close()
	ctx := context.Background()
	// Quoted so a schema derived from a test name is never parsed as SQL. The
	// name itself is already restricted to lowercase letters, digits and
	// underscores by schemaName.
	if _, err := admin.ExecContext(ctx, `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`); err != nil {
		t.Fatalf("reset schema %s: %v", schema, err)
	}
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatalf("create schema %s: %v", schema, err)
	}
	t.Cleanup(func() {
		cleanup, err := sql.Open("pgx", dsn)
		if err != nil {
			return
		}
		defer cleanup.Close()
		_, _ = cleanup.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
	})

	return store.DriverPostgres, withSearchPath(dsn, schema)
}

// withSearchPath appends a search_path to a DSN. pgx forwards settings it does
// not recognise to the server as runtime parameters, so every pooled connection
// lands in the test's own schema.
//
// Both DSN shapes pgx accepts are handled. A URL takes another query parameter;
// the keyword/value form ("host=... dbname=...") takes another keyword, and
// appending a query string to one of those would produce a DSN that parses into
// a database name with a "?" in it rather than an error anyone could read.
func withSearchPath(dsn, schema string) string {
	if !strings.Contains(dsn, "://") {
		return dsn + " search_path=" + schema
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "search_path=" + schema
}

// schemaName derives a unique, syntactically safe schema name from the test.
func schemaName(t *testing.T) string {
	var b strings.Builder
	b.WriteString("gbs_")
	for _, r := range strings.ToLower(t.Name()) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name := b.String()
	// Postgres identifiers are truncated at 63 bytes, which would collapse two
	// long test names into one schema; the counter is appended after trimming
	// so it always survives.
	const room = 48
	if len(name) > room {
		name = name[:room]
	}
	return fmt.Sprintf("%s_%d", name, schemaSeq.Add(1))
}
