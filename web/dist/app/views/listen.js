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
import { navigate } from '../router.js';

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
    el.replaceChildren(errorView(e, () => navigate(location.pathname, { replace: true })));
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

  /* --- scrubber --- */
  const scrub = document.createElement('input');
  scrub.type = 'range';
  scrub.min = '0';
  scrub.max = String(Math.max(1, player.duration));
  scrub.step = '1000';
  scrub.value = '0';
  scrub.setAttribute('aria-label', 'Playback position');

  let scrubbing = false;
  scrub.addEventListener('pointerdown', () => { scrubbing = true; });
  scrub.addEventListener('keydown', () => { scrubbing = true; });
  scrub.addEventListener('change', () => {
    scrubbing = false;
    player.seek(Number(scrub.value));
  });
  scrub.addEventListener('input', () => { times(); });

  const timeRow = document.createElement('div');
  timeRow.className = 'player-times';
  const elapsed = document.createElement('span');
  const remaining = document.createElement('span');
  timeRow.append(elapsed, remaining);

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

  function times() {
    const pos = scrubbing ? Number(scrub.value) : player.position;
    elapsed.textContent = clock(pos);
    remaining.textContent = `-${clock(Math.max(0, player.duration - pos))}`;
    scrub.setAttribute('aria-valuetext',
      `${spokenDuration(pos)} of ${spokenDuration(player.duration)}`);
  }

  function render() {
    scrub.max = String(Math.max(1, player.duration));
    if (!scrubbing) scrub.value = String(Math.min(player.duration, player.position));
    times();

    const playing = player.playing;
    playBtn.replaceChildren(icon(playing ? 'pause' : 'play'));
    playBtn.setAttribute('aria-label', playing ? 'Pause' : 'Play');
    playBtn.title = playing ? 'Pause' : 'Play';

    const c = player.chapter;
    chapterName.textContent = c
      ? `${c.title} (${player.chapterIndex + 1} of ${player.chapters.length})`
      : '';

    speedBtn.replaceChildren(icon('speed'));
    const sl = document.createElement('span');
    sl.textContent = `${trim(store.player.speed)}x`;
    speedBtn.append(sl);
    speedBtn.setAttribute('aria-label', `Playback speed, currently ${trim(store.player.speed)} times`);

    if (player.sleepEndOfChapter) sleepLabel.textContent = 'End of chapter';
    else if (player.sleepAt) sleepLabel.textContent = `${Math.ceil(player.sleepRemainingMs / 60000)}m`;
    else sleepLabel.textContent = 'Sleep';
    sleepBtn.setAttribute('aria-label', `Sleep timer: ${sleepLabel.textContent}`);

    backBtn.setAttribute('aria-label', `Skip back ${store.player.skip_back_s} seconds`);
    fwdBtn.setAttribute('aria-label', `Skip forward ${store.player.skip_fwd_s} seconds`);
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
.player-cover {
  width: min(18rem, 70vw);
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
  width: 100%;
  font-variant-numeric: tabular-nums;
  color: var(--muted);
  font-size: 0.9rem;
}
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
`;
  return s;
}
