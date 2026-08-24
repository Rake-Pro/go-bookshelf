/**
 * Item detail: cover, metadata, primary Read/Listen action, progress, blurb and
 * (for audiobooks) the chapter list.
 *
 * Everything the book supplied is rendered with textContent, never innerHTML.
 */

import { api, coverUrl, downloadUrl } from '../api.js';
import { loadingView, errorView, emptyView, errorMessage } from '../components/states.js';
import { icon } from '../components/icons.js';
import { confirmDialog } from '../components/confirm.js';
import { duration, clock, peopleOf, seriesOf, percent, date, bytes } from '../format.js';
import { player } from '../player.js';
import { navigate } from '../router.js';
import { store } from '../store.js';
import { announce } from '../live.js';

/** @param {import('../router.js').RouteCtx} ctx */
export default async function itemView(ctx) {
  const el = document.createElement('div');
  el.replaceChildren(loadingView());

  let item;
  try {
    item = await api.item(ctx.params.id);
  } catch (e) {
    el.replaceChildren(errorView(e, () => navigate(location.pathname, { replace: true })));
    return { el, title: 'Item' };
  }
  if (!item) {
    el.replaceChildren(emptyView('Not found', 'This item is no longer in the library.'));
    return { el, title: 'Not found' };
  }

  const isAudio = item.kind === 'audiobook';
  const authors = peopleOf(item, 'author');
  const narrators = peopleOf(item, 'narrator');
  const frac = item.progress?.percent ?? 0;
  const finished = Boolean(item.progress?.finished_at);

  const hero = document.createElement('div');
  hero.className = 'item-hero';

  const cover = document.createElement('img');
  cover.className = 'cover';
  cover.src = coverUrl(item.id, 'full');
  cover.alt = `Cover of ${item.title}`;
  cover.loading = 'eager';
  cover.addEventListener('error', () => { cover.style.visibility = 'hidden'; });

  const info = document.createElement('div');

  const h1 = document.createElement('h1');
  h1.textContent = item.title || 'Untitled';
  h1.tabIndex = -1;
  info.append(h1);

  if (item.subtitle) {
    const sub = document.createElement('p');
    sub.className = 'muted';
    sub.textContent = item.subtitle;
    info.append(sub);
  }

  const dl = document.createElement('dl');
  dl.className = 'meta-list';
  /** @param {string} label @param {Node|string} value */
  const meta = (label, value) => {
    if (!value || (typeof value === 'string' && !value.trim())) return;
    const row = document.createElement('div');
    const dt = document.createElement('dt');
    dt.textContent = label;
    const dd = document.createElement('dd');
    if (typeof value === 'string') dd.textContent = value;
    else dd.append(value);
    row.append(dt, dd);
    dl.append(row);
  };

  meta('Author', peopleLinks(authors, 'authors'));
  if (narrators.length) meta('Narrator', peopleLinks(narrators, 'authors'));
  const seriesRefs = seriesOf(item);
  if (seriesRefs.length) meta('Series', seriesLinks(seriesRefs));
  if (isAudio) meta('Duration', duration(item.duration_ms));
  meta('Published', date(item.published));
  meta('Publisher', item.publisher || '');
  meta('Language', item.language || '');
  if (item.size_bytes) meta('Size', bytes(item.size_bytes));
  info.append(dl);

  /* primary action + progress */
  const actions = document.createElement('div');
  actions.className = 'row';
  const primary = document.createElement('a');
  primary.className = 'btn btn--primary btn--lg';
  primary.href = `${isAudio ? '/listen' : '/read'}/${encodeURIComponent(item.id)}`;
  primary.append(icon(isAudio ? 'headphones' : 'book'));
  const plabel = document.createElement('span');
  plabel.textContent = frac > 0 && !finished
    ? (isAudio ? 'Resume listening' : 'Resume reading')
    : (isAudio ? 'Listen' : 'Read');
  primary.append(plabel);
  actions.append(primary);

  const dl2 = document.createElement('a');
  dl2.className = 'btn';
  dl2.href = downloadUrl(item.id);
  dl2.setAttribute('download', '');
  dl2.textContent = 'Download';
  actions.append(dl2);

  // Deletion is an administrator power, like adding or scanning a library, so
  // a plain user never sees a control it cannot use.
  const deleteError = document.createElement('div');
  deleteError.className = 'formerror';
  deleteError.setAttribute('role', 'alert');
  deleteError.hidden = true;
  if (store.isAdmin) {
    const del = document.createElement('button');
    del.type = 'button';
    del.className = 'btn btn--danger';
    del.append(icon('close'));
    const delLabel = document.createElement('span');
    delLabel.textContent = 'Delete';
    del.append(delLabel);
    del.addEventListener('click', async () => {
      const ok = await confirmDialog(el, {
        heading: 'Delete book',
        message: `Delete "${item.title}"? This removes its file(s) from disk. This cannot be undone.`,
        confirmLabel: 'Delete',
        danger: true,
      });
      if (!ok) return;
      del.disabled = true;
      deleteError.hidden = true;
      try {
        await api.deleteItem(item.id);
        announce(`Deleted ${item.title}`);
        navigate('/library', { replace: true });
      } catch (e) {
        del.disabled = false;
        deleteError.replaceChildren(icon('warn'), document.createTextNode(errorMessage(e)));
        deleteError.hidden = false;
      }
    });
    actions.append(del);
  }
  info.append(actions, deleteError);

  if (frac > 0 || finished) {
    const prog = document.createElement('p');
    prog.className = 'small muted';
    prog.style.marginTop = 'var(--s3)';
    prog.textContent = finished
      ? 'Finished'
      : isAudio
        ? `${percent(frac)} - ${clock(item.progress.position_ms || 0)} of ${clock(item.duration_ms || 0)}`
        : `${percent(frac)} read`;
    info.append(prog);
  }

  hero.append(cover, info);
  el.replaceChildren(hero);

  if (item.description) {
    const h2 = document.createElement('h2');
    h2.textContent = 'Description';
    h2.style.marginTop = 'var(--s8)';
    const p = document.createElement('p');
    p.style.maxWidth = '60ch';
    p.textContent = item.description;
    el.append(h2, p);
  }

  if (isAudio) {
    const chapters = chapterList(item);
    if (chapters) el.append(chapters);
  }

  if (item.tags?.length) {
    const h2 = document.createElement('h2');
    h2.textContent = 'Tags';
    h2.style.marginTop = 'var(--s8)';
    const row = document.createElement('div');
    row.className = 'row';
    for (const t of item.tags) {
      const a = document.createElement('a');
      a.className = 'btn';
      a.href = `/library?tag=${encodeURIComponent(t.name)}`;
      a.textContent = t.name;
      row.append(a);
    }
    el.append(h2, row);
  }

  return { el, title: item.title || 'Item' };
}

