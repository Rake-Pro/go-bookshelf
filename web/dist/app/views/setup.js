/**
 * First-run setup. The server prints a one-time token to its log; the operator
 * pastes it here together with the first admin account.
 */

import { auth } from '../api.js';
import { store } from '../store.js';
import { navigate } from '../router.js';
import { icon } from '../components/icons.js';
import { errorMessage } from '../components/states.js';
import { field } from './login.js';

export default async function setup() {
  const wrap = document.createElement('div');
  wrap.className = 'auth-wrap';
  const card = document.createElement('main');
  card.className = 'card auth-card';
  card.id = 'main';

  const h1 = document.createElement('h1');
  h1.textContent = 'Set up Bookshelf';
  h1.tabIndex = -1;
  const lead = document.createElement('p');
  lead.className = 'muted';
  lead.textContent = 'The setup token was printed to the server log when it first started.';
  card.append(h1, lead);

  const errBox = document.createElement('div');
  errBox.className = 'formerror';
  errBox.setAttribute('role', 'alert');
  errBox.hidden = true;
  card.append(errBox);

  const form = document.createElement('form');
  form.noValidate = true;
  form.append(
    field('Setup token', 'token', 'text', { required: true, autocomplete: 'off' }),
    field('Username', 'username', 'text', { required: true, autocomplete: 'username' }),
    field('Display name', 'display_name', 'text', { autocomplete: 'name' }),
    field('Password', 'password', 'password', {
      required: true,
      autocomplete: 'new-password',
      hint: 'At least 12 characters.',
    }),
  );

  const submit = document.createElement('button');
  submit.type = 'submit';
  submit.className = 'btn btn--primary btn--lg';
  submit.style.width = '100%';
  submit.textContent = 'Create admin account';
  form.append(submit);
  card.append(form);

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    errBox.hidden = true;
    const fd = new FormData(form);
    const body = {
      token: String(fd.get('token') || '').trim(),
      username: String(fd.get('username') || '').trim(),
      display_name: String(fd.get('display_name') || '').trim(),
      password: String(fd.get('password') || ''),
    };
    if (!body.token || !body.username || !body.password) {
      show('Token, username and password are required.');
      return;
    }
    if (body.password.length < 12) {
      show('Use a password of at least 12 characters.');
      return;
    }
    submit.disabled = true;
    submit.textContent = 'Creating...';
    try {
      const user = await auth.setup(body);
      store.setUser(user);
      await store.load().catch(() => {});
      navigate('/admin', { replace: true });
    } catch (err) {
      show(errorMessage(err));
    } finally {
      submit.disabled = false;
      submit.textContent = 'Create admin account';
    }
  });

  /** @param {string} msg */
  function show(msg) {
    errBox.replaceChildren(icon('warn'), document.createTextNode(msg));
    errBox.hidden = false;
  }

  wrap.append(card);
  queueMicrotask(() => /** @type {HTMLElement|null} */ (form.querySelector('input'))?.focus());
  return { el: wrap, title: 'Setup' };
}
