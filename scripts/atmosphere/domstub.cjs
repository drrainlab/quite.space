'use strict';
// Just enough browser to run the atmosphere runtime headlessly.
//
// This exists to make the teardown and audio-arbitration contracts into a
// TEST rather than something re-checked by hand in a browser every time.
// Those two are infrastructure now — the registry teardown and the arbiter
// will be used by surfaces that have not been written yet — and a contract
// nobody asserts is a contract that quietly stops holding.
//
// WHAT THIS STUB IS FOR, AND WHAT IT IS NOT.
//
// It answers questions about BOOKKEEPING: was the canvas removed, was stop()
// called, was the observer disconnected, who holds the audio floor, does a
// second scene displace the first. Every one of those is decided by our own
// code and needs nothing from a real browser to be true.
//
// It cannot answer questions about PIXELS or SOUND. getImageData returns
// zeros, nothing is composited, no audio is decoded. So it is deliberately
// not used for anything about brightness, flashing or loudness — those are
// measured against real pixels in a real browser, and flashcheck.cjs sweeps
// the scenes with no canvas at all. A stub that tried to answer them would
// mostly be testing itself.

function makeCtx(canvas) {
  const noop = () => {};
  return {
    canvas,
    fillStyle: '', strokeStyle: '', lineWidth: 1, lineCap: '',
    fillRect: noop, clearRect: noop, beginPath: noop, arc: noop, fill: noop,
    stroke: noop, moveTo: noop, lineTo: noop, setTransform: noop,
    drawImage: noop,
    createRadialGradient: () => ({ addColorStop: noop }),
    getImageData: (x, y, w, h) => ({ data: new Uint8ClampedArray(w * h * 4) }),
  };
}

