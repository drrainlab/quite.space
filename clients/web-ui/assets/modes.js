// @ts-check
// One mode ladder, in one place.
//
// The same idea was written three times: effects.js has MODES/minMode for
// resonance effects, starfield.js has off/still/drift, and atmosphere needs a
// third. Each copy is small; the problem is that the RULE they encode is not
// small, and three copies of a rule drift.
//
// The rule: the effective mode is the MINIMUM of what the person asked for,
// what the space permits, what accessibility asks for, and what the client can
// do right now. Nothing may raise it — a space cannot make your screen move
// more than you allowed, and a preference cannot override reduced-motion.

const MODES = (() => {
  /**
   * Pick the weaker of two rungs on a ladder.
   * @param {readonly string[]} ladder weakest first
   * @param {string} a @param {string} b
   */
  function min(ladder, a, b) {
    const ia = ladder.indexOf(a), ib = ladder.indexOf(b);
    // An unrecognised rung is treated as the weakest, not ignored: a value
    // this build does not understand must never be allowed to mean "more".
    if (ia < 0) return ib < 0 ? ladder[0] : b;
    if (ib < 0) return a;
    return ladder[Math.min(ia, ib)];
  }

  /** True when the person has asked the system for less movement. */
  function reducedMotion() {
    return !!(window.matchMedia &&
      window.matchMedia('(prefers-reduced-motion: reduce)').matches);
  }

  /** True when the person has asked for less transparency. */
  function reducedTransparency() {
    return !!(window.matchMedia &&
      window.matchMedia('(prefers-reduced-transparency: reduce)').matches);
  }

  /**
   * Resolve a mode against the four inputs.
   *
   * `floorWhenReduced` and `floorWhenMinimal` are the strongest rung still
   * allowed under those conditions — not a replacement value. Callers pass a
   * rung name, and the ladder does the rest.
   *
   * @param {object} o
   * @param {readonly string[]} o.ladder weakest first
   * @param {string} o.user what the person chose
   * @param {boolean} [o.spaceAllows] false clamps straight to the weakest rung
   * @param {string} [o.floorWhenReduced] cap under prefers-reduced-motion
   * @param {string} [o.floorWhenMinimal] cap under the minimal render mode
   * @param {string} [o.floorWhenHidden] cap while the tab is in the background
   */
  function resolve(o) {
    const L = o.ladder;
    let m = L.includes(o.user) ? o.user : L[L.length - 1];
    // A space policy is a hard clamp rather than a minimum: "this space does
    // not do effects" is a statement about the space, not a preference to be
    // weighed against yours.
    if (o.spaceAllows === false) return L[0];
    if (o.floorWhenReduced && reducedMotion()) m = min(L, m, o.floorWhenReduced);
    if (o.floorWhenMinimal && typeof renderMode === 'function' &&
        renderMode() === 'minimal') {
      m = min(L, m, o.floorWhenMinimal);
    }
    // A rung, not a flag: effects drop to 'static' in a background tab while
    // a moving scene stops entirely. Both are "less", and they are not the
    // same less.
    if (o.floorWhenHidden && document.hidden) m = min(L, m, o.floorWhenHidden);
    return m;
  }

  /**
   * Call back whenever an accessibility preference changes.
   *
   * Only the starfield did this before, so everything else sampled the media
   * query once at render time and never noticed a person turning reduced
   * motion ON while the app was open — the moment it matters most.
   * @param {() => void} fn
   */
  function onPreferenceChange(fn) {
    if (!window.matchMedia) return;
    for (const q of ['(prefers-reduced-motion: reduce)',
                     '(prefers-reduced-transparency: reduce)']) {
      const mq = window.matchMedia(q);
      if (mq.addEventListener) mq.addEventListener('change', fn);
    }
  }

  /**
   * Call back on the HIDE edge and the RETURN edge.
   *
   * Every existing document.hidden site handled only the return. Nothing in
   * the client stopped when a tab went away — which is why `body.hidden-tab`
   * in styles.css was an orphaned hook that no JavaScript ever set.
   * @param {(hidden: boolean) => void} fn
   */
  function onVisibilityChange(fn) {
    document.addEventListener('visibilitychange', () => {
      document.body.classList.toggle('hidden-tab', document.hidden);
      fn(document.hidden);
    });
    document.body.classList.toggle('hidden-tab', document.hidden);
  }

  return { min, resolve, reducedMotion, reducedTransparency, onPreferenceChange, onVisibilityChange };
})();

if (typeof window !== 'undefined') window.MODES = MODES;