/** @param {{id?:string,name:string}[]} people @param {string} base */
function peopleLinks(people, base) {
  const frag = document.createDocumentFragment();
  people.forEach((p, i) => {
    if (i) frag.append(document.createTextNode(', '));
    if (p.id != null) {
      const a = document.createElement('a');
      a.href = `/${base}/${encodeURIComponent(String(p.id))}`;
      a.textContent = p.name;
      frag.append(a);
    } else {
      frag.append(document.createTextNode(p.name));
    }
  });
  return frag;
}

/** @param {{id?:string,name:string,sequence?:number}[]} list */
function seriesLinks(list) {
  const frag = document.createDocumentFragment();
  list.forEach((s, i) => {
    if (i) frag.append(document.createTextNode(', '));
    const a = document.createElement('a');
    a.href = `/series/${encodeURIComponent(String(s.id))}`;
    a.textContent = s.sequence ? `${s.name} #${s.sequence}` : s.name;
    frag.append(a);
  });
  return frag;
}

/** Chapter list with absolute start times, computed the same way as the player. */
function chapterList(item) {
  const files = (item.files || []).slice().sort((a, b) => (a.seq ?? 0) - (b.seq ?? 0));
  /** @type {Map<string, number>} */
  const offsets = new Map();
  let acc = 0;
  for (const f of files) { offsets.set(f.id, acc); acc += f.duration_ms || 0; }

  const rows = (item.chapters || []).map((c, i) => ({
    title: c.title || `Chapter ${i + 1}`,
    start: (offsets.get(c.file_id) ?? 0) + (c.start_ms || 0),
    end: c.end_ms != null ? (offsets.get(c.file_id) ?? 0) + c.end_ms : null,
  })).sort((a, b) => a.start - b.start);
  if (!rows.length) return null;

  const wrap = document.createElement('section');
  const h2 = document.createElement('h2');
  h2.id = 'chapters';
  h2.textContent = `Chapters (${rows.length})`;
  h2.style.marginTop = 'var(--s8)';
  const list = document.createElement('div');
  list.setAttribute('role', 'list');
  list.setAttribute('aria-labelledby', 'chapters');

  rows.forEach((c, i) => {
    const b = document.createElement('button');
    b.type = 'button';
    b.className = 'chapter-row';
    b.setAttribute('role', 'listitem');
    const len = c.end ? c.end - c.start : 0;
    b.setAttribute('aria-label',
      `Play chapter ${i + 1}, ${c.title}${len ? `, ${duration(len)}` : ''}`);
    const n = document.createElement('span');
    n.className = 'num';
    n.textContent = String(i + 1);
    const t = document.createElement('span');
    t.textContent = c.title;
    const d = document.createElement('span');
    d.className = 'dur';
    d.textContent = len ? duration(len) : clock(c.start);
    b.append(n, t, d);
    b.addEventListener('click', async () => {
      await player.load(item, { startMs: c.start, autoplay: true });
      navigate(`/listen/${encodeURIComponent(item.id)}`);
    });
    list.append(b);
  });

  wrap.append(h2, list);
  return wrap;
}
