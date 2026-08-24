/**
 * Library browser. `/library` lists every library the user can see plus the
 * combined item grid; `/library/{id}` scopes the grid to one library.
 * Filters (kind, sort) live in the query string so they survive reload.
 */

import { api } from '../api.js';
import { page } from '../components/page.js';
import { itemGrid } from '../components/item-card.js';
import { emptyView, errorView, skeletonGrid } from '../components/states.js';
import { navigate, router } from '../router.js';
import { addBooksButton } from '../components/add-books.js';

const PAGE_SIZE = 60;

/** @param {import('../router.js').RouteCtx} ctx */
export default async function library(ctx) {
  const libraryId = ctx.params.id || '';
  const kind = ctx.query.get('kind') || '';
  const sort = ctx.query.get('sort') || 'title';

  const { el, body } = page(libraryId ? 'Library' : 'Library');
  const controls = document.createElement('div');
  controls.className = 'row';
  controls.style.marginBottom = 'var(--s4)';
  el.insertBefore(controls, body);

  const results = document.createElement('div');
  body.append(results);
  results.replaceChildren(skeletonGrid());

  /** @param {Record<string,string>} patch */
  const go = (patch) => {
    const q = new URLSearchParams(ctx.query);
    for (const [k, v] of Object.entries(patch)) {
      if (v) q.set(k, v); else q.delete(k);
    }
    navigate(`${libraryId ? `/library/${libraryId}` : '/library'}${q.toString() ? '?' + q : ''}`);
  };

  /**
   * @param {string} label
   * @param {string} value
   * @param {[string,string][]} options
   * @param {(v:string) => void} onChange
   */
  const select = (label, value, options, onChange) => {
    const wrap = document.createElement('label');
    wrap.className = 'field';
    wrap.style.margin = '0';
    wrap.style.minWidth = '10rem';
    const l = document.createElement('span');
    l.className = 'label';
    l.textContent = label;
    const s = document.createElement('select');
    for (const [v, t] of options) {
      const o = document.createElement('option');
      o.value = v;
      o.textContent = t;
      if (v === value) o.selected = true;
      s.append(o);
    }
    s.addEventListener('change', () => onChange(s.value));
    wrap.append(l, s);
    return wrap;
  };

  try {
    const libs = await api.libraries();
    const list = libs?.items || [];
    // "Add books" appears only for an account that may; the button builder
    // answers null otherwise rather than offering something that cannot work.
    const add = addBooksButton({
      libraries: list,
      libraryId,
      onAdded: () => router.refresh(),
    });
    if (add) {
      const sp = document.createElement('span');
      sp.className = 'spacer';
      el.firstElementChild?.append(sp, add);
    }
    controls.append(
      select('Library', libraryId, [
        ['', 'All libraries'],
        ...list.map((l) => [String(l.id), l.name]),
      ], (v) => {
        const q = new URLSearchParams(ctx.query);
        navigate(`${v ? `/library/${v}` : '/library'}${q.toString() ? '?' + q : ''}`);
      }),
      select('Type', kind, [['', 'All'], ['ebook', 'Ebooks'], ['audiobook', 'Audiobooks']],
        (v) => go({ kind: v })),
      select('Sort', sort, [
        ['title', 'Title'], ['author', 'Author'], ['added', 'Date added'], ['recent', 'Recently read'],
      ], (v) => go({ sort: v })),
    );
  } catch (e) {
    controls.append(errorView(e));
  }

  try {
    const data = await api.items({ library: libraryId, kind, sort, limit: PAGE_SIZE });
    const items = data?.items || [];
    if (!items.length) {
      results.replaceChildren(emptyView('No books here', 'Try a different filter, or scan a library.'));
    } else {
      const grid = itemGrid(items);
      results.replaceChildren(grid);
      if ((data.total ?? items.length) > items.length) {
        results.append(moreButton(results, grid, data.total, items.length, { library: libraryId, kind, sort }));
      }
    }
  } catch (e) {
    results.replaceChildren(errorView(e, () => router.refresh()));
  }

  return { el, title: 'Library' };
}

/**
 * @param {HTMLElement} host
 * @param {HTMLElement} grid
 * @param {number} total
 * @param {number} loaded
 * @param {Record<string,string>} params
 */
function moreButton(host, grid, total, loaded, params) {
  const btn = document.createElement('button');
  btn.type = 'button';
  btn.className = 'btn';
  btn.style.marginTop = 'var(--s4)';
  let offset = loaded;
  const label = () => { btn.textContent = `Show more (${offset} of ${total})`; };
  label();
  btn.addEventListener('click', async () => {
    btn.disabled = true;
    btn.textContent = 'Loading...';
    try {
      const data = await api.items({ ...params, limit: 60, offset });
      const more = data?.items || [];
      const extra = itemGrid(more);
      grid.append(...extra.children);
      offset += more.length;
      if (offset >= total) btn.remove();
      else { btn.disabled = false; label(); }
    } catch {
      btn.disabled = false;
      btn.textContent = 'Retry';
    }
  });
  return btn;
}
