/**
 * <bs-mini-player> - persistent playback bar.
 *
 * Rendered once by the shell and never re-created, so navigating routes cannot
 * interrupt audio. Hidden entirely when nothing is loaded.
 */

import { player } from '../player.js';
import { coverUrl } from '../api.js';
import { icon, iconButton } from './icons.js';
import { clock, names, peopleOf } from '../format.js';
import { router } from '../router.js';

const css = new CSSStyleSheet();
css.replaceSync(`
:host { display: block; }
:host([hidden]) { display: none; }
.bar {
  display: grid;
  grid-template-columns: auto 1fr auto auto;
  align-items: center;
  gap: var(--s3);
  height: var(--miniplayer-h);
  padding: 0 var(--s2) 0 var(--s3);
  border-bottom: 1px solid var(--border);
  background: var(--surface);
}
.open {
  display: flex;
  align-items: center;
  gap: var(--s3);
  min-width: 0;
  min-height: var(--tap);
  padding: var(--s1) var(--s2) var(--s1) 0;
  color: var(--text);
  text-decoration: none;
  border-radius: var(--radius);
}
.open:focus-visible { outline: 3px solid var(--focus); outline-offset: 2px; }
img {
  width: 3rem; height: 3rem;
  object-fit: cover;
  background: var(--surface-2);
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  flex: none;
}
.text { min-width: 0; display: grid; }
.t, .s {
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.t { font-weight: 600; font-size: 0.95rem; }
.s { color: var(--muted); font-size: 0.82rem; }
.time { font-variant-numeric: tabular-nums; color: var(--muted); font-size: 0.85rem; }
@media (max-width: 30rem) { .time { display: none; } }
button {
  display: grid;
  place-items: center;
  width: var(--tap); height: var(--tap);
  color: inherit;
  background: transparent;
  border: 1px solid transparent;
  border-radius: var(--radius);
  cursor: pointer;
}
button:hover { background: var(--surface-2); }
button:focus-visible { outline: 3px solid var(--focus); outline-offset: 2px; }
button svg { width: 1.6rem; height: 1.6rem; }
button.play {
  color: var(--accent-text);
  background: var(--accent);
  border-color: var(--accent);
}
.progress {
  height: 3px;
  background: var(--surface-2);
}
.progress > i { display: block; height: 100%; background: var(--accent); }
`);

export class MiniPlayer extends HTMLElement {
  #els = /** @type {any} */ ({});
  #wired = false;
  /** Last values written to the DOM, so a `time` tick only moves the clock. */
  #last = /** @type {{id?:string, playing?:boolean, sub?:string, time?:string}} */ ({});

  constructor() {
    super();
    const root = this.attachShadow({ mode: 'open' });
    root.adoptedStyleSheets = [css];

    const wrap = document.createElement('div');

    const bar = document.createElement('div');
    bar.className = 'bar';

    const open = document.createElement('a');
    open.className = 'open';
    const img = document.createElement('img');
    img.alt = '';
    const text = document.createElement('div');
    text.className = 'text';
    const t = document.createElement('span');
    t.className = 't';
    const s = document.createElement('span');
    s.className = 's';
    text.append(t, s);
    open.append(img, text);

    const time = document.createElement('span');
    time.className = 'time';

    const back = iconButton('skipBack', 'Skip back', () => player.skipBack());
    back.removeAttribute('class');

    const play = document.createElement('button');
    play.type = 'button';
    play.className = 'play';
    play.append(icon('play'));
    play.addEventListener('click', () => player.toggle());

    bar.append(open, time, back, play);

    const progress = document.createElement('div');
    progress.className = 'progress';
    const fill = document.createElement('i');
    progress.append(fill);

    wrap.append(progress, bar);
    root.append(wrap);

    this.#els = { open, img, t, s, time, play, fill };
  }

  connectedCallback() {
    // Attributes may only be set once connected: a custom element constructor
    // that adds attributes makes document.createElement() throw.
    if (!this.#wired) this.hidden = true;
    this.setAttribute('role', 'region');
    this.setAttribute('aria-label', 'Now playing');
    // The shell can be re-mounted after a full-screen route; only wire once.
    if (!this.#wired) {
      this.#wired = true;
      for (const ev of ['load', 'state', 'time', 'chapter']) {
        player.addEventListener(ev, () => this.#render());
      }
      // Route changes decide whether the bar duplicates the full player. Every
      // navigation, popstate included, goes through the router's resolve step,
      // so one listener covers them all; the element lives as long as the app.
      router.addEventListener('navigate', () => this.#render());
    }
    this.#render();
  }

  #render() {
    const it = player.item;
    const href = it ? `/listen/${encodeURIComponent(it.id)}` : '';
    // On the item's own player page the bar would be a second copy of the same
    // transport, right underneath it.
    this.hidden = !it || location.pathname.replace(/\/+$/, '') === href;
    if (!it) return;

    const e = this.#els;
    // `time` fires ~4x a second: only the clock and the progress fill may be
    // written on every tick, everything else is dirty-checked.
    const time = `${clock(player.position)} / ${clock(player.duration)}`;
    if (time !== this.#last.time) {
      this.#last.time = time;
      e.time.textContent = time;
    }
    e.fill.style.width = player.duration
      ? `${Math.min(100, (player.position / player.duration) * 100)}%` : '0%';

    if (it.id !== this.#last.id) {
      this.#last.id = it.id;
      const src = coverUrl(it.id, 'thumb');
      if (e.img.getAttribute('src') !== src) e.img.setAttribute('src', src);
      e.open.href = href;
      e.open.setAttribute('aria-label', `Open player for ${it.title}`);
      e.t.textContent = it.title || '';
    }

    const sub = player.chapter?.title || names(peopleOf(it, 'author'));
    if (sub !== this.#last.sub) {
      this.#last.sub = sub;
      e.s.textContent = sub;
    }

    const playing = player.playing;
    if (playing !== this.#last.playing) {
      this.#last.playing = playing;
      e.play.setAttribute('aria-label', playing ? 'Pause' : 'Play');
      e.play.title = playing ? 'Pause' : 'Play';
      e.play.replaceChildren(icon(playing ? 'pause' : 'play'));
    }
  }
}

customElements.define('bs-mini-player', MiniPlayer);
