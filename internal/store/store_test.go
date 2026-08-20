package store_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/rake-pro/go-bookshelf/internal/store"
	"github.com/rake-pro/go-bookshelf/internal/storetest"
)

// The tests below are the store's contract, and they are written once. They run
// against SQLite on every machine, and against Postgres as well whenever
// GOBOOKSHELF_TEST_POSTGRES_DSN is set - so the same assertions cover both
// backends rather than each backend having a test of its own that drifts.

func TestMigrateCreatesSchemaAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := storetest.Open(t)

	want := []string{
		"libraries", "library_paths", "items", "files", "chapters", "people",
		"item_people", "series", "item_series", "tags", "item_tags",
		"collections", "collection_items", "users", "user_library_access",
		"user_settings", "progress", "bookmarks", "sessions", "api_tokens",
		"scan_runs", "setup_state", "progress_archive", "settings",
		"cover_images", "schema_migrations",
	}
	for _, table := range want {
		if !tableExists(t, db, table) {
			t.Errorf("table %s missing", table)
		}
	}

	applied := countMigrations(t, db)
	if applied == 0 {
		t.Fatal("no migrations recorded")
	}

	// A second Migrate must be a no-op rather than an error.
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if again := countMigrations(t, db); again != applied {
		t.Errorf("migration count changed on re-run: %d -> %d", applied, again)
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	ctx := context.Background()
	db := storetest.Open(t)

	_, err := db.ExecContext(ctx,
		`INSERT INTO items (library_id, kind, title, source_key, added_at, updated_at)
		 VALUES (999, 'ebook', 'orphan', 'k', ?, ?)`, store.Now(), store.Now())
	if err == nil {
		t.Fatal("expected foreign key violation for unknown library_id")
	}
}

