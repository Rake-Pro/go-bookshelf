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
 *   GET /api/v1/auth/status  200 -> {setup_required, oidc_enabled, oidc_start_url}
 *
 * `/auth/me` stays a pure "who am I": its 401 body carries only the standard
 * error envelope. Everything the signed-out login page needs comes from the
 * public `/auth/status` endpoint, which is queried only when there is no
 * session. Both flags default to false if the call fails, so a probe error
 * degrades to password-only login instead of breaking the page.
 *
 * @returns {Promise<{user:any|null, oidc:boolean, setupRequired:boolean}>}
 */
export async function probeAuth() {
  try {
    const user = await request('/auth/me', { skipAuthRedirect: true });
    return { user, oidc: false, setupRequired: false };
  } catch (e) {
    if (!(e instanceof ApiError) || e.status !== 401) throw e;
  }
  const status = await authStatus();
  return {
    user: null,
    oidc: status.oidc_enabled === true,
    setupRequired: status.setup_required === true,
  };
}

/**
 * Public login capabilities. Never rejects: an unreachable server yields an
 * empty object and the caller falls back to password login.
 *
 * @returns {Promise<{setup_required?:boolean, oidc_enabled?:boolean,
 *                    oidc_start_url?:string, version?:string}>}
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
  /** @param {{token:string,username:string,password:string,display_name:string}} data */
  setup: (data) => send('/auth/setup', data),
  oidcStartUrl: () => `${BASE}/auth/oidc/start`,
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
