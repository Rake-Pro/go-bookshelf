/**
 * Sign in.
 *
 * The "Sign in with SSO" button is shown only when the auth probe says the
 * server has OIDC configured. The probe is `GET /api/v1/auth/me` followed, when
 * that answers 401, by the public `GET /api/v1/auth/status`.
 * See docs/FRONTEND.md.
 */

import { auth, probeAuth, ApiError } from '../api.js';
import { store } from '../store.js';
import { navigate } from '../router.js';
import { icon } from '../components/icons.js';
import { errorMessage } from '../components/states.js';

/** @param {import('../router.js').RouteCtx} ctx */
export default async function login(ctx) {
  const next = ctx.query.get('next') || '/';

  const wrap = document.createElement('div');
  wrap.className = 'auth-wrap';
  const card = document.createElement('main');
  card.className = 'card auth-card';
  card.id = 'main';

  const h1 = document.createElement('h1');
  h1.textContent = 'Bookshelf';
  h1.tabIndex = -1;
  const lead = document.createElement('p');
  lead.className = 'muted';
  lead.textContent = 'Sign in to your library.';
  card.append(h1, lead);

  const errBox = document.createElement('div');
  errBox.className = 'formerror';
  errBox.setAttribute('role', 'alert');
  errBox.hidden = true;
  card.append(errBox);

  const form = document.createElement('form');
  form.noValidate = true;
  form.append(
    field('Username', 'username', 'text', { autocomplete: 'username', required: true }),
    field('Password', 'password', 'password', { autocomplete: 'current-password', required: true }),
  );

  const submit = document.createElement('button');
  submit.type = 'submit';
  submit.className = 'btn btn--primary btn--lg';
  submit.style.width = '100%';
  submit.textContent = 'Sign in';
  form.append(submit);
  card.append(form);

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    errBox.hidden = true;
    const data = new FormData(form);
    const username = String(data.get('username') || '').trim();
    const password = String(data.get('password') || '');
    if (!username || !password) {
      showError('Enter both a username and a password.');
      return;
    }
    submit.disabled = true;
    submit.textContent = 'Signing in...';
    try {
      const user = await auth.login(username, password);
      store.setUser(user);
      await store.load().catch(() => {});
      navigate(next.startsWith('/') ? next : '/', { replace: true });
    } catch (err) {
      showError(err instanceof ApiError && err.status === 401
        ? 'Wrong username or password.'
        : errorMessage(err));
    } finally {
      submit.disabled = false;
      submit.textContent = 'Sign in';
    }
  });

  /** @param {string} msg */
  function showError(msg) {
    errBox.replaceChildren(icon('warn'), document.createTextNode(msg));
    errBox.hidden = false;
  }

  /* Probe for OIDC and first-run state. */
  try {
    const { oidc, setupRequired, user } = await probeAuth();
    if (user) { navigate(next.startsWith('/') ? next : '/', { replace: true }); }
    if (setupRequired) {
      const notice = document.createElement('p');
      notice.className = 'muted';
      const a = document.createElement('a');
      a.href = '/setup';
      a.textContent = 'Finish first-run setup';
      notice.append('This server has no accounts yet. ', a, '.');
      card.append(notice);
    }
    if (oidc) {
      const div = document.createElement('div');
      div.className = 'divider';
      div.textContent = 'or';
      const sso = document.createElement('a');
      sso.className = 'btn btn--lg';
      sso.style.width = '100%';
      sso.href = auth.oidcStartUrl();
      sso.textContent = 'Sign in with SSO';
      card.append(div, sso);
    }
  } catch { /* probe failure only hides the SSO button */ }

  wrap.append(card);
  queueMicrotask(() => /** @type {HTMLElement|null} */ (form.querySelector('input'))?.focus());
  return { el: wrap, title: 'Sign in' };
}

/**
 * @param {string} label
 * @param {string} name
 * @param {string} type
 * @param {{autocomplete?:string, required?:boolean, hint?:string, value?:string}} [opts]
 */
export function field(label, name, type, opts = {}) {
  const wrap = document.createElement('label');
  wrap.className = 'field';
  const l = document.createElement('span');
  l.className = 'label';
  l.textContent = label;
  const input = document.createElement('input');
  input.type = type;
  input.name = name;
  input.id = 'f-' + name;
  if (opts.autocomplete) input.autocomplete = opts.autocomplete;
  if (opts.required) input.required = true;
  if (opts.value) input.value = opts.value;
  wrap.setAttribute('for', input.id);
  wrap.append(l, input);
  if (opts.hint) {
    const h = document.createElement('span');
    h.className = 'hint';
    h.textContent = opts.hint;
    wrap.append(h);
  }
  return wrap;
}