func TestCheckConstraints(t *testing.T) {
	ctx := context.Background()
	db := storetest.Open(t)

	if _, err := db.ExecContext(ctx,
		`INSERT INTO libraries (name, kind, created_at) VALUES ('bad', 'video', ?)`, store.Now()); err == nil {
		t.Fatal("expected CHECK violation for unknown library kind")
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users (username, role, created_at) VALUES ('u', 'superuser', ?)`, store.Now()); err == nil {
		t.Fatal("expected CHECK violation for unknown role")
	}
}

// InsertReturningID is the one write path where the backends genuinely differ,
// so it is exercised on a connection and inside a transaction.
func TestInsertReturningID(t *testing.T) {
	ctx := context.Background()
	db := storetest.Open(t)

	libID, err := db.InsertReturningID(ctx,
		`INSERT INTO libraries (name, kind, created_at) VALUES (?, ?, ?)`, "Shelf", "mixed", store.Now())
	if err != nil {
		t.Fatalf("insert library: %v", err)
	}
	if libID <= 0 {
		t.Fatalf("library id = %d, want a generated id", libID)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	itemID, err := tx.InsertReturningID(ctx,
		`INSERT INTO items (library_id, kind, title, source_key, added_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`, libID, "ebook", "A Book", "/books/a.epub", store.Now(), store.Now())
	if err != nil {
		tx.Rollback()
		t.Fatalf("insert item in tx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if itemID <= 0 {
		t.Fatalf("item id = %d, want a generated id", itemID)
	}

	var title string
	if err := db.QueryRowContext(ctx, `SELECT title FROM items WHERE id = ?`, itemID).Scan(&title); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if title != "A Book" {
		t.Errorf("title = %q, want A Book", title)
	}
}

// The upsert forms used across the app have to mean the same thing to both
// backends: DO NOTHING without a conflict target, and DO UPDATE with one.
func TestUpsertForms(t *testing.T) {
	ctx := context.Background()
	db := storetest.Open(t)

	for i := 0; i < 2; i++ {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO people (name, sort_name) VALUES (?, ?) ON CONFLICT DO NOTHING`,
			"A. Writer", "Writer, A."); err != nil {
			t.Fatalf("insert person %d: %v", i, err)
		}
	}
	var people int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM people`).Scan(&people); err != nil {
		t.Fatalf("count people: %v", err)
	}
	if people != 1 {
		t.Errorf("people rows = %d, want 1", people)
	}

	upsert := `INSERT INTO settings (id, data, updated_at) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`
	if _, err := db.ExecContext(ctx, upsert, `{"v":1}`, store.Now()); err != nil {
		t.Fatalf("first settings write: %v", err)
	}
	if _, err := db.ExecContext(ctx, upsert, `{"v":2}`, store.Now()); err != nil {
		t.Fatalf("second settings write: %v", err)
	}
	var data string
	if err := db.QueryRowContext(ctx, `SELECT data FROM settings WHERE id = 1`).Scan(&data); err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if data != `{"v":2}` {
		t.Errorf("settings data = %q, want the second write", data)
	}
}

// Cover bytes are a BLOB on SQLite and a BYTEA on Postgres; the round-trip has
// to be byte-identical either way, including a NUL and a high byte.
func TestCoverBytesRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := storetest.Open(t)

	libID, err := db.InsertReturningID(ctx,
		`INSERT INTO libraries (name, kind, created_at) VALUES (?, ?, ?)`, "Shelf", "mixed", store.Now())
	if err != nil {
		t.Fatalf("insert library: %v", err)
	}
	itemID, err := db.InsertReturningID(ctx,
		`INSERT INTO items (library_id, kind, title, source_key, added_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`, libID, "ebook", "A Book", "/books/a.epub", store.Now(), store.Now())
	if err != nil {
		t.Fatalf("insert item: %v", err)
	}

	want := []byte{0xff, 0xd8, 0x00, 0x01, 0x7f, 0x80, 0xff}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO cover_images (item_id, variant, content_type, bytes, updated_at)
		 VALUES (?, ?, ?, ?, ?)`, itemID, "thumb", "image/jpeg", want, store.Now()); err != nil {
		t.Fatalf("insert cover: %v", err)
	}

	var got []byte
	var contentType string
	if err := db.QueryRowContext(ctx,
		`SELECT content_type, bytes FROM cover_images WHERE item_id = ? AND variant = ?`,
		itemID, "thumb").Scan(&contentType, &got); err != nil {
		t.Fatalf("read cover: %v", err)
	}
	if contentType != "image/jpeg" {
		t.Errorf("content type = %q", contentType)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("cover bytes = %v, want %v", got, want)
	}

	// Deleting the item takes its artwork with it.
	if _, err := db.ExecContext(ctx, `DELETE FROM items WHERE id = ?`, itemID); err != nil {
		t.Fatalf("delete item: %v", err)
	}
	var left int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM cover_images WHERE item_id = ?`, itemID).Scan(&left); err != nil {
		t.Fatalf("count covers: %v", err)
	}
	if left != 0 {
		t.Errorf("cover rows left after deleting the item = %d, want 0", left)
	}
}

// Placeholders are written once with "?" and rebound per dialect, so a query
// with several of them has to keep its argument order.
func TestPlaceholderOrderSurvivesRebinding(t *testing.T) {
	ctx := context.Background()
	db := storetest.Open(t)

	libID, err := db.InsertReturningID(ctx,
		`INSERT INTO libraries (name, kind, created_at) VALUES (?, ?, ?)`, "Shelf", "mixed", store.Now())
	if err != nil {
		t.Fatalf("insert library: %v", err)
	}
	for _, title := range []string{"Alpha", "Beta", "Gamma"} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO items (library_id, kind, title, sort_title, source_key, added_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			libID, "ebook", title, title, "/books/"+title, store.Now(), store.Now()); err != nil {
			t.Fatalf("insert %s: %v", title, err)
		}
	}

	rows, err := db.QueryContext(ctx,
		`SELECT title FROM items WHERE library_id = ? AND kind = ? AND lower(title) LIKE ? ESCAPE '\'
		 ORDER BY lower(sort_title) LIMIT ? OFFSET ?`,
		libID, "ebook", "%a%", 2, 1)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var title string
		if err := rows.Scan(&title); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, title)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(got) != 2 || got[0] != "Beta" || got[1] != "Gamma" {
		t.Errorf("page = %v, want [Beta Gamma]", got)
	}
}

// Timestamps are RFC3339 UTC strings in TEXT columns on both backends, written
// by the application rather than by a column default. That choice is load
// bearing: the code compares them as strings (`expires_at < ?` prunes sessions,
// `missing_at < ?` drives the janitor) and hands them to clients verbatim, so a
// column that quietly became a timestamptz on one side - reformatting the value
// on the way out, and sorting on a parsed value on the way in - would break both
// without failing a compile.
func TestTimestampRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := storetest.Open(t)

	// A fixed instant, not time.Now(), so the exact expected text is written
	// out here rather than derived by the same function under test.
	instant := time.Date(2026, 3, 7, 4, 5, 6, 0, time.UTC)
	const want = "2026-03-07T04:05:06Z"
	if got := store.FormatTime(instant); got != want {
		t.Fatalf("FormatTime = %q, want %q", got, want)
	}

	libID, err := db.InsertReturningID(ctx,
		`INSERT INTO libraries (name, kind, created_at) VALUES (?, ?, ?)`, "Shelf", "mixed", want)
	if err != nil {
		t.Fatalf("insert library: %v", err)
	}

	var got string
	if err := db.QueryRowContext(ctx,
		`SELECT created_at FROM libraries WHERE id = ?`, libID).Scan(&got); err != nil {
		t.Fatalf("read created_at: %v", err)
	}
	if got != want {
		t.Errorf("created_at = %q, want %q byte for byte", got, want)
	}
	if parsed := store.ParseTime(got); !parsed.Equal(instant) {
		t.Errorf("ParseTime(%q) = %v, want %v", got, parsed, instant)
	}

	// A nullable timestamp: NULL on the way in, "" through coalesce on the way
	// out, which is how every optional time in the schema is read.
	itemID, err := db.InsertReturningID(ctx,
		`INSERT INTO items (library_id, kind, title, source_key, added_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`, libID, "ebook", "A Book", "/books/a.epub", want, want)
	if err != nil {
		t.Fatalf("insert item: %v", err)
	}
	var missing string
	if err := db.QueryRowContext(ctx,
		`SELECT coalesce(missing_at, '') FROM items WHERE id = ?`, itemID).Scan(&missing); err != nil {
		t.Fatalf("read missing_at: %v", err)
	}
	if missing != "" {
		t.Errorf("missing_at = %q, want the empty string for NULL", missing)
	}

	// String comparison has to order these the way the janitor and the session
	// pruner assume, which is only true while they stay fixed-width UTC text.
	for _, ts := range []string{
		"2026-03-07T04:05:05Z", "2026-03-07T04:05:06Z", "2026-03-07T04:05:07Z",
	} {
		if _, err := db.ExecContext(ctx,
			`UPDATE items SET missing_at = ? WHERE id = ?`, ts, itemID); err != nil {
			t.Fatalf("set missing_at %s: %v", ts, err)
		}
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT count(*) FROM items WHERE missing_at IS NOT NULL AND missing_at < ?`, want).
			Scan(&n); err != nil {
			t.Fatalf("compare missing_at: %v", err)
		}
		wantN := 0
		if ts < want {
			wantN = 1
		}
		if n != wantN {
			t.Errorf("missing_at %q < %q matched %d rows, want %d", ts, want, n, wantN)
		}
	}
}

func tableExists(t *testing.T, db *store.DB, name string) bool {
	t.Helper()
	query := `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`
	if db.Dialect().Name() == store.DriverPostgres {
		query = `SELECT count(*) FROM information_schema.tables
			WHERE table_schema = current_schema() AND table_name = ?`
	}
	var n int
	if err := db.QueryRowContext(context.Background(), query, name).Scan(&n); err != nil {
		t.Fatalf("look up table %s: %v", name, err)
	}
	return n == 1
}

func countMigrations(t *testing.T, db *store.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	return n
}
