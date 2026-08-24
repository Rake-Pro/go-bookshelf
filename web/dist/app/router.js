/**
 * History-API router. No hashes, no server round trips: the Go server serves
 * index.html for every non-API path.
 *
 * A route maps a path pattern to a lazily imported view module. A view module's
 * default export is `async (ctx) => View` where View is:
 *   { el: Element, title?: string, chrome?: boolean, destroy?: () => void }
 */

/**
 * @typedef {Object} RouteCtx
 * @property {Record<string,string>} params
 * @property {URLSearchParams} query
 * @property {string} path
 * @property {number|null} restore scroll offset to put back for this history
 *   entry (a Back or Forward), or null for a fresh navigation, which starts at
 *   the top. The renderer owns the actual scrolling.
 */

/**
 * @typedef {Object} View
 * @property {Element} el
 * @property {string} [title]
 * @property {() => void} [destroy]
 */

/** @typedef {{pattern:string, keys:string[], re:RegExp, load:() => Promise<any>, chrome:boolean}} Route */

/**
 * Turn "/item/:id" into a regex plus the ordered param names.
 * A trailing ":rest*" segment captures the remainder including slashes.
 * @param {string} pattern
 */
function compile(pattern) {
  /** @type {string[]} */
  const keys = [];
  const source = pattern
    .split('/')
    .filter((s) => s !== '')
    .map((seg) => {
      if (seg.startsWith(':')) {
        const star = seg.endsWith('*');
        keys.push(star ? seg.slice(1, -1) : seg.slice(1));
        return star ? '/(.*)' : '/([^/]+)';
      }
      return '/' + seg.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    })
    .join('');
  return { keys, re: new RegExp('^' + (source || '/') + '/?$') };
}

/** How many history entries keep a remembered scroll offset. */
const SCROLL_MEMORY = 50;

let keySeq = 0;
/** A key that is unique per entry and survives a reload of the same entry. */
const newKey = () => `${Date.now().toString(36)}-${(keySeq++).toString(36)}`;

export class Router extends EventTarget {
  /** @type {Route[]} */
  #routes = [];
  /** @type {View|null} */
  #current = null;
  /** @type {Route|null} */
  #currentRoute = null;
  /** @type {((route:Route|null, view:View|null) => void)|null} */
  #render = null;
  /** Bumped on every navigation so a slow view cannot overwrite a newer one. */
  #nav = 0;
  /** path+search of the last resolve, so fragment-only entries are left alone. */
  #lastLoc = '';
  /**
   * Scroll offset per history entry, oldest first. The browser cannot do this
   * for us: `scrollRestoration` is turned off below because it fires against
   * the outgoing document, before the new view has been built, and would land
   * on whatever height the old page happened to have.
   * @type {Map<string, number>}
   */
  #scroll = new Map();
  /** Key of the entry currently on screen; `history.state.bsKey` mirrors it. */
  #key = '';
  /**
   * Offset to restore on the resolve now in flight, or null to go to the top.
   * @type {number|null}
   */
  #restore = null;

  /**
   * @param {string} pattern
   * @param {() => Promise<any>} load
   * @param {{chrome?:boolean}} [opts] chrome:false renders the view full-screen
   */
  add(pattern, load, opts = {}) {
    const { keys, re } = compile(pattern);
    this.#routes.push({ pattern, keys, re, load, chrome: opts.chrome !== false });
    return this;
  }

  /** @param {string} path */
  match(path) {
    for (const r of this.#routes) {
      const m = r.re.exec(path);
      if (!m) continue;
      /** @type {Record<string,string>} */
      const params = {};
      r.keys.forEach((k, i) => { params[k] = decodeURIComponent(m[i + 1] ?? ''); });
      return { route: r, params };
    }
    return null;
  }

  /** @param {(route:Route|null, view:View|null, ctx:RouteCtx) => void} render */
  start(render) {
    this.#render = render;
    if ('scrollRestoration' in history) history.scrollRestoration = 'manual';
    this.#key = this.#adoptKey();
    window.addEventListener('popstate', () => {
      // A fragment-only entry (the skip link's #main) changes neither path
      // nor search: rebuilding the view and forcing scroll-to-top would
      // punish exactly the keyboard users that link serves. Stamp a key and
      // leave the document alone.
      if (location.pathname + location.search === this.#lastLoc) {
        this.#key = this.#adoptKey();
        return;
      }
      // popstate arrives with the new entry already current but the old
      // document still on screen and still at its own scroll offset, so this
      // is the last moment the departing entry's position can be read.
      this.#saveScroll();
      this.#key = this.#adoptKey();
      this.#restore = this.#scroll.get(this.#key) ?? null;
      this.#resolve();
    });
    document.addEventListener('click', (e) => this.#onClick(e));
    this.#resolve();
  }

  /**
   * The key of the current history entry, stamping one on if it has none (a
   * cold load, or an entry pushed before the router started).
   * @returns {string}
   */
  #adoptKey() {
    const state = history.state;
    if (state && typeof state.bsKey === 'string') return state.bsKey;
    const key = newKey();
    history.replaceState({ ...(state || {}), bsKey: key }, '');
    return key;
  }

