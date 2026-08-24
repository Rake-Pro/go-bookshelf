/**
 * Modal bottom sheet built on <dialog>, which gives focus trapping, Escape and
 * inert background for free. On wide screens it centers as a panel instead, or
 * docks to the side when the caller asks for it (`openSheet(..., {dock:'side'})`)
 * so the page behind stays readable while its settings change. `dock:'compact'`
 * is the narrow-screen version of the same idea: a low sheet that leaves the
 * top of the page visible, since the side dock only exists from 900px.
 */

import { iconButton } from './icons.js';

const css = new CSSStyleSheet();
css.replaceSync(`
:host { display: contents; }
dialog {
  /* Header height, so the body's max-height and the header agree instead of
     the body being clipped by the difference. */
  --head: calc(var(--tap) + var(--s2) * 2 + 1px);
  --sheet-max: 85dvh;
  width: 100%;
  max-width: 34rem;
  max-height: var(--sheet-max);
  margin: auto auto 0;
  padding: 0;
  color: var(--text);
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius) var(--radius) 0 0;
  box-shadow: var(--shadow);
  overflow: hidden;
}
dialog::backdrop { background: var(--scrim); }
@media (min-width: 48rem) {
  dialog { margin: auto; border-radius: var(--radius); }
}
/* Compact dock: a low sheet for the narrow screens the side dock never covers,
   so the page it is meant to preview stays visible above it. */
dialog.dock-compact { --sheet-max: 55dvh; margin: auto auto 0; }
/* Side dock: a panel against the inline end, full height, so the reader page
   behind it stays visible and every change previews on the real text. */
@media (min-width: 56.25rem) {
  dialog.dock-side {
    width: min(26rem, 40vw);
    max-width: none;
    height: 100dvh;
    max-height: none;
    margin: 0 0 0 auto;
    border-radius: var(--radius) 0 0 var(--radius);
    border-right: 0;
  }
  dialog.dock-side .body { max-height: calc(100dvh - var(--head)); }
  dialog.dock-side::backdrop { background: transparent; }
}
header {
  display: flex;
  align-items: center;
  gap: var(--s2);
  padding: var(--s2) var(--s2) var(--s2) var(--s4);
  border-bottom: 1px solid var(--border);
  position: sticky;
  top: 0;
  background: var(--surface);
}
h2 { flex: 1; margin: 0; font-size: 1.05rem; }
.body {
  padding: var(--s4);
  overflow-y: auto;
  overscroll-behavior: contain;
  max-height: calc(var(--sheet-max) - var(--head));
  padding-bottom: calc(var(--s4) + env(safe-area-inset-bottom));
}
button.close {
  display: grid;
  place-items: center;
  width: 2.75rem;
  height: 2.75rem;
  color: inherit;
  background: transparent;
  border: 1px solid transparent;
  border-radius: var(--radius);
  cursor: pointer;
}
button.close:hover { background: var(--surface-2); }
button.close svg { width: 1.5rem; height: 1.5rem; }
button.close:focus-visible { outline: 3px solid var(--focus); outline-offset: 2px; }
`);

export class Sheet extends HTMLElement {
  /** @type {HTMLDialogElement} */
  #dialog;
  /** @type {HTMLElement} */
  #body;
  /** @type {HTMLElement} */
  #title;

  constructor() {
    super();
    const root = this.attachShadow({ mode: 'open' });
    root.adoptedStyleSheets = [css];
    this.#dialog = document.createElement('dialog');
    const header = document.createElement('header');
    this.#title = document.createElement('h2');
    this.#title.id = 'sheet-title';
    this.#dialog.setAttribute('aria-labelledby', this.#title.id);
    const close = iconButton('close', 'Close', () => this.close());
    close.className = 'close';
    header.append(this.#title, close);
    this.#body = document.createElement('div');
    this.#body.className = 'body';
    this.#dialog.append(header, this.#body);
    root.append(this.#dialog);
    this.#dialog.addEventListener('close', () =>
      this.dispatchEvent(new CustomEvent('sheet-close')));
    // click on the backdrop area closes
    this.#dialog.addEventListener('click', (e) => {
      if (e.target === this.#dialog) this.close();
    });
  }

  /** @param {string} v */
  set heading(v) { this.#title.textContent = v; }

  /** @param {...Node} nodes */
  setContent(...nodes) { this.#body.replaceChildren(...nodes); }

  get body() { return this.#body; }

  /** @param {string} name */
  dock(name) {
    this.#dialog.classList.toggle('dock-side', name === 'side');
    this.#dialog.classList.toggle('dock-compact', name === 'compact');
  }

  open() {
    if (!this.#dialog.open) this.#dialog.showModal();
  }

  close() {
    if (this.#dialog.open) this.#dialog.close();
  }

  get isOpen() { return this.#dialog.open; }
}

customElements.define('bs-sheet', Sheet);

/**
 * Create a sheet, append it to `host`, fill it and open it.
 * @param {Element} host
 * @param {string} heading
 * @param {Node|Node[]} content
 * @param {{dock?:'bottom'|'side'|'compact'}} [opts]
 * @returns {Sheet}
 */
export function openSheet(host, heading, content, opts = {}) {
  const s = /** @type {Sheet} */ (document.createElement('bs-sheet'));
  s.heading = heading;
  host.append(s);
  s.dock(opts.dock || 'bottom');
  s.setContent(...(Array.isArray(content) ? content : [content]));
  s.addEventListener('sheet-close', () => s.remove());
  s.open();
  return s;
}
