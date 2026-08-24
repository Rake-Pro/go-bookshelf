# go-bookshelf

A personal ebook and audiobook library server: one static Go binary over SQLite
or Postgres. Point it at a directory of `.epub`, `.m4b`, `.m4a` and `.mp3`
files; it catalogs the metadata already inside them and serves an installable
web app for reading and listening.

Accessibility is an acceptance criterion, not an afterthought: resizable text,
adjustable spacing, high-contrast themes, large touch targets, clean
screen-reader markup. See [Accessibility](#accessibility).

## Features

- **One binary, one file.** Pure-Go SQLite (no CGO), embedded frontend, no
  external services or transcoder. With Postgres it needs no local disk at all
  and can run on any node - see [Database](#database).
- **Reads embedded metadata.** EPUB from `container.xml` and the OPF package;
  MP3 from ID3v2 (2.2-2.4) with durations from MPEG frame headers (including
  Xing/VBRI); M4B/M4A from the MP4 box tree (`ilst` tags, freeform atoms,
  `chpl` chapters). No outbound calls unless an online metadata provider is
  explicitly enabled.
- **Incremental scanning.** Files re-parse only when size or mtime changes. A
  filesystem watcher picks up new books within seconds; a timer covers network
  shares that emit no events.
- **Nothing disappears by accident.** A missing file marks its item rather
  than deleting it; removal happens after seven days, and reading positions
  are kept another thirty in case the share comes back.
- **Per-user state that follows you.** Progress, bookmarks, reader typography
  and player preferences are stored server-side per account.
- **Add books from the browser.** Upload files, or paste a URL - a book file,
  or a web story the server cleans up and builds into an EPUB. Everything is
  validated by the scanner's own parser before touching a library. See
  [Adding books](#adding-books).
- **Configured in the app, not the environment.** A first-run wizard, then an
  admin settings page. SSO, base URL, sessions, cookies, scanning, proxy auth
  and metrics access live in the database (credentials encrypted) and apply
  without a restart. The environment carries only what must be known before
  the database opens.
- **Real access control.** Local accounts (argon2id), OIDC with admin/user
  group mapping, optional reverse-proxy header mode, per-library access
  grants, API tokens with read/write scopes.
- **OPDS 1.2 catalog** at `/opds`, authenticated with an API token, for
  third-party reader apps.
- **Safe with untrusted books.** Archives open under hard entry-count and size
  limits with traversal and symlink guards; book content is served in a
  sandboxed iframe under `default-src 'none'`; metadata is escaped everywhere
  it is rendered.

## Quick start

Generate the key that encrypts stored credentials - there is no default and
the server will not start without it:

```
openssl rand -base64 32
```

```
docker run -d --name go-bookshelf \
  -p 8080:8080 \
  -v go-bookshelf-data:/data \
  -v /srv/books:/books:ro \
  -e GOBOOKSHELF_SECRETS_KEY='<the key you just generated>' \
  ghcr.io/rake-pro/go-bookshelf:latest
```

Read the one-time setup token from the log:

```
docker logs go-bookshelf | grep 'one-time token'
```

Open `http://localhost:8080/setup` and paste it. The wizard covers the admin
account, the base URL, optional single sign-on, and your first library - point
it at `/books`. Everything is stored in the database and editable later at
**Admin -> Settings**.

The read-only media mount is deliberate: go-bookshelf never writes to your
library. Everything it generates - catalog, covers - lives in the database, by
default the SQLite file in `/data`.

### Environment

Only what must be known before the database is open:

| Variable | Default | Purpose |
|---|---|---|
| `GOBOOKSHELF_SECRETS_KEY` | **required** | 32 bytes of base64 (`openssl rand -base64 32`); encrypts credentials stored in the database |
| `GOBOOKSHELF_LISTEN` | `:8080` | Address to bind |
| `GOBOOKSHELF_DB_DRIVER` | `sqlite` | `sqlite` or `postgres` |
| `GOBOOKSHELF_DB_PATH` | `/data/go-bookshelf.db` | SQLite database file; refused with `postgres` |
| `GOBOOKSHELF_DB_DSN` | unset | Postgres connection string; required for `postgres`, refused for `sqlite` |
| `GOBOOKSHELF_DATA_DIR` | unset | Optional cover cache directory; unset means nothing is written to local disk |
| `GOBOOKSHELF_LOG_LEVEL` | `info` | `trace`, `debug`, `info`, `warn` or `error` |
| `GOBOOKSHELF_CONFIG` | unset | YAML file carrying the keys above |
| `GOBOOKSHELF_ADMIN_RECOVERY` | `false` | Forces the password form back on when SSO has locked you out |
| `GOBOOKSHELF_DEV_INSECURE_KEY` | `false` | **Local development only.** Fixed secrets key so a throwaway database survives restarts |

The listener, database, data-dir and log keys have YAML equivalents (see
`config.example.yaml`); the environment wins, and unknown file keys are
refused. The secrets key is deliberately environment-only: it should not sit
in a file beside the database it decrypts.

### Database

Two backends, same schema, same behaviour; migrations run at startup.

**SQLite** (default): pure Go, no server, one file - right for a single box.

**Postgres**: for more than one machine, or a scheduler that moves the
process. Set `GOBOOKSHELF_DB_DSN`; leave `GOBOOKSHELF_DB_PATH` and
`GOBOOKSHELF_DATA_DIR` unset:

```
docker run -d --name go-bookshelf \
  -p 8080:8080 \
  -v /srv/books:/books:ro \
  -e GOBOOKSHELF_DB_DRIVER=postgres \
  -e GOBOOKSHELF_DB_DSN='postgres://bookshelf:PASSWORD@db.example.com:5432/bookshelf?sslmode=require' \
  -e GOBOOKSHELF_DB_PATH= \
  -e GOBOOKSHELF_DATA_DIR= \
  -e GOBOOKSHELF_SECRETS_KEY='<your key>' \
  ghcr.io/rake-pro/go-bookshelf:latest
```

With Postgres the container needs **no writable volume**: catalog, users,
sessions, API tokens, reading positions, bookmarks, settings, the setup token,
scan history and cover images all live in the database, and media is
read-only - which is what allows rescheduling onto any node.

`GOBOOKSHELF_DATA_DIR` works with either backend and is only a cache: covers
are written to it after being read from the database, and deleting it costs
one re-read. The DSN may carry a password, so it is logged and displayed only
with the password redacted.

Covers are stored as two bounded JPEGs per book - a thumbnail at most 400px on
the longest side, a full render at most 1600px - produced once at scan time.

Backups: copy the SQLite file, or `pg_dump` the Postgres database. There is no
in-app backup.

**Everything else is configured in the app** - base URL, cookie and session
behaviour, scan interval, SSO, reverse-proxy auth, the metadata provider, the
`/metrics` allow list, and libraries - at **Admin -> Settings**, applied to
the running server without a restart.

If the secrets key is lost, the stored OIDC client secret cannot be decrypted
and the server refuses to start rather than pretending it was never
configured. Set a new key, start with `GOBOOKSHELF_ADMIN_RECOVERY=true`, and
re-enter the credentials at Admin -> Settings.

## How your files are read

**Ebooks.** One `.epub` is one item. Title, subtitle, creators with roles,
language, identifiers, publisher, date, description, subjects and series come
from the OPF - series via the EPUB 3 `belongs-to-collection` property and the
older `<meta name="...series">` convention. The cover is the OPF cover image,
falling back to the first manifest image. A `metadata.opf` next to the file
replaces the embedded metadata.

**Audiobooks.** A directory of audio files is one item, ordered by disc and
track tags then natural filename sort (`part-2` before `part-10`); a single
`.m4b` in a library root also works. Chapters come from the MP4 `chpl` list or
ID3 `CHAP` frames, else each file is one chapter. Cover art comes from
embedded artwork or a `cover.jpg`/`cover.png` in the directory.

A `metadata.json` in the audiobook directory overrides the tags:

```json
{
  "title": "The Long Afternoon",
  "subtitle": "A Study in Idleness",
  "authors": ["A. Writer"],
  "narrators": ["C. Reader"],
  "series": "Afternoons",
  "series_index": 1,
  "description": "...",
  "publisher": "Example Press",
  "language": "en",
  "published": "2019-04-01",
  "isbn": "9781234567897",
  "tags": ["Fiction"]
}
```

## Adding books

Books normally arrive by being copied into a library directory. The web app
can also add them, two ways.

**Who may.** Adding books is its own permission: administrators always,
`restricted` accounts never, everyone else via a per-account switch on the
admin page (off for existing accounts after an upgrade). The **Add books**
button appears only for permitted accounts, and only for libraries they can
already see.

**Uploading.** Drag files onto the sheet or pick them. The selection goes in
one request, which matters for audiobooks: files whose album and author tags
agree become one book, not several.

| Library kind | Extensions | Size cap per file |
|---|---|---|
| Ebooks | `.epub` | 200 MiB |
| Audiobooks | `.m4b`, `.m4a`, `.mp3` | 2 GiB |
| Mixed | all four | as above |

Extensions are claims, not evidence: every file is checked against the
format's magic bytes (a real zip with an `application/epub+zip` `mimetype`
entry, an MP4 `ftyp` box, an ID3 tag or genuine MPEG frame header) and parsed
by the same reader the scanner uses, under the same limits. A failed file is
discarded and the library is untouched: uploads stage in a hidden directory
the scanner skips and are renamed into place only after passing.

The uploaded filename is used only for its extension (a browser will happily
send `../../etc/cron.d/x`). On-disk names derive from the book's own metadata:

```
A. Writer - The Long Afternoon.epub
A. Writer - The Long Night/01 - Part One.mp3
A. Writer - The Long Night/02 - Part Two.mp3
```

folded to ASCII, stripped of filesystem-reserved characters, length-capped,
suffixed `(2)`, `(3)` on collision. One plain subfolder may be named to file
into - a name, not a path. A byte-identical re-upload answers with a link to
the existing copy.

**Importing from a URL.** The server fetches in the background; the sheet
polls the job and can cancel it. What the URL is gets decided by the bytes,
not the extension or the remote server's claims:

- **A book file** goes through the same validation as an upload.
- **A web page** goes to the story importer: title and author from the page's
  own metadata (`og:title`, schema.org, a byline), article body extracted
  (scripts, styles, forms, navigation, footers, share bars, comments and ads
  stripped; headings, paragraphs, lists and images kept), images re-fetched
  and embedded, "next chapter" links followed (same site only, one request a
  second), and the result built into an EPUB - then validated like any upload.

The importer does not run JavaScript (client-rendered pages yield nothing),
does not sign in (no paywalls), uses a generic extractor that will sometimes
take too much or too little (per-site adapters are the intended fix), and
refuses private, loopback and link-local addresses. Imports are rate limited
per account, run one at a time server-wide, and are capped at 2 GiB per
download and 2,000 chapters per story.

## First run

There are no default credentials. On a database with no accounts, the server
prints a one-time setup token to the log (storing only its hash). Until the
wizard finishes, every route other than the wizard and the public status
endpoints answers `403 setup_required`.

The wizard, at `/setup`:

1. **Token** - checked here, not spent, so a typo fails early.
2. **Administrator** - username, display name, password; the token is spent
   and the account signed in.
3. **Base URL** - prefilled from `X-Forwarded-Proto`/`X-Forwarded-Host` or the
   address you opened.
4. **Single sign-on** - optional; **Test** runs discovery without saving.
5. **First library** - name, kind, an existing readable path. Skippable.
6. **Done.**

`/setup/token`, `/setup/admin` and `/auth/login` are rate limited per source
address.

Upgrading from a pre-wizard release: a database with accounts starts with
setup complete. Set `GOBOOKSHELF_SECRETS_KEY` and re-enter OIDC credentials at
Admin -> Settings; the old `GOBOOKSHELF_OIDC_*` variables are gone.

## Single sign-on

Fill issuer, client id and client secret at Admin -> Settings (or during
setup) and a "Sign in with SSO" button appears. Register the redirect URI your
settings page shows verbatim:

```
<your base URL>/api/v1/auth/oidc/callback
```

Saving runs discovery first - a provider that does not answer fails the save
now rather than at the next sign-in. The issuer is stored exactly as typed,
trailing slash included, because that is what the token's `iss` claim is
compared against.

**Group mapping.** Two optional group names, matched against the **Groups
claim** (`groups` by default):

- **Admin group** - members get the administrator role.
- **User group** - when set, only members of either group may sign in; anyone
  else is refused and no account is created. Left empty, any authenticated
  identity signs in as an ordinary user.

The role is re-evaluated on every sign-in, so directory changes promote and
demote here too. Two accounts are never touched: `restricted` ones (a local
decision) and an administrator who still has a local password - the
break-glass account.

An account is matched on OIDC subject first, then adopted by username, so you
can pre-create an account and grant library access before its first sign-in.
Turning **Create accounts automatically** off requires accounts to exist
before a verified identity is let in.

If discovery fails at startup the server logs it and continues with password
sign-in rather than refusing to boot.

**Locked out?** Password sign-in can only be disabled while SSO is on. If it
happens anyway, restart with `GOBOOKSHELF_ADMIN_RECOVERY=true`: the password
form returns while the variable is set. Deliberately not settable in-app.

## Behind a reverse proxy

- Set the base URL at Admin -> Settings to the external URL. It decides the
  SSO redirect, OPDS identifiers, and (under the default `auto` cookie mode)
  whether session cookies are `Secure`.
- Media streaming relies on range requests: pass `Range` and `If-Range`
  through and do not buffer whole responses.
- Do not add a second `Content-Security-Policy`: book content ships its own
  deliberately strict policy, and a proxy-level one breaks the reader.
- For proxy-terminated auth, enable **Reverse-proxy authentication** and name
  the header plus trusted CIDRs. The header is honored only when the immediate
  peer is inside those CIDRs; forwarding headers like `X-Forwarded-For` never
  confer trust. An empty CIDR list is refused.
- `/metrics` serves Prometheus exposition, limited to the CIDRs under
  **Metrics** (loopback and private ranges by default).

## Accessibility

- **Text scales two ways.** No pixel root font size, so browser and OS text
  scaling apply in full; an interface scale in Settings (100-160%) multiplies
  on top, and the reader's font scale (70-250%) is independent of both.
- **Typography you control.** Line height, letter/word/paragraph spacing,
  margins, alignment, one or two columns, paginated or scrolled flow, and a
  dyslexia-friendly font stack with a system-sans fallback.
- **Themes, including high contrast.** Paper, sepia, gray, night,
  high-contrast light/dark, and a custom pair. App and reading themes are set
  separately, and a reading theme colors the reader's own chrome too.
  `prefers-color-scheme` is the default; on automatic, `prefers-contrast:
  more` hardens the palette.
- **Motion.** `prefers-reduced-motion` disables every transition and
  animation.
- **Keyboard.** Everything is operable by keyboard, including the reader
  (paging, contents, settings, jump to start/end) and the player - with key
  handling attached inside the book frame, where events do not reach the host
  document. Focus is visible everywhere, a skip link leads to the main region,
  and modal sheets use `<dialog>` for focus trapping.
- **Targets.** Every interactive control is at least 44x44 CSS pixels.
- **Screen readers.** Landmarks on every region, accessible names on icon-only
  controls, live regions announcing playback state, reader position, saved
  settings and search counts, and sliders with human `aria-valuetext`
  ("1 hour 24 minutes of 9 hours").
- **Never colour alone.** Progress bars pair with text; error and success
  blocks pair icon and heading with the colour.

The full contract, including what a change must not regress, is in
[docs/FRONTEND.md](docs/FRONTEND.md#accessibility-contract).

## API

Everything the app does is available as JSON at `/api/v1`, authenticated with
the session cookie (`gbs_session`) or `Authorization: Bearer <api token>`.
Errors are `{"error":{"code":"...","message":"..."}}`; list endpoints accept
`?limit=&offset=&sort=&q=` and return `{"items":[...],"total":n}`.

**[docs/DESIGN.md](docs/DESIGN.md#http-api-apiv1) is the API reference** -
every route, shape, the data model and per-handler security rules.
[docs/FRONTEND.md](docs/FRONTEND.md) documents the same surface from the
client's side.

A short tour:

| Route | What it answers |
|---|---|
| `GET /auth/status` | Public: setup pending, SSO offered, password form offered |
| `POST /setup/{step}` | The first-run wizard: `token`, `admin`, `base-url`, `oidc`, `library`, `complete` |
| `GET\|PUT /admin/settings` | Admin: the whole application configuration |
| `GET /auth/me` | The signed-in account, or 401 |
| `GET /home` | Continue reading, recently added, series in progress |
| `GET /items` | The catalog, filtered by library, kind, author, series, tag or query |
| `GET /items/{id}` | One item with people, series, tags, files, chapters and your progress |
| `GET /items/{id}/epub` | Reading manifest: spine, sizes, resource locations |
| `GET /items/{id}/files/{file_id}/stream` | Audio, with range requests |
| `POST /libraries/{id}/upload` | Add book files to a library (multipart) |
| `POST /libraries/{id}/import` | Queue a URL import; `GET /me/imports` follows it |
| `PUT /me/progress/{item_id}` | Where you got to, per device |
| `GET\|PUT /me/settings` | Reader, player and interface preferences |
| `GET /system/status` | Admin: version, database size, counts, last scans, SSO state |

Create a token under Settings, or:

```
curl -b cookies.txt -X POST https://books.example.com/api/v1/me/tokens \
  -H 'Content-Type: application/json' \
  -d '{"name":"reader app","scopes":["read"]}'
```

The secret is shown once and stored as a hash. Tokens carry `read` or
`read`+`write`; read-only tokens are refused on mutating routes, and no token
can mint another. The same token authenticates OPDS clients via HTTP Basic,
token in the password field.

## Development

Requires Go 1.26 or newer.

```
make build     # static binary into bin/
make test      # unit, contract and security tests
make vet       # go vet
make fmt       # gofmt
make checkweb  # resolve every frontend import and asset reference (no Node needed)
make smoke     # build, generate a synthetic library, boot the server, drive it
make docker    # multi-stage container build
make all       # fmt-check, vet, checkweb, test, build
```

`make smoke` drives the same journey the web app performs - setup, login,
library, scan, home, item detail, EPUB manifest and resources, ranged audio,
progress, settings, bookmarks, OPDS, logout - so a backend change that breaks
a view fails the build. The same journey runs as a Go test in
`internal/api/frontend_test.go`.

The test suite generates its own fixtures (EPUB containers, MPEG frames and
MP4 boxes built byte by byte in `internal/fixtures`), so no sample books live
in the repository. To get a clickable library:

```
go run ./scripts/gen-samples -dir /tmp/sample-library
```

Layout:

```
cmd/go-bookshelf/   flags, wiring, graceful shutdown
internal/config/    the bootstrap variables (listener, database, data dir, log, secrets key)
internal/settings/  the DB-backed application configuration, secrets encrypted
internal/store/     the two backends, the dialect seam, embedded migrations
internal/library/   catalog queries, scanner, watcher, janitor
internal/epub/      safe archive reader, container.xml and OPF parsing
internal/audio/     ID3v2, MPEG frame and MP4 box parsing
internal/images/    cover decoding, resizing and the optional on-disk cache
internal/auth/      accounts, sessions, tokens, OIDC, proxy mode
internal/api/       JSON handlers, media streaming, OPDS
internal/server/    router, middleware, SPA fallback
internal/remote/    the guarded outbound HTTP client
internal/oidctest/  a fake identity provider, used only by tests
internal/storetest/ throwaway test databases on either backend, used only by tests
web/                embedded frontend (web/dist ships byte for byte as written)
scripts/checkweb/   static resolution check for the frontend
docs/               DESIGN.md (API and data model), FRONTEND.md (client contract)
```

## License

MIT. See [LICENSE](LICENSE).
