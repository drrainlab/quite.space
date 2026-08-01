'use strict';
// The atmosphere's sound has to ASK for its bytes.
//
//   node scripts/atmosphere/bed.cjs
//
// Reported from two live nodes: the author heard her post's bed and the
// follower heard nothing and was told nothing. The cause is invisible from
// either side — the author's bytes were already on her disk, so the <audio>
// element loaded; the follower's node had never fetched them, the asset route
// answered 409, and el.play() rejected into a catch that says nothing.
//
// The pictures never had this bug: their plates go through observeMedia,
// which asks the node to fetch. So what is asserted here is not "sound plays"
// — a stub cannot hear anything — but the bookkeeping that was missing: a
// fetch is REQUESTED, the element is not built until the answer comes, and a
// reader who leaves while the answer is in the air does not get sound from a
// post they closed.

const fs = require('fs');
const path = require('path');
const vm = require('vm');
const { install } = require('./domstub.cjs');

const ROOT = path.resolve(__dirname, '..', '..');
const ASSETS = path.join(ROOT, 'clients', 'web-ui', 'assets');

let failures = 0;
const verbose = process.argv.includes('--verbose');

function assert(name, cond, why) {
  if (!cond) {
    failures++;
    console.error(`FAIL  ${name}\n      ${why}`);
  } else if (verbose) {
    console.log(`  ok  ${name}`);
  }
  return cond;
}

const ASSET = 'a1b2c3d4e5f6';

/**
 * A node that has not got the bytes yet. `asked` records every fetch the
 * client requests; `settle` is how the test decides when they arrive.
 */
function boot() {
  const ctx = vm.createContext(Object.create(null));
  const harness = install(ctx);
  const asked = [];
  let resolveFetch = null;
  let report = null;   // the live pass's onProgress
  let quiet = null;    // the live pass's onUnavailable
  ctx.autoFetchAsset = (assetId, onReady, onUnavailable, onProgress) => {
    asked.push(assetId);
    report = onProgress || null;
    quiet = onUnavailable || null;
    return new Promise((res) => { resolveFetch = res; });
  };
  ctx.assetURL = (assetId) => '/api/spaces/S/assets/' + assetId + '?token=t';
  ctx.observeMedia = () => {};
  ctx.autoMediaSrc = (img, id) => { img.src = ctx.assetURL(id); };
  for (const f of ['modes.js', 'audio.js', 'brush.js', 'scenes.js', 'stage.js', 'atmosphere.js']) {
    const p = path.join(ASSETS, f);
    vm.runInContext(fs.readFileSync(p, 'utf8'), ctx, { filename: p });
  }
  return {
    harness, asked,
    atmo: vm.runInContext('ATMO', ctx),
    arrive: (ok) => { if (resolveFetch) { resolveFetch(ok); resolveFetch = null; } },
    /** Chunks landing mid-pass, the way a real poll reports them. */
    chunks: (have, total) => { if (report) report(have, total); },
    /** The node saying nobody has answered yet, while still asking. */
    noSource: () => { if (quiet) quiet('no_source'); },
    // The fetch resolves through a promise, so the callbacks that follow it
    // land a microtask later. Give them one.
    settle: () => new Promise((res) => setImmediate(res)),
  };
}

function withSound() {
  return {
    visual: { scene: 'drift@1', seed: 7, palette: [{ name: 'ground', hex: '#0b1020' }] },
    audio: { asset: ASSET, mode: 'loop', gain: 700 },
    fallback: { text: 'A slow moving background.' },
  };
}

/** Everything the bar ended up saying. The stub's textContent is per-node,
 *  so this walks what a reader would actually read. */
function words(node) {
  let out = String(node.textContent || '');
  for (const c of node.children || []) out += ' ' + words(c);
  return out;
}

function shell(harness) {
  const s = harness.document.createElement('div');
  harness.body.appendChild(s);
  return s;
}

