/**
 * The persistent chrome: desktop sidebar, mobile app bar + tab bar, and the
 * mini-player dock. Built once at start-up; routes only swap the <main>
 * contents, which is what keeps the single <audio> element alive.
 *
 * Uses light DOM on purpose so app.css owns the layout.
 */

import { icon } from './icons.js';
import { store } from '../store.js';
import './mini-player.js';

/**
 * @typedef {{href:string, label:string, icon:string, tab?:boolean, admin?:boolean}} NavItem
 */

/** @type {NavItem[]} */
const NAV = [
  { href: '/', label: 'Home', icon: 'home', tab: true },
  { href: '/library', label: 'Library', icon: 'library', tab: true },
  { href: '/authors', label: 'Authors', icon: 'authors' },
  { href: '/series', label: 'Series', icon: 'series' },
  { href: '/search', label: 'Search', icon: 'search', tab: true },
  { href: '/settings', label: 'Settings', icon: 'settings', tab: true },
  { href: '/admin', label: 'Admin', icon: 'admin', admin: true },
];

export function createShell() {
  const shell = document.createElement('div');
  shell.className = 'shell';

  /* --- mobile app bar --- */
  const appbar = document.createElement('header');
  appbar.className = 'appbar';
  const barTitle = document.createElement('h1');
  barTitle.textContent = 'Bookshelf';
  const spacer = document.createElement('span');
  spacer.className = 'spacer';
  const searchLink = document.createElement('a');
  searchLink.className = 'iconbtn';
  searchLink.href = '/search';
  searchLink.setAttribute('aria-label', 'Search');
  searchLink.title = 'Search';
  searchLink.append(icon('search'));
  appbar.append(barTitle, spacer, searchLink);

  /* --- desktop sidebar --- */
  const sidebar = document.createElement('div');
  sidebar.className = 'sidebar';
  const brand = document.createElement('a');
  brand.className = 'brand';
  brand.href = '/';
  brand.append(icon('book'));
  const brandText = document.createElement('span');
  brandText.textContent = 'Bookshelf';
  brand.append(brandText);
  const nav = document.createElement('nav');
  nav.setAttribute('aria-label', 'Main');
  const list = document.createElement('ul');
  nav.append(list);
  sidebar.append(brand, nav);

  /* --- main --- */
  const main = document.createElement('main');
  main.className = 'content';
  main.id = 'main';
  main.tabIndex = -1;

  /* --- dock: mini-player above the tab bar --- */
  const dock = document.createElement('div');
  dock.className = 'dock';
  const mini = document.createElement('bs-mini-player');
  const tabbar = document.createElement('nav');
  tabbar.className = 'tabbar';
  tabbar.setAttribute('aria-label', 'Sections');
  dock.append(mini, tabbar);

  shell.append(appbar, sidebar, main, dock);

  /** @type {HTMLAnchorElement[]} */
  let links = [];

  function buildNav() {
    list.replaceChildren();
    tabbar.replaceChildren();
    links = [];
    for (const n of NAV) {
      if (n.admin && !store.isAdmin) continue;
      const li = document.createElement('li');
      const a = document.createElement('a');
      a.href = n.href;
      a.append(icon(n.icon));
      const label = document.createElement('span');
      label.textContent = n.label;
      a.append(label);
      li.append(a);
      list.append(li);
      links.push(a);

      if (n.tab) {
        const t = document.createElement('a');
        t.href = n.href;
        t.append(icon(n.icon));
        const tl = document.createElement('span');
        tl.textContent = n.label;
        t.append(tl);
        tabbar.append(t);
        links.push(t);
      }
    }
  }

  buildNav();
  store.addEventListener('user', buildNav);

  /**
   * @param {string} path
   */
  function setActive(path) {
    for (const a of links) {
      const href = a.getAttribute('href') || '';
      const active = href === '/' ? path === '/' : path === href || path.startsWith(href + '/');
      if (active) a.setAttribute('aria-current', 'page');
      else a.removeAttribute('aria-current');
    }
  }

  /** @param {string} title */
  function setTitle(title) { barTitle.textContent = title; }

  return { el: shell, main, setActive, setTitle };
}
