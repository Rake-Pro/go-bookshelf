# go-bookshelf design

Single Go binary over SQLite or Postgres, serving a personal/family ebook and audiobook
library through an installable PWA. Accessibility is an acceptance criterion:
resizable text, adjustable spacing, high-contrast themes, large touch targets,
keyboard-complete, screen-reader-clean markup.

## Principles

- One static binary, one database, media mounted read-only. The database is
  SQLite by default and Postgres when the deployment has more than one node;
  everything the server owns lives in it, so with Postgres there is no local
  state at all and the process can be rescheduled anywhere.
- Minimal dependency tree. Frontend is vanilla JavaScript ES modules + Web
  Components with no build step or Node toolchain, served straight from
  `embed.FS`. The only third-party frontend runtime dependency is the vendored
  EPUB renderer (pinned, with its license, under `web/dist/vendor/`).
- No outbound network calls unless an online metadata provider is explicitly
  enabled. Embedded metadata (OPF, ID3, MP4 atoms) is the source of truth.
- Everything the book supplies is untrusted: metadata rendered as text only,
  EPUB content served from an isolated path into a sandboxed iframe under a
  strict CSP, archives extracted with size/entry/zip-slip guards.
- No default credentials. First run prints a one-time setup token to the log.
- Configuration is application data, not deployment plumbing. The environment
  carries only what must be known before the database is open; everything else
  is entered in the setup wizard or the admin settings page and stored in the
  database, with credentials encrypted at rest.
- Progress, bookmarks and reader/player preferences are stored per user on the
  server and follow the user across devices.

## Layout

```
cmd/go-bookshelf/        main: flags/env, wiring, serve
internal/config/         bootstrap only: listener, database selection, data dir, log level, secrets key
internal/settings/       DB-backed application config, AES-GCM secrets, live reload
internal/store/          the dialect seam (sqlite via modernc, postgres via pgx), embedded migrations
internal/storetest/      throwaway test databases on either backend, imported only by tests
internal/library/        scanner, file watcher, ingest pipeline
internal/epub/           container.xml/OPF parse, cover, spine, safe zip reader
internal/audio/          mp3 (ID3v2) + m4b/m4a (MP4 atoms, chpl/Nero chapters), duration
internal/auth/           local users (argon2id), sessions, OIDC, proxy-header mode
internal/api/            JSON handlers (/api/v1), OPDS (/opds), media streaming
internal/server/         router, middleware (CSP, logging, auth, setup gate), static
internal/oidctest/       a fake OpenID Connect provider, imported only by tests
web/                     embed.go (`//go:embed all:dist`) + dist/ (index.html, app/, vendor/, icons/)
docs/                    this file, API reference, deploy notes
```

## Config

Configuration is split in two, by the only line that matters: whether the value
has to be known before the database is open.

### Bootstrap (environment, or the small YAML file)

| Var (yaml key) | Default | Purpose |
|---|---|---|
| `GOBOOKSHELF_SECRETS_KEY` | **required** | base64, 32 bytes; AES-256 key for the secret settings values |
| `GOBOOKSHELF_LISTEN` (`listen`) | `:8080` | bind address |
| `GOBOOKSHELF_DB_DRIVER` (`db_driver`) | `sqlite` | `sqlite` or `postgres` |
| `GOBOOKSHELF_DB_PATH` (`db_path`) | `/data/go-bookshelf.db` | SQLite file. Refused when the driver is `postgres` |
| `GOBOOKSHELF_DB_DSN` (`db_dsn`) | unset | Postgres connection string. Required when the driver is `postgres`, refused otherwise. May carry a password, so it is only ever logged redacted |
| `GOBOOKSHELF_DATA_DIR` (`data_dir`) | unset | Optional local cover cache. Empty means nothing is written to local disk |
| `GOBOOKSHELF_LOG_LEVEL` (`log_level`) | `info` | zerolog level |
| `GOBOOKSHELF_CONFIG` | unset | path to the YAML file holding the keys above |
| `GOBOOKSHELF_ADMIN_RECOVERY` | `false` | force the password form on regardless of the stored settings |
| `GOBOOKSHELF_DEV_INSECURE_KEY` | `false` | development only: derive a fixed secrets key and log a warning |

A missing or wrong-length secrets key is fatal, with a message naming
`openssl rand -base64 32`. Booting without it would look like an unconfigured
server rather than a broken one, and any secret written under a wrong key is
unrecoverable. YAML decoding is strict, so a file left over from an older
release fails instead of being half-honored. The secrets key has no YAML key: it
must not live in a file beside the database it decrypts.

`GOBOOKSHELF_ADMIN_RECOVERY` is environment-only on purpose. Turning it on takes
a restart, so an attacker who reaches the admin UI cannot re-enable the password
path they would otherwise have to get past the identity provider for.

### Application (database, edited in the app)

One JSON document in `settings`, managed by `internal/settings`:

```json
{"general":  {"base_url":"", "secure_cookies":"auto|on|off",
              "session_ttl":"720h", "scan_interval":"6h"},
 "oidc":     {"enabled":false, "issuer":"", "client_id":"", "client_secret":"",
              "admin_group":"", "user_group":"", "groups_claim":"groups",
              "scopes":"", "auto_register":true, "local_login_enabled":true},
 "proxy_auth":{"enabled":false, "header":"", "trusted_proxies":[]},
 "metadata": {"provider":"none|openlibrary", "allow_private":false},
 "metrics":  {"allow":["cidr"]},
 "setup_complete": false, "updated_at": ""}
