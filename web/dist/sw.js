/**
 * Bookshelf service worker.
 *
 * Strategy:
 *   /app/*, /vendor/*, /icons/*, /assets/*  cache-first  (immutable per VERSION)
 *   cover images                            cache-first, capped LRU-ish cache
 *   /api/*                                  network-first, no cache fallback
 *   navigations                             network-first, app shell fallback
 *   anything else                           network, then cache
 *
 * Bump VERSION on every frontend change: the old caches are dropped on activate.
 */

const VERSION = 'v2';
const SHELL_CACHE = `bookshelf-shell-${VERSION}`;
const COVER_CACHE = `bookshelf-covers-${VERSION}`;
const MAX_COVERS = 400;

/** Files that must be present for the app to boot offline. */
const SHELL = [
  '/',
  '/app/tokens.css',
  '/app/app.css',
  '/app/main.js',
  '/app/router.js',
  '/app/api.js',
  '/app/store.js',
  '/app/live.js',
  '/app/format.js',
  '/app/player.js',
  '/app/components/app-shell.js',
  '/app/components/icons.js',
  '/app/components/states.js',
  '/app/components/page.js',
  '/app/components/item-card.js',
  '/app/components/mini-player.js',
  '/app/components/sheet.js',
  '/app/views/home.js',
  '/manifest.webmanifest',
  '/icons/icon.svg',
  '/icons/icon-192.png',
  '/icons/icon-512.png',
];

self.addEventListener('install', (event) => {
  event.waitUntil((async () => {
    const cache = await caches.open(SHELL_CACHE);
    // Individually, so one missing file cannot fail the whole install.
    await Promise.all(SHELL.map((url) =>
      cache.add(new Request(url, { cache: 'reload' })).catch(() => {})));
    await self.skipWaiting();
  })());
});

self.addEventListener('activate', (event) => {
  event.waitUntil((async () => {
    const keys = await caches.keys();
    await Promise.all(keys
      .filter((k) => k.startsWith('bookshelf-') && k !== SHELL_CACHE && k !== COVER_CACHE)
      .map((k) => caches.delete(k)));
    await self.clients.claim();
  })());
});

self.addEventListener('message', (event) => {
  if (event.data === 'skip-waiting') self.skipWaiting();
});

self.addEventListener('fetch', (event) => {
  const req = event.request;
  if (req.method !== 'GET') return;

  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;

  // Never intercept media streams: Range requests must reach the server.
  if (url.pathname.includes('/stream') || url.pathname.endsWith('/download')) return;
  // EPUB resources are per-book and large; leave them to the network.
  if (url.pathname.includes('/epub/')) return;

  if (isCover(url)) {
    event.respondWith(cacheFirst(req, COVER_CACHE, MAX_COVERS));
    return;
  }
  if (url.pathname.startsWith('/api/')) {
    event.respondWith(networkOnly(req));
    return;
  }
  if (isStatic(url)) {
    event.respondWith(cacheFirst(req, SHELL_CACHE));
    return;
  }
  if (req.mode === 'navigate') {
    event.respondWith(navigation(req));
    return;
  }
  event.respondWith(cacheFirst(req, SHELL_CACHE));
});

/** @param {URL} url */
const isStatic = (url) =>
  url.pathname.startsWith('/app/')
  || url.pathname.startsWith('/vendor/')
  || url.pathname.startsWith('/assets/')
  || url.pathname.startsWith('/icons/')
  || url.pathname === '/manifest.webmanifest';

/** @param {URL} url */
const isCover = (url) => /^\/api\/v1\/items\/[^/]+\/cover$/.test(url.pathname);

/**
 * @param {Request} req
 * @param {string} cacheName
 * @param {number} [limit]
 */
async function cacheFirst(req, cacheName, limit) {
  const cache = await caches.open(cacheName);
  const hit = await cache.match(req);
  if (hit) return hit;
  try {
    const res = await fetch(req);
    if (res.ok && res.type === 'basic') {
      cache.put(req, res.clone());
      if (limit) trim(cache, limit);
    }
    return res;
  } catch (e) {
    const shell = await caches.match('/');
    if (req.mode === 'navigate' && shell) return shell;
    return offlineResponse();
  }
}

/** @param {Request} req */
async function networkOnly(req) {
  try {
    return await fetch(req);
  } catch (e) {
    return new Response(
      JSON.stringify({ error: { code: 'offline', message: 'You are offline.' } }),
      { status: 503, headers: { 'Content-Type': 'application/json' } },
    );
  }
}

/** @param {Request} req */
async function navigation(req) {
  try {
    return await fetch(req);
  } catch (e) {
    const cache = await caches.open(SHELL_CACHE);
    return (await cache.match('/')) || offlineResponse();
  }
}

function offlineResponse() {
  return new Response(OFFLINE_HTML, {
    status: 503,
    headers: { 'Content-Type': 'text/html; charset=utf-8' },
  });
}

/** Drop the oldest entries once a cache grows past `limit`. */
async function trim(cache, limit) {
  const keys = await cache.keys();
  if (keys.length <= limit) return;
  for (const k of keys.slice(0, keys.length - limit)) await cache.delete(k);
}

const OFFLINE_HTML = `<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Offline - Bookshelf</title>
<style>
  :root { color-scheme: light dark; }
  body {
    margin: 0; min-height: 100vh;
    display: grid; place-items: center; text-align: center;
    font-family: system-ui, -apple-system, "Segoe UI", Roboto, Arial, sans-serif;
    line-height: 1.5; color: #1f1d1a; background: #faf8f4; padding: 1.5rem;
  }
  @media (prefers-color-scheme: dark) { body { color: #ece7df; background: #141210; } }
  a { color: #c2561f; font-weight: 600; }
  h1 { font-size: 1.4rem; }
</style>
<main>
  <h1>You are offline</h1>
  <p>Bookshelf cannot reach the server right now.</p>
  <p><a href="/">Try again</a></p>
</main>`;
