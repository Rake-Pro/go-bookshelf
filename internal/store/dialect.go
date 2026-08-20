package store

import (
	"strconv"
	"strings"
)

// Driver names accepted by Open and by GOBOOKSHELF_DB_DRIVER.
const (
	DriverSQLite   = "sqlite"
	DriverPostgres = "postgres"
)

// Dialect isolates the differences between the two supported backends.
//
// Only three of them survive at this layer: the placeholder style, which
// embedded migration directory to apply, and whether an insert has to ask for
// the generated id with RETURNING. Everything else is kept out of Go by
// writing SQL that both engines accept - `lower(x)` instead of
// `COLLATE NOCASE`, `ON CONFLICT DO NOTHING` instead of `INSERT OR IGNORE` -
// which is checked by the lint test in this package.
type Dialect interface {
	// Name is "sqlite" or "postgres".
	Name() string
	// Rebind translates "?" placeholders into the dialect's style.
	Rebind(query string) string
	// migrationsDir is the embedded directory holding this dialect's schema.
	migrationsDir() string
	// usesReturning reports whether a generated id must be read back with
	// RETURNING rather than from sql.Result.LastInsertId.
	usesReturning() bool
}

type sqliteDialect struct{}

func (sqliteDialect) Name() string           { return DriverSQLite }
func (sqliteDialect) Rebind(q string) string { return q }
func (sqliteDialect) migrationsDir() string  { return "migrations/sqlite" }
func (sqliteDialect) usesReturning() bool    { return false }

type postgresDialect struct{}

func (postgresDialect) Name() string          { return DriverPostgres }
func (postgresDialect) migrationsDir() string { return "migrations/postgres" }
func (postgresDialect) usesReturning() bool   { return true }

// Rebind rewrites "?" placeholders to "$1, $2, ..." in order. Question marks
// inside single-quoted string literals are left alone, so a LIKE pattern or an
// ESCAPE clause containing one is not mistaken for a parameter.
func (postgresDialect) Rebind(q string) string {
	if !strings.ContainsRune(q, '?') {
		return q
	}
	var b strings.Builder
	b.Grow(len(q) + 8)
	n := 0
	inString := false
	for i := 0; i < len(q); i++ {
		c := q[i]
		switch {
		case c == '\'':
			inString = !inString
			b.WriteByte(c)
		case c == '?' && !inString:
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
