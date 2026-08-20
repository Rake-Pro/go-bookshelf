/**
 * Reader settings controls, shared between the reader's settings sheet and the
 * Settings page (where they act as defaults).
 *
 * Every control writes through `store.update('reader', ...)`, which debounces a
 * single PUT /me/settings and mirrors to localStorage immediately.
 */

import { store, READER_DEFAULTS } from '../store.js';
import { icon } from './icons.js';
import { announce } from '../live.js';

export const FONT_FAMILIES = [
  ['publisher', "Publisher's font"],
  ['system', 'System'],
  ['serif', 'Serif'],
  ['sans', 'Sans serif'],
  ['dyslexic', 'Dyslexia-friendly'],
];

export const READER_THEMES = [
  ['light', 'Light'],
  ['dark', 'Dark'],
  ['sepia', 'Sepia'],
  ['hc-light', 'High contrast light'],
  ['hc-dark', 'High contrast dark'],
  ['custom', 'Custom'],
];

/**
 * @param {() => void} onChange called after any setting changes
 * @param {{preview?:boolean}} [opts]
 * @returns {DocumentFragment}
 */
export function readerSettingsControls(onChange, opts = {}) {
  const frag = document.createDocumentFragment();
  const r = () => store.reader;
  const set = (patch) => { store.update('reader', patch); onChange(); };

  /* --- font scale: A- / slider / A+ --- */
  const scaleGroup = document.createElement('div');
  scaleGroup.className = 'field';
  const scaleLabel = document.createElement('span');
  scaleLabel.className = 'label';
  scaleLabel.id = 'lbl-font-scale';
  scaleLabel.textContent = 'Text size';

  const scaleRow = document.createElement('div');
  scaleRow.className = 'row';
  scaleRow.style.flexWrap = 'nowrap';

  const minus = bigButton('aMinus', 'Decrease text size', () => nudge(-0.1));
  const plus = bigButton('aPlus', 'Increase text size', () => nudge(0.1));

  const scale = document.createElement('input');
  scale.type = 'range';
  scale.min = '0.7';
  scale.max = '2.5';
  scale.step = '0.1';
  scale.value = String(r().font_scale);
  scale.setAttribute('aria-labelledby', 'lbl-font-scale');
  scale.style.flex = '1';

  const scaleOut = document.createElement('output');
  scaleOut.style.minWidth = '3.5rem';
  scaleOut.style.textAlign = 'right';
  scaleOut.style.fontVariantNumeric = 'tabular-nums';

  const syncScale = () => {
    scale.value = String(r().font_scale);
    scale.setAttribute('aria-valuetext', `${Math.round(r().font_scale * 100)} percent`);
    scaleOut.textContent = `${Math.round(r().font_scale * 100)}%`;
    minus.disabled = r().font_scale <= 0.7;
    plus.disabled = r().font_scale >= 2.5;
  };

  /** @param {number} delta */
  function nudge(delta) {
    const v = clamp(round1(r().font_scale + delta), 0.7, 2.5);
    set({ font_scale: v });
    syncScale();
    announce(`Text size ${Math.round(v * 100)} percent`);
  }

  scale.addEventListener('input', () => {
    set({ font_scale: clamp(round1(Number(scale.value)), 0.7, 2.5) });
    syncScale();
  });

  scaleRow.append(minus, scale, plus, scaleOut);
  scaleGroup.append(scaleLabel, scaleRow);
  syncScale();
  frag.append(scaleGroup);

  if (opts.preview !== false) frag.append(preview());

  /* --- theme --- */
  frag.append(radioGroup('Reader theme', 'reader-theme', READER_THEMES, r().theme,
    (v) => { set({ theme: /** @type {any} */ (v) }); syncCustom(); }));

  const custom = document.createElement('div');
  custom.className = 'row';
  custom.style.marginBottom = 'var(--s4)';
  custom.append(
    colorField('Text color', r().custom_fg, (v) => set({ custom_fg: v })),
    colorField('Background', r().custom_bg, (v) => set({ custom_bg: v })),
  );
  const syncCustom = () => { custom.hidden = r().theme !== 'custom'; };
  syncCustom();
  frag.append(custom);

  /* --- typography --- */
  frag.append(selectField('Font', FONT_FAMILIES, r().font_family,
    (v) => set({ font_family: /** @type {any} */ (v) })));

  frag.append(rangeField('Line height', r().line_height, 1, 2.4, 0.05,
    (v) => set({ line_height: v }), (v) => v.toFixed(2)));

  frag.append(rangeField('Letter spacing', r().letter_spacing, -0.05, 0.3, 0.01,
    (v) => set({ letter_spacing: v }), (v) => `${v.toFixed(2)}em`));

  frag.append(rangeField('Word spacing', r().word_spacing, 0, 1, 0.05,
    (v) => set({ word_spacing: v }), (v) => `${v.toFixed(2)}em`));

  frag.append(rangeField('Paragraph spacing', r().paragraph_spacing, 0, 3, 0.25,
    (v) => set({ paragraph_spacing: v }), (v) => `${v.toFixed(2)}em`));

  /* --- layout --- */
  frag.append(radioGroup('Margins', 'reader-margin',
    [['narrow', 'Narrow'], ['normal', 'Normal'], ['wide', 'Wide']], r().margin,
    (v) => set({ margin: /** @type {any} */ (v) })));

  frag.append(radioGroup('Text alignment', 'reader-align',
    [['publisher', 'As published'], ['left', 'Left'], ['justify', 'Justified']], r().align,
    (v) => set({ align: /** @type {any} */ (v) })));

  frag.append(radioGroup('Layout', 'reader-layout',
    [['paginated', 'Pages'], ['scrolled', 'Scrolling']], r().layout,
    (v) => set({ layout: /** @type {any} */ (v) })));

  frag.append(radioGroup('Columns', 'reader-columns',
    [['auto', 'Automatic'], ['1', 'One'], ['2', 'Two']], r().columns,
    (v) => set({ columns: /** @type {any} */ (v) })));

  /* --- reset --- */
  const reset = document.createElement('button');
  reset.type = 'button';
  reset.className = 'btn';
  reset.textContent = 'Reset reading settings';
  reset.addEventListener('click', () => {
    store.update('reader', { ...READER_DEFAULTS });
    announce('Reading settings reset');
    onChange();
  });
  frag.append(reset);

  return frag;
}

