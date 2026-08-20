/**
 * Admin: libraries (create, paths, scan + scan status) and users.
 * Non-admins get a clear refusal instead of a broken page.
 */

import { api } from '../api.js';
import { store } from '../store.js';
import { page } from '../components/page.js';
import { emptyView, errorView, errorMessage, loadingView } from '../components/states.js';
import { icon } from '../components/icons.js';
import { date, bytes } from '../format.js';
import { announce } from '../live.js';

/** @param {import('../router.js').RouteCtx} ctx */
export default async function admin(ctx) {
  const settingsLink = document.createElement('a');
  settingsLink.className = 'btn';
  settingsLink.href = '/admin/settings';
  settingsLink.textContent = 'Settings';

  const { el, body } = page('Admin', { actions: [settingsLink] });

  if (!store.isAdmin) {
    body.replaceChildren(emptyView(
      'Admins only',
      'Your account does not have permission to manage this server.',
      { label: 'Go home', href: '/' },
    ));
    return { el, title: 'Admin' };
  }

  const libs = document.createElement('section');
  const users = document.createElement('section');
  const status = document.createElement('section');
  body.append(libs, users, status);

  renderLibraries(libs);
  renderUsers(users);
  renderStatus(status);

  return { el, title: 'Admin' };
}

/** @param {string} title */
function card(title) {
  const s = document.createElement('div');
  s.className = 'card';
  s.style.marginBottom = 'var(--s6)';
  const h = document.createElement('h2');
  h.textContent = title;
  s.append(h);
  return s;
}

/** @param {string} msg @param {'ok'|'error'} kind */
function flash(msg, kind = 'ok') {
  const p = document.createElement('p');
  p.setAttribute('role', 'status');
  p.className = 'row small';
  p.style.color = kind === 'ok' ? 'var(--ok)' : 'var(--danger)';
  p.append(icon(kind === 'ok' ? 'check' : 'warn'), document.createTextNode(msg));
  announce(msg);
  return p;
}

/* ---------------- libraries ---------------- */

/** @param {HTMLElement} host */
async function renderLibraries(host) {
  host.replaceChildren(loadingView('Loading libraries'));
  let data;
  try {
    data = await api.libraries();
  } catch (e) {
    host.replaceChildren(errorView(e, () => renderLibraries(host)));
    return;
  }

  const c = card('Libraries');
  const list = data?.items || [];

  if (!list.length) {
    c.append(emptyView('No libraries yet', 'Add one below and point it at a folder of books.'));
  } else {
    const ul = document.createElement('ul');
    ul.className = 'linklist';
    for (const lib of list) ul.append(libraryRow(lib, host));
    c.append(ul);
  }

  c.append(createLibraryForm(host));
  host.replaceChildren(c);
}

/** @param {any} lib @param {HTMLElement} host */
function libraryRow(lib, host) {
  const li = document.createElement('li');
  li.style.padding = 'var(--s3) 0';
  li.style.borderBottom = '1px solid var(--border)';

  const top = document.createElement('div');
  top.className = 'row';

  const name = document.createElement('div');
  const strong = document.createElement('div');
  strong.style.fontWeight = '600';
  strong.textContent = lib.name;
  const sub = document.createElement('div');
  sub.className = 'muted small';
  sub.textContent = `${lib.kind} - ${(lib.paths || []).join(', ') || 'no paths'}`;
  name.append(strong, sub);

  const sp = document.createElement('span');
  sp.className = 'spacer';

  const browse = document.createElement('a');
  browse.className = 'btn';
  browse.href = `/library/${encodeURIComponent(String(lib.id))}`;
  browse.textContent = 'Browse';

  const scan = document.createElement('button');
  scan.type = 'button';
  scan.className = 'btn btn--primary';
  scan.append(icon('refresh'));
  const scanLabel = document.createElement('span');
  scanLabel.textContent = 'Scan';
  scan.append(scanLabel);

  const result = document.createElement('div');
  result.style.marginTop = 'var(--s2)';

  scan.addEventListener('click', async () => {
    scan.disabled = true;
    scanLabel.textContent = 'Scanning...';
    result.replaceChildren();
    try {
      await api.scanLibrary(lib.id);
      result.replaceChildren(flash('Scan started.'));
      pollScans(lib.id, result);
    } catch (e) {
      result.replaceChildren(flash(errorMessage(e), 'error'));
    } finally {
      scan.disabled = false;
      scanLabel.textContent = 'Scan';
    }
  });

  top.append(name, sp, browse, scan);
  li.append(top, result);
  loadLastScan(lib.id, result);
  return li;
}

