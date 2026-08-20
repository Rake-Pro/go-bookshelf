-- Cover artwork moves into the database.
--
-- Both rendered variants are stored as rows so a deployment can run with no
-- writable volume at all: the data directory, when one is configured, becomes
-- a cache in front of these rows rather than the only copy. The bytes are
-- always JPEG, re-encoded from whatever the book carried, and bounded by the
-- limits in internal/images.
CREATE TABLE cover_images (
    item_id      BIGINT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    variant      TEXT NOT NULL CHECK (variant IN ('thumb', 'full')),
    content_type TEXT NOT NULL DEFAULT 'image/jpeg',
    bytes        BYTEA NOT NULL,
    updated_at   TEXT NOT NULL,
    PRIMARY KEY (item_id, variant)
);

-- items.cover_path pointed at a file under the data directory. It is replaced
-- by a flag, so listing a page of items still costs one row read and never a
-- lookup into the artwork itself. Existing rows keep advertising a cover; the
-- bytes are written on the next scan that re-ingests the item, and until then
-- the cover endpoint falls back to the cached file the old code left behind.
ALTER TABLE items ADD COLUMN has_cover INTEGER NOT NULL DEFAULT 0;
UPDATE items SET has_cover = 1 WHERE cover_path <> '';
ALTER TABLE items DROP COLUMN cover_path;
