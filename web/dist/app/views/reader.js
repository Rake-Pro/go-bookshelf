/**
 * /read/{id} - full-screen EPUB reader.
 *
 * Layout: a <foliate-view> fills the screen. Three invisible tap zones sit on
 * top: left = previous page, right = next page, center = toggle chrome. A slim
 * top bar (back / title / contents / settings) and footer (progress slider,
 * "Page x of y", pages left in the chapter) float over the page and hide
 * themselves ~2 s after the book opens and on every page turn, so the text gets
 * the whole viewport. They are never removed from the DOM: hiding sets `inert`
 * plus `visibility: hidden`, and focus or a key press brings them straight back.
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

const SAVE_DEBOUNCE_MS = 1200;
/** How long the chrome stays up before it gets out of the way again. */
const CHROME_HIDE_MS = 2000;
/** A page turn every 300 ms must not produce a live-region message each time. */
const ANNOUNCE_MS = 1500;

/** Side margin and running-head band, relative to the "normal" setting. */
const MARGIN_FACTORS = { narrow: 0.6, normal: 1, wide: 1.6 };
/** Target measure of one column, in ems of the reader's own font size. */
const COLUMN_EM = 38;
/** Two columns need at least this much width, and a landscape viewport. */
const TWO_COLUMN_MIN_PX = 1100;
/** A section with less text than this is a cover or a title page. */
const SINGLE_PAGE_CHARS = 400;
const SWIPE_MIN_PX = 45;

