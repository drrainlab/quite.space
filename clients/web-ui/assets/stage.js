// @ts-check
// The first animation loop in this client, and deliberately the only one.
//
// Until now there was no requestAnimationFrame loop anywhere: the single rAF
// call in resonance.js is a one-shot layout-settle deferral, and every canvas
// in the app draws exactly one frame. That was a good property — a feed that
// never stops moving is a feed nobody can read — and atmosphere is the first
// thing that genuinely needs continuous drawing.
//
// So it gets ONE driver, with the limits written into the mechanism rather
// than trusted to each scene:
//
//   - one active scene at a time; starting a second stops the first
//   - a frame budget, so a scene cannot ask for more than 30fps
//   - delta time in seconds, so a slow machine plays slower rather than
//     stuttering through the same number of frames
//   - a real HIDE edge: hidden tab, offscreen element, or reduced motion all
//     stop the loop rather than merely declining to start it
//
// A scene is a plain object: { draw(ctx, t, dt), stop?() }. It gets a 2D
// context and two numbers. It is not handed the canvas, the document, or
// anything it could use to reach the rest of the page.

const STAGE = (() => {
  /** Never faster than this, whatever the display does. */
  const MAX_FPS = 30;
  const MIN_FRAME_MS = 1000 / MAX_FPS;
  /**
   * A budget enforced by strict rejection always rounds DOWN to the next
   * divisor of the display rate: on a 60Hz screen, 33.3ms rejects a 32ms gap
   * and the loop settles at every third frame — 20fps, not 30. The tolerance
   * lets an interval that is within a tenth of the budget count as meeting it,
   * so 60Hz gives every second frame and 120Hz every fourth: 30fps on both.
   */
  const FRAME_TOLERANCE = MIN_FRAME_MS * 0.1;
  /** A tab that was hidden for a while must not resume with a huge dt. */
  const MAX_DT = 0.1;

  let raf = 0;
  let current = null; // { scene, canvas, ctx, host, t, last, io }

  function running() { return raf !== 0; }

  /** Stop whatever is on stage. Safe to call at any time, including twice. */
  function clear() {
    if (raf) { cancelAnimationFrame(raf); raf = 0; }
    if (current) {
      const c = current;
      current = null;
      try { c.scene.stop?.(); } catch { /* a scene's failure is not ours */ }
      c.io?.disconnect();
      c.canvas.remove();
    }
  }

  function frame(now) {
    if (!current) { raf = 0; return; }
    const c = current;
    if (now - c.last < MIN_FRAME_MS - FRAME_TOLERANCE) { raf = requestAnimationFrame(frame); return; }
    const dt = Math.min(MAX_DT, (now - c.last) / 1000);
    c.last = now;
    c.t += dt;
    try {
      c.scene.draw(c.ctx, c.t, dt);
    } catch (err) {
      // A scene that throws is stopped, not retried sixty times a second.
      // The poster underneath stays: meaning may not degrade (ADR-013 §1).
      console.warn('atmosphere scene stopped:', err);
      clear();
      return;
    }
    raf = requestAnimationFrame(frame);
  }

  function resize(c) {
    // Cap the backing store: a scene on a 4K display should not quietly cost
    // four times what it costs elsewhere.
    const dpr = Math.min(2, window.devicePixelRatio || 1);
    const r = c.host.getBoundingClientRect();
    c.canvas.width = Math.max(1, Math.round(r.width * dpr));
    c.canvas.height = Math.max(1, Math.round(r.height * dpr));
    c.ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  }

  /**
   * Put a scene on stage inside host. Returns false when it declined to run,
   * which is a normal outcome and not an error: reduced motion, a hidden tab,
   * or the minimal render mode all mean "show the poster instead".
   *
   * @param {HTMLElement} host
   * @param {{draw:(ctx:CanvasRenderingContext2D,t:number,dt:number)=>void, stop?:()=>void}} scene
   */
  function play(host, scene) {
    clear();
    if (!host || !scene || typeof scene.draw !== 'function') return false;
    if (!allowed()) return false;

    const canvas = document.createElement('canvas');
    canvas.className = 'stage-canvas';
    canvas.setAttribute('aria-hidden', 'true');
    const ctx = canvas.getContext('2d', { alpha: true });
    if (!ctx) return false;
    host.prepend(canvas);

    const c = { scene, canvas, ctx, host, t: 0, last: performance.now(), io: null };
    current = c;
    resize(c);

    // Offscreen is the same as hidden. Scrolling a post out of view should
    // cost nothing, and on a long feed that is the common case.
    if (window.IntersectionObserver) {
      c.io = new IntersectionObserver(entries => {
        const visible = entries.some(e => e.isIntersecting);
        if (visible) { start(); } else { pause(); }
      }, { threshold: 0.01 });
      c.io.observe(host);
    }
    start();
    return true;
  }

  function start() {
    if (!current || raf || !allowed()) return;
    current.last = performance.now();
    raf = requestAnimationFrame(frame);
  }

  function pause() {
    if (raf) { cancelAnimationFrame(raf); raf = 0; }
  }

  /** May a scene move at all, right now? */
  function allowed() {
    if (document.hidden) return false;
    if (MODES.reducedMotion()) return false;
    if (typeof renderMode === 'function' && renderMode() === 'minimal') return false;
    return true;
  }

  // The hide edge, at last wired: this also sets body.hidden-tab, the CSS hook
  // that has been sitting in styles.css with nothing to set it.
  MODES.onVisibilityChange(hidden => { if (hidden) pause(); else start(); });
  // Turning reduced motion ON while a scene is playing must stop it, not wait
  // for the next navigation.
  MODES.onPreferenceChange(() => { if (!allowed()) clear(); });
  window.addEventListener('resize', () => { if (current) resize(current); });

  return { play, clear, running, allowed };
})();

if (typeof window !== 'undefined') window.STAGE = STAGE;
