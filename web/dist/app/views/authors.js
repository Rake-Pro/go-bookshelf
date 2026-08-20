/** Authors index and a single author's books. */

import { api } from '../api.js';
import { page } from '../components/page.js';
import { itemGrid } from '../components/item-card.js';
import { emptyView, errorView, loadingView } from '../components/states.js';

/** @param {import('../router.js').RouteCtx} ctx */
export default async function authors(ctx) {
  return ctx.params.id ? one(ctx.params.id) : index();
}

async function index() {
  const { el, body } = page('Authors');
  body.replaceChildren(loadingView());
  try {
    const data = await api.authors({ limit: 500 });
    const list = data?.items || [];
    if (!list.length) {
      body.replaceChildren(emptyView('No authors yet', 'Scan a library to populate this list.'));
      return { el, title: 'Authors' };
    }
    const ul = document.createElement('ul');
    ul.className = 'linklist';
    for (const a of list) {
      const li = document.createElement('li');
      const link = document.createElement('a');
      link.href = `/authors/${encodeURIComponent(String(a.id))}`;
      const name = document.createElement('span');
      name.textContent = a.name;
      const count = document.createElement('span');
      count.className = 'muted small spacer';
      count.style.textAlign = 'right';
      count.textContent = a.item_count != null ? `${a.item_count} book${a.item_count === 1 ? '' : 's'}` : '';
      link.append(name, count);
      li.append(link);
      ul.append(li);
    }
    body.replaceChildren(ul);
  } catch (e) {
    body.replaceChildren(errorView(e, () => index()));
  }
  return { el, title: 'Authors' };
}

/** @param {string} id */
async function one(id) {
  const { el, body } = page('Author');
  body.replaceChildren(loadingView());
  try {
    // GET /authors/{id} -> {author:{id,name,...}, items:[...], total}
    const data = await api.author(id);
    const name = data?.author?.name || 'Author';
    const heading = el.querySelector('h1');
    if (heading) heading.textContent = name;
    const items = data?.items || [];
    body.replaceChildren(items.length
      ? itemGrid(items)
      : emptyView('No books', 'This author has no books you can see.'));
    return { el, title: name };
  } catch (e) {
    body.replaceChildren(errorView(e, () => one(id)));
    return { el, title: 'Author' };
  }
}
