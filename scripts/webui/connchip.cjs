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
const relayVerdict = new Function('t', cut('relayVerdict') + '; return relayVerdict;')(t);

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

// REVERSED, ON PURPOSE, AND HERE IS THE WHOLE STORY. This case was found on
// a phone: Wi-Fi came back while the radio stayed attached, the messages
// visibly went the fast way, and the chip went on saying radio merely because
// a modem was plugged in. The fix then was to put local links first.
//
// The ladder has since changed above this function: the relay is asked FIRST
// and wins whenever it is healthy, which is what "Wi-Fi came back" means in
// practice — so the case that produced the old ordering no longer reaches
// here at all. What reaches here is the remainder: a local network that
// cannot get out, with a modem attached. Naming the radio there is the
// useful answer, and it is what the person who plugged it in asked for.
check('no way out but the air, and a modem is attached',
  { lan: { peers: 1, listening: true }, radio: { connected: true, peer_links: 1 } },
  { kind: 'radio', count: 1 });

// AND THE ONE THAT SENT ME BACK HERE. lan.peers used to count every live
// link, so a radio arrived as a LAN peer and two people talking over LoRa in
// a field were told they were on the same network. That is fixed in the node
// — this pins the interface's half: a count that says nothing about which
// transport it belongs to can only be read one way.
check('nobody on the network, one peer met over the air',
  { lan: { peers: 0, listening: false }, radio: { connected: true, peer_links: 1 } },
  { kind: 'radio', count: 1 });

// ---- the transport policy, which outranks every socket --------------------
//
// A listener that exists but may not be used is not a way out. Every claim
// below is about the chip describing a door the person themselves bolted:
// getting these wrong tells somebody their setting did not take, which is the
// one mistake a privacy switch must never make.
check('offline by choice — the sockets are irrelevant',
  { lan: { peers: 3, listening: true }, radio: { connected: true, peer_links: 2 },
    connectivity: { mode: 'offline' } },
  { kind: 'off', count: undefined });

check('radio only — a LAN peer is not a way out',
  { lan: { peers: 2, listening: true }, radio: { connected: true, peer_links: 1 },
    connectivity: { mode: 'radio' } },
  { kind: 'radio', count: 1 });

// Radio-only with no radio is genuinely nowhere, and must say so rather than
// falling back to the network the policy just forbade.
check('radio only, nothing plugged in',
  { lan: { peers: 2, listening: true }, radio: {}, connectivity: { mode: 'radio' } },
  { kind: 'off', count: undefined });

check('internet only — an attached modem stays off the air',
  { lan: { peers: 2, listening: true }, radio: { connected: true, peer_links: 1 },
    connectivity: { mode: 'internet' } },
  { kind: 'direct', count: 2 });

// A stored mode this build cannot read means the node is holding everything.
// "Not connected" would send somebody hunting for a fault they do not have.
check('a policy this build cannot read',
  { lan: { peers: 2, listening: true }, radio: {},
    connectivity: { mode: 'quantum', unreadable: true } },
  { kind: 'off', count: undefined });

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

// THE ROOM BESIDE THE RELAY (T6-LAN). Two devices authenticated on the local
// wire carry each other's copies directly; the relay keeps the mark — it
// reaches the people who are not here — but the words must say the room is
// carrying too. Zero authenticated peers must leave the text exactly as it
// always was: sockets are not people, and a mere listener names nobody.
{
  const withRoom = relayVerdict('direct', healthy, 2);
  if (!withRoom || withRoom.text !== 'conn.relay_room' || withRoom.kind !== 'relay') {
    failures++;
    console.log(`FAIL a healthy relay with 2 authenticated local peers must say the room is carrying
  got  ${withRoom && withRoom.text}`);
  } else {
    console.log('ok   relay + 2 in the room — the words carry both facts');
  }
  const alone = relayVerdict('off', healthy, 0);
  if (!alone || alone.text !== 'relay') {
    failures++;
    console.log('FAIL zero local peers must not change the relay text');
  } else {
    console.log('ok   relay alone reads exactly as before');
  }
}

// A relay silenced BY POLICY is not a verdict about the relay. It used to
// arrive as last_error — "relay is not permitted in offline mode" — in the
// same field as a dead socket, and the chip drew the red light for a choice
// the person made on purpose. The node now says `blocked` and means it.
verdict('the relay is silent because it was told to be',
  'off', { active: true, blocked: true }, 'off');

if (failures) {
  console.log(`\n${failures} claim(s) the chip makes are not true`);
  process.exit(1);
}
console.log('\nthe chip says what is actually carrying');
