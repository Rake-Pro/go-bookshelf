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
import { player } from '../player.js';
import { openBook, readerCSS, isDarkReader } from '../epub.js';
import { icon, iconButton } from '../components/icons.js';
import { openSheet } from '../components/sheet.js';
import { readerSettingsControls, TWO_COLUMN_MIN_PX } from '../components/reader-settings.js';
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
/** A section with less text than this is a cover or a title page. */
const SINGLE_PAGE_CHARS = 400;
const SWIPE_MIN_PX = 45;
/** A phone in portrait: the running-head band shrinks so the text gets the room. */
const SHORT_VIEWPORT_PX = 700;
/** A height change smaller than this is the mobile URL bar, not a real resize. */
const URLBAR_FLUTTER_PX = 120;
/** A pointer that moved more than this between press and release was a drag. */
const TAP_SLOP_PX = 10;

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
  /** Pending renderer re-layout, so a slider drag re-columnizes once per frame. */
  let applyRaf = 0;
  /** True while the position slider is being dragged: relocations must not
      overwrite the projected readout under the user's thumb. */
  let scrubbing = false;
  /** Total locations from the last relocation, for the projected readout. */
  let lastTotal = 0;
  /** Viewport at the last re-layout, so URL-bar flutter does not repaginate. */
  let lastW = window.innerWidth;
  let lastH = window.innerHeight;

  const cleanup = () => {
    cancelAnimationFrame(applyRaf);
    clearTimeout(saveTimer);
    clearTimeout(hideTimer);
    clearTimeout(resizeTimer);
    clearTimeout(announceTimer);
    saveNow();
    document.removeEventListener('keydown', onKey, true);
    window.removeEventListener('pagehide', saveNow);
    window.removeEventListener('resize', onResize);
    for (const [ev, fn] of audioSubs) player.removeEventListener(ev, fn);
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
  // Audiobook transport, for the case where one is playing while an ebook is
  // being read: pausing it should not cost the reader their page. Hidden
  // entirely when no audio is loaded. It takes the reader palette for free,
  // like every other .iconbtn in this bar.
  const audioBtn = iconButton('pause', 'Pause audiobook', () => player.toggle());
  audioBtn.hidden = true;
  top.append(back, title, audioBtn, tocBtn, setBtn);

  /** Last shape written to the DOM: '' (no audio), 'play' or 'pause'. */
  let lastAudio = /** @type {string|null} */ (null);
  function renderAudio() {
    const on = Boolean(player.item);
    const want = on ? (player.playing ? 'pause' : 'play') : '';
    if (want === lastAudio) return;
    lastAudio = want;
    audioBtn.hidden = !on;
    if (!on) return;
    const label = want === 'pause' ? 'Pause audiobook' : 'Play audiobook';
    audioBtn.setAttribute('aria-label', label);
    audioBtn.title = label;
    audioBtn.replaceChildren(icon(want));
  }
  /** @type {[string, () => void][]} */
  const audioSubs = [];
  for (const ev of ['load', 'state', 'ended']) {
    const fn = () => renderAudio();
    player.addEventListener(ev, fn);
    audioSubs.push([ev, fn]);
  }
  renderAudio();

  const bottom = document.createElement('footer');
  bottom.className = 'reader-bar reader-bar--bottom';
  const slider = document.createElement('input');
  slider.type = 'range';
  slider.className = 'reader-slider range-touch';
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

  // Dragging shows where the release would land without navigating there; the
  // jump happens on release. Without this the readout keeps reporting the page
  // still on screen, so the slider gives no feedback at all while in use.
  slider.addEventListener('input', () => {
    scrubbing = true;
    const f = Number(slider.value) / 1000;
    const text = lastTotal > 1
      ? `Page ${clamp(Math.round(f * lastTotal) + 1, 1, lastTotal)} of ${lastTotal}`
      : `${percent(f)} through`;
    readout.textContent = text;
    slider.setAttribute('aria-valuetext', text);
  });
  slider.addEventListener('change', () => {
    scrubbing = false;
    const f = Number(slider.value) / 1000;
    view?.goToFraction(f);
  });
  // A drag that ends back where it started fires no `change`, so release and
  // blur clear the flag too rather than leaving the readout frozen.
  for (const ev of ['pointerup', 'pointercancel', 'blur']) {
    slider.addEventListener(ev, () => { scrubbing = false; });
  }

  /* Tap zones. Only the left and right gutters take pointer events: the center
     has to reach the book so in-book links, text selection and the renderer's
     own touch paging work. The center zone stays a real button for the
     keyboard; pointer taps in the center are caught by watchTaps() instead. */
  const zones = document.createElement('div');
  zones.className = 'reader-zones';
  const zPrev = zoneButton('Previous page', () => turn(-1), true);
  const zCenter = zoneButton('Show or hide reading controls', () => setChrome(!chromeVisible), false);
  const zNext = zoneButton('Next page', () => turn(1), true);
  zones.append(zPrev, zCenter, zNext);
  addSwipe(zones);
  watchTaps(stage);

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
      // The renderer cancels every in-book link click and re-dispatches it
      // here. An uncancelled `link` keeps its default, which is the renderer's
      // own goTo() - exactly what a footnote or a TOC target needs, so it gets
      // no listener. `external-link` does need one: its default is
      // `window.open(href)` on the raw attribute, which keeps an opener and
      // would follow a "javascript:" href out of the sandbox.
      view.addEventListener('external-link', (e) => {
        e.preventDefault();
        const href = e.detail?.href_ || '';
        if (!/^(https?|mailto):/i.test(href)) return;
        window.open(href, '_blank', 'noopener,noreferrer');
      });

      applySettings({ immediate: true });

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

  /**
   * Showing and hiding the mobile URL bar fires a resize worth 60-100px of
   * height and nothing else. Re-columnizing on that throws the reader onto a
   * different page mid-sentence, so only a width change or a height jump big
   * enough to be a rotation or a split-screen resize re-runs the layout.
   */
  function onResize() {
    clearTimeout(resizeTimer);
    resizeTimer = setTimeout(() => {
      const w = window.innerWidth;
      const h = window.innerHeight;
      if (w === lastW && Math.abs(h - lastH) < URLBAR_FLUTTER_PX) return;
      lastW = w;
      lastH = h;
      applyLayout();
    }, 150);
  }

  /* ---------- settings ---------- */

  /**
   * @param {{immediate?:boolean}} [opts] immediate: re-lay out in this tick
   *   rather than on the next frame, for the first application on open.
   */
  function applySettings(opts = {}) {
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

    if (!view?.renderer) return;
    // A range slider fires `input` on every pointer sample, and each one would
    // otherwise re-columnize the whole section. Coalesce to one application per
    // frame; the callback re-reads the store, so the latest settings win.
    cancelAnimationFrame(applyRaf);
    if (opts.immediate) { applyRenderer(); return; }
    applyRaf = requestAnimationFrame(() => { applyRaf = 0; applyRenderer(); });
  }

  function applyRenderer() {
    const r = view?.renderer;
    if (!r) return;
    applyLayout();
    r.setStyles?.(readerCSS(store.reader));
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
   *   px) scale together with the margin setting. The band is always empty
   *   here (no running heads are set), so on a short viewport it shrinks to a
   *   thin strip instead of eating a tenth of the screen at each end - which
   *   also stops the renderer from capping full-page images at height minus
   *   twice the band.
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

    // An explicit "Two" is a preference, not an override: two columns on a
    // phone would give each one a ~180px measure. The settings sheet disables
    // the option for the same reason, so the two agree.
    const canTwo = w >= TWO_COLUMN_MIN_PX && w > h;
    const wantsTwo = canTwo && (s.columns === '2' || s.columns === 'auto');
    const columns = singlePageSection || s.layout === 'scrolled' || !wantsTwo ? 1 : 2;

    const attr = (name, value) => {
      if (r.getAttribute(name) !== value) r.setAttribute(name, value);
    };
    attr('flow', s.layout === 'scrolled' ? 'scrolled' : 'paginated');
    attr('max-inline-size', `${Math.round(COLUMN_EM * 16 * s.font_scale)}px`);
    attr('max-column-count', String(columns));
    attr('max-block-size', `${Math.max(480, h)}px`);
    attr('gap', `${clamp(Math.round(7 * factor * 10) / 10, 3.5, 14)}%`);
    const band = h < SHORT_VIEWPORT_PX ? clamp(h * 0.03, 8, 72) : clamp(h * 0.055, 30, 72);
    attr('margin', `${Math.round(band * factor)}px`);
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
    // The side dock only exists from 900px. Narrower than that, a full-height
    // sheet would cover the very page the settings are meant to preview, so it
    // opens as a low sheet with no sample box: the real text above it is the
    // preview.
    const side = window.matchMedia?.('(min-width: 56.25rem)').matches ?? false;
    // The controls hang a window resize listener; tie its life to the sheet's.
    const ac = new AbortController();
    const content = readerSettingsControls(() => applySettings(), { preview: side, signal: ac.signal });
    const sheet = openSheet(el, 'Reading settings', content, { dock: side ? 'side' : 'compact' });
    sheet.addEventListener('sheet-close', () => ac.abort(), { once: true });
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

    const loc = detail?.location;
    lastTotal = Number(loc?.total) > 1 ? Number(loc.total) : 0;
    const text = loc && loc.total > 1
      ? `Page ${loc.current + 1} of ${loc.total}`
      : `${percent(fraction)} through`;
    // Mid-drag the slider shows where the release will land; don't fight it.
    if (!scrubbing) {
      slider.value = String(Math.round(fraction * 1000));
      readout.textContent = text;
      slider.setAttribute('aria-valuetext', text);
    }

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
    // Nor do clicks, so the center tap is wired per book document.
    watchTaps(doc);
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
   * Toggle the chrome on a tap that is not a drag, a link or a selection.
   *
   * The tap-zone layer used to own this, but it covered the whole page and so
   * swallowed every touch the book itself needed. The zones are edge gutters
   * now, and the center tap is read where the tap actually lands: on the stage
   * for the renderer's own margins, and on each book document (clicks do not
   * cross the iframe boundary). The renderer cancels link clicks before this
   * listener runs, which is what `defaultPrevented` filters out.
   *
   * @param {Document|HTMLElement} target
   */
  function watchTaps(target) {
    let x0 = 0, y0 = 0;
    target.addEventListener('pointerdown', (e) => {
      x0 = /** @type {PointerEvent} */ (e).clientX;
      y0 = /** @type {PointerEvent} */ (e).clientY;
    }, true);
    target.addEventListener('click', (e) => {
      const ev = /** @type {MouseEvent} */ (e);
      if (ev.defaultPrevented) return;
      if (Math.abs(ev.clientX - x0) > TAP_SLOP_PX || Math.abs(ev.clientY - y0) > TAP_SLOP_PX) return;
      const t = /** @type {Element|null} */ (ev.target);
      if (t?.closest?.('a[href]')) return;
      const sel = t?.ownerDocument?.getSelection?.();
      if (sel && !sel.isCollapsed) return;
      setChrome(!chromeVisible);
    });
  }

  /**
   * Swipe to page, on the tap-zone layer, which is now just the two edge
   * gutters: in the center the renderer's own touch handling drags the page
   * with the finger and snaps on release, which is the better gesture. A swipe
   * suppresses the click the browser sends afterwards, so a flick across the
   * "next page" zone does not also count as a tap.
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

/** @param {string} label @param {() => void} onClick @param {boolean} edge */
function zoneButton(label, onClick, edge) {
  const b = document.createElement('button');
  b.type = 'button';
  b.className = edge ? 'reader-zone reader-zone--edge' : 'reader-zone';
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
  /* No pull-to-refresh and no scroll chaining out of the reader: a downward
     drag on the page must never reload the book. */
  overscroll-behavior: none;
  /* The floating bars, so the scrolled layout can keep its text clear of them. */
  --reader-bar-h: calc(var(--tap) + var(--s1) * 2 + 1px);
  --reader-foot-h: calc(var(--tap) + var(--s6) + var(--s1) * 2 + 1px);
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

/* Scrolled mode pins the bars open, so the text has to start below the top one
   and stop above the footer. */
.flow-scrolled .reader-stage {
  padding-top: calc(var(--reader-bar-h) + env(safe-area-inset-top));
  padding-bottom: calc(var(--reader-foot-h) + env(safe-area-inset-bottom));
  overscroll-behavior: contain;
}

.reader-zones {
  position: absolute;
  inset: 0;
  display: grid;
  /* Edge gutters. The center column is transparent to the pointer so in-book
     links, text selection and the renderer's own drag-to-page reach the book;
     the center tap is handled by watchTaps() instead. */
  grid-template-columns: 18% 1fr 18%;
  pointer-events: none;
}
.flow-scrolled .reader-zones { display: none; }
.reader-zone {
  border: 0;
  background: transparent;
  cursor: pointer;
  pointer-events: none;
  -webkit-tap-highlight-color: transparent;
}
/* Keyboard users still reach the center zone: it is focusable and clickable
   with Enter, it just never intercepts a pointer. */
.reader-zone--edge { pointer-events: auto; touch-action: pan-y pinch-zoom; }
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

/* Progress slider: a 44px-tall hit area. The track and thumb, including the
   bigger coarse-pointer thumb, come from .range-touch in app.css, which the
   audiobook scrubber shares; the reader palette reaches them because .reader
   re-points --accent, --border and --surface above. */
.reader-slider { width: 100%; height: var(--tap); margin: 0; }

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
