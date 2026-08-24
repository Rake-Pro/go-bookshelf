/**
 * Admin, Settings: the whole application configuration, edited in the app.
 *
 * Every value here used to be a GOBOOKSHELF_* environment variable. It is now
 * one document at `GET|PUT /api/v1/admin/settings`, stored in the database with
 * the OIDC client secret encrypted. A save applies to the running server, so
 * there is nothing to restart.
 *
 * Each card saves on its own, so a mistake in one section cannot lose the edits
 * in another, and a rejected save leaves the server exactly as it was.
 */

import { api } from '../api.js';
import { store } from '../store.js';
import { page } from '../components/page.js';
import { emptyView, errorView, errorMessage, loadingView } from '../components/states.js';
import { icon } from '../components/icons.js';
import { announce } from '../live.js';
import { field } from './login.js';
import { checkField, oidcFields, oidcTestButton } from './setup.js';

export default async function adminSettings() {
  const { el, body } = page('Settings', {
    subtitle: 'Applies to the running server; nothing here needs a restart.',
    actions: [backLink()],
  });

  if (!store.isAdmin) {
    body.replaceChildren(emptyView(
      'Admins only',
      'Your account does not have permission to manage this server.',
      { label: 'Go home', href: '/' },
    ));
    return { el, title: 'Settings' };
  }

  await load(body);
  return { el, title: 'Settings' };
}

/** @param {HTMLElement} body */
async function load(body) {
  body.replaceChildren(loadingView('Loading settings'));
  let settings;
  try {
    settings = await api.adminSettings();
  } catch (e) {
    body.replaceChildren(errorView(e, () => load(body)));
    return;
  }

  body.replaceChildren(
    generalCard(settings),
    oidcCard(settings),
    proxyCard(settings),
    metadataCard(settings),
    metricsCard(settings),
    footer(settings),
  );
}

function backLink() {
  const a = document.createElement('a');
  a.className = 'btn';
  a.href = '/admin';
  a.textContent = 'Back to admin';
  return a;
}

/* ---------------- general ---------------- */

function generalCard(settings) {
  const { card, form, save, status } = sectionCard('General');
  const g = settings.general || {};

  form.append(
    field('Base URL', 'base_url', 'url', {
      value: g.base_url || '',
      hint: 'The address readers use. Decides sign-on redirects, catalog links and the cookie Secure flag.',
    }),
    selectField('Secure cookies', 'secure_cookies', g.secure_cookies || 'auto', [
      ['auto', 'Automatic (Secure when the base URL is https)'],
      ['on', 'Always Secure'],
      ['off', 'Never Secure'],
    ], 'Leave on automatic unless you terminate TLS somewhere this server cannot see.'),
    field('Session lifetime', 'session_ttl', 'text', {
      value: g.session_ttl || '',
      hint: 'A duration such as 720h or 30m. Applies to sessions created from now on.',
    }),
    field('Scan interval', 'scan_interval', 'text', {
      value: g.scan_interval || '',
      hint: 'How often every library is rescanned. 0 turns the timer off and leaves the file watcher.',
    }),
  );

  wireSave(form, save, status, () => {
    const fd = new FormData(form);
    return {
      general: {
        base_url: String(fd.get('base_url') || '').trim(),
        secure_cookies: String(fd.get('secure_cookies') || 'auto'),
        session_ttl: String(fd.get('session_ttl') || '').trim(),
        scan_interval: String(fd.get('scan_interval') || '').trim(),
      },
    };
  });
  return card;
}

/* ---------------- single sign-on ---------------- */

function oidcCard(settings) {
  const { card, form, save, status } = sectionCard('Single sign-on');
  const o = settings.oidc || {};

  const state = document.createElement('p');
  state.className = 'muted small';
  state.style.marginTop = '0';
  if (o.enabled && o.active) {
    state.textContent = 'Connected. The sign-in page offers the SSO button.';
  } else if (o.enabled) {
    state.textContent = 'Configured, but the provider could not be reached at the last attempt.';
  } else {
    state.textContent = 'Off. Everyone signs in with a username and password.';
  }
  form.append(state);

  if (o.redirect_url) {
    const note = document.createElement('p');
    note.className = 'muted small';
    note.append(document.createTextNode('Redirect URI to register with your provider: '));
    const code = document.createElement('code');
    code.textContent = o.redirect_url;
    note.append(code);
    form.append(note);
  }

  form.append(checkField('Use single sign-on', 'enabled', o.enabled === true,
    'Saving with this on runs discovery against the issuer; a provider that does not answer fails the save.'));

  const fields = oidcFields(o);
  form.append(...fields.elements);

  form.append(checkField('Allow password sign-in', 'local_login_enabled',
    o.local_login_enabled !== false,
    settings.admin_recovery
      ? 'GOBOOKSHELF_ADMIN_RECOVERY is set on this server, so the password form stays available whatever this says.'
      : 'Can only be turned off while single sign-on is on, so there is always a way in.'));

  form.append(oidcTestButton(() => fields.read()).el);

  wireSave(form, save, status, () => {
    const fd = new FormData(form);
    return {
      oidc: {
        ...fields.read(),
        enabled: fd.get('enabled') === 'on',
        local_login_enabled: fd.get('local_login_enabled') === 'on',
      },
    };
  });
  return card;
}

/* ---------------- reverse-proxy authentication ---------------- */

