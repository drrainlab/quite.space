'use strict';
// The ASCII-ladder contract (RA-1/RA-2), asserted rather than eyeballed.
//
//   node scripts/resonance/ascii.cjs
//
// What is checked is BOOKKEEPING: which rung fires for which window, how
// many glyph nodes exist, that budgets refuse instead of queueing, that
// history (no deltas) never animates, and that every glyph self-cleans so
// the live counter cannot leak. Pixels and motion are CSS-owned and are
// checked against a real browser in the wave's live pass.

const fs = require('fs');
const path = require('path');
const vm = require('vm');
const { install } = require('../atmosphere/domstub.cjs');

const ROOT = path.resolve(__dirname, '..', '..');
const ASSETS = path.join(ROOT, 'clients', 'web-ui', 'assets');

let failures = 0;
const verbose = process.argv.includes('--verbose');

function check(name, got, want) {
  const ok = JSON.stringify(got) === JSON.stringify(want);
  if (!ok) {
    failures++;
    console.error(`FAIL  ${name}\n      expected ${JSON.stringify(want)}, got ${JSON.stringify(got)}`);
  } else if (verbose) {
    console.log(`  ok  ${name}`);
  }
  return ok;
}

// boot: the domstub plus a CONTROLLABLE clock shared by Date.now and
// setTimeout — effects.js paces glyph lifetimes and the ladder window with
// both, and a test that cannot advance time cannot assert either.
function boot() {
  const ctx = vm.createContext(Object.create(null));
  const harness = install(ctx);
  let simNow = 0;
  let timers = [];
  ctx.Date = { now: () => simNow };
  ctx.setTimeout = (fn, ms) => { timers.push({ at: simNow + (ms || 0), fn }); return timers.length; };
  ctx.clearTimeout = () => {};
  ctx.innerHeight = 800; // inViewport reads window.innerHeight
  for (const f of ['modes.js', 'resonance.js', 'effects.js']) {
    const p = path.join(ASSETS, f);
    vm.runInContext(fs.readFileSync(p, 'utf8'), ctx, { filename: p });
  }
  const advance = (ms) => {
    simNow += ms;
    // run due timers in order; a timer may schedule another
    for (;;) {
      const due = timers.filter(t => t.at <= simNow).sort((a, b) => a.at - b.at);
      if (!due.length) break;
      timers = timers.filter(t => t.at > simNow);
      for (const t of due) t.fn();
    }
  };
  // A message row shaped like the feed's: node > .bubble (the fx host),
  // with an on-screen bounding box.
  const makeRow = () => {
    const node = harness.document.createElement('div');
    const bubble = harness.document.createElement('div');
    bubble.className = 'bubble';
    bubble.getBoundingClientRect = () => ({ top: 10, bottom: 120, width: 320, height: 110 });
    node.appendChild(bubble);
    harness.body.appendChild(node);
    return { node, bubble };
  };
  const glyphs = (bubble, cls) => {
    const layer = bubble.querySelector('.fx-layer');
    if (!layer) return [];
    return layer.children.filter(c => c.classList.contains(cls || 'fx-glyph'));
  };
  return { ctx, harness, advance, makeRow, glyphs,
    get: (n) => vm.runInContext(n, ctx) };
}

// The node resolves each group's authoritative fallback symbol before it
// reaches the client; the fixture mirrors that (core registry symbols).
const SYM = { resonates: '〰', warmth: '♡', spark: '✦', support: '◌',
  curious: '⌁', join: '↗', weight: '●' };
const sem = (key, count) => ({ kind: 'semantic', key, count, fallback: SYM[key] });
const agg = (...groups) => ({ groups });
const D = (group, delta) => ({ group, delta });

// -------------------------------------------------------------- the rungs

{
  const { get, makeRow, glyphs } = boot();
  const RESFX = get('RESFX');
  const { node, bubble } = makeRow();

  RESFX.onAggregateChange(node, agg(sem('warmth', 1)), [D(sem('warmth', 1), 1)]);
  check('one arrival is an Echo: one glyph', glyphs(bubble).length, 1);
  check('...of the reaction\'s own symbol', glyphs(bubble)[0].textContent, '♡');
  check('...and the pulse class is on the host',
    bubble.classList.contains('fx-arrive-sunwash'), true);
}

