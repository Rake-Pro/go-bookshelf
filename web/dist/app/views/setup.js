/**
 * First-run wizard.
 *
 * Six steps, one card, one step visible at a time: the one-time token, the
 * administrator account, the external URL, single sign-on (skippable), the
 * first library (skippable), and done. Each step posts to its own
 * `POST /api/v1/setup/{step}` route, so a reload resumes where the server
 * already is rather than replaying everything.
 *
 * Nothing here is configured with an environment variable: the token is the
 * gate, and every value the server needs is typed into these fields.
 */

import { setup, api, authStatus } from '../api.js';
import { store } from '../store.js';
import { navigate } from '../router.js';
import { icon } from '../components/icons.js';
import { errorMessage } from '../components/states.js';
import { announce } from '../live.js';
import { field } from './login.js';

/** The step order, for the "step n of m" counter and the back button. */
const STEPS = ['token', 'admin', 'base-url', 'oidc', 'library', 'done'];

export default async function setupView() {
  const wrap = document.createElement('div');
  wrap.className = 'auth-wrap';
  const card = document.createElement('main');
  card.className = 'card auth-card';
  card.id = 'main';
  card.style.width = 'min(34rem, 100%)';
  wrap.append(card);

  /** Everything gathered so far. `token` is needed by the account step too. */
  const state = { token: '', baseUrl: '', redirectUrl: '' };

  /** @param {string} name */
  function show(name) {
    const step = STEPS.indexOf(name);
    card.replaceChildren();

    const counter = document.createElement('p');
    counter.className = 'muted small';
    counter.style.margin = '0 0 var(--s2)';
    counter.textContent = `Step ${step + 1} of ${STEPS.length}`;
    card.append(counter);

    const build = {
      'token': stepToken,
      'admin': stepAdmin,
      'base-url': stepBaseURL,
      'oidc': stepOIDC,
      'library': stepLibrary,
      'done': stepDone,
    }[name];
    card.append(build());

    // Move focus to the new step's heading so a screen reader announces it.
    const h = /** @type {HTMLElement|null} */ (card.querySelector('h1'));
    queueMicrotask(() => {
      h?.focus();
      /** @type {HTMLElement|null} */ (card.querySelector('input, select'))?.focus();
    });
  }

  /* ---------------- step 1: the one-time token ---------------- */

  function stepToken() {
    const { el, form, err, submit } = stepShell(
      'Set up Bookshelf',
      'The server printed a one-time setup token to its log when it first started.',
      'Continue',
    );
    form.append(field('Setup token', 'token', 'text', {
      required: true,
      autocomplete: 'off',
      hint: 'Look for "one-time token" in the server log.',
    }));

    onSubmit(form, submit, err, async () => {
      const token = String(new FormData(form).get('token') || '').trim();
      if (!token) throw new Error('Paste the setup token from the server log.');
      const res = await setup.checkToken(token);
      state.token = token;
      state.baseUrl = res?.suggested_base_url || location.origin;
      show('admin');
    });
    return el;
  }

  /* ---------------- step 2: the administrator ---------------- */

  function stepAdmin() {
    const { el, form, err, submit } = stepShell(
      'Create the administrator',
      'This is the account you will manage the server with. There are no default credentials.',
      'Create account',
    );
    form.append(
      field('Username', 'username', 'text', { required: true, autocomplete: 'username' }),
      field('Display name', 'display_name', 'text', { autocomplete: 'name' }),
      field('Password', 'password', 'password', {
        required: true,
        autocomplete: 'new-password',
        hint: 'At least 10 characters.',
      }),
    );

    onSubmit(form, submit, err, async () => {
      const fd = new FormData(form);
      const body = {
        token: state.token,
        username: String(fd.get('username') || '').trim(),
        display_name: String(fd.get('display_name') || '').trim(),
        password: String(fd.get('password') || ''),
      };
      if (!body.username || !body.password) {
        throw new Error('A username and a password are required.');
      }
      // The server's own minimum; see auth.MinPasswordLength.
      if (body.password.length < 10) {
        throw new Error('Use a password of at least 10 characters.');
      }
      const user = await setup.createAdmin(body);
      store.setUser(user);
      show('base-url');
    });
    return el;
  }

  /* ---------------- step 3: the external URL ---------------- */

  function stepBaseURL() {
    const { el, form, err, submit } = stepShell(
      'Where is this server reached?',
      'Used for single sign-on redirects, catalog links, and whether session cookies are marked Secure.',
      'Save and continue',
    );
    form.append(field('Base URL', 'base_url', 'url', {
      required: true,
      value: state.baseUrl,
      hint: 'The address your readers type, for example https://books.example.com. No trailing slash.',
    }));

    onSubmit(form, submit, err, async () => {
      const value = String(new FormData(form).get('base_url') || '').trim();
      if (!value) throw new Error('Enter the URL this server is reached on.');
      const res = await setup.baseUrl(value);
      state.baseUrl = res?.base_url || value;
      state.redirectUrl = res?.redirect_url || '';
      show('oidc');
    });
    return el;
  }

  /* ---------------- step 4: single sign-on ---------------- */

  function stepOIDC() {
    const { el, form, err, submit } = stepShell(
      'Single sign-on',
      'Optional. Connect an OpenID Connect provider so people sign in with an account they already have.',
      'Save and continue',
    );

    if (state.redirectUrl) {
      const note = document.createElement('p');
      note.className = 'muted small';
      note.append(document.createTextNode('Register this redirect URI with your provider: '));
      const code = document.createElement('code');
      code.textContent = state.redirectUrl;
      note.append(code);
      form.append(note);
    }

    const fields = oidcFields();
    form.append(...fields.elements);

    form.append(oidcTestButton(() => fields.read()).el);

    onSubmit(form, submit, err, async () => {
      const body = fields.read();
      if (!body.issuer && !body.client_id && !body.client_secret) {
        throw new Error('Fill the issuer, client id and client secret, or choose "Skip for now".');
      }
      await setup.oidc({ ...body, enabled: true });
      show('library');
    });

    form.append(skipButton('Skip for now', submit, err, async () => {
      await setup.oidc({ skip: true });
      show('library');
    }));

    return el;
  }

  /* ---------------- step 5: the first library ---------------- */

  function stepLibrary() {
    const { el, form, err, submit } = stepShell(
      'Add your first library',
      'Point it at a directory of books the server can read. You can add more later.',
      'Create library',
    );
    form.append(
      field('Name', 'name', 'text', { hint: 'For example: Family ebooks.' }),
      kindField(),
      field('Path', 'path', 'text', {
        hint: 'One absolute path, readable by the server, for example /books.',
      }),
      checkField('Create the folder if it does not exist yet', 'create_missing', true,
        'Handy on a fresh, empty media share.'),
    );

    onSubmit(form, submit, err, async () => {
      const fd = new FormData(form);
      const body = {
        name: String(fd.get('name') || '').trim(),
        kind: String(fd.get('kind') || 'mixed'),
        path: String(fd.get('path') || '').trim(),
        create_missing: fd.get('create_missing') === 'on',
      };
      if (!body.name || !body.path) {
        throw new Error('Enter a name and a path, or choose "Skip for now".');
      }
      await setup.library(body);
      await finish();
    });

    form.append(skipButton('Skip for now', submit, err, async () => {
      await setup.library({ skip: true });
      await finish();
    }));
    return el;
  }

  async function finish() {
    await setup.complete();
    await store.load().catch(() => {});
    show('done');
  }

  /* ---------------- step 6: done ---------------- */

  function stepDone() {
    const el = document.createElement('div');
    const h1 = document.createElement('h1');
    h1.textContent = 'Bookshelf is ready';
    h1.tabIndex = -1;
    const lead = document.createElement('p');
    lead.className = 'muted';
    lead.textContent = 'Everything above can be changed later under Admin, Settings.';
    const go = document.createElement('button');
    go.type = 'button';
    go.className = 'btn btn--primary btn--lg';
    go.style.width = '100%';
    go.textContent = 'Go to the admin screen';
    go.addEventListener('click', () => navigate('/admin', { replace: true }));
    el.append(h1, lead, go);
    announce('Setup complete');
    return el;
  }

  // Resume rather than restart: a reload part-way through arrives with the
  // administrator already created and the one-time token already spent, so
  // asking for it again would be a dead end.
  const status = await authStatus();
  if (status.setup_complete === true) {
    navigate('/admin', { replace: true });
    return { el: wrap, title: 'Setup' };
  }
  show(store.user ? 'base-url' : 'token');
  return { el: wrap, title: 'Setup' };
}