/** @param {string} id @param {HTMLElement} host */
async function loadLastScan(id, host) {
  try {
    const data = await api.scans(id);
    const last = (data?.items || [])[0];
    if (!last) return;
    host.replaceChildren(scanSummary(last));
  } catch { /* scan history is optional detail */ }
}

/** @param {any} s */
function scanSummary(s) {
  const p = document.createElement('p');
  p.className = 'muted small';
  p.style.margin = '0';
  const running = !s.finished_at;
  p.textContent = running
    ? `Scan running since ${date(s.started_at)}`
    : `Last scan ${date(s.finished_at)}: ${s.added ?? 0} added, ${s.updated ?? 0} updated, `
      + `${s.removed ?? 0} removed, ${s.errors ?? 0} errors`;
  return p;
}

/** Poll scan status until it finishes (or 60 tries, ~2 minutes). */
function pollScans(id, host) {
  let tries = 0;
  const tick = async () => {
    tries++;
    try {
      const data = await api.scans(id);
      const last = (data?.items || [])[0];
      if (last) host.replaceChildren(scanSummary(last));
      if (last && last.finished_at) {
        announce('Scan finished');
        return;
      }
    } catch { /* keep polling */ }
    if (tries < 60) setTimeout(tick, 2000);
  };
  setTimeout(tick, 2000);
}

/** @param {HTMLElement} host */
function createLibraryForm(host) {
  const details = document.createElement('details');
  details.style.marginTop = 'var(--s4)';
  const summary = document.createElement('summary');
  summary.className = 'btn';
  summary.style.display = 'inline-flex';
  summary.append(icon('plus'));
  const st = document.createElement('span');
  st.textContent = 'Add library';
  summary.append(st);
  details.append(summary);

  const form = document.createElement('form');
  form.style.marginTop = 'var(--s4)';
  form.noValidate = true;

  const name = textField('Name', 'name', 'e.g. Family ebooks');
  const kind = document.createElement('label');
  kind.className = 'field';
  const kl = document.createElement('span');
  kl.className = 'label';
  kl.textContent = 'Kind';
  const ks = document.createElement('select');
  for (const [v, t] of [['ebook', 'Ebooks'], ['audiobook', 'Audiobooks'], ['mixed', 'Mixed']]) {
    const o = document.createElement('option');
    o.value = v;
    o.textContent = t;
    ks.append(o);
  }
  ks.name = 'kind';
  kind.append(kl, ks);

  const paths = document.createElement('label');
  paths.className = 'field';
  const pl = document.createElement('span');
  pl.className = 'label';
  pl.textContent = 'Paths';
  const pt = document.createElement('textarea');
  pt.name = 'paths';
  pt.placeholder = '/media/books';
  const ph = document.createElement('span');
  ph.className = 'hint';
  ph.textContent = 'One absolute path per line, readable by the server.';
  paths.append(pl, pt, ph);

  const submit = document.createElement('button');
  submit.type = 'submit';
  submit.className = 'btn btn--primary';
  submit.textContent = 'Create library';

  const out = document.createElement('div');
  form.append(name, kind, paths, submit, out);

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const fd = new FormData(form);
    const body = {
      name: String(fd.get('name') || '').trim(),
      kind: String(fd.get('kind') || 'ebook'),
      paths: String(fd.get('paths') || '').split('\n').map((x) => x.trim()).filter(Boolean),
    };
    if (!body.name || !body.paths.length) {
      out.replaceChildren(flash('A name and at least one path are required.', 'error'));
      return;
    }
    submit.disabled = true;
    try {
      await api.createLibrary(body);
      renderLibraries(host);
      announce('Library created');
    } catch (err) {
      out.replaceChildren(flash(errorMessage(err), 'error'));
    } finally {
      submit.disabled = false;
    }
  });

  details.append(form);
  return details;
}

/* ---------------- users ---------------- */

/** @param {HTMLElement} host */
async function renderUsers(host) {
  host.replaceChildren(loadingView('Loading users'));
  let data;
  try {
    data = await api.users();
  } catch (e) {
    host.replaceChildren(errorView(e, () => renderUsers(host)));
    return;
  }
  const c = card('Users');
  const list = data?.items || [];

  const table = document.createElement('ul');
  table.className = 'linklist';
  for (const u of list) {
    const li = document.createElement('li');
    const row = document.createElement('div');
    row.className = 'row';
    row.style.padding = 'var(--s3) 0';
    row.style.borderBottom = '1px solid var(--border)';
    const who = document.createElement('div');
    const n = document.createElement('div');
    n.style.fontWeight = '600';
    n.textContent = u.display_name || u.username;
    const s = document.createElement('div');
    s.className = 'muted small';
    s.textContent = `${u.username} - ${u.role}${u.disabled_at ? ' - disabled' : ''}`;
    who.append(n, s);
    row.append(who);
    li.append(row);
    table.append(li);
  }
  if (list.length) c.append(table);
  else c.append(emptyView('No users', 'Add the first account below.'));

  c.append(createUserForm(host));
  host.replaceChildren(c);
}

