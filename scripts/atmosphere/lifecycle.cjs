'use strict';
// The teardown and audio-arbitration contracts, asserted rather than eyeballed.
//
//   node scripts/atmosphere/lifecycle.cjs
//
// Both of these are infrastructure: the stage owns the client's only animation
// loop, and the arbiter decides who is allowed to make noise across seven
// independent playback owners. Surfaces that do not exist yet will rely on
// them, and "I checked it in the browser once" does not survive a refactor.
//
// What is checked here is bookkeeping — canvases, observers, frame callbacks,
// who holds the floor — all of it decided by our own code. Anything about
// pixels or loudness is measured elsewhere against a real browser; see the
// note at the top of domstub.cjs.

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

/** A fresh runtime with a fresh stub, so no test inherits another's state. */
function boot(files) {
  const ctx = vm.createContext(Object.create(null));
  const harness = install(ctx);
  for (const f of files) {
    const p = path.join(ASSETS, f);
    vm.runInContext(fs.readFileSync(p, 'utf8'), ctx, { filename: p });
  }
  return { ctx, harness, get: n => vm.runInContext(n, ctx) };
}

// ---------------------------------------------------------------- the arbiter

function arbiter() {
  const { get } = boot(['audio.js']);
  const AUDIO = get('AUDIO');

  check('an idle floor grants the first request', AUDIO.request('player:a', 'player'), true);
  check('...and reports the holder', AUDIO.current(), 'player:a');
  check('an equal priority does not displace', AUDIO.request('player:b', 'player'), false);
  check('the holder is unchanged', AUDIO.current(), 'player:a');

  let evicted = 0;
  AUDIO.release('player:a');
  AUDIO.request('player:c', 'player', () => { evicted++; });
  check('a listening room displaces a player', AUDIO.request('room:x', 'room'), true);
  check('...and the displaced owner is TOLD', evicted, 1);
  check('...and the room now holds it', AUDIO.current(), 'room:x');

  check('a bed never preempts a room', AUDIO.request('bed:p1', 'bed'), false);
  AUDIO.release('room:x');
  check('a bed takes an idle floor', AUDIO.request('bed:p1', 'bed'), true);
  check('a player displaces a bed', AUDIO.request('player:d', 'player'), true);
  AUDIO.release('player:d');

  // The bed also yields to the owners the arbiter does not manage yet.
  const { get: get2, harness } = (() => {
    const b = boot(['audio.js']);
    const el = b.harness.document.createElement('audio');
    el.paused = false; el.ended = false; el.currentTime = 1; el.muted = false;
    b.harness.body.appendChild(el);
    return b;
  })();
  check('a bed yields to unmigrated audio it can only detect',
    get2('AUDIO').request('bed:p2', 'bed'), false);
  check('...and says why', get2('AUDIO').busyReason(), 'something else is playing');

  check('releasing a floor you never held is harmless',
    (AUDIO.release('nobody'), AUDIO.current()), null);
}

// ----------------------------------------------------------------- the stage