{
  const { get, makeRow, glyphs } = boot();
  const RESFX = get('RESFX');
  const { node, bubble } = makeRow();

  RESFX.onAggregateChange(node, agg(sem('warmth', 4)), [D(sem('warmth', 4), 4)]);
  const stream = glyphs(bubble, 'fx-glyph-stream');
  check('four arrivals in a window are a Stream', stream.length > 0, true);
  check('a Stream is bounded at 8 glyphs', stream.length <= 8, true);
}

{
  const { get, makeRow, glyphs, advance } = boot();
  const RESFX = get('RESFX');
  const { node, bubble } = makeRow();

  RESFX.onAggregateChange(node, agg(sem('spark', 5)), [D(sem('spark', 5), 5)]);
  advance(2000); // pulse finished; window (6s) still holds the 5
  RESFX.onAggregateChange(node, agg(sem('spark', 10)), [D(sem('spark', 10), 5)]);
  const fount = glyphs(bubble, 'fx-glyph-fount');
  check('9+ in the window is a Fountain', fount.length > 0, true);
  check('a Fountain burst is bounded at 24', fount.length <= 24, true);

  advance(10000); // window drains AND every glyph self-cleans
  check('all glyphs self-clean (timeout net)', glyphs(bubble).length, 0);
  RESFX.onAggregateChange(node, agg(sem('spark', 11)), [D(sem('spark', 11), 1)]);
  check('a drained window walks back down to Echo',
    glyphs(bubble, 'fx-glyph-echo').length, 1);
}

// ------------------------------------------------- no-replay and refusals

{
  const { get, makeRow, glyphs } = boot();
  const RESFX = get('RESFX');
  const { node, bubble } = makeRow();

  RESFX.onAggregateChange(node, agg(sem('warmth', 30)), []);
  check('history (no deltas) never animates: zero glyphs', glyphs(bubble).length, 0);
  check('...and no residue is painted — the chips carry the state',
    bubble.classList.contains('fx-residue'), false);
  check('...and no arrival class fired',
    bubble.className.includes('fx-arrive-'), false);
}

{
  const { get, makeRow, glyphs, ctx } = boot();
  ctx.localStorage.setItem('qp.effects', 'subtle');
  const RESFX = get('RESFX');
  const { node, bubble } = makeRow();

  RESFX.onAggregateChange(node, agg(sem('warmth', 6)), [D(sem('warmth', 6), 6)]);
  check('subtle never climbs the ladder: a single Echo glyph',
    glyphs(bubble, 'fx-glyph-echo').length, 1);
  check('...and no stream', glyphs(bubble, 'fx-glyph-stream').length, 0);
}

{
  const { get, makeRow, glyphs, ctx } = boot();
  ctx.localStorage.setItem('qp.effects', 'static');
  const RESFX = get('RESFX');
  const { node, bubble } = makeRow();

  RESFX.onAggregateChange(node, agg(sem('warmth', 3)), [D(sem('warmth', 3), 3)]);
  check('static refuses all glyphs', glyphs(bubble).length, 0);
  check('...and paints no residue either', bubble.classList.contains('fx-residue'), false);
}

{
  const { get, makeRow, glyphs } = boot();
  const RESFX = get('RESFX');
  const { node, bubble } = makeRow();
  bubble.getBoundingClientRect = () => ({ top: 5000, bottom: 5100, width: 320, height: 100 });

  RESFX.onAggregateChange(node, agg(sem('warmth', 2)), [D(sem('warmth', 2), 2)]);
  check('an offscreen card spawns nothing', glyphs(bubble).length, 0);
}

// ------------------------------------------------------- the glyph budget

{
  const { get, makeRow, glyphs } = boot();
  const RESFX = get('RESFX');
  // Four join-heavy fountains at once (MAX_CONCURRENT pulses): each wants
  // 24 fountain glyphs + 7 pluses = 124 total — over the 120 cap. The
  // budget must REFUSE the overflow, not queue it.
  const rows = [];
  for (let i = 0; i < 4; i++) rows.push(makeRow());
  for (const { node } of rows) {
    RESFX.onAggregateChange(node, agg(sem('join', 10)),
      [D(sem('join', 10), 10)]);
  }
  let total = 0;
  for (const { bubble } of rows) total += glyphs(bubble).length;
  check('the viewport glyph budget holds (≤120)', total <= 120, true);
  check('...and it actually engaged (some glyphs were refused)', total < 124, true);
}

if (failures) {
  console.error(`\n${failures} failure(s)`);
  process.exit(1);
}
console.log('resonance ascii: all checks passed');