```

- **Secrets.** `oidc.client_secret` is AES-256-GCM encrypted inside the stored
  JSON, with a fresh nonce per write, under `GOBOOKSHELF_SECRETS_KEY`. It is
  decrypted once on load, kept in memory, and never returned by the API: reads
  answer `has_client_secret`, and a write that sends an empty value keeps what
  is stored. A decryption failure is an error, not an empty value, because
  "someone changed the key" and "it was never set" need different answers.
- **A save is validated, prepared, persisted, applied, in that order.**
  Preparing is where anything that can fail against the outside world happens -
  OIDC discovery above all - so an unreachable issuer is a 400 with the provider
  error and nothing is stored or changed. Applying swaps a whole live snapshot
  into the auth manager, so a request sees either the old configuration or the
  new one, never a mixture. No restart.
- **The issuer is stored verbatim** apart from surrounding whitespace. A token's
  `iss` claim and the discovery document's own issuer field are compared against
  it byte for byte, and some providers publish a trailing slash.
- **Validation** rejects a base URL that is not absolute http(s), a session
  lifetime under a minute, a scan interval that is negative or under a minute
  (0 means "watcher only"), OIDC on without an issuer, client id and secret,
  proxy authentication on without a header or without trusted CIDRs, an unknown
  metadata provider, and any CIDR that does not parse. It also refuses to turn
  the password form off while OIDC is off, because that would leave no way in.
- **Setup gate.** While `setup_complete` is false the auth middleware answers
  every `/api/v1` route except `/auth/*`, `/setup/*`, `/healthz`, `/readyz` and
  `/admin/settings/oidc/test` with `403 setup_required`. The 401 check runs
  first, so an anonymous caller still learns only that it must authenticate. A
  database that already has accounts is seeded with `setup_complete: true`, so
  an upgrade is not sent back through the wizard.

Libraries (name, kind, paths) are created in the admin UI and stored in their
own tables, not in this document.

## Data model

The schema is written twice, once per backend, under
`internal/store/migrations/{sqlite,postgres}/`, with matching filenames. The
alternative - one file plus a rewriter - was rejected because the DDL is exactly
where the two engines differ most (identity columns, blob types, the order
tables have to be created in), and a rewriter covering all of it would be the
least reviewable code in the repository. What keeps the two sides honest is a
test rather than a convention: `internal/store` checks that the directories hold
the same steps and that neither has picked up the other's syntax.

Everything below is dialect-neutral prose; the concrete types differ (`INTEGER
PRIMARY KEY` against `BIGINT GENERATED BY DEFAULT AS IDENTITY`, `REAL` against
`DOUBLE PRECISION`, `BLOB` against `BYTEA`). Timestamps are RFC3339 UTC strings
written by the application on both, so the two schemas cannot drift on time
formatting, and no column is a boolean - flags are 0/1 integers - so nothing
depends on how a driver scans one.

```sql
libraries(id, name, kind CHECK(kind IN ('ebook','audiobook','mixed')), created_at)
library_paths(library_id, path, PRIMARY KEY(library_id, path))
items(id, library_id, kind CHECK(kind IN ('ebook','audiobook')), title, sort_title,
      subtitle, description, language, published, isbn, asin, publisher,
      has_cover, duration_ms, size_bytes, added_at, updated_at, missing_at)
files(id, item_id, path UNIQUE, size, mtime, sha1, format, duration_ms, seq)
chapters(file_id, seq, title, start_ms, end_ms, PRIMARY KEY(file_id, seq))
people(id, name UNIQUE, sort_name)
item_people(item_id, person_id, role CHECK(role IN ('author','narrator','translator')), seq)
series(id, name UNIQUE)
item_series(item_id, series_id, sequence REAL)
tags(id, name UNIQUE)            item_tags(item_id, tag_id)
collections(id, user_id NULL, name)   collection_items(collection_id, item_id, seq)
users(id, username UNIQUE, display_name, password_hash NULL, oidc_subject NULL,
      role CHECK(role IN ('admin','user','restricted')), created_at, disabled_at,
      can_upload INTEGER NOT NULL DEFAULT 0)
user_library_access(user_id, library_id)
user_settings(user_id PRIMARY KEY, reader_json, player_json, ui_json, updated_at)
progress(user_id, item_id, locator, position_ms, percent REAL, finished_at NULL,
         device, updated_at, PRIMARY KEY(user_id, item_id))
bookmarks(id, user_id, item_id, locator, position_ms, note, created_at)
settings(id CHECK(id = 1), data TEXT, updated_at)   -- one JSON document; secrets encrypted
sessions(id, user_id, created_at, expires_at, user_agent, ip)
api_tokens(id, user_id, name, token_hash, scopes, created_at, last_used_at)
scan_runs(id, library_id, started_at, finished_at, added, updated, removed, errors)
setup_state(id CHECK(id = 1), token_hash, created_at, used_at)  -- one-time first-run token
progress_archive(user_id, library_id, source_key, locator, position_ms, percent,
                 finished_at NULL, device, archived_at, PRIMARY KEY(user_id, library_id, source_key))
cover_images(item_id, variant CHECK(variant IN ('thumb','full')), content_type,
             bytes, updated_at, PRIMARY KEY(item_id, variant))
import_jobs(id, user_id, library_id, url,
            status CHECK(status IN ('queued','running','done','failed')),
            message, item_id NULL, created_at, updated_at)
```

`users.can_upload` is a per-account flag rather than a role, because "may add
books" and "may administer the server" are different powers. An administrator
always may and the `restricted` role never may, so the column decides only the
case in between; it defaults to 0, so an upgrade grants nobody anything.

`import_jobs` is the queue behind URL imports. The row is the only thing the
client and the worker share: the client polls it, the worker advances it, and a
cancel deletes it - which the worker notices between steps and stops. There is
deliberately no `cancelled` status, so there is exactly one place the answer to
"is this job still wanted" lives.

Cover artwork is rows, not files. The scan that ingests a book re-encodes
whatever artwork it carried into two bounded JPEGs - a thumbnail at most 400px
on its longest side, a full render at most 1600px - and writes both, plus the
`items.has_cover` flag, in one transaction, so an item never advertises a cover
that only half exists. `GET /items/{id}/cover` reads them back. When
`GOBOOKSHELF_DATA_DIR` is set the same bytes are mirrored into a write-through
file cache under it and served from there on the next request; when it is not,
they are served straight from the database and the process touches no local
disk. The flag is a column rather than an `EXISTS` subquery so that listing a
page of items still costs one row read and never a lookup into the artwork.

### The dialect seam

Feature packages write SQL once, with `?` placeholders, and `store.DB`/`store.Tx`
rebind it (`?` to `$n`) before execution. Only three differences survive that
far, and each is handled in one place:

| Difference | Where it is handled |
|---|---|
| Placeholder style | `Dialect.Rebind`, applied by the `DB`/`Tx` wrappers |
| Generated ids (`LastInsertId` has no Postgres equivalent) | `InsertReturningID`, which appends `RETURNING id` on Postgres |
| Schema DDL: identity columns, `BLOB`/`BYTEA`, `REAL`/`DOUBLE PRECISION`, foreign keys needing their target to exist already | the per-dialect migration files |

Everything else is avoided rather than translated, because a construct that is
never written cannot be mistranslated:

| Avoided | Written instead |
|---|---|
| `COLLATE NOCASE` (SQLite-only) | `lower(x)`, in `ORDER BY` and in the username lookup |
| `LIKE` case-folding (insensitive on SQLite, sensitive on Postgres) | `lower(column) LIKE ?` with a lowered pattern |
| `INSERT OR IGNORE` (SQLite-only) | `ON CONFLICT DO NOTHING`, which both accept |
| `datetime('now')` (SQLite-only) | `store.Now()`, so both backends store the same format |
| Untyped parameters in an `INSERT ... SELECT` projection, which Postgres cannot infer | explicit `CAST(? AS BIGINT/TEXT)`, which both accept |
| Booleans | 0/1 integer columns |

A test in `internal/store` walks the Go sources and fails on `COLLATE NOCASE`,
`INSERT OR`, `datetime('now')`, `LastInsertId`, `PRAGMA` and `sqlite_master`
reappearing outside this package, so those conventions are enforced rather than
remembered. The rest - the folded `LIKE`, the casts, the absent booleans - are
carried by the round-trip tests, which run against both backends.

Connection handling differs by backend: SQLite gets WAL, `foreign_keys=ON` and a
small pool; Postgres gets a bounded pool (16 open, 4 idle) with a 30-minute
connection lifetime, so a rolling restart of the database is not felt as a wall
of dead-connection errors.

### Testing both backends

`internal/storetest` hands every suite a throwaway database: SQLite by default,
Postgres when `GOBOOKSHELF_TEST_POSTGRES_DSN` is set, each test getting a schema
of its own. Nothing in the tests knows which one it got, so setting that one
variable re-runs the entire suite - the API and frontend contract tests included
- against the other backend. CI does exactly that in a second job with a
`postgres:17` service, and `scripts/smoke.sh --driver postgres` drives the real
binary against it with no data directory at all.

`locator` is an EPUB CFI string for ebooks; `position_ms` is the absolute
position across the concatenated file sequence for audiobooks.

## Ingest rules

- Ebook: one `.epub` file = one item. Metadata from OPF (title, creators with
  role, language, identifiers, publisher, date, description, series via
  `calibre:series`/`belongs-to-collection`), cover from OPF cover-image or first
  image in spine. Sidecar `metadata.opf` next to the file wins over embedded.
- Audiobook: a directory containing one or more `.m4b`/`.m4a`/`.mp3` = one item;
  files ordered by disc/track tags then natural filename sort. A single
  `.m4b` at library root is also one item. Chapters from MP4 `chpl`/Nero or
  embedded per-file chapter lists; fallback: one chapter per file. Metadata
  from tags; sidecar `metadata.json` wins. Cover from embedded art or
  `cover.jpg|png` in the directory.
- Rescan is incremental (size+mtime, sha1 only on change). Files that vanish
  mark the item `missing_at`; items missing for 7 days are hard-deleted by a
  janitor (progress retained 30 more days).
- Hard limits: zip entries <= 10,000, single entry <= 256 MiB, total
  uncompressed <= 2 GiB, no absolute paths, no `..`, no symlinks followed,
  images resized with a 100 MP pixel cap and 10 s timeout.

## Adding books

`internal/upload` is the only code in the server that writes into a library
directory, and both ways in - an upload from the browser and a URL import - end
there. Nothing reaches a library until it has been parsed by the same reader
the scanner uses.

The order of operations is the design:

1. **Stage.** The bytes are streamed into `<library root>/.gbs-incoming`, a
   dot-directory the scanner's walk skips, so a half-written or rejected file is
   never a candidate for ingest. The write is capped as it goes: 200 MiB for an
   EPUB, 2 GiB for an audio file. Nothing is buffered in memory.
2. **Check the extension** against what the library's kind accepts - `ebook`
   takes `.epub`, `audiobook` takes `.m4b`, `.m4a`, `.mp3`, `mixed` takes all
   four. `.mp4` is deliberately absent: the scanner reads it, but accepting an
   upload of it invites a video file.
3. **Check the magic bytes**, which is where an extension stops being evidence.
   EPUB: the zip local header, then a `mimetype` entry holding exactly
   `application/epub+zip`. The specification also requires that entry to be
   first and stored uncompressed; plenty of real books break that while being
   otherwise valid, so a deviation is accepted and logged rather than refused.
   MP4: an `ftyp` box at offset 4 with brand `M4A `, `M4B `, `mp42` or `isom`.
   MP3: an ID3v2 tag, or an MPEG frame header - all four reserved bit patterns
   checked - within the first 64 KiB.
4. **Parse** with `epub.Open` or `audio.Probe`, which applies the archive limits
   above. An upload that is accepted here is one the scanner can ingest.
5. **Deduplicate** on the SHA-1 of the bytes against `files.sha1`, server-wide.
   The same book saved twice under two names is one book; the answer is 409
   naming the existing item.
6. **Name it.** The client's filename never reaches the filesystem: it is read
   for its extension and kept only as a last-resort label. An ebook becomes
   `<Author> - <Title>.epub`; audio files become
   `<Author> - <Title>/<NN> - <chapter>.ext`, with several files of one book
   grouped into one directory when their album and author tags agree - which is
   what makes them one item rather than several. Names are ASCII-folded,
   stripped of everything a filesystem reserves, length-capped and suffixed
   ` (2)`, ` (3)` on a collision. An optional `subdir` is one plain name, never
   a path.
7. **Rename into place**, after an `fsync`, from the staging directory in the
   same library root - so the rename is atomic even on NFS - and flush the
   directory entry. Then an incremental scan of that library runs, and the ids
   of the items it produced are returned.

Uploads are rate limited per account (30 of burst, then one every two seconds)
and one upload at a time per account, so a 2 GiB cap cannot be multiplied by
however many requests a browser will open.

### URL imports

A URL is fetched through `internal/remote`, so the SSRF guard applies to it and
to everything it leads to. What it turns out to be is decided by the bytes, not
by the URL's extension or the server's `Content-Type`:

- **A book file** is streamed straight into the validation path above.
- **An HTML page** goes to the web-story importer: title and author from
  `og:title`, schema.org `Book`/`Article` JSON-LD, `<title>` or a byline; the
  body chosen by a readability-style scorer that weights paragraph text and
  class names and discounts link density; then sanitized to an allowlist of
  elements and attributes - no scripts, styles, iframes, forms, navigation,
  footers or anything whose class marks it as an ad, a share bar or a comment
  thread. Links are unwrapped to their text. Images are re-fetched through the
  guard, checked by magic bytes, capped and embedded. `rel="next"` links, and
  anchors that say "next" or name the following chapter, are followed on the
  same host only, at one request a second, up to 2,000 chapters. The result is
  built into an EPUB 3 - OPF, nav document, one XHTML file per chapter - and
  handed to the upload validation path, because building it here is no reason
  to trust it.

Per-site adapters implement `importer.Site` (`Match(url) bool`,
`Book(ctx, url) (*Book, error)`) and register a factory that receives the
guarded fetcher. The generic extractor is the fallback, so an adapter can be
added without touching the pipeline.

## HTTP API (`/api/v1`)

All JSON. Auth via session cookie (`gbs_session`, HttpOnly, SameSite=Lax) or
`Authorization: Bearer <api token>`. Errors: `{"error":{"code":"...","message":"..."}}`.
List endpoints accept `?limit=&offset=&sort=&q=` and return
`{"items":[...],"total":n}`.

Auth
- `GET /auth/status` (public) -> `{setup_required, setup_complete, oidc_enabled,
  oidc_start_url, local_login, version}`
- `POST /auth/login` `{username, password}` -> sets cookie, returns user.
  403 when the password form is off and `GOBOOKSHELF_ADMIN_RECOVERY` is not set
- `POST /auth/logout`
- `GET /auth/oidc/start` -> 302; `GET /auth/oidc/callback`
- `GET /auth/me` -> `{id, username, display_name, role, libraries:[ids], auth_method}`

`/auth/me` is a pure "who am I": with no credential it answers 401 carrying
nothing but the error envelope. Everything the signed-out login page needs -
whether to offer SSO, whether to show the password form, whether to send the
operator to `/setup` - comes from `/auth/status`, which is deliberately public
and reveals only which sign-in methods exist.

First-run wizard (`POST /api/v1/setup/{step}`)

One route per step rather than one endpoint with a step field, so a half-
finished wizard is resumable from the steps' own status codes. Every step
answers 409 once setup is complete.

- `token` `{token}` -> `{ok, suggested_base_url}`. Checks the one-time token
  without spending it, and suggests a base URL from `X-Forwarded-Proto` /
  `X-Forwarded-Host`, falling back to `Host`. Public, rate limited.
- `admin` `{token, username, password, display_name}` -> the user object, and
  sets the session cookie. Spends the token. Public, rate limited.
- `base-url` `{base_url}` -> `{base_url, redirect_url}`. Admin.
- `oidc` `{skip}` or an OIDC document -> `{oidc_enabled}`. Admin.
- `library` `{skip}` or `{name, kind, path}` -> the library. The path must
  already exist and be a directory. Admin.
- `complete` -> `{setup_complete: true}`. Admin. Opens the rest of the API.

Settings (admin)

- `GET|PUT /admin/settings` -> the document above, with `oidc.client_secret`
  replaced by `has_client_secret`, plus `oidc.redirect_url` (derived),
  `oidc.active` (discovery actually succeeded, as opposed to merely configured)
  and `admin_recovery` (the environment flag, so the page can explain why the
  password form is still on offer). PUT takes any subset: every section is
  optional and an absent field keeps its stored value. An empty
  `client_secret` keeps the stored one.
- `POST /admin/settings/oidc/test` -> `{ok, issuer, redirect_url, groups_claim,
  admin_group, user_group}` or `{ok:false, error}`. Runs discovery against a
  candidate document and stores nothing. Answers 200 either way: the verdict is
  the payload, not the status. Discovery proves the issuer answers and says
  nothing about what a token will carry, which is why the group mapping is
  echoed back for the operator to check against their provider.

### OIDC group mapping

Both group names are optional and both are matched against the claim named by
`groups_claim` (default `groups`); the `groups` scope is added to the request
automatically when either is set.

| Admin group | User group | Outcome |
|---|---|---|
| member | any | role `admin` |
| not a member | empty | role `user` - any authenticated identity is admitted |
| not a member | member | role `user` |
| not a member | not a member | refused, `ErrNotAuthorized`, no account created |

The role is re-evaluated on every sign-in, so a directory change promotes or
demotes. Two exceptions: an account with the `restricted` role is never
rewritten, and an administrator who still has a local password is never demoted
- that is the break-glass account, and losing it when the directory is what
broke is the failure this whole feature has to survive.

Libraries (admin for writes)
- `GET /libraries`; `POST /libraries` `{name, kind, paths:[]}`; `PATCH /libraries/{id}`; `DELETE /libraries/{id}`
- `POST /libraries/{id}/scan` -> `{scan_id}`; `GET /libraries/{id}/scans`

Adding books (needs `can_upload`; admins always have it, `restricted` never
does, and the library must be one the caller can see or the answer is 404)
- `POST /libraries/{id}/upload` `multipart/form-data`: one or more `files`
  parts and an optional `subdir` field, which must precede them because the
  request is streamed (or be given as `?subdir=`). Answers 201
  `{status:"complete", files:[{filename, kind, title, author, size_bytes,
  item_id}]}`, or 202 with `status:"scanning"` when the scan that follows is
  still running and the ids are not ready. 409 carries `item_id` and `title`
  alongside the error envelope; 413 with code `too_large` is the size cap.
- `POST /libraries/{id}/import` `{url}` -> 202 with the job. Refuses a
  non-http(s) scheme or a literal private address immediately, so nothing is
  queued that could never run.
- `GET /me/imports` -> the caller's jobs, newest first; an administrator sees
  every account's, which is what makes the queue diagnosable.
- `GET /imports/{id}`, `DELETE /imports/{id}` (cancel a queued or running job,
  or clear a finished one). Somebody else's job answers 404, not 403.

Items
- `GET /items?library=&kind=&author=&series=&tag=&q=&sort=added|title|author|recent`
- `GET /items/{id}` -> item + people + series + tags + files + chapters + progress.
  `chapters` is every file's chapters flattened in play order; each entry names
  its `file_id` and its `start_ms`/`end_ms` are relative to that file, so a
  client adds the durations of the preceding `files[]` (ordered by `seq`) to
  get an absolute position. `files[].duration_ms` is always populated.
- `PATCH /items/{id}` (admin) metadata edits
- `GET /items/{id}/cover?size=thumb|full` (image, long cache, ETag)
- `GET /items/{id}/epub` reading manifest:
  `{item_id, title, language, resource_url, container_url, cover_url?, spine:[{href,url,size}], progress}`.
  `spine[].size` is the uncompressed byte size, which the reader uses to weight
  reading progress without probing.
- `GET /items/{id}/epub/{path...}` one entry of the container, addressed
  **relative to the container root** (`META-INF/container.xml`,
  `OEBPS/chapter1.xhtml`) - the same address space the EPUB's own
  `container.xml`, OPF and navigation document use, so a renderer can follow
  them directly. Answers `HEAD` with `Content-Length`. Sandboxed; see below.
  No table of contents is published: a client parses the book's nav/NCX through
  this route, as the bundled reader does.
- `GET /items/{id}/files/{file_id}/stream` audio with Range support
- `GET /items/{id}/download` original file(s); zip for multi-file audiobooks

Discovery
- `GET /home` -> `{continue:[Item], recent:[Item], series_in_progress:[{series, finished, total, next_item}]}`.
  `series_in_progress` entries are series standings, not items.
- `GET /authors` -> `{items:[{id,name,sort_name,item_count}],total}`
- `GET /authors/{id}` -> `{author:{...}, items:[Item], total}`
- `GET /series` -> `{items:[{id,name,item_count}],total}`; `GET /series/{id}` -> `{series:{...}, items:[Item], total}`
- `GET /tags` -> `{items:[{id,name,item_count}],total}`
- `GET /search?q=` -> `{query, items:{items,total}, authors:{items,total}, series:{items,total}}`,
  one list envelope per group

User state
- `GET|PUT /me/settings` -> `{reader:{...}, player:{...}, ui:{...}}`. A PUT is a
  partial document: groups and keys that are absent keep their stored values,
  and every value is clamped to its documented range on the way in.
- `GET /me/progress?since=` ; `PUT /me/progress/{item_id}` `{locator?, position_ms?, percent, finished?, device}`
- `GET|POST /me/bookmarks?item=` ; `DELETE /me/bookmarks/{id}`
- `GET|POST /me/tokens` ; `DELETE /me/tokens/{id}`
- `GET /me/imports` -> the import queue; see "Adding books" above

Admin
- `GET|POST /users`, `PATCH /users/{id}`, `DELETE /users/{id}`, `PUT /users/{id}/libraries`.
  Both `POST` and `PATCH` take `can_upload`; `GET /auth/me` answers `can_upload`
  with the role already folded in, so the frontend never reimplements the rule.
- `GET /system/status` -> `{version, go_version, db_path, db_size_bytes, data_dir,
  counts:{ebooks,audiobooks}, libraries, users, last_scans, oidc_enabled,
  local_login, settings_updated_at, base_url, time}`

Other
- `GET /healthz`, `GET /readyz`, `GET /metrics` (Prometheus; limited to the CIDRs in the stored `metrics.allow`, default loopback + RFC1918)
- `GET /opds` OPDS 1.2 root, `/opds/{library}`, `/opds/search?q=`; Basic auth with api token
- `GET /manifest.webmanifest`, `/sw.js`, `/assets/*`

### EPUB isolation

EPUB resources are served under `/api/v1/items/{id}/epub/` with
`Content-Security-Policy: default-src 'none'; script-src 'none'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; font-src 'self' data:; media-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'self'; sandbox`
and `X-Content-Type-Options: nosniff`. The reader loads them inside an iframe
with `sandbox="allow-same-origin"` only (no scripts); the vendored renderer is
patched to drop upstream's `allow-scripts`, which is recorded in
`web/dist/vendor/VERSIONS.md`. Reader settings are applied by injecting a
stylesheet from the parent, not by rewriting the book.

## Reader settings (`user_settings.reader_json`)

```json
{"font_scale":1.15,"font_family":"publisher|system|serif|sans|dyslexic",
 "line_height":1.6,"letter_spacing":0,"word_spacing":0,"paragraph_spacing":0,
 "margin":"narrow|normal|wide","align":"publisher|left|justify",
 "theme":"light|sepia|gray|dark|hc-dark|hc-light|custom","custom_fg":"#1f1d1a","custom_bg":"#faf8f4",
 "layout":"paginated|scrolled","columns":"auto|1|2"}
```
`font_scale` range 0.7-2.5 in 0.05 steps. App chrome uses `rem` everywhere and
never sets a px root font size so OS text scaling applies.

## Player settings (`player_json`)

```json
{"speed":1.0,"skip_back_s":15,"skip_fwd_s":30,"sleep_timer_min":null,
 "sleep_end_of_chapter":false,"volume_boost":false}
```
Speed 0.5-3.0 in 0.05 steps. MediaSession API wires lock-screen, headset and
car controls. Position is pushed to the server every 15 s while playing, on
pause, on chapter change, and on `visibilitychange`.

## UI settings (`ui_json`)

```json
{"theme":"auto|light|dark|hc-light|hc-dark","text_scale":1.0}
```
`text_scale` is 1.0-1.6 in 0.05 steps and is applied by the frontend as a
percentage of the browser's own font size, never as a pixel value, so OS text
scaling still compounds on top of it.

## PWA

- `manifest.webmanifest`: standalone, any-maskable icons (SVG + 192/512 PNG),
  theme color follows user theme.
- Service worker: cache-first for `/assets/*` and covers; network-first for
  `/api/*`; app shell offline page. Offline downloads (whole book/audiobook
  into Cache Storage) arrive in v0.3.
- Routes: `/` home, `/library/{id}`, `/item/{id}`, `/read/{id}`, `/listen/{id}`,
  `/authors`, `/series`, `/search`, `/settings`, `/admin/*`, `/login`, `/setup`.

## Accessibility acceptance (CI gate)

- axe-core run against `/`, `/item/{id}`, `/read/{id}`, `/listen/{id}`,
  `/settings`, `/login` in headless Chromium: zero serious/critical.
- All interactive elements >= 44x44 CSS px; focus visible; skip link present.
- Player state announced via `aria-live="polite"`; reader page changes update
  an offscreen live region with "Page x of y".
- Respects `prefers-reduced-motion`, `prefers-contrast`, `prefers-color-scheme`.

## Security test matrix (v0.1 tests, not aspirational)

| Class | Test |
|---|---|
| Path traversal | `/items/{id}/epub/../../etc/passwd`, library paths outside allowed roots, zip entries with `..` or absolute paths |
| Zip bomb | entry count, per-entry size, total size limits enforced |
| Stored XSS | item title/description containing `<script>` renders as text in every view and OPDS |
| Auth bypass | every `/api/v1` route except `/auth/*`, `/healthz`, `/readyz`, `/manifest.webmanifest`, `/sw.js`, `/assets/*` returns 401 unauthenticated; route matching is exact, no prefix tricks (`/api/v1/itemsX`) |
| Token confusion | session cookie rejected as Bearer and vice versa; api token scopes enforced |
| Cross-library access | user without library access gets 404 on item, cover, stream, epub, download |
| SSRF | cover/metadata fetch refuses non-http(s), loopback, link-local, RFC1918 unless allowlisted; disabled entirely when no provider enabled |
| Proxy-header auth | header ignored from non-trusted source IP |
| Rate limit | login and setup endpoints limited per IP |
| Settings at rest | the OIDC client secret is ciphertext in the row, never returned by the API, and a wrong key fails loudly rather than reading as unset |
| Setup gate | every `/api/v1` route except the wizard and the public probes answers 403 until setup completes; the 401 check still runs first |
| Settings authorization | `GET`/`PUT /admin/settings` and the OIDC test are admin-only; a plain user gets 403, an anonymous caller 401 |
| Lockout | the password form cannot be turned off while OIDC is off, nor while no administrator could sign in through it; `GOBOOKSHELF_ADMIN_RECOVERY` is environment-only |
| SSO group mapping | an identity in neither configured group is refused and creates no account |
| Setup token | spendable exactly once even under concurrent requests, so the wizard can only ever create one administrator; a rejected account hands the token back |
| Upload permission | a plain user without `can_upload`, and a `restricted` account with it set, are both refused on upload and import; the flag grants both; withdrawing it takes effect on the next request; `/auth/me` agrees with what the endpoints do |
| Upload validation | wrong extension for the library kind, magic bytes that do not match the extension, a zip that is not an EPUB, an archive over the entry limit, an empty file and a file over the size cap are each refused, and none of them leaves anything behind - staging directory included |
| Upload naming | the client's filename is read for its extension only: a traversal in it cannot produce a path, and a `subdir` that is a path, `..`, or absolute is refused |
| Upload duplication | the same bytes twice answers 409 naming the existing item, whether in one request or two |
| Upload rate limit | uploads are limited per account, and only one is accepted at a time from one account |
| Import SSRF | a URL import refuses loopback, private, link-local and carrier-grade-NAT addresses and any non-http(s) scheme before a job is queued, and never echoes the address back; a chapter walk never leaves the starting host |
| Cross-library upload | uploading or importing into a library the caller cannot see answers 404 and writes nothing into it |

## Milestones

- v0.1 walking skeleton: scan, ingest EPUB + M4B/MP3, catalog, single admin,
  home/library/item views, reader (font scale + theme + layout), player
  (chapters, speed, skip, position sync), PWA manifest + shell SW.
- v0.2 users/roles, OIDC, per-library access, per-user settings/progress,
  continue rows, OPDS read-only.
- v0.3 full reader settings, high-contrast themes, reduced motion, axe gate,
  offline downloads, background progress sync.
- v0.4 series/authors/tags/collections UI, metadata edit, opt-in online
  metadata, duplicates, stats.
- v0.5 third-party sync API, OPDS-PSE, MediaSession polish.
- v1.0 stable API, docs, backup/export, progress import.
