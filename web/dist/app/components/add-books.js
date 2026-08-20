/**
 * "Add books": the sheet behind the button on the library and admin pages.
 *
 * Two tabs, because there are two ways in and they have nothing in common:
 * files from this device, uploaded over one multipart request so that the
 * chapters of an audiobook arrive together and become one book; or a URL,
 * which is queued as a job on the server and polled, because fetching somebody
 * else's site takes as long as it takes.
 *
 * The sheet renders inside `<bs-sheet>`, whose shadow root the page stylesheet
 * does not reach, so the styles this component needs travel with it.
 */

import { api, uploadBooks, ApiError } from '../api.js';
import { openSheet } from './sheet.js';
import { icon } from './icons.js';
import { errorMessage } from './states.js';
import { store } from '../store.js';
import { bytes } from '../format.js';
import { announce } from '../live.js';

/** How often the job list re-reads the server while anything is running. */
const POLL_MS = 2000;

const STYLES = `
.tabs { display: flex; gap: var(--s2); border-bottom: 1px solid var(--border); margin-bottom: var(--s4); }
.tabs button {
  min-height: 2.75rem;
  padding: var(--s2) var(--s3);
  color: var(--muted);
  background: none;
  border: 0;
  border-bottom: 3px solid transparent;
  font: inherit;
  cursor: pointer;
}
.tabs button[aria-selected="true"] { color: var(--text); border-bottom-color: var(--accent); font-weight: 600; }
.tabs button:focus-visible { outline: 3px solid var(--focus); outline-offset: -3px; }
.drop {
  display: grid;
  place-items: center;
  gap: var(--s2);
  min-height: 8rem;
  padding: var(--s4);
  color: var(--muted);
  text-align: center;
  background: var(--surface-2);
  border: 2px dashed var(--border);
  border-radius: var(--radius);
  cursor: pointer;
}
.drop svg { width: 2rem; height: 2rem; }
.drop:focus-visible { outline: 3px solid var(--focus); outline-offset: 2px; }
.drop[data-over="true"] { color: var(--text); border-color: var(--accent); border-style: solid; }
.field { display: block; margin: var(--s4) 0; }
.field > .label { display: block; margin-bottom: var(--s1); font-weight: 600; }
.field .hint { display: block; margin-top: var(--s1); color: var(--muted); font-size: 0.9rem; }
input, select {
  width: 100%;
  min-height: 2.75rem;
  padding: var(--s2) var(--s3);
  color: var(--text);
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  font: inherit;
}
input:focus-visible, select:focus-visible { outline: 3px solid var(--focus); outline-offset: 1px; }
ul { list-style: none; margin: var(--s4) 0 0; padding: 0; }
li {
  display: grid;
  gap: var(--s1);
  padding: var(--s3) 0;
  border-bottom: 1px solid var(--border);
}
li .top { display: flex; align-items: baseline; gap: var(--s2); }
li .name { flex: 1 1 auto; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
li .size, li .state { color: var(--muted); font-size: 0.9rem; white-space: nowrap; }
li .state--ok { color: var(--ok); }
li .state--bad { color: var(--danger); }
progress { width: 100%; height: 0.4rem; }
.actions { display: flex; align-items: center; gap: var(--s3); margin-top: var(--s4); flex-wrap: wrap; }
.actions .spacer { flex: 1 1 auto; }
.btn {
  display: inline-flex;
  align-items: center;
  gap: var(--s2);
  min-height: 2.75rem;
  padding: var(--s2) var(--s4);
  color: var(--text);
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  font: inherit;
  cursor: pointer;
}
.btn svg { width: 1.25rem; height: 1.25rem; }
.btn:hover:not(:disabled) { background: var(--surface-2); }
.btn:disabled { opacity: 0.55; cursor: default; }
.btn:focus-visible { outline: 3px solid var(--focus); outline-offset: 2px; }
.btn--primary { color: var(--accent-text); background: var(--accent); border-color: var(--accent); }
.btn--quiet { border-color: transparent; }
.problem { display: flex; gap: var(--s2); color: var(--danger); margin-top: var(--s3); }
.problem svg { width: 1.25rem; height: 1.25rem; flex: 0 0 auto; }
a { color: var(--accent); }
.muted { color: var(--muted); }
.small { font-size: 0.9rem; }
`;

/**
 * The button that opens the sheet, or null when this account may not add
 * books. Returning null rather than a disabled button is deliberate: there is
 * nothing the user could do to make it work, so offering it would be a lie.
 *
 * @param {{libraries:any[], libraryId?:string, onAdded?:() => void}} opts
 * @returns {HTMLButtonElement|null}
 */
