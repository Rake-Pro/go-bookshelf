/**
 * /listen/{id} - the full audiobook player.
 *
 * All state lives in the global player controller, so this view is a pure
 * renderer: it subscribes on mount, unsubscribes on destroy, and never touches
 * the <audio> element. Navigating away keeps playback running (the mini-player
 * takes over).
 *
 * Keyboard: Space toggles play, Left/Right skip, Up/Down change speed,
 * Home restarts the chapter.
 */

import { api, coverUrl } from '../api.js';
import { player } from '../player.js';
import { store } from '../store.js';
import { icon, iconButton } from '../components/icons.js';
import { openSheet } from '../components/sheet.js';
import { loadingView, errorView } from '../components/states.js';
import { clock, duration, names, peopleOf, spokenDuration } from '../format.js';
import { announce } from '../live.js';
import { navigate, router } from '../router.js';

const SPEED_PRESETS = [1, 1.25, 1.5, 2];
const SLEEP_PRESETS = [15, 30, 45, 60];

/** @param {import('../router.js').RouteCtx} ctx */
export default async function listen(ctx) {
  const el = document.createElement('div');
  el.append(style());
  el.append(loadingView('Loading audiobook'));

  let item;
  try {
    item = await api.item(ctx.params.id);
  } catch (e) {
    el.replaceChildren(errorView(e, () => router.refresh()));
    return { el, title: 'Player' };
  }

  if (item.kind !== 'audiobook') {
    navigate(`/item/${encodeURIComponent(item.id)}`, { replace: true });
    return { el, title: item.title || 'Item' };
  }

  await player.load(item);

  const wrap = document.createElement('div');
  wrap.className = 'player';

  /* --- artwork + titles --- */
  const cover = document.createElement('img');
  cover.className = 'player-cover';
  cover.src = coverUrl(item.id, 'full');
  cover.alt = `Cover of ${item.title}`;
  cover.addEventListener('error', () => { cover.style.visibility = 'hidden'; });

  const h1 = document.createElement('h1');
  h1.textContent = item.title || 'Untitled';
  h1.tabIndex = -1;

  const byline = document.createElement('p');
  byline.className = 'muted';
  const author = names(peopleOf(item, 'author'));
  const narrator = names(peopleOf(item, 'narrator'));
  byline.textContent = [author, narrator ? `Narrated by ${narrator}` : ''].filter(Boolean).join(' - ');

  const chapterName = document.createElement('p');
  chapterName.className = 'player-chapter';

  /* --- scrubber ---
     Scoped to the chapter whenever there is more than one: a nine-hour book
     across ~330px of track is about 100 seconds per pixel, which makes a small
     correction impossible. The value stays an absolute position, so nothing
     downstream has to know about the scoping; only min/max move. The whole-book
     picture stays in the times row either side of the chapter clock. */
  const scrub = document.createElement('input');
  scrub.type = 'range';
  scrub.className = 'range-touch';
  scrub.min = '0';
  scrub.max = String(Math.max(1, player.duration));
  scrub.step = '1000';
  scrub.value = '0';
  scrub.setAttribute('aria-label', 'Playback position');

  const SCRUB_KEYS = new Set(['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown', 'Home', 'End', 'PageUp', 'PageDown']);
  let scrubbing = false;
  scrub.addEventListener('pointerdown', () => { scrubbing = true; });
  scrub.addEventListener('keydown', (e) => { if (SCRUB_KEYS.has(e.key)) scrubbing = true; });
  scrub.addEventListener('change', () => {
    scrubbing = false;
    // The chapter bounds already keep the range inside the book, but a stale
    // max (a chapter list jump landing mid-drag) must not send a seek past the
    // last track either.
    player.seek(clamp(Number(scrub.value), 0, Math.max(0, player.duration)));
    // Releasing is also when the bounds frozen during the drag catch up.
    render();
  });
  scrub.addEventListener('blur', () => {
    if (!scrubbing) return;
    scrubbing = false;
    render();
  });
  // A press released on the same pixel fires neither input nor change, and
  // the range keeps focus, so pointerup/pointercancel must also unfreeze.
  for (const ev of ['pointerup', 'pointercancel']) {
    scrub.addEventListener(ev, () => {
      if (!scrubbing) return;
      scrubbing = false;
      render();
    });
  }
  scrub.addEventListener('input', () => { times(); });

  const timeRow = document.createElement('div');
  timeRow.className = 'player-times';
  const elapsed = document.createElement('span');
  const chapterTime = document.createElement('span');
  chapterTime.className = 'player-chapter-time';
  chapterTime.title = 'Position in this chapter';
  const remaining = document.createElement('span');
  timeRow.append(elapsed, chapterTime, remaining);

  /* --- transport --- */
  const transport = document.createElement('div');
  transport.className = 'player-transport';
  const backBtn = iconButton('skipBack', `Skip back ${store.player.skip_back_s} seconds`,
    () => player.skipBack(), 'player-skip');
  const fwdBtn = iconButton('skipFwd', `Skip forward ${store.player.skip_fwd_s} seconds`,
    () => player.skipForward(), 'player-skip');

  const playBtn = document.createElement('button');
  playBtn.type = 'button';
  playBtn.className = 'player-play';
  playBtn.addEventListener('click', () => player.toggle());

  transport.append(backBtn, playBtn, fwdBtn);

  /* --- secondary controls --- */
  const tools = document.createElement('div');
  tools.className = 'player-tools';

  const speedBtn = document.createElement('button');
  speedBtn.type = 'button';
  speedBtn.className = 'btn';
  speedBtn.setAttribute('aria-haspopup', 'dialog');
  speedBtn.addEventListener('click', openSpeed);

  const sleepBtn = document.createElement('button');
  sleepBtn.type = 'button';
  sleepBtn.className = 'btn';
  sleepBtn.setAttribute('aria-haspopup', 'dialog');
  sleepBtn.append(icon('timer'));
  const sleepLabel = document.createElement('span');
  sleepBtn.append(sleepLabel);
  sleepBtn.addEventListener('click', openSleep);

  const chaptersBtn = document.createElement('button');
  chaptersBtn.type = 'button';
  chaptersBtn.className = 'btn';
  chaptersBtn.setAttribute('aria-haspopup', 'dialog');
  chaptersBtn.append(icon('list'));
  const cl = document.createElement('span');
  cl.textContent = 'Chapters';
  chaptersBtn.append(cl);
  chaptersBtn.addEventListener('click', openChapters);

  const markBtn = document.createElement('button');
  markBtn.type = 'button';
  markBtn.className = 'btn';
  markBtn.append(icon('bookmark'));
  const ml = document.createElement('span');
  ml.textContent = 'Bookmark';
  markBtn.append(ml);
  markBtn.addEventListener('click', addBookmark);

  tools.append(speedBtn, sleepBtn, chaptersBtn, markBtn);

  wrap.append(cover, h1, byline, chapterName, scrub, timeRow, transport, tools);
  el.replaceChildren(style(), wrap);

  /* --- rendering --- */

  /**
   * The chapter the scrubber is currently scoped to, or null while it spans the
   * whole book. Held rather than recomputed so the bounds and the readouts
   * cannot disagree: playback carries on during a drag and can cross into the
   * next chapter, and the slider under the thumb must not move with it.
   * @type {{title:string, start:number, end:number}|null}
   */
  let scope = null;
  let lastScrubLabel = '';

  /**
   * Point min/max at the current chapter, or at the whole book when there are
   * fewer than two chapters to scope to. Both ends are clamped into the book,
   * so no value the slider can produce seeks outside the track list. A drag in
   * progress keeps the bounds it started with; the release re-runs this.
   */
  function applyBounds() {
    if (scrubbing) return;
    const total = Math.max(1, player.duration);
    const c = player.chapters.length > 1 ? player.chapter : null;
    // A chapter shorter than a couple of steps has nothing to scope to, and a
    // min equal to its max would leave the slider stuck. A position outside
    // the chapter's own bounds (an intro before the first mark, credits past
    // the last) must not be scoped either: the browser would clamp the value
    // to the nearer bound, pinning the thumb and making a release seek there.
    const pos = player.position;
    scope = c && c.end - c.start >= 2000 && pos >= c.start && pos <= c.end ? c : null;
    const lo = scope ? clamp(scope.start, 0, total) : 0;
    const hi = scope ? clamp(scope.end, lo, total) : total;
    if (scrub.min !== String(lo)) scrub.min = String(lo);
    if (scrub.max !== String(hi)) scrub.max = String(hi);
    const label = scope ? 'Playback position in chapter' : 'Playback position';
    if (label !== lastScrubLabel) {
      lastScrubLabel = label;
      scrub.setAttribute('aria-label', label);
    }
  }

  function times() {
    const pos = scrubbing ? Number(scrub.value) : player.position;
    elapsed.textContent = clock(pos);
    remaining.textContent = `-${clock(Math.max(0, player.duration - pos))}`;

    const book = `${spokenDuration(pos)} of ${spokenDuration(player.duration)}`;
    let chapterClock = '';
    let spoken = book;
    if (scope) {
      const len = Math.max(0, scope.end - scope.start);
      const into = clamp(pos - scope.start, 0, len);
      chapterClock = `${clock(into)} / ${clock(len)}`;
      spoken = `${spokenDuration(into)} of ${spokenDuration(len)} in ${scope.title}, `
        + `${book} in the book`;
    }
    if (chapterTime.textContent !== chapterClock) chapterTime.textContent = chapterClock;
    if (scrub.getAttribute('aria-valuetext') !== spoken) {
      scrub.setAttribute('aria-valuetext', spoken);
    }
  }

  // `time` fires ~4x a second. Rebuilding the play icon and rewriting the aria
  // labels on every tick is DOM churn a screen reader hears as a stream of
  // changes, so anything that only changes on a state change is dirty-checked;
  // per-tick work stays the time text and the slider position.
  let lastPlaying = /** @type {boolean|null} */ (null);
  let lastSpeed = /** @type {number|null} */ (null);
  let lastSkips = '';

  function render() {
    applyBounds();
    if (!scrubbing) scrub.value = String(Math.min(player.duration, player.position));
    times();

    const playing = player.playing;
    if (playing !== lastPlaying) {
      lastPlaying = playing;
      playBtn.replaceChildren(icon(playing ? 'pause' : 'play'));
      playBtn.setAttribute('aria-label', playing ? 'Pause' : 'Play');
      playBtn.title = playing ? 'Pause' : 'Play';
    }

    const c = player.chapter;
    const chapterText = c
      ? `${c.title} (${player.chapterIndex + 1} of ${player.chapters.length})`
      : '';
    if (chapterName.textContent !== chapterText) chapterName.textContent = chapterText;

    const speed = store.player.speed;
    if (speed !== lastSpeed) {
      lastSpeed = speed;
      speedBtn.replaceChildren(icon('speed'));
      const sl = document.createElement('span');
      sl.textContent = `${trim(speed)}x`;
      speedBtn.append(sl);
      speedBtn.setAttribute('aria-label', `Playback speed, currently ${trim(speed)} times`);
    }

    let sleepText = 'Sleep';
    if (player.sleepEndOfChapter) sleepText = 'End of chapter';
    else if (player.sleepAt) sleepText = `${Math.ceil(player.sleepRemainingMs / 60000)}m`;
    if (sleepLabel.textContent !== sleepText) {
      sleepLabel.textContent = sleepText;
      sleepBtn.setAttribute('aria-label', `Sleep timer: ${sleepText}`);
    }

    const skips = `${store.player.skip_back_s}/${store.player.skip_fwd_s}`;
    if (skips !== lastSkips) {
      lastSkips = skips;
      backBtn.setAttribute('aria-label', `Skip back ${store.player.skip_back_s} seconds`);
      fwdBtn.setAttribute('aria-label', `Skip forward ${store.player.skip_fwd_s} seconds`);
    }
  }

  /** @type {[string, () => void][]} */
  const subs = [];
  for (const ev of ['state', 'time', 'chapter', 'speed', 'sleep', 'load']) {
    const fn = () => render();
    player.addEventListener(ev, fn);
    subs.push([ev, fn]);
  }
  render();

  /* --- sheets --- */

  function openSpeed() {
    const box = document.createElement('div');
    const presets = document.createElement('div');
    presets.className = 'row';
    for (const p of SPEED_PRESETS) {
      const b = document.createElement('button');
      b.type = 'button';
      b.className = 'btn';
      b.textContent = `${trim(p)}x`;
      b.addEventListener('click', () => { player.setSpeed(p); sync(); });
      presets.append(b);
    }

    const label = document.createElement('label');
    label.className = 'label';
    label.setAttribute('for', 'speed-range');
    label.textContent = 'Speed';
    const range = document.createElement('input');
    range.type = 'range';
    range.id = 'speed-range';
    range.min = '0.5';
    range.max = '3';
    range.step = '0.05';
    range.value = String(store.player.speed);
    const out = document.createElement('output');
    out.style.fontVariantNumeric = 'tabular-nums';
    const sync = () => {
      range.value = String(store.player.speed);
      out.textContent = `${trim(store.player.speed)}x`;
      range.setAttribute('aria-valuetext', `${trim(store.player.speed)} times`);
    };
    range.addEventListener('input', () => { player.setSpeed(Number(range.value)); sync(); });
    sync();

    const row = document.createElement('div');
    row.className = 'row';
    row.style.flexWrap = 'nowrap';
    row.append(range, out);

    box.append(presets, label, row);
    openSheet(el, 'Playback speed', box);
  }

  function openSleep() {
    const box = document.createElement('div');
    const list = document.createElement('ul');
    list.className = 'linklist';
    /** @param {string} text @param {() => void} fn */
    const row = (text, fn) => {
      const li = document.createElement('li');
      const b = document.createElement('button');
      b.type = 'button';
      b.textContent = text;
      b.addEventListener('click', () => { fn(); render(); sheet.close(); });
      li.append(b);
      list.append(li);
    };
    for (const m of SLEEP_PRESETS) row(`${m} minutes`, () => player.setSleepTimer(m));
    row('End of chapter', () => player.setSleepEndOfChapter());
    row('Off', () => player.setSleepTimer(null));
    box.append(list);
    const sheet = openSheet(el, 'Sleep timer', box);
  }

  function openChapters() {
    const box = document.createElement('div');
    const list = document.createElement('div');
    list.setAttribute('role', 'list');
    const current = player.chapterIndex;
    player.chapters.forEach((c, i) => {
      const b = document.createElement('button');
      b.type = 'button';
      b.className = 'chapter-row';
      b.setAttribute('role', 'listitem');
      if (i === current) b.setAttribute('aria-current', 'true');
      const len = c.end - c.start;
      b.setAttribute('aria-label',
        `${i === current ? 'Currently playing. ' : ''}Chapter ${i + 1}, ${c.title}, ${duration(len)}`);
      const n = document.createElement('span');
      n.className = 'num';
      if (i === current) n.append(icon('play'));
      else n.textContent = String(i + 1);
      const t = document.createElement('span');
      t.textContent = c.title;
      const d = document.createElement('span');
      d.className = 'dur';
      d.textContent = duration(len);
      b.append(n, t, d);
      b.addEventListener('click', () => { player.goToChapter(i); sheet.close(); });
      list.append(b);
    });
    box.append(list);
    const sheet = openSheet(el, 'Chapters', box);
    queueMicrotask(() => list.children[current]?.scrollIntoView({ block: 'center' }));
  }

  async function addBookmark() {
    markBtn.disabled = true;
    try {
      await api.addBookmark({
        item_id: item.id,
        position_ms: Math.round(player.position),
        note: player.chapter?.title || '',
      });
      announce(`Bookmark added at ${clock(player.position)}`);
    } catch {
      announce('Could not add bookmark');
    } finally {
      markBtn.disabled = false;
    }
  }

  /* --- keyboard --- */

  /** @param {KeyboardEvent} e */
  function onKey(e) {
    if (e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey) return;
    const t = /** @type {HTMLElement|null} */ (e.target);
    if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable)) return;
    if (el.querySelector('bs-sheet')) return;
    switch (e.key) {
      case ' ': player.toggle(); break;
      case 'ArrowLeft': player.skipBack(); break;
      case 'ArrowRight': player.skipForward(); break;
      case 'ArrowUp': player.setSpeed(store.player.speed + 0.05); break;
      case 'ArrowDown': player.setSpeed(store.player.speed - 0.05); break;
      case 'Home': player.goToChapter(player.chapterIndex); break;
      default: return;
    }
    e.preventDefault();
  }
  document.addEventListener('keydown', onKey);

  return {
    el,
    title: item.title || 'Player',
    destroy() {
      document.removeEventListener('keydown', onKey);
      for (const [ev, fn] of subs) player.removeEventListener(ev, fn);
    },
  };
}

