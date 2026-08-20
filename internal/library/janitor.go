package library

import (
	"context"
	"time"

	"github.com/rake-pro/go-bookshelf/internal/images"
	"github.com/rake-pro/go-bookshelf/internal/store"
	"github.com/rs/zerolog/log"
)

// Retention windows for items whose files have disappeared.
const (
	// MissingGrace is how long an item stays in the catalog after its files
	// vanish, so an unmounted share does not wipe a library.
	MissingGrace = 7 * 24 * time.Hour
	// ProgressGrace is how much longer a deleted item's reading positions are
	// kept, keyed by the path they came from.
	ProgressGrace = 30 * 24 * time.Hour
)

// Janitor removes items that have been missing past the grace period.
type Janitor struct {
	cat    *Catalog
	covers *images.Store
}

// NewJanitor builds a janitor.
func NewJanitor(cat *Catalog, covers *images.Store) *Janitor {
	return &Janitor{cat: cat, covers: covers}
}

// Run executes one pass, returning the number of items deleted.
func (j *Janitor) Run(ctx context.Context) (int, error) {
	cutoff := store.FormatTime(time.Now().Add(-MissingGrace))

	rows, err := j.cat.db.QueryContext(ctx,
		`SELECT id, library_id, source_key FROM items WHERE missing_at IS NOT NULL AND missing_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	type doomed struct {
		id        int64
		libraryID int64
		sourceKey string
	}
	var list []doomed
	for rows.Next() {
		var d doomed
		if err := rows.Scan(&d.id, &d.libraryID, &d.sourceKey); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	deleted := 0
	for _, d := range list {
		if _, err := j.cat.db.ExecContext(ctx,
			`INSERT INTO progress_archive
				(user_id, library_id, source_key, locator, position_ms, percent, finished_at, device, archived_at)
			 SELECT user_id, ?, ?, locator, position_ms, percent, finished_at, device, ?
			 FROM progress WHERE item_id = ?
			 ON CONFLICT(user_id, library_id, source_key) DO UPDATE SET
				locator = excluded.locator, position_ms = excluded.position_ms,
				percent = excluded.percent, finished_at = excluded.finished_at,
				device = excluded.device, archived_at = excluded.archived_at`,
			d.libraryID, d.sourceKey, store.Now(), d.id); err != nil {
			return deleted, err
		}
		if _, err := j.cat.db.ExecContext(ctx, `DELETE FROM items WHERE id = ?`, d.id); err != nil {
			return deleted, err
		}
		if j.covers != nil {
			j.covers.Remove(d.id)
		}
		deleted++
	}

	if _, err := j.cat.db.ExecContext(ctx,
		`DELETE FROM progress_archive WHERE archived_at < ?`,
		store.FormatTime(time.Now().Add(-ProgressGrace))); err != nil {
		return deleted, err
	}
	if deleted > 0 {
		log.Info().Int("items", deleted).Msg("janitor removed items missing past the grace period")
	}
	return deleted, nil
}

// Start runs the janitor on a ticker until ctx is cancelled.
func (j *Janitor) Start(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = 6 * time.Hour
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		if _, err := j.Run(ctx); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("janitor pass failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// restoreProgress re-attaches archived reading positions to a freshly ingested
// item that came back at the same path.
func (s *Scanner) restoreProgress(ctx context.Context, itemID, libraryID int64, sourceKey string) error {
	if _, err := s.cat.db.ExecContext(ctx,
		`INSERT INTO progress (user_id, item_id, locator, position_ms, percent, finished_at, device, updated_at)
		 SELECT user_id, ?, locator, position_ms, percent, finished_at, device, ?
		 FROM progress_archive WHERE library_id = ? AND source_key = ?
		 ON CONFLICT(user_id, item_id) DO NOTHING`,
		itemID, store.Now(), libraryID, sourceKey); err != nil {
		return err
	}
	_, err := s.cat.db.ExecContext(ctx,
		`DELETE FROM progress_archive WHERE library_id = ? AND source_key = ?`, libraryID, sourceKey)
	return err
}