(async () => {
  // The reader pressed the sound door. Before anything is pointed at a URL,
  // the node is asked for the bytes.
  {
    const b = boot();
    const bar = b.harness.document.createElement('div');
    b.atmo.mount(shell(b.harness), withSound(), 'p1', { bar, enter: 'sound' });

    assert('the bytes are requested', b.asked.length === 1 && b.asked[0] === ASSET,
      `asked for ${JSON.stringify(b.asked)} — a follower who never asks never hears anything`);
    assert('nothing is played before they arrive', b.harness.audios().length === 0,
      'an <audio> was pointed at bytes the node does not hold — which fails silently');

    b.arrive(true);
    await b.settle();
    const a = b.harness.audios();
    assert('the bed starts once they are here', a.length === 1 && a[0].played === true,
      `built ${a.length} audio element(s), played=${a[0] && a[0].played}`);
    assert('it plays the asset that was fetched', !!a[0] && a[0].src.includes(ASSET),
      `src was ${a[0] && a[0].src}`);
    b.atmo.unmount();
  }

  // Nobody answers. Patience is spent on progress, never on silence — so a
  // pass that adds nothing is followed by ONE more, and then the reader is
  // told, rather than left with a silent post and no explanation.
  {
    const b = boot();
    const bar = b.harness.document.createElement('div');
    b.atmo.mount(shell(b.harness), withSound(), 'p1', { bar, enter: 'sound' });
    b.noSource();
    assert('silence is named while we keep asking', /nobody is answering/i.test(words(bar)),
      `the bar said ${JSON.stringify(words(bar))}`);
    b.arrive(false);
    await b.settle();
    assert('one empty pass is not the answer', b.asked.length === 2,
      `gave up after ${b.asked.length} pass(es)`);
    b.arrive(false);
    await b.settle();
    assert('no bed when the bytes never came', b.harness.audios().length === 0,
      'a bed was started for bytes that never arrived');
    assert('and the bar says so', /never arrived/i.test(words(bar)),
      `the bar said ${JSON.stringify(words(bar))}`);
    b.atmo.unmount();
  }

  // A bed is the biggest thing an atmosphere carries. While its chunks are
  // still landing the reader sees them land — that is the whole difference
  // between "wait a moment" and "something is wrong" — and the asking does
  // not stop just because one pass ran out of patience.
  {
    const b = boot();
    const bar = b.harness.document.createElement('div');
    b.atmo.mount(shell(b.harness), withSound(), 'p1', { bar, enter: 'sound' });
    b.chunks(9, 57);
    assert('the count is shown', /9\/57/.test(words(bar)),
      `the bar said ${JSON.stringify(words(bar))}`);
    b.arrive(false);           // the pass ran out, but chunks HAD arrived
    await b.settle();
    assert('progress buys another pass', b.asked.length === 2,
      `gave up at 9/57 after ${b.asked.length} pass(es)`);
    b.chunks(57, 57);
    b.arrive(true);
    await b.settle();
    assert('and it plays when they are all here', b.harness.audios().length === 1,
      'every chunk arrived and nothing played');
    b.atmo.unmount();
  }

  // The reader closed the post while the fetch was still in the air. Sound
  // arriving afterwards would come from nowhere.
  {
    const b = boot();
    const bar = b.harness.document.createElement('div');
    b.atmo.mount(shell(b.harness), withSound(), 'p1', { bar, enter: 'sound' });
    b.atmo.unmount();
    b.arrive(true);
    await b.settle();
    assert('a closed post does not start sound', b.harness.audios().length === 0,
      'the bed started for a post the reader had already left');
  }

  // Opening quiet asks for nothing: the sound is a separate consent, and
  // fetching it anyway would spend a follower's bandwidth on a decision they
  // did not make.
  {
    const b = boot();
    const bar = b.harness.document.createElement('div');
    b.atmo.mount(shell(b.harness), withSound(), 'p1', { bar, enter: 'quiet' });
    assert('quiet fetches nothing', b.asked.length === 0,
      `asked for ${JSON.stringify(b.asked)} without being asked to`);
    b.atmo.unmount();
  }

  if (failures) {
    console.error(`\n${failures} failing`);
    process.exit(1);
  }
  console.log('atmosphere bed: ok');
})();
