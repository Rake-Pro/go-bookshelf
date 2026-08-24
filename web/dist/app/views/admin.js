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
import { addBooksButton } from '../components/add-books.js';
import { confirmDialog } from '../components/confirm.js';


/** @param {import('../router.js').RouteCtx} ctx */
export default async function admin(ctx) {
  const settingsLink = document.createElement('a');
  settingsLink.className = 'btn';
  settingsLink.href = '/admin/settings';
  settingsLink.textContent = 'Settings';

  const { el, body } = page('Admin', { actions: [settingsLink] });
  // Scan-status poll timers for THIS mount, keyed by library id. Per-mount so
  // an overlapping admin mount's destroy cannot clear the new mount's polls.
  const scanTimers = new Map();
  const destroy = () => { for (const t of scanTimers.values()) clearTimeout(t); scanTimers.clear(); };

  if (!store.isAdmin) {
    body.replaceChildren(emptyView(
      'Admins only',
      'Your account does not have permission to manage this server.',
      { label: 'Go home', href: '/' },
    ));
    return { el, title: 'Admin', destroy };
  }

  const libs = document.createElement('section');
  const users = document.createElement('section');
  const status = document.createElement('section');
  body.append(libs, users, status);

  renderLibraries(libs, scanTimers);
  renderUsers(users);
  renderStatus(status);

  return { el, title: 'Admin', destroy };
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
  p.append(icon(kind === 'ok' ? 'check' : 'warn', { size: '1.25rem' }), document.createTextNode(msg));
  announce(msg);
  return p;
}

/* ---------------- libraries ---------------- */

/** @param {HTMLElement} host */
async function renderLibraries(host, timers) {
  host.replaceChildren(loadingView('Loading libraries'));
  let data;
  try {
    data = await api.libraries();
  } catch (e) {
    host.replaceChildren(errorView(e, () => renderLibraries(host, timers)));
    return;
  }

  const c = card('Libraries');
  const list = data?.items || [];

  if (!list.length) {
    c.append(emptyView('No libraries yet', 'Add one below and point it at a folder of books.'));
  } else {
    const ul = document.createElement('ul');
    ul.className = 'linklist';
    for (const lib of list) ul.append(libraryRow(lib, host, timers));
    c.append(ul);
  }

  // Adding books is not an admin power, but the admin page is where the
  // libraries are, so the same button lives here too.
  const add = addBooksButton({ libraries: list, onAdded: () => renderLibraries(host, timers) });
  if (add) {
    const row = document.createElement('div');
    row.className = 'row';
    row.style.marginTop = 'var(--s4)';
    row.append(add);
    c.append(row);
  }

  c.append(createLibraryForm(host, timers));
  host.replaceChildren(c);
}

/** @param {any} lib @param {HTMLElement} host */
function libraryRow(lib, host, timers) {
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
    if (scan.disabled) return;
    scan.disabled = true;
    scanLabel.textContent = 'Scanning...';
    // The region goes live only once a scan was asked for: page load writes
    // the last-scan summary in here too, and a live region would read every
    // library's history aloud unprompted. The paired announce() covers the
    // first write, which lands in the same tick the region is registered.
    if (!result.hasAttribute('role')) {
      result.setAttribute('role', 'status');
      result.setAttribute('aria-live', 'polite');
    }
    result.replaceChildren(scanLine('Scan started.'));
    announce('Scan started');
    try {
      await api.scanLibrary(lib.id);
      pollScans(lib.id, result, timers, scan, scanLabel);
    } catch (e) {
      result.replaceChildren(scanLine(errorMessage(e), 'error'));
      scan.disabled = false;
      scanLabel.textContent = 'Scan';
    }
  });

  top.append(name, sp, browse, scan);
  li.append(top, result, libraryEditDetails(lib, strong, sub), libraryDeleteControl(lib, host, timers));
  loadLastScan(lib.id, result, timers, scan, scanLabel);
  return li;
}

/**
 * The scan status line, written into the library row's own always-present
 * live region (never a freshly role="status" node of its own - a screen
 * reader is not guaranteed to pick up a role landing on a node that is itself
 * being swapped in).
 * @param {string} text @param {'ok'|'error'} [kind]
 */
function scanLine(text, kind) {
  const p = document.createElement('p');
  p.className = kind ? 'row small' : 'muted small';
  p.style.margin = '0';
  if (kind) {
    p.style.color = kind === 'ok' ? 'var(--ok)' : 'var(--danger)';
    p.append(icon(kind === 'ok' ? 'check' : 'warn', { size: '1.25rem' }));
  }
  p.append(document.createTextNode(text));
  return p;
}

