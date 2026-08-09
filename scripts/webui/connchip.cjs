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

function cut(name) {
  const s = src.indexOf('function ' + name + '(');
  if (s < 0) throw new Error(name + ' is gone — this harness is stale');
  let d = 0, e = -1;
  for (let j = src.indexOf('{', s); j < src.length; j++) {
    if (src[j] === '{') d++;
    else if (src[j] === '}') { d--; if (d === 0) { e = j + 1; break; } }
  }
  return src.slice(s, e);
}
const relayVerdict = new Function(cut('relayVerdict') + '; return relayVerdict;')();

let failures = 0;
function check(what, status, want) {
  const got = connectionSummary(status);
  const ok = got.kind === want.kind && (want.count === undefined
    ? !got.text.includes('|')          // no count shown at all
    : got.text.endsWith('|' + want.count));
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

// A modem plugged in with nobody on the segment is still a radio — that is
// how this device is reachable — but it is not a connection to ONE PERSON.
// The chip counted the modem itself as a peer and announced "Radio · 1".
check('a modem attached, empty segment',
  { lan: { peers: 0, listening: true }, radio: { connected: true, peer_links: 0 } },
  { kind: 'radio', count: undefined });

check('a modem attached, one neighbour on the air',
  { lan: { peers: 0, listening: true }, radio: { connected: true, peer_links: 1 } },
  { kind: 'radio', count: 1 });

// AND THE ONE THE USER FOUND. Wi-Fi comes back while the radio is still
// attached: the messages visibly travel the fast way, and the chip went on
// saying radio because a radio was plugged in.
check('wifi returns while the radio stays attached',
  { lan: { peers: 1, listening: true }, radio: { connected: true, peer_links: 1 } },
  { kind: 'direct', count: 1 });

// AND THE ONE THAT SENT ME BACK HERE. lan.peers used to count every live
// link, so a radio arrived as a LAN peer and two people talking over LoRa in
// a field were told they were on the same network. That is fixed in the node
// — this pins the interface's half: a count that says nothing about which
// transport it belongs to can only be read one way.
check('nobody on the network, one peer met over the air',
  { lan: { peers: 0, listening: false }, radio: { connected: true, peer_links: 1 } },
  { kind: 'radio', count: 1 });

// ---- where the relay stands against everything else -----------------------
//
// The chip is one claim, so when several things work something has to lose.
// A relay that is FAILING is not a way to reach anybody, and it used to take
// the mark anyway: a phone with Wi-Fi off and a radio attached and working
// announced "relay · issue" — true about the relay, false about the device.
function verdict(what, base, rs, want) {
  const d = relayVerdict(base, rs);
  const kind = d && d.text ? d.kind : base;      // null text = keep the base
  if (kind !== want) {
    failures++;
    console.log(`FAIL ${what}\n  want ${want}\n  got  ${kind}`);
  } else {
    console.log(`ok   ${what} — ${kind}`);
  }
  return d;
}

const healthy = { active: true, interval_ms: 4000 };
const unwell = { active: true, last_error: 'dial tcp: no route to host' };
const none = { active: false };

verdict('a healthy relay and nothing else', 'off', healthy, 'relay');
verdict('a healthy relay reaches everyone, a segment does not',
  'radio', healthy, 'relay');
verdict('nobody at all, and the relay is unwell — its complaint is the news',
  'off', unwell, 'relay');

// THE ONE ON THE SCREENSHOT. Wi-Fi off, radio attached and working, relay
// unreachable because there is no internet. The device is not "relay · issue".
verdict('wifi off, radio working, relay unreachable', 'radio', unwell, 'radio');

const kept = relayVerdict('radio', unwell);
if (!kept || kept.text !== null || !kept.why) {
  failures++;
  console.log('FAIL the relay\'s reason must survive even when it loses the mark');
} else {
  console.log('ok   the relay still explains itself in the tooltip');
}

verdict('no relay configured at all', 'radio', none, 'radio');

if (failures) {
  console.log(`\n${failures} claim(s) the chip makes are not true`);
  process.exit(1);
}
console.log('\nthe chip says what is actually carrying');
