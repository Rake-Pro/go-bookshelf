/** Series index and a single series, ordered by sequence. */

import { api } from '../api.js';
import { page } from '../components/page.js';
import { itemGrid } from '../components/item-card.js';
import { emptyView, errorView, loadingView } from '../components/states.js';

/** @param {import('../router.js').RouteCtx} ctx */
export default async function series(ctx) {
  return ctx.params.id ? one(ctx.params.id) : index();
}

async function index() {
  const { el, body } = page('Series');
  body.replaceChildren(loadingView());
  try {
    const data = await api.seriesList({ limit: 500 });
    const list = data?.items || [];
    if (!list.length) {
      body.replaceChildren(emptyView('No series yet', 'Series come from book metadata during a scan.'));
      return { el, title: 'Series' };
    }
    const ul = document.createElement('ul');
    ul.className = 'linklist';
    for (const s of list) {
      const li = document.createElement('li');
      const link = document.createElement('a');
      link.href = `/series/${encodeURIComponent(String(s.id))}`;
      const name = document.createElement('span');
      name.textContent = s.name;
      const count = document.createElement('span');
      count.className = 'muted small spacer';
      count.style.textAlign = 'right';
      count.textContent = s.item_count != null ? `${s.item_count} book${s.item_count === 1 ? '' : 's'}` : '';
      link.append(name, count);
      li.append(link);
      ul.append(li);
    }
    body.replaceChildren(ul);
  } catch (e) {
    body.replaceChildren(errorView(e, () => index()));
  }
  return { el, title: 'Series' };
}

/** @param {string} id */
async function one(id) {
  const { el, body } = page('Series');
  body.replaceChildren(loadingView());
  try {
    // GET /series/{id} -> {series:{id,name,...}, items:[...], total}
    const data = await api.series(id);
    const name = data?.series?.name || 'Series';
    const heading = el.querySelector('h1');
    if (heading) heading.textContent = name;
    // Each item carries its own position as `series.sequence`.
    const items = (data?.items || []).slice()
      .sort((a, b) => (a.series?.sequence ?? 0) - (b.series?.sequence ?? 0));
    body.replaceChildren(items.length
      ? itemGrid(items)
      : emptyView('No books', 'This series is empty.'));
    return { el, title: name };
  } catch (e) {
    body.replaceChildren(errorView(e, () => one(id)));
    return { el, title: 'Series' };
  }
}
