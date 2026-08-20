/**
 * Glue between the backend's extracted-EPUB resource route and the vendored
 * renderer.
 *
 * The backend serves container entries individually:
 *   GET /api/v1/items/{id}/epub/{path...}
 * so no ZIP reader ships in the browser. We construct the vendored `EPUB` class
 * directly with a fetch-backed loader instead of calling its `makeBook()`
 * helper (which would pull in fflate/zip.js).
 *
 * Book-supplied scripts are refused at the loader, and a `script-src 'none'`
 * meta CSP is injected into every (X)HTML document so inline scripts cannot run
 * either. See docs/DESIGN.md "EPUB isolation".
 */

import { epubRoot } from './api.js';

/** Weight used for a spine item whose size could not be determined. */
const FALLBACK_SIZE = 20000;
/** Beyond this many spine items, skip the size probe and weight them equally. */
const MAX_SIZE_PROBE = 400;
const PROBE_CONCURRENCY = 8;

/**
 * @param {string} itemId
 * @returns {Promise<any>} a foliate-js `EPUB` instance, already initialised
 */
export async function openBook(itemId) {
  const root = epubRoot(itemId);
  /** @param {string} path container-relative, already URL-decoded */
  const url = (path) =>
    `${root}/${String(path).split('/').map(encodeURIComponent).join('/')}`;

  /**
   * @param {string} path
   * @returns {Promise<string>} '' when the entry does not exist
   */
  const loadText = async (path) => {
    const res = await fetch(url(path), { credentials: 'same-origin' });
    if (res.status === 404) return '';
    if (!res.ok) throw new Error(`Cannot read ${path} (HTTP ${res.status})`);
    return res.text();
  };

  /** @param {string} path @returns {Promise<Blob|null>} */
  const loadBlob = async (path) => {
    const res = await fetch(url(path), { credentials: 'same-origin' });
    if (res.status === 404) return null;
    if (!res.ok) throw new Error(`Cannot read ${path} (HTTP ${res.status})`);
    return res.blob();
  };

  const sizes = await spineSizes(root, url, loadText);
  /** @param {string} href */
  const getSize = (href) => sizes.get(href) || FALLBACK_SIZE;

  const { EPUB } = await import('../vendor/foliate-js/epub.js');
  const book = new EPUB({ loadText, loadBlob, getSize });
  await book.init();

  // 1. never fetch a script resource
  book.transformTarget?.addEventListener('load', (e) => {
    if (e.detail?.isScript) e.detail.allow = false;
  });
  // 2. and forbid inline scripts inside the documents we do fetch
  book.transformTarget?.addEventListener('data', (e) => {
    const type = String(e.detail?.type || '');
    if (!/html|xml/i.test(type)) return;
    e.detail.data = Promise.resolve(e.detail.data).then(injectCSP);
  });

  return book;
}

/**
 * Insert a restrictive meta CSP as the first child of <head>.
 * @param {any} data
 * @returns {any} unchanged unless it is an (X)HTML string
 */
function injectCSP(data) {
  if (typeof data !== 'string') return data;
  const meta = '<meta http-equiv="Content-Security-Policy" '
    + 'content="script-src \'none\'; object-src \'none\'; base-uri \'none\';" />';
  const m = /<head\b[^>]*>/i.exec(data);
  if (m) return data.slice(0, m.index + m[0].length) + meta + data.slice(m.index + m[0].length);
  const html = /<html\b[^>]*>/i.exec(data);
  if (html) {
    const at = html.index + html[0].length;
    return data.slice(0, at) + '<head>' + meta + '</head>' + data.slice(at);
  }
  return data;
}

/**
 * Byte size per spine item, used to weight overall reading progress.
 *
 * First choice is `GET /api/v1/items/{id}/epub`, the reading manifest, which
 * already knows the spine and each entry's uncompressed size: one request
 * instead of one per chapter. If it is unavailable or carries no sizes, fall
 * back to probing each spine document with a HEAD request (8 in flight). A
 * backend that answers neither simply falls back to equal weighting: progress
 * stays monotonic, only the "location" estimate gets coarser.
 *
 * @param {string} root manifest URL, and the base of the resource route
 * @param {(p:string) => string} url
 * @param {(p:string) => Promise<string>} loadText
 * @returns {Promise<Map<string, number>>} keyed by container-relative href
 */