const reduceMotion = () => window.matchMedia?.('(prefers-reduced-motion: reduce)').matches;

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
  /** @type {number|undefined} */
  let hideTimer;
  /** @type {number|undefined} */
  let resizeTimer;
  /** @type {number|undefined} */
  let announceTimer;
  let lastAnnounced = '';
  /** @type {{fraction:number, cfi:string}|null} */
  let last = null;
  let chromeVisible = true;
  /** True while a cover or title page is showing: it gets one centered page. */
  let singlePageSection = false;

  const cleanup = () => {
    clearTimeout(saveTimer);
    clearTimeout(hideTimer);
    clearTimeout(resizeTimer);
    clearTimeout(announceTimer);
    saveNow();
    document.removeEventListener('keydown', onKey, true);
    window.removeEventListener('pagehide', saveNow);
    window.removeEventListener('resize', onResize);
    document.documentElement.removeAttribute('data-reader-theme');
    store.applyTheme();
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
  slider.className = 'reader-slider';
  slider.min = '0';
  slider.max = '1000';
  slider.step = '1';
  slider.value = '0';
  slider.setAttribute('aria-label', 'Position in book');
  const info = document.createElement('div');
  info.className = 'reader-info';
  const context = document.createElement('span');
  context.className = 'reader-context truncate';
  const readout = document.createElement('span');
  readout.className = 'reader-readout';
  info.append(context, readout);
  bottom.append(slider, info);

  slider.addEventListener('change', () => {
    const f = Number(slider.value) / 1000;
    view?.goToFraction(f);
  });

  /* Tap zones. They sit under the bars so the bars stay clickable. */
  const zones = document.createElement('div');
  zones.className = 'reader-zones';
  const zPrev = zoneButton('Previous page', () => turn(-1));
  const zCenter = zoneButton('Show or hide reading controls', () => setChrome(!chromeVisible));
  const zNext = zoneButton('Next page', () => turn(1));
  zones.append(zPrev, zCenter, zNext);
  addSwipe(zones);

  el.append(zones, top, bottom);

  for (const bar of [top, bottom]) {
    // Tabbing into the chrome must keep it up; leaving it starts the clock.
    bar.addEventListener('focusin', () => setChrome(true));
    bar.addEventListener('focusout', () => {
      if (!bar.contains(document.activeElement)) setChrome(true, { auto: true });
    });
  }

  /**
   * @param {boolean} visible
   * @param {{auto?:boolean, quiet?:boolean}} [opts] auto: hide again shortly
   */
  function setChrome(visible, opts = {}) {
    clearTimeout(hideTimer);
    // Scrolled mode has no tap zones, so the bars are the only controls.
    if (!visible && store.reader.layout === 'scrolled') visible = true;
    const changed = visible !== chromeVisible;
    chromeVisible = visible;
    el.classList.toggle('chrome-hidden', !visible);
    for (const bar of [top, bottom]) {
      bar.toggleAttribute('inert', !visible);
      bar.setAttribute('aria-hidden', String(!visible));
    }
    if (visible && opts.auto) {
      // The automatic hide is silent: a page turn must not narrate twice.
      hideTimer = setTimeout(() => setChrome(false, { quiet: true }), CHROME_HIDE_MS);
    }
    if (changed && !opts.quiet) {
      announce(visible ? 'Reading controls shown' : 'Reading controls hidden');
    }
  }

  /** @param {-1|1} dir @param {{keyboard?:boolean}} [opts] */
  function turn(dir, opts = {}) {
    if (opts.keyboard) setChrome(true, { auto: true, quiet: true });
    else setChrome(false, { quiet: true });
    if (dir < 0) view?.prev();
    else view?.next();
  }

  /* ---------- load ---------- */

  // The book is opened only after this view is in the document: the
  // renderer's iframe has no browsing context while detached, so opening it
  // before the router mounts the element leaves the reader stuck.
  let started = false;
  async function start() {
    if (started) return;
    started = true;
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
      view.addEventListener('load', (e) => onSectionLoad(e.detail));

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
      window.addEventListener('resize', onResize);
      setChrome(true, { auto: true, quiet: true });
    } catch (e) {
      stage.replaceChildren(errorView(e, () => navigate(location.pathname, { replace: true })));
      return;
    }
  }
  const startWhenConnected = () => {
    if (el.isConnected) { start(); return; }
    requestAnimationFrame(startWhenConnected);
  };
  requestAnimationFrame(startWhenConnected);

  function onResize() {
    clearTimeout(resizeTimer);
    resizeTimer = setTimeout(() => applyLayout(), 150);
  }

  /* ---------- settings ---------- */

  function applySettings() {
    const s = store.reader;
    const root = document.documentElement;
    root.setAttribute('data-reader-theme', s.theme);
    if (s.theme === 'custom') {
      root.style.setProperty('--custom-fg', s.custom_fg);
      root.style.setProperty('--custom-bg', s.custom_bg);
    }
    el.classList.toggle('reader-dark', isDarkReader(s));
    el.classList.toggle('flow-scrolled', s.layout === 'scrolled');
    const meta = document.querySelector('meta[name="theme-color"]');
    if (meta) {
      meta.setAttribute('content',
        getComputedStyle(root).getPropertyValue('--reader-bg').trim() || '#faf8f4');
    }
    if (s.layout === 'scrolled') setChrome(true, { quiet: true });

    const r = view?.renderer;
    if (!r) return;
    applyLayout();
    r.setStyles?.(readerCSS(s));
  }

  /**
   * Renderer geometry. Everything here is a documented `<foliate-paginator>`
   * attribute; the layout engine itself is untouched.
   *
   * - `max-inline-size` is the measure of one column, ~38em of the reader's own
   *   font size, in px (the renderer parses this value as a number of pixels).
   * - `max-column-count` is 2 only on a wide landscape viewport - the renderer
   *   already drops to one column in portrait - and always 1 while a cover or
   *   title page is showing, so it is centered rather than stranded in the
   *   left half of an empty spread.
   * - `gap` (a percentage of the page) and `margin` (the running-head band, in
   *   px) scale together with the margin setting.
   * - `max-block-size` is raised to the viewport height so the text block fills
   *   it instead of stopping at the renderer's 1440px default.
   */
  function applyLayout() {
    const r = view?.renderer;
    if (!r) return;
    const s = store.reader;
    const rect = el.getBoundingClientRect();
    const w = Math.round(rect.width) || window.innerWidth;
    const h = Math.round(rect.height) || window.innerHeight;
    const factor = MARGIN_FACTORS[s.margin] ?? MARGIN_FACTORS.normal;

    const wantsTwo = s.columns === '2'
      || (s.columns === 'auto' && w >= TWO_COLUMN_MIN_PX && w > h);
    const columns = singlePageSection || s.layout === 'scrolled' || !wantsTwo ? 1 : 2;

    const attr = (name, value) => {
      if (r.getAttribute(name) !== value) r.setAttribute(name, value);
    };
    attr('flow', s.layout === 'scrolled' ? 'scrolled' : 'paginated');
    attr('max-inline-size', `${Math.round(COLUMN_EM * 16 * s.font_scale)}px`);
    attr('max-column-count', String(columns));
    attr('max-block-size', `${Math.max(480, h)}px`);
    attr('gap', `${clamp(Math.round(7 * factor * 10) / 10, 3.5, 14)}%`);
    attr('margin', `${Math.round(clamp(h * 0.055, 30, 72) * factor)}px`);
    r.toggleAttribute('animated', !reduceMotion());
  }

  /**
   * A cover, a title page or a part divider is a single short page. Left in a
   * two-column spread it would sit alone on the left with an empty column
   * beside it, so the section is laid out as one centered column. Decided on
   * `load`, which the renderer fires before it measures the page.
   * @param {{doc:Document}} detail
   */
  function onSectionLoad(detail) {
    hardenFrame(detail);
    const doc = detail?.doc;
    const chars = doc?.body?.textContent?.trim().length ?? 0;
    singlePageSection = chars < SINGLE_PAGE_CHARS;
    applyLayout();
  }

  function openSettings() {
    setChrome(true);
    const content = readerSettingsControls(() => applySettings());
    openSheet(el, 'Reading settings', content, { dock: 'side' });
  }

  function openToc() {
    setChrome(true);
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
    const sheet = openSheet(el, 'Contents', wrap, { dock: 'side' });
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

    const chapter = detail?.tocItem?.label || '';
    const left = pagesLeftInChapter();
    context.textContent = left === null ? chapter : left;
    context.title = left === null ? chapter : `${left}${chapter ? ` - ${chapter}` : ''}`;

    say(chapter ? `${text}. ${chapter}` : text);

    clearTimeout(saveTimer);
    saveTimer = setTimeout(saveNow, SAVE_DEBOUNCE_MS);
  }

  /**
   * "N pages left in chapter", from the renderer's own page count for the
   * current section. `pages` includes one blank page at each end, and `page` is
   * 1-based within the text, so what is left after this one is `pages - 2 - page`.
   * Returns null in scrolled mode and while the count is not yet known.
   * @returns {string|null}
   */
  function pagesLeftInChapter() {
    const r = view?.renderer;
    if (!r || r.scrolled) return null;
    const pages = Number(r.pages);
    const page = Number(r.page);
    if (!Number.isFinite(pages) || !Number.isFinite(page) || pages < 3) return null;
    const left = pages - 2 - page;
    if (left < 0) return null;
    if (left === 0) return 'Last page in chapter';
    return `${left} page${left === 1 ? '' : 's'} left in chapter`;
  }

  /** Announce at most once per ANNOUNCE_MS, and never the same string twice. */
  function say(message) {
    if (message === lastAnnounced) return;
    lastAnnounced = message;
    clearTimeout(announceTimer);
    announceTimer = setTimeout(() => announce(message), ANNOUNCE_MS);
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

  /* ---------- gestures ---------- */

  /**
   * Swipe to page, on the tap-zone layer. The book frame is sandboxed without
   * scripts, so its own touch handling never fires; this is the only source of
   * gestures. A swipe suppresses the click the browser sends afterwards, so a
   * flick across the "next page" zone does not also count as a tap.
   * @param {HTMLElement} layer
   */
  function addSwipe(layer) {
    let x0 = 0, y0 = 0, id = -1, swiped = false;
    layer.addEventListener('pointerdown', (e) => {
      if (e.pointerType === 'mouse' && e.button !== 0) return;
      id = e.pointerId;
      x0 = e.clientX;
      y0 = e.clientY;
      swiped = false;
    });
    layer.addEventListener('pointerup', (e) => {
      if (e.pointerId !== id) return;
      id = -1;
      if (store.reader.layout === 'scrolled') return;
      const dx = e.clientX - x0;
      const dy = e.clientY - y0;
      if (Math.abs(dx) < SWIPE_MIN_PX || Math.abs(dx) <= Math.abs(dy)) return;
      swiped = true;
      turn(dx < 0 ? 1 : -1);
    });
    layer.addEventListener('pointercancel', () => { id = -1; });
    layer.addEventListener('click', (e) => {
      if (!swiped) return;
      swiped = false;
      e.preventDefault();
      e.stopPropagation();
    }, true);
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
      case 'ArrowLeft': case 'PageUp': turn(-1, { keyboard: true }); break;
      case 'ArrowRight': case 'PageDown': case ' ': turn(1, { keyboard: true }); break;
      case 'Home': setChrome(true, { auto: true, quiet: true }); view?.goToFraction(0); break;
      case 'End': setChrome(true, { auto: true, quiet: true }); view?.goToFraction(1); break;
      case 't': case 'T': openToc(); break;
      case 's': case 'S': openSettings(); break;
      case 'Escape': navigate(`/item/${encodeURIComponent(ctx.params.id)}`); break;
      default: return;
    }
    e.preventDefault();
  }

  window.addEventListener('pagehide', saveNow);

  return { el, title: 'Reader', destroy: cleanup };
}

