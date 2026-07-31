'use strict';
// The arrival-delta contract (RA-0), asserted rather than eyeballed.
//
//   node scripts/resonance/deltas.cjs
//
// Arrival effects fire for the group that actually GREW between two
// aggregate snapshots — never "the dominant one", never history. The feed
// computes that with computeResDeltas against its previous per-entry count
// map; this harness pins the helper's semantics, because every rung of the
// ASCII ladder (Echo → Stream → Fountain) stands on these deltas.

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

function boot(files) {
  const ctx = vm.createContext(Object.create(null));
  install(ctx);
  for (const f of files) {
    const p = path.join(ASSETS, f);
    vm.runInContext(fs.readFileSync(p, 'utf8'), ctx, { filename: p });
  }
  return { get: (n) => vm.runInContext(n, ctx) };
}

const { get } = boot(['resonance.js']);
const resCounts = get('resCounts');
const computeResDeltas = get('computeResDeltas');

const sem = (key, count) => ({ kind: 'semantic', key, count });
const uni = (value, count) => ({ kind: 'unicode', value, count });
const agg = (...groups) => ({ groups });

// ------------------------------------------------------------- resCounts

check('resCounts keys semantic groups by s:key',
  resCounts(agg(sem('warmth', 3))), { 's:warmth': 3 });
check('resCounts keys unicode groups by u:value',
  resCounts(agg(uni('🔥', 2))), { 'u:🔥': 2 });
check('resCounts of nothing is empty',
  resCounts(null), {});

// ------------------------------------------------------- computeResDeltas

check('a group that grew is the arrival, with its magnitude',
  computeResDeltas({ 's:warmth': 3 }, agg(sem('warmth', 7))),
  [{ group: sem('warmth', 7), delta: 4 }]);

// The RA-0 defect this helper exists to fix: a group appearing 0→1 beside
// a dominant one fires for ITSELF, not for the neighbour with 12.
check('a new group beside a dominant one fires for itself',
  computeResDeltas({ 's:warmth': 12 }, agg(sem('spark', 1), sem('warmth', 12))),
  [{ group: sem('spark', 1), delta: 1 }]);

check('a clear (count going down) is never an arrival',
  computeResDeltas({ 's:warmth': 3 }, agg(sem('warmth', 2))), []);

check('a vanished group is never an arrival',
  computeResDeltas({ 's:warmth': 1 }, agg()), []);

check('no previous snapshot means history, not arrival',
  computeResDeltas(null, agg(sem('warmth', 30))), []);
check('...and undefined behaves the same',
  computeResDeltas(undefined, agg(sem('warmth', 1))), []);

check('mixed tick: only the growers arrive',
  computeResDeltas({ 's:warmth': 2, 'u:🔥': 5 },
    agg(sem('warmth', 4), uni('🔥', 3), uni('✨', 2))),
  [{ group: sem('warmth', 4), delta: 2 }, { group: uni('✨', 2), delta: 2 }]);

check('an unchanged aggregate produces no deltas',
  computeResDeltas({ 's:warmth': 2 }, agg(sem('warmth', 2))), []);

if (failures) {
  console.error(`\n${failures} failure(s)`);
  process.exit(1);
}
console.log('resonance deltas: all checks passed');
