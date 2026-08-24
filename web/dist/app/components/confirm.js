/**
 * A confirmation dialog built on the shared sheet: names what is being acted
 * on and asks for one explicit click before anything destructive happens.
 *
 * The sheet renders inside `<bs-sheet>`, whose shadow root the page
 * stylesheet does not reach, so the styles this needs travel with it (same
 * pattern as components/add-books.js).
 */

import { openSheet } from './sheet.js';

const STYLES = `
.row { display: flex; align-items: center; gap: var(--s3); flex-wrap: wrap; }
.btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: var(--s2);
  min-height: var(--tap);
  min-width: var(--tap);
  padding: var(--s2) var(--s4);
  font: inherit;
  font-weight: 600;
  color: var(--text);
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  cursor: pointer;
  transition: background var(--motion) ease, border-color var(--motion) ease;
}
.btn:hover { background: var(--surface-2); }
.btn:disabled { opacity: 0.55; cursor: not-allowed; }
.btn:focus-visible { outline: 3px solid var(--focus); outline-offset: 2px; }
.btn--primary { color: var(--accent-text); background: var(--accent); border-color: var(--accent); }
.btn--primary:hover { background: var(--accent); filter: brightness(1.08); }
.btn--danger { color: var(--danger); border-color: var(--danger); }
`;

/**
 * @param {Element} host
 * @param {{heading:string, message:string, confirmLabel?:string, danger?:boolean}} opts
 * @returns {Promise<boolean>} resolves true only if the person clicked confirm
 */
export function confirmDialog(host, opts) {
  return new Promise((resolve) => {
    let settled = false;
    /** @param {boolean} v */
    const finish = (v) => {
      if (settled) return;
      settled = true;
      resolve(v);
      sheet.close();
    };

    const style = document.createElement('style');
    style.textContent = STYLES;

    const body = document.createElement('div');
    const p = document.createElement('p');
    p.textContent = opts.message;

    const actions = document.createElement('div');
    actions.className = 'row';
    actions.style.marginTop = 'var(--s4)';
    actions.style.justifyContent = 'flex-end';

    const cancel = document.createElement('button');
    cancel.type = 'button';
    cancel.className = 'btn';
    cancel.textContent = 'Cancel';
    cancel.addEventListener('click', () => finish(false));

    const confirmBtn = document.createElement('button');
    confirmBtn.type = 'button';
    confirmBtn.className = opts.danger ? 'btn btn--danger' : 'btn btn--primary';
    confirmBtn.textContent = opts.confirmLabel || 'Confirm';
    confirmBtn.addEventListener('click', () => finish(true));

    actions.append(cancel, confirmBtn);
    body.append(p, actions);

    const sheet = openSheet(host, opts.heading, [style, body]);
    // Escape and the backdrop close the sheet without going through finish();
    // that must count as a cancel, not leave the promise unsettled.
    sheet.addEventListener('sheet-close', () => { if (!settled) { settled = true; resolve(false); } });
  });
}