export function addBooksButton(opts) {
  if (!store.canUpload) return null;
  const b = document.createElement('button');
  b.type = 'button';
  b.className = 'btn btn--primary';
  b.append(icon('plus'));
  const label = document.createElement('span');
  label.textContent = 'Add books';
  b.append(label);
  b.addEventListener('click', () => openAddBooks(document.body, opts));
  return b;
}

/**
 * Open the sheet.
 *
 * @param {Element} host
 * @param {{libraries:any[], libraryId?:string, onAdded?:() => void}} opts
 */
export function openAddBooks(host, opts) {
  const libraries = (opts.libraries || []).filter(Boolean);
  const box = document.createElement('div');

  const style = document.createElement('style');
  style.textContent = STYLES;

  const tablist = document.createElement('div');
  tablist.className = 'tabs';
  tablist.setAttribute('role', 'tablist');
  tablist.setAttribute('aria-label', 'How to add books');

  const uploadPanel = document.createElement('div');
  const urlPanel = document.createElement('div');
  const panels = [uploadPanel, urlPanel];
  const tabs = [
    makeTab('Upload files', 'add-upload'),
    makeTab('From a URL', 'add-url'),
  ];
  tabs.forEach((tab, i) => {
    const panel = panels[i];
    panel.id = tab.id.replace('tab-', 'panel-');
    panel.setAttribute('role', 'tabpanel');
    panel.setAttribute('aria-labelledby', tab.id);
    panel.tabIndex = 0;
    tab.setAttribute('aria-controls', panel.id);
    tab.addEventListener('click', () => select(i));
    tablist.append(tab);
  });

  /** @param {number} index */
  function select(index) {
    tabs.forEach((tab, i) => {
      const on = i === index;
      tab.setAttribute('aria-selected', String(on));
      tab.tabIndex = on ? 0 : -1;
      panels[i].hidden = !on;
    });
    tabs[index].focus();
  }
  // Left and right move between tabs, which is what a tablist owes a keyboard.
  tablist.addEventListener('keydown', (e) => {
    const current = tabs.findIndex((t) => t.getAttribute('aria-selected') === 'true');
    if (e.key === 'ArrowRight') { e.preventDefault(); select((current + 1) % tabs.length); }
    if (e.key === 'ArrowLeft') { e.preventDefault(); select((current + tabs.length - 1) % tabs.length); }
    if (e.key === 'Home') { e.preventDefault(); select(0); }
    if (e.key === 'End') { e.preventDefault(); select(tabs.length - 1); }
  });

  box.append(style, tablist, uploadPanel, urlPanel);

  // The sheet is a <dialog> opened with showModal(), so the focus trap, the
  // Escape key and the inert background come from the platform.
  const sheet = openSheet(host, 'Add books', box);
  const cleanups = [];
  sheet.addEventListener('sheet-close', () => cleanups.forEach((fn) => fn()));

  buildUpload(uploadPanel, libraries, opts, cleanups);
  buildImport(urlPanel, libraries, opts, cleanups);
  select(0);
  return sheet;
}

/** @param {string} text @param {string} id */
function makeTab(text, id) {
  const b = document.createElement('button');
  b.type = 'button';
  b.id = 'tab-' + id;
  b.setAttribute('role', 'tab');
  b.setAttribute('aria-selected', 'false');
  b.tabIndex = -1;
  b.textContent = text;
  return b;
}

/**
 * A library picker, or a fixed label when there is only one choice.
 * @param {any[]} libraries
 * @param {string|undefined} preferred
 */
function libraryField(libraries, preferred) {
  const wrap = document.createElement('label');
  wrap.className = 'field';
  const label = document.createElement('span');
  label.className = 'label';
  label.textContent = 'Library';
  const select = document.createElement('select');
  for (const lib of libraries) {
    const o = document.createElement('option');
    o.value = String(lib.id);
    o.textContent = `${lib.name} (${lib.kind})`;
    if (String(lib.id) === String(preferred)) o.selected = true;
    select.append(o);
  }
  wrap.append(label, select);
  if (libraries.length <= 1) wrap.hidden = libraries.length === 1;
  return { el: wrap, select, value: () => select.value || String(libraries[0]?.id || '') };
}

/** The accepted extensions for the currently chosen library. */
function acceptFor(libraries, id) {
  const lib = libraries.find((l) => String(l.id) === String(id));
  switch (lib?.kind) {
    case 'ebook': return '.epub';
    case 'audiobook': return '.m4b,.m4a,.mp3';
    default: return '.epub,.m4b,.m4a,.mp3';
  }
}

