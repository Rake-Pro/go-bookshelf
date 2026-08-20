/** Page scaffolding shared by the shell routes. */

/**
 * @param {string} title rendered as the view's <h1>
 * @param {{subtitle?:string, actions?:Node[]}} [opts]
 * @returns {{el:HTMLElement, body:HTMLElement, head:HTMLElement}}
 */
export function page(title, opts = {}) {
  const el = document.createElement('div');
  const head = document.createElement('div');
  head.className = 'row';
  head.style.marginBottom = 'var(--s4)';

  const titles = document.createElement('div');
  const h1 = document.createElement('h1');
  h1.textContent = title;
  h1.tabIndex = -1;
  titles.append(h1);
  if (opts.subtitle) {
    const p = document.createElement('p');
    p.className = 'muted';
    p.style.margin = '0';
    p.textContent = opts.subtitle;
    titles.append(p);
  }
  head.append(titles);
  if (opts.actions?.length) {
    const sp = document.createElement('span');
    sp.className = 'spacer';
    head.append(sp, ...opts.actions);
  }

  const body = document.createElement('div');
  el.append(head, body);
  return { el, body, head };
}

/**
 * A labelled section with a heading that list elements can point at.
 * @param {string} title
 * @param {{href?:string, linkLabel?:string}} [opts]
 */
export function sectionHead(title, opts = {}) {
  const wrap = document.createElement('div');
  wrap.className = 'section-head';
  const h2 = document.createElement('h2');
  h2.id = 'sec-' + title.toLowerCase().replace(/[^a-z0-9]+/g, '-');
  h2.textContent = title;
  wrap.append(h2);
  if (opts.href) {
    const sp = document.createElement('span');
    sp.className = 'spacer';
    const a = document.createElement('a');
    a.href = opts.href;
    a.textContent = opts.linkLabel || 'See all';
    wrap.append(sp, a);
  }
  return { el: wrap, id: h2.id };
}
