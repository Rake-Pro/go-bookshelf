/**
 * Search. Results are grouped (items / authors / series), each group a list
 * envelope, per the API. The query
 * lives in the URL so a search can be shared and reloaded, and results are
 * announced to assistive technology.
 */

import { api } from '../api.js';
import { page } from '../components/page.js';
import { itemGrid } from '../components/item-card.js';
import { emptyView, errorView, loadingView } from '../components/states.js';
import { navigate } from '../router.js';
import { announce } from '../live.js';

/** @param {import('../router.js').RouteCtx} ctx */
export default async function search(ctx) {
  const q = ctx.query.get('q') || '';
  const { el, body } = page('Search');

  const form = document.createElement('form');
  form.setAttribute('role', 'search');
  form.className = 'row';
  form.style.marginBottom = 'var(--s6)';

  const label = document.createElement('label');
  label.className = 'visually-hidden';
  label.setAttribute('for', 'q');
  label.textContent = 'Search books, authors and series';

  const input = document.createElement('input');
  input.type = 'search';
  input.id = 'q';
  input.name = 'q';
  input.value = q;
  input.placeholder = 'Title, author or series';
  input.autocomplete = 'off';
  input.style.flex = '1 1 16rem';

  const submit = document.createElement('button');
  submit.type = 'submit';
  submit.className = 'btn btn--primary';
  submit.textContent = 'Search';

  form.append(label, input, submit);
  form.addEventListener('submit', (e) => {
    e.preventDefault();
    const v = input.value.trim();
    navigate('/search' + (v ? `?q=${encodeURIComponent(v)}` : ''));
  });
  el.insertBefore(form, body);

  if (!q) {
    body.replaceChildren(emptyView('Search your library', 'Type a title, author or series name.'));
    queueMicrotask(() => input.focus());
    return { el, title: 'Search' };
  }

  body.replaceChildren(loadingView('Searching'));
  try {
    const data = await api.search(q);
    // Each group is a standard list envelope: {items:[...], total:n}.
    const items = data?.items?.items || [];
    const people = data?.authors?.items || [];
    const seriesList = data?.series?.items || [];
    const total = items.length + people.length + seriesList.length;
    announce(total ? `${total} results for ${q}` : `No results for ${q}`);

    if (!total) {
      body.replaceChildren(emptyView('No results', `Nothing matched "${q}".`));
      return { el, title: `Search: ${q}` };
    }

    const frag = document.createDocumentFragment();
    if (items.length) {
      frag.append(heading('Books', 'search-books'), itemGrid(items, { labelledBy: 'search-books' }));
    }
    if (people.length) frag.append(heading('Authors', 'search-authors'), links(people, '/authors'));
    if (seriesList.length) frag.append(heading('Series', 'search-series'), links(seriesList, '/series'));
    body.replaceChildren(frag);
  } catch (e) {
    body.replaceChildren(errorView(e, () => search(ctx)));
  }
  return { el, title: `Search: ${q}` };
}

/** @param {string} text @param {string} id */
function heading(text, id) {
  const h = document.createElement('h2');
  h.id = id;
  h.textContent = text;
  h.style.marginTop = 'var(--s6)';
  return h;
}

/** @param {{id:string,name:string}[]} list @param {string} base */
function links(list, base) {
  const ul = document.createElement('ul');
  ul.className = 'linklist';
  for (const x of list) {
    const li = document.createElement('li');
    const a = document.createElement('a');
    a.href = `${base}/${encodeURIComponent(String(x.id))}`;
    a.textContent = x.name;
    li.append(a);
    ul.append(li);
  }
  return ul;
}
