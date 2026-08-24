/**
 * Client state: the signed-in user, reader/player/ui settings and theme.
 *
 * Settings live on the server (`GET|PUT /api/v1/me/settings`) so they follow the
 * user across devices, and are mirrored into localStorage so a cold start can
 * apply the right theme and font scale before the network answers. Writes are
 * debounced 500 ms and merged, so dragging a slider produces one PUT.
 */

import { api, request } from './api.js';

const LS_KEY = 'bookshelf.settings.v1';

/**
 * @typedef {Object} ReaderSettings
 * @property {number} font_scale        0.7 - 2.5, step 0.05
 * @property {'publisher'|'system'|'serif'|'sans'|'dyslexic'} font_family
 * @property {number} line_height       1.0 - 2.4
 * @property {number} letter_spacing    em
 * @property {number} word_spacing      em
 * @property {number} paragraph_spacing em
 * @property {'narrow'|'normal'|'wide'} margin
 * @property {'publisher'|'left'|'justify'} align
 * @property {'light'|'dark'|'sepia'|'gray'|'hc-dark'|'hc-light'|'custom'} theme
 * @property {string} custom_fg
 * @property {string} custom_bg
 * @property {'paginated'|'scrolled'} layout
 * @property {'auto'|'1'|'2'} columns
 */

/**
 * @typedef {Object} PlayerSettings
 * @property {number} speed             0.5 - 3.0, step 0.05
 * @property {number} skip_back_s
 * @property {number} skip_fwd_s
 * @property {number|null} sleep_timer_min
 * @property {boolean} sleep_end_of_chapter
 * @property {boolean} volume_boost
 */

/**
 * @typedef {Object} UiSettings
 * @property {'auto'|'light'|'dark'|'hc-light'|'hc-dark'} theme
 * @property {number} text_scale        1.0 - 1.6, scales app chrome only
 */

/** @type {ReaderSettings} */
export const READER_DEFAULTS = {
  font_scale: 1.15,
  font_family: 'publisher',
  line_height: 1.6,
  letter_spacing: 0,
  word_spacing: 0,
  paragraph_spacing: 0,
  margin: 'normal',
  align: 'publisher',
  theme: 'light',
  custom_fg: '#1f1d1a',
  custom_bg: '#faf8f4',
  layout: 'paginated',
  columns: 'auto',
};

/** @type {PlayerSettings} */
export const PLAYER_DEFAULTS = {
  speed: 1.0,
  skip_back_s: 15,
  skip_fwd_s: 30,
  sleep_timer_min: null,
  sleep_end_of_chapter: false,
  volume_boost: false,
};

/** @type {UiSettings} */
export const UI_DEFAULTS = {
  theme: 'auto',
  text_scale: 1.0,
};

/** Everything that is not an explicit reader theme falls back to the app theme. */
const APP_THEMES = ['light', 'dark', 'hc-light', 'hc-dark'];

class Store extends EventTarget {
  /** @type {any|null} */
  user = null;
  /** @type {ReaderSettings} */
  reader = { ...READER_DEFAULTS };
  /** @type {PlayerSettings} */
  player = { ...PLAYER_DEFAULTS };
  /** @type {UiSettings} */
  ui = { ...UI_DEFAULTS };
  /** True once settings have come back from the server at least once. */
  loaded = false;

  /** @type {number|undefined} */
  #timer;
  /** @type {{reader?:object,player?:object,ui?:object}} */
  #pending = {};

  constructor() {
    super();
    this.#readCache();
    if (window.matchMedia) {
      const mq = window.matchMedia('(prefers-color-scheme: dark)');
      const on = () => { if (this.ui.theme === 'auto') this.applyTheme(); };
      if (mq.addEventListener) mq.addEventListener('change', on);
    }
  }

