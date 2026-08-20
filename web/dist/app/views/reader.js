/**
 * /read/{id} - full-screen EPUB reader.
 *
 * Layout: a <foliate-view> fills the screen. Three invisible tap zones sit on
 * top: left = previous page, right = next page, center = toggle chrome. The top
 * bar has back / title / contents / settings; the bottom bar has a progress
 * slider and the "Page x of y" readout, which is also mirrored into a polite
 * live region on every relocation.
 *
 * Keyboard: Left/Right and PageUp/PageDown page, Home/End jump, Escape leaves,
 * "t" opens contents, "s" opens settings. Every control is reachable by Tab.
 */

import { api } from '../api.js';
import { store, deviceName } from '../store.js';
import { openBook, readerCSS, isDarkReader } from '../epub.js';
import { icon, iconButton } from '../components/icons.js';
import { openSheet } from '../components/sheet.js';
import { readerSettingsControls } from '../components/reader-settings.js';
import { loadingView, errorView } from '../components/states.js';
import { announce } from '../live.js';
import { navigate } from '../router.js';
import { percent } from '../format.js';

const MARGINS = { narrow: 12, normal: 36, wide: 72 };
const SAVE_DEBOUNCE_MS = 1200;

/** @param {import('../router.js').RouteCtx} ctx */
export default async function reader(ctx) {
  const el = document.createElement('div');
  el.className = 'reader';
  el.append(styleTag());

  const stage = document.createElement('div');
  stage.className = 'reader-stage';
  el.append(stage);
  stage.append(loadingView('Opening book'));

  /** @type {any} */
  let item = null;
  /** @type {any} */
  let view = null;
  /** @type {number|undefined} */
  let saveTimer;
  /** @type {{fraction:number, cfi:string}|null} */
  let last = null;
  let chromeVisible = true;

  const cleanup = () => {
    clearTimeout(saveTimer);
    saveNow();
    document.removeEventListener('keydown', onKey, true);
    window.removeEventListener('pagehide', saveNow);
    document.documentElement.removeAttribute('data-reader-theme');
    try { view?.close?.(); } catch { /* already gone */ }
  };

  /* ---------- chrome ---------- */

  const top = document.createElement('header');
  top.className = 'reader-bar reader-bar--top';
  const back = document.createElement('a');
  back.className = 'iconbtn';
  back.href = '/';
  back.setAttribute('aria-label', 'Back to library');
  back.title = 'Back';
  back.append(icon('back'));
  const title = document.createElement('h1');
  title.className = 'reader-title truncate';
  const tocBtn = iconButton('toc', 'Contents', () => openToc());
  const setBtn = iconButton('gear', 'Reading settings', () => openSettings());
  top.append(back, title, tocBtn, setBtn);

  const bottom = document.createElement('footer');
  bottom.className = 'reader-bar reader-bar--bottom';
  const slider = document.createElement('input');
  slider.type = 'range';
  slider.min = '0';
  slider.max = '1000';
  slider.step = '1';
  slider.value = '0';
  slider.setAttribute('aria-label', 'Position in book');
  const readout = document.createElement('span');
  readout.className = 'reader-readout';
  bottom.append(slider, readout);

  slider.addEventListener('change', () => {
    const f = Number(slider.value) / 1000;
    view?.goToFraction(f);
  });

  /* Tap zones. They sit under the bars so the bars stay clickable. */
  const zones = document.createElement('div');
  zones.className = 'reader-zones';
  const zPrev = zoneButton('Previous page', () => view?.prev());
  const zCenter = zoneButton('Show or hide reading controls', () => toggleChrome());
  const zNext = zoneButton('Next page', () => view?.next());
  zones.append(zPrev, zCenter, zNext);

  el.append(zones, top, bottom);

  function toggleChrome(force) {
    chromeVisible = force ?? !chromeVisible;
    el.classList.toggle('chrome-hidden', !chromeVisible);
    top.setAttribute('aria-hidden', String(!chromeVisible));
    bottom.setAttribute('aria-hidden', String(!chromeVisible));
    for (const b of [back, tocBtn, setBtn, slider]) {
      /** @type {any} */ (b).tabIndex = chromeVisible ? 0 : -1;
    }
    announce(chromeVisible ? 'Controls shown' : 'Controls hidden');
  }

  /* ---------- load ---------- */

  try {
    item = await api.item(ctx.params.id);
    title.textContent = item?.title || 'Reading';
    back.href = `/item/${encodeURIComponent(ctx.params.id)}`;

    await import('../../vendor/foliate-js/view.js');
    const book = await openBook(ctx.params.id);

    view = document.createElement('foliate-view');
    stage.replaceChildren(view);
    await view.open(book);

    view.addEventListener('relocate', (e) => onRelocate(e.detail));
    view.addEventListener('load', (e) => hardenFrame(e.detail));

    applySettings();

    const locator = item?.progress?.locator;
    const fraction = item?.progress?.percent;
    try {
      if (locator) await view.init({ lastLocation: locator });
      else {
        await view.init({ showTextStart: true });
        if (fraction) await view.goToFraction(fraction);
      }
    } catch {
      // A stale or unparseable locator must not stop the book from opening.
      await view.init({ showTextStart: true });
    }

    document.addEventListener('keydown', onKey, true);
  } catch (e) {
    stage.replaceChildren(errorView(e, () => navigate(location.pathname, { replace: true })));
    return { el, title: 'Reader', destroy: cleanup };
  }

  /* ---------- settings ---------- */

  function applySettings() {
    const s = store.reader;
    document.documentElement.setAttribute('data-reader-theme', s.theme);
    if (s.theme === 'custom') {
      document.documentElement.style.setProperty('--custom-fg', s.custom_fg);
      document.documentElement.style.setProperty('--custom-bg', s.custom_bg);
    }
    el.classList.toggle('reader-dark', isDarkReader(s));

    const r = view?.renderer;
    if (!r) return;
    r.setAttribute('flow', s.layout === 'scrolled' ? 'scrolled' : 'paginated');
    r.setAttribute('margin', `${MARGINS[s.margin] ?? MARGINS.normal}px`);
    r.setAttribute('gap', '6%');
    r.setAttribute('max-column-count', s.columns === 'auto' ? '2' : s.columns);
    r.setAttribute('max-inline-size', s.columns === '1' ? '48rem' : '38rem');
    r.setStyles?.(readerCSS(s));
  }

  function openSettings() {
    const content = readerSettingsControls(() => applySettings());
    openSheet(el, 'Reading settings', content);
  }

  function openToc() {
    const toc = view?.book?.toc || [];
    const wrap = document.createElement('div');
    if (!toc.length) {
      const p = document.createElement('p');
      p.className = 'muted';
      p.textContent = 'This book has no table of contents.';
      wrap.append(p);
    } else {
      const ul = document.createElement('ul');
      ul.className = 'linklist';
      /** @param {any[]} items @param {number} depth */
      const add = (items, depth) => {
        for (const t of items) {
          const li = document.createElement('li');
          const b = document.createElement('button');
          b.type = 'button';
          b.style.paddingLeft = `calc(var(--s3) + ${depth * 1}rem)`;
          b.textContent = t.label || 'Untitled';
          b.addEventListener('click', () => {
            sheet.close();
            view.goTo(t.href).catch(() => {});
          });
          li.append(b);
          ul.append(li);
          if (t.subitems?.length) add(t.subitems, depth + 1);
        }
      };
      add(toc, 0);
      wrap.append(ul);
    }
    const sheet = openSheet(el, 'Contents', wrap);
  }

  /* ---------- progress ---------- */

  /** @param {any} detail */
  function onRelocate(detail) {
    const fraction = detail?.fraction ?? 0;
    last = { fraction, cfi: detail?.cfi || '' };
    slider.value = String(Math.round(fraction * 1000));

    const loc = detail?.location;
    const text = loc && loc.total > 1
      ? `Page ${loc.current + 1} of ${loc.total}`
      : `${percent(fraction)} through`;
    readout.textContent = text;
    slider.setAttribute('aria-valuetext', text);
    const chapter = detail?.tocItem?.label;
    announce(chapter ? `${text}. ${chapter}` : text);

    clearTimeout(saveTimer);
    saveTimer = setTimeout(saveNow, SAVE_DEBOUNCE_MS);
  }

  function saveNow() {
    if (!item || !last) return;
    api.putProgress(item.id, {
      locator: last.cfi || undefined,
      percent: last.fraction,
      finished: last.fraction >= 0.999,
      device: deviceName(),
    }).catch(() => { /* best effort */ });
  }

  /**
   * Belt-and-braces. The frame is sandboxed to `allow-same-origin` only and
   * every document carries an injected `script-src 'none'`, but strip any
   * script node that survived the loader anyway.
   * @param {{doc:Document}} detail
   */
  function hardenFrame(detail) {
    const doc = detail?.doc;
    if (!doc) return;
    for (const s of Array.from(doc.querySelectorAll('script'))) s.remove();
    // Key events inside the book iframe do not reach the host document.
    doc.addEventListener('keydown', onKey, true);
    for (const a of Array.from(doc.querySelectorAll('a[href]'))) {
      const href = a.getAttribute('href') || '';
      if (/^(https?:)?\/\//i.test(href)) {
        a.setAttribute('target', '_blank');
        a.setAttribute('rel', 'noopener noreferrer nofollow');
      }
    }
  }

  /* ---------- keyboard ---------- */

  /** @param {KeyboardEvent} e */
  function onKey(e) {
    if (e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey) return;
    const t = /** @type {HTMLElement|null} */ (e.target);
    const typing = t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable);
    if (typing) return;
    if (el.querySelector('bs-sheet')) return;

    switch (e.key) {
      case 'ArrowLeft': case 'PageUp': view?.prev(); break;
      case 'ArrowRight': case 'PageDown': case ' ': view?.next(); break;
      case 'Home': view?.goToFraction(0); break;
      case 'End': view?.goToFraction(1); break;
      case 't': case 'T': openToc(); break;
      case 's': case 'S': openSettings(); break;
      case 'Escape': navigate(`/item/${encodeURIComponent(ctx.params.id)}`); break;
      default: return;
    }
    e.preventDefault();
  }

  window.addEventListener('pagehide', saveNow);

  return { el, title: item?.title || 'Reader', destroy: cleanup };
}