/* ---------------- shared pieces ---------------- */

/**
 * The heading, lead paragraph, error box, form and primary button every step
 * is built from.
 * @param {string} title @param {string} lead @param {string} action
 */
function stepShell(title, lead, action) {
  const el = document.createElement('div');
  const h1 = document.createElement('h1');
  h1.textContent = title;
  h1.tabIndex = -1;
  const p = document.createElement('p');
  p.className = 'muted';
  p.textContent = lead;

  const err = document.createElement('div');
  err.className = 'formerror';
  err.setAttribute('role', 'alert');
  err.hidden = true;

  const form = document.createElement('form');
  form.noValidate = true;

  const submit = document.createElement('button');
  submit.type = 'submit';
  submit.className = 'btn btn--primary btn--lg';
  submit.style.width = '100%';
  submit.textContent = action;

  el.append(h1, p, err, form);
  return { el, form, err, submit };
}

/**
 * Wires a step's submit handler: the primary button is appended last so it
 * always sits below the fields the step added.
 * @param {HTMLFormElement} form
 * @param {HTMLButtonElement} submit
 * @param {HTMLElement} err
 * @param {() => Promise<void>} run
 */
function onSubmit(form, submit, err, run) {
  form.append(submit);
  const label = submit.textContent || '';
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    err.hidden = true;
    submit.disabled = true;
    submit.textContent = 'Working...';
    try {
      await run();
    } catch (ex) {
      err.replaceChildren(icon('warn'), document.createTextNode(errorMessage(ex)));
      err.hidden = false;
      announce(errorMessage(ex));
    } finally {
      submit.disabled = false;
      submit.textContent = label;
    }
  });
}

