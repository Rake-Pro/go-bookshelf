# Changelog

All notable changes to this project are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project uses
[semantic versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **Adding books from the browser.** An "Add books" button on the library and
  admin pages opens a sheet with two ways in.
  - **Upload.** `POST /api/v1/libraries/{id}/upload` takes a multipart body of
    one or more files and an optional `subdir`. Everything is validated before
    it goes anywhere near the library: a size cap (200 MiB per EPUB, 2 GiB per
    audio file) applied while streaming, an extension allowlist that follows the
    library's kind, a magic-byte check for each format, and finally a parse by
    the same reader the scanner uses. Uploads are staged in a hidden directory
    inside the library root - which the scanner skips - and renamed into place
    only once they pass, so a rejected file leaves the library untouched. The
    name on disk is derived from the book's own metadata
    (`<Author> - <Title>.epub`, or a numbered directory for an audiobook), never
    from the client's filename; several audio files whose album and author tags
    agree become one book. Identical bytes already in the catalog answer 409
    with the existing item.
  - **Import from a URL.** `POST /api/v1/libraries/{id}/import` queues a job
    (`GET /me/imports`, `GET|DELETE /imports/{id}`) that a worker runs one at a
    time through the SSRF-guarded HTTP client. A URL that answers with a book
    file goes straight into the upload validation path. A URL that answers with
    a web page goes to a new web-story importer: metadata from `og:`/schema.org/
    byline, the article body chosen by a readability-style scorer, sanitized to
    an element and attribute allowlist with scripts, styles, forms, navigation,
    footers and ad blocks removed, images re-fetched through the guard and
    embedded, `rel="next"` and "next chapter" links followed on the same host at
    one request a second, and the result built into an EPUB 3 - which is then
    validated like any other upload. Per-site adapters can be added by
    implementing `importer.Site` without touching the pipeline.
  - **A new `can_upload` permission**, per account, with a toggle on the admin
    users page. Administrators always have it, the `restricted` role never does,
    and `GET /auth/me` answers `can_upload` with the role already folded in.
    Migration `0004` adds the column (default 0, so an upgrade grants nobody
    anything) and the `import_jobs` table.
  - Uploads are rate limited per account and limited to one in flight per
    account, so the 2 GiB cap cannot be multiplied by parallel requests.
### Changed
- **Reader layout.** The page now fills the viewport. The top bar and the footer
  float over the text, hide themselves two seconds after the book opens and on
  every page turn, and come back on a tap in the center of the page, on any key,
  or as soon as focus enters them - they stay in the document, `inert` and
  hidden, so keyboard and screen-reader users never lose them. The column
  measure follows the reading size (~38em per column) instead of a fixed value,
  a second column appears only on a landscape viewport at least 1100px wide, and
  the side margins, page gap and running-head band scale with the margin
  setting. A cover, title page or part divider is laid out as one centered page
  rather than stranded in the left half of an empty spread.
- **Reader themes.** Paper, Sepia, Gray, Night, high-contrast light and dark,
  and Custom - each with its own link and selection colors. The theme is applied
  to the reader's own chrome and sheets as well as to the page, so a dark page
  no longer sits in a light frame, and `<meta name="theme-color">` follows it
  while a book is open.
- **Reader defaults** are now a reading size of 1.15 and a line height of 1.6
  (`font_scale` also accepts 0.05 steps, was 0.1), with a book serif fallback
  behind the publisher's own font.
- **Reading settings** are grouped into Text, Theme and Layout with 44px
  controls, theme swatches and a live preview, and open as a side panel from
  900px wide so the page stays visible while it changes. Letter, word and
  paragraph spacing moved into a "Fine tuning" section.
- **Reader footer** shows how many pages are left in the chapter beside
  "Page x of y", falling back to the chapter title when the renderer cannot
  supply a count.
- Swipe left or right on the page turns it, alongside the existing tap zones,
  and the reader now respects the display cutout (safe-area) insets.

### Fixed
- Reader: page turn announcements are throttled instead of narrating every
  relocation, and the tap-zone overlay no longer swallows scrolling in the
  scrolled reading mode.
- Admin: the icon inside a text button no longer stretches to its container's
  width, which had left the Scan button with an oversized glyph. A `.btn` inside
  a link list keeps its own shape instead of being flattened into a list row.
- Uploads: a rejected file's message no longer carries the `upload:` package
  prefix into the browser.
- URL imports of large files are no longer cut off after twenty seconds. The
  buffered client's timeout covers reading the body, so a streamed download now
  gets its own thirty-minute deadline on the context instead.

## [0.2.2] - 2026-08-20

### Fixed
- Service worker: the cache version is stamped with the build version at serve
  time, so installed clients actually receive a new frontend. 0.2.1's fixes had
  shipped behind an unchanged cache key.

## [0.2.1] - 2026-08-20

### Added
- `create_missing` on `POST /api/v1/setup/library` and `POST /api/v1/libraries`
  creates the directory first, so an empty media share no longer blocks setup.

### Fixed
- The reader now renders in a browser. Four client-side causes: the application
  CSP blocked the `blob:` frames, styles and fonts the renderer needs; the
  injected per-chapter CSP `<meta>` tag was not self-closed, making XHTML
  chapters unparseable; the book was opened before the reader element was in
  the document, leaving the renderer's iframe without a browsing context; and
  the mini-player set an attribute in its constructor, which makes
  `document.createElement` throw.
- An OIDC issuer pointing at this application's own base URL is rejected with a
  clear message instead of a confusing discovery 404.

## [0.2.0] - 2026-08-20

### Added

- **Postgres storage backend.** `GOBOOKSHELF_DB_DRIVER=postgres` with
  `GOBOOKSHELF_DB_DSN` runs the whole application on Postgres instead of SQLite;
  SQLite stays the default and is unchanged. The schema is shipped per dialect
  under `internal/store/migrations/{sqlite,postgres}/` and migrates on startup
  either way. The DSN may carry a password, so it is only ever logged - and only
  ever reported on the admin page - with the password replaced.
- **Cover images are stored in the database**, as two bounded JPEGs per book: a
  thumbnail at most 400px on its longest side and a full render at most 1600px.
  `GET /api/v1/items/{id}/cover` serves them with the same caching headers as
  before.
- `GOBOOKSHELF_TEST_POSTGRES_DSN` re-runs the entire test suite - the API and
  frontend contract tests included - against a real Postgres, and CI does so in
  a `postgres:17` job. `scripts/smoke.sh --driver postgres` drives the built
  binary against one with no data directory at all.
- First-run wizard at `/setup`: the one-time token, the administrator account,
  the base URL (prefilled from the request's forwarding headers), single sign-on
  with a **Test** button that runs discovery without saving, and the first
  library with a path the server checks exists. The last two can be skipped.
- Admin settings page at `/admin/settings`, one card per group, each saving on
  its own, with the same validation as the wizard and a live-region status.
- `GET|PUT /api/v1/admin/settings` and `POST /api/v1/admin/settings/oidc/test`.
- Single sign-on group mapping for both roles: an **admin group** grants the
  administrator role, and a **user group**, when set, is the requirement for
  signing in at all - an identity in neither is refused and no account is
  created for it. Roles are re-evaluated on every sign-in, except for
  `restricted` accounts and for an administrator who still has a local password,
  which are never rewritten.
- Password sign-in can be turned off once single sign-on is working, guarded so
  it cannot leave the server with no way in, and overridable with
  `GOBOOKSHELF_ADMIN_RECOVERY`.
- Automatic account creation on single sign-on is now optional.
- `GET /api/v1/auth/status` reports `setup_complete` and `local_login`;
  `GET /api/v1/system/status` reports `local_login`, `settings_updated_at` and
  `base_url`.
- `internal/oidctest`, a minimal OpenID Connect provider used by the tests, so
  sign-in is exercised through real discovery, code exchange and token
  verification rather than against internal helpers.

### Changed

- **`GOBOOKSHELF_DATA_DIR` is now optional and defaults to empty.** When it is
  set it is a write-through cache for cover images and nothing else; when it is
  not, the server writes nothing to local disk. Combined with Postgres this
  means a deployment needs no writable volume and can be rescheduled onto any
  node: the catalog, users, sessions, API tokens, reading positions, bookmarks,
  the settings document, the first-run setup token, scan history and the cover
  artwork are all in the database, and the media mount is read-only.
- `GOBOOKSHELF_DB_PATH` is refused when the driver is `postgres`, and
  `GOBOOKSHELF_DB_DSN` when it is `sqlite`, rather than being silently ignored.
- `GET /api/v1/system/status` reports `db_driver` and a redacted `db_dsn`.
  `db_size_bytes` is zero on Postgres, which has no single file to measure.
- Full-size covers are now rendered up to 1600px rather than 1400px.
- **Breaking: application configuration moved out of the environment and into
  the app.** Single sign-on, the external base URL, cookie and session
  behaviour, the background scan interval, reverse-proxy authentication, the
  online metadata provider and the `/metrics` allow list are now entered in a
  setup wizard on first run, edited afterwards at **Admin -> Settings**, and
  stored in the database. A save applies to the running server; nothing needs a
  restart.
- **Breaking: the environment carries only what must be known before the
  database is open**: `GOBOOKSHELF_SECRETS_KEY` (new, required),
  `GOBOOKSHELF_LISTEN`, `GOBOOKSHELF_DB_DRIVER`, `GOBOOKSHELF_DB_PATH`,
  `GOBOOKSHELF_DB_DSN`, `GOBOOKSHELF_DATA_DIR`, `GOBOOKSHELF_LOG_LEVEL`,
  `GOBOOKSHELF_CONFIG`, `GOBOOKSHELF_ADMIN_RECOVERY` and the development-only
  `GOBOOKSHELF_DEV_INSECURE_KEY`. Every other `GOBOOKSHELF_*` variable, and its
  YAML key, is gone; an unknown key in the config file is now refused rather
  than ignored.
- **Breaking: `POST /api/v1/auth/setup` is replaced by
  `POST /api/v1/setup/{step}`** - `token`, `admin`, `base-url`, `oidc`,
  `library`, `complete`.
- Until first-run setup is complete, every `/api/v1` route other than the wizard
  and the public probes answers `403 setup_required`.

**Upgrading from configuration in the environment.** Set
`GOBOOKSHELF_SECRETS_KEY` to 32 base64 bytes (`openssl rand -base64 32`) and
drop the variables that are gone. A database that
already has accounts starts with setup marked complete, so no wizard appears and
nothing is gated - but the OIDC settings do not migrate: re-enter the issuer,
client id, client secret and group names at **Admin -> Settings**. Until you do,
sign-in is password-only. If single sign-on locks you out, restart with
`GOBOOKSHELF_ADMIN_RECOVERY=true`.

**Upgrading to Postgres, or to no data directory.** Nothing to do for a SQLite
installation: the new migration adds the cover table and converts
`items.cover_path` into a flag, and existing covers keep being served from the
cache under `GOBOOKSHELF_DATA_DIR` until the next scan re-ingests each book and
writes its artwork into the database. If you have already cleared that
directory, rescan the library to regenerate the covers. Moving an existing
installation to Postgres is not migrated for you: point it at an empty database
and rescan.

### Security

- The OIDC client secret is AES-256-GCM encrypted at rest under
  `GOBOOKSHELF_SECRETS_KEY`, with a fresh nonce per write. The API reports only
  `has_client_secret` and never the value; an empty value on write keeps the
  stored one. A missing, wrong-length or wrong key is a hard failure with an
  actionable message rather than a server that looks unconfigured.
- A settings save runs OIDC discovery before persisting anything, so an issuer
  that does not answer is a 400 carrying the provider's error and leaves both
  the stored document and the running configuration untouched.
- `GOBOOKSHELF_ADMIN_RECOVERY` is environment-only by design: re-enabling the
  password path takes a restart, so it is not reachable from a compromised admin
  session.

## [0.1.0] - 2026-08-20

The walking skeleton, end to end: scan a directory of books, catalog them, and
read or listen to them in an installable web app, with per-user accounts,
progress and preferences. One static binary, one SQLite file, media mounted
read-only.

### Added

- First walking skeleton of the server: single binary, SQLite catalog, and the
  frontend embedded from `web/dist`.
- EPUB ingest: `container.xml` and OPF parsing for title, subtitle, creators
  with roles, language, identifiers, publisher, date, description, subjects and
  series, with the cover taken from the OPF cover image. A sidecar
  `metadata.opf` next to the file overrides the embedded metadata.
- Audiobook ingest: ID3v2 (2.2/2.3/2.4) tags and MPEG frame parsing for MP3,
  including CBR and Xing/VBRI duration; MP4 atom parsing for M4B/M4A covering
  `mvhd` duration, `ilst` tags, freeform atoms and `chpl` chapter lists. A
  directory of audio files becomes one item; a sidecar `metadata.json`
  overrides the tags.
- Incremental scanner keyed on size and modification time, with SHA-1 only on
  change; a filesystem watcher with a debounce; and a janitor that retires
  items whose files stayed missing for seven days while parking their reading
  positions for another thirty.
- Accounts: local argon2id passwords, sessions, API tokens with read and write
  scopes, a one-time first-run setup token printed once to the log, OIDC login,
  and a reverse-proxy header mode gated on trusted CIDRs.
- JSON API under `/api/v1` covering auth, libraries, items, media streaming
  with range support, discovery, per-user settings, progress, bookmarks and
  tokens, plus an admin surface and an OPDS 1.2 catalog.
- Cover cache on disk with a pixel and time budget on every decode.
- Installable PWA frontend, embedded in the binary and served from `embed.FS`:
  vanilla ES modules and Web Components with no build step, no bundler and no
  Node toolchain. Home, library, item, authors, series, search, settings and
  admin views, plus login and first-run setup.
- Reader for EPUB at `/read/{id}`, built on a pinned vendored renderer that is
  fed by HTTP from the extracted-container route, so no ZIP reader ships in the
  browser. Font scale and family, line, letter, word and paragraph spacing,
  margins, alignment, columns, paginated or scrolled flow, six themes plus a
  custom colour pair, table of contents, keyboard paging, and reading position
  synced as an EPUB CFI.
- Audiobook player at `/listen/{id}`: one global `<audio>` element that survives
  navigation, multi-file sequencing with absolute positions, chapters, speed,
  skip, a sleep timer, bookmarks, MediaSession integration for lock-screen and
  headset controls, and position pushed to the server on a timer, on pause and
  on unload.
- Service worker and web manifest: cache-first application shell and covers,
  network-only API, an offline page, and a version constant that drops every
  stale cache on activate.
- Accessibility as an acceptance criterion: no pixel root font size so OS text
  scaling applies, an additional interface scale, high-contrast themes,
  `prefers-reduced-motion` and `prefers-contrast` support, 44 px minimum touch
  targets, visible focus, landmarks, live-region announcements, and full
  keyboard operation of the reader and the player.
- `scripts/checkweb`, a dependency-free static check that resolves every import
  specifier and asset reference in `web/dist`, wired into `make all` and CI.
- An end-to-end contract test and an extended `make smoke` that both walk the
  exact sequence the web app performs, from first-run setup through to logout.

### Security

- Security test suite covering every row of the design's security matrix.
- Book content is rendered in an iframe sandboxed to `allow-same-origin` only.
  The vendored renderer's upstream `allow-scripts` is patched out, recorded in
  `web/dist/vendor/VERSIONS.md`; scripts are additionally refused at the loader,
  blocked by an injected `script-src 'none'` meta policy, stripped from the
  document on load, and forbidden by the response CSP.

[Unreleased]: https://github.com/rake-pro/go-bookshelf/compare/v0.2.2...HEAD
[0.2.2]: https://github.com/rake-pro/go-bookshelf/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/rake-pro/go-bookshelf/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/rake-pro/go-bookshelf/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/rake-pro/go-bookshelf/releases/tag/v0.1.0
