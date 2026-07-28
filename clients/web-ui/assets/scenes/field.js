// One engine. Six atmospheres.
//
// AM-4 made the count conditional and the condition binding: six scenes only
// if they are six PARAMETERISATIONS of one engine — same draw loop, same
// parameter vocabulary, same budget — and otherwise three calm plus one
// audio-reactive. Six half-tuned engines is how a frame budget and a
// photosensitivity cap both get quietly negotiated away.
//
// So there is one engine, and the presets below are points in its parameter
// space rather than six pieces of code. The trick that makes that honest is
// that the flow field is a WEIGHTED SUM OF VECTORS, not a switch:
//
//     v = swirl * (procedural turbulence)
//       + stream * (one constant direction)
//       + orbit  * (rotation about the centre)
//
// Drift, tide, orbit, dust and ember are not different behaviours; they are
// different weights on those three terms. Blending as vectors and taking the
// heading at the end matters — averaging ANGLES would make a north-east flow
// out of a north and an east one at some weights and a south-west one at
// others, which is the sort of thing that gets "fixed" by adding a special
// case per scene, and then there are six engines again.
//
// Everything is drawn through the brush (assets/brush.js), so none of this
// can strobe, render a glyph, or reach the page. clients/web-ui/scenes_test.go
// enforces that mechanically, and scripts/atmosphere/flashcheck.cjs sweeps
// every preset at every corner of its parameter space.

