/**
 * Loading / empty / error blocks. Every view uses these so the three states
 * look and read the same everywhere. Status is never conveyed by color alone:
 * each block has an icon and a text heading.
 */

import { icon } from './icons.js';
import { ApiError } from '../api.js';

/** @param {string} [label] */
export function loadingView(label = 'Loading') {
  const d = document.createElement('div');
  d.className = 'state';
  d.setAttribute('role', 'status');
  d.innerHTML = '<div class="spinner"></div>';
  const p = document.createElement('p');
  p.textContent = label + '...';
  d.append(p);
  return d;
}

/** A grid of placeholder cards while items load. @param {number} n */
export function skeletonGrid(n = 12) {
  const ul = document.createElement('ul');
  ul.className = 'grid-items';
  ul.setAttribute('aria-hidden', 'true');
  for (let i = 0; i < n; i++) {
    const li = document.createElement('li');
    const box = document.createElement('div');
    box.className = 'skeleton';
    box.style.aspectRatio = '2 / 3';
    const line = document.createElement('div');
    line.className = 'skeleton';
    line.style.height = '0.9rem';
    line.style.marginTop = 'var(--s2)';
    li.append(box, line);
    ul.append(li);
  }
  return ul;
}

/**
 * @param {string} title
 * @param {string} [body]
 * @param {{label:string, href?:string, onClick?:() => void}} [action]
 */
export function emptyView(title, body = '', action) {
  const d = document.createElement('div');
  d.className = 'state';
  d.append(icon('book', { size: '2.5rem' }));
  const h = document.createElement('h2');
  h.textContent = title;
  d.append(h);
  if (body) {
    const p = document.createElement('p');
    p.textContent = body;
    d.append(p);
  }
  if (action) d.append(actionEl(action));
  return d;
}

/**
 * @param {unknown} err
 * @param {() => void} [retry]
 */
export function errorView(err, retry) {
  const d = document.createElement('div');
  d.className = 'state state--error';
  d.setAttribute('role', 'alert');
  d.append(icon('warn', { size: '2.5rem' }));
  const h = document.createElement('h2');
  h.textContent = 'Something went wrong';
  d.append(h);
  const p = document.createElement('p');
  p.textContent = errorMessage(err);
  d.append(p);
  if (retry) d.append(actionEl({ label: 'Try again', onClick: retry }));
  return d;
}

/** @param {unknown} err */
export function errorMessage(err) {
  if (err instanceof ApiError) {
    if (err.status === 403) return 'You do not have access to this.';
    if (err.status === 404) return 'Not found.';
    if (err.status === 0) return err.message;
    return err.message || `Request failed (${err.status}).`;
  }
  if (err instanceof Error) return err.message;
  return String(err);
}

/** @param {{label:string, href?:string, onClick?:() => void}} action */
function actionEl(action) {
  const el = document.createElement(action.href ? 'a' : 'button');
  el.className = 'btn btn--primary';
  el.textContent = action.label;
  if (action.href) el.setAttribute('href', action.href);
  else {
    /** @type {HTMLButtonElement} */ (el).type = 'button';
    if (action.onClick) el.addEventListener('click', action.onClick);
  }
  return el;
}

/**
 * Render an async section into a container with loading/error handling.
 * @param {Element} host
 * @param {() => Promise<Element>} build
 */
export async function section(host, build) {
  host.replaceChildren(loadingView());
  try {
    host.replaceChildren(await build());
  } catch (e) {
    host.replaceChildren(errorView(e, () => section(host, build)));
  }
}
