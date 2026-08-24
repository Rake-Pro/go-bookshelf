/**
 * Application entry point: boots auth, settings and the router, mounts the
 * shell, and registers the service worker.
 */

import { router, navigate } from './router.js';
import { setUnauthorizedHandler, setSetupRequiredHandler, probeAuth } from './api.js';
import { store } from './store.js';
import { createShell } from './components/app-shell.js';
import { loadingView, errorView } from './components/states.js';
import { showUpdateToast } from './components/update-toast.js';

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
  .add('/admin/settings', () => import('./views/admin-settings.js'))
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

/* An unfinished wizard closes every other route, so send the operator to it. */
setSetupRequiredHandler(() => {
  if (location.pathname !== '/setup') navigate('/setup', { replace: true });
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
    navigator.serviceWorker.register('/sw.js').then((reg) => {
      // A worker already waiting when this tab loaded - it finished
      // installing while the tab was backgrounded - gets the same banner as
      // one that finishes during this session.
      if (reg.waiting && navigator.serviceWorker.controller) notifyUpdate(reg);
      reg.addEventListener('updatefound', () => {
        const installing = reg.installing;
        if (!installing) return;
        installing.addEventListener('statechange', () => {
          // A controller already existing is what tells "installed" apart
          // from "this is the very first install": that one has nothing to
          // hand off to and needs no banner.
          if (installing.state === 'installed' && navigator.serviceWorker.controller) {
            notifyUpdate(reg);
          }
        });
      });
    }).catch(() => {
      // A failed registration only costs offline support.
    });

    // The new worker takes over exactly once control is handed to it; reload
    // then, and only then, so a second event (there should never be one)
    // cannot loop the page.
    let reloaded = false;
    navigator.serviceWorker.addEventListener('controllerchange', () => {
      if (reloaded) return;
      reloaded = true;
      location.reload();
    });
  });
}

/**
 * Tell the person a new version is ready and, if they act on it, hand
 * control to the worker that is waiting for it. The controllerchange
 * listener above reloads the page once that worker actually takes over.
 * @param {ServiceWorkerRegistration} reg
 */
function notifyUpdate(reg) {
  showUpdateToast(() => {
    reg.waiting?.postMessage({ type: 'SKIP_WAITING' });
  });
}
