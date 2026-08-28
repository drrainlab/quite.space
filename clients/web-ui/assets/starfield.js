// @ts-check
// A quiet sky behind dialogs and the conversation.
//
// The stars are painted ONCE into an offscreen canvas and handed to CSS as
// tiled background images. Everything after that is a single slow keyframe
// animation, so the effect costs no JavaScript per frame. That matters more
// here than in most projects: this client is meant to run on a Raspberry Pi
// and on a phone that might be someone's only radio for the evening, and a
// decorative canvas redrawing at 60 fps is a genuinely bad trade for a
// background.
//
// The sky lives in each element's OWN background rather than a pseudo-element,
// and that is a correctness requirement, not a preference — see the note in
// styles.css. Both surfaces would break the obvious version.
//
// It follows the same discipline as effects.js: the effective mode is the
// MINIMUM of what the person asked for, what accessibility asks for, what
// the render mode allows, and whether the tab is even visible.

const SKY = (() => {
  const KEY = 'qp.starfield';

  /** @returns {'off'|'still'|'drift'} */
  function userMode() {
    const v = localStorage.getItem(KEY);
    return v === 'off' || v === 'still' || v === 'drift' ? v : 'drift';
  }

  /**
   * The strongest mode allowed right now. Reduced motion keeps the stars but
   * stops them moving — a still sky is still a sky, and turning the whole
   * thing off would punish people who need calm rather than accommodate them.
   * @returns {'off'|'still'|'drift'}
   */
  function mode() {
    return MODES.resolve({
      ladder: ['off', 'still', 'drift'],
      user: userMode(),
      // Reduced motion keeps the stars and stops them — a still sky is still
      // a sky. Minimal drops the whole thing.
      floorWhenReduced: 'still',
      floorWhenMinimal: 'off',
    });
  }

  /**
   * One layer of stars as a tileable data URI.
   *
   * Tileable is the whole trick: a seamless tile means CSS can repeat it over
   * any size and drift it forever without us ever touching a pixel again.
   * Stars near an edge are drawn again on the opposite side so the seam does
   * not show.
   * @param {number} size @param {number} count @param {number} maxR @param {number} alpha
   */
  function layer(size, count, maxR, alpha) {
    const c = document.createElement('canvas');
    c.width = c.height = size;
    const g = c.getContext('2d');
    if (!g) return '';
    for (let i = 0; i < count; i++) {
      const x = Math.random() * size, y = Math.random() * size;
      const r = 0.4 + Math.random() * maxR;
      // A touch of colour variation: pure white stars read as dust.
      const warm = Math.random();
      const col = warm > 0.85 ? '190,205,255' : warm < 0.12 ? '255,230,200' : '255,255,255';
      const a = alpha * (0.35 + Math.random() * 0.65);
      for (const [dx, dy] of [[0, 0], [size, 0], [-size, 0], [0, size], [0, -size]]) {
        const px = x + dx, py = y + dy;
        if (px < -maxR || px > size + maxR || py < -maxR || py > size + maxR) continue;
        const grd = g.createRadialGradient(px, py, 0, px, py, r * 3);
        grd.addColorStop(0, `rgba(${col},${a})`);
        grd.addColorStop(0.4, `rgba(${col},${a * 0.35})`);
        grd.addColorStop(1, 'rgba(0,0,0,0)');
        g.fillStyle = grd;
        g.beginPath();
        g.arc(px, py, r * 3, 0, Math.PI * 2);
        g.fill();
      }
    }
    return c.toDataURL('image/png');
  }

  /**
   * The shimmer layer: a sparse scatter of small, soft points of light.
   *
   * Because everything is composited with `screen`, each little point
   * BRIGHTENS whatever star it happens to be passing over, then lets it go.
   * The points are barely wider than a star and drift faster than any star
   * layer, so what you see is individual stars catching light one at a time —
   * pointwise twinkling — rather than a cloud passing across the whole field.
   *
   * Real per-star animation would mean a canvas redrawing every frame or
   * hundreds of animated elements. This is one more tile.
   * @param {number} size @param {number} count
   */
  function sparkle(size, count) {
    const c = document.createElement('canvas');
    c.width = c.height = size;
    const g = c.getContext('2d');
    if (!g) return '';
    for (let i = 0; i < count; i++) {
      const x = Math.random() * size, y = Math.random() * size;
      // Faint enough to be invisible over empty sky, which is the point: on
      // its own this layer must read as nothing at all. It only becomes
      // visible where it lands on a star and `screen` adds the two together.
      // Too bright and the points turn into drifting smudges of their own.
      const r = 5 + Math.random() * 6;
      const a = 0.018 + Math.random() * 0.028;
      for (const [dx, dy] of [[0, 0], [size, 0], [-size, 0], [0, size], [0, -size]]) {
        const grd = g.createRadialGradient(x + dx, y + dy, 0, x + dx, y + dy, r);
        grd.addColorStop(0, `rgba(180,195,240,${a})`);
        grd.addColorStop(1, 'rgba(0,0,0,0)');
        g.fillStyle = grd;
        g.beginPath();
        g.arc(x + dx, y + dy, r, 0, Math.PI * 2);
        g.fill();
      }
    }
    return c.toDataURL('image/png');
  }

  let built = false;

  /** Paint the layers once and hand them to CSS as custom properties. */
  function build() {
    if (built) return;
    built = true;
    const root = document.documentElement.style;
    // Three depths, deliberately sparse and dim. The counts here are the whole
    // difference between "a night sky" and "a screensaver": few enough stars
    // that you notice them only when your eyes rest, faint enough that text
    // never competes with them.
    root.setProperty('--sky-far', `url("${layer(320, 26, 0.5, 0.26)}")`);
    root.setProperty('--sky-mid', `url("${layer(420, 16, 0.8, 0.34)}")`);
    root.setProperty('--sky-near', `url("${layer(560, 9, 1.2, 0.46)}")`);
    root.setProperty('--sky-glow', `url("${sparkle(260, 20)}")`);
  }

  /** Reflect the current mode on <html>, which is all the CSS needs. */
  function apply() {
    const m = mode();
    if (m !== 'off') build();
    document.documentElement.setAttribute('data-sky', m);
  }

  /** @param {'off'|'still'|'drift'} m */
  function set(m) {
    localStorage.setItem(KEY, m);
    apply();
    if (typeof syncSettingsUI === 'function') syncSettingsUI();
  }

  /** Give every dialog and the whole main stage a sky. The stage, not just
   *  the conversation: one continuous field runs under the chat AND under
   *  the info panel, so the panel's glass has something to shimmer over.
   *  Done once at load, and again for dialogs added later. */
  function paintSurfaces() {
    document.querySelectorAll('dialog').forEach(d => d.classList.add('sky'));
    const stage = document.querySelector('main');
    if (stage) stage.classList.add('sky');
  }

  document.addEventListener('DOMContentLoaded', () => { paintSurfaces(); apply(); });
  // A hidden tab paints nothing: the animation is paused in CSS, but the
  // attribute is refreshed on return in case the render mode changed.
  MODES.onVisibilityChange(hidden => { if (!hidden) apply(); });
  MODES.onPreferenceChange(apply);

  return { set, mode, userMode, apply };
})();

if (typeof window !== 'undefined') {
  window.SKY = SKY;
  window.setStarfield = (/** @type {'off'|'still'|'drift'} */ m) => SKY.set(m);
}
