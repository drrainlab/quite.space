// @ts-check
// The surface a scene is allowed to touch.
//
// A scene never receives a canvas or a 2D context. It receives a *brush*: five
// drawing verbs, a palette it cannot extend, a seeded random source, and two
// numbers. Everything AM-3 promises about scenes follows from what is missing
// here rather than from a rule scenes are asked to obey:
//
//   no fillText, no drawImage, no path API   → a scene cannot render a glyph,
//                                              and so cannot forge system UI
//   no colour argument, only a palette index → a scene cannot invent white on
//                                              black to strobe with
//   no ctx, no canvas, no host element       → a scene cannot reach the page
//
// Say the boundary precisely, because the loose version is false: a scene runs
// in the same JavaScript realm as the app and *could* reach `fetch`, `window`
// or `localStorage` whatever we hand it. This is a reviewed implementation
// boundary, not a security sandbox. What makes it hold in practice is that
// scene modules are small, are read, and are checked mechanically —
// clients/web-ui/scenes_test.go rejects network, storage and DOM references
// inside them. The real sandbox question returns with renderer packs, which
// need their own ADR (ADR-013 invariant 2).
//
// THE PHOTOSENSITIVITY CAP LIVES HERE, and this is the only place it can
// honestly live. A universal runtime clamp would mean analysing finished
// frames — expensive, and still imperfect. So the brush tracks the mean
// luminance it is painting and refuses to move it faster than MAX_LUMA_STEP
// per frame: an op that would overshoot is drawn *fainter*, not dropped, so a
// scene degrades into calm rather than stuttering. The budget is measured
// against the luminance at the START of the frame, so a thousand small marks
// cannot collectively do what one big wash is forbidden to do.
//
// EVERY FRAME STARTS FROM THE TRUTH, not from what the brush thinks it did.
// Each frame opens by reading back an 8x8 downscale of the canvas — 64 pixels,
// a flat ~0.5ms whatever the canvas size, against a 33ms budget — and the
// frame's allowance is measured from that. Predicting instead was not merely
// less accurate, it was unsound: a canvas stores 8-bit channels, so darkening
// an already-dark picture by a hair rounds to no change at all, and a model
// that believed it had gone back to black would keep granting a fresh climb
// from a black the picture was not at. Measured, that cost a scene 0.055 of
// luminance in one frame against a 0.007 cap. Within a single frame the brush
// still predicts, because it cannot read back between marks, so it predicts
// PESSIMISTICALLY (see QUANT_SLOP) and the next frame re-measures regardless.
//
// What that is actually worth, measured against real pixels rather than
// asserted — seven scenes written to attack the cap, 150 frames each, on the
// most dangerous palette an author may legally write (black and white):
//
//   flashes per second   0 everywhere, against a WCAG limit of 3
//   per-frame step       0.007 for full-surface attacks, i.e. exactly the cap
//                        up to 0.023 for fine bright speckle on dark ground,
//                        where luminance-of-mean and mean-of-luminance part
//                        company — not a general flash, and it produced none
//   cost                 2.2ms of a 33ms frame for a realistic calm scene
//
// Because the model is still a model inside the frame, every built-in scene is
// additionally swept by scripts/atmosphere/flashcheck.cjs.