/** @param {any} s @returns {string} */
function scanSummaryText(s) {
  return s.finished_at
    ? `Last scan ${date(s.finished_at)}: ${s.added ?? 0} added, ${s.updated ?? 0} updated, `
      + `${s.removed ?? 0} removed, ${s.errors ?? 0} errors`
    : `Scan running since ${date(s.started_at)}`;
}

/**
 * @param {string} id @param {HTMLElement} host @param {Map<string, number>} timers
 * @param {HTMLButtonElement} [scanBtn] @param {HTMLElement} [scanLabel]
 */
async function loadLastScan(id, host, timers, scanBtn, scanLabel) {
  try {
    const data = await api.scans(id);
    const last = (data?.items || [])[0];
    if (!last) return;
    const text = scanSummaryText(last);
    if (host.textContent !== text) host.replaceChildren(scanLine(text));
    // A scan started elsewhere (another tab, another admin) is still worth
    // watching, so pick up polling rather than leaving the row to freeze.
    if (!last.finished_at) {
      if (scanBtn) { scanBtn.disabled = true; scanLabel.textContent = 'Scanning...'; }
      pollScans(id, host, timers, scanBtn, scanLabel);
    }
  } catch { /* scan history is optional detail */ }
}

/**
 * Poll scan status until it finishes (or 60 tries, ~2 minutes). Cancels any
 * poll already running for this library, and stops on its own once the host
 * row leaves the document (a navigation away from /admin).
 * @param {string} id @param {HTMLElement} host @param {Map<string, number>} timers
 * @param {HTMLButtonElement} [scanBtn] @param {HTMLElement} [scanLabel]
 */
function pollScans(id, host, timers, scanBtn, scanLabel) {
  const prev = timers.get(id);
  if (prev) clearTimeout(prev);

  /** Both a finish and a give-up hand the button back. */
  const release = () => { if (scanBtn) { scanBtn.disabled = false; scanLabel.textContent = 'Scan'; } };

  let tries = 0;
  const tick = async () => {
    if (!host.isConnected) { timers.delete(id); return; }
    tries++;
    try {
      const data = await api.scans(id);
      const last = (data?.items || [])[0];
      if (last && last.finished_at) {
        timers.delete(id);
        host.replaceChildren(scanLine(scanSummaryText(last), 'ok'));
        release();
        return;
      }
      if (last) {
        // Same text, same nodes: rewriting a live region re-announces it, so
        // only touch it when the status actually changed.
        const text = scanSummaryText(last);
        if (host.textContent !== text) host.replaceChildren(scanLine(text));
      }
    } catch { /* keep polling */ }
    if (tries < 60) {
      // Re-check after the await: the row can leave the document mid-request.
      if (!host.isConnected) { timers.delete(id); return; }
      timers.set(id, setTimeout(tick, 2000));
      return;
    }
    timers.delete(id);
    const line = scanLine('Still scanning - this is taking longer than expected.');
    const refresh = document.createElement('button');
    refresh.type = 'button';
    refresh.className = 'btn btn--quiet';
    refresh.textContent = 'Refresh';
    refresh.addEventListener('click', () => loadLastScan(id, host, timers, scanBtn, scanLabel));
    line.append(refresh);
    host.replaceChildren(line);
    release();
  };
  timers.set(id, setTimeout(tick, 2000));
}

/**
 * Name, kind and paths - everything `PATCH /libraries/{id}` accepts, in one
 * form. Saves only the changed fields and, on success, updates the row's own
 * name/sub line in place rather than re-rendering the whole panel, so an
 * in-flight scan poll for this row (or any other) is left running untouched.
 *
 * @param {any} lib @param {HTMLElement} strong @param {HTMLElement} sub
 */
