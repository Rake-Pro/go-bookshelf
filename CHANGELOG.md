# Changelog

All notable changes to this project are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project uses
[semantic versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

Nothing yet.

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

[Unreleased]: https://github.com/rake-pro/go-bookshelf/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/rake-pro/go-bookshelf/releases/tag/v0.1.0