/** 1.25 -> "1.25", 2 -> "2" */
const trim = (n) => String(Number(n.toFixed(2)));

const clamp = (n, lo, hi) => Math.min(hi, Math.max(lo, n));

function style() {
  const s = document.createElement('style');
  s.textContent = `
.player {
  display: grid;
  justify-items: center;
  gap: var(--s3);
  max-width: 32rem;
  margin: 0 auto;
  text-align: center;
}
.player h1 { margin: var(--s4) 0 0; font-size: 1.35rem; }
.player p { margin: 0; }
/* Bounded by height as well as width: on a 360x640 phone an 18rem square
   pushed the play button below the fold. */
.player-cover {
  width: min(18rem, 70vw, 38dvh);
  aspect-ratio: 1 / 1;
  object-fit: cover;
  background: var(--surface-2);
  border: 1px solid var(--border);
  border-radius: var(--radius);
}
.player-chapter { font-weight: 600; }
.player input[type="range"] { width: 100%; margin-top: var(--s4); }
.player-times {
  display: flex;
  justify-content: space-between;
  gap: var(--s2);
  width: 100%;
  font-variant-numeric: tabular-nums;
  color: var(--muted);
  font-size: 0.9rem;
}
/* Position inside the chapter the scrubber is scoped to, between the book's
   own elapsed and remaining. Empty, and so invisible, on a book with no
   chapters to scope to. The narrowest phones give the two book clocks the
   room instead. */
.player-chapter-time { flex: none; }
@media (max-width: 22rem) { .player-chapter-time { display: none; } }
.player-transport {
  display: flex;
  align-items: center;
  gap: var(--s6);
  margin-top: var(--s2);
}
.player-play {
  display: grid;
  place-items: center;
  width: 4.5rem;
  height: 4.5rem;
  color: var(--accent-text);
  background: var(--accent);
  border: 1px solid var(--accent);
  border-radius: 50%;
  cursor: pointer;
}
.player-play svg { width: 2.5rem; height: 2.5rem; }
.player-play:focus-visible { outline: 3px solid var(--focus); outline-offset: 3px; }
.player-skip { width: 3.5rem; height: 3.5rem; }
.player-skip svg { width: 2rem; height: 2rem; }
.player-tools {
  display: flex;
  flex-wrap: wrap;
  justify-content: center;
  gap: var(--s2);
  margin-top: var(--s4);
}

/* A phone on its side has no room for a stacked player: cover to the left,
   everything you actually touch to the right, the same split .item-hero uses
   once there is width for it. */
@media (orientation: landscape) and (max-height: 30rem) {
  .player {
    /* One row per control, so the cover can span them all: "1 / -1" only
       reaches the end of the explicit grid. */
    grid-template-columns: auto minmax(0, 1fr);
    grid-template-rows: repeat(7, auto);
    justify-items: stretch;
    align-items: center;
    column-gap: var(--s6);
    max-width: 48rem;
    text-align: left;
  }
  .player-cover { grid-column: 1; grid-row: 1 / -1; width: min(14rem, 42vw, 60dvh); }
  .player > :not(.player-cover) { grid-column: 2; }
  .player h1 { margin: 0; font-size: 1.15rem; }
  .player input[type="range"] { margin-top: var(--s2); }
  .player-transport { justify-content: center; margin-top: 0; }
  .player-tools { justify-content: flex-start; margin-top: var(--s2); }
}
`;
  return s;
}
