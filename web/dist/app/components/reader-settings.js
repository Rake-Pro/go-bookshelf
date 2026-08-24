/**
 * Reader settings controls, shared between the reader's settings sheet and the
 * Settings page (where they act as defaults).
 *
 * Every control writes through `store.update('reader', ...)`, which debounces a
 * single PUT /me/settings and mirrors to localStorage immediately.
 *
 * The controls carry their own <style> element rather than relying on app.css,
 * because the reader opens them inside the sheet's shadow root where a document
 * stylesheet does not reach. Every class is prefixed `rs-` so the same tag is
 * harmless in the light DOM of the Settings page.
 */

import { store, READER_DEFAULTS } from '../store.js';
import { READER_PALETTES, readerPalette } from '../epub.js';
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
  ['light', 'Paper'],
  ['sepia', 'Sepia'],
  ['gray', 'Gray'],
  ['dark', 'Night'],
  ['hc-light', 'Contrast light'],
  ['hc-dark', 'Contrast dark'],
  ['custom', 'Custom'],
];

/**
 * Two columns need at least this much width, and a landscape viewport. Lives
 * here rather than in the reader view so the control and the layout that
 * honours it read the same number.
 */
export const TWO_COLUMN_MIN_PX = 1100;

/** Whether the viewport can carry two columns of a readable measure. */
export function twoColumnsPossible() {
  return window.innerWidth >= TWO_COLUMN_MIN_PX && window.innerWidth > window.innerHeight;
}

const SCALE_MIN = 0.7;
const SCALE_MAX = 2.5;
const SCALE_STEP = 0.05;
/** A- / A+ move in bigger jumps than the slider's own step. */
const SCALE_NUDGE = 0.1;

/**
 * @param {() => void} onChange called after any setting changes
 * @param {{preview?:boolean}} [opts]
 * @returns {DocumentFragment}
 */
