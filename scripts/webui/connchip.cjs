// What the connection chip claims, checked against what is true.
//
// WHY A HARNESS FOR ONE FUNCTION. The chip is a claim about HOW A MESSAGE
// TRAVELS, and a wrong claim there is worse than no claim: somebody deciding
// whether it is safe to say something out of range reads that mark. The
// function is four lines of branching that nobody can hold in their head at
// three in the morning, and every case below came from a real state a device
// was actually in.
//
// It runs the real source rather than a copy: app.js is read, the one
// function is cut out by name, and evaluated. A copy here would pass forever
// while the app drifted.
const fs = require('fs');
const path = require('path');

const src = fs.readFileSync(
  path.join(__dirname, '..', '..', 'clients', 'web-ui', 'assets', 'app.js'), 'utf8');

const start = src.indexOf('function connectionSummary(');
if (start < 0) throw new Error('connectionSummary is gone — this harness is stale');
// Balance braces from the opening one.
let i = src.indexOf('{', start), depth = 0, end = -1;
for (let j = i; j < src.length; j++) {
  if (src[j] === '{') depth++;
  else if (src[j] === '}') { depth--; if (depth === 0) { end = j + 1; break; } }
}
const body = src.slice(start, end);

// The two names the function reaches for, stubbed to something inspectable.
const t = (key, vars) => (vars && vars.state ? `${vars.state}|${vars.count}` : key);
const connectionSummary = new Function('t', body + '; return connectionSummary;')(t);

let failures = 0;
function check(what, status, want) {
  const got = connectionSummary(status);
  const ok = got.kind === want.kind && (want.count === undefined ||
    got.text.endsWith('|' + want.count));
  if (!ok) {
    failures++;
    console.log(`FAIL ${what}\n  want kind=${want.kind}` +
      (want.count === undefined ? '' : ` count=${want.count}`) +
      `\n  got  kind=${got.kind} text=${JSON.stringify(got.text)}`);
  } else {
    console.log(`ok   ${what} — ${got.kind}`);
  }
}

const off = { lan: { peers: 0, listening: false }, radio: {} };

check('nothing at all', off, { kind: 'off' });

check('listening, nobody here',
  { lan: { peers: 0, listening: true }, radio: {} }, { kind: 'lan' });

check('somebody across the room',
  { lan: { peers: 2, listening: true }, radio: {} }, { kind: 'direct', count: 2 });

// THE ONE THAT WAS WRONG. A modem plugged in with nobody on the segment is
// not a connection to anybody, and it certainly is not one person: the chip
// counted the modem itself as a peer and announced "Radio · 1".
check('a modem attached, empty segment',
  { lan: { peers: 0, listening: true }, radio: { connected: true, peer_links: 0 } },
  { kind: 'lan' });

check('a modem attached, one neighbour on the air',
  { lan: { peers: 0, listening: true }, radio: { connected: true, peer_links: 1 } },
  { kind: 'radio', count: 1 });

// AND THE ONE THE USER FOUND. Wi-Fi comes back while the radio is still
// attached: the messages visibly travel the fast way, and the chip went on
// saying radio because a radio was plugged in.
check('wifi returns while the radio stays attached',
  { lan: { peers: 1, listening: true }, radio: { connected: true, peer_links: 1 } },
  { kind: 'direct', count: 1 });

if (failures) {
  console.log(`\n${failures} claim(s) the chip makes are not true`);
  process.exit(1);
}
console.log('\nthe chip says what is actually carrying');
