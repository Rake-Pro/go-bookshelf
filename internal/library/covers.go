package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/rake-pro/go-bookshelf/internal/images"
	"github.com/rake-pro/go-bookshelf/internal/store"
)

// Cover is one stored cover variant, ready to serve.
type Cover struct {
	Variant     string
	ContentType string
	Bytes       []byte
	UpdatedAt   time.Time
}

// SaveCover re-encodes raw artwork into both bounded JPEG variants and stores
// them against the item, inside one transaction so an item is never left
// advertising a cover that only half exists. The stored variants are returned
// so the caller can also drop them into a local cache.
func (c *Catalog) SaveCover(ctx context.Context, itemID int64, raw []byte) ([]Cover, error) {
	covers := make([]Cover, 0, 2)
	for _, variant := range []string{images.VariantFull, images.VariantThumb} {
		encoded, err := images.Convert(ctx, raw, images.MaxDim(variant))
		if err != nil {
			return nil, fmt.Errorf("cover %s: %w", variant, err)
		}
		covers = append(covers, Cover{
			Variant: variant, ContentType: "image/jpeg", Bytes: encoded, UpdatedAt: time.Now().UTC(),
		})
	}

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := store.Now()
	for _, cov := range covers {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO cover_images (item_id, variant, content_type, bytes, updated_at)
			 VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT(item_id, variant) DO UPDATE SET content_type = excluded.content_type,
				bytes = excluded.bytes, updated_at = excluded.updated_at`,
			itemID, cov.Variant, cov.ContentType, cov.Bytes, now); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE items SET has_cover = 1 WHERE id = ?`, itemID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return covers, nil
}

// Cover returns one stored variant, or store.ErrNotFound when the item has no
// artwork.
func (c *Catalog) Cover(ctx context.Context, itemID int64, variant string) (Cover, error) {
	cov := Cover{Variant: images.Variant(variant)}
	var updatedAt string
	err := c.db.QueryRowContext(ctx,
		`SELECT content_type, bytes, updated_at FROM cover_images WHERE item_id = ? AND variant = ?`,
		itemID, cov.Variant).Scan(&cov.ContentType, &cov.Bytes, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Cover{}, store.ErrNotFound
	}
	if err != nil {
		return Cover{}, err
	}
	cov.UpdatedAt = store.ParseTime(updatedAt)
	return cov, nil
}

// DeleteCovers drops an item's artwork. Deleting the item itself cascades, so
// this is only for clearing a cover in place.
func (c *Catalog) DeleteCovers(ctx context.Context, itemID int64) error {
	if _, err := c.db.ExecContext(ctx, `DELETE FROM cover_images WHERE item_id = ?`, itemID); err != nil {
		return err
	}
	_, err := c.db.ExecContext(ctx, `UPDATE items SET has_cover = 0 WHERE id = ?`, itemID)
	return err
}
