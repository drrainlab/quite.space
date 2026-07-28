'use strict';
// Sweep every built-in scene and prove nobody can look at one and be hurt.
//
// Run directly, or through clients/web-ui/scenes_test.go, which is what puts
// it in `go test ./...`:
//
//     node scripts/atmosphere/flashcheck.cjs [--verbose]
//
// The scenes are loaded into a bare vm context with NO document, NO window,
// NO fetch and NO timers. That is the point of doing it this way rather than
// in a headless browser: a scene that quietly depends on any of them does not
// merely fail a lint, it fails to run at all.
//
// Luminance comes from the brush's own model, not from pixels, because the
// brush mediates every mark a scene can make (clients/web-ui/assets/brush.js).
// That is a proxy and is named as one. What it measures exactly is what the
// scene ASKED to paint, which is the thing the safety cap acts on.
//
// Three assertions, and the middle one is the reason the fixtures exist:
//
//   1. the analyser flags a real strobe               (synthetic series)
//   2. a scene that TRIES to strobe comes out calm    (adversarial fixtures)
//   3. every registered scene passes across seeds and parameter corners
//
// Without 1, a green run could mean the analyser is blind. Without 2, it could
// mean no scene happened to try.

const fs = require('fs');
const path = require('path');
const vm = require('vm');
const { analyse, LIMIT } = require('./flash.cjs');

const ROOT = path.resolve(__dirname, '..', '..');
const ASSETS = path.join(ROOT, 'clients', 'web-ui', 'assets');
const SCENE_DIR = path.join(ASSETS, 'scenes');
const FIXTURES = path.join(__dirname, 'fixtures');

const FPS = 30;
const SECONDS = 4;
/**
 * Three seeds, because a seed changes the field's phases and where everything
 * starts — different weather, same dynamics — and one sample of that is not
 * evidence. --quick drops to one for local iteration, and says so loudly:
 * a sweep that quietly narrows its own coverage reads exactly like a sweep
 * that found nothing.
 */
const ALL_SEEDS = [1, 0x5eed, 4294967291];
const quick = process.argv.includes('--quick');
const SEEDS = quick ? ALL_SEEDS.slice(0, 1) : ALL_SEEDS;
/** The most dangerous palette an author may legally write. */
const PALETTE = ['#000000', '#ffffff', '#ff0000', '#00ff88'];

const verbose = process.argv.includes('--verbose');
let failures = 0;

function fail(what, detail) {
  failures++;
  console.error(`FAIL  ${what}\n      ${detail}`);
}

// ---------------------------------------------------------------- 1. analyser

function selfTest() {
  const cases = [
    ['alternating black/white at 30fps', Array.from({ length: 120 },
      (_, i) => (i % 2 ? 1 : 0)), false],
    ['alternating every third frame', Array.from({ length: 120 },
      (_, i) => (Math.floor(i / 3) % 2 ? 1 : 0)), false],
    // A reversal every 15 frames at 30fps is two transitions a second, and
    // every transition after the first closes a pair: two flashes a second.
    ['two flashes per second', Array.from({ length: 120 },
      (_, i) => (Math.floor(i / 15) % 2 ? 0.9 : 0.1)), true],
    // One frame faster than the cap allows, to pin which side of the limit
    // ten frames a side falls on.
    ['a reversal every nine frames', Array.from({ length: 120 },
      (_, i) => (Math.floor(i / 9) % 2 ? 0.9 : 0.1)), false],
    ['a slow ramp', Array.from({ length: 120 }, (_, i) => i / 119), true],
    ['a still picture', Array.from({ length: 120 }, () => 0.4), true],
    ['dither inside a ramp', Array.from({ length: 120 },
      (_, i) => i / 119 + (i % 2 ? 0.008 : -0.008)), true],
    ['big swings above 0.80, which the darker-state rule exempts',
      Array.from({ length: 120 }, (_, i) => (i % 2 ? 1 : 0.85)), true],
  ];
  for (const [name, series, wantOK] of cases) {
    const r = analyse(series, FPS);
    if (r.ok !== wantOK) {
      fail(`analyser: ${name}`,
        `expected ok=${wantOK}, got ok=${r.ok} at ${r.flashesPerSecond}/s ` +
        `(limit ${LIMIT}, ${r.transitions} transitions)`);
    } else if (verbose) {
      console.log(`  ok    analyser: ${name} → ${r.flashesPerSecond}/s`);
    }
  }
}

// ------------------------------------------------------------------ 2. loading

