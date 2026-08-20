/** 404 for unknown client-side routes. */

import { page } from '../components/page.js';
import { emptyView } from '../components/states.js';

/** @param {import('../router.js').RouteCtx} ctx */
export default async function notFound(ctx) {
  const { el, body } = page('Page not found');
  body.replaceChildren(emptyView(
    'That page does not exist',
    ctx.path,
    { label: 'Go home', href: '/' },
  ));
  return { el, title: 'Not found' };
}