function libraryEditDetails(lib, strong, sub) {
  const details = document.createElement('details');
  details.style.marginTop = 'var(--s2)';
  const summary = document.createElement('summary');
  summary.className = 'btn';
  summary.style.display = 'inline-flex';
  summary.append(icon('gear'));
  const st = document.createElement('span');
  st.textContent = 'Edit';
  summary.append(st);
  details.append(summary);

  const form = document.createElement('form');
  form.style.marginTop = 'var(--s4)';
  form.noValidate = true;

  const nameField = textField('Name', 'name', '');
  const nameInput = /** @type {HTMLInputElement} */ (nameField.querySelector('input'));
  nameInput.value = lib.name;

  const kindField = document.createElement('label');
  kindField.className = 'field';
  const kindLabel = document.createElement('span');
  kindLabel.className = 'label';
  kindLabel.textContent = 'Kind';
  const kindSelect = document.createElement('select');
  kindSelect.name = 'kind';
  for (const [v, t] of [['ebook', 'Ebooks'], ['audiobook', 'Audiobooks'], ['mixed', 'Mixed']]) {
    const o = document.createElement('option');
    o.value = v;
    o.textContent = t;
    if (v === lib.kind) o.selected = true;
    kindSelect.append(o);
  }
  kindField.append(kindLabel, kindSelect);

  const pathsField = document.createElement('label');
  pathsField.className = 'field';
  const pathsLabel = document.createElement('span');
  pathsLabel.className = 'label';
  pathsLabel.textContent = 'Paths';
  const pathsInput = document.createElement('textarea');
  pathsInput.name = 'paths';
  pathsInput.value = (lib.paths || []).join('\n');
  const pathsHint = document.createElement('span');
  pathsHint.className = 'hint';
  pathsHint.textContent = 'One absolute path per line, readable by the server.';
  pathsField.append(pathsLabel, pathsInput, pathsHint);

  const submit = document.createElement('button');
  submit.type = 'submit';
  submit.className = 'btn btn--primary';
  submit.textContent = 'Save';
  const out = document.createElement('div');

  form.append(nameField, kindField, pathsField, submit, out);
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const fd = new FormData(form);
    const patch = {};
    const newName = String(fd.get('name') || '').trim();
    if (!newName) {
      out.replaceChildren(flash('A name is required.', 'error'));
      return;
    }
    if (newName !== lib.name) patch.name = newName;
    const newKind = String(fd.get('kind') || lib.kind);
    if (newKind !== lib.kind) patch.kind = newKind;
    const newPaths = String(fd.get('paths') || '').split('\n').map((x) => x.trim()).filter(Boolean);
    const oldPaths = lib.paths || [];
    const pathsChanged = newPaths.length !== oldPaths.length || newPaths.some((p, i) => p !== oldPaths[i]);
    if (pathsChanged) {
      if (!newPaths.length) {
        out.replaceChildren(flash('At least one path is required.', 'error'));
        return;
      }
      patch.paths = newPaths;
    }
    if (Object.keys(patch).length === 0) {
      out.replaceChildren(flash('No changes to save.'));
      return;
    }
    submit.disabled = true;
    out.replaceChildren();
    try {
      const updated = await api.updateLibrary(lib.id, patch);
      lib.name = updated?.name ?? patch.name ?? lib.name;
      lib.kind = updated?.kind ?? patch.kind ?? lib.kind;
      lib.paths = updated?.paths ?? patch.paths ?? lib.paths;
      strong.textContent = lib.name;
      sub.textContent = `${lib.kind} - ${(lib.paths || []).join(', ') || 'no paths'}`;
      out.replaceChildren(flash('Saved.'));
    } catch (err) {
      out.replaceChildren(flash(errorMessage(err), 'error'));
    } finally {
      submit.disabled = false;
    }
  });

  details.append(form);
  return details;
}

/**
 * The delete control. Confirmed via `internal/library/queries.go`
 * `DeleteLibrary`: it only removes the `libraries` row (cascading to its
 * items, paths, scan history and per-user grants); it never touches the
 * filesystem, so the dialog copy says exactly that.
 *
 * @param {any} lib @param {HTMLElement} host
 */
function libraryDeleteControl(lib, host, timers) {
  const wrap = document.createElement('div');
  wrap.className = 'row';
  wrap.style.marginTop = 'var(--s2)';

  const del = document.createElement('button');
  del.type = 'button';
  del.className = 'btn btn--danger';
  del.append(icon('close'));
  const label = document.createElement('span');
  label.textContent = 'Delete';
  del.append(label);
  const out = document.createElement('div');

  del.addEventListener('click', async () => {
    del.disabled = true;
    const ok = await confirmDialog(wrap, {
      heading: 'Delete library',
      message: `Delete library ${lib.name}? Its books leave the catalog; files on disk are not touched.`,
      confirmLabel: 'Delete',
      danger: true,
    });
    if (!ok) { del.disabled = false; return; }
    out.replaceChildren();
    try {
      await api.deleteLibrary(lib.id);
      announce(`Deleted ${lib.name}`);
      renderLibraries(host, timers);
    } catch (err) {
      del.disabled = false;
      out.replaceChildren(flash(errorMessage(err), 'error'));
    }
  });

  wrap.append(del, out);
  return wrap;
}