function loadContext() {
  // Nothing but the language. No document, no window, no timers, no fetch.
  const ctx = vm.createContext(Object.create(null));
  vm.runInContext('globalThis.console = undefined;', ctx);
  const load = file => {
    const src = fs.readFileSync(file, 'utf8');
    try {
      vm.runInContext(src, ctx, { filename: file });
    } catch (err) {
      fail(`loading ${path.relative(ROOT, file)}`, String(err && err.message || err));
    }
  };
  load(path.join(ASSETS, 'brush.js'));
  load(path.join(ASSETS, 'scenes.js'));

  const sceneFiles = fs.existsSync(SCENE_DIR)
    ? fs.readdirSync(SCENE_DIR).filter(f => f.endsWith('.js')).sort() : [];
  for (const f of sceneFiles) load(path.join(SCENE_DIR, f));
  const shipped = new Set(vm.runInContext('SCENES.list()', ctx));

  const fixtureFiles = fs.existsSync(FIXTURES)
    ? fs.readdirSync(FIXTURES).filter(f => f.endsWith('.js')).sort() : [];
  for (const f of fixtureFiles) load(path.join(FIXTURES, f));
  const all = vm.runInContext('SCENES.list()', ctx);

  // Count SCENES, not the files they came from: one module can register a
  // whole family, and reporting "1 shipped" for six presets reads as though
  // five of them were never swept.
  return {
    ctx, shipped,
    sceneCount: shipped.size,
    fixtureCount: all.length - shipped.size,
  };
}

// ------------------------------------------------------------------ 3. sweeping

/** all-default, all-min, all-max, then each parameter alone at each end. */
function corners(params) {
  const names = Object.keys(params);
  const out = [{ label: 'defaults', values: { ...params } }];
  const all = v => Object.fromEntries(names.map(n => [n, v]));
  if (names.length) {
    out.push({ label: 'all=0', values: all(0) });
    out.push({ label: 'all=1000', values: all(1000) });
    for (const n of names) {
      for (const v of [0, 1000]) {
        out.push({ label: `${n}=${v}`, values: { ...params, [n]: v } });
      }
    }
  }
  return out;
}

function run(ctx, id, seed, values) {
  const BRUSH = vm.runInContext('BRUSH', ctx);
  const SCENES = vm.runInContext('SCENES', ctx);
  const scene = SCENES.make(id, { seed, params: values });
  if (!scene) return { error: 'make() returned null' };
  const b = BRUSH.make({ width: 800, height: 600, palette: PALETTE, seed, ctx: null });
  const series = [];
  const dt = 1 / FPS;
  let t = 0;
  for (let i = 0; i < FPS * SECONDS; i++) {
    b.begin();
    try {
      scene.draw(b.surface, t, dt);
    } catch (err) {
      return { error: `draw() threw at frame ${i}: ${err && err.message || err}` };
    }
    series.push(b.end());
    t += dt;
  }
  try { scene.stop && scene.stop(); } catch { /* not the scene's finest hour */ }
  return { report: analyse(series, FPS) };
}

function sweep(ctx, shipped) {
  const SCENES = vm.runInContext('SCENES', ctx);
  const ids = SCENES.list();
  let cases = 0;
  for (const id of ids) {
    const def = SCENES.get(id);
    const kind = shipped.has(id) ? 'scene  ' : 'fixture';
    let worst = null;
    for (const seed of SEEDS) {
      for (const c of corners(def.params)) {
        cases++;
        const { error, report } = run(ctx, id, seed, c.values);
        if (error) { fail(`${id} [seed ${seed}, ${c.label}]`, error); continue; }
        if (!report.ok) {
          fail(`${id} [seed ${seed}, ${c.label}]`,
            `${report.flashesPerSecond} flashes/s exceeds ${LIMIT} ` +
            `(worst around frame ${report.worstFrame}, ` +
            `max amplitude ${report.maxAmplitude.toFixed(3)})`);
        }
        if (!worst || report.flashesPerSecond > worst.flashesPerSecond ||
            (report.flashesPerSecond === worst.flashesPerSecond &&
             report.maxFrameStep > worst.maxFrameStep)) {
          worst = report;
        }
      }
    }
    if (worst) {
      console.log(`  ${kind} ${id.padEnd(24)} worst ${worst.flashesPerSecond}/s, ` +
        `step ${worst.maxFrameStep.toFixed(4)}, amplitude ${worst.maxAmplitude.toFixed(3)}`);
    }
  }
  return { ids, cases };
}

// ---------------------------------------------------------------------- main

selfTest();
const { ctx, shipped, sceneCount, fixtureCount } = loadContext();
if (fixtureCount === 0) {
  fail('adversarial fixtures', `none found in ${path.relative(ROOT, FIXTURES)} — ` +
    'a green sweep would then prove only that nobody tried');
}
const { ids, cases } = sweep(ctx, shipped);

console.log(`\n${ids.length} scenes (${sceneCount} shipped, ${fixtureCount} adversarial), ` +
  `${cases} cases, ${SECONDS}s each at ${FPS}fps`);
if (quick) {
  console.log(`QUICK: 1 seed of ${ALL_SEEDS.length}. ` +
    `${(ALL_SEEDS.length - 1) * cases} cases were NOT run — this is not the gate.`);
}
if (failures) {
  console.error(`\n${failures} failure(s)`);
  process.exit(1);
}
console.log('all within the WCAG general flash threshold');
