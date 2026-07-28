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
// A scene is a plain object: { draw(brush, t, dt), stop?() }. It gets a brush
// (assets/brush.js) and two numbers — not a 2D context, not the canvas, not
// the document. Handing over the context was the obvious design and the wrong
// one: it is what would have made "no glyphs" and "no strobe" rules that
// scenes are asked to follow rather than facts about what they can express.
//
// The client keeps the last word (ADR-013 invariant 5). Four inputs, and the
// weakest wins:
//
//   poster  a still first frame and nothing more
//   calm    the scene runs, at half speed
//   full    the scene runs
//
// reduced motion, the minimal render mode and a hidden tab all floor at
// poster. The space's MotionPolicy caps it — read as the words mean, since
// `still` is literally no movement and `quiet` is the default every space
// starts with, so quiet must be gentle motion rather than none.

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
  /**
   * The largest backing store a scene may be drawn into, in pixels.
   * 640x400 in round numbers — see resize() for why this is a hard cap and
   * not a device-pixel-ratio multiplier.
   */
  const MAX_BACKING_PIXELS = 256000;

  const LADDER = ['poster', 'calm', 'full'];
  /** What each space MotionPolicy allows at most. */
  const POLICY_CAP = { still: 'poster', quiet: 'calm', lively: 'full' };
  /** How fast time runs on each rung. */
  const RATE = { poster: 0, calm: 0.5, full: 1 };

  let raf = 0;
  let current = null; // { scene, canvas, brush, host, t, last, io }
  /** Re-measures the host whenever it gains or changes layout — see below. */
  let ro = null;
  /** The current space's MotionPolicy, or '' when no space is in view. */
  let policy = '';

  function running() { return raf !== 0; }

  /** Stop whatever is on stage. Safe to call at any time, including twice. */
  function clear() {
    if (raf) { cancelAnimationFrame(raf); raf = 0; }
    userPaused = false; // the next scene must not inherit a frozen state
    if (current) {
      const c = current;
      current = null;
      try { c.scene.stop?.(); } catch { /* a scene's failure is not ours */ }
      c.io?.disconnect();
      if (ro) ro.unobserve(c.host);
      c.canvas.remove();
    }
  }

  function frame(now) {
    if (!current) { raf = 0; return; }
    const c = current;
    if (now - c.last < MIN_FRAME_MS - FRAME_TOLERANCE) { raf = requestAnimationFrame(frame); return; }
    // A slower rung slows the scene's own clock rather than dropping frames:
    // half as much movement, not half as smooth. The rate is cached rather
    // than resolved here — mode() reads localStorage and two media queries,
    // and thirty times a second on a Pi is not where that belongs.
    const dt = Math.min(MAX_DT, (now - c.last) / 1000) * c.rate;
    c.last = now;
    c.t += dt;
    try {
      c.brush.begin();
      c.scene.draw(c.brush.surface, c.t, dt);
      c.brush.end();
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
    const r = c.host.getBoundingClientRect();
    const w = Math.max(1, Math.round(r.width)), h = Math.max(1, Math.round(r.height));
    // CAP THE BACKING STORE, and cap it hard.
    //
    // The cost of a bed is fill rate, and fill rate is proportional to area
    // however the marks are arranged: holding brightness constant means total
    // painted coverage is a fixed FRACTION of the surface, so a surface with
    // ten times the pixels costs ten times as much. Measured, drift at
    // 1400x900 took 40ms a frame — past the whole 33ms budget — while the same
    // scene at 400x300 took 4ms.
    //
    // So the scene is drawn small and stretched by CSS. That is a free trade
    // here in a way it would not be for a photograph or for text: an ambient
    // bed is soft gradients with no edge anyone can focus on, and upscaling
    // soft gradients is invisible. What it buys is a bed that costs the same
    // on a phone, a laptop and a wall display, instead of one that quietly
    // becomes the most expensive thing on the page at 4K.
    const dpr = Math.min(2, window.devicePixelRatio || 1);
    let scale = dpr;
    if (w * h * scale * scale > MAX_BACKING_PIXELS) {
      scale = Math.sqrt(MAX_BACKING_PIXELS / (w * h));
    }
    c.canvas.width = Math.max(1, Math.round(w * scale));
    c.canvas.height = Math.max(1, Math.round(h * scale));
    // The scene keeps thinking in CSS pixels whatever the backing store does,
    // so a recipe means the same thing on every display.
    c.ctx.setTransform(scale, 0, 0, scale, 0, 0);
    // Setting width clears the canvas, so the brush starts from the ground
    // colour again — which is exactly what its luminance model should believe.
    c.brush = BRUSH.make({ width: w, height: h, palette: c.palette,
                           seed: c.seed, ctx: c.ctx, level: c.level });
  }

  /**
   * Put a scene on stage inside host. Returns false when it declined to run,
   * which is a normal outcome and not an error: reduced motion, a hidden tab,
   * the minimal render mode or a space that asked for stillness all mean
   * "show the poster instead".
   *
   * @param {HTMLElement} host
   * @param {{draw:(b:any,t:number,dt:number)=>void, stop?:()=>void}} scene
   * @param {{palette?:string[], seed?:number, level?:(()=>number)}} [opts]
   *   the recipe's own palette and seed, plus the bed's loudness if one is
   *   playing — the single live value a scene is allowed to see
   */
  function play(host, scene, opts) {
    clear();
    if (!host || !scene || typeof scene.draw !== 'function') return false;
    if (!allowed()) return false;

    const canvas = document.createElement('canvas');
    canvas.className = 'stage-canvas';
    canvas.setAttribute('aria-hidden', 'true');
    const ctx = canvas.getContext('2d', { alpha: true });
    if (!ctx) return false;
    host.prepend(canvas);

    const c = {
      scene, canvas, ctx, host, t: 0, last: performance.now(), io: null,
      brush: null, rate: 1,
      palette: (opts && opts.palette) || ['#000000'],
      seed: (opts && opts.seed) || 0,
      level: (opts && opts.level) || null,
    };
    current = c;
    resize(c);
    if (ro) ro.observe(host);

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

  /**
   * True while the PERSON paused the scene. Distinct from the mechanical
   * pauses (hidden tab, offscreen host): those end on their own — the
   * IntersectionObserver and the visibility handler both call start() when
   * their condition lifts — and without this flag they would quietly resume
   * a scene the person had explicitly frozen. A person's pause outranks
   * every automatic resume, and only resume() clears it.
   */
  let userPaused = false;

  function start() {
    if (!current || raf || userPaused || !allowed()) return;
    current.rate = RATE[mode()] || 0;
    current.last = performance.now();
    raf = requestAnimationFrame(frame);
  }

  /**
   * Re-resolve the ladder after something that could have changed it — a
   * settings row, a policy, a media query. Stops a scene that is no longer
   * allowed and re-rates one that is.
   */
  function apply() {
    if (!current) return;
    if (!allowed()) { clear(); return; }
    current.rate = RATE[mode()] || 0;
  }

  function pause() {
    if (raf) { cancelAnimationFrame(raf); raf = 0; }
  }

  /**
   * Freeze the scene ON ITS CURRENT FRAME. The canvas keeps what it holds;
   * nothing is redrawn — a pause that jumped back to the opening frame
   * would read as the composition resetting, which is an exit, not a pause.
   * Survives tab hides, host scrolling and preference re-applies until
   * userResume() lifts it.
   */
  function userPause() {
    if (!current) return;
    userPaused = true;
    pause();
  }

  /** Continue from exactly where userPause froze. */
  function userResume() {
    userPaused = false;
    start();
  }

  /** Is the person's pause in force? */
  function paused() { return userPaused; }

  /** @returns {'off'|'poster'|'calm'|'full'} what the person chose. */
  function userMode() {
    const v = localStorage.getItem('qp.atmosphere');
    return v === 'off' || v === 'poster' || v === 'calm' ? v : 'full';
  }

  /**
   * The strongest rung allowed right now: the weakest of what the person
   * chose, what the space permits, what accessibility asks for and what the
   * client can do. 'off' is not a rung — it means no atmosphere at all, not
   * even a poster — so it resolves here to 'poster' and the caller reads
   * `wanted()` to decide whether to render anything.
   * @returns {'poster'|'calm'|'full'}
   */
  function mode() {
    const m = MODES.resolve({
      ladder: LADDER,
      user: userMode() === 'off' ? 'poster' : userMode(),
      floorWhenReduced: 'poster',
      floorWhenMinimal: 'poster',
      floorWhenHidden: 'poster',
    });
    // The space's policy is a cap on the rung, not a preference weighed
    // against yours: nothing here can make a scene move MORE than you allowed.
    return /** @type {'poster'|'calm'|'full'} */ (
      MODES.min(LADDER, m, POLICY_CAP[policy] || 'full'));
  }

  /** False when the person has turned atmosphere off entirely. */
  function wanted() { return userMode() !== 'off'; }

  /**
   * Tell the stage which space is in view. `MotionPolicy` has been crossing
   * the wire since SC-0 (node/composition.go) and been read by nothing;
   * atmosphere is its first consumer.
   * @param {string} p quiet | still | lively
   */
  function setPolicy(p) {
    const next = POLICY_CAP[p] ? p : '';
    if (next === policy) return;
    policy = next;
    // A space that asks for stillness must stop what is already moving, not
    // take effect at the next navigation.
    apply();
  }

  /** @param {'off'|'poster'|'calm'|'full'} m */
  function set(m) {
    localStorage.setItem('qp.atmosphere', m);
    apply();
    if (typeof syncSettingsUI === 'function') syncSettingsUI();
  }

  /** May a scene move at all, right now? */
  function allowed() {
    if (document.hidden) return false;
    if (!wanted()) return false;
    return mode() !== 'poster';
  }

  // The hide edge, at last wired: this also sets body.hidden-tab, the CSS hook
  // that has been sitting in styles.css with nothing to set it.
  MODES.onVisibilityChange(hidden => { if (hidden) pause(); else start(); });
  // Turning reduced motion ON while a scene is playing must stop it, not wait
  // for the next navigation.
  MODES.onPreferenceChange(apply);
  // Watching the WINDOW is not enough, and the gap is not exotic: a host is
  // measured the moment play() is called, and at that moment it may have no
  // layout at all — an article still display:none, a pane the browser has
  // collapsed, a column that resizes when the sidebar opens. A host with no
  // layout hands back a zero rectangle and the scene gets a 1x1 canvas
  // stretched across the region, which looks like a broken scene rather than
  // a mis-measured one. Observing the host itself covers every case,
  // including the window resize that used to be handled alone.
  ro = window.ResizeObserver ? new ResizeObserver(() => {
    if (current) resize(current);
  }) : null;
  if (!ro) window.addEventListener('resize', () => { if (current) resize(current); });

  return { play, clear, running, allowed, mode, wanted, apply, set,
           setPolicy, userMode,
           pause: userPause, resume: userResume, paused };
})();

if (typeof window !== 'undefined') {
  window.STAGE = STAGE;
  window.setAtmosphere = (/** @type {'off'|'poster'|'calm'|'full'} */ m) => STAGE.set(m);
}
