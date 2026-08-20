/**
 * Inline SVG icons. Icons are decorative: every icon-only control carries its
 * own aria-label, so the svg is always aria-hidden.
 */

/** @type {Record<string,string>} 24x24 path data, stroke-based */
const PATHS = {
  home: 'M3 10.5 12 3l9 7.5M5.5 9.5V20h13V9.5M9.5 20v-6h5v6',
  library: 'M4 4h5v16H4zM11 4h4v16h-4zM17.2 5.2l3.4.9-3.6 14-3.4-.9z',
  authors: 'M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8ZM4.5 20a7.5 7.5 0 0 1 15 0',
  series: 'M5 6h11M5 12h11M5 18h11M20 6v12',
  search: 'M11 4a7 7 0 1 0 0 14 7 7 0 0 0 0-14ZM20 20l-4-4',
  settings: 'M12 15.5a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7Z'
    + 'M19.4 13.5a7.6 7.6 0 0 0 0-3l1.8-1.3-1.9-3.3-2.1.8a7.7 7.7 0 0 0-2.6-1.5L14.2 3H9.8l-.4 2.2a7.7 7.7 0 0 0-2.6 1.5l-2.1-.8-1.9 3.3 1.8 1.3a7.6 7.6 0 0 0 0 3l-1.8 1.3 1.9 3.3 2.1-.8a7.7 7.7 0 0 0 2.6 1.5l.4 2.2h4.4l.4-2.2a7.7 7.7 0 0 0 2.6-1.5l2.1.8 1.9-3.3Z',
  admin: 'M12 3 4 6v5.5c0 4.6 3.2 8.5 8 9.5 4.8-1 8-4.9 8-9.5V6ZM9 12l2 2 4-4',
  book: 'M4 5.5A2.5 2.5 0 0 1 6.5 3H20v15H6.5A2.5 2.5 0 0 0 4 20.5ZM4 20.5A2.5 2.5 0 0 1 6.5 18H20v3H6.5A2.5 2.5 0 0 1 4 20.5Z',
  headphones: 'M4 15v-3a8 8 0 0 1 16 0v3M4 15a2 2 0 0 1 2-2h1v6H6a2 2 0 0 1-2-2ZM20 15a2 2 0 0 0-2-2h-1v6h1a2 2 0 0 0 2-2Z',
  play: 'M8 5.5v13l11-6.5Z',
  pause: 'M9 5h2.5v14H9zM14.5 5H17v14h-2.5z',
  back: 'M15 5 8 12l7 7',
  forward: 'M9 5l7 7-7 7',
  close: 'M6 6l12 12M18 6 6 18',
  toc: 'M4 6h16M4 12h16M4 18h10',
  gear: 'M12 15.5a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7Z',
  skipBack: 'M11 8V5L5 9.5 11 14v-3M9 12a6 6 0 1 0 6-6',
  skipFwd: 'M13 8V5l6 4.5-6 4.5v-3M15 12a6 6 0 1 1-6-6',
  speed: 'M12 20a8 8 0 1 1 8-8M12 12l4.5-4.5M12 20h8',
  timer: 'M12 21a7 7 0 1 0 0-14 7 7 0 0 0 0 14ZM12 10.5V14l2 1.5M9 3h6',
  bookmark: 'M7 4h10v16l-5-3.5L7 20Z',
  list: 'M4 7h16M4 12h16M4 17h16',
  plus: 'M12 5v14M5 12h14',
  refresh: 'M20 12a8 8 0 1 1-2.4-5.7M20 4v4h-4',
  logout: 'M14 7V5H5v14h9v-2M18 12H9M15 9l3 3-3 3',
  warn: 'M12 4 2.5 20h19ZM12 10v4M12 17.2v.1',
  check: 'M4 12.5 9.5 18 20 6.5',
  chevronRight: 'M9 5l7 7-7 7',
  aMinus: 'M3 19 8 5l5 14M4.8 14.5h6.4M16 12h5',
  aPlus: 'M3 19 8 5l5 14M4.8 14.5h6.4M16 12h5M18.5 9.5v5',
};

/**
 * @param {keyof typeof PATHS|string} name
 * @param {{size?:string}} [opts]
 * @returns {SVGElement}
 */
export function icon(name, opts = {}) {
  const d = PATHS[name] || PATHS.book;
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('fill', 'none');
  svg.setAttribute('stroke', 'currentColor');
  svg.setAttribute('stroke-width', '1.8');
  svg.setAttribute('stroke-linecap', 'round');
  svg.setAttribute('stroke-linejoin', 'round');
  svg.setAttribute('aria-hidden', 'true');
  svg.setAttribute('focusable', 'false');
  if (opts.size) { svg.style.width = opts.size; svg.style.height = opts.size; }
  const path = document.createElementNS('http://www.w3.org/2000/svg', 'path');
  path.setAttribute('d', d);
  if (name === 'play' || name === 'pause' || name === 'bookmark') {
    path.setAttribute('fill', 'currentColor');
    path.setAttribute('stroke-width', '1');
  }
  svg.append(path);
  return svg;
}

/** Icon-only button with a required accessible name. */
export function iconButton(name, label, onClick, extraClass = '') {
  const b = document.createElement('button');
  b.type = 'button';
  b.className = 'iconbtn ' + extraClass;
  b.setAttribute('aria-label', label);
  b.title = label;
  b.append(icon(name));
  if (onClick) b.addEventListener('click', onClick);
  return b;
}