/** @param {HTMLElement} host */
function createLibraryForm(host, timers) {
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

  const create = document.createElement('div');
  create.className = 'field';
  const crow = document.createElement('label');
  crow.className = 'check';
  const cb = document.createElement('input');
  cb.type = 'checkbox';
  cb.name = 'create_missing';
  cb.checked = true;
  const cl = document.createElement('span');
  cl.textContent = 'Create folders that do not exist yet';
  crow.append(cb, cl);
  create.append(crow);

  const submit = document.createElement('button');
  submit.type = 'submit';
  submit.className = 'btn btn--primary';
  submit.textContent = 'Create library';

  const out = document.createElement('div');
  form.append(name, kind, paths, create, submit, out);

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const fd = new FormData(form);
    const body = {
      name: String(fd.get('name') || '').trim(),
      kind: String(fd.get('kind') || 'ebook'),
      paths: String(fd.get('paths') || '').split('\n').map((x) => x.trim()).filter(Boolean),
      create_missing: fd.get('create_missing') === 'on',
    };
    if (!body.name || !body.paths.length) {
      out.replaceChildren(flash('A name and at least one path are required.', 'error'));
      return;
    }
    submit.disabled = true;
    try {
      await api.createLibrary(body);
      renderLibraries(host, timers);
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
  let data, libData, status;
  try {
    [data, libData, status] = await Promise.all([api.users(), api.libraries(), api.systemStatus()]);
  } catch (e) {
    host.replaceChildren(errorView(e, () => renderUsers(host)));
    return;
  }
  const c = card('Users');
  const list = data?.items || [];
  const libraries = libData?.items || [];
  // Whether a password can currently do anything (composes the setting with
  // GOBOOKSHELF_ADMIN_RECOVERY, same as the login page and /auth/status), and
  // whether single sign-on is configured at all - both decide which edit
  // controls are meaningful to offer, rather than left to fail server-side.
  const localLogin = status?.local_login !== false;
  const oidcEnabled = status?.oidc_enabled === true;
  // How many enabled administrators exist right now, so the delete, role and
  // disable controls can grey themselves out on the one it would be a
  // lockout to touch, rather than let the click go to the server and come
  // back refused.
  const enabledAdmins = list.filter((u) => u.role === 'admin' && !u.disabled_at).length;

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
    const sp = document.createElement('span');
    sp.className = 'spacer';
    row.append(sp, uploadToggle(u, host));
    const isLastAdmin = u.role === 'admin' && !u.disabled_at && enabledAdmins <= 1;
    li.append(
      row,
      libraryAccessField(u, libraries),
      userEditDetails(u, host, { localLogin, oidcEnabled, isLastAdmin }),
      userDeleteControl(u, host, enabledAdmins),
    );
    table.append(li);
  }
  if (list.length) c.append(table);
  else c.append(emptyView('No users', 'Add the first account below.'));

  c.append(createUserForm(host, localLogin, oidcEnabled));
  host.replaceChildren(c);
}

/**
 * The per-account "may add books" switch.
 *
 * An administrator always may and a restricted account never may, so for those
 * two the control states the rule instead of pretending to offer a choice.
 *
 * @param {any} u @param {HTMLElement} host
 */
function uploadToggle(u, host) {
  if (u.role === 'admin' || u.role === 'restricted') {
    const note = document.createElement('span');
    note.className = 'muted small';
    note.textContent = u.role === 'admin' ? 'Can add books' : 'Cannot add books';
    return note;
  }
  const box = document.createElement('div');
  const wrap = document.createElement('label');
  wrap.className = 'check';
  const cb = document.createElement('input');
  cb.type = 'checkbox';
  cb.checked = u.can_upload === true;
  const text = document.createElement('span');
  text.className = 'small';
  text.textContent = 'Can add books';
  wrap.append(cb, text);
  const out = document.createElement('div');
  box.append(wrap, out);

  cb.addEventListener('change', async () => {
    cb.disabled = true;
    out.replaceChildren();
    try {
      await api.updateUser(u.id, { can_upload: cb.checked });
      announce(`${u.username} ${cb.checked ? 'can' : 'cannot'} add books`);
      cb.disabled = false;
      renderUsers(host);
    } catch (err) {
      cb.checked = !cb.checked;
      out.replaceChildren(flash(errorMessage(err), 'error'));
      cb.disabled = false;
    }
  });
  return box;
}