/**
 * The "Skip for now" control a step offers alongside its primary action.
 * @param {string} label
 * @param {HTMLButtonElement} submit
 * @param {HTMLElement} err
 * @param {() => Promise<void>} run
 */
function skipButton(label, submit, err, run) {
  const btn = document.createElement('button');
  btn.type = 'button';
  btn.className = 'btn btn--lg';
  btn.style.width = '100%';
  btn.style.marginTop = 'var(--s3)';
  btn.textContent = label;
  btn.addEventListener('click', async () => {
    err.hidden = true;
    btn.disabled = true;
    submit.disabled = true;
    try {
      await run();
    } catch (ex) {
      err.replaceChildren(icon('warn'), document.createTextNode(errorMessage(ex)));
      err.hidden = false;
      announce(errorMessage(ex));
    } finally {
      btn.disabled = false;
      submit.disabled = false;
    }
  });
  return btn;
}

/** The library kind selector. */
function kindField() {
  const wrap = document.createElement('label');
  wrap.className = 'field';
  const l = document.createElement('span');
  l.className = 'label';
  l.textContent = 'Kind';
  const select = document.createElement('select');
  select.name = 'kind';
  select.id = 'f-kind';
  for (const [value, text] of [['mixed', 'Ebooks and audiobooks'], ['ebook', 'Ebooks'], ['audiobook', 'Audiobooks']]) {
    const o = document.createElement('option');
    o.value = value;
    o.textContent = text;
    select.append(o);
  }
  wrap.setAttribute('for', select.id);
  wrap.append(l, select);
  return wrap;
}

/**
 * The OIDC fields, shared in spirit with the admin settings page. Returns the
 * elements plus a reader that turns them into the request body.
 * @param {object} [initial]
 */
