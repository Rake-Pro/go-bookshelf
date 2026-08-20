# Vendored frontend dependencies

Everything under `web/dist/vendor/` is third-party source, copied verbatim at a
pinned commit, apart from the one patch recorded below. Do not edit these files
otherwise; to update, re-copy from upstream at a new commit, re-apply the patch,
and update this file.

## foliate-js

- Purpose: EPUB parsing and pagination for the reader route (`/read/{id}`).
- Upstream: https://github.com/johnfactotum/foliate-js
- Pinned commit: `78914aef4466eb960965702401634c2cb348e9b1` (2026-05-01)
- License: MIT (see `foliate-js/LICENSE`)

Only the modules required for EPUB rendering are vendored:

| File | Why |
|---|---|
| `view.js` | `<foliate-view>` custom element, the public entry point |
| `epub.js` | OPF/container parsing, spine, TOC, resource loader |
| `epubcfi.js` | CFI parse/generate, used for progress locators |
| `paginator.js` | `<foliate-paginator>` reflowable renderer (paginated + scrolled) |
| `fixed-layout.js` | `<foliate-fxl>` renderer for `pre-paginated` books |
| `progress.js` | section/TOC progress mapping |
| `overlayer.js` | annotation/selection overlay used by `view.js` |
| `text-walker.js` | text range walking used by `view.js` |
| `search.js` | in-book search, lazily imported by `view.js` |

Deliberately not vendored: `mobi.js`, `pdf.js`, `fb2.js`, `comic-book.js`,
`opds.js`, `dict.js`, `tts.js`, `footnotes.js`, `quote-image.js`, `reader.js`,
`ui/`, and `vendor/` (`fflate`, `zip.js`, `pdfjs`). The app never calls
`makeBook()`, so the ZIP readers are unreachable: the backend serves already
extracted EPUB resources over HTTP and the app constructs `EPUB` directly with
its own `loadText`/`loadBlob` loader (`app/epub.js`).

### Local patch: iframe sandbox

Upstream renders every book document in an iframe with
`sandbox="allow-same-origin allow-scripts"`. Both files below are patched to
`sandbox="allow-same-origin"`:

| File | Line |
|---|---|
| `foliate-js/paginator.js` | `#createFrame` / `View` constructor |
| `foliate-js/fixed-layout.js` | `#createFrame` |

Why the patch is safe here:

- The renderer does not run scripts of its own inside the frame. It reads and
  writes the frame through `contentDocument` **from the parent realm** - style
  injection, column layout, `ResizeObserver`, range and CFI work - all of which
  `allow-same-origin` alone permits.
- `allow-same-origin` together with `allow-scripts` is equivalent to no sandbox
  at all: a script in the frame shares this app's origin and can remove the
  sandbox attribute from its own parent. Since book content is untrusted, that
  combination contradicts the isolation contract in `docs/DESIGN.md`.

What upstream's `allow-scripts` bought, and why this app does not need it: the
comment cites WebKit bug 218086, where DOM events are not dispatched inside a
sandboxed frame that lacks `allow-scripts`. That affects the renderer's own
in-frame touch, pointer and selection listeners. This app does not rely on
them - `app/views/reader.js` overlays its own full-size tap-zone `<button>`
elements for paging, handles keys on the host document, and never exposes
in-book selection - so on WebKit the loss is confined to gestures the app
already intercepts. Initial layout runs from the frame's `load` event on the
parent side, which is unaffected.

Defence in depth remains in place either way: `app/epub.js` refuses any
JavaScript manifest item at the loader and injects
`script-src 'none'; object-src 'none'; base-uri 'none'` into every (X)HTML
document, `reader.js` strips surviving `<script>` nodes on load, and the
backend serves `/api/v1/items/{id}/epub/{path...}` with `default-src 'none';
script-src 'none'; ... sandbox`.
