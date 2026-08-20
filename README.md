# go-bookshelf

A personal ebook and audiobook library that runs as one static Go binary with a
SQLite catalog. Point it at a directory of `.epub`, `.m4b`, `.m4a` and `.mp3`
files; it reads the metadata that is already inside them, builds a catalog, and
serves an installable web app for reading and listening.

Accessibility is an acceptance criterion, not an afterthought: resizable text,
adjustable spacing, high-contrast themes, large touch targets, and markup that
is clean for a screen reader. See [Accessibility](#accessibility).

## Features

- **One binary, one file.** Pure-Go SQLite, no CGO, no external services, no
  transcoder to install. The frontend is embedded in the executable.
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
- **Real access control.** Local accounts with argon2id passwords, OIDC login,
  an optional reverse-proxy header mode, per-library access grants, and API
  tokens with read and write scopes.
- **OPDS 1.2 catalog** at `/opds`, authenticated with an API token, for
  third-party reader apps.
- **Safe with untrusted books.** Archives are opened under hard entry-count and
  size limits with traversal and symlink guards; book content is served into a
  sandboxed iframe under `default-src 'none'`; metadata is returned as data and
  escaped everywhere it is rendered.

## Quick start

```
docker run -d --name go-bookshelf \
  -p 8080:8080 \
  -v go-bookshelf-data:/data \
  -v /srv/books:/books:ro \
  -e GOBOOKSHELF_BASE_URL=http://localhost:8080 \
  ghcr.io/rake-pro/go-bookshelf:latest
```

Then read the one-time setup token out of the log:

```
docker logs go-bookshelf | grep 'one-time token'
```

Open `http://localhost:8080/setup`, paste the token, and create the first
administrator. Add a library from the admin screen, pointing it at `/books`.

Mounting media read-only is deliberate: go-bookshelf never writes to your
library. Everything it generates - the database, the cover cache - lives in
`/data`.

### Environment

| Variable | Default | Purpose |
|---|---|---|
| `GOBOOKSHELF_LISTEN` | `:8080` | Address to bind |
| `GOBOOKSHELF_DB_PATH` | `/data/go-bookshelf.db` | SQLite database file |
| `GOBOOKSHELF_DATA_DIR` | `/data` | Writable directory for the cover cache |
| `GOBOOKSHELF_BASE_URL` | `http://localhost:8080` | External URL, used for the OIDC redirect, OPDS links and the cookie Secure flag |
| `GOBOOKSHELF_LOG_LEVEL` | `info` | `trace`, `debug`, `info`, `warn` or `error` |
| `GOBOOKSHELF_CONFIG` | unset | Path to a YAML config file |
| `GOBOOKSHELF_SCAN_INTERVAL` | `6h` | Background rescan interval |
| `GOBOOKSHELF_SESSION_TTL` | `720h` | How long a login session stays valid |
| `GOBOOKSHELF_SECURE_COOKIES` | auto | Forced on when `BASE_URL` is https |
| `GOBOOKSHELF_OIDC_ISSUER` | unset | OIDC issuer URL |
| `GOBOOKSHELF_OIDC_CLIENT_ID` | unset | OIDC client id |
| `GOBOOKSHELF_OIDC_CLIENT_SECRET` | unset | OIDC client secret |
| `GOBOOKSHELF_OIDC_ADMIN_GROUP` | unset | Group claim value that grants the admin role |
| `GOBOOKSHELF_OIDC_GROUPS_CLAIM` | `groups` | Claim inspected for group membership |
| `GOBOOKSHELF_OIDC_SCOPES` | `openid,profile,email` | Scopes requested at login |
| `GOBOOKSHELF_PROXY_AUTH_HEADER` | unset | Header naming the authenticated user, for example `Remote-User` |
| `GOBOOKSHELF_TRUSTED_PROXIES` | unset | Comma-separated CIDRs allowed to set that header |
| `GOBOOKSHELF_METRICS_ALLOW` | loopback + private ranges | CIDRs allowed to read `/metrics` |
| `GOBOOKSHELF_METADATA_PROVIDER` | unset | Names an online metadata provider; while empty, no outbound requests are made at all |
| `GOBOOKSHELF_METADATA_ALLOW_PRIVATE` | `false` | Let the metadata fetcher reach private addresses |

Every variable has a YAML equivalent; see `config.example.yaml`. The
environment always wins over the file. Libraries themselves are not configured
here - they are created in the app and stored in the database.

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
generates a one-time setup token, prints it to the log once, and stores only
its hash. `POST /api/v1/auth/setup` (or the `/setup` page) exchanges that token
for the first administrator account, after which the token is spent and the
endpoint is closed permanently. Both `/auth/setup` and `/auth/login` are rate
limited per source address.

## OIDC

Set the issuer, client id and client secret, and a "Sign in with your provider"
option appears alongside the password form. Register this redirect URI with
your provider:

```
<GOBOOKSHELF_BASE_URL>/api/v1/auth/oidc/callback
```

An account is matched first on the OIDC subject, then adopted by username, so
you can pre-create an account and grant it library access before its owner logs
in for the first time. With `GOBOOKSHELF_OIDC_ADMIN_GROUP` set, membership of
that group grants the admin role on every login. If discovery fails at startup
the server logs the failure and continues with local logins only, rather than
refusing to boot because the provider is briefly unreachable.

## Behind a reverse proxy

- Set `GOBOOKSHELF_BASE_URL` to the external URL. It decides the OIDC redirect,
  the identifiers in OPDS feeds, and whether session cookies are marked
  `Secure`.
- Media streaming relies on range requests. Make sure your proxy passes
  `Range` and `If-Range` through and does not buffer whole responses.
- Do not add a second `Content-Security-Policy`: book content is served under a
  deliberately strict policy of its own, and a proxy-level policy would break
  the reader.
- For proxy-terminated authentication, set `GOBOOKSHELF_PROXY_AUTH_HEADER` and
  `GOBOOKSHELF_TRUSTED_PROXIES`. The header is honored only when the immediate
  peer address falls inside those CIDRs; forwarding headers such as
  `X-Forwarded-For` never confer trust, because they are attacker-controlled.
  Setting the header without any trusted CIDRs is refused at startup.
- `/metrics` answers a small Prometheus exposition and is limited to
  `GOBOOKSHELF_METRICS_ALLOW` (loopback and private ranges by default).

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
| `GET /auth/status` | Public: whether first-run setup is pending and whether SSO is offered |
| `GET /auth/me` | The signed-in account, or 401 |
| `GET /home` | Continue reading, recently added, series in progress |
| `GET /items` | The catalog, filtered by library, kind, author, series, tag or query |
| `GET /items/{id}` | One item with its people, series, tags, files, chapters and your progress |
| `GET /items/{id}/epub` | A reading manifest: spine, sizes, and where the resources live |
| `GET /items/{id}/files/{file_id}/stream` | Audio, with range requests |
| `PUT /me/progress/{item_id}` | Where you got to, per device |
| `GET|PUT /me/settings` | Reader, player and interface preferences |
| `GET /system/status` | Admin: version, database size, item counts, last scans |

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
internal/config/    YAML plus GOBOOKSHELF_* environment overrides
internal/store/     SQLite connection and embedded migrations
internal/library/   catalog queries, scanner, watcher, janitor
internal/epub/      safe archive reader, container.xml and OPF parsing
internal/audio/     ID3v2, MPEG frame and MP4 box parsing
internal/images/    cover decoding, resizing and on-disk cache
internal/auth/      accounts, sessions, tokens, OIDC, proxy mode
internal/api/       JSON handlers, media streaming, OPDS
internal/server/    router, middleware, SPA fallback
internal/remote/    the guarded outbound HTTP client
web/                embedded frontend (web/dist ships byte for byte as written)
scripts/checkweb/   static resolution check for the frontend
docs/               DESIGN.md (API and data model), FRONTEND.md (client contract)
```

## License

MIT. See [LICENSE](LICENSE).
