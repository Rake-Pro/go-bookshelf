package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMigrateCreatesSchemaAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	want := []string{
		"libraries", "library_paths", "items", "files", "chapters", "people",
		"item_people", "series", "item_series", "tags", "item_tags",
		"collections", "collection_items", "users", "user_library_access",
		"user_settings", "progress", "bookmarks", "sessions", "api_tokens",
		"scan_runs", "setup_state", "progress_archive", "schema_migrations",
	}
	for _, table := range want {
		var n int
		err := db.QueryRowContext(ctx,
			`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n)
		if err != nil {
			t.Fatalf("query %s: %v", table, err)
		}
		if n != 1 {
			t.Errorf("table %s missing", table)
		}
	}

	var applied int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied == 0 {
		t.Fatal("no migrations recorded")
	}

	// A second Migrate must be a no-op rather than an error.
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var applied2 int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&applied2); err != nil {
		t.Fatalf("count migrations again: %v", err)
	}
	if applied != applied2 {
		t.Errorf("migration count changed on re-run: %d -> %d", applied, applied2)
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "fk.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	_, err = db.ExecContext(ctx,
		`INSERT INTO items (library_id, kind, title, source_key, added_at, updated_at)
		 VALUES (999, 'ebook', 'orphan', 'k', ?, ?)`, Now(), Now())
	if err == nil {
		t.Fatal("expected foreign key violation for unknown library_id")
	}
}

func TestCheckConstraints(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "chk.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx,
		`INSERT INTO libraries (name, kind, created_at) VALUES ('bad', 'video', ?)`, Now()); err == nil {
		t.Fatal("expected CHECK violation for unknown library kind")
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users (username, role, created_at) VALUES ('u', 'superuser', ?)`, Now()); err == nil {
		t.Fatal("expected CHECK violation for unknown role")
	}
}
