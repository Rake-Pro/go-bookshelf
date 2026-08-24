/**
 * Global audiobook playback controller.
 *
 * There is exactly one <audio> element for the whole app, owned by this module,
 * so navigating between routes never interrupts playback. Views subscribe to
 * events and render; they never touch the element.
 *
 * Positions are always ABSOLUTE milliseconds across the concatenated file
 * sequence (matching `progress.position_ms` in the API). Per-file offsets are
 * derived from `files[].duration_ms` in spine order (`files[].seq`).
 *
 * Chapter contract: `chapters[].start_ms`/`end_ms` are relative to their own
 * file; this module adds the file offset to get absolute chapter bounds.
 */

import { api, streamUrl, coverUrl, BASE } from './api.js';
import { store, deviceName } from './store.js';
import { announce } from './live.js';
import { names, peopleOf, clock } from './format.js';

const SAVE_INTERVAL_MS = 15000;

/**
 * @typedef {Object} Track
 * @property {string} id      file id
 * @property {number} offset  absolute ms where this file starts
 * @property {number} duration ms
 */

/**
 * @typedef {Object} Chapter
 * @property {string} title
 * @property {number} start  absolute ms
 * @property {number} end    absolute ms
 */

class PlayerController extends EventTarget {
  /** @type {HTMLAudioElement} */
  audio = new Audio();
  /** @type {any|null} */
  item = null;
  /** @type {Track[]} */
  tracks = [];
  /** @type {Chapter[]} */
  chapters = [];
  /** total ms across all files */
  duration = 0;
  /** index into `tracks` */
  index = 0;
  /** @type {'idle'|'loading'|'playing'|'paused'|'ended'|'error'} */
  state = 'idle';
  /** @type {number|null} epoch ms when playback should stop */
  sleepAt = null;
  /** stop at the end of the current chapter instead of a clock */
  sleepEndOfChapter = false;

  #lastSavedAt = 0;
  #lastSaved = -1;
  #lastChapter = -1;
  #seekPending = 0;
  /** True when `duration` was summed from files[].duration_ms rather than taken from item.duration_ms. */
  #durationIsSum = false;