export function oidcFields(initial = {}) {
  const elements = [
    field('Issuer URL', 'issuer', 'url', {
      value: initial.issuer || '',
      hint: 'Copied from your provider exactly as it publishes it, trailing slash and all.',
    }),
    field('Client ID', 'client_id', 'text', { value: initial.client_id || '' }),
    field('Client secret', 'client_secret', 'password', {
      autocomplete: 'new-password',
      hint: initial.has_client_secret
        ? 'A secret is stored. Leave this empty to keep it.'
        : 'Stored encrypted; it is never shown again.',
    }),
    field('Admin group', 'admin_group', 'text', {
      value: initial.admin_group || '',
      hint: 'Members of this group get the administrator role on every sign-in. For example: myapp-admins.',
    }),
    field('User group', 'user_group', 'text', {
      value: initial.user_group || '',
      hint: 'When set, only members of the admin or user group may sign in at all. '
        + 'Leave empty to admit anyone your provider authenticates. For example: myapp-users.',
    }),
    field('Groups claim', 'groups_claim', 'text', {
      value: initial.groups_claim || 'groups',
      hint: 'The claim the two group names are matched against.',
    }),
    field('Scopes', 'scopes', 'text', {
      value: initial.scopes || '',
      hint: 'Comma separated. Empty means openid, profile and email.',
    }),
    checkField('Create accounts automatically', 'auto_register',
      initial.auto_register !== false,
      'Off means somebody must already have made the account before they can sign in.'),
  ];

  /** @returns {Record<string, any>} */
  function read() {
    const form = elements[0].closest('form');
    const fd = new FormData(/** @type {HTMLFormElement} */ (form));
    const str = (name) => String(fd.get(name) || '').trim();
    return {
      issuer: str('issuer'),
      client_id: str('client_id'),
      client_secret: String(fd.get('client_secret') || ''),
      admin_group: str('admin_group'),
      user_group: str('user_group'),
      groups_claim: str('groups_claim') || 'groups',
      scopes: str('scopes'),
      auto_register: fd.get('auto_register') === 'on',
    };
  }

  return { elements, read };
}

/**
 * A checkbox laid out like the other fields.
 * @param {string} label @param {string} name @param {boolean} checked @param {string} [hint]
 */
export function checkField(label, name, checked, hint) {
  const wrap = document.createElement('div');
  wrap.className = 'field';
  const row = document.createElement('label');
  row.className = 'check';
  const input = document.createElement('input');
  input.type = 'checkbox';
  input.name = name;
  input.id = 'f-' + name;
  input.checked = checked;
  const text = document.createElement('span');
  text.textContent = label;
  row.append(input, text);
  wrap.append(row);
  if (hint) {
    const h = document.createElement('span');
    h.className = 'hint';
    h.textContent = hint;
    wrap.append(h);
  }
  return wrap;
}

/**
 * The "Test connection" button and its live region. Discovery is run against
 * the values in the form without saving them, so a wrong issuer is found
 * before it is the thing standing between an operator and their server.
 * @param {() => Record<string, any>} read
 */
export function oidcTestButton(read) {
  const el = document.createElement('div');
  el.style.marginBottom = 'var(--s4)';

  const btn = document.createElement('button');
  btn.type = 'button';
  btn.className = 'btn btn--lg';
  btn.textContent = 'Test connection';

  const out = document.createElement('p');
  out.className = 'small';
  out.style.margin = 'var(--s2) 0 0';
  out.setAttribute('role', 'status');
  out.setAttribute('aria-live', 'polite');

  async function run() {
    btn.disabled = true;
    out.replaceChildren(document.createTextNode('Testing...'));
    try {
      const res = await api.testOidc(read());
      if (res?.ok) {
        out.style.color = 'var(--ok)';
        out.replaceChildren(icon('check', { size: '1.25rem' }), document.createTextNode(
          `Discovery succeeded. Group membership is read from the "${res.groups_claim}" claim.`));
      } else {
        out.style.color = 'var(--danger)';
        out.replaceChildren(icon('warn', { size: '1.25rem' }), document.createTextNode(res?.error || 'The provider could not be reached.'));
      }
    } catch (e) {
      out.style.color = 'var(--danger)';
      out.replaceChildren(icon('warn', { size: '1.25rem' }), document.createTextNode(errorMessage(e)));
    } finally {
      btn.disabled = false;
    }
  }

  btn.addEventListener('click', run);
  el.append(btn, out);
  return { el, run };
}
