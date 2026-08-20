/**
 * <bs-item-card> - one cover tile in a grid or rail.
 *
 * Usage: `itemCard(item)` (the element is also registered as a custom element
 * so it can be used declaratively). The whole tile is a single link, so there
 * is exactly one tab stop per item and the accessible name carries title,
 * author and progress.
 */

import { coverUrl } from '../api.js';
import { names, peopleOf, percent } from '../format.js';

const css = new CSSStyleSheet();
css.replaceSync(`
:host { display: block; }
a {
  display: grid;
  gap: var(--s2);
  color: var(--text);
  text-decoration: none;
  border-radius: var(--radius);
}
a:focus-visible { outline: 3px solid var(--focus); outline-offset: 3px; }
.frame {
  position: relative;
  aspect-ratio: 2 / 3;
  background: var(--surface-2);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  overflow: hidden;
}
img { width: 100%; height: 100%; object-fit: cover; display: block; }
.fallback {
  display: grid;
  place-items: center;
  height: 100%;
  padding: var(--s3);
  text-align: center;
  font-size: 0.8rem;
  font-weight: 600;
  color: var(--muted);
}
.badge {
  position: absolute;
  inset: auto var(--s1) var(--s1) auto;
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 2px 6px;
  font-size: 0.7rem;
  font-weight: 700;
  letter-spacing: 0.02em;
  text-transform: uppercase;
  color: var(--accent-text);
  background: var(--accent);
  border-radius: var(--radius-sm);
}
.bar {
  position: absolute;
  inset: auto 0 0 0;
  height: 4px;
  background: var(--surface-2);
  border-top: 1px solid var(--border);
}
.bar > i { display: block; height: 100%; background: var(--accent); }
.title { font-weight: 600; font-size: 0.95rem; line-height: 1.3; }
.by { color: var(--muted); font-size: 0.85rem; line-height: 1.3; }
.title, .by {
  display: -webkit-box;
  -webkit-line-clamp: 2;
  line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
`);

export class ItemCard extends HTMLElement {
  /** @type {any} */
  #item = null;

  constructor() {
    super();
    this.attachShadow({ mode: 'open' }).adoptedStyleSheets = [css];
  }

  /** @param {any} item */
  set item(item) { this.#item = item; this.#render(); }
  get item() { return this.#item; }

  #render() {
    const it = this.#item;
    if (!it || !this.shadowRoot) return;
    const author = names(peopleOf(it, 'author'));
    const frac = it.progress?.percent ?? 0;
    const finished = Boolean(it.progress?.finished_at);
    const isAudio = it.kind === 'audiobook';

    const a = document.createElement('a');
    a.href = `/item/${encodeURIComponent(it.id)}`;
    const label = [
      it.title,
      author ? `by ${author}` : '',
      isAudio ? 'audiobook' : 'ebook',
      finished ? 'finished' : frac > 0 ? `${percent(frac)} read` : '',
    ].filter(Boolean).join(', ');
    a.setAttribute('aria-label', label);

    const frame = document.createElement('div');
    frame.className = 'frame';

    const img = document.createElement('img');
    img.loading = 'lazy';
    img.decoding = 'async';
    img.alt = '';
    img.src = coverUrl(it.id, 'thumb');
    img.addEventListener('error', () => {
      const fb = document.createElement('div');
      fb.className = 'fallback';
      fb.textContent = it.title || 'Untitled';
      img.replaceWith(fb);
    });
    frame.append(img);

    const badge = document.createElement('span');
    badge.className = 'badge';
    badge.textContent = finished ? 'Done' : isAudio ? 'Audio' : 'Book';
    frame.append(badge);

    if (frac > 0 && !finished) {
      const bar = document.createElement('div');
      bar.className = 'bar';
      const i = document.createElement('i');
      i.style.width = percent(frac);
      bar.append(i);
      frame.append(bar);
    }

    const title = document.createElement('span');
    title.className = 'title';
    title.textContent = it.title || 'Untitled';

    const by = document.createElement('span');
    by.className = 'by';
    by.textContent = author;

    a.append(frame, title, by);
    this.shadowRoot.replaceChildren(a);
  }
}

customElements.define('bs-item-card', ItemCard);

/** @param {any} item */
export function itemCard(item) {
  const el = /** @type {ItemCard} */ (document.createElement('bs-item-card'));
  el.item = item;
  return el;
}

/**
 * @param {any[]} items
 * @param {{rail?:boolean, labelledBy?:string}} [opts]
 */
export function itemGrid(items, opts = {}) {
  const ul = document.createElement('ul');
  ul.className = opts.rail ? 'rail' : 'grid-items';
  if (opts.labelledBy) ul.setAttribute('aria-labelledby', opts.labelledBy);
  for (const it of items) {
    const li = document.createElement('li');
    li.append(itemCard(it));
    ul.append(li);
  }
  return ul;
}