function proxyCard(settings) {
  const { card, form, save, status } = sectionCard('Reverse-proxy authentication');
  const p = settings.proxy_auth || {};

  form.append(
    checkField('Trust an authentication header', 'enabled', p.enabled === true,
      'For deployments where a proxy in front of this server has already authenticated the request.'),
    field('Header', 'header', 'text', {
      value: p.header || '',
      hint: 'The header naming the authenticated user, for example Remote-User.',
    }),
    textareaField('Trusted proxies', 'trusted_proxies', (p.trusted_proxies || []).join('\n'),
      'One CIDR or address per line. The header is honored only from these peers; '
      + 'forwarding headers never confer trust, so an empty list is refused.'),
  );

  wireSave(form, save, status, () => {
    const fd = new FormData(form);
    return {
      proxy_auth: {
        enabled: fd.get('enabled') === 'on',
        header: String(fd.get('header') || '').trim(),
        trusted_proxies: lines(String(fd.get('trusted_proxies') || '')),
      },
    };
  });
  return card;
}

/* ---------------- metadata ---------------- */

function metadataCard(settings) {
  const { card, form, save, status } = sectionCard('Online metadata');
  const m = settings.metadata || {};

  form.append(
    selectField('Provider', 'provider', m.provider || 'none', [
      ['none', 'None - never make outbound requests'],
      ['openlibrary', 'Open Library'],
    ], 'While this is None the server makes no outbound requests at all. '
      + 'Metadata embedded in your files is always the source of truth.'),
    checkField('Allow private addresses', 'allow_private', m.allow_private === true,
      'Only useful when the provider runs on your own network. Off refuses loopback, '
      + 'link-local and private destinations.'),
  );

  wireSave(form, save, status, () => {
    const fd = new FormData(form);
    return {
      metadata: {
        provider: String(fd.get('provider') || 'none'),
        allow_private: fd.get('allow_private') === 'on',
      },
    };
  });
  return card;
}

/* ---------------- metrics ---------------- */

function metricsCard(settings) {
  const { card, form, save, status } = sectionCard('Metrics');
  form.append(textareaField('Allowed peers', 'allow',
    (settings.metrics?.allow || []).join('\n'),
    'One CIDR or address per line; who may read /metrics. Empty restores loopback plus the private ranges.'));

  wireSave(form, save, status, () => ({
    metrics: { allow: lines(String(new FormData(form).get('allow') || '')) },
  }));
  return card;
}

/* ---------------- footer ---------------- */

function footer(settings) {
  const p = document.createElement('p');
  p.className = 'muted small';
  p.textContent = settings.updated_at
    ? `Last changed ${settings.updated_at}.`
    : 'These settings have not been changed since installation.';
  return p;
}

/* ---------------- shared pieces ---------------- */

/** @param {string} title */
function sectionCard(title) {
  const card = document.createElement('section');
  card.className = 'card';
  card.style.marginBottom = 'var(--s6)';

  const h2 = document.createElement('h2');
  h2.textContent = title;

  const form = document.createElement('form');
  form.noValidate = true;

  const save = document.createElement('button');
  save.type = 'submit';
  save.className = 'btn btn--primary btn--lg';
  save.textContent = 'Save';

  const status = document.createElement('p');
  status.className = 'small';
  status.style.margin = 'var(--s2) 0 0';
  status.setAttribute('role', 'status');
  status.setAttribute('aria-live', 'polite');

  card.append(h2, form);
  return { card, form, save, status };
}

/**
 * Appends the save button and wires the submit handler.
 * @param {HTMLFormElement} form
 * @param {HTMLButtonElement} save
 * @param {HTMLElement} status
 * @param {() => object} read
 */
function wireSave(form, save, status, read) {
  form.append(save, status);
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    save.disabled = true;
    status.style.color = 'var(--muted)';
    status.replaceChildren(document.createTextNode('Saving...'));
    try {
      await api.putAdminSettings(read());
      status.style.color = 'var(--ok)';
      status.replaceChildren(icon('check', { size: '1.25rem' }), document.createTextNode('Saved and applied.'));
      announce('Settings saved');
    } catch (err) {
      status.style.color = 'var(--danger)';
      status.replaceChildren(icon('warn', { size: '1.25rem' }), document.createTextNode(errorMessage(err)));
      announce(errorMessage(err));
    } finally {
      save.disabled = false;
    }
  });
}

/**
 * @param {string} label @param {string} name @param {string} value
 * @param {[string,string][]} options @param {string} [hint]
 */
function selectField(label, name, value, options, hint) {
  const wrap = document.createElement('label');
  wrap.className = 'field';
  const l = document.createElement('span');
  l.className = 'label';
  l.textContent = label;
  const select = document.createElement('select');
  select.name = name;
  select.id = 'f-' + name;
  for (const [v, text] of options) {
    const o = document.createElement('option');
    o.value = v;
    o.textContent = text;
    o.selected = v === value;
    select.append(o);
  }
  wrap.setAttribute('for', select.id);
  wrap.append(l, select);
  if (hint) {
    const h = document.createElement('span');
    h.className = 'hint';
    h.textContent = hint;
    wrap.append(h);
  }
  return wrap;
}

/** @param {string} label @param {string} name @param {string} value @param {string} [hint] */
function textareaField(label, name, value, hint) {
  const wrap = document.createElement('label');
  wrap.className = 'field';
  const l = document.createElement('span');
  l.className = 'label';
  l.textContent = label;
  const area = document.createElement('textarea');
  area.name = name;
  area.id = 'f-' + name;
  area.value = value;
  wrap.setAttribute('for', area.id);
  wrap.append(l, area);
  if (hint) {
    const h = document.createElement('span');
    h.className = 'hint';
    h.textContent = hint;
    wrap.append(h);
  }
  return wrap;
}

/** @param {string} v @returns {string[]} */
function lines(v) {
  return v.split('\n').map((s) => s.trim()).filter(Boolean);
}