export function readerSettingsControls(onChange, opts = {}) {
  const frag = document.createDocumentFragment();
  frag.append(controlsStyle());

  const root = document.createElement('div');
  root.className = 'rs';
  frag.append(root);

  const r = () => store.reader;
  const set = (patch) => { store.update('reader', patch); onChange(); };
  /** Re-read the store into every control; used after "reset". */
  /** @type {(() => void)[]} */
  const syncers = [];

  /* ---------------- Text ---------------- */

  const text = group('Text');

  // Label on its own line, controls below, the same shape as rangeRow: a
  // 360px-wide phone cannot fit a label, two 44px steppers, a slider and a
  // readout on one line without leaving the slider unusable.
  const sizeField = document.createElement('div');
  sizeField.className = 'rs-field';
  const sizeRow = document.createElement('div');
  sizeRow.className = 'rs-row';
  const scaleLabel = document.createElement('span');
  scaleLabel.className = 'rs-label';
  scaleLabel.id = 'lbl-font-scale';
  scaleLabel.textContent = 'Size';

  const minus = stepButton('aMinus', 'Decrease text size', () => nudge(-SCALE_NUDGE));
  const plus = stepButton('aPlus', 'Increase text size', () => nudge(SCALE_NUDGE));

  const scale = document.createElement('input');
  scale.type = 'range';
  scale.className = 'rs-range';
  scale.min = String(SCALE_MIN);
  scale.max = String(SCALE_MAX);
  scale.step = String(SCALE_STEP);
  scale.value = String(r().font_scale);
  scale.setAttribute('aria-labelledby', 'lbl-font-scale');

  const scaleOut = document.createElement('output');
  scaleOut.className = 'rs-out';

  const syncScale = () => {
    scale.value = String(r().font_scale);
    scale.setAttribute('aria-valuetext', `${Math.round(r().font_scale * 100)} percent`);
    scaleOut.textContent = `${Math.round(r().font_scale * 100)}%`;
    minus.disabled = r().font_scale <= SCALE_MIN;
    plus.disabled = r().font_scale >= SCALE_MAX;
  };

  /** @param {number} delta */
  function nudge(delta) {
    const v = clamp(round2(r().font_scale + delta), SCALE_MIN, SCALE_MAX);
    set({ font_scale: v });
    syncScale();
    announce(`Text size ${Math.round(v * 100)} percent`);
  }

  scale.addEventListener('input', () => {
    set({ font_scale: clamp(round2(Number(scale.value)), SCALE_MIN, SCALE_MAX) });
    syncScale();
  });

  sizeRow.append(minus, scale, plus, scaleOut);
  sizeField.append(scaleLabel, sizeRow);
  text.append(sizeField);
  syncScale();

  text.append(segmented('Font', 'reader-font', FONT_FAMILIES, () => r().font_family,
    (v) => set({ font_family: /** @type {any} */ (v) }), syncers, { wrap: true }));

  root.append(text);

  if (opts.preview !== false) root.append(preview());

  /* ---------------- Theme ---------------- */

  const theme = group('Theme');
  const swatches = document.createElement('div');
  swatches.className = 'rs-swatches';
  swatches.setAttribute('role', 'radiogroup');
  swatches.setAttribute('aria-label', 'Reader theme');
  /** @type {HTMLLabelElement[]} */
  const swatchEls = [];
  for (const [value, label] of READER_THEMES) {
    const el = swatch(value, label, () => {
      set({ theme: /** @type {any} */ (value) });
      syncTheme();
      announce(`${label} theme`);
    });
    swatchEls.push(el);
    swatches.append(el);
  }
  theme.append(swatches);

  const custom = document.createElement('div');
  custom.className = 'rs-row rs-custom';
  custom.append(
    colorField('Text', () => r().custom_fg, (v) => { set({ custom_fg: v }); syncTheme(); }),
    colorField('Background', () => r().custom_bg, (v) => { set({ custom_bg: v }); syncTheme(); }),
  );
  theme.append(custom);

  const syncTheme = () => {
    for (const el of swatchEls) {
      const on = el.dataset.value === r().theme;
      el.classList.toggle('is-on', on);
      /** @type {HTMLInputElement} */ (el.querySelector('input')).checked = on;
      if (el.dataset.value === 'custom') {
        const chip = /** @type {HTMLElement} */ (el.querySelector('.rs-chip'));
        chip.style.background = r().custom_bg;
        chip.style.color = r().custom_fg;
      }
    }
    custom.hidden = r().theme !== 'custom';
  };
  syncTheme();
  root.append(theme);

  /* ---------------- Layout ---------------- */

  const layout = group('Layout');

  layout.append(segmented('Margins', 'reader-margin',
    [['narrow', 'Narrow'], ['normal', 'Normal'], ['wide', 'Wide']],
    () => r().margin, (v) => set({ margin: /** @type {any} */ (v) }), syncers));

  layout.append(rangeRow('Line spacing', () => r().line_height, 1.2, 2.4, 0.05,
    (v) => set({ line_height: v }), (v) => v.toFixed(2), syncers));

  layout.append(segmented('Alignment', 'reader-align',
    [['publisher', 'As published'], ['left', 'Left'], ['justify', 'Justified']],
    () => r().align, (v) => set({ align: /** @type {any} */ (v) }), syncers, { wrap: true }));

  layout.append(segmented('Reading mode', 'reader-layout',
    [['paginated', 'Pages'], ['scrolled', 'Scrolling']],
    () => r().layout, (v) => set({ layout: /** @type {any} */ (v) }), syncers));

  // Two columns on a phone would give each one a ~180px measure, so the
  // renderer ignores the choice there. Show that in the control rather than
  // accepting a setting that will not be honoured.
  const TWO_COL_HINT = 'Two columns need a wider screen in landscape';
  const columnsField = segmented('Columns', 'reader-columns',
    [['auto', 'Automatic'], ['1', 'One'], ['2', 'Two']],
    () => r().columns, (v) => set({ columns: /** @type {any} */ (v) }), syncers,
    twoColumnsPossible() ? {} : {
      disabled: ['2'],
      disabledHint: TWO_COL_HINT,
    });
  layout.append(columnsField);

  // twoColumnsPossible() was only read once, at build time; rotating the
  // device while the sheet is open must not leave the option stuck in
  // whichever state it started in.
  const twoColLabel = /** @type {HTMLLabelElement|null} */ (columnsField.querySelector('label[data-value="2"]'));
  const twoColInput = /** @type {HTMLInputElement|null} */ (twoColLabel?.querySelector('input'));
  const syncTwoColumnsDisabled = () => {
    if (!twoColLabel || !twoColInput) return;
    const disabled = !twoColumnsPossible();
    twoColInput.disabled = disabled;
    twoColLabel.classList.toggle('is-off', disabled);
    if (disabled) {
      twoColLabel.title = TWO_COL_HINT;
      twoColInput.setAttribute('aria-label', `Two. ${TWO_COL_HINT}`);
    } else {
      twoColLabel.removeAttribute('title');
      twoColInput.removeAttribute('aria-label');
    }
  };
  // Self-removes once the control leaves the document (the sheet closes and
  // discards its content), the same teardown shape as preview()'s listener.
  const onResize = () => {
    if (!root.isConnected) { window.removeEventListener('resize', onResize); return; }
    syncTwoColumnsDisabled();
  };
  // opts.signal ties the listener to the caller's lifetime (a closing sheet,
  // a leaving view); the isConnected check above is the fallback.
  window.addEventListener('resize', onResize, opts.signal ? { signal: opts.signal } : undefined);

  root.append(layout);

  /* ---------------- Fine tuning ---------------- */

  const more = document.createElement('details');
  more.className = 'rs-more';
  const summary = document.createElement('summary');
  summary.textContent = 'Fine tuning';
  more.append(summary);

  more.append(rangeRow('Letter spacing', () => r().letter_spacing, -0.05, 0.3, 0.01,
    (v) => set({ letter_spacing: v }), (v) => `${v.toFixed(2)}em`, syncers));
  more.append(rangeRow('Word spacing', () => r().word_spacing, 0, 1, 0.05,
    (v) => set({ word_spacing: v }), (v) => `${v.toFixed(2)}em`, syncers));
  more.append(rangeRow('Paragraph spacing', () => r().paragraph_spacing, 0, 3, 0.25,
    (v) => set({ paragraph_spacing: v }), (v) => `${v.toFixed(2)}em`, syncers));

  const reset = document.createElement('button');
  reset.type = 'button';
  reset.className = 'rs-btn';
  reset.textContent = 'Reset reading settings';
  reset.addEventListener('click', () => {
    store.update('reader', { ...READER_DEFAULTS });
    announce('Reading settings reset');
    syncScale();
    syncTheme();
    for (const sync of syncers) sync();
    onChange();
  });
  more.append(reset);
  root.append(more);

  return frag;
}