function stageLifecycle() {
  const { ctx, harness, get } = boot([
    'modes.js', 'audio.js', 'brush.js', 'scenes.js', 'scenes/field.js', 'stage.js',
  ]);
  const STAGE = get('STAGE'), SCENES = get('SCENES');
  const host = harness.document.createElement('div');
  harness.body.appendChild(host);
  const canvases = () => host.querySelectorAll('canvas').length;

  let stops = 0, draws = 0;
  const scene = () => ({ draw() { draws++; }, stop() { stops++; } });

  check('nothing is on stage to begin with', [canvases(), STAGE.running()], [0, false]);

  STAGE.play(host, scene(), { palette: ['#000000', '#ffffff'] });
  check('play adds exactly one canvas', canvases(), 1);
  check('...and starts the loop', STAGE.running(), true);
  check('...and watches the host for layout changes', harness.observed(), 1);

  harness.tick(40);
  check('a frame was drawn', draws > 0, true);

  STAGE.clear();
  check('clear removes the canvas', canvases(), 0);
  check('...calls stop() exactly once', stops, 1);
  check('...stops the loop', STAGE.running(), false);
  check('...and stops watching the host', harness.observed(), 0);
  harness.tick(40);
  check('no frame callback survives clear', harness.pending(), 0);

  // A second scene replaces the first rather than joining it: one loop, ever.
  const a = scene(), b = scene();
  STAGE.play(host, a, { palette: ['#000000'] });
  STAGE.play(host, b, { palette: ['#000000'] });
  check('starting a second scene leaves one canvas', canvases(), 1);
  check('...and the first was stopped', stops, 2);
  STAGE.clear();

  // The case the manual check covered: many re-renders, nothing accumulating.
  stops = 0;
  for (let i = 0; i < 20; i++) {
    STAGE.play(host, scene(), { palette: ['#000000', '#ffffff'] });
    harness.tick(40);
  }
  check('20 re-renders leave exactly one canvas', canvases(), 1);
  check('...one watched host', harness.observed(), 1);
  check('...and stopped the 19 that were replaced', stops, 19);
  STAGE.clear();
  check('and after the last teardown, nothing at all',
    [canvases(), STAGE.running(), harness.observed(), harness.pending()],
    [0, false, 0, 0]);

  // A scene that throws is stopped, not retried thirty times a second.
  let threw = 0;
  STAGE.play(host, { draw() { threw++; throw new Error('boom'); } }, { palette: ['#000000'] });
  harness.tick(40);
  harness.tick(40);
  harness.tick(40);
  check('a throwing scene draws once', threw, 1);
  check('...and is torn down', [canvases(), STAGE.running()], [0, false]);

  // The person's pause: freezes on the current frame, outranks every
  // automatic resume, and Resume continues rather than restarts. This lives
  // here on the synthetic clock because a browser pane that happens to be
  // hidden pauses scenes too (correctly!), which makes visual verification
  // of "is it still moving" indistinguishable from the feature under test.
  let draws2 = 0, stops2 = 0, lastT = -1;
  STAGE.play(host, { draw(b, t) { draws2++; lastT = t; }, stop() { stops2++; } },
             { palette: ['#000000', '#ffffff'] });
  harness.tick(200);
  const beforePause = draws2, tAtPause = lastT;
  STAGE.pause();
  check('pause stops the loop', STAGE.running(), false);
  check('...and reports itself', STAGE.paused(), true);
  harness.tick(200);
  check('nothing is drawn while paused', draws2, beforePause);
  check('no frame callback is queued while paused', harness.pending(), 0);
  // The automatic resumes must not override the person.
  harness.document.hidden = true; harness.document.dispatch('visibilitychange');
  harness.document.hidden = false; harness.document.dispatch('visibilitychange');
  harness.tick(100);
  check('a tab hide/show does not lift a user pause', draws2, beforePause);
  STAGE.resume();
  harness.tick(200);
  check('resume continues drawing', draws2 > beforePause, true);
  check('...the scene was never stopped/restarted', stops2, 0);
  check('...and its clock continued rather than reset', lastT >= tAtPause, true);
  STAGE.clear();
  check('clear() ends the pause state with everything else', STAGE.paused(), false);

  // A real scene, through the real registry, for a full second of frames.
  const real = SCENES.make('drift@1', { seed: 3 });
  check('a built-in scene resolves', !!real, true);
  STAGE.play(host, real, { palette: ['#0b1020', '#7ab4e0'] });
  let frames = 0;
  const before = harness.now();
  while (harness.now() - before < 1000) { harness.tick(1000 / 60); frames++; }
  check('drift@1 survives a second of frames', STAGE.running(), true);
  STAGE.clear();
  check('...and tears down like any other', [canvases(), harness.observed()], [0, 0]);
}

// ------------------------------------------- same-post re-render preservation

