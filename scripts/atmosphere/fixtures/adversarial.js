// Scenes that try to hurt you.
//
// These never ship — they live beside the harness, not in clients/web-ui/assets
// — and they exist so that a green sweep means something. A run where every
// scene happens to be gentle proves nothing about the cap in brush.js; a run
// where a scene explicitly asks for a full-contrast strobe and still comes out
// under the threshold proves the cap is load-bearing.
//
// Each one attacks the cap from a different angle. The last two matter most:
// they are the ways a per-op limit would have been defeated.

// A pure strobe: maximum contrast, maximum alpha, every frame.
SCENES.define('adv-strobe@1', {
  label: 'strobe',
  params: {},
  make() {
    let i = 0;
    return {
      draw(b) {
        // Palette index 0 is black and 1 is white in the sweep palette.
        b.wash(i++ % 2, 1);
      },
    };
  },
});

// The same, at the rate photosensitive epilepsy is most provoked by: the
// hazard peaks near 15Hz but 3-5Hz is where a "tasteful" pulse would land.
SCENES.define('adv-pulse@1', {
  label: 'pulse',
  params: { rate: 500 },
  make(env) {
    // 1Hz .. 20Hz across the parameter's whole range, so the sweep's corners
    // walk straight through the dangerous band rather than around it.
    const hz = 1 + env.params.rate * 19;
    let t = 0;
    return {
      draw(b, _t, dt) {
        t += dt;
        b.wash(Math.floor(t * hz * 2) % 2, 1);
      },
    };
  },
});

// Flood the surface with opaque marks instead of washing it: a per-verb cap
// that only watched `wash` would be walked straight around by this.
SCENES.define('adv-blowout@1', {
  label: 'blowout',
  params: {},
  make() {
    let i = 0;
    return {
      draw(b) {
        const c = i++ % 2;
        for (let k = 0; k < 400; k++) {
          b.dot(b.rand() * b.width, b.rand() * b.height, b.width, c, 1);
        }
      },
    };
  },
});

// Death by a thousand cuts: thousands of marks, each far too faint to be
// worth capping on its own, adding up to a full-contrast flip every frame.
// This is the case a per-op alpha limit cannot see and a per-frame budget
// measured from the frame's own start does.
SCENES.define('adv-creep@1', {
  label: 'creep',
  params: {},
  make() {
    let i = 0;
    return {
      draw(b) {
        const c = i++ % 2;
        for (let k = 0; k < 3000; k++) b.wash(c, 0.004);
      },
    };
  },
});

// The optimal attack, worked out rather than guessed.
//
// The brush allows 0.01 of luminance per frame, and WCAG calls 0.10 a
// transition, so the fastest legal transition is exactly ten frames: three a
// second, which is the bound MAX_LUMA_STEP was derived to produce. Reversing
// sooner makes each swing too small to count at all; reversing later wastes
// frames. Ten is therefore the worst a scene can do, and this fixture does it,
// so the sweep sits on the boundary rather than somewhere comfortably inside
// it. If MAX_LUMA_STEP is ever raised, this is the first thing to go red.
//
// It measures 2/s rather than 3, because a window holding three transitions
// holds only two closed pairs. The derivation counts a flash per transition,
// which overstates by one — the safe direction for a bound.
// The parameter walks 8, 15 and 22 frames a side at the sweep's corners, so
// the boundary is probed from below, at, and above rather than assumed. Below
// it, each swing is too small to qualify as a transition at all and the scene
// registers nothing; at it, the rate is the fastest a scene can legally
// achieve. That number is what MAX_LUMA_STEP was chosen to set, so if anyone
// raises the constant this is the first fixture to go red.
SCENES.define('adv-worst@1', {
  label: 'worst case',
  params: { period: 500 },
  make(env) {
    const side = 8 + Math.round(env.params.period * 14);
    let i = 0;
    return {
      draw(b) {
        b.wash(Math.floor(i++ / side) % 2, 1);
      },
    };
  },
});

// Ride the cap at a range of rates either side of that optimum.
SCENES.define('adv-sawtooth@1', {
  label: 'sawtooth',
  params: { period: 500 },
  make(env) {
    const frames = 2 + Math.round(env.params.period * 28); // 2..30 frames a side
    let i = 0;
    return {
      draw(b) {
        const c = Math.floor(i++ / frames) % 2;
        b.wash(c, 1);
      },
    };
  },
});