/** @param {{width?:number, height?:number}} [size] the rect every element reports */
function install(global, size) {
  const W = (size && size.width) || 640;
  const H = (size && size.height) || 360;
  const listeners = new Map();

  function makeEl(tag) {
    const el = {
      tagName: String(tag).toUpperCase(),
      id: '', textContent: '', title: '',
      style: { cssText: '', setProperty() {}, removeProperty() {} },
      dataset: {},
      children: [], parentNode: null,
      width: 0, height: 0,
      classList: {
        _s: new Set(),
        add(c) { this._s.add(c); }, remove(c) { this._s.delete(c); },
        toggle(c, on) { on ? this._s.add(c) : this._s.delete(c); },
        contains(c) { return this._s.has(c); },
      },
      // Attributes are real state. They were no-ops, and a no-op getAttribute
      // does not fail a test — it returns null, which reads as "the element
      // does not have it", so code that depends on one looks correct and does
      // nothing. That is the shape of a stub that tests itself.
      attrs: Object.create(null),
      setAttribute(k, v) { el.attrs[k] = String(v); },
      getAttribute(k) { return k in el.attrs ? el.attrs[k] : null; },
      removeAttribute(k) { delete el.attrs[k]; },
      addEventListener() {}, removeEventListener() {},
      appendChild(c) {
        // A fragment is SPREAD by whoever receives it — that is what makes it
        // a fragment rather than a wrapper, and a stub that nested it instead
        // would report a tree one level deeper than the browser builds.
        if (c && c.tagName === '#FRAGMENT') {
          for (const g of c.children.slice()) { g.parentNode = el; el.children.push(g); }
          c.children = [];
          return c;
        }
        c.parentNode = el; el.children.push(c); return c;
      },
      append(...cs) { for (const c of cs) el.appendChild(c); },
      // Only `innerHTML = ''` is modelled, because that is the only form the
      // client uses — as "empty this". Leaving it unimplemented was worse
      // than absent: assignments silently did nothing, so a rebuilt bar kept
      // every button it had ever drawn and a test clicking one by its label
      // could reach a stale render.
      set innerHTML(v) {
        if (String(v) !== '') throw new Error('domstub: innerHTML only models clearing');
        for (const c of el.children) c.parentNode = null;
        el.children = [];
      },
      get innerHTML() { return ''; },
      prepend(c) { c.parentNode = el; el.children.unshift(c); return c; },
      remove() {
        if (!el.parentNode) return;
        const i = el.parentNode.children.indexOf(el);
        if (i >= 0) el.parentNode.children.splice(i, 1);
        el.parentNode = null;
      },
      // `rect` is settable so a test can lay elements out along the page —
      // which is the only way to assert "which stage is the reader in", a
      // question that is entirely about geometry.
      rect: null,
      getBoundingClientRect() {
        return Object.assign({ width: W, height: H, top: 0, left: 0 }, el.rect || {});
      },
      getContext: tag === 'canvas' ? function () { return makeCtx(el); } : undefined,
      querySelectorAll(sel) { return findAll(el, sel); },
      querySelector(sel) { return findAll(el, sel)[0] || null; },
    };
    // className and classList must be the SAME state: real code assigns
    // el.className = 'x' and later queries by class, and a stub where those
    // are two stores makes class selectors silently blind.
    Object.defineProperty(el, 'className', {
      get() { return [...el.classList._s].join(' '); },
      set(v) { el.classList._s = new Set(String(v).split(/\s+/).filter(Boolean)); },
    });
    if (String(tag).toLowerCase() === 'img') {
      // An image decodes asynchronously, and the sequence renderer depends on
      // that: it swaps plates on `load`, so a stub that reported a picture as
      // present the instant src was assigned would test a path the browser
      // never takes.
      let src = '';
      el.complete = false;
      el.naturalWidth = 0;
      el.onload = null;
      Object.defineProperty(el, 'src', {
        get() { return src; },
        set(v) {
          src = String(v);
          el.complete = false;
          el.naturalWidth = 0;
          global.requestAnimationFrame(() => {
            if (el.src !== src) return; // superseded before it arrived
            el.complete = true;
            el.naturalWidth = 1;
            if (el.onload) el.onload();
          });
        },
      });
    }
    return el;
  }

  /** Enough selector support for what the runtime asks: tag, .class, [attr]. */
  function findAll(root, sel) {
    const want = String(sel).split(',').map(s => s.trim().replace(/^:scope\s*>\s*/, ''));
    const out = [];
    const walk = (n, depth) => {
      for (const c of n.children) {
        for (const w of want) {
          const attr = /^\[([a-z-]+)\]$/.exec(w);
          if (attr) {
            // [data-block-id] → dataset.blockId
            const key = attr[1].replace(/^data-/, '')
              .replace(/-([a-z])/g, (_, ch) => ch.toUpperCase());
            if (c.dataset[key] !== undefined) { out.push(c); break; }
            continue;
          }
          const [tag, cls] = w.includes('.') ? w.split('.') : [w, ''];
          const tagOK = !tag || c.tagName === tag.toUpperCase();
          const clsOK = !cls || c.classList.contains(cls);
          if (tagOK && clsOK) { out.push(c); break; }
        }
        walk(c, depth + 1);
      }
    };
    walk(root, 0);
    return out;
  }

  const body = makeEl('body');
  const doc = {
    hidden: false,
    body,
    createElement: makeEl,
    // A fragment is a parentless bag of children that vanishes into whatever
    // it is appended to. Modelled as an element whose appendChild is spread
    // by the receiver — enough for code that builds a tree and hands it over.
    createDocumentFragment: () => makeEl('#fragment'),
    createTextNode: (t) => { const n = makeEl('#text'); n.textContent = String(t); return n; },
    getElementById: () => null,
    querySelectorAll: sel => findAll(body, sel),
    querySelector: sel => findAll(body, sel)[0] || null,
    addEventListener(type, fn) { listeners.set(type, fn); },
    dispatch(type) { const fn = listeners.get(type); if (fn) fn(); },
  };

  // A synthetic clock, so a "second" of animation costs no wall time and the
  // frame budget can be asserted exactly instead of approximately.
  let now = 0;
  let nextRaf = 1;
  let queue = new Map();

  const store = new Map();
  const observers = new Set();
  const audios = [];
  // Timers ride the same synthetic clock as frames, so "no two background
  // changes closer together than N milliseconds" is an assertion rather than
  // a sleep.
  let nextTimer = 1;
  const timers = new Map(); // id -> {at, fn}
  const winListeners = new Map(); // type -> Set<fn>

  Object.assign(global, {
    document: doc,
    innerHeight: H,
    performance: { now: () => now },
    requestAnimationFrame(fn) { const id = nextRaf++; queue.set(id, fn); return id; },
    cancelAnimationFrame(id) { queue.delete(id); },
    setTimeout(fn, ms) {
      const id = nextTimer++;
      timers.set(id, { at: now + (Number(ms) || 0), fn });
      return id;
    },
    clearTimeout(id) { timers.delete(id); },
    localStorage: {
      getItem: k => (store.has(k) ? store.get(k) : null),
      setItem: (k, v) => store.set(k, String(v)),
      removeItem: k => store.delete(k),
    },
    matchMedia: () => ({ matches: false, addEventListener() {} }),
    devicePixelRatio: 1,
    addEventListener(type, fn) {
      if (!winListeners.has(type)) winListeners.set(type, new Set());
      winListeners.get(type).add(fn);
    },
    removeEventListener(type, fn) {
      const s = winListeners.get(type);
      if (s) s.delete(fn);
    },
    ResizeObserver: class {
      constructor(fn) { this.fn = fn; this.seen = new Set(); observers.add(this); }
      observe(el) { this.seen.add(el); }
      unobserve(el) { this.seen.delete(el); }
      disconnect() { this.seen.clear(); observers.delete(this); }
    },
    IntersectionObserver: class {
      constructor() {}
      observe() {} unobserve() {} disconnect() {}
    },
    console: { warn() {}, error() {}, log() {} },
    // Enough <audio> to answer bookkeeping questions — WAS one created, and
    // WITH WHICH url. Nothing is decoded and nothing sounds; see the note at
    // the top about what this stub is and is not for.
    Audio: class {
      constructor() {
        this.src = ''; this.volume = 1; this.loop = false;
        // `paused` starts true and `currentTime` at zero, as a real element
        // does — and play() clears the flag rather than only setting another,
        // because "did this ever play" and "is it playing NOW" are different
        // questions and a pause/resume test needs the second one.
        this.paused = true; this.currentTime = 0; this.played = false;
        audios.push(this);
      }
      play() { this.played = true; this.paused = false; return Promise.resolve(); }
      pause() { this.paused = true; }
      addEventListener() {} removeEventListener() {}
    },
  });
  global.window = global;

  return {
    body,
    document: doc,
    /** How many elements are still being watched, across every observer. */
    observed() { let n = 0; for (const o of observers) n += o.seen.size; return n; },
    /** Every <audio> element built so far, oldest first. */
    audios() { return audios; },
    /** Advance the synthetic clock and run whatever is due — frames, then timers. */
    tick(ms) {
      now += ms;
      const q = queue; queue = new Map();
      for (const fn of q.values()) fn(now);
      for (const [id, t] of [...timers]) {
        if (t.at <= now) { timers.delete(id); t.fn(); }
      }
    },
    /** How many frame callbacks are outstanding. Zero means the loop stopped. */
    pending() { return queue.size; },
    /** How many timers are still armed. */
    timers() { return timers.size; },
    /** Fire a window event, the way a scroll or a resize would. */
    fire(type) { for (const fn of winListeners.get(type) || []) fn(); },
    /** How many window listeners are registered for a type. */
    listening(type) { return (winListeners.get(type) || new Set()).size; },
    now: () => now,
  };
}

module.exports = { install };