const BRUSH = (() => {
  /** A full-surface wash may never move the picture more than this at once. */
  const MAX_WASH_ALPHA = 0.12;
  /** Any single mark. Marks are small; this is a backstop, not the guard. */
  const MAX_INK_ALPHA = 0.6;
  /**
   * Mean luminance may not move more than this between two frames.
   *
   * DERIVED, not chosen. WCAG 2.3.1 calls a change of 0.10 or more a
   * transition, a pair of opposing transitions a flash, and three flashes in
   * any second the limit. With a per-frame ceiling of `s`, one transition
   * costs at least ceil(0.10 / s) frames, so at F frames per second the scene
   * cannot exceed F / ceil(0.10 / s) flashes per second. stage.js caps F at
   * 30, so merely staying at or under three would allow s = 0.0111.
   *
   * That is the compliance boundary, and sitting exactly on it is the wrong
   * place to sit for a photosensitivity floor: 0.011 measures 3/s in the
   * sweep — passing, with nothing to spare. 0.007 needs 15 frames a
   * transition, so 2/s, a whole flash of margin. It costs almost nothing,
   * because the cap only bites on full-surface change: dots, glows and lines
   * are area-limited and barely move the mean at all. What it buys is that an
   * error in the model still lands inside the standard rather than outside it.
   *
   * Changing this number without redoing that arithmetic breaks the guarantee
   * silently; scripts/atmosphere/flashcheck.cjs is what would catch it.
   */
  const MAX_LUMA_STEP = 0.007;
  /** Sum of alpha x coverage a scene may lay down in one frame. */
  const MAX_FRAME_COVERAGE = 2.0;
  /** A scene that asks for more marks than this is stopped for the frame. */
  const MAX_FRAME_OPS = 4000;
  /** No mark may be larger than this fraction of the shorter side. */
  const MAX_MARK_SPAN = 0.6;
  /** The readback used to start each frame from the truth. 64 pixels. */
  const METER = 8;
  /**
   * Re-measure mid-frame after this many marks.
   *
   * Inside a frame the brush must predict, and a prediction compounds: the
   * canvas rounds every composite to 8 bits, so a scene issuing hundreds of
   * marks a frame accumulates error in whichever direction the rounding
   * happens to go. Measured, that let one frame move twice its allowance.
   * An ordinary scene draws well under this and never pays; a scene that
   * draws enough marks to drift pays 0.5ms to be told where it really is.
   */
  const READ_EVERY_OPS = 256;
  /**
   * Half an 8-bit step: what one composite can gain to rounding, per channel.
   *
   * The canvas rounds every blend it performs, and the rounding is not
   * zero-mean when a scene keeps pushing the same direction — twenty faint
   * washes toward white measured twice their predicted movement. So each mark
   * is charged the slop it MIGHT gain rather than the movement it asked for,
   * which makes the within-frame model pessimistic instead of optimistic.
   *
   * Weighted by coverage, because the rounding happens per pixel: a mark that
   * touches a thousandth of the surface can only shift the mean by a
   * thousandth of a step. Without that weighting a scene drawing a few hundred
   * small marks would be charged for movement it never made and clamp itself
   * into stillness.
   */
  const QUANT_SLOP = 0.5;

  function clamp(v, lo, hi) { return v < lo ? lo : v > hi ? hi : v; }
  /** A scene that computes NaN gets the fallback, never a broken canvas. */
  function num(v, d) { return typeof v === 'number' && isFinite(v) ? v : d; }

  /**
   * sRGB channel to linear light, through a table.
   *
   * This is the hottest arithmetic in the client: every mark costs one
   * luminance evaluation, and a mark that hits the frame's ceiling costs
   * eleven more as the bisection converges — three Math.pow calls each. The
   * table removes the pow entirely. It took the headless sweep of six scenes
   * from 92 seconds to a length that will not get the test switched off, and
   * it is worth as much in the browser, where the same code runs per mark.
   *
   * 1024 entries with linear interpolation is accurate to about 1e-6, which
   * is four orders below MAX_LUMA_STEP — the model's own approximations are
   * far larger, and the per-frame readback corrects those anyway.
   */
  const LIN_N = 1024;
  const LIN = new Float64Array(LIN_N + 1);
  for (let i = 0; i <= LIN_N; i++) {
    const c = i / LIN_N;
    LIN[i] = c <= 0.04045 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
  }
  function lin(c) {
    // Callers pass channels that the pessimism term can push a hair outside
    // 0..255 before clamping; fold that in here rather than at every site.
    const u = c <= 0 ? 0 : c >= 255 ? LIN_N : (c / 255) * LIN_N;
    const i = u | 0;
    if (i >= LIN_N) return LIN[LIN_N];
    const f = u - i;
    return LIN[i] + (LIN[i + 1] - LIN[i]) * f;
  }

  /**
   * WCAG relative luminance of a #rrggbb colour, 0..1.
   * An unparseable colour reads as black — the safe direction, since the
   * flash rule cares about *changes* and black is where the palette starts.
   * @param {string} hex
   */
  function luminance(hex) {
    const m = /^#([0-9a-fA-F]{6})$/.exec(String(hex || ''));
    if (!m) return 0;
    const n = parseInt(m[1], 16);
    return 0.2126 * lin((n >> 16) & 255) + 0.7152 * lin((n >> 8) & 255) +
           0.0722 * lin(n & 255);
  }

  /**
   * A small, fast, seeded generator (SplitMix32).
   *
   * Scenes get this instead of Math.random because the recipe's seed is inside
   * a signature: a scene that reaches for global randomness is not the scene
   * that was signed, and "same recipe, same picture" quietly stops being true.
   * @param {number} seed
   */
  function rng(seed) {
    let s = (seed >>> 0) || 0x9e3779b9;
    return function next() {
      s = (s + 0x9e3779b9) >>> 0;
      let z = s;
      z = Math.imul(z ^ (z >>> 16), 0x21f0aaad);
      z = Math.imul(z ^ (z >>> 15), 0x735a2d97);
      return ((z ^ (z >>> 15)) >>> 0) / 4294967296;
    };
  }

  /**
   * Build a brush and the controller that drives it.
   *
   * The scene gets `surface` and nothing else. The stage keeps `begin`, `end`
   * and `luma` to itself — a scene that could call `begin` could reset its own
   * luminance budget, which is the whole guard.
   *
   * @param {object} o
   * @param {number} o.width  CSS pixels
   * @param {number} o.height CSS pixels
   * @param {string[]} o.palette #rrggbb tokens; index 0 is the ground
   * @param {number} [o.seed]
   * @param {CanvasRenderingContext2D|null} [o.ctx] null meters without painting
   * @param {(() => number)|null} [o.level] the bed's own loudness, 0..1
   */
  function make(o) {
    const W = Math.max(1, num(o.width, 1));
    const H = Math.max(1, num(o.height, 1));
    const area = W * H;
    const maxSpan = Math.min(W, H) * MAX_MARK_SPAN;
    const ctx = o.ctx || null;

    const pal = (o.palette && o.palette.length ? o.palette : ['#000000'])
      .map(h => (/^#[0-9a-fA-F]{6}$/.test(String(h)) ? String(h) : '#000000'));
    const palRGB = pal.map(h => {
      const n = parseInt(h.slice(1), 16);
      return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
    });
    // Each palette colour's own luminance, so admit() can tell which way a
    // mark would move the picture without recomputing it per mark.
    const palLuma = palRGB.map(c => lumaOf(c));

    // THE MODEL TRACKS MEAN sRGB CHANNELS, NOT MEAN LUMINANCE, and that is a
    // correctness requirement rather than a detail. A canvas composites alpha
    // in sRGB, so `rgba(255,255,255,0.01)` over black lands on sRGB 2.55 —
    // a relative luminance of 0.0008, not 0.01. Accounting in linear light
    // made the model believe a frame had moved eight times more than it had,
    // which let a scene painting thousands of faint marks drift across half
    // the luminance range while every frame looked, to the model, compliant.
    // Blending per channel in sRGB is exactly what the canvas does, so the
    // model and the picture now agree by construction.
    //
    // Luminance is then taken OF the mean colour, rather than as the mean of
    // per-pixel luminance, and resync() measures the same quantity so the two
    // never fight. The two coincide exactly for a spatially uniform frame —
    // which is precisely the full-screen flash the threshold exists to stop.
    // Where they differ is a picture of fine bright speckle on dark ground,
    // which is not a general flash at all.
    const ground = palRGB[0];
    const ch = ground.slice();
    const tmp = [0, 0, 0];
    let frameLuma = 0;
    let coverage = 0, ops = 0, frames = 0;
    let pendingRead = false;
    let meter = null; // 8x8 readback surface, built lazily

    /** Relative luminance of a mean colour. */
    function lumaOf(c) {
      return 0.2126 * lin(c[0]) + 0.7152 * lin(c[1]) + 0.0722 * lin(c[2]);
    }
    /**
     * Mean colour after painting `c` over `base` with total weight `w`, plus
     * the rounding this composite could gain over `f` of the surface. The
     * slop always points the way the mark is already moving, so the model
     * over-states movement and the brush admits a little less than it could.
     */
    function blend(base, c, w, out, f) {
      for (let k = 0; k < 3; k++) {
        const d = (c[k] - base[k]) * w;
        const slop = d === 0 ? 0 : Math.sign(d) * QUANT_SLOP * (f || 0);
        // Clamped because a real channel is: without this the pessimism could
        // walk the model past white and keep going, and a model that believes
        // in a brightness the screen cannot show stops being conservative and
        // starts being wrong.
        out[k] = clamp(base[k] + d + slop, 0, 255);
      }
    }
    frameLuma = lumaOf(ch);

    /**
     * Fold one alpha-over paint into the model and return the alpha actually
     * allowed. A mark of colour `c` covering fraction `f` at alpha `a` moves
     * the mean by weight w = a*f — exact for the mean, whatever the mark's
     * shape, since only its area enters.
     */
    function admit(f, a, c, lc) {
      if (ops >= MAX_FRAME_OPS || coverage >= MAX_FRAME_COVERAGE) return 0;
      if (pendingRead) { pendingRead = false; resync(); }
      f = clamp(f, 0, 1);
      a = clamp(a, 0, 1);
      if (f <= 0 || a <= 0) return 0;
      // Once the frame has spent its allowance, every further mark in the
      // SAME direction is refused outright. Without this the bisection below
      // still ran eleven times for each of them, only to converge on nothing:
      // a scene draws hundreds of marks a frame and hits the ceiling early,
      // so that was most of the work the sweep was doing. Marks going the
      // other way still have room and fall through.
      const cur = lumaOf(ch);
      const off = cur - frameLuma;
      if (lc > cur && off >= MAX_LUMA_STEP - 1e-9) return 0;
      if (lc < cur && off <= 1e-9 - MAX_LUMA_STEP) return 0;
      let w = a * f;
      blend(ch, c, w, tmp, f);
      if (Math.abs(lumaOf(tmp) - frameLuma) > MAX_LUMA_STEP) {
        // Paint what fits instead of nothing: fainter is a picture, skipped
        // is a flicker. Luminance is monotone in each channel and the blend
        // is monotone in w, so the largest admissible weight is a bisection
        // away — and only ops that actually hit the ceiling pay for it.
        let lo = 0, hi = w;
        // Eleven halvings put the weight within 5e-4 of the true boundary,
        // which is a thousandth of the step it is bounding.
        for (let k = 0; k < 11; k++) {
          const mid = (lo + hi) / 2;
          blend(ch, c, mid, tmp, f);
          if (Math.abs(lumaOf(tmp) - frameLuma) > MAX_LUMA_STEP) hi = mid; else lo = mid;
        }
        w = lo;
        if (w <= 1e-6) return 0;
        blend(ch, c, w, tmp, f);
      }
      ch[0] = tmp[0]; ch[1] = tmp[1]; ch[2] = tmp[2];
      coverage += w;
      ops++;
      // The alpha is returned before this, so the mark about to be painted is
      // not yet on the canvas — read back on the NEXT call instead, when it
      // is. `frameLuma` is untouched: the allowance still runs from the start
      // of the frame, only the belief about where we are gets corrected.
      if (ops % READ_EVERY_OPS === 0) pendingRead = true;
      return clamp(w / f, 0, 1);
    }

    /** @param {number} i */
    function colour(i) {
      const k = clamp(Math.floor(num(i, 0)), 0, pal.length - 1);
      return { hex: pal[k], rgb: palRGB[k], luma: palLuma[k] };
    }

    /** @param {string} hex @param {number} a */
    function rgba(hex, a) {
      const n = parseInt(hex.slice(1), 16);
      return `rgba(${(n >> 16) & 255},${(n >> 8) & 255},${n & 255},${a.toFixed(4)})`;
    }

    const surface = {
      /** Width in CSS pixels. */
      get width() { return W; },
      /** Height in CSS pixels. */
      get height() { return H; },
      /** How many colours this recipe offers. */
      get colours() { return pal.length; },
      /** Deterministic 0..1, seeded by the recipe. */
      rand: rng(num(o.seed, 1)),

      /**
       * How loud the post's OWN ambient bed is right now, 0..1.
       *
       * The one live input a scene gets, and the only one it will get. It is
       * PERFORMANCE, not recipe (see the honesty split): it never enters the
       * signed object, so a scene that leans on it is still the same scene
       * everywhere — it just sits calmer where there is no sound. Zero when
       * nothing is playing, which is the common case and must therefore look
       * deliberate rather than broken.
       *
       * Deliberately not the microphone, not another post's audio, and not
       * the system output: a scene that could hear the room would make every
       * page that carries one a listening device.
       */
      level() { return clamp(num(o.level ? o.level() : 0, 0), 0, 1); },

      /**
       * Lay a colour over the whole surface. The only full-surface verb, and
       * the reason full-screen high-contrast alternation is unavailable: its
       * alpha is capped low, so "black frame, white frame" cannot be written.
       * @param {number} colourIndex @param {number} alpha
       */
      wash(colourIndex, alpha) {
        const c = colour(colourIndex);
        const a = admit(1, clamp(num(alpha, 0), 0, MAX_WASH_ALPHA), c.rgb, c.luma);
        if (a <= 0 || !ctx) return;
        ctx.fillStyle = rgba(c.hex, a);
        ctx.fillRect(0, 0, W, H);
      },

      /**
       * A hard-edged disc.
       * @param {number} x @param {number} y @param {number} r
       * @param {number} colourIndex @param {number} alpha
       */
      dot(x, y, r, colourIndex, alpha) {
        r = clamp(num(r, 0), 0, maxSpan);
        if (r <= 0) return;
        const c = colour(colourIndex);
        const a = admit(Math.PI * r * r / area,
          clamp(num(alpha, 0), 0, MAX_INK_ALPHA), c.rgb, c.luma);
        if (a <= 0 || !ctx) return;
        ctx.fillStyle = rgba(c.hex, a);
        ctx.beginPath();
        ctx.arc(num(x, 0), num(y, 0), r, 0, Math.PI * 2);
        ctx.fill();
      },

      /**
       * A soft disc that falls off to nothing at the rim. Coverage counts as
       * a third of the disc, which is the volume under a linear falloff.
       * @param {number} x @param {number} y @param {number} r
       * @param {number} colourIndex @param {number} alpha
       */
      glow(x, y, r, colourIndex, alpha) {
        r = clamp(num(r, 0), 0, maxSpan);
        if (r <= 0) return;
        const c = colour(colourIndex);
        const a = admit(Math.PI * r * r / 3 / area,
          clamp(num(alpha, 0), 0, MAX_INK_ALPHA), c.rgb, c.luma);
        if (a <= 0 || !ctx) return;
        const cx = num(x, 0), cy = num(y, 0);
        const g = ctx.createRadialGradient(cx, cy, 0, cx, cy, r);
        g.addColorStop(0, rgba(c.hex, a));
        g.addColorStop(1, rgba(c.hex, 0));
        ctx.fillStyle = g;
        ctx.beginPath();
        ctx.arc(cx, cy, r, 0, Math.PI * 2);
        ctx.fill();
      },

      /**
       * A straight stroke. Scenes draw curves as many short strokes, which
       * keeps the verb list short and every mark's area computable.
       * @param {number} x0 @param {number} y0 @param {number} x1 @param {number} y1
       * @param {number} width @param {number} colourIndex @param {number} alpha
       */
      line(x0, y0, x1, y1, width, colourIndex, alpha) {
        const ax = num(x0, 0), ay = num(y0, 0), bx = num(x1, 0), by = num(y1, 0);
        const w = clamp(num(width, 1), 0, maxSpan);
        const len = Math.hypot(bx - ax, by - ay);
        if (w <= 0 || len <= 0) return;
        const c = colour(colourIndex);
        const a = admit(len * w / area,
          clamp(num(alpha, 0), 0, MAX_INK_ALPHA), c.rgb, c.luma);
        if (a <= 0 || !ctx) return;
        ctx.strokeStyle = rgba(c.hex, a);
        ctx.lineWidth = w;
        ctx.lineCap = 'round';
        ctx.beginPath();
        ctx.moveTo(ax, ay);
        ctx.lineTo(bx, by);
        ctx.stroke();
      },
    };

    /** Read back what is really on the canvas, cheaply: 64 pixels. */
    function resync() {
      if (!ctx || typeof document === 'undefined') return;
      try {
        if (!meter) {
          const m = document.createElement('canvas');
          m.width = m.height = METER;
          meter = m.getContext('2d', { willReadFrequently: true });
          if (!meter) return;
        }
        meter.clearRect(0, 0, METER, METER);
        meter.drawImage(ctx.canvas, 0, 0, METER, METER);
        const d = meter.getImageData(0, 0, METER, METER).data;
        let r = 0, g = 0, b = 0;
        for (let i = 0; i < d.length; i += 4) {
          // Unpainted pixels are transparent; the ground shows through them.
          const al = d[i + 3] / 255;
          r += d[i] * al + ground[0] * (1 - al);
          g += d[i + 1] * al + ground[1] * (1 - al);
          b += d[i + 2] * al + ground[2] * (1 - al);
        }
        // The same quantity the model tracks — mean sRGB — so a resync is a
        // correction, never an argument between two different measurements.
        const n = METER * METER;
        ch[0] = r / n; ch[1] = g / n; ch[2] = b / n;
      } catch { /* readback unavailable: the model carries on alone */ }
    }

    return {
      surface,
      /**
       * Open a frame. Measure first, then set the allowance from what is
       * actually on screen — the order is the guarantee.
       */
      begin() {
        resync();
        frameLuma = lumaOf(ch);
        coverage = 0;
        ops = 0;
        frames++;
      },
      /** Close a frame and report the mean luminance now on screen. */
      end() {
        return lumaOf(ch);
      },
      /** Current mean luminance, 0..1. */
      luma() { return lumaOf(ch); },
    };
  }

  return { make, luminance, rng, limits: {
    MAX_WASH_ALPHA, MAX_INK_ALPHA, MAX_LUMA_STEP,
    MAX_FRAME_COVERAGE, MAX_FRAME_OPS, MAX_MARK_SPAN,
  } };
})();

if (typeof window !== 'undefined') window.BRUSH = BRUSH;
// The flash harness runs this file under Node, with no canvas at all: passing
// ctx: null meters a scene without painting it.
if (typeof module !== 'undefined' && module.exports) module.exports = BRUSH;