(function () {
  const TAU = Math.PI * 2;

  /** Agents never exceed this, whatever `density` asks for. */
  const MAX_AGENTS = 420;
  /** Steps taken before the first frame, so a scene opens settled. */
  const WARMUP = 60;
  /**
   * The surface these presets were tuned against, in pixels.
   *
   * A mark's contribution to the picture is its area over the SURFACE's area,
   * so a fixed number of fixed-size motes gets fainter as the canvas grows —
   * the same post would be a soft glow on a phone and almost nothing on a
   * desktop. Measured: tuned at 420x260, the same preset on 800x600 fell to a
   * fifteenth of its brightness and read as an empty rectangle.
   *
   * The fix is to scale the picture rather than the population: marks, speed
   * and the field's spatial frequency all scale with a power of area / this,
   * which holds brightness roughly constant at ZERO extra cost, because the
   * number of marks never changes. Scaling the agent count instead would have
   * made a large display pay several times the frame budget for the same
   * picture.
   *
   * The exponent is a plain 0.5 — r proportional to sqrt(area) makes a mark's
   * r^2/area coverage cancel exactly — and it is worth recording that this was
   * NOT true before stage.js capped the backing store. Measured against real
   * pixels then, brightness still fell as area^-0.36 and the fit wanted 0.68,
   * because a glow of radius 1.5px does not cover the pi*r^2 the geometry
   * bills it for: antialiasing spreads it wider, so the small surface — the
   * one full of small marks — came out brighter than its arithmetic.
   *
   * Capping the backing store removed that: every surface now rasterises at
   * roughly the same device scale, so marks land in the same size regime
   * everywhere and the geometry is honest again. Re-fitting after the cap
   * gave 0.51. That is a satisfying result rather than a lucky one — it says
   * the correction had been standing in for a rasterisation artefact, and
   * once the artefact was gone so was the need to fudge the exponent.
   */
  const REF_AREA = 400 * 300;
  const SCALE_EXP = 0.5;

  /**
   * Cheap deterministic turbulence: three sines that do not share a period.
   *
   * Not real curl noise, and it does not need to be — at the spatial scale an
   * ambient bed uses, the visible difference is nil and the cost is a tenth.
   * The phases come from the recipe's seed, so two posts with the same scene
   * and different seeds get different weather rather than the same picture.
   */
  function turbulence(x, y, t, ph) {
    return Math.sin(x + ph[0] + t * 0.11) * Math.cos(y * 1.31 + ph[1] - t * 0.07)
         + Math.sin((x + y) * 0.67 + ph[2] + t * 0.05) * 0.5;
  }

  /**
   * The engine. `env.params` are already 0..1 (scenes.js divides the recipe's
   * permille exactly once, so "the parameter is bounded" stays true here
   * without every scene remembering it).
   */
  function make(env) {
    const p = env.params;
    const count = Math.round(24 + p.density * (MAX_AGENTS - 24));
    const speed = 6 + p.pace * 110;          // pixels a second
    const scale = 0.0008 + p.scale * 0.0075; // spatial frequency of the field
    const dir = p.direction * TAU;
    const dirX = Math.cos(dir), dirY = Math.sin(dir);
    const fade = 0.012 + p.fade * 0.075;
    const bloom = p.bloom;
    const grain = p.grain;
    const tone = p.tone;
    const reactive = p.reactive;

    let agents = null;      // built on the first frame: only the brush knows the size
    let ph = [0, 0, 0];

    /** @param {any} b */
    function spawn(b) {
      ph = [b.rand() * TAU, b.rand() * TAU, b.rand() * TAU];
      const list = new Array(count);
      for (let i = 0; i < count; i++) {
        list[i] = {
          x: b.rand() * b.width,
          y: b.rand() * b.height,
          px: 0, py: 0,
          // Per-agent size and colour, fixed at birth so a mote keeps its
          // identity instead of shimmering colour every frame.
          size: 0.4 + b.rand() * (0.6 + grain * 2.6),
          hue: 1 + Math.floor(b.rand() * Math.max(1, b.colours - 1)),
          life: b.rand(),
          // A FINITE LIFE, and it is what makes this a field rather than a
          // smear. A flow field has convergence zones; agents that live
          // forever all end up in them, and what you see is a few thick
          // ribbons pressed against one edge with the rest of the frame
          // empty — measured by looking at it, after everything numeric said
          // the scene was fine. Retiring each agent after a couple of seconds
          // and starting it somewhere new keeps the population spread without
          // any force pushing it around, and costs one comparison per agent.
          //
          // Ages start scattered across the range rather than at zero, so the
          // whole field does not blink out and refill in unison.
          age: b.rand(),
          span: 1.4 + b.rand() * 2.6, // seconds
        };
      }
      for (const a of list) { a.px = a.x; a.py = a.y; }
      return list;
    }

    /** Heading at a point: the three terms, summed as vectors. */
    function heading(a, x, y, t, cx, cy, k) {
      const n = turbulence(x * scale / k, y * scale / k, t, ph);
      let vx = p.swirl * Math.cos(n * Math.PI);
      let vy = p.swirl * Math.sin(n * Math.PI);
      vx += p.stream * dirX;
      vy += p.stream * dirY;
      const ox = x - cx, oy = y - cy;
      const r = Math.hypot(ox, oy) || 1;
      vx += p.orbit * (-oy / r);
      vy += p.orbit * (ox / r);
      const m = Math.hypot(vx, vy);
      if (m < 1e-6) return null; // all three weights are zero: hold still
      a.vx = vx / m;
      a.vy = vy / m;
      return a;
    }

    return {
      /**
       * @param {any} b the brush — the only surface this scene can touch
       * @param {number} t seconds since the scene started
       * @param {number} dt seconds since the last frame
       */
      draw(b, t, dt) {
        if (!agents) {
          agents = spawn(b);
          // Open settled rather than from an empty field: step the simulation
          // without drawing, so the first frame — which is also what a still
          // marker shows — has the scene's real texture in it.
          for (let k = 0; k < WARMUP; k++) step(b, t, 1 / 30, false);
        }
        step(b, t, dt, true);
      },

      stop() { agents = null; },
    };

    function step(b, t, dt, paint) {
      const W = b.width, H = b.height;
      const cx = W / 2, cy = H / 2;
      // Resolution independence: the same picture, scaled. See REF_AREA.
      const k = Math.pow((W * H) / REF_AREA, SCALE_EXP) || 1;
      // The bed's loudness lifts pace and brightness. At zero — no sound, or
      // sound the person declined — this is simply a calm scene, which is why
      // every preset stays worth looking at silently.
      const lift = reactive > 0 ? 1 + reactive * b.level() * 2.2 : 1;

      // Fade the previous frame. This is the only full-surface mark, and the
      // brush caps it far below anything that could read as a flash.
      if (paint) b.wash(0, fade);

      for (const a of agents) {
        a.px = a.x;
        a.py = a.y;
        a.age += dt;
        if (a.age > a.span) {
          // Retire and respawn. px/py move with it so the frame does not get
          // a streak drawn clean across it from the old place to the new one.
          a.age = 0;
          a.x = b.rand() * W;
          a.y = b.rand() * H;
          a.px = a.x;
          a.py = a.y;
        } else if (heading(a, a.x, a.y, t, cx, cy, k)) {
          a.x += a.vx * speed * k * lift * dt;
          a.y += a.vy * speed * k * lift * dt;
        }
        // Wrap rather than reflect: a bed should have no edges to notice.
        if (a.x < 0) { a.x += W; a.px = a.x; }
        else if (a.x > W) { a.x -= W; a.px = a.x; }
        if (a.y < 0) { a.y += H; a.py = a.y; }
        else if (a.y > H) { a.y -= H; a.py = a.y; }

        if (!paint) continue;

        // `tone` slides the whole field between the palette's first accent and
        // its last, and `life` spreads each agent around that point, so a
        // scene reads as one colour with variation rather than a fruit salad.
        // Clamped at BOTH ends. Without the lower one, a low `tone` with a
        // high `grain` produced index 0 — the ground — so a slice of every
        // frame's marks were painted in the background colour and simply did
        // not exist. Measured at 1200 of 34000 marks on the default preset.
        const span = Math.max(1, b.colours - 1);
        const idx = 1 + Math.max(0, Math.min(span - 1,
          Math.floor((tone * (span - 1)) + (a.life - 0.5) * grain * span)));
        // TUNED AGAINST THE EQUILIBRIUM, not picked. Each frame the wash pulls
        // the picture toward the ground by `fade` while the marks push it back
        // by roughly (agents x coverage x alpha), so the brightness a scene
        // settles at is the ratio of the two — it has nothing to do with how
        // bright any single mark looks. The first values here were about
        // twenty times too much ink: every preset saturated into a bright fog
        // within seconds, and the picture you actually saw was the safety cap
        // refusing to let it get brighter. A bed whose look is decided by the
        // photosensitivity floor is not a bed anyone designed.
        //
        // Measured over 30 seconds at these values, all six presets settle
        // 15-29 sRGB above the ground with the cap engaging on 0% of frames —
        // so the floor is present and silent, which is what a floor should be.
        // TUNED AGAINST THE EQUILIBRIUM, then confirmed by eye with text on
        // top — which is the only question a bed has to answer.
        //
        // Each frame the wash pulls the picture toward the ground by `fade`
        // while the marks push it back by roughly (agents x coverage x alpha),
        // so the brightness a scene settles at is the RATIO of those two and
        // has nothing to do with how bright any single mark looks. The first
        // attempt was about twenty times too much ink: every preset saturated
        // into a fog within seconds and what you saw was the safety cap
        // refusing to let it get brighter. A bed whose look is decided by the
        // photosensitivity floor is not a bed anyone designed.
        //
        // A later pass raised these fourfold after a contact sheet of all six
        // scenes looked black — that was the contact sheet lying, not the
        // numbers: six panels downscaled into one screenshot washes a dark
        // soft field out completely. Rendered full size with real text over
        // it, the original values were already right and fourfold was far too
        // loud to read through. Hence roughly where they started.
        //
        // Measured at these values every preset settles 10-25 sRGB above the
        // ground with the cap engaging on 0% of frames — the floor present and
        // silent, which is what a floor should be.
        //
        // The envelope fades each agent in and out across its life, so a
        // retirement is a mote dimming rather than a mote vanishing.
        const u = a.age / a.span;
        const env = Math.min(1, Math.min(u, 1 - u) * 6);
        const alpha = (0.015 + a.life * 0.036 + bloom * 0.015) * env;

        // The mark is a BLEND of streak and bloom rather than a choice between
        // them: at bloom 0 it is a pure streak field, at 1 soft motes, and
        // every value between is a scene in its own right. A discrete style
        // switch here is exactly where a second engine would have started.
        if (bloom < 0.97) {
          b.line(a.px, a.py, a.x, a.y, a.size * k * (1 + bloom), idx,
                 alpha * (1 - bloom * 0.7));
        }
        if (bloom > 0.03) {
          // Radius enters the equilibrium as its square, so this multiplier is
          // the other half of the tuning above — it was 9 and had to come down
          // with the alpha.
          b.glow(a.x, a.y, a.size * k * (1 + bloom * 3.2), idx, alpha * bloom * 0.8);
        }
      }
    }
  }

  /**
   * The parameter vocabulary, shared by every preset. Defaults here are only
   * a fallback — each preset supplies its own, and that is the whole of what
   * makes one preset differ from another.
   */
  const PARAMS = {
    density: 400,   // how many agents
    pace: 300,      // how fast they move
    swirl: 700,     // weight of procedural turbulence
    stream: 200,    // weight of one constant direction
    direction: 0,   // ...and which direction, 0..1 of a full turn
    orbit: 0,       // weight of rotation about the centre
    scale: 300,     // spatial frequency of the field
    bloom: 600,     // streak (0) to soft mote (1000)
    fade: 300,      // how long trails persist
    grain: 400,     // spread of size and colour between agents
    tone: 400,      // where in the palette the field sits
    reactive: 0,    // how much the bed's loudness lifts pace and brightness
  };

  /** @param {string} id @param {string} label @param {object} over */
  function preset(id, label, over) {
    SCENES.define(id, { label, params: Object.assign({}, PARAMS, over), make });
  }

  // Slow luminous motes wandering a soft field. The default, and the one a
  // person who has not thought about it should get.
  preset('drift@1', 'Drift', {
    density: 300, pace: 180, swirl: 800, stream: 120, scale: 250,
    bloom: 850, fade: 220, grain: 500, tone: 350,
  });

  // A slow horizontal current with long streaks: reading weather.
  preset('tide@1', 'Tide', {
    density: 620, pace: 600, swirl: 300, stream: 800, direction: 20,
    scale: 180, bloom: 150, fade: 100, grain: 300, tone: 500,
  });

  // Everything turning about the middle of the frame, slowly.
  preset('orbit@1', 'Orbit', {
    density: 550, pace: 400, swirl: 200, stream: 0, orbit: 850,
    scale: 400, bloom: 400, fade: 140, grain: 350, tone: 250,
  });

  // Dense, tiny, barely moving: a grain that breathes rather than a motion.
  // The one to reach for behind text.
  preset('dust@1', 'Dust', {
    density: 900, pace: 60, swirl: 600, stream: 60, scale: 700,
    bloom: 700, fade: 90, grain: 700, tone: 600,
  });

  // Sparse warm motes rising. Upward is three quarters of a turn, because
  // the canvas y axis points down.
  preset('ember@1', 'Ember', {
    density: 180, pace: 260, swirl: 450, stream: 700, direction: 750,
    scale: 320, bloom: 950, fade: 260, grain: 600, tone: 850,
  });

  // The audio-reactive one, and the only preset that reads anything live.
  // Calm and complete in silence — the loudness of the post's own bed lifts
  // its pace and brightness, and nothing else reaches it.
  preset('pulse@1', 'Pulse', {
    density: 340, pace: 120, swirl: 650, stream: 150, orbit: 200,
    scale: 300, bloom: 800, fade: 300, grain: 450, tone: 450,
    reactive: 800,
  });
})();