/**
 * The per-account library access picker. An administrator sees every
 * library regardless of what is granted, so the control states that instead
 * of offering a choice that would never change anything. A restricted or
 * ordinary account with nothing granted here sees no books anywhere in the
 * app, which is worth spelling out rather than leaving the admin to infer it
 * from an empty library page.
 *
 * @param {any} u @param {any[]} libraries
 */
function libraryAccessField(u, libraries) {
  const wrap = document.createElement('div');
  wrap.className = 'field';
  wrap.style.marginTop = 0;
  const label = document.createElement('span');
  label.className = 'label';
  label.textContent = 'Libraries';
  wrap.append(label);

  if (u.role === 'admin') {
    const note = document.createElement('span');
    note.className = 'muted small';
    note.textContent = 'All libraries (admin)';
    wrap.append(note);
    return wrap;
  }

  if (!libraries.length) {
    const note = document.createElement('span');
    note.className = 'muted small';
    note.textContent = 'No libraries exist yet.';
    wrap.append(note);
    return wrap;
  }

  const body = document.createElement('div');
  body.append(loadingView('Loading access'));
  wrap.append(body);
  loadLibraryAccess(u, libraries, body);
  return wrap;
}

/** @param {any} u @param {any[]} libraries @param {HTMLElement} body */
async function loadLibraryAccess(u, libraries, body) {
  let granted;
  try {
    const data = await api.userLibraries(u.id);
    granted = new Set(data?.libraries || []);
  } catch (e) {
    body.replaceChildren(errorView(e, () => loadLibraryAccess(u, libraries, body)));
    return;
  }

  const list = document.createElement('div');
  list.className = 'row';
  list.style.flexWrap = 'wrap';
  const boxes = libraries.map((lib) => {
    const row = document.createElement('label');
    row.className = 'check';
    const cb = document.createElement('input');
    cb.type = 'checkbox';
    cb.checked = granted.has(lib.id);
    const text = document.createElement('span');
    text.className = 'small';
    text.textContent = lib.name;
    row.append(cb, text);
    list.append(row);
    return { id: lib.id, cb };
  });

  const save = document.createElement('button');
  save.type = 'button';
  save.className = 'btn';
  save.textContent = 'Save';
  const out = document.createElement('div');

  save.addEventListener('click', async () => {
    save.disabled = true;
    out.replaceChildren();
    const ids = boxes.filter((b) => b.cb.checked).map((b) => b.id);
    try {
      await api.setUserLibraries(u.id, ids);
      out.replaceChildren(flash(ids.length
        ? `${u.username} can see ${ids.length} librar${ids.length === 1 ? 'y' : 'ies'}.`
        : `${u.username} has no library access and will see no books.`));
    } catch (err) {
      out.replaceChildren(flash(errorMessage(err), 'error'));
    } finally {
      save.disabled = false;
    }
  });

  body.replaceChildren(list, save, out);
}

/**
 * Display name, username, admin-initiated password reset, role and disabled
 * state - everything `PATCH /users/{id}` accepts, in one form. Each control
 * greys itself out, with an explanation, exactly where the server would
 * otherwise refuse it - the house rule that a control whose backing
 * capability is off must not accept-then-error.
 *
 * @param {any} u @param {HTMLElement} host
 * @param {{localLogin:boolean, oidcEnabled:boolean, isLastAdmin:boolean}} opts
 */