/* ---------------------------- the upload tab ---------------------------- */

/**
 * @param {HTMLElement} panel
 * @param {any[]} libraries
 * @param {{libraryId?:string, onAdded?:() => void}} opts
 * @param {(() => void)[]} cleanups
 */
function buildUpload(panel, libraries, opts, cleanups) {
  const lib = libraryField(libraries, opts.libraryId);

  const input = document.createElement('input');
  input.type = 'file';
  input.multiple = true;
  input.accept = acceptFor(libraries, lib.value());
  input.id = 'add-books-input';
  input.style.position = 'absolute';
  input.style.width = '1px';
  input.style.height = '1px';
  input.style.opacity = '0';
  lib.select.addEventListener('change', () => { input.accept = acceptFor(libraries, lib.value()); });

  const drop = document.createElement('div');
  drop.className = 'drop';
  drop.tabIndex = 0;
  drop.setAttribute('role', 'button');
  drop.setAttribute('aria-label', 'Choose files to upload, or drop them here');
  drop.append(icon('plus'));
  const dropText = document.createElement('span');
  dropText.textContent = 'Drop books here, or choose files';
  const dropHint = document.createElement('span');
  dropHint.className = 'small';
  dropHint.textContent = 'EPUB up to 200 MB, audio up to 2 GB per file.';
  drop.append(dropText, dropHint);

  drop.addEventListener('click', () => input.click());
  drop.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); input.click(); }
  });
  for (const type of ['dragenter', 'dragover']) {
    drop.addEventListener(type, (e) => { e.preventDefault(); drop.dataset.over = 'true'; });
  }
  for (const type of ['dragleave', 'drop']) {
    drop.addEventListener(type, () => { drop.dataset.over = 'false'; });
  }
  drop.addEventListener('drop', (e) => {
    e.preventDefault();
    add(Array.from(e.dataTransfer?.files || []));
  });
  input.addEventListener('change', () => add(Array.from(input.files || [])));

  const subdir = document.createElement('label');
  subdir.className = 'field';
  const subdirLabel = document.createElement('span');
  subdirLabel.className = 'label';
  subdirLabel.textContent = 'Subfolder (optional)';
  const subdirInput = document.createElement('input');
  subdirInput.type = 'text';
  subdirInput.placeholder = 'New arrivals';
  const subdirHint = document.createElement('span');
  subdirHint.className = 'hint';
  subdirHint.textContent = 'One plain folder name inside the library. Leave empty to file at the top level.';
  subdir.append(subdirLabel, subdirInput, subdirHint);

  const list = document.createElement('ul');
  const status = document.createElement('p');
  status.setAttribute('role', 'status');
  status.setAttribute('aria-live', 'polite');
  status.className = 'muted small';
  const problem = document.createElement('div');

  const submit = document.createElement('button');
  submit.type = 'button';
  submit.className = 'btn btn--primary';
  submit.textContent = 'Upload';
  submit.disabled = true;

  const clear = document.createElement('button');
  clear.type = 'button';
  clear.className = 'btn btn--quiet';
  clear.textContent = 'Clear';
  clear.disabled = true;

  const actions = document.createElement('div');
  actions.className = 'actions';
  const spacer = document.createElement('span');
  spacer.className = 'spacer';
  actions.append(status, spacer, clear, submit);

  panel.append(lib.el, drop, input, subdir, list, problem, actions);

  /** @type {{file:File, row:HTMLElement, bar:HTMLProgressElement, state:HTMLElement}[]} */
  let rows = [];
  let busy = false;

  /** @param {File[]} files */
  function add(files) {
    for (const f of files) {
      if (rows.some((r) => r.file.name === f.name && r.file.size === f.size)) continue;
      rows.push(rowFor(f, () => { rows = rows.filter((r) => r.file !== f); render(); }));
    }
    render();
  }

  function render() {
    list.replaceChildren(...rows.map((r) => r.row));
    submit.disabled = busy || rows.length === 0;
    clear.disabled = busy || rows.length === 0;
    input.value = '';
  }

  clear.addEventListener('click', () => { rows = []; problem.replaceChildren(); status.textContent = ''; render(); });

  submit.addEventListener('click', async () => {
    if (!rows.length || busy) return;
    busy = true;
    render();
    problem.replaceChildren();

    const form = new FormData();
    // The subfolder goes first: the server streams the request and reads the
    // field before the files it describes.
    if (subdirInput.value.trim()) form.append('subdir', subdirInput.value.trim());
    for (const r of rows) {
      form.append('files', r.file, r.file.name);
      r.state.textContent = 'Waiting';
      r.state.className = 'state';
      r.bar.removeAttribute('value');
      r.bar.value = 0;
    }

    const totalFileBytes = rows.reduce((sum, r) => sum + r.file.size, 0);
    status.textContent = 'Uploading...';

    const upload = uploadBooks(lib.value(), form, (loaded, total) => {
      // The wire total includes the multipart headers; scaling by the ratio of
      // real bytes to wire bytes keeps each bar honest without having to model
      // the encoding.
      const scaled = total ? loaded * (totalFileBytes / total) : loaded;
      let before = 0;
      for (const r of rows) {
        const done = Math.max(0, Math.min(r.file.size, scaled - before));
        r.bar.value = r.file.size ? done / r.file.size : 1;
        r.state.textContent = done >= r.file.size ? 'Sent' : 'Uploading';
        before += r.file.size;
      }
      const pct = totalFileBytes ? Math.round((Math.min(scaled, totalFileBytes) / totalFileBytes) * 100) : 100;
      status.textContent = `Uploading... ${pct}%`;
    });
    cleanups.push(upload.cancel);

    try {
      const result = await upload.promise;
      showResults(result);
    } catch (err) {
      showFailure(err);
    } finally {
      busy = false;
      submit.disabled = rows.length === 0;
      clear.disabled = rows.length === 0;
    }
  });

  /** @param {any} result */
  function showResults(result) {
    const added = result?.files || [];
    list.replaceChildren(...added.map(resultRow));
    rows = [];
    const scanning = result?.status === 'scanning';
    status.textContent = scanning
      ? `Added ${added.length}. The library is still being scanned.`
      : `Added ${added.length}.`;
    announce(status.textContent);
    opts.onAdded?.();
  }

  /** @param {unknown} err */
  function showFailure(err) {
    const box = document.createElement('div');
    box.className = 'problem';
    box.append(icon('warn'));
    const text = document.createElement('div');
    text.textContent = errorMessage(err);
    box.append(text);
    // A duplicate is not really a failure: the book is there, so offer it.
    const itemID = err instanceof ApiError ? err.body?.item_id : null;
    if (itemID) {
      const a = document.createElement('a');
      a.href = `/item/${itemID}`;
      a.textContent = 'Open the copy you already have';
      text.append(document.createElement('br'), a);
    }
    problem.replaceChildren(box);
    status.textContent = '';
    announce(errorMessage(err));
  }
}