  #readCache() {
    try {
      const raw = localStorage.getItem(LS_KEY);
      if (!raw) return;
      const c = JSON.parse(raw);
      this.reader = { ...READER_DEFAULTS, ...(c.reader || {}) };
      this.player = { ...PLAYER_DEFAULTS, ...(c.player || {}) };
      this.ui = { ...UI_DEFAULTS, ...(c.ui || {}) };
    } catch { /* corrupt or unavailable storage: fall back to defaults */ }
  }

  #writeCache() {
    try {
      localStorage.setItem(LS_KEY, JSON.stringify({
        reader: this.reader, player: this.player, ui: this.ui,
      }));
    } catch { /* private mode / quota: server copy is still authoritative */ }
  }

  /** Pull the server copy and apply it. Safe to call on every app start. */
  async load() {
    const s = await api.settings();
    this.reader = { ...READER_DEFAULTS, ...(s?.reader || {}) };
    this.player = { ...PLAYER_DEFAULTS, ...(s?.player || {}) };
    this.ui = { ...UI_DEFAULTS, ...(s?.ui || {}) };
    this.loaded = true;
    this.#writeCache();
    this.applyTheme();
    this.applyTextScale();
    this.#emit('settings');
  }

  /** @param {'reader'|'player'|'ui'} group @param {object} patch */
  update(group, patch) {
    this[group] = { ...this[group], ...patch };
    this.#pending[group] = { ...(this.#pending[group] || {}), ...patch };
    this.#writeCache();
    if (group === 'ui' && 'theme' in patch) this.applyTheme();
    if (group === 'ui' && 'text_scale' in patch) this.applyTextScale();
    this.#emit('settings', { group, patch });
    this.#schedule();
  }

  #schedule() {
    clearTimeout(this.#timer);
    this.#timer = setTimeout(() => this.flush(), 500);
  }

  /**
   * Force any pending settings write out now (used on pagehide).
   * @param {{keepalive?:boolean, retry?:boolean}} [opts] keepalive survives page
   *   unload, where a normal fetch may be cancelled, matching player.js
   *   saveProgress; retry marks the one automatic re-attempt after a failure
   *   so it does not schedule another one of itself.
   */
  async flush(opts = {}) {
    clearTimeout(this.#timer);
    this.#timer = undefined;
    if (!Object.keys(this.#pending).length) return;
    const body = this.#pending;
    this.#pending = {};
    try {
      if (opts.keepalive) {
        await request('/me/settings', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
          keepalive: true,
        });
      } else {
        await api.putSettings(body);
      }
    } catch (e) {
      // Merge the unsent patch back under anything newer, so it is not lost
      // and a later flush retries it.
      for (const group of /** @type {const} */ (['reader', 'player', 'ui'])) {
        if (body[group]) this.#pending[group] = { ...body[group], ...(this.#pending[group] || {}) };
      }
      this.#emit('settings-error', e);
      // One automatic retry through the debounce timer, so a transient
      // failure is not stuck waiting for the next edit or pagehide. If the
      // retry itself fails, leave the patch pending as usual rather than
      // retrying forever, and skip it entirely for a pagehide flush, which
      // has no page left to retry from.
      // ... but never clobber a sooner timer: an edit made while this PUT was
      // in flight has already armed its own 500 ms debounce, which covers the
      // merged-back patch too.
      if (!opts.retry && !opts.keepalive && this.#timer === undefined) {
        this.#timer = setTimeout(() => this.flush({ retry: true }), 5000);
      }
    }
  }

  /** @param {any} user */
  setUser(user) {
    this.user = user;
    this.#emit('user');
  }

  get isAdmin() { return this.user?.role === 'admin'; }

  /**
   * Whether this account may add books. The server folds the role into the
   * flag before answering `/auth/me`, so there is one rule and it lives there.
   */
  get canUpload() { return this.user?.can_upload === true; }

  /**
   * Resolve and apply the app theme to <html>.
   * data-theme-source="auto" tells tokens.css that prefers-contrast may take over.
   */
  applyTheme() {
    const root = document.documentElement;
    const explicit = APP_THEMES.includes(this.ui.theme) ? this.ui.theme : null;
    if (explicit) {
      root.setAttribute('data-theme', explicit);
      root.setAttribute('data-theme-source', 'user');
    } else {
      root.removeAttribute('data-theme');
      root.setAttribute('data-theme-source', 'auto');
    }
    const dark = explicit
      ? explicit === 'dark' || explicit === 'hc-dark'
      : window.matchMedia?.('(prefers-color-scheme: dark)').matches;
    const meta = document.querySelector('meta[name="theme-color"]');
    if (meta) meta.setAttribute('content', dark ? '#141210' : '#faf8f4');
  }

  /**
   * Scale the app chrome as a PERCENTAGE of the browser's default font size,
   * never a px value, so OS/browser text scaling still applies on top.
   */
  applyTextScale() {
    const v = Math.max(1, Math.min(1.6, this.ui.text_scale || 1));
    document.documentElement.style.fontSize = v === 1 ? '' : `${Math.round(v * 100)}%`;
  }

  /** @param {string} name @param {any} [detail] */
  #emit(name, detail) {
    this.dispatchEvent(new CustomEvent(name, { detail }));
  }
}

export const store = new Store();

/* Apply the cached theme and text scale immediately, before any view renders. */
store.applyTheme();
store.applyTextScale();

/** Stable per-device label sent with progress updates. */
export function deviceName() {
  let d = null;
  try { d = localStorage.getItem('bookshelf.device'); } catch { /* ignore */ }
  if (!d) {
    d = 'web-' + Math.random().toString(36).slice(2, 8);
    try { localStorage.setItem('bookshelf.device', d); } catch { /* ignore */ }
  }
  return d;
}