function userEditDetails(u, host, opts) {
  const details = document.createElement('details');
  details.style.marginTop = 'var(--s2)';
  const summary = document.createElement('summary');
  summary.className = 'btn';
  summary.style.display = 'inline-flex';
  summary.append(icon('gear'));
  const st = document.createElement('span');
  st.textContent = 'Edit';
  summary.append(st);
  details.append(summary);

  const form = document.createElement('form');
  form.style.marginTop = 'var(--s4)';
  form.noValidate = true;

  const nameField = textField('Display name', 'display_name', '');
  const nameInput = /** @type {HTMLInputElement} */ (nameField.querySelector('input'));
  nameInput.value = u.display_name || '';

  // Username: purely cosmetic once bound to an SSO identity - every lookup
  // after the first sign-in goes by the subject, never by username again.
  // Unbound, and while single sign-on is configured at all, the current
  // username is exactly what a first sign-in matches a pre-created account
  // by; renaming it then would silently break that match for whoever is
  // still waiting to sign in as this account.
  const usernameField = textField('Username', 'username', '');
  const usernameInput = /** @type {HTMLInputElement} */ (usernameField.querySelector('input'));
  usernameInput.value = u.username;
  const usernameLocked = opts.oidcEnabled && !u.oidc_linked;
  if (usernameLocked) {
    usernameInput.disabled = true;
    usernameField.append(hintEl('Cannot be renamed until this account signs in through SSO once - '
      + 'that is what matches it to a new sign-in by name.'));
  }

  // Password reset only does anything while password sign-in is available -
  // and off is deployment-wide, not a per-account exception, so it is left
  // out entirely rather than greyed: the grey-not-error rule is for a
  // control someone could reasonably expect to use, not one that can never
  // do anything on this server at all.
  const pwField = opts.localLogin
    ? textField('Reset password', 'password', 'Leave blank to keep the current password', 'password')
    : null;

  // Role and disabled state can each lock every administrator out; grey them
  // on your own row (the server refuses both unconditionally) and on the
  // last enabled administrator's row (refused so nobody is left).
  const lockedForAdminCount = u.id === store.user?.id || opts.isLastAdmin;
  const lockReason = u.id === store.user?.id
    ? (field) => `You cannot ${field} your own account.`
    : () => 'This is the last administrator; promote another account first.';

  const roleField = document.createElement('label');
  roleField.className = 'field';
  const roleLabel = document.createElement('span');
  roleLabel.className = 'label';
  roleLabel.textContent = 'Role';
  const roleSelect = document.createElement('select');
  roleSelect.name = 'role';
  for (const [v, t] of [['user', 'User'], ['restricted', 'Restricted'], ['admin', 'Admin']]) {
    const o = document.createElement('option');
    o.value = v;
    o.textContent = t;
    if (v === u.role) o.selected = true;
    roleSelect.append(o);
  }
  roleField.append(roleLabel, roleSelect);
  if (lockedForAdminCount) {
    roleSelect.disabled = true;
    roleField.append(hintEl(lockReason('change the role of')));
  }

  const disabledField = document.createElement('div');
  disabledField.className = 'field';
  const disabledRow = document.createElement('label');
  disabledRow.className = 'check';
  const disabledBox = document.createElement('input');
  disabledBox.type = 'checkbox';
  disabledBox.name = 'disabled';
  disabledBox.checked = Boolean(u.disabled_at);
  const disabledText = document.createElement('span');
  disabledText.textContent = 'Account disabled';
  disabledRow.append(disabledBox, disabledText);
  disabledField.append(disabledRow);
  if (lockedForAdminCount) {
    disabledBox.disabled = true;
    disabledField.append(hintEl(lockReason('disable')));
  }

  const submit = document.createElement('button');
  submit.type = 'submit';
  submit.className = 'btn btn--primary';
  submit.textContent = 'Save';
  const out = document.createElement('div');

  form.append(nameField, usernameField, ...(pwField ? [pwField] : []), roleField, disabledField, submit, out);
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const fd = new FormData(form);
    const patch = {};
    const newName = String(fd.get('display_name') || '').trim();
    if (newName !== (u.display_name || '')) patch.display_name = newName;
    if (!usernameLocked) {
      const newUsername = String(fd.get('username') || '').trim();
      if (newUsername && newUsername !== u.username) patch.username = newUsername;
    }
    if (opts.localLogin) {
      const newPassword = String(fd.get('password') || '');
      if (newPassword) patch.password = newPassword;
    }
    if (!lockedForAdminCount) {
      const newRole = String(fd.get('role') || u.role);
      if (newRole !== u.role) patch.role = newRole;
      const newDisabled = fd.get('disabled') === 'on';
      if (newDisabled !== Boolean(u.disabled_at)) patch.disabled = newDisabled;
    }
    if (Object.keys(patch).length === 0) return;
    submit.disabled = true;
    out.replaceChildren();
    try {
      await api.updateUser(u.id, patch);
      announce(`Saved ${u.username}`);
      renderUsers(host);
    } catch (err) {
      out.replaceChildren(flash(errorMessage(err), 'error'));
      submit.disabled = false;
    }
  });

  details.append(form);
  return details;
}

