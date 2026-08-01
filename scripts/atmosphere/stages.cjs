'use strict';
// The image-sequence contract (AM-7), asserted rather than scrolled through.
//
//   node scripts/atmosphere/stages.cjs
//
// Three things here are worth a test and nothing else in the tree covers
// them: WHICH stage a reader is in (pure geometry, and it must answer the
// same going up as going down), the DWELL rule that keeps a fast scroll from
// playing every picture it flies past, and TEARDOWN — the sequence installs a
// window listener, a resize observer, a timer and two plates, and a reader
// who closes a post must be left with none of them.
//
// Pixels and brightness are not measured here; see the note at the top of
// domstub.cjs. What is measured is bookkeeping our own code decides.

const fs = require('fs');
const path = require('path');
const vm = require('vm');
const { install } = require('./domstub.cjs');

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

function boot(files) {
  const ctx = vm.createContext(Object.create(null));
  const harness = install(ctx);
  for (const f of files) {
    const p = path.join(ASSETS, f);
    vm.runInContext(fs.readFileSync(p, 'utf8'), ctx, { filename: p });
  }
  return { ctx, harness, get: n => vm.runInContext(n, ctx) };
}

const IMAGES = ['aa', 'bb', 'cc'];

function sequence() {
  return {
    visual: {
      palette: [{ name: 'ground', hex: '#0b1020' }],
      stages: [
        { anchor: 'b1', image: IMAGES[0] },
        { anchor: 'b2', image: IMAGES[1], transition: 'fade', duration_ms: 400 },
        { anchor: 'b3', image: IMAGES[2], transition: 'cut' },
      ],
    },
    fallback: { text: 'Three photographs, in order.' },
  };
}

/**
 * An article whose three blocks sit 1000px apart, mounted with a sequence.
 * The viewport is 360 tall (domstub's default), so the activation line is at
 * 144 — a block is "reached" once its top passes it.
 */
function article() {
  const { harness, get } = boot(['modes.js', 'audio.js', 'brush.js', 'atmosphere.js']);
  const ATMO = get('ATMO');
  const shell = harness.document.createElement('div');
  harness.body.appendChild(shell);
  const blocks = {};
  for (const id of ['b1', 'b2', 'b3']) {
    const el = harness.document.createElement('div');
    el.dataset.blockId = id;
    shell.appendChild(el);
    blocks[id] = el;
  }
  scrollTo(blocks, 0);
  const bar = harness.document.createElement('div');
  const asked = [];
  ATMO.mount(shell, sequence(), 'p1', {
    bar,
    // The preview seam, which is also the only way a plate gets a src in a
    // context with no app.js: the held path goes through assetURL.
    stageInto: (img, aid) => { asked.push(aid); img.src = 'blob:' + aid; },
  });
  return { harness, ATMO, shell, blocks, bar, asked };
}

/** Put the reader `y` pixels down the article. */
function scrollTo(blocks, y) {
  blocks.b1.rect = { top: 0 - y };
  blocks.b2.rect = { top: 1000 - y };
  blocks.b3.rect = { top: 2000 - y };
}

/** Which stage is showing, by the picture on the visible plate. */
function showing(shell) {
  const on = shell.querySelectorAll('.atmo-plate').filter(p => p.classList.contains('on'));
  if (on.length !== 1) return on.length === 0 ? 'none' : 'two at once';
  return String(on[0].dataset.asset || '');
}

/** Let the frame land and any image finish decoding. */
function settle(harness, ms) {
  harness.tick(ms || 0);
  harness.tick(0);
  harness.tick(0);
}

// ---------------------------------------------------------------- the reading

function reading() {
  const { harness, shell, blocks } = article();
  settle(harness);

  check('the post opens on the first picture', showing(shell), IMAGES[0]);
  check('...and exactly two plates exist, never one per stage',
    shell.querySelectorAll('.atmo-plate').length, 2);

  // Far enough for b2 to pass the line, and well past the dwell.
  harness.tick(2000);
  scrollTo(blocks, 900);
  harness.fire('scroll');
  settle(harness);
  check('reaching the second block changes the background', showing(shell), IMAGES[1]);

  harness.tick(2000);
  scrollTo(blocks, 1900);
  harness.fire('scroll');
  settle(harness);
  check('and the third', showing(shell), IMAGES[2]);

  // The whole point of reading the document instead of counting intersection
  // events: going back up has to give the same answer as coming down.
  harness.tick(2000);
  scrollTo(blocks, 900);
  harness.fire('scroll');
  settle(harness);
  check('scrolling back up returns to the second picture', showing(shell), IMAGES[1]);

  harness.tick(2000);
  scrollTo(blocks, 0);
  harness.fire('scroll');
  settle(harness);
  check('...and to the first', showing(shell), IMAGES[0]);
}

// ------------------------------------------------------------------ the dwell

function dwell() {
  const { harness, shell, blocks } = article();
  settle(harness);
  check('opens on the first picture', showing(shell), IMAGES[0]);

  // A flick from the top to the bottom, inside one dwell window.
  scrollTo(blocks, 900);
  harness.fire('scroll');
  settle(harness);
  check('a change inside the dwell window does not land yet', showing(shell), IMAGES[0]);

  scrollTo(blocks, 1900);
  harness.fire('scroll');
  settle(harness);
  check('...and neither does the next one', showing(shell), IMAGES[0]);

  // When the window opens, it shows where the reader ENDED, not every
  // picture they flew past.
  harness.tick(800);
  settle(harness);
  check('the dwell timer shows where the reader ended up', showing(shell), IMAGES[2]);
  check('...and only one timer was ever armed', harness.timers(), 0);
}

