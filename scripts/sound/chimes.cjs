'use strict';

// Quiet Chimes contract harness. Two separate laws, tested separately:
//
//   THE VOICE (chime-dsp.js): every tier renders, durations sit in their
//   design bands, and the anti-escalation invariants hold — signal+ is
//   LONGER not LOUDER than signal, and every tier shares ONE peak budget.
//
//   THE LAW (chime.js arbitration core): one meaningful chime per window,
//   highest rank wins, families cool down independently.
//
// Run directly (node scripts/sound/chimes.cjs) or via the web-ui suite.

const path = require('path');
const assets = path.join(__dirname, '..', '..', 'clients', 'web-ui', 'assets');
const DSP = require(path.join(assets, 'chime-dsp.js'));
const CHIME = require(path.join(assets, 'chime.js'));

let failures = 0;
const fail = (msg) => { console.error('FAIL ' + msg); failures++; };
const ok = (msg) => console.log('ok  ' + msg);

const SR = 44100;
const stats = (buf) => {
  let peak = 0, sum = 0;
  for (const v of buf) { peak = Math.max(peak, Math.abs(v)); sum += v * v; }
  return { peak, rms: Math.sqrt(sum / buf.length), dur: buf.length / SR };
};

// ---- the voice --------------------------------------------------------

const tiers = ['tick', 'room', 'signal', 'personal'];
const mods = [null, 'person', 'instrument', 'space'];
const S = {};
for (const t of tiers) {
  for (const m of mods) {
    const st = stats(DSP.renderChime(t, m, SR));
    S[t + ':' + (m || '')] = st;
    if (st.rms < 0.01) fail(`${t}/${m || 'base'} is silence`);
    if (st.peak > DSP.CHIME_CONST.PEAK + 1e-3) {
      fail(`${t}/${m || 'base'} peak ${st.peak.toFixed(3)} breaks the ONE peak budget`);
    }
  }
}
ok('every tier×modifier renders, non-silent, inside the shared peak budget');

if (S['tick:'].dur > 0.090) fail(`tick lasts ${S['tick:'].dur}s — it stopped being tactile`);
const sig = S['signal:'], per = S['personal:'];
if (sig.dur < 0.3 || sig.dur > 0.7) fail(`the signature lasts ${sig.dur}s — outside its 300–700 ms identity`);
if (per.dur <= sig.dur) fail('personal is not LONGER than signal');
if (per.peak > sig.peak + 1e-3) fail('personal is LOUDER than signal — the law is broken');
ok('tick ≤ 90 ms; signature within 300–700 ms; personal longer, never louder');

// The same seed, the same breath: a chime is an identity, not a dice roll.
const a = DSP.renderChime('signal', null, SR), b = DSP.renderChime('signal', null, SR);
let same = a.length === b.length;
for (let i = 0; same && i < a.length; i += 97) same = a[i] === b[i];
if (!same) fail('two renders of the signature differ — the voice must be deterministic');
else ok('the voice is deterministic');

// ---- the law ----------------------------------------------------------

const arb = CHIME._arbitrate;
const t0 = 1_000_000;
const run = (cands, familyUntil = {}, now = t0) => arb(cands, familyUntil, now);

let r = run([{ tier: 'tick' }, { tier: 'signal' }]);
if (r.winner?.tier !== 'signal') fail('tick+signal in one window must resolve to signal');
else ok('tick + signal within the window → signal');

r = run([{ tier: 'tick' }, { tier: 'room' }, { tier: 'personal' }]);
if (r.winner?.tier !== 'personal') fail('tick+room+personal must resolve to personal');
else ok('tick + room + personal → personal');

r = run(Array.from({ length: 10 }, () => ({ tier: 'tick' })));
if (r.winner?.tier !== 'tick') fail('a burst of ticks must still yield exactly one tick');
else ok('10 × tick in one window → one tick');

// After a personal, the ATTENTION family cools as one clock…
let fu = run([{ tier: 'personal' }]).familyUntil;
r = run([{ tier: 'signal' }], fu, t0 + 1000);
if (r.winner !== null) fail('a signal one second after a personal must be silent (same family)');
else ok('signal during attention-family cooldown → silence');

// …while the MESSAGE family keeps its own clock.
r = run([{ tier: 'tick' }], fu, t0 + 1000);
if (r.winner?.tier !== 'tick') fail('a tick during attention cooldown must still play (its own family)');
else ok('tick during attention-family cooldown → plays');

// And a cooling favourite must not shadow the race: signal cooling,
// tick free → the tick wins rather than nothing.
r = run([{ tier: 'signal' }, { tier: 'tick' }], fu, t0 + 1000);
if (r.winner?.tier !== 'tick') fail('a cooling signal must not shadow a free tick');
else ok('cooling signal + free tick in one window → tick');

if (failures) { console.error(failures + ' failure(s)'); process.exit(1); }
console.log('the voice and the law hold');