/** @param {string} text */
function hintEl(text) {
  const h = document.createElement('span');
  h.className = 'hint';
  h.textContent = text;
  return h;
}

/**
 * The delete control. Greyed out - never accept-then-error - on the two
 * accounts the server would refuse anyway: your own, and the last enabled
 * administrator.
 *
 * @param {any} u @param {HTMLElement} host @param {number} enabledAdmins
 */
function userDeleteControl(u, host, enabledAdmins) {
  const wrap = document.createElement('div');
  wrap.className = 'row';
  wrap.style.marginTop = 'var(--s2)';

  const isSelf = u.id === store.user?.id;
  const isLastAdmin = u.role === 'admin' && !u.disabled_at && enabledAdmins <= 1;

  const del = document.createElement('button');
  del.type = 'button';
  del.className = 'btn btn--danger';
  del.append(icon('close'));
  const label = document.createElement('span');
  label.textContent = 'Delete';
  del.append(label);
  const out = document.createElement('div');

  if (isSelf || isLastAdmin) {
    del.disabled = true;
    del.setAttribute('aria-disabled', 'true');
    del.title = isSelf
      ? 'You cannot delete your own account.'
      : 'This is the last administrator; promote another account first.';
  } else {
    del.addEventListener('click', async () => {
      del.disabled = true;
      const ok = await confirmDialog(wrap, {
        heading: 'Delete user',
        message: `Delete ${u.display_name || u.username} (${u.username})? This removes their library access, `
          + 'settings, progress and bookmarks. This cannot be undone.',
        confirmLabel: 'Delete',
        danger: true,
      });
      if (!ok) { del.disabled = false; return; }
      out.replaceChildren();
      try {
        await api.deleteUser(u.id);
        announce(`Deleted ${u.username}`);
        renderUsers(host);
      } catch (err) {
        del.disabled = false;
        out.replaceChildren(flash(errorMessage(err), 'error'));
      }
    });
  }

  wrap.append(del, out);
  return wrap;
}

/** @param {HTMLElement} host @param {boolean} localLogin @param {boolean} oidcEnabled */
function createUserForm(host, localLogin, oidcEnabled) {
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

  const uploadWrap = document.createElement('div');
  uploadWrap.className = 'field';
  const uploadRow = document.createElement('label');
  uploadRow.className = 'check';
  const uploadBox = document.createElement('input');
  uploadBox.type = 'checkbox';
  uploadBox.name = 'can_upload';
  const uploadText = document.createElement('span');
  uploadText.textContent = 'Can add books (upload and import)';
  uploadRow.append(uploadBox, uploadText);
  uploadWrap.append(uploadRow);

  const submit = document.createElement('button');
  submit.type = 'submit';
  submit.className = 'btn btn--primary';
  submit.textContent = 'Create user';
  const out = document.createElement('div');

  // Password sign-in off means a password set here could never be used to
  // sign in with, and off is deployment-wide rather than a per-account
  // exception - so the field is left out entirely rather than offered
  // greyed, and the account is created by username alone for single sign-on
  // to adopt on its first login (docs/DESIGN.md, "OIDC group mapping").
  const pwField = localLogin ? textField('Password', 'password', '', 'password') : null;
  // With single sign-on on, a blank password is a legitimate way to
  // pre-create an SSO-only account for someone to sign into by username.
  if (pwField && oidcEnabled) {
    pwField.append(hintEl('Leave blank to create an SSO-only account.'));
  }

  form.append(
    textField('Username', 'username', ''),
    textField('Display name', 'display_name', ''),
    ...(pwField ? [pwField] : []),
    roleWrap, uploadWrap, submit, out,
  );

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const fd = new FormData(form);
    const body = {
      username: String(fd.get('username') || '').trim(),
      display_name: String(fd.get('display_name') || '').trim(),
      password: String(fd.get('password') || ''),
      role: String(fd.get('role') || 'user'),
      can_upload: fd.get('can_upload') === 'on',
    };
    if (!body.username || (localLogin && !oidcEnabled && !body.password)) {
      out.replaceChildren(flash(
        localLogin && !oidcEnabled ? 'Username and password are required.' : 'Username is required.', 'error'));
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
  host.replaceChildren(loadingView('Loading server status'));
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
  } catch (e) {
    host.replaceChildren(errorView(e, () => renderStatus(host)));
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