const clamp = (n, lo, hi) => Math.min(hi, Math.max(lo, n));

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
  /* Re-point the shell's tokens at the reader palette so the chrome, the
     sheets and the book page are one surface with no light frame around a
     dark page. */
  --bg: var(--reader-bg);
  --surface: var(--reader-chrome);
  --surface-2: var(--reader-chrome);
  --surface-2: color-mix(in srgb, var(--reader-fg) 10%, var(--reader-bg));
  --text: var(--reader-fg);
  --muted: var(--reader-muted);
  --border: var(--reader-border);
  --accent: var(--reader-link);
  --accent-text: var(--reader-bg);
}
.reader-stage {
  position: absolute;
  inset: 0;
  padding:
    env(safe-area-inset-top) env(safe-area-inset-right)
    env(safe-area-inset-bottom) env(safe-area-inset-left);
}
.reader-stage foliate-view { display: block; width: 100%; height: 100%; }

.reader-zones {
  position: absolute;
  inset: 0;
  display: grid;
  grid-template-columns: 1fr 1.2fr 1fr;
  touch-action: pan-y pinch-zoom;
}
.flow-scrolled .reader-zones { display: none; }
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
  padding: var(--s1) var(--s2);
  color: var(--reader-fg);
  background: var(--reader-chrome);
  background: color-mix(in srgb, var(--reader-chrome) 88%, transparent);
  backdrop-filter: blur(12px);
  transition: transform var(--motion) ease, opacity var(--motion) ease,
    visibility var(--motion) step-end;
}
.reader-bar--top {
  top: 0;
  border-bottom: 1px solid var(--reader-border);
  padding-top: calc(var(--s1) + env(safe-area-inset-top));
  padding-left: calc(var(--s2) + env(safe-area-inset-left));
  padding-right: calc(var(--s2) + env(safe-area-inset-right));
}
.reader-bar--bottom {
  bottom: 0;
  display: grid;
  gap: 0;
  border-top: 1px solid var(--reader-border);
  padding-bottom: calc(var(--s1) + env(safe-area-inset-bottom));
  padding-left: calc(var(--s4) + env(safe-area-inset-left));
  padding-right: calc(var(--s4) + env(safe-area-inset-right));
}
.reader-title { flex: 1; margin: 0; font-size: 1rem; font-weight: 600; }

