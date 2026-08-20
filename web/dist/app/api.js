/**
 * Thin wrapper over the JSON API at /api/v1.
 *
 * Every response envelope follows docs/DESIGN.md:
 *   errors    {"error":{"code":"...","message":"..."}}
 *   lists     {"items":[...],"total":n}
 *
 * A 401 on any call other than the auth probe redirects to /login and rejects.
 */

export const BASE = '/api/v1';

/** @typedef {{code:string,message:string,status:number}} ApiErrorShape */

export class ApiError extends Error {
  /**
   * @param {number} status
   * @param {string} code
   * @param {string} message
   * @param {any} [body]
   */
  constructor(status, code, message, body) {
    super(message || code || `HTTP ${status}`);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.body = body;
  }
}

/** Set by main.js so api.js does not import the router (avoids a cycle). */
let onUnauthorized = () => {};
/** @param {(path:string)=>void} fn */
export function setUnauthorizedHandler(fn) { onUnauthorized = fn; }

/**
 * Called when the server answers 403 setup_required: the first-run wizard has
 * not been finished, so every ordinary route is closed and the only useful
 * place to be is /setup.
 */
let onSetupRequired = () => {};
/** @param {()=>void} fn */
export function setSetupRequiredHandler(fn) { onSetupRequired = fn; }

/**
 * @param {string} path path under /api/v1, e.g. "/items?limit=20"
 * @param {RequestInit & {skipAuthRedirect?:boolean}} [opts]
 * @returns {Promise<any>}
 */
export async function request(path, opts = {}) {
  const { skipAuthRedirect, headers, ...rest } = opts;
  /** @type {Response} */
  let res;
  try {
    res = await fetch(BASE + path, {
      credentials: 'same-origin',
      headers: { Accept: 'application/json', ...(headers || {}) },
      ...rest,
    });
  } catch (e) {
    throw new ApiError(0, 'network', 'Cannot reach the server. Check your connection.');
  }

  if (res.status === 204) return null;

  const type = res.headers.get('content-type') || '';
  const body = type.includes('json') ? await res.json().catch(() => null) : await res.text();

  if (!res.ok) {
    const err = body && typeof body === 'object' ? body.error : null;
    if (res.status === 401 && !skipAuthRedirect) onUnauthorized(location.pathname + location.search);
    if (res.status === 403 && err?.code === 'setup_required') onSetupRequired();
    throw new ApiError(res.status, err?.code || String(res.status), err?.message || '', body);
  }
  return body;
}

/**
 * @param {string} path
 * @param {any} data
 * @param {string} [method]
 */
function send(path, data, method = 'POST') {
  return request(path, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(data),
  });
}

/**
 * @param {Record<string, string|number|null|undefined>} params
 * @returns {string} "" or "?a=b&c=d"
 */
export function qs(params) {
  const u = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v === null || v === undefined || v === '') continue;
    u.set(k, String(v));
  }
  const s = u.toString();
  return s ? `?${s}` : '';
}

/* ---------------- auth ---------------- */

/**
 * Auth probe, in two steps:
 *
 *   GET /api/v1/auth/me      200 -> the user; 401 -> nobody is signed in
 *   GET /api/v1/auth/status  200 -> {setup_required, setup_complete,
 *                                    oidc_enabled, oidc_start_url, local_login}
 *
 * `/auth/me` stays a pure "who am I": its 401 body carries only the standard
 * error envelope. Everything the signed-out login page needs comes from the
 * public `/auth/status` endpoint, which is queried only when there is no
 * session. The flags fall back to "password login, setup done" if the call
 * fails, so a probe error degrades to a usable form instead of a blank page.
 *
 * @returns {Promise<{user:any|null, oidc:boolean, setupRequired:boolean, localLogin:boolean}>}
 */
export async function probeAuth() {
  try {
    const user = await request('/auth/me', { skipAuthRedirect: true });
    return { user, oidc: false, setupRequired: false, localLogin: true };
  } catch (e) {
    if (!(e instanceof ApiError) || e.status !== 401) throw e;
  }
  const status = await authStatus();
  return {
    user: null,
    oidc: status.oidc_enabled === true,
    setupRequired: status.setup_required === true,
    // Absent means an older backend that has no such switch, so the form stays.
    localLogin: status.local_login !== false,
  };
}

