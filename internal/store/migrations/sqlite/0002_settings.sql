-- Application configuration, one JSON document in one row.
--
-- Everything that used to be a GOBOOKSHELF_* variable other than the listener,
-- the database path, the data directory, the log level and the secrets key now
-- lives here, entered in the setup wizard or on the admin settings page. Secret
-- fields inside the document are AES-256-GCM encrypted with the key from
-- GOBOOKSHELF_SECRETS_KEY, so a copy of the database is not a copy of the
-- credentials.
--
-- The document is JSON rather than a column per setting so that adding a
-- setting is a struct change in Go and never another migration.
CREATE TABLE settings (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    data       TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