/** @param {HTMLElement} host */
function createUserForm(host) {
  const details = document.createElement('details');
  details.style.marginTop = 'var(--s4)';
  const summary = document.createElement('summary');
  summary.className = 'btn';
  summary.style.display = 'inline-flex';
  summary.append(icon('plus'));
  const st = document.createElement('span');
  st.textContent = 'Add user';
  summary.append(st);
  details.append(summary);

  const form = document.createElement('form');
  form.style.marginTop = 'var(--s4)';
  form.noValidate = true;

  const roleWrap = document.createElement('label');
  roleWrap.className = 'field';
  const rl = document.createElement('span');
  rl.className = 'label';
  rl.textContent = 'Role';
  const rs = document.createElement('select');
  rs.name = 'role';
  for (const [v, t] of [['user', 'User'], ['restricted', 'Restricted'], ['admin', 'Admin']]) {
    const o = document.createElement('option');
    o.value = v;
    o.textContent = t;
    rs.append(o);
  }
  roleWrap.append(rl, rs);

  const submit = document.createElement('button');
  submit.type = 'submit';
  submit.className = 'btn btn--primary';
  submit.textContent = 'Create user';
  const out = document.createElement('div');

  form.append(
    textField('Username', 'username', ''),
    textField('Display name', 'display_name', ''),
    textField('Password', 'password', '', 'password'),
    roleWrap, submit, out,
  );

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const fd = new FormData(form);
    const body = {
      username: String(fd.get('username') || '').trim(),
      display_name: String(fd.get('display_name') || '').trim(),
      password: String(fd.get('password') || ''),
      role: String(fd.get('role') || 'user'),
    };
    if (!body.username || !body.password) {
      out.replaceChildren(flash('Username and password are required.', 'error'));
      return;
    }
    submit.disabled = true;
    try {
      await api.createUser(body);
      renderUsers(host);
      announce('User created');
    } catch (err) {
      out.replaceChildren(flash(errorMessage(err), 'error'));
    } finally {
      submit.disabled = false;
    }
  });

  details.append(form);
  return details;
}

/* ---------------- status ---------------- */

/** @param {HTMLElement} host */
async function renderStatus(host) {
  try {
    const s = await api.systemStatus();
    const c = card('Server');
    const dl = document.createElement('dl');
    dl.className = 'meta-list';
    /** @param {string} k @param {string} v */
    const row = (k, v) => {
      if (!v) return;
      const d = document.createElement('div');
      const dt = document.createElement('dt');
      dt.textContent = k;
      const dd = document.createElement('dd');
      dd.textContent = v;
      d.append(dt, dd);
      dl.append(d);
    };
    row('Version', s?.version || '');
    // Only a SQLite installation has a file to measure; a Postgres one is
    // named rather than shown with a blank size.
    row('Database', s?.db_driver === 'postgres'
      ? 'Postgres'
      : (s?.db_size_bytes ? 'SQLite, ' + bytes(s.db_size_bytes) : 'SQLite'));
    row('Ebooks', s?.counts?.ebooks != null ? String(s.counts.ebooks) : '');
    row('Audiobooks', s?.counts?.audiobooks != null ? String(s.counts.audiobooks) : '');
    row('Base URL', s?.base_url || '');
    row('Single sign-on', s?.oidc_enabled ? 'On' : 'Off');
    row('Password sign-in', s?.local_login === false ? 'Off' : 'On');
    row('Settings changed', s?.settings_updated_at || '');
    c.append(dl);

    const link = document.createElement('a');
    link.className = 'btn';
    link.href = '/admin/settings';
    link.textContent = 'Edit settings';
    c.append(link);
    host.replaceChildren(c);
  } catch {
    host.replaceChildren();
  }
}

/**
 * @param {string} label @param {string} name @param {string} placeholder
 * @param {string} [type]
 */
function textField(label, name, placeholder, type = 'text') {
  const wrap = document.createElement('label');
  wrap.className = 'field';
  const l = document.createElement('span');
  l.className = 'label';
  l.textContent = label;
  const i = document.createElement('input');
  i.type = type;
  i.name = name;
  if (placeholder) i.placeholder = placeholder;
  if (type === 'password') i.autocomplete = 'new-password';
  wrap.append(l, i);
  return wrap;
}
