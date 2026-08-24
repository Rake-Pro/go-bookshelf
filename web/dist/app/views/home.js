/** Home: continue reading/listening, recently added, series in progress. */

import { api } from '../api.js';
import { page, sectionHead } from '../components/page.js';
import { itemGrid } from '../components/item-card.js';
import { emptyView, skeletonGrid, errorView } from '../components/states.js';
import { store } from '../store.js';

/** @param {import('../router.js').RouteCtx} ctx */
export default async function home(ctx) {
  const name = store.user?.display_name || store.user?.username || '';
  const { el, body } = page('Home', { subtitle: name ? `Signed in as ${name}` : '' });
  await load(body);
  return { el, title: 'Home' };
}

/** @param {HTMLElement} body */
async function load(body) {
  body.replaceChildren(skeletonGrid(6, { rail: true }));

  /** @param {string} title @param {any[]} items @param {string} [href] */
  const rail = (title, items, href) => {
    if (!items?.length) return null;
    const frag = document.createDocumentFragment();
    const head = sectionHead(title, href ? { href } : {});
    frag.append(head.el, itemGrid(items, { rail: true, labelledBy: head.id }));
    return frag;
  };

  try {
    const data = await api.home();
    const parts = [
      rail('Continue', data?.continue || []),
      rail('Recently added', data?.recent || [], '/library'),
      seriesSection(data?.series_in_progress || []),
    ].filter(Boolean);

    if (!parts.length) {
      body.replaceChildren(emptyView(
        'Nothing here yet',
        'Once a library has been scanned, your books show up here.',
        store.isAdmin
          ? { label: 'Set up a library', href: '/admin' }
          : { label: 'Browse the library', href: '/library' },
      ));
    } else {
      body.replaceChildren(...parts);
    }
  } catch (e) {
    body.replaceChildren(errorView(e, () => load(body)));
  }
}

/**
 * `GET /home` returns series progress as `{series, finished, total, next_item}`
 * rather than plain items, so this row is a list of series with their standing,
 * not a cover rail.
 *
 * @param {{series:{id:number,name:string}, finished:number, total:number,
 *          next_item:any}[]} rows
 */
function seriesSection(rows) {
  if (!rows.length) return null;
  const frag = document.createDocumentFragment();
  const head = sectionHead('Series in progress', { href: '/series' });
  const ul = document.createElement('ul');
  ul.className = 'linklist';
  ul.setAttribute('aria-labelledby', head.id);

  for (const row of rows) {
    if (!row?.series) continue;
    const li = document.createElement('li');
    const link = document.createElement('a');
    link.href = `/series/${encodeURIComponent(String(row.series.id))}`;

    const name = document.createElement('span');
    name.textContent = row.series.name;

    const standing = document.createElement('span');
    standing.className = 'muted small spacer';
    standing.style.textAlign = 'right';
    const next = row.next_item?.title;
    standing.textContent = next
      ? `${row.finished} of ${row.total} finished - next: ${next}`
      : `${row.finished} of ${row.total} finished`;

    link.append(name, standing);
    link.setAttribute('aria-label', `${row.series.name}, ${standing.textContent}`);
    li.append(link);
    ul.append(li);
  }
  if (!ul.children.length) return null;
  frag.append(head.el, ul);
  return frag;
}