// --------------------------------------------------------------- the teardown

function teardown() {
  const { harness, ATMO, shell } = article();
  settle(harness);
  check('the sequence listens for scrolling', harness.listening('scroll'), 1);
  check('...and for resizing', harness.listening('resize'), 1);
  check('...and watches the article for layout changes', harness.observed(), 1);

  ATMO.unmount();
  check('unmount removes both plates', shell.querySelectorAll('.atmo-plate').length, 0);
  check('...stops listening for scrolling', harness.listening('scroll'), 0);
  check('...stops listening for resizing', harness.listening('resize'), 0);
  check('...and stops watching the article', harness.observed(), 0);
  harness.tick(5000);
  check('no timer survives unmount', harness.timers(), 0);
}

// ------------------------------------------------------------- the re-arm bug

function rearm() {
  const { harness, ATMO, shell, blocks } = article();
  settle(harness);

  // renderArticle keeps the atmosphere alive and rebuilds every block node.
  // Before the re-arm in reattach(), the sequence went on watching orphans
  // and the background froze wherever it happened to be.
  ATMO.detach();
  for (const id of ['b1', 'b2', 'b3']) blocks[id].remove();
  for (const id of ['b1', 'b2', 'b3']) {
    const el = harness.document.createElement('div');
    el.dataset.blockId = id;
    shell.appendChild(el);
    blocks[id] = el;
  }
  scrollTo(blocks, 0);
  ATMO.reattach(shell);

  harness.tick(2000);
  scrollTo(blocks, 900);
  harness.fire('scroll');
  settle(harness);
  check('a rebuilt article still moves the story on', showing(shell), IMAGES[1]);
}

// ------------------------------------------------------------ dangling anchor

function dangling() {
  const { harness, get } = boot(['modes.js', 'audio.js', 'brush.js', 'atmosphere.js']);
  const ATMO = get('ATMO');
  const shell = harness.document.createElement('div');
  harness.body.appendChild(shell);
  // Only b1 and b3 exist: b2's stage is one the decoder accepted and this
  // document cannot place. It must be skipped, not guessed at.
  const blocks = {};
  for (const id of ['b1', 'b3']) {
    const el = harness.document.createElement('div');
    el.dataset.blockId = id;
    shell.appendChild(el);
    blocks[id] = el;
  }
  blocks.b2 = { rect: {}, dataset: {} }; // never in the DOM
  blocks.b1.rect = { top: 0 };
  blocks.b3.rect = { top: 2000 };
  ATMO.mount(shell, sequence(), 'p1', {
    bar: harness.document.createElement('div'),
    stageInto: (img, aid) => { img.src = 'blob:' + aid; },
  });
  settle(harness);
  check('a sequence with a dangling anchor still opens', showing(shell), IMAGES[0]);

  harness.tick(2000);
  blocks.b1.rect = { top: -1900 };
  blocks.b3.rect = { top: 100 };
  harness.fire('scroll');
  settle(harness);
  check('...and skips straight past the stage it cannot place', showing(shell), IMAGES[2]);
}

// ------------------------------------------------------------- no layout yet

// An article measured before its pane has a size reports every anchor at
// top 0. With an activation line of zero every one of them counts as
// reached, so the sequence would open on its LAST picture — found live in an
// embedded webview, where innerHeight is 0 while the page is plainly there.
function noLayout() {
  const { ctx, harness, get } = boot(['modes.js', 'audio.js', 'brush.js', 'atmosphere.js']);
  ctx.innerHeight = 0;
  ctx.document.documentElement = { clientHeight: 0 };
  const ATMO = get('ATMO');
  const shell = harness.document.createElement('div');
  harness.body.appendChild(shell);
  const blocks = {};
  for (const id of ['b1', 'b2', 'b3']) {
    const el = harness.document.createElement('div');
    el.dataset.blockId = id;
    el.rect = { top: 0 };
    shell.appendChild(el);
    blocks[id] = el;
  }
  ATMO.mount(shell, sequence(), 'p1', {
    bar: harness.document.createElement('div'),
    stageInto: (img, aid) => { img.src = 'blob:' + aid; },
  });
  settle(harness);
  // Past the dwell window twice, so nothing is merely waiting its turn —
  // without the guard this is where the story jumps to its last picture.
  for (let i = 0; i < 3; i++) { harness.tick(2000); settle(harness); }
  check('with no layout the story stays on its FIRST picture', showing(shell), IMAGES[0]);

  // And it catches up the moment there is something to measure.
  ctx.document.documentElement = { clientHeight: 360 };
  scrollTo(blocks, 900);
  harness.tick(2000);
  harness.fire('scroll');
  settle(harness);
  check('...and moves on once the pane has a size', showing(shell), IMAGES[1]);
}

// ----------------------------------------------------------------------- main

reading();
noLayout();
dwell();
teardown();
rearm();
dangling();

if (failures) {
  console.error(`\n${failures} failing`);
  process.exit(1);
}
console.log('atmosphere stages: ok');
