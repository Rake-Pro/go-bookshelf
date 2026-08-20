/**
 * Modal bottom sheet built on <dialog>, which gives focus trapping, Escape and
 * inert background for free. On wide screens it centers as a panel instead.
 */

import { iconButton } from './icons.js';

const css = new CSSStyleSheet();
css.replaceSync(`
:host { display: contents; }
dialog {
  width: 100%;
  max-width: 34rem;
  max-height: 85dvh;
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
  max-height: calc(85dvh - 3.5rem);
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
 * @returns {Sheet}
 */
export function openSheet(host, heading, content) {
  const s = /** @type {Sheet} */ (document.createElement('bs-sheet'));
  s.heading = heading;
  host.append(s);
  s.setContent(...(Array.isArray(content) ? content : [content]));
  s.addEventListener('sheet-close', () => s.remove());
  s.open();
  return s;
}
