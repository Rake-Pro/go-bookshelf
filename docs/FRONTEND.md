# Frontend

The PWA in `web/dist/` is served from `embed.FS` by the Go binary. There is no
build step, no bundler, no Node toolchain and no framework: plain ES modules,
Web Components and CSS custom properties, with JSDoc for types. Everything in
`web/dist/` ships byte-for-byte as written.

## Contents

- [Architecture](#architecture)
- [File map](#file-map)
- [Design tokens and theming](#design-tokens-and-theming)
- [Routing](#routing)
- [API endpoints consumed](#api-endpoints-consumed)
- [Auth probe](#auth-probe)
- [Settings keys](#settings-keys)
- [Reader](#reader)
- [Player](#player)
- [PWA](#pwa)
- [Accessibility contract](#accessibility-contract)
- [Adding a view](#adding-a-view)
- [Checking your changes](#checking-your-changes)
- [Appendix: regenerating the PNG icons](#appendix-regenerating-the-png-icons)

## Architecture

```
index.html
  |- app/tokens.css        design tokens only (colors, spacing, radius, fonts)
  |- app/app.css           shell layout + shared primitives
  `- app/main.js  (module) boot
       |- api.js           fetch wrapper, one function per endpoint
       |- store.js         user + settings, localStorage mirror, debounced PUT
       |- router.js        history API router, lazily imports views
       |- player.js        the single global <audio> controller
       |- live.js          screen reader announcements
       |- epub.js          fetch-backed loader for the vendored EPUB renderer
       |- components/      reusable UI (custom elements + builders)
       `- views/           one module per route, default-exported factory
```

Three rules keep this simple:

1. **Views own no global state.** A view is `async (ctx) => { el, title, destroy? }`.
   The router swaps `el` into the shell's `<main>` and calls `destroy()` on the
   way out. Anything that must outlive a route lives in `store` or `player`.
2. **One `<audio>` element, owned by `player.js`,** created with `new Audio()`
   and never attached to the DOM. Route changes cannot interrupt playback; the
   mini-player is a view onto the controller, not the owner of it.
3. **Nothing from a book is ever HTML.** Titles, descriptions and metadata are
   set with `textContent`. Book content is rendered inside the renderer's
   sandboxed iframe with scripts refused at the loader and again by an injected
   `script-src 'none'` meta CSP.

## File map

| Path | Purpose |
|---|---|
| `index.html` | Document head, skip link, `#app` mount point, live regions |
| `manifest.webmanifest` | PWA manifest |
| `sw.js` | Service worker (cache-first shell, network-first API) |
| `icons/` | `icon.svg`, `icon-192.png`, `icon-512.png`, `maskable-512.png` |
| `app/tokens.css` | All design tokens, light/dark/high-contrast, reader themes |
| `app/app.css` | Shell layout, buttons, forms, grids, states |
| `app/main.js` | Route table, boot sequence, shell mount, SW registration |
| `app/router.js` | Pattern matching, history API, link interception |
| `app/api.js` | `request()`, `api.*`, `auth.*`, `setup.*`, media URL builders, `ApiError` |
| `app/store.js` | `store` singleton: user, `reader`/`player`/`ui` settings, theme |
| `app/player.js` | `player` singleton: transport, chapters, MediaSession, progress |
| `app/epub.js` | HTTP loader + reader CSS generation for the vendored renderer |
| `app/live.js` | `announce()` / `alert()` into the polite and assertive regions |
| `app/format.js` | Duration, clock, percent, names, dates, bytes |
| `app/components/app-shell.js` | Sidebar, app bar, tab bar, mini-player dock |
| `app/components/icons.js` | `icon(name)`, `iconButton(name, label, fn)` |
| `app/components/states.js` | Loading, skeleton, empty, error blocks |
| `app/components/page.js` | `page(title)` scaffold and `sectionHead()` |
| `app/components/item-card.js` | `<bs-item-card>`, `itemCard()`, `itemGrid()` |
| `app/components/mini-player.js` | `<bs-mini-player>` |
| `app/components/sheet.js` | `<bs-sheet>` modal bottom sheet, `openSheet()` |
| `app/components/confirm.js` | `confirmDialog()`: a sheet-based Cancel/confirm prompt for destructive actions |
| `app/components/update-toast.js` | `<bs-update-toast>`, `showUpdateToast()`: the SW-update banner |
| `app/components/reader-settings.js` | Reader settings controls (reader + settings page) |
| `app/components/add-books.js` | `addBooksButton()`, `openAddBooks()`: the upload / URL-import sheet |
| `app/views/*.js` | One module per route |
| `vendor/foliate-js/` | Pinned EPUB renderer, MIT, see `vendor/VERSIONS.md` |

### Components

| Element | Tag | Notes |
|---|---|---|
| Item card | `bs-item-card` | Shadow DOM. One link per tile; accessible name carries title, author, kind and progress. Lazy-loaded 2:3 cover with a text fallback. |
| Mini player | `bs-mini-player` | Shadow DOM. Hidden until something is loaded, and on the loaded item's own `/listen` page. Subscribes to `player` events once, survives shell re-mounts. |
| Sheet | `bs-sheet` | Wraps `<dialog>` + `showModal()` for free focus trapping and Escape. Bottom sheet on narrow screens, centered panel from 48rem. |
| Update toast | `bs-update-toast` | Shadow DOM. Fixed, non-modal banner; shown only once a waiting service worker exists. |

`add-books.js` renders inside `bs-sheet`, so its content lives in that shadow
root and `app.css` does not reach it; the styles it needs travel with it as a
`<style>` node it appends to its own subtree.

Non-element modules (`app-shell`, `page`, `states`, `icons`, `reader-settings`)
export plain builder functions and use light DOM so `app.css` applies.

## Design tokens and theming

All color, spacing, radius and font values are CSS custom properties in
`app/tokens.css`. Never hard-code a color in a component.

- **Root font size is never set in px.** The optional interface scale is applied
  as a percentage (`html { font-size: 120% }`), so OS and browser text scaling
  still multiply on top. Everything else is `rem`/`em`.
- **App theme** resolution order: explicit `ui.theme` -> `prefers-color-scheme`.
  The resolved theme is written to `<html data-theme>`; `data-theme-source` is
  `user` or `auto`. When it is `auto`, a `prefers-contrast: more` media query
  hardens borders and mutes automatically.
- **Reader theme** is separate from the app theme and is written to
  `<html data-reader-theme>` while the reader is open:
  `light | sepia | gray | dark | hc-light | hc-dark | custom`. Each theme sets
  `--reader-bg/-fg/-link/-sel/-chrome/-border/-muted` in `tokens.css`; the
  reader view re-points the shell tokens (`--surface`, `--text`, `--border`,
  `--accent`, ...) at them so its bars and sheets match the page. The same
  colors exist once more as literals in `app/epub.js` (`READER_PALETTES`),
  because the stylesheet injected into the book iframe cannot read the host
  document's variables - change both together.
- **Reduced motion**: a global `prefers-reduced-motion` block disables every
  transition and animation.
- `<meta name="theme-color">` is rewritten whenever the theme resolves.

Spacing scale: `--s1 4px`, `--s2 8px`, `--s3 12px`, `--s4 16px`, `--s6 24px`,
`--s8 32px`. Radius `--radius: 10px`. Minimum touch target `--tap: 2.75rem`.

## Routing

`router.js` matches `location.pathname` against patterns registered in
`main.js`. `:name` captures one segment. A route registered with
`{ chrome: false }` replaces the whole shell (reader, login, setup).

| Path | View | Chrome |
|---|---|---|
| `/` | `views/home.js` | yes |
| `/library`, `/library/{id}` | `views/library.js` | yes |
| `/item/{id}` | `views/item.js` | yes |
| `/read/{id}` | `views/reader.js` | no |
| `/listen/{id}` | `views/listen.js` | yes (the mini-player hides here: the full player is the page) |
| `/authors`, `/authors/{id}` | `views/authors.js` | yes |
| `/series`, `/series/{id}` | `views/series.js` | yes |
| `/search?q=` | `views/search.js` | yes |
| `/settings` | `views/settings.js` | yes |
| `/admin`, `/admin/{section}` | `views/admin.js` | yes |
| `/admin/settings` | `views/admin-settings.js` | yes |
| `/login?next=` | `views/login.js` | no |
| `/setup` | `views/setup.js` | no (the first-run wizard) |
| anything else | `views/not-found.js` | yes |

The server must serve `index.html` for every path that is not `/api/*`,
`/opds*`, `/assets/*`, `/app/*`, `/vendor/*`, `/icons/*`, `/sw.js`,
`/manifest.webmanifest`, `/healthz`, `/readyz` or `/metrics`.

Left clicks on same-origin `<a href>` without a modifier, `target`, `download`
or an absolute scheme are intercepted and turned into `pushState`. Navigation is
generation-counted, so a slow view cannot overwrite a newer one.

Scroll restoration is manual (`history.scrollRestoration = 'manual'`): the
router stamps `{bsKey}` into every history entry's state, remembers the
scroll offset of the entry being left (bounded to the last 50 entries), and
Back/Forward restore it after the view mounts, clamped to the new page
height. Fresh navigations still start at the top; chrome-less routes are
untouched.

## API endpoints consumed

Base path `/api/v1`. Credentials are the session cookie (`same-origin`). Every
non-2xx response is expected to carry `{"error":{"code","message"}}`; a 401 on
any call other than the auth probe redirects to `/login?next=...`.

List endpoints are read as `{"items":[...],"total":n}`.

### Auth

| Call | Request | Response the frontend reads |
|---|---|---|
| `GET /auth/me` | - | `{id, username, display_name, role, libraries:[id], auth_method}` |
| `GET /auth/status` | - | `{setup_required, setup_complete, oidc_enabled, oidc_start_url, local_login, version}` |
| `POST /auth/login` | `{username, password}` | the same user object; sets the cookie |
| `POST /auth/logout` | - | 204 or `{}` |
| `GET /auth/oidc/start` | - | plain link target, full page navigation |

### First-run wizard

One call per step, all `POST /api/v1/setup/{step}`. The first two run before
anybody is signed in and opt out of the 401 redirect; the rest run as the
administrator step 2 created.

| Step | Request | Response the wizard reads |
|---|---|---|
| `token` | `{token}` | `{ok, suggested_base_url}` |
| `admin` | `{token, username, password, display_name}` | user object; sets the cookie |
| `base-url` | `{base_url}` | `{base_url, redirect_url}` |
| `oidc` | `{skip:true}` or an OIDC document with `enabled:true` | `{oidc_enabled}` |
| `library` | `{skip:true}` or `{name, kind, path}` | the library, 201 |
| `complete` | - | `{setup_complete:true}` |

Every step answers 409 once setup is finished, which is how a reload lands on
`/admin` instead of an empty form. `views/setup.js` resumes rather than
restarts: it reads `/auth/status` on mount, goes to `/admin` when setup is
complete, and starts at the base URL step when somebody is already signed in
(the one-time token has been spent by then).

### Settings (admin)

| Call | Body / response |
|---|---|
| `GET /admin/settings` | `{general, oidc, proxy_auth, metadata, metrics, setup_complete, updated_at, admin_recovery}` |
| `PUT /admin/settings` | any subset of the same sections; absent fields keep their stored values |
| `POST /admin/settings/oidc/test` | a candidate OIDC document -> `{ok, groups_claim, admin_group, user_group, redirect_url}` or `{ok:false, error}`, always 200 |

`oidc` carries `has_client_secret` and never `client_secret`: the secret can be
written but not read. Sending an empty `client_secret` keeps the stored one, so
a form that never received it can still be saved. `oidc.active` is whether
discovery actually succeeded, as distinct from `oidc.enabled`, which is only
what is stored. `admin_recovery` mirrors the environment flag, so the page can
say why the password form is still on offer after being turned off.

`views/admin-settings.js` saves each card independently, so a rejected section
cannot lose the edits in another. A save applies to the running server; the
status line under each card is an `aria-live="polite"` region.

### Catalog

| Call | Response fields used |
|---|---|
| `GET /home` | `{continue:[Item], recent:[Item], series_in_progress:[{series:{id,name}, finished, total, next_item}]}` |
| `GET /items?library=&kind=&sort=&limit=&offset=&q=&tag=` | `{items:[Item], total}` |
| `GET /items/{id}` | full `Item` (below) |
| `DELETE /items/{id}` (admin) | `{status:"deleted"}`; removes the file(s) on disk and the catalog record |
| `GET /authors?limit=` | `{items:[{id, name, item_count?}], total}` |
| `GET /authors/{id}` | `{author:{id, name, item_count}, items:[Item], total}` |
| `GET /series?limit=` | `{items:[{id, name, item_count?}], total}` |
| `GET /series/{id}` | `{series:{id, name, item_count}, items:[Item], total}`; each item's position is its own `series.sequence` |
| `GET /search?q=` | `{query, items:{items,total}, authors:{items,total}, series:{items,total}}` - one list envelope per group |
| `GET /items/{id}/cover?size=thumb\|full` | image bytes |
| `GET /items/{id}/download` | file or zip, opened as a plain link |
| `GET /items/{id}/files/{file_id}/stream` | audio with Range support |
| `GET /items/{id}/epub` | `{resource_url, container_url, spine:[{href,url,size}], progress, ...}` |
| `GET /items/{id}/epub/{path...}` | one entry of the container, addressed relative to the **container root** |

`Item` as consumed by the frontend:

```json
{
  "id": "string",
  "kind": "ebook|audiobook",
  "title": "string",
  "subtitle": "string|null",
  "description": "string|null",
  "language": "string|null",
  "published": "RFC3339 or YYYY-MM-DD|null",
  "publisher": "string|null",
  "duration_ms": 0,
  "size_bytes": 0,
  "cover_url": "/api/v1/items/1/cover",
  "authors":   ["string"],
  "narrators": ["string"],
  "series":  { "id": 1, "name": "string", "sequence": 1.0 },
  "people":  [{ "id": 1, "name": "string", "role": "author|narrator|translator", "seq": 0 }],
  "tags":    [{ "id": 1, "name": "string" }],
  "files":   [{ "id": 1, "seq": 0, "format": "m4b", "duration_ms": 0, "size_bytes": 0,
                "filename": "string", "stream_url": "string" }],
  "chapters":[{ "file_id": 1, "seq": 0, "title": "string", "start_ms": 0, "end_ms": 0 }],
  "progress":{ "locator": "epubcfi(...)", "position_ms": 0, "percent": 0.0,
               "finished": false, "finished_at": "", "device": "web-ab12cd",
               "updated_at": "RFC3339" }
}
```

`people`, `tags`, `files` and `chapters` are on `GET /items/{id}` only. List
payloads (`GET /items`, `GET /home`) carry the flat `authors` / `narrators`
name arrays instead, which is why `format.js` `peopleOf()` reads either shape
and `seriesOf()` normalises `series` to an array.

Notes for the backend:

- **Grid items need `progress`.** `GET /items` and `GET /home` entries should
  carry at least `{percent, finished_at}` so cards can show a progress bar; the
  card degrades gracefully if it is absent.
- **`chapters[].start_ms` / `end_ms` are relative to their own file**, and the
  backend guarantees it. The frontend adds the sum of preceding
  `files[].duration_ms` (ordered by `seq`) to get the absolute position, which
  is what `position_ms` uses. `chapters` is flattened across files in play
  order and each entry names its `file_id`; the same list is also nested under
  `files[].chapters`.
- **`files[].duration_ms` must be populated** for multi-file audiobooks, or the
  absolute position mapping cannot be built. Real durations from `loadedmetadata`
  refine the active file at runtime, but the initial map comes from the API.
- **EPUB resource paths are relative to the container root.** `app/epub.js`
  hands foliate-js a fetch-backed loader, and foliate resolves every manifest
  href against the OPF's own location, so the paths it asks for look like
  `META-INF/container.xml` and `OEBPS/chapter1.xhtml`. A backend that resolved
  them relative to the OPF *directory* would 404 the very first request.
- **Spine sizes come from `GET /items/{id}/epub`.** The manifest publishes
  `spine[].size`, which the reader uses to weight reading progress in one
  request. If it is missing, the reader falls back to probing each spine
  document with **HEAD** and reading `Content-Length` (8 concurrent, skipped
  past 400 items), and if that fails too, to equal weights.

### User state

| Call | Body |
|---|---|
| `GET /me/settings` | `{reader:{...}, player:{...}, ui:{...}}` |
| `PUT /me/settings` | partial: only the groups that changed, merged server-side |
| `PUT /me/progress/{item_id}` | `{locator?, position_ms?, percent, finished, device}` |
| `GET /me/bookmarks?item={id}` | `{items:[{id, item_id, position_ms, locator, note, created_at}]}` |
| `POST /me/bookmarks` | `{item_id, position_ms?, locator?, note?}` |
| `DELETE /me/bookmarks/{id}` | - |
| `GET /me/imports` | `{items:[{id, url, status, message, item_id, created_at, updated_at}]}` newest first |

`PUT /me/settings` receives a **partial** document. Sending only
`{"reader":{"font_scale":1.3}}` must not clear `player` or `ui`.

### Admin

| Call | Body / response |
|---|---|
| `GET /libraries` | `{items:[{id, name, kind, paths:[string]}]}` |
| `POST /libraries` | `{name, kind, paths:[string]}` |
| `PATCH /libraries/{id}` | partial library |
| `DELETE /libraries/{id}` | - |
| `POST /libraries/{id}/scan` | `{scan_id}` |
| `GET /libraries/{id}/scans` | `{items:[{id, started_at, finished_at, added, updated, removed, errors}]}` newest first |
| `GET /users` | `{items:[{id, username, display_name, role, disabled_at, oidc_linked}]}`; `oidc_linked` is derived from `oidc_subject`, never the subject itself |
| `POST /users` | `{username, display_name, password, role, can_upload?}`; `password` may be omitted/blank to pre-create an SSO-only account |
| `PATCH /users/{id}` | partial user; `username` (see below), `display_name`, `password` (admin-initiated reset), `role`, `disabled` |
| `DELETE /users/{id}` | `{status:"deleted"}`; 400 on your own account or the last administrator |
| `GET /users/{id}/libraries` | `{user_id, libraries:[id]}` |
| `PUT /users/{id}/libraries` | `{libraries:[id]}` |
| `GET /system/status` | `{version, db_driver, db_dsn (redacted), db_size_bytes (0 on Postgres), counts:{ebooks, audiobooks}, libraries, users, last_scans, oidc_enabled, local_login, settings_updated_at, base_url, ...}` |

The scan button polls `GET /libraries/{id}/scans` every 2 s for up to 2 minutes
and reads the newest entry; a row with `finished_at: null` renders as running.

Each user's Edit disclosure reads `GET /system/status`'s `local_login` and
`oidc_enabled` (fetched alongside `GET /users` and `GET /libraries` when the
Users panel loads) plus the row's own `role`/`disabled_at`/`oidc_linked` to
decide what to grey out - or, for password sign-in, leave out entirely -
rather than let the save come back refused:
- **Reset password** is not rendered at all when `local_login` is false, and
  neither is its label or hint: the capability is off for the whole
  deployment, not a per-account exception, so the grey-not-error rule (a
  control someone could reasonably expect to use) does not apply the same
  way it does to username/role/disabled below - there is nothing to grey,
  only something to omit, and the field must not leave a labeled gap where
  it would have been.
- **Username** is disabled, with a hint, when `oidc_enabled` is true and the
  row's `oidc_linked` is false - see docs/DESIGN.md's "Username rename".
- **Role** and **Disabled** are each disabled, with a hint naming which,
  on your own row (unconditionally) and on the row that is currently the
  last enabled administrator (`role === 'admin' && !disabled_at`, and no
  other row satisfies the same test).
The **Add user** form's password field is the same: omitted, not greyed,
under the same `local_login` check, and an account is created by username
alone for single sign-on to adopt on its first login.

### Adding books

| Call | Body / response |
|---|---|
| `POST /libraries/{id}/upload` | `multipart/form-data`: `subdir` field first, then one or more `files` parts. 201 `{status:"complete"|"scanning", files:[{filename, kind, title, author, size_bytes, item_id}]}` |
| `POST /libraries/{id}/import` | `{url}` -> 202 `{id, status, url, message, item_id}` |
| `GET /imports/{id}` | the job |
| `DELETE /imports/{id}` | cancel a queued or running job, or clear a finished one |

The button is built by `addBooksButton()`, which answers `null` unless
`store.canUpload` - that is, unless `GET /auth/me` said `can_upload: true`. The
server folds the role into the flag before answering, so the frontend has one
boolean to read and no rule to reimplement. Returning `null` rather than a
disabled button is deliberate: there is nothing the user could do to make it
work, so offering it would be a lie.

The sheet has two tabs (`role="tablist"`, arrow keys, Home and End move between
them; each panel is a `tabpanel` labelled by its tab):

- **Upload files.** A drop zone that is also a real button for the keyboard,
  plus a file picker whose `accept` follows the chosen library's kind. The whole
  selection goes in **one** request, because the server groups an audiobook's
  files into one book by their tags and can only do that if it sees them
  together. Progress comes from `uploadBooks()` in `api.js`, which is the one
  call that uses `XMLHttpRequest` rather than `fetch`: `fetch` cannot report how
  much of a request body has been sent, and an upload with no progress bar is
  indistinguishable from a hung one when the file is an audiobook. Per-file bars
  are derived from the single upload event by scaling the wire total against the
  sum of the real file sizes, which keeps them honest without modelling the
  multipart encoding. `uploadBooks()` returns `{promise, cancel}`, and closing
  the sheet aborts the transfer rather than orphaning it.
- **From a URL.** A field, a submit, and the job list, re-read every 2 s while
  anything is queued or running and then left alone. Each row can be cancelled.
  A job that reaches `done` links to the item it produced.

Both panels announce their outcomes through `announce()` into the polite live
region, and the job list only speaks when a job actually changes state, so it
does not repeat itself every two seconds.

## Auth probe

**The probe is two calls, and the second one only when nobody is signed in.**
There is no `/auth/providers` endpoint.

```
GET /api/v1/auth/me
200 -> {"id":"...","username":"...","display_name":"...","role":"admin","libraries":[...]}
401 -> {"error":{"code":"unauthorized","message":"authentication required"}}

GET /api/v1/auth/status            (public; only called after the 401)
200 -> {"setup_required": false, "setup_complete": true, "oidc_enabled": true,
        "oidc_start_url": "/api/v1/auth/oidc/start", "local_login": true,
        "version": "0.1.0"}
```

- `/auth/me` stays a pure "who am I". Its 401 body carries the standard error
  envelope and nothing else, so no capability information leaks onto a route
  that exists to answer one question.
- `oidc_enabled` is true only when single sign-on is configured and its
  discovery succeeded. It is the only thing that renders the "Sign in with SSO"
  button, which is a plain link to `oidc_start_url`.
- `local_login` is whether the password form may be shown. It is false only when
  an administrator turned it off, and it is forced back to true by
  `GOBOOKSHELF_ADMIN_RECOVERY` on the server. When it is false the form is
  hidden and the SSO button moves above the fold; when both are false the page
  says sign-in is unavailable rather than showing a form that cannot work.
- `setup_required` is true until the wizard finishes (`setup_complete` is its
  positive form). On boot it sends the user to `/setup`; `/login` also links to
  it.
- The flags degrade safely when the status call fails: `setup_required` and
  `oidc_enabled` to false, `local_login` to true, so an unreachable or older
  backend leaves a usable password form instead of a blank page.

`/auth/status` is the only 401-adjacent call the frontend inspects; every other
401 simply redirects to `/login?next=...`. `/login` and `/setup` are excluded
from that redirect.

**`403 setup_required`** is the other global redirect. While the wizard is
unfinished the server refuses every ordinary `/api/v1` route with that code;
`api.js` turns it into a navigation to `/setup`, the same way a 401 becomes a
navigation to `/login`. Both handlers are installed by `main.js` so `api.js`
never has to import the router.

## Settings keys

Stored server-side in `user_settings` and mirrored to `localStorage` under
`bookshelf.settings.v1` (theme and font scale apply before the network answers).
Writes are merged and debounced 500 ms, and flushed on `pagehide`. A failed
flush merges the unsent patch back under any newer edits and retries once
after 5 s; after that it waits for the next edit or `pagehide`.

### `reader`

| Key | Type | Default | Range / values |
|---|---|---|---|
| `font_scale` | number | `1.15` | 0.7-2.5 step 0.05 |
| `font_family` | string | `publisher` | `publisher\|system\|serif\|sans\|dyslexic` |
| `line_height` | number | `1.6` | 1.0-3.0, 1.2-2.4 in the UI |
| `letter_spacing` | number (em) | `0` | -0.05-0.3 step 0.01 |
| `word_spacing` | number (em) | `0` | 0-1 step 0.05 |
| `paragraph_spacing` | number (em) | `0` | 0-3 step 0.25 |
| `margin` | string | `normal` | `narrow\|normal\|wide` (x0.6 / x1 / x1.6) |
| `align` | string | `publisher` | `publisher\|left\|justify` |
| `theme` | string | `light` | `light\|sepia\|gray\|dark\|hc-light\|hc-dark\|custom` |
| `custom_fg` | string | `#1f1d1a` | hex, used when `theme=custom` |
| `custom_bg` | string | `#faf8f4` | hex, used when `theme=custom` |
| `layout` | string | `paginated` | `paginated\|scrolled` |
| `columns` | string | `auto` | `auto\|1\|2` |

No font files are bundled. `dyslexic` maps to a system fallback stack
(`OpenDyslexic`, `Lexie Readable`, `Comic Sans MS`, `Comic Neue`, Verdana) and
degrades to the system sans if none are installed.

### `player`

| Key | Type | Default | Range / values |
|---|---|---|---|
| `speed` | number | `1.0` | 0.5-3.0 step 0.05 |
| `skip_back_s` | number | `15` | 5 / 10 / 15 / 30 |
| `skip_fwd_s` | number | `30` | 15 / 30 / 45 / 60 |
| `sleep_timer_min` | number\|null | `null` | 15 / 30 / 45 / 60 |
| `sleep_end_of_chapter` | boolean | `false` | |
| `volume_boost` | boolean | `false` | stored only; not yet applied |

### `ui`

| Key | Type | Default | Values |
|---|---|---|---|
| `theme` | string | `auto` | `auto\|light\|dark\|hc-light\|hc-dark` |
| `text_scale` | number | `1.0` | 1.0-1.6 step 0.05, applied as a percentage |

Both groups are stored and range-clamped server-side; a value outside its range
comes back snapped to the nearest valid step rather than rejected.

A per-device id (`bookshelf.device`, e.g. `web-9fk2ab`) is generated in
`localStorage` and sent as `device` on every progress write. It is not a name.

## Reader

`/read/{id}` renders `<foliate-view>` from the vendored renderer full-screen.
`app/epub.js` builds the book with a fetch-backed loader against
`GET /api/v1/items/{id}/epub/{path...}`, so no ZIP reader ships in the browser
and `makeBook()` is never called. The frame is sandboxed to `allow-same-origin`
only; upstream's `allow-scripts` is patched out (see `vendor/VERSIONS.md`).

Hardening, in three layers:

1. The loader refuses any manifest item whose media type is JavaScript.
2. Every (X)HTML document gets `<meta http-equiv="Content-Security-Policy"
   content="script-src 'none'; object-src 'none'; base-uri 'none'">` injected
   into `<head>` before it becomes a blob URL, which also kills inline scripts.
3. On `load`, any surviving `<script>` node is removed and external links get
   `target="_blank" rel="noopener noreferrer nofollow"`.

Layout. `views/reader.js` never touches the layout engine; it only sets
documented `<foliate-paginator>` attributes, recomputed on open, on a settings
change, on resize and on every section load:

| Attribute | Value |
|---|---|
| `flow` | `paginated`, or `scrolled` for the scrolled reading mode |
| `max-inline-size` | `38em x font_scale`, **in px** - the renderer parses this value as a number of pixels, so a `rem` value would be read as that many pixels |
| `max-column-count` | `2` only when the viewport is landscape and at least 1100px wide (the renderer already forces one column in portrait), `1` otherwise and always while a cover or title page is showing |
| `max-block-size` | the viewport height, so the text block fills it instead of stopping at the renderer's 1440px default |
| `gap` | `7% x margin factor`, clamped to 3.5-14% |
| `margin` | the running-head band, `5.5% of the viewport height x margin factor` clamped to 30-72px, or `3% x factor` clamped to 8-72px on viewports under 700px tall |
| `animated` | set unless `prefers-reduced-motion` |

The margin factor is 0.6 / 1 / 1.6 for `narrow` / `normal` / `wide`. A section
whose text is shorter than 400 characters is treated as a cover, title page or
part divider and laid out as one centered column; the decision is made in the
renderer's `load` event, which fires before the page is measured.

Interaction:

- Chrome: the top bar and footer float over the page. They hide two seconds
  after the book opens, on a keyboard page turn, and immediately on a tap or
  swipe page turn. A tap in the center zone toggles them and keeps them up; any
  key brings them back; `focusin` on a bar cancels the timer and `focusout`
  restarts it. Hidden means `visibility: hidden` plus `inert` and
  `aria-hidden="true"` - never removed from the DOM.
- Tap zones: left 18% / center 1fr / right 18%. They are real `<button>`s with
  labels, so they are keyboard reachable and screen-reader legible, but only
  the two edge gutters accept pointer events; the center passes touches
  through to the book, so in-book links and text selection work and the
  vendored paginator handles finger-tracked drags. Center taps (detected on
  the stage and on each book document, with 10px slop) toggle the chrome. A
  horizontal drag of 45px or more on an edge gutter pages and suppresses the
  click that follows it. The zones are removed in the scrolled reading mode,
  where they would otherwise swallow scrolling, and the chrome then stays up.
- Top bar: back, title, an audiobook play/pause (present only while audio is
  loaded), contents, settings. Footer: position slider, then the
  pages left in the current chapter (or the chapter title when the renderer has
  no page count) and the "Page x of y" readout, which falls back to
  "N% through" when the renderer cannot supply a location count.
- Keys: Left/PageUp previous, Right/PageDown/Space next, Home/End jump,
  `t` contents, `s` settings, Escape back to the item. The handler is attached
  to both the host document and each loaded book document, because key events
  inside the iframe do not bubble out.
- Announcements are throttled to one every 1.5 s and never repeat the same
  string, so holding a page key does not flood the live region.
- Settings changes call `renderer.setStyles(readerCSS(settings))` and reapply
  the attributes above. The book is never rewritten. Reading settings open in a
  sheet that docks to the side from 900px wide, so the page previews the change.
- Progress: every relocation is debounced 1.2 s and pushed as
  `PUT /me/progress/{id}` with `locator` (EPUB CFI) and `percent`. Also flushed
  on `pagehide` and on leaving the route.

## Player

`app/player.js` is a singleton `EventTarget`. Events: `load`, `state`, `time`,
`chapter`, `speed`, `sleep`, `ended`.

- Multi-file sequencing: `files` sorted by `seq` become tracks with cumulative
  offsets. `player.position` is always absolute ms. Reaching the end of a file
  loads the next and continues.
- The position slider is chapter-scoped when the book has two or more
  chapters: min/max follow the current chapter's absolute bounds (updated on
  the `chapter` event, held while dragging and applied on release), so a
  finger has seconds-per-pixel precision instead of minutes. The times row
  keeps whole-book elapsed/remaining plus a compact chapter readout;
  `aria-valuetext` names both scopes. With 0-1 chapters it scrubs the whole
  book as before.
- `MediaSession`: metadata (chapter title, author, "read by" narrator, cover
  artwork) plus `play`, `pause`, `stop`, `seekbackward`, `seekforward`,
  `previoustrack`/`nexttrack` (previous/next chapter) and `seekto`, with
  `setPositionState` on every tick.
- Progress writes: every 15 s of wall-clock while playing, on pause, on chapter
  change, on `visibilitychange` to hidden and on `pagehide`. The last two use
  `fetch(..., {keepalive: true})` so they survive unload.
- Sleep timer: a clock timer (15/30/45/60) or end-of-chapter; both pause and
  announce.
- The mini-player is rendered by the shell, not by the player view, so audio
  keeps running while browsing.

## PWA

- `manifest.webmanifest`: name and short name "Bookshelf", `standalone`, scope
  `/`, background and theme `#faf8f4`, SVG + 192/512 PNG icons plus a maskable
  512. `icons/icon.svg` is the master artwork; the PNGs are checked in and were
  produced by the standard-library-only generator in
  [Appendix: regenerating the PNG icons](#appendix-regenerating-the-png-icons)
  (no imaging dependency, no external assets).
- `sw.js`: `VERSION` constant at the top busts every cache on activate.
  - cache-first: `/app/*`, `/vendor/*`, `/assets/*`, `/icons/*`, manifest
  - cache-first with a 400-entry cap: `/api/v1/items/{id}/cover`
  - network-only with a JSON `offline` error: everything else under `/api/*`
  - network-first with the cached app shell as fallback: navigations
  - never intercepted: `/stream`, `/download`, `/epub/` (Range requests and
    large per-book resources must reach the server)
  - offline fallback page is inlined in the worker

Bump `VERSION` in `sw.js` whenever anything in `web/dist/` changes.

Update handoff: a new worker installs and precaches in the background but
never calls `skipWaiting()` itself, so it never swaps out from under a page
mid-session - an audiobook playing in the mini-player must not be interrupted
by a deploy. `main.js` watches the registration (`updatefound` /
`statechange`, plus a `waiting` worker already present at load) and shows
`<bs-update-toast>` once a worker is waiting with an existing controller (a
first-ever install has no controller and shows nothing). Clicking Refresh
posts `{type:'SKIP_WAITING'}` to the waiting worker; the worker's message
handler calls `self.skipWaiting()`, `clients.claim()` on `activate` hands it
every open tab, and a one-shot `controllerchange` listener reloads the page -
guarded so it cannot fire more than once.

## Accessibility contract

Treated as acceptance criteria, not polish:

- Skip link to `#main`; `<main>` is focused on every route change.
- Landmarks: `header` app bar, `nav` (Main / Sections), `main`, mini-player is a
  labelled `region`.
- Every icon-only control has `aria-label` and `title`; every SVG is
  `aria-hidden`.
- Minimum target 2.75rem (44 px) via `--tap` on `.btn`, `.iconbtn`, tab bar
  links, list rows, inputs and range tracks.
- `:focus-visible` draws a 3 px `--focus` ring with 2 px offset, on shadow DOM
  controls too.
- Live regions in `index.html`: `#live-polite` (player state, "Page x of y",
  settings changes, search result counts) and `#live-assertive`.
- Sliders carry `aria-valuetext` in human terms ("1 hour 24 minutes of 9 hours",
  "130 percent") rather than raw numbers.
- Full keyboard operation in reader and player (see above); modal sheets use
  `<dialog>` `showModal()` for focus trapping and Escape.
- `prefers-reduced-motion` disables all transitions; `prefers-contrast: more`
  hardens the palette when the theme is `auto`; `prefers-color-scheme` is the
  default source of the theme.
- No state is signalled by color alone: progress bars pair with text, error and
  success blocks pair an icon and a heading with the color.
- The setup wizard moves focus to the new step's heading (`tabindex="-1"`) and
  then to its first control, so a step change is announced rather than silently
  swapped; the step counter is real text, not a graphic.
- Every field in the wizard and on the settings page is a real `<label>` wrapping
  its control, with the explanation in a `.hint` inside the same label. Checkbox
  rows use `.check`, which carries the 44 px minimum height.
- The "Test connection" verdict and each settings card's save status are
  `role="status" aria-live="polite"` regions, and they pair an icon with the
  colour so a red line is never the only signal.

## Adding a view

1. Create `web/dist/app/views/my-view.js`:

   ```js
   import { page } from '../components/page.js';

   /** @param {import('../router.js').RouteCtx} ctx */
   export default async function myView(ctx) {
     const { el, body } = page('My view');
     body.textContent = ctx.params.id;
     return { el, title: 'My view' };
   }
   ```

   Return `{ el, title, destroy? }`. Add `destroy()` if you attach listeners
   outside `el` (document, window, `player`, `store`).

2. Register it in `app/main.js`:

   ```js
   .add('/my-view/:id', () => import('./views/my-view.js'))
   ```

   Pass `{ chrome: false }` for a full-screen route.

3. Add a nav entry in `app/components/app-shell.js` (`NAV`) if it belongs in the
   sidebar; set `tab: true` to also place it in the mobile tab bar, `admin: true`
   to show it to admins only.

4. Use `loadingView()` / `emptyView()` / `errorView()` from
   `components/states.js` for the three non-happy paths. Every view must handle
   all three.

5. Add the module path to `SHELL` in `sw.js` only if it is needed for the very
   first paint; lazily loaded views are cached on first use.

6. Bump `VERSION` in `sw.js`.

## Checking your changes

There is no Node toolchain and none is required to run the app.

The check that runs in CI needs no Node at all:

```
make checkweb          # or: go run ./scripts/checkweb
```

It walks every module under `web/dist`, resolves each import specifier on disk,
verifies that every named import is actually exported by the module it comes
from, and checks that `index.html`, `manifest.webmanifest` and the service
worker's precache list only reference files that exist. `make smoke` runs it
too, after driving the whole API contract end to end.

If a real parser is wanted as well, any ES-module-aware one works; the checks
used while writing this were:

```
# every module parses
esbuild --target=es2022 --outfile=/dev/null web/dist/app/**/*.js

# the whole import graph resolves and every named import exists
esbuild --bundle web/dist/app/main.js --outfile=/dev/null --format=esm
```

The bundle check reports seven unresolved dynamic imports inside
`vendor/foliate-js/view.js` (`mobi.js`, `pdf.js`, `fb2.js`, `comic-book.js`,
`tts.js`, `vendor/fflate.js`, `vendor/zip.js`). Those live in `makeBook()` and
`initTTS()`, which this app never calls, so they are never evaluated at runtime.
They are the reason those files are not vendored - see `vendor/VERSIONS.md`.

To eyeball the UI without the Go binary, serve the directory statically. Only
`/` works that way (no API, no history fallback):

```
python3 -m http.server 8080 --directory web/dist
```

## Appendix: regenerating the PNG icons

`icons/icon.svg` is authoritative. The PNG variants are produced by this
script, which uses only the Python standard library (`zlib` + `struct`), so
no imaging package is needed anywhere in the toolchain. Save it somewhere
outside the repo and run `python3 mkicons.py web/dist/icons`; keep the
colors in step with `--accent` in `app/tokens.css`.

```python
"""Generate the PNG app icons from a tiny hand-rolled rasteriser.

No third-party imaging library is available, so this writes PNGs directly
(zlib + struct only, both in the Python standard library).
"""
import struct, zlib, sys

ACCENT = (0xC2, 0x56, 0x1F)
WHITE = (0xFF, 0xFF, 0xFF)


def rounded_rect_mask(w, h, x0, y0, x1, y1, r):
    def inside(x, y):
        if x < x0 or x > x1 or y < y0 or y > y1:
            return False
        for cx, cy in ((x0 + r, y0 + r), (x1 - r, y0 + r), (x0 + r, y1 - r), (x1 - r, y1 - r)):
            if (x < x0 + r or x > x1 - r) and (y < y0 + r or y > y1 - r):
                if ((x - cx) ** 2 + (y - cy) ** 2) ** 0.5 > r and \
                   abs(x - cx) <= r + 1 and abs(y - cy) <= r + 1:
                    return False
        return True
    return inside


def draw(size, maskable=False):
    px = [[(0, 0, 0, 0)] * size for _ in range(size)]
    s = size / 512.0
    radius = 0 if maskable else 96 * s
    inside = rounded_rect_mask(size, size, 0, 0, size - 1, size - 1, radius)
    for y in range(size):
        for x in range(size):
            if inside(x, y):
                px[y][x] = ACCENT + (255,)

    # Book glyph: two pages meeting at a spine, plus a shelf bar.
    pad = (0.22 if maskable else 0.16) * size
    top = pad
    bot = size - pad
    left = pad
    right = size - pad
    spine = size / 2
    stroke = max(2, int(round(0.05 * size)))
    shelf_y = bot - stroke // 2

    def hline(y, xa, xb, t=stroke):
        for yy in range(int(y - t / 2), int(y + t / 2) + 1):
            for xx in range(int(xa), int(xb) + 1):
                if 0 <= xx < size and 0 <= yy < size:
                    px[yy][xx] = WHITE + (255,)

    def vline(x, ya, yb, t=stroke):
        # extend by half a stroke so corners meet squarely
        for xx in range(int(x - t / 2), int(x + t / 2) + 1):
            for yy in range(int(ya - t / 2), int(yb + t / 2) + 1):
                if 0 <= xx < size and 0 <= yy < size:
                    px[yy][xx] = WHITE + (255,)

    body_bottom = bot - 2.2 * stroke
    # outer edges
    vline(left, top, body_bottom)
    vline(right, top, body_bottom)
    vline(spine, top, body_bottom)
    hline(top, left, right)
    hline(body_bottom, left, right)
    # page lines
    for frac in (0.33, 0.55, 0.77):
        y = top + (body_bottom - top) * frac
        hline(y, left + 2.2 * stroke, spine - 2.2 * stroke, max(2, stroke // 2))
        hline(y, spine + 2.2 * stroke, right - 2.2 * stroke, max(2, stroke // 2))
    # shelf
    hline(shelf_y, left, right, stroke)
    return px


def write_png(path, px):
    size = len(px)
    raw = bytearray()
    for row in px:
        raw.append(0)
        for r, g, b, a in row:
            raw += bytes((r, g, b, a))
    def chunk(tag, data):
        c = struct.pack('>I', len(data)) + tag + data
        return c + struct.pack('>I', zlib.crc32(tag + data) & 0xFFFFFFFF)
    png = b'\x89PNG\r\n\x1a\n'
    png += chunk(b'IHDR', struct.pack('>IIBBBBB', size, size, 8, 6, 0, 0, 0))
    png += chunk(b'IDAT', zlib.compress(bytes(raw), 9))
    png += chunk(b'IEND', b'')
    open(path, 'wb').write(png)


out = sys.argv[1]
write_png(f'{out}/icon-192.png', draw(192))
write_png(f'{out}/icon-512.png', draw(512))
write_png(f'{out}/maskable-512.png', draw(512, maskable=True))
print('icons written')
```