/** Live sample paragraph that reflects the current settings. */
function preview() {
  const box = document.createElement('div');
  box.className = 'card';
  box.style.marginBottom = 'var(--s4)';
  box.setAttribute('aria-label', 'Text preview');
  const p = document.createElement('p');
  p.style.margin = '0';
  p.textContent =
    'The quick brown fox jumps over the lazy dog. Sample text shows how a page '
    + 'will look with the current size, spacing and theme.';
  box.append(p);

  const apply = () => {
    if (!box.isConnected && box.dataset.wasConnected) {
      store.removeEventListener('settings', apply);
      return;
    }
    if (box.isConnected) box.dataset.wasConnected = '1';
    const s = store.reader;
    box.style.background = 'var(--reader-bg)';
    box.style.color = 'var(--reader-fg)';
    p.style.fontSize = `${s.font_scale}em`;
    p.style.lineHeight = String(s.line_height);
    p.style.letterSpacing = `${s.letter_spacing}em`;
    p.style.wordSpacing = `${s.word_spacing}em`;
    p.style.textAlign = s.align === 'publisher' ? 'start' : s.align;
    p.style.fontFamily = fontStack(s.font_family) || 'inherit';
  };
  apply();
  store.addEventListener('settings', apply);
  return box;
}

/** @param {string} family */
export function fontStack(family) {
  switch (family) {
    case 'system': return 'var(--font)';
    case 'serif': return 'var(--font-serif)';
    case 'sans': return 'var(--font-sans)';
    case 'dyslexic': return 'var(--font-dyslexic)';
    default: return '';
  }
}

/* ---------------- small form helpers ---------------- */

