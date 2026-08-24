/**
 * Settings: profile, app appearance, reading defaults and playback defaults.
 * Every control writes through the store, which debounces one PUT /me/settings.
 */

import { auth } from '../api.js';
import { store } from '../store.js';
import { page } from '../components/page.js';
import { navigate } from '../router.js';
import { readerSettingsControls, radioGroup, rangeField } from '../components/reader-settings.js';
import { announce } from '../live.js';
import { player } from '../player.js';

export default async function settings() {
  const { el, body } = page('Settings');
  // Tears down the reading controls' window listener when the route exits.
  const ac = new AbortController();

  body.append(
    profileSection(),
    appearanceSection(),
    readingSection(ac.signal),
    playbackSection(),
  );

  return { el, title: 'Settings', destroy: () => ac.abort() };
}

/** @param {string} title */
function group(title) {
  const s = document.createElement('section');
  s.className = 'card';
  s.style.marginBottom = 'var(--s6)';
  const h = document.createElement('h2');
  h.textContent = title;
  s.append(h);
  return s;
}

function profileSection() {
  const s = group('Account');
  const u = store.user;
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
  row('Name', u?.display_name || '');
  row('Username', u?.username || '');
  row('Role', u?.role || '');
  s.append(dl);

  const out = document.createElement('button');
  out.type = 'button';
  out.className = 'btn btn--danger';
  out.textContent = 'Sign out';
  out.addEventListener('click', async () => {
    out.disabled = true;
    try { await auth.logout(); } catch { /* session may already be gone */ }
    store.setUser(null);
    navigate('/login', { replace: true });
    location.reload();
  });
  s.append(out);
  return s;
}

function appearanceSection() {
  const s = group('Appearance');
  s.append(radioGroup('App theme', 'app-theme', [
    ['auto', 'Match system'],
    ['light', 'Light'],
    ['dark', 'Dark'],
    ['hc-light', 'High contrast light'],
    ['hc-dark', 'High contrast dark'],
  ], store.ui.theme, (v) => {
    store.update('ui', { theme: /** @type {any} */ (v) });
    announce(`Theme set to ${v === 'auto' ? 'match system' : v}`);
  }));

  s.append(rangeField('Interface text size', store.ui.text_scale, 1, 1.6, 0.05,
    (v) => store.update('ui', { text_scale: v }),
    (v) => `${Math.round(v * 100)}%`));

  const note = document.createElement('p');
  note.className = 'muted small';
  note.textContent =
    'The interface scales with your system text size as well; this is an extra '
    + 'multiplier on top of it.';
  s.append(note);
  return s;
}

function readingSection(signal) {
  const s = group('Reading defaults');
  const p = document.createElement('p');
  p.className = 'muted small';
  p.textContent = 'These apply to every book. You can change them while reading too.';
  s.append(p);
  s.append(readerSettingsControls(() => {}, { signal }));
  return s;
}

function playbackSection() {
  const s = group('Playback defaults');

  s.append(rangeField('Speed', store.player.speed, 0.5, 3, 0.05,
    (v) => { store.update('player', { speed: v }); player.audio.playbackRate = v; },
    (v) => `${v.toFixed(2)}x`));

  s.append(radioGroup('Skip back', 'skip-back',
    [['5', '5s'], ['10', '10s'], ['15', '15s'], ['30', '30s']],
    String(store.player.skip_back_s),
    (v) => store.update('player', { skip_back_s: Number(v) })));

  s.append(radioGroup('Skip forward', 'skip-fwd',
    [['15', '15s'], ['30', '30s'], ['45', '45s'], ['60', '60s']],
    String(store.player.skip_fwd_s),
    (v) => store.update('player', { skip_fwd_s: Number(v) })));

  const boost = document.createElement('label');
  boost.className = 'check';
  const cb = document.createElement('input');
  cb.type = 'checkbox';
  cb.checked = Boolean(store.player.volume_boost);
  cb.addEventListener('change', () => store.update('player', { volume_boost: cb.checked }));
  const sp = document.createElement('span');
  sp.textContent = 'Volume boost for quiet recordings';
  boost.append(cb, sp);
  s.append(boost);

  return s;
}
