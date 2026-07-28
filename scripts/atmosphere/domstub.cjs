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
      className: '', id: '', textContent: '', title: '',
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
      setAttribute() {}, getAttribute() { return null; },
      addEventListener() {}, removeEventListener() {},
      appendChild(c) { c.parentNode = el; el.children.push(c); return c; },
      prepend(c) { c.parentNode = el; el.children.unshift(c); return c; },
      remove() {
        if (!el.parentNode) return;
        const i = el.parentNode.children.indexOf(el);
        if (i >= 0) el.parentNode.children.splice(i, 1);
        el.parentNode = null;
      },
      getBoundingClientRect: () => ({ width: W, height: H, top: 0, left: 0 }),
      getContext: tag === 'canvas' ? function () { return makeCtx(el); } : undefined,
      querySelectorAll(sel) { return findAll(el, sel); },
      querySelector(sel) { return findAll(el, sel)[0] || null; },
    };
    return el;
  }

  /** Enough selector support for what the runtime actually asks: tag, .class. */
  function findAll(root, sel) {
    const want = String(sel).split(',').map(s => s.trim().replace(/^:scope\s*>\s*/, ''));
    const out = [];
    const walk = (n, depth) => {
      for (const c of n.children) {
        for (const w of want) {
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

  Object.assign(global, {
    document: doc,
    performance: { now: () => now },
    requestAnimationFrame(fn) { const id = nextRaf++; queue.set(id, fn); return id; },
    cancelAnimationFrame(id) { queue.delete(id); },
    localStorage: {
      getItem: k => (store.has(k) ? store.get(k) : null),
      setItem: (k, v) => store.set(k, String(v)),
      removeItem: k => store.delete(k),
    },
    matchMedia: () => ({ matches: false, addEventListener() {} }),
    devicePixelRatio: 1,
    addEventListener() {},
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
  });
  global.window = global;

  return {
    body,
    document: doc,
    /** How many elements are still being watched, across every observer. */
    observed() { let n = 0; for (const o of observers) n += o.seen.size; return n; },
    /** Advance the synthetic clock and run whatever rAF callbacks are due. */
    tick(ms) {
      now += ms;
      const q = queue; queue = new Map();
      for (const fn of q.values()) fn(now);
    },
    /** How many frame callbacks are outstanding. Zero means the loop stopped. */
    pending() { return queue.size; },
    now: () => now,
  };
}

module.exports = { install };