async function spineSizes(root, url, loadText) {
  /** @type {Map<string, number>} */
  const sizes = new Map();
  try {
    const res = await fetch(root, {
      credentials: 'same-origin',
      headers: { Accept: 'application/json' },
    });
    if (res.ok) {
      const manifest = await res.json();
      for (const entry of manifest?.spine || []) {
        if (entry?.href && entry.size > 0) sizes.set(normalize(decodePath(entry.href)), entry.size);
      }
      if (sizes.size) return sizes;
    }
  } catch {
    // Fall through to the HEAD probe.
  }
  try {
    const containerXml = await loadText('META-INF/container.xml');
    if (!containerXml) return sizes;
    const parser = new DOMParser();
    const container = parser.parseFromString(containerXml, 'application/xml');
    const opfPath = container.querySelector('rootfile')?.getAttribute('full-path');
    if (!opfPath) return sizes;

    const opfXml = await loadText(opfPath);
    if (!opfXml) return sizes;
    const opf = parser.parseFromString(opfXml, 'application/xml');

    const base = opfPath.includes('/') ? opfPath.slice(0, opfPath.lastIndexOf('/') + 1) : '';
    /** @type {Map<string,string>} id -> href relative to the container root */
    const manifest = new Map();
    for (const item of opf.querySelectorAll('manifest > item')) {
      const id = item.getAttribute('id');
      const href = item.getAttribute('href');
      if (id && href) manifest.set(id, normalize(base + decodeURI(href)));
    }
    /** @type {string[]} */
    const spine = [];
    for (const ref of opf.querySelectorAll('spine > itemref')) {
      const href = manifest.get(ref.getAttribute('idref') || '');
      if (href) spine.push(href);
    }
    if (!spine.length || spine.length > MAX_SIZE_PROBE) return sizes;

    let next = 0;
    const worker = async () => {
      while (next < spine.length) {
        const href = spine[next++];
        try {
          const res = await fetch(url(href), { method: 'HEAD', credentials: 'same-origin' });
          const len = Number(res.headers.get('content-length'));
          if (res.ok && Number.isFinite(len) && len > 0) sizes.set(href, len);
        } catch { /* leave it unset: falls back to FALLBACK_SIZE */ }
      }
    };
    await Promise.all(
      Array.from({ length: Math.min(PROBE_CONCURRENCY, spine.length) }, worker));
  } catch {
    // Sizing is an optimisation; never block opening a book on it.
  }
  return sizes;
}

/** decodeURI that never throws on a malformed escape. */
function decodePath(path) {
  try { return decodeURI(path); } catch { return path; }
}

/** Collapse "a/./b" and "a/../b" the way the renderer's resolver does. */
function normalize(path) {
  /** @type {string[]} */
  const out = [];
  for (const seg of path.split('/')) {
    if (seg === '.' || seg === '') continue;
    if (seg === '..') out.pop();
    else out.push(seg);
  }
  return out.join('/');
}

/**
 * Build the CSS injected into the book's iframe from the user's reader
 * settings. Kept here so the reader view stays about interaction.
 *
 * @param {import('./store.js').ReaderSettings} s
 * @returns {string}
 */
export function readerCSS(s) {
  const fg = readerColor(s, 'fg');
  const bg = readerColor(s, 'bg');
  const family = {
    system: 'system-ui, -apple-system, "Segoe UI", Roboto, Arial, sans-serif',
    serif: 'Georgia, "Iowan Old Style", "Times New Roman", Times, serif',
    sans: 'system-ui, -apple-system, "Segoe UI", Roboto, Arial, sans-serif',
    dyslexic: '"OpenDyslexic", "Lexie Readable", "Comic Sans MS", "Comic Neue", Verdana, sans-serif',
  }[s.font_family];

  const align = s.align === 'publisher' ? '' : `text-align: ${s.align} !important;`;
  const fontRule = family
    ? `font-family: ${family} !important;`
    : '';

  return `
@namespace epub "http://www.idpf.org/2007/ops";
html {
  color-scheme: ${isDarkReader(s) ? 'dark' : 'light'};
  color: ${fg};
  background: ${bg};
  font-size: ${s.font_scale}em;
  hyphens: auto;
}
body {
  color: ${fg};
  background: ${bg};
  line-height: ${s.line_height} !important;
  letter-spacing: ${s.letter_spacing}em !important;
  word-spacing: ${s.word_spacing}em !important;
  ${fontRule}
  ${align}
}
p, li, blockquote, dd, div {
  line-height: ${s.line_height} !important;
  letter-spacing: ${s.letter_spacing}em !important;
  word-spacing: ${s.word_spacing}em !important;
  ${fontRule}
  ${align}
}
p { margin-block: ${s.paragraph_spacing}em !important; }
a, a:visited { color: ${readerColor(s, 'link')}; }
img, svg, video { max-width: 100% !important; height: auto !important; }
::selection { background: ${fg}; color: ${bg}; }
`;
}

/** @param {any} s @param {'fg'|'bg'|'link'} which */
function readerColor(s, which) {
  const map = {
    light: { fg: '#1f1d1a', bg: '#faf8f4', link: '#c2561f' },
    dark: { fg: '#ece7df', bg: '#141210', link: '#e8834f' },
    sepia: { fg: '#5b4636', bg: '#f4ecd8', link: '#8c4a1f' },
    'hc-light': { fg: '#000000', bg: '#ffffff', link: '#0043a8' },
    'hc-dark': { fg: '#ffffff', bg: '#000000', link: '#ffd400' },
  };
  if (s.theme === 'custom') {
    if (which === 'fg') return s.custom_fg;
    if (which === 'bg') return s.custom_bg;
    return s.custom_fg;
  }
  return (map[s.theme] || map.light)[which];
}

/** @param {any} s */
export function isDarkReader(s) {
  if (s.theme === 'dark' || s.theme === 'hc-dark') return true;
  if (s.theme === 'custom') return luminance(s.custom_bg) < 0.5;
  return false;
}

/** @param {string} hex */
function luminance(hex) {
  const m = /^#?([0-9a-f]{6})$/i.exec(hex || '');
  if (!m) return 1;
  const n = parseInt(m[1], 16);
  const r = (n >> 16) & 255, g = (n >> 8) & 255, b = n & 255;
  return (0.2126 * r + 0.7152 * g + 0.0722 * b) / 255;
}
