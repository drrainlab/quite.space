// PH-4: the wording contract for an absent file.
//
// This checks sentences rather than mechanics, on purpose. What a person
// reads when media does not arrive is the whole feature: "failed" and "no
// online source" describe the same bytes and mean opposite things to the
// reader — one says something is broken, the other says come back later.
// Only the second is true, and only for the network case.
//
// The hash-mismatch case is the exception and must stay one: those bytes
// really are wrong, waiting will not fix them, and softening that would be
// the dishonest direction.
'use strict';

const fs = require('fs');
const path = require('path');
const vm = require('vm');

const appPath = path.join(__dirname, '..', '..', 'clients', 'web-ui', 'assets', 'app.js');
const src = fs.readFileSync(appPath, 'utf8');

// Pull out just the pieces under test. Loading all of app.js would drag in
// the whole client; the contract lives in three functions.
function extract(name) {
  const start = src.indexOf(`function ${name}(`);
  if (start < 0) throw new Error(`${name} is gone from app.js`);
  let depth = 0, i = src.indexOf('{', start);
  for (let j = i; j < src.length; j++) {
    if (src[j] === '{') depth++;
    else if (src[j] === '}' && --depth === 0) return src.slice(start, j + 1);
  }
  throw new Error(`${name} does not close`);
}

const UNAVAILABLE_TEXT = /const UNAVAILABLE_TEXT = '([^']+)'/.exec(src);
if (!UNAVAILABLE_TEXT) throw new Error('UNAVAILABLE_TEXT is gone from app.js');

// A DOM small enough to render a note into.
function el() {
  const node = {
    className: '', _text: '', children: [], style: {}, onclick: null,
    appendChild(c) { this.children.push(c); return c; },
    get textContent() {
      return this._text + this.children.map((c) => c.textContent || '').join('');
    },
    set textContent(v) { this._text = v; this.children = []; },
  };
  return node;
}

const ctx = {
  document: { createElement: () => el() },
  UNAVAILABLE_TEXT: UNAVAILABLE_TEXT[1],
  PROTOCOL: false, // the human path; Protocol view has its own vocabulary
  current: 'space', token: 't',
  api: async () => ({}), refreshSpace: () => {},
  fmtBytes: (n) => `${n} B`,
  // The progress rail is a drawing, not wording — this harness only cares
  // that assetNote can build one.
  makeProgress: () => el(),
  window: { open: () => {} },
};
vm.createContext(ctx);
vm.runInContext(extract('retryLink'), ctx);
vm.runInContext(extract('waitingForText'), ctx);
vm.runInContext(extract('assetNote'), ctx);

const cases = [
  { name: 'fetching', asset: { id: 'a', state: 'fetching', missing: 5, total: 12, size: 10 },
    must: ['fetching'], mustNot: ['fail', 'error', 'unavailable'] },
  // STILL ASKING vs GAVE UP. The node has always distinguished these —
  // `fetching` + no_source means the loop is running and nobody has answered
  // YET; `failed` + no_source means it stopped — and both used to render the
  // same sentence with the same button. So the one question a person has,
  // "is it still trying?", had no answer on the screen, and the honest
  // half ("temporarily") was doing the work of both.
  { name: 'no source while still asking',
    asset: { id: 'b', state: 'fetching', reason: 'no_source', missing: 12, total: 12, size: 10 },
    must: ['still looking'],
    // No retry offered while it is already retrying: a button that repeats
    // what is happening anyway teaches people to press it at random.
    mustNot: ['try again', 'fail', 'error', 'deleted', 'gone', 'missing'] },
  { name: 'no source after giving up',
    asset: { id: 'c', state: 'failed', reason: 'no_source', missing: 12, total: 12, size: 10 },
    // It stopped, and the way back is offered rather than implied.
    must: ['answer', 'again'],
    mustNot: ['still looking', 'fail', 'error', 'deleted', 'gone'] },
  { name: 'no relay configured',
    asset: { id: 'd', state: 'failed', reason: 'no_peers', missing: 12, total: 12, size: 10 },
    must: ['temporarily unavailable'], mustNot: ['fail', 'error', 'deleted'] },
  { name: 'hash mismatch',
    asset: { id: 'e', state: 'failed', reason: 'integrity_error', missing: 0, total: 12, size: 10 },
    must: ['did not match its hash'],
    // This one is terminal: it must NOT promise that trying again helps.
    mustNot: ['try again', 'temporarily'] },
];

// WHOSE PHONE IS ASLEEP. A photo's bytes are not on the relay — they stay on
// the device that sent them — so "no online source" describes a person, not a
// fault, and the entry usually knows which person. These pin that it is said
// when it can be and never guessed when it cannot.
const whoCases = [
  { name: 'the sender is named when known',
    entry: { author_name: 'Alex',
      asset: { id: 'w1', state: 'fetching', reason: 'no_source', missing: 9, total: 9, size: 10 } },
    must: ['still looking', 'alex'],
    mustNot: ['fail', 'error', 'deleted', 'gone'] },
  // "waiting for you" on a device that is plainly running is nonsense — it
  // happens when fetching our own media onto a second device.
  { name: 'never waits for ourselves',
    entry: { mine: true, author_name: 'Me',
      asset: { id: 'w2', state: 'fetching', reason: 'no_source', missing: 9, total: 9, size: 10 } },
    must: ['still looking'], mustNot: ["'s device"] },
  { name: 'an unnamed author falls back rather than guessing',
    entry: { asset: { id: 'w3', state: 'fetching', reason: 'no_source', missing: 9, total: 9, size: 10 } },
    must: ['still looking'], mustNot: ["'s device", 'undefined'] },
  // no_peers is about THIS device having no way out. Blaming somebody else's
  // phone for our own missing relay would send a person to the wrong place.
  { name: 'no way out of here is not somebody else being away',
    entry: { author_name: 'Alex',
      asset: { id: 'w4', state: 'failed', reason: 'no_peers', missing: 9, total: 9, size: 10 } },
    must: ['temporarily unavailable'], mustNot: ['alex'] },
];

let failures = 0;
for (const c of whoCases) {
  const text = ctx.assetNote(c.entry).textContent.toLowerCase();
  for (const m of c.must) {
    if (!text.includes(m.toLowerCase())) {
      console.error(`FAIL ${c.name}: expected to say "${m}", said "${text}"`);
      failures++;
    }
  }
  for (const m of c.mustNot) {
    if (text.includes(m.toLowerCase())) {
      console.error(`FAIL ${c.name}: must not say "${m}", said "${text}"`);
      failures++;
    }
  }
  console.log(`ok  ${c.name}: "${text}"`);
}

for (const c of cases) {
  const text = ctx.assetNote({ asset: c.asset }).textContent.toLowerCase();
  for (const m of c.must) {
    if (!text.includes(m.toLowerCase())) {
      console.error(`FAIL ${c.name}: expected to say "${m}", said "${text}"`);
      failures++;
    }
  }
  for (const m of c.mustNot) {
    if (text.includes(m.toLowerCase())) {
      console.error(`FAIL ${c.name}: must not say "${m}", said "${text}"`);
      failures++;
    }
  }
  console.log(`ok  ${c.name}: "${text}"`);
}

if (failures) {
  console.error(`${failures} wording violation(s)`);
  process.exit(1);
}
console.log('wording contract holds');
