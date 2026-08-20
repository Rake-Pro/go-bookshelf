package store

import (
	"context"
	"embed"
	"fmt"
	"sort"

	"github.com/rs/zerolog/log"
)

// Migrations are shipped as one directory per dialect, with matching filenames
// on both sides. Two files per step is more text than one file plus a rewriter,
// but the DDL is where the backends differ most - identity columns, blob types,
// the order tables have to be created in - and a rewriter that had to cover all
// of it would be the least reviewable part of the codebase. A test in this
// package checks that the two directories stay in step and that neither has
// picked up the other's syntax.
//
//go:embed migrations/sqlite/*.sql migrations/postgres/*.sql
var migrationsFS embed.FS

// Migrate applies every embedded migration not yet recorded in
// schema_migrations, in filename order, each inside its own transaction. The
// recorded version is the bare filename, which is identical for both dialects,
// so a database migrated before this package grew a second backend is still
// considered up to date.
func (db *DB) Migrate(ctx context.Context) error {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	dir := db.dialect.migrationsDir()
	names, err := migrationNames(dir)
	if err != nil {
		return err
	}

	for _, name := range names {
		var applied int
		if err := db.QueryRowContext(ctx,
			`SELECT count(*) FROM schema_migrations WHERE version = ?`, name).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if applied > 0 {
			continue
		}

		body, err := migrationsFS.ReadFile(dir + "/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx for %s: %w", name, err)
		}
		// Migration bodies are dialect-specific DDL and must not be rebound:
		// a "?" inside one is part of the schema, not a parameter.
		if _, err := tx.Tx.ExecContext(ctx, string(body)); err != nil {
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
		log.Info().Str("migration", name).Str("dialect", db.dialect.Name()).Msg("applied migration")
	}
	return nil
}

func migrationNames(dir string) ([]string, error) {
	entries, err := migrationsFS.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations %s: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}