function preserveAcrossRerender() {
  const { harness, get } = boot([
    'modes.js', 'audio.js', 'brush.js', 'scenes.js', 'scenes/field.js',
    'stage.js', 'atmosphere.js',
  ]);
  const STAGE = get('STAGE'), ATMO = get('ATMO');
  const doc = harness.document;

  const recipe = {
    visual: { scene: 'drift@1', seed: 7, params: [],
              palette: [{ name: 'g', hex: '#0b1020' }, { name: 'a', hex: '#7ab4e0' }] },
    fallback: { text: 'a field' },
  };

  const shell = doc.createElement('div');
  const bar = doc.createElement('div');
  harness.body.appendChild(shell);
  harness.body.appendChild(bar);

  ATMO.mount(shell, recipe, 'p1', { bar, enter: 'quiet' });
  harness.tick(200);
  check('entering quiet starts the scene', STAGE.running(), true);
  const stage0 = shell.querySelectorAll('div.atmo-stage')[0];
  const canvas0 = stage0 && stage0.querySelectorAll('canvas')[0];
  check('the stage layer holds the canvas', !!canvas0, true);

  // Recognition: the same post+recipe is held; an edited recipe is not.
  check('the same recipe is recognised as held', ATMO.holds('p1', recipe), true);
  check('another post is not', ATMO.holds('p2', recipe), false);
  const edited = JSON.parse(JSON.stringify(recipe));
  edited.visual.seed = 8;
  check('an edited recipe is not', ATMO.holds('p1', edited), false);

  // The reaction path: the article is rebuilt around a LIVE atmosphere.
  // Nothing may stop — this is the bug where reacting to a post killed its
  // scene and sound.
  const before = harness.pending();
  const kept = ATMO.detach();
  check('detach hands back the bar node', kept && kept.bar === bar, true);
  harness.tick(100); // the article is being rebuilt; the scene keeps drawing
  check('the scene never stopped during the rebuild', STAGE.running(), true);
  const shell2 = doc.createElement('div');
  harness.body.appendChild(shell2);
  ATMO.reattach(shell2);
  harness.tick(100);
  check('...and keeps running after reattach', STAGE.running(), true);
  check('...on the SAME canvas, not a replacement',
    shell2.querySelectorAll('div.atmo-stage')[0].querySelectorAll('canvas')[0] === canvas0, true);
  check('...and the new shell carries the class',
    shell2.classList.contains('atmo-shell'), true);

  ATMO.unmount();
  check('unmount still tears everything down',
    [STAGE.running(), harness.pending()], [false, 0]);
}

// ------------------------------------------------------------- the mode ladder

function ladder() {
  const { get, harness } = boot(['modes.js', 'audio.js', 'brush.js', 'scenes.js', 'stage.js']);
  const STAGE = get('STAGE');
  const ls = get('localStorage');

  const rows = [];
  for (const user of ['off', 'poster', 'calm', 'full']) {
    ls.setItem('qp.atmosphere', user);
    for (const policy of ['', 'still', 'quiet', 'lively']) {
      STAGE.setPolicy(policy);
      rows.push(`${user}/${policy || '-'}=${STAGE.mode()}${STAGE.wanted() ? '' : '(off)'}`);
    }
  }
  check('nothing can raise a rung the person did not allow', rows, [
    'off/-=poster(off)', 'off/still=poster(off)', 'off/quiet=poster(off)', 'off/lively=poster(off)',
    'poster/-=poster', 'poster/still=poster', 'poster/quiet=poster', 'poster/lively=poster',
    'calm/-=calm', 'calm/still=poster', 'calm/quiet=calm', 'calm/lively=calm',
    'full/-=full', 'full/still=poster', 'full/quiet=calm', 'full/lively=full',
  ]);

  ls.setItem('qp.atmosphere', 'full');
  STAGE.setPolicy('lively');
  harness.document.hidden = true;
  check('a hidden tab floors at poster', STAGE.mode(), 'poster');
  harness.document.hidden = false;
}

// ----------------------------------------------------------------------- main

arbiter();
stageLifecycle();
preserveAcrossRerender();
ladder();

if (failures) {
  console.error(`\n${failures} failure(s)`);
  process.exit(1);
}
console.log('teardown, audio arbitration and the mode ladder all hold');