/**
 * @param {File} f
 * @param {() => void} onRemove
 */
function rowFor(f, onRemove) {
  const row = document.createElement('li');
  const top = document.createElement('div');
  top.className = 'top';
  const name = document.createElement('span');
  name.className = 'name';
  name.textContent = f.name;
  const size = document.createElement('span');
  size.className = 'size';
  size.textContent = bytes(f.size);
  const state = document.createElement('span');
  state.className = 'state';
  state.textContent = 'Ready';
  const remove = document.createElement('button');
  remove.type = 'button';
  remove.className = 'btn btn--quiet';
  remove.setAttribute('aria-label', `Remove ${f.name}`);
  remove.append(icon('close'));
  remove.addEventListener('click', onRemove);
  top.append(name, size, state, remove);

  const bar = document.createElement('progress');
  bar.max = 1;
  bar.value = 0;
  bar.setAttribute('aria-label', `Upload progress for ${f.name}`);
  row.append(top, bar);
  return { file: f, row, bar, state };
}

/** One line of the result list: what was added, and where it went. */
function resultRow(file) {
  const li = document.createElement('li');
  const top = document.createElement('div');
  top.className = 'top';
  const name = document.createElement('span');
  name.className = 'name';
  if (file.item_id) {
    const a = document.createElement('a');
    a.href = `/item/${file.item_id}`;
    a.textContent = file.title || file.filename;
    name.append(a);
  } else {
    name.textContent = file.title || file.filename;
  }
  const state = document.createElement('span');
  state.className = file.item_id ? 'state state--ok' : 'state';
  state.textContent = file.item_id ? 'Added' : 'Scanning';
  top.append(name, state);
  const where = document.createElement('span');
  where.className = 'small muted';
  where.textContent = file.filename;
  li.append(top, where);
  return li;
}

/* ------------------------------ the URL tab ----------------------------- */

/**
 * @param {HTMLElement} panel
 * @param {any[]} libraries
 * @param {{libraryId?:string, onAdded?:() => void}} opts
 * @param {(() => void)[]} cleanups
 */