/** @param {string} label @param {() => void} onClick */
function zoneButton(label, onClick) {
  const b = document.createElement('button');
  b.type = 'button';
  b.className = 'reader-zone';
  b.setAttribute('aria-label', label);
  b.addEventListener('click', onClick);
  return b;
}

/** Reader-only CSS, scoped to this view so it never leaks into the shell. */
function styleTag() {
  const style = document.createElement('style');
  style.textContent = `
.reader {
  position: fixed;
  inset: 0;
  display: grid;
  background: var(--reader-bg);
  color: var(--reader-fg);
  overflow: hidden;
}
.reader-stage { position: absolute; inset: 0; }
.reader-stage foliate-view { display: block; width: 100%; height: 100%; }

.reader-zones {
  position: absolute;
  inset: 0;
  display: grid;
  grid-template-columns: 1fr 1.2fr 1fr;
}
.reader-zone {
  border: 0;
  background: transparent;
  cursor: pointer;
  -webkit-tap-highlight-color: transparent;
}
.reader-zone:focus-visible { outline: 3px solid var(--focus); outline-offset: -6px; }

.reader-bar {
  position: absolute;
  left: 0;
  right: 0;
  display: flex;
  align-items: center;
  gap: var(--s2);
  padding: var(--s2);
  color: var(--text);
  background: var(--surface);
  border-color: var(--border);
  transition: transform var(--motion) ease, opacity var(--motion) ease;
}
.reader-bar--top { top: 0; border-bottom: 1px solid var(--border); }
.reader-bar--bottom {
  bottom: 0;
  border-top: 1px solid var(--border);
  padding-bottom: calc(var(--s2) + env(safe-area-inset-bottom));
}
.reader-title { flex: 1; margin: 0; font-size: 1rem; font-weight: 600; }
.reader-readout {
  min-width: 8rem;
  text-align: right;
  font-variant-numeric: tabular-nums;
  font-size: 0.9rem;
  color: var(--muted);
}
.reader-bar--bottom input[type="range"] { flex: 1; }

.chrome-hidden .reader-bar--top { transform: translateY(-110%); opacity: 0; }
.chrome-hidden .reader-bar--bottom { transform: translateY(110%); opacity: 0; }

@media (max-width: 30rem) {
  .reader-bar--bottom { flex-wrap: wrap; }
  .reader-readout { width: 100%; text-align: center; }
}
`;
  return style;
}
