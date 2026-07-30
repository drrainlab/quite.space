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
  current: 'space', token: 't',
  api: async () => ({}), refreshSpace: () => {},
  fmtBytes: (n) => `${n} B`,
  makeWaterfall: () => el(),
  window: { open: () => {} },
};
vm.createContext(ctx);
vm.runInContext(extract('retryLink'), ctx);
vm.runInContext(extract('assetNote'), ctx);

const cases = [
  { name: 'fetching', asset: { id: 'a', state: 'fetching', missing: 5, total: 12, size: 10 },
    must: ['fetching'], mustNot: ['fail', 'error', 'unavailable'] },
  { name: 'no source while still asking',
    asset: { id: 'b', state: 'fetching', reason: 'no_source', missing: 12, total: 12, size: 10 },
    must: ['temporarily unavailable', 'no online source', 'try again'],
    mustNot: ['fail', 'error', 'deleted', 'gone', 'missing'] },
  { name: 'no source after giving up',
    asset: { id: 'c', state: 'failed', reason: 'no_source', missing: 12, total: 12, size: 10 },
    must: ['temporarily unavailable', 'try again'],
    mustNot: ['fail', 'error', 'deleted', 'gone'] },
  { name: 'no relay configured',
    asset: { id: 'd', state: 'failed', reason: 'no_peers', missing: 12, total: 12, size: 10 },
    must: ['temporarily unavailable'], mustNot: ['fail', 'error', 'deleted'] },
  { name: 'hash mismatch',
    asset: { id: 'e', state: 'failed', reason: 'integrity_error', missing: 0, total: 12, size: 10 },
    must: ['did not match its hash'],
    // This one is terminal: it must NOT promise that trying again helps.
    mustNot: ['try again', 'temporarily'] },
];

let failures = 0;
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