  constructor() {
    super();
    this.audio.preload = 'metadata';

    this.audio.addEventListener('timeupdate', () => this.#onTime());
    this.audio.addEventListener('play', () => this.#setState('playing'));
    this.audio.addEventListener('pause', () => {
      if (this.state !== 'ended') this.#setState('paused');
      this.saveProgress();
    });
    this.audio.addEventListener('waiting', () => this.#setState('loading'));
    this.audio.addEventListener('playing', () => this.#setState('playing'));
    this.audio.addEventListener('ended', () => this.#onTrackEnded());
    this.audio.addEventListener('error', () => {
      if (!this.item) return;
      this.#setState('error');
      announce('Playback error');
    });
    this.audio.addEventListener('loadedmetadata', () => {
      // Trust real metadata over the catalog's duration for the active file.
      const t = this.tracks[this.index];
      if (t && Number.isFinite(this.audio.duration) && this.audio.duration > 0) {
        t.duration = Math.round(this.audio.duration * 1000);
        // Offsets and chapter bounds stay on the catalog scale: shifting the
        // later tracks here would desync chapters[], which is baked from the
        // original offsets at load(). Only the summed total is refreshed.
        if (this.#durationIsSum) {
          this.duration = this.tracks.reduce((sum, x) => sum + x.duration, 0);
        }
      }
      if (this.#seekPending) {
        const target = this.#seekPending;
        this.#seekPending = 0;
        this.seek(target);
      }
      this.#emit('time');
    });

    document.addEventListener('visibilitychange', () => {
      if (document.visibilityState === 'hidden') this.saveProgress({ keepalive: true });
    });
    window.addEventListener('pagehide', () => this.saveProgress({ keepalive: true }));

    this.#setupMediaSession();
  }

  /* ---------------- loading ---------------- */

  /**
   * @param {any} item full item payload from GET /items/{id}
   * @param {{startMs?:number, autoplay?:boolean}} [opts]
   */
  async load(item, opts = {}) {
    const sameItem = this.item?.id === item.id;
    if (sameItem && this.tracks.length) {
      if (opts.startMs != null) this.seek(opts.startMs);
      if (opts.autoplay) await this.play();
      return;
    }
    if (this.item && !sameItem) this.saveProgress();

    this.item = item;
    const files = (item.files || []).slice().sort((a, b) => (a.seq ?? 0) - (b.seq ?? 0));
    let offset = 0;
    this.tracks = files.map((f) => {
      const t = { id: f.id, offset, duration: f.duration_ms || 0 };
      offset += t.duration;
      return t;
    });
    this.duration = item.duration_ms || offset;
    this.#durationIsSum = !item.duration_ms;
    this.chapters = this.#buildChapters(item, files);
    this.index = 0;
    this.#lastChapter = -1;
    this.#lastSaved = -1;
    this.#lastSavedAt = 0;

    const start = opts.startMs ?? item.progress?.position_ms ?? 0;
    this.audio.playbackRate = store.player.speed || 1;
    this.#loadTrackAt(start);
    this.#emit('load');
    this.#updateMediaMetadata();
    if (opts.autoplay) await this.play();
  }

  /**
   * @param {any} item
   * @param {any[]} files
   * @returns {Chapter[]}
   */
  #buildChapters(item, files) {
    /** @type {Chapter[]} */
    const out = [];
    const offsets = new Map(this.tracks.map((t) => [t.id, t.offset]));
    const raw = (item.chapters || []).slice()
      .sort((a, b) => (offsets.get(a.file_id) ?? 0) + (a.start_ms || 0)
        - ((offsets.get(b.file_id) ?? 0) + (b.start_ms || 0)));
    for (const c of raw) {
      const base = offsets.get(c.file_id) ?? 0;
      out.push({
        title: c.title || `Chapter ${out.length + 1}`,
        start: base + (c.start_ms || 0),
        end: c.end_ms != null ? base + c.end_ms : 0,
      });
    }
    if (!out.length) {
      // Fallback: one chapter per file.
      files.forEach((f, i) => {
        const t = this.tracks[i];
        if (!t) return;
        out.push({ title: f.title || `Part ${i + 1}`, start: t.offset, end: t.offset + t.duration });
      });
    }
    for (let i = 0; i < out.length; i++) {
      if (!out[i].end) out[i].end = out[i + 1]?.start ?? this.duration;
    }
    return out;
  }

  /** @param {number} absMs */
  #loadTrackAt(absMs) {
    if (!this.item || !this.tracks.length) return;
    const i = this.trackIndexAt(absMs);
    const t = this.tracks[i];
    const within = Math.max(0, absMs - t.offset);
    if (i !== this.index || !this.audio.src) {
      this.index = i;
      this.audio.src = streamUrl(this.item.id, t.id);
      this.#seekPending = absMs;
      this.audio.load();
    } else {
      this.audio.currentTime = within / 1000;
    }
  }

  /** @param {number} absMs @returns {number} */
  trackIndexAt(absMs) {
    let i = 0;
    for (let k = 0; k < this.tracks.length; k++) {
      if (absMs >= this.tracks[k].offset) i = k; else break;
    }
    return i;
  }

  /* ---------------- transport ---------------- */

  get position() {
    const t = this.tracks[this.index];
    if (!t) return 0;
    return t.offset + Math.round((this.audio.currentTime || 0) * 1000);
  }

  get playing() { return this.state === 'playing' || (!this.audio.paused && !this.audio.ended); }

  async play() {
    if (!this.item) return;
    try {
      this.audio.playbackRate = store.player.speed || 1;
      await this.audio.play();
    } catch (e) {
      this.#setState('paused');
    }
  }

  pause() { this.audio.pause(); }

  toggle() { return this.playing ? this.pause() : this.play(); }

  /** @param {number} absMs */
  seek(absMs) {
    if (!this.tracks.length) return;
    const clamped = Math.max(0, Math.min(this.duration || 0, absMs));
    const i = this.trackIndexAt(clamped);
    if (i !== this.index) {
      const wasPlaying = this.playing;
      this.index = i;
      this.audio.src = streamUrl(this.item.id, this.tracks[i].id);
      this.#seekPending = clamped;
      this.audio.load();
      if (wasPlaying) this.play();
    } else {
      this.audio.currentTime = Math.max(0, clamped - this.tracks[i].offset) / 1000;
    }
    this.#emit('time');
  }

  /** @param {number} seconds negative to rewind */
  skip(seconds) { this.seek(this.position + seconds * 1000); }

  skipBack() { this.skip(-(store.player.skip_back_s || 15)); }
  skipForward() { this.skip(store.player.skip_fwd_s || 30); }

  /** @param {number} rate 0.5 - 3.0 */
  setSpeed(rate) {
    const v = Math.max(0.5, Math.min(3, Math.round(rate * 20) / 20));
    this.audio.playbackRate = v;
    store.update('player', { speed: v });
    announce(`Speed ${v.toFixed(2).replace(/0$/, '')} times`);
    this.#emit('speed');
  }

  /** @param {number|null} minutes null clears; use setSleepEndOfChapter for the chapter mode */
  setSleepTimer(minutes) {
    this.sleepEndOfChapter = false;
    this.sleepAt = minutes ? Date.now() + minutes * 60000 : null;
    store.update('player', { sleep_timer_min: minutes, sleep_end_of_chapter: false });
    announce(minutes ? `Sleep timer set for ${minutes} minutes` : 'Sleep timer off');
    this.#emit('sleep');
  }

  setSleepEndOfChapter() {
    this.sleepAt = null;
    this.sleepEndOfChapter = true;
    store.update('player', { sleep_timer_min: null, sleep_end_of_chapter: true });
    announce('Sleep timer set to end of chapter');
    this.#emit('sleep');
  }

  get sleepRemainingMs() {
    return this.sleepAt ? Math.max(0, this.sleepAt - Date.now()) : 0;
  }

  /* ---------------- chapters ---------------- */

  get chapterIndex() {
    const p = this.position;
    for (let i = this.chapters.length - 1; i >= 0; i--) {
      if (p >= this.chapters[i].start) return i;
    }
    return 0;
  }

  get chapter() { return this.chapters[this.chapterIndex] || null; }

  /** @param {number} i */
  goToChapter(i) {
    const c = this.chapters[i];
    if (!c) return;
    this.seek(c.start);
    announce(c.title);
  }

  nextChapter() { this.goToChapter(Math.min(this.chapters.length - 1, this.chapterIndex + 1)); }
  prevChapter() { this.goToChapter(Math.max(0, this.chapterIndex - 1)); }

  /* ---------------- internals ---------------- */

  #onTime() {
    const pos = this.position;
    const ci = this.chapterIndex;
    if (ci !== this.#lastChapter) {
      this.#lastChapter = ci;
      this.#updateMediaMetadata();
      this.#emit('chapter');
      if (this.state === 'playing') this.saveProgress();
    }
    if (this.playing && Date.now() - this.#lastSavedAt > SAVE_INTERVAL_MS) {
      this.saveProgress();
    }
    if (this.sleepAt && Date.now() >= this.sleepAt) {
      this.sleepAt = null;
      this.pause();
      announce('Sleep timer finished. Playback paused.');
      this.#emit('sleep');
    }
    if (this.sleepEndOfChapter) {
      const c = this.chapter;
      if (c && pos >= c.end - 250) {
        this.sleepEndOfChapter = false;
        this.pause();
        announce('End of chapter. Playback paused.');
        this.#emit('sleep');
      }
    }
    this.#updatePositionState();
    this.#emit('time');
  }

  #onTrackEnded() {
    if (this.index < this.tracks.length - 1) {
      const next = this.index + 1;
      this.index = next;
      this.audio.src = streamUrl(this.item.id, this.tracks[next].id);
      this.#seekPending = this.tracks[next].offset;
      this.audio.load();
      this.play();
      return;
    }
    this.#setState('ended');
    announce('Finished');
    this.saveProgress({ finished: true });
    this.#emit('ended');
  }

  /** @param {typeof PlayerController.prototype.state} s */
  #setState(s) {
    if (this.state === s) return;
    this.state = s;
    if (s === 'playing' && !this.#lastSavedAt) this.#lastSavedAt = Date.now();
    if ('mediaSession' in navigator) {
      navigator.mediaSession.playbackState =
        s === 'playing' ? 'playing' : s === 'paused' ? 'paused' : 'none';
    }
    if (s === 'playing') announce('Playing');
    else if (s === 'paused') announce('Paused');
    this.#emit('state');
  }

  /**
   * Push the current position to the server.
   * @param {{keepalive?:boolean, finished?:boolean}} [opts]
   */
  saveProgress(opts = {}) {
    if (!this.item || !this.tracks.length) return;
    const position = this.position;
    if (!opts.finished && !opts.keepalive && position === this.#lastSaved) return;
    this.#lastSaved = position;
    this.#lastSavedAt = Date.now();
    const body = {
      position_ms: Math.round(position),
      percent: this.duration ? Math.min(1, position / this.duration) : 0,
      finished: Boolean(opts.finished),
      device: deviceName(),
    };
    if (opts.keepalive) {
      // fetch(keepalive) survives page unload where a normal request would not.
      try {
        fetch(`${BASE}/me/progress/${encodeURIComponent(this.item.id)}`, {
          method: 'PUT',
          credentials: 'same-origin',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
          keepalive: true,
        }).catch(() => {});
      } catch { /* ignore: nothing can be done during unload */ }
      return;
    }
    api.putProgress(this.item.id, body).catch(() => {
      // Progress is best-effort; the next tick retries.
      this.#lastSaved = -1;
      this.#lastSavedAt = 0;
    });
  }

  #setupMediaSession() {
    if (!('mediaSession' in navigator)) return;
    const ms = navigator.mediaSession;
    /** @type {[string, () => void][]} */
    const handlers = [
      ['play', () => this.play()],
      ['pause', () => this.pause()],
      ['stop', () => { this.pause(); this.seek(0); }],
      ['seekbackward', () => this.skipBack()],
      ['seekforward', () => this.skipForward()],
      ['previoustrack', () => this.prevChapter()],
      ['nexttrack', () => this.nextChapter()],
    ];
    for (const [name, fn] of handlers) {
      try { ms.setActionHandler(name, fn); } catch { /* unsupported action */ }
    }
    try {
      ms.setActionHandler('seekto', (details) => {
        if (details.seekTime != null) this.seek(details.seekTime * 1000);
      });
    } catch { /* unsupported */ }
  }

  #updateMediaMetadata() {
    if (!('mediaSession' in navigator) || !this.item) return;
    const author = names(peopleOf(this.item, 'author'));
    const narrator = names(peopleOf(this.item, 'narrator'));
    try {
      navigator.mediaSession.metadata = new MediaMetadata({
        title: this.chapter?.title || this.item.title || '',
        artist: [author, narrator ? `read by ${narrator}` : ''].filter(Boolean).join(' - '),
        album: this.item.title || '',
        artwork: [
          { src: coverUrl(this.item.id, 'full'), sizes: '512x512', type: 'image/jpeg' },
        ],
      });
    } catch { /* MediaMetadata unavailable */ }
  }

  #updatePositionState() {
    if (!('mediaSession' in navigator) || !navigator.mediaSession.setPositionState) return;
    if (!this.duration) return;
    try {
      navigator.mediaSession.setPositionState({
        duration: this.duration / 1000,
        playbackRate: this.audio.playbackRate || 1,
        position: Math.min(this.duration, this.position) / 1000,
      });
    } catch { /* out-of-range during a seek */ }
  }

  /** @param {string} name */
  #emit(name) { this.dispatchEvent(new Event(name)); }

  /** Human readable "12:03 of 4:21:00" for live regions. */
  spokenPosition() {
    return `${clock(this.position)} of ${clock(this.duration)}`;
  }
}

export const player = new PlayerController();
