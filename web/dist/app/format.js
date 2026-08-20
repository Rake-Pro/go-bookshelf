/** Small formatting helpers shared by views and components. */

/**
 * "1h 24m" / "24m 05s" style duration for metadata.
 * @param {number|null|undefined} ms
 */
export function duration(ms) {
  if (!ms || ms < 0) return '';
  const total = Math.round(ms / 1000);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  if (h) return `${h}h ${String(m).padStart(2, '0')}m`;
  if (m) return `${m}m ${String(s).padStart(2, '0')}s`;
  return `${s}s`;
}

/**
 * Clock format for the player scrubber: "1:02:03" or "4:07".
 * @param {number} ms
 */
export function clock(ms) {
  const total = Math.max(0, Math.floor((ms || 0) / 1000));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  const mm = h ? String(m).padStart(2, '0') : String(m);
  return (h ? `${h}:` : '') + `${mm}:${String(s).padStart(2, '0')}`;
}

/**
 * Spoken duration for screen readers: "1 hour 24 minutes".
 * @param {number} ms
 */
export function spokenDuration(ms) {
  const total = Math.max(0, Math.floor((ms || 0) / 1000));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  const parts = [];
  if (h) parts.push(`${h} hour${h === 1 ? '' : 's'}`);
  if (m) parts.push(`${m} minute${m === 1 ? '' : 's'}`);
  if (!h && !m) parts.push(`${s} second${s === 1 ? '' : 's'}`);
  return parts.join(' ');
}

/** @param {number} fraction 0..1 */
export const percent = (fraction) =>
  `${Math.max(0, Math.min(100, Math.round((fraction || 0) * 100)))}%`;

/**
 * Join people names for display.
 * @param {{name:string}[]|undefined} people
 */
export const names = (people) => (people || []).map((p) => p.name).join(', ');

/** Role -> the flat name array a list item carries instead of `people`. */
const NAME_LIST = { author: 'authors', narrator: 'narrators' };

/**
 * Pick people of a role out of an item.
 *
 * `GET /items/{id}` returns a full `people` array with ids; the list shapes
 * from `GET /items` and `GET /home` carry only `authors` / `narrators` name
 * arrays, which become id-less entries so cards and the player render the same
 * way from either payload.
 *
 * @param {any} item
 * @param {'author'|'narrator'|'translator'} role
 * @returns {{id?:number|string, name:string}[]}
 */
export function peopleOf(item, role) {
  if (Array.isArray(item?.people) && item.people.length) {
    return item.people.filter((p) => p.role === role);
  }
  const flat = item?.[NAME_LIST[role]];
  return Array.isArray(flat) ? flat.map((name) => ({ name })) : [];
}

/**
 * An item's series entries. The API carries at most one, as an object; this
 * always hands back an array so callers do not branch.
 *
 * @param {any} item
 * @returns {{id?:number|string, name:string, sequence?:number}[]}
 */
export function seriesOf(item) {
  const s = item?.series;
  if (!s) return [];
  return Array.isArray(s) ? s : [s];
}

/** @param {string|null|undefined} iso */
export function date(iso) {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return String(iso);
  return d.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' });
}

/** @param {number|null|undefined} bytes */
export function bytes(n) {
  if (!n && n !== 0) return '';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  return `${v < 10 && i > 0 ? v.toFixed(1) : Math.round(v)} ${units[i]}`;
}
