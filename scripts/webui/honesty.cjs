// What the honesty surfaces say, checked against what the node reports.
//
// Two functions, both claims about what a person CANNOT see: the line under
// the feed (honestyParts) and the relay diagnostics panel
// (renderRelayDiagnostics). Each case below is a state a node was actually
// in — the field case being a newcomer admitted into a space with
// memory=everything whose room stayed empty while the node's own API said
// "no route to N members" and the reader's log held frames behind a hole.
// Neither number reached a screen. These checks make sure they keep
// reaching it.
//
// It runs the real source rather than a copy: app.js is read, each function
// is cut out by name, and evaluated against stubs. A copy here would pass
// forever while the app drifted.
const fs = require('fs');
const path = require('path');

const src = fs.readFileSync(
  path.join(__dirname, '..', '..', 'clients', 'web-ui', 'assets', 'app.js'), 'utf8');

function cut(name) {
  const s = src.indexOf('function ' + name + '(');
  if (s < 0) throw new Error(name + ' is gone — this harness is stale');
  // An `async` prefix must travel with the body, or `await` inside it is a
  // syntax error in the evaluated copy.
  const start = src.lastIndexOf('async ', s) === s - 'async '.length ? s - 'async '.length : s;
  let d = 0, e = -1;
  for (let j = src.indexOf('{', s); j < src.length; j++) {
    if (src[j] === '{') d++;
    else if (src[j] === '}') { d--; if (d === 0) { e = j + 1; break; } }
  }
  return src.slice(start, e);
}

// The names the functions reach for, stubbed to something inspectable: t()
// renders `key|var=value,…` so a check can see both the key and the count.
const t = (key, vars) => vars
  ? key + '|' + Object.entries(vars).map(([k, v]) => `${k}=${v}`).join(',')
  : key;
const esc = (s) => String(s);
const relTime = (s) => `${s}s`;

const honestyParts = new Function('t', 'esc', 'relTime',
  cut('honestyParts') + '; return honestyParts;')(t, esc, relTime);

// A three-line DOM: enough for rows of label + value.
function element(tag) {
  return {
    tag, className: '', textContent: '', innerHTML: '', children: [],
    appendChild(c) { this.children.push(c); return c; },
    append(...cs) { this.children.push(...cs); },
  };
}
let panel;
const document = {
  getElementById: (id) => (id === 'relayDiagPanel' ? panel : null),
  createElement: element,
};
let diag = {};
const api = async () => diag;
const renderRelayDiagnostics = new Function('api', 't', 'document',
  cut('renderRelayDiagnostics') + '; return renderRelayDiagnostics;')(api, t, document);

let failures = 0;
function fail(what, detail) { failures++; console.log(`FAIL ${what}\n  ${detail}`); }
function ok(what) { console.log(`ok   ${what}`); }

// --- the line under the feed --------------------------------------------

function partsFor(st) { return honestyParts(st); }

{
  const parts = partsFor({ undecryptable: 0, pending: 0 });
  parts.length === 0 ? ok('a clean room says nothing') : fail('a clean room says nothing', JSON.stringify(parts));
}
{
  const parts = partsFor({ undecryptable: 0, pending: 2 });
  const hit = parts.find(p => p.includes('honesty.pending|count=2'));
  hit && parts.length === 1
    ? ok('two parked frames are counted, and named as held')
    : fail('two parked frames are counted', JSON.stringify(parts));
  hit && hit.includes('class="warn"')
    ? ok('the held count is a warning, like the no-key count')
    : fail('the held count is a warning', JSON.stringify(parts));
}
{
  const parts = partsFor({ undecryptable: 3, pending: 1 });
  parts.length === 2 && parts[0].includes('honesty.undecryptable|count=3') && parts[1].includes('honesty.pending|count=1')
    ? ok('no-key and no-predecessor are two separate sentences, in that order')
    : fail('two kinds of unreadable moment stay separate', JSON.stringify(parts));
}
{
  const parts = partsFor({ undecryptable: 0, pending: 1,
    observation: { display: '21.5 °C', freshness: 'stale', age_seconds: 90, simulated: false } });
  parts.length === 2 && parts[1].includes('21.5 °C') && parts[1].includes('90s')
    ? ok('the observation line still follows')
    : fail('the observation line still follows', JSON.stringify(parts));
}
{
  const parts = partsFor({});
  parts.length === 0 ? ok('a state with no counts at all is not a warning') : fail('undefined counts', JSON.stringify(parts));
}

// --- the relay diagnostics panel ----------------------------------------

async function rows(d) {
  panel = element('div');
  diag = d;
  await renderRelayDiagnostics();
  return panel.children.map(r => r.children.map(c => c.textContent));
}
function row(rs, label) { return rs.find(r => r[0] === label); }

(async () => {
  const base = { mode: 'automatic', primary: 'relay:one', sync_active: true };
  {
    const rs = await rows(base);
    !row(rs, 'relay.diag.no_route') && !row(rs, 'relay.diag.stranded')
      ? ok('a routed, unstranded node shows neither row')
      : fail('quiet rows stay absent', JSON.stringify(rs));
  }
  {
    const rs = await rows({ ...base, no_route_peers: 3 });
    const r = row(rs, 'relay.diag.no_route');
    r && r[1] === 'relay.diag.no_route_value|count=3'
      ? ok('three members without a route are shown, with the count')
      : fail('no-route members reach the screen', JSON.stringify(rs));
  }
  {
    const rs = await rows({ ...base, no_route_peers: 0 });
    !row(rs, 'relay.diag.no_route')
      ? ok('zero members without a route is not a row')
      : fail('zero is silence', JSON.stringify(rs));
  }
  {
    const rs = await rows({ ...base, stranded: [
      { space: 'aaaa0000', event: 'e1e1e1e1', device: 'd0d0d0d0', seq: 7, bytes: 300 * 1024 + 1, reason: 'x' },
      { space: 'aaaa0000', event: 'e2e2e2e2', device: 'd1d1d1d1', seq: 2, bytes: 1024, reason: 'x' },
    ] });
    const sr = rs.filter(r => r[0] === 'relay.diag.stranded');
    sr.length === 2 && sr[0][1] === 'relay.diag.stranded_value|seq=7,device=d0d0d0d0,kb=301'
      && sr[1][1] === 'relay.diag.stranded_value|seq=2,device=d1d1d1d1,kb=1'
      ? ok('each stranded frame is a row naming the chain it stalls')
      : fail('stranded frames reach the screen', JSON.stringify(rs));
  }
  {
    const rs = await rows({ ...base, no_route_peers: 1, last_error: 'dial tcp: refused' });
    const order = rs.map(r => r[0]);
    order.indexOf('relay.diag.error') < order.indexOf('relay.diag.no_route')
      ? ok('the hold rows come after the relay state, not among it')
      : fail('row order', JSON.stringify(order));
  }
  if (failures) { console.log(`\n${failures} failure(s)`); process.exit(1); }
  console.log('\nhonesty surfaces: all checks passed');
})().catch(e => { console.log('FAIL harness threw: ' + (e.stack || e)); process.exit(1); });
