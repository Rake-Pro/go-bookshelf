# go-bookshelf

A personal ebook and audiobook library that runs as one static Go binary over
SQLite or Postgres. Point it at a directory of `.epub`, `.m4b`, `.m4a` and `.mp3`
files; it reads the metadata that is already inside them, builds a catalog, and
serves an installable web app for reading and listening.

Accessibility is an acceptance criterion, not an afterthought: resizable text,
adjustable spacing, high-contrast themes, large touch targets, and markup that
is clean for a screen reader. See [Accessibility](#accessibility).

## Features

- **One binary, one file.** Pure-Go SQLite, no CGO, no external services, no
  transcoder to install. The frontend is embedded in the executable. Point it
  at Postgres instead and it needs no local disk at all, which is what lets it
  run on more than one node - see [Database](#database).
- **Reads what the files already say.** EPUB metadata from `container.xml` and
  the OPF package document; MP3 metadata from ID3v2 (2.2, 2.3 and 2.4) with
  durations computed from MPEG frame headers, including Xing and VBRI variable
  bitrate headers; M4B/M4A metadata from the MP4 box tree, including `ilst`
  tags, freeform atoms and `chpl` chapter lists. No outbound calls are made
  unless an online metadata provider is explicitly enabled.
- **Incremental scanning.** Files are re-parsed only when their size or
  modification time changes. A filesystem watcher picks up new books within
  seconds; a timer catches network shares that emit no events.
- **Nothing disappears by accident.** A file that goes missing marks its item
  rather than deleting it. Only after seven days is the item removed, and the
  reading positions are kept for another thirty in case the share comes back.
- **Per-user state that follows you.** Progress, bookmarks, reader typography
  and player preferences are stored server-side per account.
- **Configured in the app, not the environment.** A setup wizard on first run
  and an admin settings page after it. Single sign-on, the base URL, sessions,
  cookies, scanning, proxy authentication and metrics access are all stored in
  the database - credentials encrypted - and applied to the running server without a
  restart. What is left in the environment is the handful of values that have to
  be known before the database is open, one of which is the key that encrypts
  the rest.
- **Real access control.** Local accounts with argon2id passwords, OIDC login
  with admin and user group mapping, an optional reverse-proxy header mode,
  per-library access grants, and API tokens with read and write scopes.
- **OPDS 1.2 catalog** at `/opds`, authenticated with an API token, for
  third-party reader apps.
- **Safe with untrusted books.** Archives are opened under hard entry-count and
  size limits with traversal and symlink guards; book content is served into a
  sandboxed iframe under `default-src 'none'`; metadata is returned as data and
  escaped everywhere it is rendered.

## Quick start

First, generate the key that encrypts the credentials go-bookshelf stores in
its own database. There is no default and the server will not start without it:

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

Then read the one-time setup token out of the log:

```
docker logs go-bookshelf | grep 'one-time token'
```

Open `http://localhost:8080/setup` and paste it. The wizard walks through the
administrator account, the URL this server is reached on, single sign-on
(optional), and your first library - point it at `/books`. Everything it asks
for is stored in the database and can be changed later under **Admin ->
Settings**. Nothing else needs to be in the environment.

Mounting media read-only is deliberate: go-bookshelf never writes to your
library. Everything it generates - the catalog, the cover artwork - is in the
database, which by default is the SQLite file in `/data`.

### Environment

The environment carries only what has to be known before the database is open.

| Variable | Default | Purpose |
|---|---|---|
| `GOBOOKSHELF_SECRETS_KEY` | **required** | 32 bytes of base64 (`openssl rand -base64 32`). Encrypts the credentials stored in the database |
| `GOBOOKSHELF_LISTEN` | `:8080` | Address to bind |
| `GOBOOKSHELF_DB_DRIVER` | `sqlite` | `sqlite` or `postgres` |
| `GOBOOKSHELF_DB_PATH` | `/data/go-bookshelf.db` | SQLite database file. Only for `sqlite`; setting it with `postgres` is refused |
| `GOBOOKSHELF_DB_DSN` | unset | Postgres connection string. Required for `postgres`, refused for `sqlite` |
| `GOBOOKSHELF_DATA_DIR` | unset | Optional writable directory used as a cover cache. Leave it unset and nothing is written to local disk |
| `GOBOOKSHELF_LOG_LEVEL` | `info` | `trace`, `debug`, `info`, `warn` or `error` |
| `GOBOOKSHELF_CONFIG` | unset | Path to a YAML file carrying the keys above |
| `GOBOOKSHELF_ADMIN_RECOVERY` | `false` | Forces the password form back on when single sign-on has locked you out |
| `GOBOOKSHELF_DEV_INSECURE_KEY` | `false` | **Local development only.** Derives a fixed secrets key from a published constant so a throwaway database survives a restart |

`GOBOOKSHELF_LISTEN`, `_DB_DRIVER`, `_DB_PATH`, `_DB_DSN`, `_DATA_DIR` and
`_LOG_LEVEL` also have YAML equivalents; see `config.example.yaml`. The
environment always wins over the file, and an unknown key in the file is refused
rather than ignored. The secrets key is deliberately environment-only: it should
not sit in a file beside the database it decrypts.

### Database

Two backends, same schema, same behaviour. Migrations run automatically at
startup for whichever one is selected.

**SQLite** (the default) is pure Go - no cgo, no server - and keeps an
installation to one file. It is the right answer for a single box.

**Postgres** is for running go-bookshelf on more than one machine, or on a
scheduler that may move it between machines. Point `GOBOOKSHELF_DB_DSN` at a
database and leave `GOBOOKSHELF_DB_PATH` and `GOBOOKSHELF_DATA_DIR` unset:

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

With Postgres the container needs **no writable volume at all**. Every piece of
state - the catalog, users, sessions, API tokens, reading positions, bookmarks,
the settings document, the first-run setup token, scan history and the cover
images - lives in the database, and the media mount is read-only. That is what
lets the process be rescheduled onto any node.

`GOBOOKSHELF_DATA_DIR` stays available for either backend, and it is now only a
cache: cover images are written to it after they are read from the database, and
deleting it costs nothing but the next read. The DSN may carry a password, so it
is only ever logged, and only ever shown on the admin page, with the password
replaced.

Cover artwork is stored in the database as two bounded JPEGs per book - a
thumbnail at most 400px on its longest side for grids, and a full-size render at
most 1600px for the detail page and the reader. Both are produced once, during
the scan that ingests the book, from whatever the file happened to contain.

Backups differ by backend: copy the SQLite file, or `pg_dump` the Postgres
database. There is no in-app backup for either.

**Everything else is configured in the app**: the base URL, cookie and session
behaviour, the scan interval, single sign-on, reverse-proxy authentication, the
online metadata provider and the `/metrics` allow list. Libraries too. All of it
lives in the database and is edited at **Admin -> Settings**, with a save applying to
the running server rather than needing a restart.

If you lose the secrets key, the stored OIDC client secret cannot be decrypted
and the server refuses to start rather than pretending it was never configured.
Set a new key, start with `GOBOOKSHELF_ADMIN_RECOVERY=true`, and re-enter the
credentials at Admin -> Settings.

## How your files are read

**Ebooks.** One `.epub` file is one item. Title, subtitle, creators with their
roles, language, identifiers, publisher, date, description, subjects and series
come from the OPF package document. Series are read from the EPUB 3
`belongs-to-collection` property and from the older `<meta name="...series">`
convention that most library tools write. The cover is the OPF cover image,
falling back to the first image in the manifest. A `metadata.opf` sitting next
to the file replaces the embedded metadata.

**Audiobooks.** A directory containing audio files is one item; the files are
ordered by disc and track tags, then by a natural filename sort so `part-2`
precedes `part-10`. A single `.m4b` sitting directly in a library root is also
one item. Chapters come from the MP4 `chpl` list or ID3 `CHAP` frames; where a
file carries none, the file itself becomes one chapter. Cover art is taken from
the embedded artwork or from a `cover.jpg`/`cover.png` in the directory.

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

## First run

There are no default credentials. On a database with no accounts, the server
generates a one-time setup token, prints it to the log once, and stores only its
hash. Until the wizard finishes, every API route other than the wizard itself
and the public status endpoints answers `403 setup_required`, so a
half-configured server cannot be driven through its ordinary API.

The wizard, at `/setup`:

1. **Token** - paste the one-time token from the log. It is checked, not spent,
   so a typo fails here rather than after you have also chosen a password.
2. **Administrator** - username, display name and password. The token is spent
   here and the account is signed in, so the remaining steps run as an admin.
3. **Base URL** - prefilled from `X-Forwarded-Proto` / `X-Forwarded-Host` when a
   proxy set them, otherwise from the address you opened.
4. **Single sign-on** - optional, with a **Test** button that runs discovery
   against the issuer without saving anything. Skippable.
5. **First library** - name, kind and a path that must already exist and be
   readable by the server. Skippable.
6. **Done** - setup is marked complete and the rest of the app opens.

`/setup/token`, `/setup/admin` and `/auth/login` are rate limited per source
address.

Upgrading from a release that had no wizard? A database that already has
accounts starts with setup marked complete, so nothing is gated. Set
`GOBOOKSHELF_SECRETS_KEY` and re-enter your OIDC credentials at Admin ->
Settings; the old `GOBOOKSHELF_OIDC_*` variables are gone.

## Single sign-on

Fill the issuer, client id and client secret at Admin -> Settings (or during
setup) and a "Sign in with SSO" button appears alongside the password form.
Register this redirect URI with your provider - the settings page shows it
verbatim:

```
<your base URL>/api/v1/auth/oidc/callback
```

Saving runs discovery against the issuer first: a provider that does not answer
fails the save with the error, rather than being stored and discovered to be
broken at the next sign-in attempt. The issuer is stored exactly as you type it,
trailing slash and all, because that is what a token's `iss` claim is compared
against.

**Group mapping.** Two group names, both optional, both matched against the
claim named in **Groups claim** (`groups` by default):

- **Admin group** - members get the administrator role.
- **User group** - when set, only members of the admin group or the user group
  may sign in at all. Anyone else is refused with "not authorized for this
  application" and no account is created for them. Left empty, any identity your
  provider authenticates signs in as an ordinary user.

The role is re-evaluated on every sign-in, so moving somebody between groups in
your directory promotes or demotes them here too. Two accounts are never
touched: one with the `restricted` role, which is a local decision your
directory knows nothing about, and an administrator who still has a local
password - the break-glass account. Demoting that one would remove the way back
in at exactly the moment the directory is what went wrong.

An account is matched first on the OIDC subject, then adopted by username, so
you can pre-create an account and grant it library access before its owner signs
in for the first time. Turning **Create accounts automatically** off means an
account must already exist before a verified identity is let in.

If discovery fails at startup the server logs it and continues with password
sign-in, rather than refusing to boot because the provider is briefly
unreachable.

**Locked out?** Password sign-in can be turned off, but only while single
sign-on is on, and only once an administrator could actually get back in through
it. If it happens anyway, restart with `GOBOOKSHELF_ADMIN_RECOVERY=true`: the
password form comes back for as long as that variable is set. It is deliberately
not settable from inside the app.

## Behind a reverse proxy

- Set the base URL at Admin -> Settings to the external URL. It decides the SSO
  redirect, the identifiers in OPDS feeds, and - under the default `auto` cookie
  mode - whether session cookies are marked `Secure`.
- Media streaming relies on range requests. Make sure your proxy passes `Range`
  and `If-Range` through and does not buffer whole responses.
- Do not add a second `Content-Security-Policy`: book content is served under a
  deliberately strict policy of its own, and a proxy-level policy would break
  the reader.
- For proxy-terminated authentication, turn on **Reverse-proxy authentication**
  at Admin -> Settings and name the header plus the trusted CIDRs. The header is
  honored only when the immediate peer address falls inside those CIDRs;
  forwarding headers such as `X-Forwarded-For` never confer trust, because they
  are attacker-controlled. Turning it on with an empty CIDR list is refused.
- `/metrics` answers a small Prometheus exposition and is limited to the CIDRs
  under **Metrics** (loopback and the private ranges by default).

## Accessibility

Accessibility is treated as a feature with acceptance criteria, not as polish
applied at the end. What that means in practice:

- **Text scales two ways.** The interface never sets a pixel root font size, so
  the browser's and the operating system's own text scaling apply in full. A
  separate interface scale in Settings (100-160%) multiplies on top of that, and
  the reader's own font scale (70-250%) is independent of both.
- **Typography you control.** Line height, letter spacing, word spacing,
  paragraph spacing, margins, alignment, one or two columns, and paginated or
  scrolled flow. A dyslexia-friendly font stack is offered and falls back to the
  system sans when none of its faces are installed.
- **Themes, including high contrast.** Light, dark, sepia, high-contrast light,
  high-contrast dark, and a custom foreground/background pair. The app theme and
  the reading theme are set separately. `prefers-color-scheme` is the default,
  and while the theme is left on automatic, `prefers-contrast: more` hardens the
  palette on its own.
- **Motion.** `prefers-reduced-motion` disables every transition and animation.
- **Keyboard.** Everything is reachable and operable by keyboard, including the
  reader (paging, contents, settings, jump to start or end) and the player. Key
  handling is attached inside the book frame as well, because events there do
  not reach the host document. Focus is visible everywhere, a skip link leads to
  the main region, and modal sheets use `<dialog>` for focus trapping.
- **Targets and pointers.** Every interactive control is at least 44x44 CSS
  pixels.
- **Screen readers.** Landmarks on every region, an accessible name on every
  icon-only control, and live regions that announce playback state, reader
  position ("Page 4 of 212"), saved settings and search result counts. Sliders
  carry human `aria-valuetext` ("1 hour 24 minutes of 9 hours") rather than raw
  numbers.
- **Never colour alone.** Progress bars are paired with text, and error and
  success blocks pair an icon and a heading with the colour.

The full contract, including what a change is expected not to regress, is in
[docs/FRONTEND.md](docs/FRONTEND.md#accessibility-contract).

## API

Everything the app does is available over JSON at `/api/v1`, authenticated with
the session cookie (`gbs_session`) or `Authorization: Bearer <api token>`.
Errors are `{"error":{"code":"...","message":"..."}}`, and list endpoints accept
`?limit=&offset=&sort=&q=` and return `{"items":[...],"total":n}`.

**[docs/DESIGN.md](docs/DESIGN.md#http-api-apiv1) is the API reference**: every
route, its request and response shape, the data model behind it, and the
security rules each handler enforces. [docs/FRONTEND.md](docs/FRONTEND.md)
documents the same surface from the client's side, including the exact fields
the bundled app reads.

A short tour of the shape:

| Route | What it answers |
|---|---|
| `GET /auth/status` | Public: whether first-run setup is pending, whether SSO is offered, whether the password form is |
| `POST /setup/{step}` | The first-run wizard: `token`, `admin`, `base-url`, `oidc`, `library`, `complete` |
| `GET\|PUT /admin/settings` | Admin: the whole application configuration |
| `GET /auth/me` | The signed-in account, or 401 |
| `GET /home` | Continue reading, recently added, series in progress |
| `GET /items` | The catalog, filtered by library, kind, author, series, tag or query |
| `GET /items/{id}` | One item with its people, series, tags, files, chapters and your progress |
| `GET /items/{id}/epub` | A reading manifest: spine, sizes, and where the resources live |
| `GET /items/{id}/files/{file_id}/stream` | Audio, with range requests |
| `PUT /me/progress/{item_id}` | Where you got to, per device |
| `GET|PUT /me/settings` | Reader, player and interface preferences |
| `GET /system/status` | Admin: version, database size, item counts, last scans, whether SSO is on, when the settings last changed |

Create a token under Settings, or:

```
curl -b cookies.txt -X POST https://books.example.com/api/v1/me/tokens \
  -H 'Content-Type: application/json' \
  -d '{"name":"reader app","scopes":["read"]}'
```

The secret is shown once and stored only as a hash. Tokens carry `read` or
`read`+`write`; a read-only token is refused on every mutating route, and no
token can mint another token. The same token authenticates OPDS clients through
HTTP Basic, with the token in the password field.

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

`make smoke` walks the same sequence the web app performs - setup, login,
create a library, scan, home, item detail, EPUB manifest and resources, a
ranged audio request, progress, settings, bookmarks, OPDS, logout - so a
backend change that breaks a view fails the build rather than the user. The
same journey runs as a Go test in `internal/api/frontend_test.go`.

The test suite generates its own fixtures - EPUB containers, MPEG frames and
MP4 boxes are built byte by byte in `internal/fixtures` - so no sample books
are carried in the repository. `scripts/gen-samples` writes the same fixtures
to a directory if you want a library to click around in:

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