/** Live sample paragraph that reflects the current settings. */
function preview() {
  const box = document.createElement('div');
  box.className = 'rs-preview';
  box.setAttribute('aria-label', 'Text preview');
  const p = document.createElement('p');
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
    // Painted from the palette rather than the reader tokens: on the Settings
    // page no reading theme is applied to <html>, so the sample must carry it.
    const pal = readerPalette(s);
    box.style.background = pal.bg;
    box.style.color = pal.fg;
    p.style.fontSize = `${s.font_scale}em`;
    p.style.lineHeight = String(s.line_height);
    p.style.letterSpacing = `${s.letter_spacing}em`;
    p.style.wordSpacing = `${s.word_spacing}em`;
    p.style.textAlign = s.align === 'publisher' ? 'start' : s.align;
    p.style.fontFamily = fontStack(s.font_family) || 'var(--font-serif)';
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

/** @param {string} title */
function group(title) {
  const section = document.createElement('section');
  section.className = 'rs-group';
  const h = document.createElement('h3');
  h.className = 'rs-legend';
  h.textContent = title;
  section.append(h);
  return section;
}

/** @param {string} iconName @param {string} label @param {() => void} onClick */
function stepButton(iconName, label, onClick) {
  const b = document.createElement('button');
  b.type = 'button';
  b.className = 'rs-step';
  b.setAttribute('aria-label', label);
  b.title = label;
  b.append(icon(iconName));
  b.addEventListener('click', onClick);
  return b;
}

/**
 * Segmented control: one radio per option, styled as a single strip. The
 * radios stay real inputs (arrow-key navigation, screen reader semantics);
 * only their default rendering is hidden.
 *
 * @param {string} legend
 * @param {string} name
 * @param {[string,string][]} options
 * @param {() => string} value
 * @param {(v:string) => void} onChange
 * @param {(() => void)[]} syncers
 * @param {{wrap?:boolean, disabled?:string[], disabledHint?:string}} [opts]
 */
function segmented(legend, name, options, value, onChange, syncers, opts = {}) {
  const fs = document.createElement('fieldset');
  fs.className = 'rs-field';
  const lg = document.createElement('legend');
  lg.className = 'rs-label';
  lg.textContent = legend;
  fs.append(lg);

  const strip = document.createElement('div');
  strip.className = opts.wrap ? 'rs-seg rs-seg--wrap' : 'rs-seg';
  /** @type {HTMLLabelElement[]} */
  const labels = [];
  const sync = () => {
    for (const l of labels) {
      const on = l.dataset.value === value();
      l.classList.toggle('is-on', on);
      /** @type {HTMLInputElement} */ (l.querySelector('input')).checked = on;
    }
  };
  syncers.push(() => sync());
  for (const [v, label] of options) {
    const l = document.createElement('label');
    l.dataset.value = v;
    const input = document.createElement('input');
    input.type = 'radio';
    input.name = name;
    input.value = v;
    input.checked = v === value();
    input.addEventListener('change', () => {
      if (!input.checked) return;
      onChange(v);
      sync();
    });
    const span = document.createElement('span');
    span.textContent = label;
    if (opts.disabled?.includes(v)) {
      input.disabled = true;
      l.classList.add('is-off');
      if (opts.disabledHint) {
        l.title = opts.disabledHint;
        input.setAttribute('aria-label', `${label}. ${opts.disabledHint}`);
      }
    }
    l.append(input, span);
    labels.push(l);
    strip.append(l);
  }
  sync();
  fs.append(strip);
  return fs;
}

/**
 * @param {string} value @param {string} label @param {() => void} onChange
 * @returns {HTMLLabelElement}
 */
function swatch(value, label, onChange) {
  const l = document.createElement('label');
  l.className = 'rs-swatch';
  l.dataset.value = value;
  const input = document.createElement('input');
  input.type = 'radio';
  input.name = 'reader-theme';
  input.value = value;
  input.addEventListener('change', () => { if (input.checked) onChange(); });
  const chip = document.createElement('span');
  chip.className = 'rs-chip';
  chip.setAttribute('aria-hidden', 'true');
  chip.textContent = 'Aa';
  const p = READER_PALETTES[value];
  if (p) {
    chip.style.background = p.bg;
    chip.style.color = p.fg;
  }
  const name = document.createElement('span');
  name.className = 'rs-swatch-name';
  name.textContent = label;
  l.append(input, chip, name);
  return l;
}

/**
 * @param {string} label
 * @param {() => number} value
 * @param {number} min @param {number} max @param {number} step
 * @param {(v:number) => void} onChange
 * @param {(v:number) => string} fmt
 * @param {(() => void)[]} syncers
 */
function rangeRow(label, value, min, max, step, onChange, fmt, syncers) {
  const wrap = document.createElement('div');
  wrap.className = 'rs-field';
  const l = document.createElement('label');
  l.className = 'rs-label';
  const id = 'rs-' + label.toLowerCase().replace(/\W+/g, '-');
  l.setAttribute('for', id);
  l.textContent = label;
  const row = document.createElement('div');
  row.className = 'rs-row';
  const input = document.createElement('input');
  input.type = 'range';
  input.className = 'rs-range';
  input.id = id;
  input.min = String(min);
  input.max = String(max);
  input.step = String(step);
  input.value = String(value());
  const out = document.createElement('output');
  out.className = 'rs-out';
  const sync = () => {
    out.textContent = fmt(Number(input.value));
    input.setAttribute('aria-valuetext', out.textContent);
  };
  sync();
  syncers.push(() => { input.value = String(value()); sync(); });
  input.addEventListener('input', () => { onChange(Number(input.value)); sync(); });
  row.append(input, out);
  wrap.append(l, row);
  return wrap;
}

/**
 * @param {string} label @param {() => string} value @param {(v:string) => void} onChange
 */
function colorField(label, value, onChange) {
  const wrap = document.createElement('label');
  wrap.className = 'rs-color';
  const l = document.createElement('span');
  l.className = 'rs-label';
  l.textContent = label;
  const input = document.createElement('input');
  input.type = 'color';
  input.value = value();
  input.setAttribute('aria-label', `${label} color`);
  input.addEventListener('input', () => onChange(input.value));
  wrap.append(l, input);
  return wrap;
}

/* ---------------- radio / select / range for the Settings page ---------------- */

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
 * Styles for the controls above. Travels with the fragment so it works both in
 * the light DOM (Settings page) and inside the sheet's shadow root (reader).
 */
function controlsStyle() {
  const style = document.createElement('style');
  style.textContent = `
.rs { display: grid; gap: var(--s6); font-family: var(--font); }
.rs, .rs * { box-sizing: border-box; }
/* A class that sets display would otherwise beat the UA rule for [hidden]. */
.rs [hidden] { display: none !important; }
.rs-group { display: grid; gap: var(--s3); }
.rs-legend {
  margin: 0;
  font-size: 0.75rem;
  font-weight: 700;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: var(--muted);
}
.rs-field { display: grid; gap: var(--s2); margin: 0; padding: 0; border: 0; min-width: 0; }
.rs-label { display: block; padding: 0; font-size: 0.95rem; font-weight: 600; }
.rs-row { display: flex; align-items: center; gap: var(--s2); }
.rs-out {
  min-width: 3.5rem;
  text-align: right;
  font-variant-numeric: tabular-nums;
  color: var(--muted);
}

.rs-range {
  flex: 1;
  width: 100%;
  height: var(--tap);
  margin: 0;
  accent-color: var(--accent);
  background: transparent;
}
.rs-range:focus-visible { outline: 3px solid var(--focus); outline-offset: 2px; }

.rs-step, .rs-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-height: var(--tap);
  min-width: var(--tap);
  padding: var(--s2) var(--s4);
  font: inherit;
  font-weight: 600;
  color: var(--text);
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  cursor: pointer;
}
.rs-step { padding: 0; }
.rs-step svg { width: 1.5rem; height: 1.5rem; }
.rs-step:disabled { opacity: 0.45; cursor: not-allowed; }
.rs-step:hover:not(:disabled), .rs-btn:hover { background: var(--surface-2); }
.rs-step:focus-visible, .rs-btn:focus-visible { outline: 3px solid var(--focus); outline-offset: 2px; }
.rs-btn { width: 100%; margin-top: var(--s3); }

.rs-seg {
  display: flex;
  gap: var(--s1);
  padding: var(--s1);
  background: var(--surface-2);
  border: 1px solid var(--border);
  border-radius: var(--radius);
}
.rs-seg--wrap {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(7rem, 1fr));
}
.rs-seg label {
  position: relative;
  flex: 1 1 auto;
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: var(--tap);
  padding: 0 var(--s3);
  border-radius: var(--radius-sm);
  font-size: 0.95rem;
  font-weight: 600;
  text-align: center;
  cursor: pointer;
}
.rs-seg input {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  margin: 0;
  opacity: 0;
  cursor: pointer;
}
.rs-seg label.is-on {
  color: var(--accent-text);
  background: var(--accent);
}
.rs-seg label:hover:not(.is-on):not(.is-off) { background: var(--surface); }
/* An option the viewport cannot honour is shown greyed out, not silently
   ignored. */
.rs-seg label.is-off { opacity: 0.45; cursor: not-allowed; }
.rs-seg label.is-off input { cursor: not-allowed; }
.rs-seg label:has(input:focus-visible) { outline: 3px solid var(--focus); outline-offset: 2px; }

.rs-swatches {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(6.5rem, 1fr));
  gap: var(--s2);
}
.rs-swatch {
  position: relative;
  display: grid;
  gap: var(--s1);
  justify-items: center;
  padding: var(--s2);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  cursor: pointer;
}
.rs-swatch input { position: absolute; inset: 0; opacity: 0; margin: 0; cursor: pointer; }
.rs-chip {
  display: grid;
  place-items: center;
  width: 100%;
  height: 2.5rem;
  border: 1px solid rgb(128 128 128 / 35%);
  border-radius: var(--radius-sm);
  font-size: 1rem;
  font-weight: 700;
  font-family: var(--font-serif);
}
.rs-swatch-name { font-size: 0.85rem; font-weight: 600; text-align: center; }
.rs-swatch.is-on { border-color: var(--accent); box-shadow: inset 0 0 0 2px var(--accent); }
.rs-swatch:has(input:focus-visible) { outline: 3px solid var(--focus); outline-offset: 2px; }

.rs-custom { gap: var(--s4); }
.rs-color { display: grid; gap: var(--s1); }
.rs-color input {
  width: 4.5rem;
  height: var(--tap);
  padding: 2px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
}

.rs-preview {
  padding: var(--s4);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  font-family: var(--font-serif);
}
.rs-preview p { margin: 0; }

.rs-more > summary {
  min-height: var(--tap);
  display: flex;
  align-items: center;
  font-weight: 600;
  cursor: pointer;
}
.rs-more > summary:focus-visible { outline: 3px solid var(--focus); outline-offset: 2px; }
.rs-more[open] { display: grid; gap: var(--s3); }
`;
  return style;
}

const round2 = (n) => Math.round(n * 100) / 100;
const clamp = (n, lo, hi) => Math.min(hi, Math.max(lo, n));