  /** Remember where the entry on screen is scrolled to, bounded to the last N. */
  #saveScroll() {
    if (!this.#key) return;
    this.#scroll.delete(this.#key);
    this.#scroll.set(this.#key, window.scrollY);
    while (this.#scroll.size > SCROLL_MEMORY) {
      const oldest = this.#scroll.keys().next().value;
      if (oldest === undefined) break;
      this.#scroll.delete(oldest);
    }
  }

  /**
   * @param {string} to
   * @param {{replace?:boolean}} [opts]
   */
  navigate(to, opts = {}) {
    const url = new URL(to, location.origin);
    if (url.origin !== location.origin) { location.href = to; return; }
    const same = url.pathname + url.search === location.pathname + location.search;
    if (same) return;
    this.#saveScroll();
    const key = newKey();
    if (opts.replace) {
      // The entry being replaced is gone for good, and so is its position.
      this.#scroll.delete(this.#key);
      history.replaceState({ bsKey: key }, '', url);
    } else {
      history.pushState({ bsKey: key }, '', url);
    }
    this.#key = key;
    // A fresh navigation starts at the top; only Back and Forward restore.
    this.#restore = null;
    this.#resolve();
  }

  /** Intercept same-origin left clicks on plain links. */
  #onClick(e) {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    // Events from a shadow root retarget e.target to the host; composedPath()[0]
    // is the element actually clicked, so links inside bs-item-card etc. are
    // still caught instead of falling through to a full page load.
    const origin = /** @type {Element} */ (e.composedPath?.()[0] ?? e.target);
    const a = origin?.closest?.('a[href]');
    if (!a) return;
    const href = a.getAttribute('href') || '';
    if (a.hasAttribute('download') || a.getAttribute('target') === '_blank') return;
    if (href.startsWith('#')) return;
    // any absolute scheme (http:, mailto:, blob:) is the browser's job
    if (/^[a-z][a-z0-9+.-]*:/i.test(href)) return;
    const url = new URL(href, location.href);
    if (url.origin !== location.origin) return;
    if (url.pathname.startsWith('/api/')) return;
    e.preventDefault();
    this.navigate(url.pathname + url.search + url.hash);
  }

  async #resolve() {
    const nav = ++this.#nav;
    const path = location.pathname;
    this.#lastLoc = path + location.search;
    const hit = this.match(path);
    // Consumed here, so a view that resolves late cannot restore a position
    // belonging to a navigation that has since been superseded: a newer
    // #resolve has taken the value and this one carries its own copy, which
    // the generation check below discards along with the rest of the render.
    const restore = this.#restore;
    this.#restore = null;
    /** @type {RouteCtx} */
    const ctx = {
      params: hit?.params ?? {},
      query: new URLSearchParams(location.search),
      path,
      restore,
    };
    this.dispatchEvent(new CustomEvent('navigate', { detail: ctx }));

    if (!hit) {
      const mod = await import('./views/not-found.js');
      if (nav !== this.#nav) return;
      this.#swap(null, await mod.default(ctx), ctx);
      return;
    }

    let view;
    try {
      const mod = await hit.route.load();
      if (nav !== this.#nav) return;
      view = await mod.default(ctx);
    } catch (e) {
      if (nav !== this.#nav) return;
      const { errorView } = await import('./components/states.js');
      view = { el: errorView(e, () => this.#resolve()), title: 'Error' };
    }
    if (nav !== this.#nav) { try { view?.destroy?.(); } catch (e) { console.error(e); } return; }
    this.#swap(hit.route, view, ctx);
  }

  /**
   * @param {Route|null} route
   * @param {View|null} view
   * @param {RouteCtx} ctx
   */
  #swap(route, view, ctx) {
    try { this.#current?.destroy?.(); } catch (e) { console.error(e); }
    this.#current = view;
    this.#currentRoute = route;
    document.title = view?.title ? `${view.title} - Bookshelf` : 'Bookshelf';
    this.#render?.(route, view, ctx);
  }

  /** Re-run the current route (after login, after a mutation, on retry). */
  refresh() { return this.#resolve(); }

  get currentRoute() { return this.#currentRoute; }
}

export const router = new Router();

/** @param {string} to @param {{replace?:boolean}} [opts] */
export const navigate = (to, opts) => router.navigate(to, opts);