function buildImport(panel, libraries, opts, cleanups) {
  const lib = libraryField(libraries, opts.libraryId);

  const field = document.createElement('label');
  field.className = 'field';
  const label = document.createElement('span');
  label.className = 'label';
  label.textContent = 'Address';
  const input = document.createElement('input');
  input.type = 'url';
  input.placeholder = 'https://example.com/a-story';
  input.setAttribute('inputmode', 'url');
  const hint = document.createElement('span');
  hint.className = 'hint';
  hint.textContent = 'A link to a book file, or to the first page of a story. '
    + 'A story is followed through its "next chapter" links and built into an EPUB.';
  field.append(label, input, hint);

  const submit = document.createElement('button');
  submit.type = 'button';
  submit.className = 'btn btn--primary';
  submit.textContent = 'Import';

  const actions = document.createElement('div');
  actions.className = 'actions';
  const spacer = document.createElement('span');
  spacer.className = 'spacer';
  actions.append(spacer, submit);

  const problem = document.createElement('div');
  const status = document.createElement('p');
  status.setAttribute('role', 'status');
  status.setAttribute('aria-live', 'polite');
  status.className = 'muted small';
  const list = document.createElement('ul');

  panel.append(lib.el, field, actions, problem, status, list);

  let announced = new Map();
  let timer = 0;

  async function refresh() {
    try {
      const data = await api.imports();
      const jobs = data?.items || [];
      list.replaceChildren(...jobs.map(jobRow));
      // Only say something when a job actually changed state, or the live
      // region would repeat itself every two seconds.
      for (const job of jobs) {
        if (announced.get(job.id) !== job.status && (job.status === 'done' || job.status === 'failed')) {
          announce(job.status === 'done' ? `Import finished: ${job.message || job.url}` : `Import failed: ${job.message}`);
          if (job.status === 'done') opts.onAdded?.();
        }
        announced.set(job.id, job.status);
      }
      const running = jobs.some((j) => j.status === 'queued' || j.status === 'running');
      status.textContent = running ? 'Working...' : '';
      schedule(running);
    } catch (err) {
      status.textContent = errorMessage(err);
      schedule(false);
    }
  }

  /** @param {boolean} soon */
  function schedule(soon) {
    clearTimeout(timer);
    if (soon) timer = setTimeout(refresh, POLL_MS);
  }
  cleanups.push(() => clearTimeout(timer));

  function jobRow(job) {
    const li = document.createElement('li');
    const top = document.createElement('div');
    top.className = 'top';
    const name = document.createElement('span');
    name.className = 'name';
    if (job.item_id) {
      const a = document.createElement('a');
      a.href = `/item/${job.item_id}`;
      a.textContent = job.message || job.url;
      name.append(a);
    } else {
      name.textContent = job.url;
    }
    const state = document.createElement('span');
    state.className = 'state' + (job.status === 'done' ? ' state--ok' : job.status === 'failed' ? ' state--bad' : '');
    state.textContent = { queued: 'Queued', running: 'Working', done: 'Added', failed: 'Failed' }[job.status] || job.status;

    const cancel = document.createElement('button');
    cancel.type = 'button';
    cancel.className = 'btn btn--quiet';
    cancel.setAttribute('aria-label',
      job.status === 'done' || job.status === 'failed' ? `Clear ${job.url}` : `Cancel importing ${job.url}`);
    cancel.append(icon('close'));
    cancel.addEventListener('click', async () => {
      cancel.disabled = true;
      try {
        await api.cancelImport(job.id);
      } catch (err) {
        status.textContent = errorMessage(err);
      }
      refresh();
    });

    top.append(name, state, cancel);
    li.append(top);
    if (job.status === 'failed' && job.message) {
      const why = document.createElement('span');
      why.className = 'small muted';
      why.textContent = job.message;
      li.append(why);
    }
    return li;
  }

  submit.addEventListener('click', async () => {
    const url = input.value.trim();
    problem.replaceChildren();
    if (!url) {
      input.focus();
      return;
    }
    submit.disabled = true;
    try {
      await api.importUrl(lib.value(), url);
      input.value = '';
      announce('Import queued');
      refresh();
    } catch (err) {
      const box = document.createElement('div');
      box.className = 'problem';
      box.append(icon('warn'));
      const text = document.createElement('div');
      text.textContent = errorMessage(err);
      box.append(text);
      problem.replaceChildren(box);
      announce(errorMessage(err));
    } finally {
      submit.disabled = false;
    }
  });
  input.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') { e.preventDefault(); submit.click(); }
  });

  refresh();
}
