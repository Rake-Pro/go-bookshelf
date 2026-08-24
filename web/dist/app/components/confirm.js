/**
 * A confirmation dialog built on the shared sheet: names what is being acted
 * on and asks for one explicit click before anything destructive happens.
 */

import { openSheet } from './sheet.js';

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

    const sheet = openSheet(host, opts.heading, body);
    // Escape and the backdrop close the sheet without going through finish();
    // that must count as a cancel, not leave the promise unsettled.
    sheet.addEventListener('sheet-close', () => { if (!settled) { settled = true; resolve(false); } });
  });
}
