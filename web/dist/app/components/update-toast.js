/**
 * <bs-update-toast> - the "a new version is ready" banner.
 *
 * Shown once main.js sees a service worker waiting to take over. Deliberately
 * not a modal and not automatic: an audiobook mid-play must never be
 * interrupted by a background deploy, so the update sits until the person
 * chooses to act on it.
 */

const css = new CSSStyleSheet();
css.replaceSync(`
:host { display: contents; }
.bar {
  position: fixed;
  z-index: 90;
  left: 50%;
  top: var(--s3);
  top: calc(var(--s3) + env(safe-area-inset-top));
  transform: translateX(-50%);
  display: flex;
  align-items: center;
  gap: var(--s3);
  max-width: calc(100vw - 2 * var(--s4));
  padding: var(--s3) var(--s3) var(--s3) var(--s4);
  color: var(--text);
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  box-shadow: var(--shadow);
}
p { margin: 0; font-size: 0.92rem; }
button {
  flex: none;
  min-height: var(--tap);
  padding: 0 var(--s4);
  font: inherit;
  font-weight: 600;
  color: var(--accent-text);
  background: var(--accent);
  border: 1px solid transparent;
  border-radius: var(--radius-sm);
  cursor: pointer;
}
button:hover { filter: brightness(1.08); }
button:focus-visible { outline: 3px solid var(--focus); outline-offset: 2px; }
`);

export class UpdateToast extends HTMLElement {
  constructor() {
    super();
    const root = this.attachShadow({ mode: 'open' });
    root.adoptedStyleSheets = [css];
    const bar = document.createElement('div');
    bar.className = 'bar';
    bar.setAttribute('role', 'status');
    const p = document.createElement('p');
    p.textContent = 'Update available.';
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.textContent = 'Refresh';
    btn.addEventListener('click', () => this.dispatchEvent(new CustomEvent('toast-refresh')));
    bar.append(p, btn);
    root.append(bar);
  }
}

customElements.define('bs-update-toast', UpdateToast);

/**
 * Show the update toast, wired to call `onRefresh` when its button is
 * pressed. Safe to call more than once: an existing toast is left alone
 * rather than duplicated, since a second install-in-background before the
 * first is acted on names the same waiting worker anyway.
 *
 * @param {() => void} onRefresh
 */
export function showUpdateToast(onRefresh) {
  if (document.querySelector('bs-update-toast')) return;
  const el = /** @type {UpdateToast} */ (document.createElement('bs-update-toast'));
  el.addEventListener('toast-refresh', onRefresh, { once: true });
  document.body.append(el);
}