.reader-info {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--s4);
  padding-bottom: var(--s1);
  font-size: 0.8rem;
  color: var(--reader-muted);
}
.reader-context { min-width: 0; }
.reader-readout { flex: none; font-variant-numeric: tabular-nums; }

/* Progress slider: a 44px-tall hit area with a thumb big enough to grab. */
.reader-slider {
  -webkit-appearance: none;
  appearance: none;
  width: 100%;
  height: var(--tap);
  margin: 0;
  background: transparent;
  cursor: pointer;
}
.reader-slider:focus-visible { outline: 3px solid var(--focus); outline-offset: 2px; }
.reader-slider::-webkit-slider-runnable-track {
  height: 4px;
  border-radius: 2px;
  background: var(--reader-border);
}
.reader-slider::-moz-range-track {
  height: 4px;
  border-radius: 2px;
  background: var(--reader-border);
}
.reader-slider::-webkit-slider-thumb {
  -webkit-appearance: none;
  width: 1.5rem;
  height: 1.5rem;
  margin-top: -0.625rem;
  border: 2px solid var(--reader-chrome);
  border-radius: 50%;
  background: var(--reader-link);
}
.reader-slider::-moz-range-thumb {
  width: 1.5rem;
  height: 1.5rem;
  border: 2px solid var(--reader-chrome);
  border-radius: 50%;
  background: var(--reader-link);
}
@media (pointer: coarse) {
  .reader-slider::-webkit-slider-thumb { width: 2rem; height: 2rem; margin-top: -0.875rem; }
  .reader-slider::-moz-range-thumb { width: 2rem; height: 2rem; }
}

.chrome-hidden .reader-bar {
  visibility: hidden;
  opacity: 0;
  transition-timing-function: ease, ease, step-end;
}
.chrome-hidden .reader-bar--top { transform: translateY(-100%); }
.chrome-hidden .reader-bar--bottom { transform: translateY(100%); }

@media (prefers-contrast: more) {
  .reader-bar { backdrop-filter: none; background: var(--reader-chrome); }
}
@media (prefers-reduced-motion: reduce) {
  .reader-bar { transition: none; }
}
@media (max-width: 30rem) {
  .reader-title { font-size: 0.95rem; }
}
`;
  return style;
}
