/**
 * Application entry point: boots auth, settings and the router, mounts the
 * shell, and registers the service worker.
 */

import { router, navigate } from './router.js';
import { setUnauthorizedHandler, probeAuth } from './api.js';
import { store } from './store.js';
import { createShell } from './components/app-shell.js';
import { loadingView, errorView } from './components/states.js';

/* Routes. `chrome:false` means the view replaces the whole shell. */
router
  .add('/', () => import('./views/home.js'))
  .add('/library', () => import('./views/library.js'))
  .add('/library/:id', () => import('./views/library.js'))
  .add('/item/:id', () => import('./views/item.js'))
  .add('/read/:id', () => import('./views/reader.js'), { chrome: false })
  .add('/listen/:id', () => import('./views/listen.js'))
  .add('/authors', () => import('./views/authors.js'))
  .add('/authors/:id', () => import('./views/authors.js'))
  .add('/series', () => import('./views/series.js'))
  .add('/series/:id', () => import('./views/series.js'))
  .add('/search', () => import('./views/search.js'))
  .add('/settings', () => import('./views/settings.js'))
  .add('/admin', () => import('./views/admin.js'))
  .add('/admin/:section', () => import('./views/admin.js'))
  .add('/login', () => import('./views/login.js'), { chrome: false })
  .add('/setup', () => import('./views/setup.js'), { chrome: false });

const root = /** @type {HTMLElement} */ (document.getElementById('app'));
const shell = createShell();

/** Paths that render without the shell and without requiring a session. */
const PUBLIC = new Set(['/login', '/setup']);

setUnauthorizedHandler((from) => {
  if (PUBLIC.has(location.pathname)) return;
  const next = from && from !== '/' ? `?next=${encodeURIComponent(from)}` : '';
  navigate('/login' + next, { replace: true });
});

/** @type {boolean} */
let shellMounted = false;

/** @type {(route:any, view:any, ctx:any) => void} */
function render(route, view, ctx) {
  const wantsChrome = route ? route.chrome : true;
  if (!view) return;
  if (wantsChrome) {
    if (!shellMounted) {
      root.replaceChildren(shell.el);
      shellMounted = true;
    }
    shell.setActive(ctx.path);
    shell.setTitle(view.title || 'Bookshelf');
    shell.main.replaceChildren(view.el);
    // Move focus to the top of the new view for screen reader and keyboard users.
    shell.main.focus({ preventScroll: true });
    window.scrollTo({ top: 0 });
  } else {
    shellMounted = false;
    root.replaceChildren(view.el);
  }
}

async function boot() {
  root.replaceChildren(loadingView('Starting'));
  try {
    const { user, setupRequired } = await probeAuth();
    store.setUser(user);
    if (!user) {
      if (setupRequired && location.pathname !== '/setup') {
        history.replaceState(null, '', '/setup');
      } else if (!setupRequired && !PUBLIC.has(location.pathname)) {
        const next = location.pathname + location.search;
        const q = next && next !== '/' ? `?next=${encodeURIComponent(next)}` : '';
        history.replaceState(null, '', '/login' + q);
      }
    } else {
      await store.load().catch(() => { /* fall back to cached settings */ });
    }
  } catch (e) {
    root.replaceChildren(errorView(e, () => boot()));
    return;
  }
  router.start(render);
}

/* Flush any debounced settings write before the page goes away. */
window.addEventListener('pagehide', () => { store.flush(); });

boot();

if ('serviceWorker' in navigator) {
  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js').catch(() => {
      // A failed registration only costs offline support.
    });
  });
}
