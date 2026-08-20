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
    window.addEventListener('popstate', () => this.#resolve());
    document.addEventListener('click', (e) => this.#onClick(e));
    this.#resolve();
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
    if (opts.replace) history.replaceState(null, '', url);
    else history.pushState(null, '', url);
    this.#resolve();
  }

  /** Intercept same-origin left clicks on plain links. */
  #onClick(e) {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    const a = /** @type {Element} */ (e.target)?.closest?.('a[href]');
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
    const hit = this.match(path);
    /** @type {RouteCtx} */
    const ctx = {
      params: hit?.params ?? {},
      query: new URLSearchParams(location.search),
      path,
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
    if (nav !== this.#nav) return;
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