/**
 * Public login capabilities. Never rejects: an unreachable server yields an
 * empty object and the caller falls back to password login.
 *
 * @returns {Promise<{setup_required?:boolean, setup_complete?:boolean,
 *                    oidc_enabled?:boolean, oidc_start_url?:string,
 *                    local_login?:boolean, version?:string}>}
 */
export async function authStatus() {
  try {
    return (await request('/auth/status', { skipAuthRedirect: true })) || {};
  } catch {
    return {};
  }
}

export const auth = {
  /** @param {string} username @param {string} password */
  login: (username, password) =>
    send('/auth/login', { username, password }),
  logout: () => request('/auth/logout', { method: 'POST', skipAuthRedirect: true }),
  oidcStartUrl: () => `${BASE}/auth/oidc/start`,
};

/* ---------------- first-run wizard ---------------- */

/**
 * One call per step of `POST /api/v1/setup/{step}`. The first two run before
 * anybody is signed in, so they opt out of the 401 redirect; the rest run as
 * the administrator the second step created.
 */
export const setup = {
  /** @param {string} token @returns {Promise<{ok:boolean, suggested_base_url:string}>} */
  checkToken: (token) =>
    request('/setup/token', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ token }),
      skipAuthRedirect: true,
    }),
  /** @param {{token:string,username:string,password:string,display_name:string}} data */
  createAdmin: (data) =>
    request('/setup/admin', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(data),
      skipAuthRedirect: true,
    }),
  /** @param {string} baseUrl @returns {Promise<{base_url:string, redirect_url:string}>} */
  baseUrl: (baseUrl) => send('/setup/base-url', { base_url: baseUrl }),
  /** @param {object} data an OIDC document, or `{skip:true}` */
  oidc: (data) => send('/setup/oidc', data),
  /** @param {object} data `{name, kind, path}`, or `{skip:true}` */
  library: (data) => send('/setup/library', data),
  complete: () => send('/setup/complete', {}),
};

/* ---------------- catalog ---------------- */