function bigButton(iconName, label, onClick) {
  const b = document.createElement('button');
  b.type = 'button';
  b.className = 'btn';
  b.style.minWidth = '3.25rem';
  b.style.minHeight = '3.25rem';
  b.setAttribute('aria-label', label);
  b.title = label;
  b.append(icon(iconName));
  b.addEventListener('click', onClick);
  return b;
}

/**
 * @param {string} legend
 * @param {string} name
 * @param {[string,string][]} options
 * @param {string} value
 * @param {(v:string) => void} onChange
 */
export function radioGroup(legend, name, options, value, onChange) {
  const fs = document.createElement('fieldset');
  fs.style.border = '0';
  fs.style.padding = '0';
  fs.style.margin = '0 0 var(--s4)';
  const lg = document.createElement('legend');
  lg.className = 'label';
  lg.style.padding = '0';
  lg.textContent = legend;
  fs.append(lg);
  const row = document.createElement('div');
  row.className = 'row';
  for (const [v, text] of options) {
    const l = document.createElement('label');
    l.className = 'btn';
    l.style.fontWeight = '500';
    const input = document.createElement('input');
    input.type = 'radio';
    input.name = name;
    input.value = v;
    if (v === value) input.checked = true;
    input.addEventListener('change', () => { if (input.checked) onChange(v); });
    const span = document.createElement('span');
    span.textContent = text;
    l.append(input, span);
    row.append(l);
  }
  fs.append(row);
  return fs;
}

/**
 * @param {string} label
 * @param {[string,string][]} options
 * @param {string} value
 * @param {(v:string) => void} onChange
 */
export function selectField(label, options, value, onChange) {
  const wrap = document.createElement('label');
  wrap.className = 'field';
  const l = document.createElement('span');
  l.className = 'label';
  l.textContent = label;
  const s = document.createElement('select');
  for (const [v, t] of options) {
    const o = document.createElement('option');
    o.value = v;
    o.textContent = t;
    if (v === value) o.selected = true;
    s.append(o);
  }
  s.addEventListener('change', () => onChange(s.value));
  wrap.append(l, s);
  return wrap;
}

/**
 * @param {string} label
 * @param {number} value
 * @param {number} min @param {number} max @param {number} step
 * @param {(v:number) => void} onChange
 * @param {(v:number) => string} fmt
 */
export function rangeField(label, value, min, max, step, onChange, fmt) {
  const wrap = document.createElement('div');
  wrap.className = 'field';
  const l = document.createElement('label');
  l.className = 'label';
  const id = 'r-' + label.toLowerCase().replace(/\W+/g, '-');
  l.setAttribute('for', id);
  l.textContent = label;
  const row = document.createElement('div');
  row.className = 'row';
  row.style.flexWrap = 'nowrap';
  const input = document.createElement('input');
  input.type = 'range';
  input.id = id;
  input.min = String(min);
  input.max = String(max);
  input.step = String(step);
  input.value = String(value);
  input.style.flex = '1';
  const out = document.createElement('output');
  out.style.minWidth = '4rem';
  out.style.textAlign = 'right';
  out.style.fontVariantNumeric = 'tabular-nums';
  const sync = () => {
    out.textContent = fmt(Number(input.value));
    input.setAttribute('aria-valuetext', out.textContent);
  };
  sync();
  input.addEventListener('input', () => { onChange(Number(input.value)); sync(); });
  row.append(input, out);
  wrap.append(l, row);
  return wrap;
}

/**
 * @param {string} label @param {string} value @param {(v:string) => void} onChange
 */
function colorField(label, value, onChange) {
  const wrap = document.createElement('label');
  wrap.className = 'field';
  wrap.style.margin = '0';
  const l = document.createElement('span');
  l.className = 'label';
  l.textContent = label;
  const input = document.createElement('input');
  input.type = 'color';
  input.value = value;
  input.style.minHeight = 'var(--tap)';
  input.style.minWidth = '4rem';
  input.addEventListener('input', () => onChange(input.value));
  wrap.append(l, input);
  return wrap;
}

const round1 = (n) => Math.round(n * 10) / 10;
const clamp = (n, lo, hi) => Math.min(hi, Math.max(lo, n));
