# go-bookshelf design

Single Go binary + SQLite that serves a personal/family ebook and audiobook
library through an installable PWA. Accessibility is an acceptance criterion:
resizable text, adjustable spacing, high-contrast themes, large touch targets,
keyboard-complete, screen-reader-clean markup.

## Principles

- One static binary, one SQLite file, media mounted read-only.
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
- Progress, bookmarks and reader/player preferences are stored per user on the
  server and follow the user across devices.

## Layout

```
cmd/go-bookshelf/        main: flags/env, wiring, serve
internal/config/         env + yaml config (GOBOOKSHELF_* overrides)
internal/store/          sqlite (modernc), embedded migrations, queries
internal/library/        scanner, file watcher, ingest pipeline
internal/epub/           container.xml/OPF parse, cover, spine, safe zip reader
internal/audio/          mp3 (ID3v2) + m4b/m4a (MP4 atoms, chpl/Nero chapters), duration
internal/auth/           local users (argon2id), sessions, OIDC, proxy-header mode
internal/api/            JSON handlers (/api/v1), OPDS (/opds), media streaming
internal/server/         router, middleware (CSP, logging, auth, rate limit), static
web/                     embed.go (`//go:embed all:dist`) + dist/ (index.html, app/, vendor/, icons/)
docs/                    this file, API reference, deploy notes
```

## Config

Env vars (yaml keys in parentheses):

| Var | Default | Purpose |
|---|---|---|
| `GOBOOKSHELF_LISTEN` (`listen`) | `:8080` | bind address |
| `GOBOOKSHELF_DB_PATH` (`db_path`) | `/data/go-bookshelf.db` | SQLite file |
| `GOBOOKSHELF_DATA_DIR` (`data_dir`) | `/data` | covers cache, thumbnails |
| `GOBOOKSHELF_BASE_URL` (`base_url`) | `http://localhost:8080` | external URL (OIDC redirect, OPDS links) |
| `GOBOOKSHELF_LOG_LEVEL` | `info` | zerolog level |
| `GOBOOKSHELF_OIDC_ISSUER` / `_CLIENT_ID` / `_CLIENT_SECRET` | unset | enables OIDC login when all set |
| `GOBOOKSHELF_OIDC_ADMIN_GROUP` | unset | group claim that grants admin |
| `GOBOOKSHELF_PROXY_AUTH_HEADER` | unset | e.g. `Remote-User`; only honored from `GOBOOKSHELF_TRUSTED_PROXIES` CIDRs |
| `GOBOOKSHELF_TRUSTED_PROXIES` | unset | comma-separated CIDRs |
| `GOBOOKSHELF_SECURE_COOKIES` | auto (true when base_url is https) | |

Libraries (name, kind, paths) are created in the admin UI and stored in SQLite,
not in config.

## Data model (SQLite)

```sql
libraries(id, name, kind CHECK(kind IN ('ebook','audiobook','mixed')), created_at)
library_paths(library_id, path, PRIMARY KEY(library_id, path))
items(id, library_id, kind CHECK(kind IN ('ebook','audiobook')), title, sort_title,
      subtitle, description, language, published, isbn, asin, publisher,
      cover_path, duration_ms, size_bytes, added_at, updated_at, missing_at)
files(id, item_id, path UNIQUE, size, mtime, sha1, format, duration_ms, seq)
chapters(file_id, seq, title, start_ms, end_ms, PRIMARY KEY(file_id, seq))
people(id, name UNIQUE, sort_name)
item_people(item_id, person_id, role CHECK(role IN ('author','narrator','translator')), seq)
series(id, name UNIQUE)
item_series(item_id, series_id, sequence REAL)
tags(id, name UNIQUE)            item_tags(item_id, tag_id)
collections(id, user_id NULL, name)   collection_items(collection_id, item_id, seq)
users(id, username UNIQUE, display_name, password_hash NULL, oidc_subject NULL,
      role CHECK(role IN ('admin','user','restricted')), created_at, disabled_at)
user_library_access(user_id, library_id)
user_settings(user_id PRIMARY KEY, reader_json, player_json, ui_json, updated_at)
progress(user_id, item_id, locator, position_ms, percent REAL, finished_at NULL,
         device, updated_at, PRIMARY KEY(user_id, item_id))
bookmarks(id, user_id, item_id, locator, position_ms, note, created_at)
sessions(id, user_id, created_at, expires_at, user_agent, ip)
api_tokens(id, user_id, name, token_hash, scopes, created_at, last_used_at)
scan_runs(id, library_id, started_at, finished_at, added, updated, removed, errors)
```

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

## HTTP API (`/api/v1`)

All JSON. Auth via session cookie (`gbs_session`, HttpOnly, SameSite=Lax) or
`Authorization: Bearer <api token>`. Errors: `{"error":{"code":"...","message":"..."}}`.
List endpoints accept `?limit=&offset=&sort=&q=` and return
`{"items":[...],"total":n}`.

Auth
- `GET /auth/status` (public) -> `{setup_required, oidc_enabled, oidc_start_url, version}`
- `POST /auth/setup` `{token, username, password, display_name}` first-run admin
- `POST /auth/login` `{username, password}` -> sets cookie, returns user
- `POST /auth/logout`
- `GET /auth/oidc/start` -> 302; `GET /auth/oidc/callback`
- `GET /auth/me` -> `{id, username, display_name, role, libraries:[ids], auth_method}`

`/auth/me` is a pure "who am I": with no credential it answers 401 carrying
nothing but the error envelope. Everything the signed-out login page needs -
whether to offer SSO, whether to send the operator to `/setup` - comes from
`/auth/status`, which is deliberately public and reveals only which login
methods exist.

Libraries (admin for writes)
- `GET /libraries`; `POST /libraries` `{name, kind, paths:[]}`; `PATCH /libraries/{id}`; `DELETE /libraries/{id}`
- `POST /libraries/{id}/scan` -> `{scan_id}`; `GET /libraries/{id}/scans`

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

Admin
- `GET|POST /users`, `PATCH /users/{id}`, `DELETE /users/{id}`, `PUT /users/{id}/libraries`
- `GET /system/status` -> `{version, go_version, db_path, db_size_bytes, data_dir,
  counts:{ebooks,audiobooks}, libraries, users, last_scans, oidc_enabled, time}`

Other
- `GET /healthz`, `GET /readyz`, `GET /metrics` (Prometheus; bind-limited by `GOBOOKSHELF_METRICS_ALLOW` CIDRs, default loopback + RFC1918)
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
{"font_scale":1.0,"font_family":"publisher|system|serif|sans|dyslexic",
 "line_height":1.5,"letter_spacing":0,"word_spacing":0,"paragraph_spacing":0,
 "margin":"narrow|normal|wide","align":"publisher|left|justify",
 "theme":"light|dark|sepia|hc-dark|hc-light|custom","custom_fg":"#1f1d1a","custom_bg":"#faf8f4",
 "layout":"paginated|scrolled","columns":"auto|1|2"}
```
`font_scale` range 0.7-2.5 in 0.1 steps. App chrome uses `rem` everywhere and
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