export const api = {
  home: () => request('/home'),

  /** @param {Record<string, any>} [params] library, kind, author, series, tag, q, sort, limit, offset */
  items: (params = {}) => request('/items' + qs(params)),
  /** @param {string} id */
  item: (id) => request(`/items/${encodeURIComponent(id)}`),
  /** @param {string} id @param {any} patch */
  patchItem: (id, patch) => send(`/items/${encodeURIComponent(id)}`, patch, 'PATCH'),

  authors: (params = {}) => request('/authors' + qs(params)),
  author: (id) => request(`/authors/${encodeURIComponent(id)}`),
  seriesList: (params = {}) => request('/series' + qs(params)),
  series: (id) => request(`/series/${encodeURIComponent(id)}`),
  tags: () => request('/tags'),
  /** @param {string} q */
  search: (q) => request('/search' + qs({ q })),

  libraries: () => request('/libraries'),
  /** @param {{name:string,kind:string,paths:string[]}} data */
  createLibrary: (data) => send('/libraries', data),
  updateLibrary: (id, patch) => send(`/libraries/${encodeURIComponent(id)}`, patch, 'PATCH'),
  deleteLibrary: (id) => request(`/libraries/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  scanLibrary: (id) => request(`/libraries/${encodeURIComponent(id)}/scan`, { method: 'POST' }),
  scans: (id) => request(`/libraries/${encodeURIComponent(id)}/scans`),

  users: () => request('/users'),
  createUser: (data) => send('/users', data),
  updateUser: (id, patch) => send(`/users/${encodeURIComponent(id)}`, patch, 'PATCH'),
  deleteUser: (id) => request(`/users/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  setUserLibraries: (id, libraryIds) =>
    send(`/users/${encodeURIComponent(id)}/libraries`, { libraries: libraryIds }, 'PUT'),

  systemStatus: () => request('/system/status'),

  /** Admin-only application settings; the OIDC client secret is never returned. */
  adminSettings: () => request('/admin/settings'),
  /** @param {object} patch every section is optional */
  putAdminSettings: (patch) => send('/admin/settings', patch, 'PUT'),
  /** @param {object} data a candidate OIDC document @returns {Promise<{ok:boolean, error?:string}>} */
  testOidc: (data) => send('/admin/settings/oidc/test', data),

  /* -------- adding books -------- */

  /**
   * Queue a URL import. Answers 202 with the job; the job list is polled from
   * there, because fetching somebody else's site outlives the request.
   * @param {string} libraryId @param {string} url
   * @returns {Promise<{id:number,status:string,url:string,message:string,item_id:number}>}
   */
  importUrl: (libraryId, url) => send(`/libraries/${encodeURIComponent(libraryId)}/import`, { url }),
  /** The caller's own import jobs, newest first. An admin sees everybody's. */
  imports: () => request('/me/imports'),
  /** @param {string|number} id */
  importJob: (id) => request(`/imports/${encodeURIComponent(id)}`),
  /** Cancel a queued or running import, or clear a finished one. */
  cancelImport: (id) => request(`/imports/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  settings: () => request('/me/settings'),
  /** @param {{reader?:object,player?:object,ui?:object}} s */
  putSettings: (s) => send('/me/settings', s, 'PUT'),

  /** @param {string} [since] RFC3339 */
  progressSince: (since) => request('/me/progress' + qs({ since })),
  /**
   * @param {string} itemId
   * @param {{locator?:string,position_ms?:number,percent:number,finished?:boolean,device:string}} p
   */
  putProgress: (itemId, p) => send(`/me/progress/${encodeURIComponent(itemId)}`, p, 'PUT'),

  /** @param {string} itemId */
  bookmarks: (itemId) => request('/me/bookmarks' + qs({ item: itemId })),
  /** @param {{item_id:string,locator?:string,position_ms?:number,note?:string}} b */
  addBookmark: (b) => send('/me/bookmarks', b),
  deleteBookmark: (id) => request(`/me/bookmarks/${encodeURIComponent(id)}`, { method: 'DELETE' }),
};

/* ---------------- media URLs ---------------- */

/**
 * @param {string} itemId
 * @param {'thumb'|'full'} [size]
 */
export const coverUrl = (itemId, size = 'thumb') =>
  `${BASE}/items/${encodeURIComponent(itemId)}/cover?size=${size}`;

/** @param {string} itemId @param {string} fileId */
export const streamUrl = (itemId, fileId) =>
  `${BASE}/items/${encodeURIComponent(itemId)}/files/${encodeURIComponent(fileId)}/stream`;

/** @param {string} itemId */
export const downloadUrl = (itemId) =>
  `${BASE}/items/${encodeURIComponent(itemId)}/download`;

/** Root of the extracted EPUB container for an item (no trailing slash). */
export const epubRoot = (itemId) =>
  `${BASE}/items/${encodeURIComponent(itemId)}/epub`;

/* ---------------- uploads ---------------- */

/**
 * Upload books to a library.
 *
 * This is the one call that does not go through `request`: fetch cannot report
 * how much of a request body has been sent, and an upload with no progress bar
 * is indistinguishable from a hung one when the file is an audiobook. XHR can,
 * so XHR is what this uses - and it returns the abort handle with the promise,
 * so closing the sheet stops the transfer instead of orphaning it.
 *
 * The whole selection goes in one request: the server groups an audiobook's
 * files into one book by their tags, and it can only do that if it sees them
 * together.
 *
 * @param {string} libraryId
 * @param {FormData} form `files` parts, and an optional `subdir` field first
 * @param {(loaded:number, total:number) => void} [onProgress]
 * @returns {{promise:Promise<any>, cancel:() => void}}
 */
export function uploadBooks(libraryId, form, onProgress) {
  const xhr = new XMLHttpRequest();
  const promise = new Promise((resolve, reject) => {
    xhr.open('POST', `${BASE}/libraries/${encodeURIComponent(libraryId)}/upload`);
    xhr.setRequestHeader('Accept', 'application/json');
    xhr.upload.addEventListener('progress', (e) => {
      if (e.lengthComputable) onProgress?.(e.loaded, e.total);
    });
    xhr.addEventListener('load', () => {
      let body = null;
      try { body = JSON.parse(xhr.responseText); } catch { body = null; }
      if (xhr.status >= 200 && xhr.status < 300) { resolve(body); return; }
      const err = body && typeof body === 'object' ? body.error : null;
      if (xhr.status === 401) onUnauthorized(location.pathname + location.search);
      reject(new ApiError(xhr.status, err?.code || String(xhr.status), err?.message || '', body));
    });
    xhr.addEventListener('error', () =>
      reject(new ApiError(0, 'network', 'Cannot reach the server. Check your connection.')));
    xhr.addEventListener('abort', () =>
      reject(new ApiError(0, 'aborted', 'The upload was cancelled.')));
    xhr.send(form);
  });
  return { promise, cancel: () => xhr.abort() };
}
