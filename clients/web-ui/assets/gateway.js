// The Gateway screen (RB-2). What to look at when the radio is not working.
//
// Ordered the way a person actually asks: is my radio there · is it set up
// for this segment · is a gateway out there · do I trust it. Each answer
// carries what to do next, because "not connected" on its own has never
// helped anyone standing next to a radio.
//
// It never shows a channel key. The node hashes those where it decodes them
// and the plaintext never reaches this far — see transports/meshtastic.

let gwTimer = null;
// The minted channel, held in memory for this screen only. Never persisted:
// it contains the key, and the whole design of the prepare flow is that we
// are the key's origin and never its keeper.
let GW_PREPARED = null;

// The radio lives in ONE place — the Radio tab of Settings — reachable from
// the header chip and from Settings alike. It used to live in a dialog of its
// own, with the Settings tab holding a paragraph and a button that closed
// Settings to open it. That button existed only because the content was
// somewhere else, which is a step a person pays for and nobody chose.
function openGateway() {
  openSettings();
  showSettingsCat('radio'); // which arms the poll — see armGatewayPoll
}

async function refreshGateway() {
  const box = document.getElementById('gwBody');
  if (!box) return;
  let g;
  try {
    g = await api('/api/gateway');
  } catch (err) {
    box.innerHTML = `<p class="hint">could not read gateway status: ${esc(err.message)}</p>`;
    return;
  }
  // A RENDER FAILURE MUST NOT LOOK LIKE A HUNG RADIO.
  //
  // One of these blocks threw — a variable read in a scope that did not have
  // it — and because the whole screen is built in one expression, nothing was
  // written at all. The scan had already come back; the button simply stayed
  // on "scanning…" because no repaint ever happened. That is indistinguishable
  // from a serial port that never answered, and it was reported as exactly
  // that, twice, sending both of us to look at the radio layer.
  //
  // So the failure says what it is, and says it where the screen is.
  try {
    box.innerHTML = [
      gwAdvice(g), gwScanBlock(g), gwAppliedBlock(), gwPrepareBlock(g), gwRadioBlock(g), gwProfileBlock(g),
      gwPresenceBlock(g), gwPinsBlock(g),
    ].filter(Boolean).join('');
  } catch (err) {
    GW_SCANNING = false; // or the button stays disabled on a dead screen
    box.innerHTML = `<p class="hint">This screen could not draw itself: ${
      esc(err && err.message ? err.message : String(err))}. The radio is not
      the problem — nothing above was read from it.</p>`;
    throw err; // still surfaces in the console, where the stack is
  }
  if (GW_PREPARED) renderPrepared();
}

// Advice comes first and is the only part written as sentences: everything
// below it is evidence for what it says.
function gwAdvice(g) {
  if (!g.advice || !g.advice.length) return '';
  return `<div class="gw-advice">${g.advice.map(a =>
    `<p>${esc(a)}</p>`).join('')}</div>`;
}

function gwRadioBlock(g) {
  const r = g.radio;
  // With no radio and nothing that went wrong there is exactly one row to
  // draw — "no radio attached" — and the state line at the top of the screen
  // has already said it. A block whose whole content is a repetition is not
  // evidence, it is noise.
  if (!r.attached && !r.err && !r.attempts && !g.network && !g.config) return '';
  let state, cls;
  if (!r.attached) { state = 'no radio attached'; cls = 'off'; }
  else if (r.connected) { state = 'connected'; cls = 'on'; }
  else if (r.reconnecting) {
    state = 'reconnecting' + (r.nextRetryIn ? ` — next attempt in ${r.nextRetryIn}` : '');
    cls = 'warn';
  } else { state = 'not responding'; cls = 'off'; }

  const rows = [row('radio', `<span class="gw-${cls}">${esc(state)}</span>`)];
  // The carrier is a DIAGNOSTIC, shown where diagnostics belong. Nobody is
  // being asked to pick one — that is the doctrine, and this screen is the
  // only place in the interface that names a driver at all.
  if (r.carrier) rows.push(row('carrier', `<code>${esc(r.carrier)}</code>`));
  if (r.nodeNum) rows.push(row('node', `<code>${esc(r.nodeNum)}</code>`));
  // Packet counters are Meshtastic's; a modem this device drives has none,
  // and showing 0 out / 0 in for it would be a reading that was never taken.
  if (r.attached && r.carrier !== 'rnode') {
    rows.push(row('packets', `${r.tx} out · ${r.rx} in`));
  }
  if (r.attached) {
    rows.push(row('', `<button class="btn-plain" onclick="gwDetachRadio()">${
      esc(t('radio.detach'))}</button>`));
  }
  // Reconnects are shown only once there have been some: on a healthy bench
  // link this line is noise, and on a flaky one it is the whole story.
  if (r.reconnects > 0 || r.attempts > 0) {
    rows.push(row('reconnects', `${r.reconnects} (after ${r.attempts} attempts)`));
  }
  if (r.err) rows.push(row('last error', `<span class="gw-off">${esc(r.err)}</span>`));
  if (g.network) rows.push(row('network', esc(g.network)));

  const c = g.config;
  if (c) {
    if (c.firmware) rows.push(row('firmware', esc(c.firmware)));
    if (c.region) rows.push(row('region', esc(c.region)));
    if (c.preset) rows.push(row('preset', esc(c.preset)));
    rows.push(row('hop limit', c.hopLimit));
    rows.push(row('transmit', c.txEnabled ? 'enabled'
      : '<span class="gw-off">DISABLED</span>'));
    (c.channels || []).forEach(ch => {
      const fp = ch.fingerprint ? ` <span class="dim mono">[${esc(ch.fingerprint)}]</span>` : '';
      rows.push(row(`channel ${ch.index}`,
        `${esc(ch.name || '(unnamed)')} · ${esc(ch.role)} · key ${esc(ch.key)}${fp}`));
    });
  } else if (r.connected) {
    rows.push(row('settings', '<span class="dim">this node reported none</span>'));
  }
  return section('radio', rows.join(''));
}

function gwProfileBlock(g) {
  if (!g.profile || !g.profile.length) return '';
  const head = {
    wrong: '<span class="gw-off">this radio is not on the same air as the segment</span>',
    unverified: '<span class="gw-warn">everything reported agrees; some fields could not be checked</span>',
    ok: '<span class="gw-on">configured for the segment</span>',
  }[g.profileVerdict] || '';

  const rows = g.profile.map(c => {
    if (c.status === 'ok') {
      return row(c.field, `<span class="gw-on">ok</span> ${esc(c.got)}`);
    }
    if (c.status === 'unknown') {
      return row(c.field, `<span class="dim">not reported — ${esc(c.why)}</span>`);
    }
    return row(c.field,
      `<span class="gw-off">is ${esc(c.got)}, segment needs ${esc(c.want)}</span>` +
      `<div class="hint">${esc(c.why)}</div>` +
      `<div class="gw-fix"><code>${esc(c.fix)}</code></div>`);
  });
  return section('against the segment profile', `<p>${head}</p>` + rows.join(''));
}

function gwPresenceBlock(g) {
  if (!g.gateways || !g.gateways.length) {
    // Who is on the mesh is a question about a mesh this device is not on.
    // Once a radio is attached the same emptiness IS the reading, and it is
    // shown.
    if (!g.radio || !g.radio.attached) return '';
    return section('gateways', `<p class="hint">${esc(g.summary || '')}</p>`);
  }
  const rows = g.gateways.map(gw => {
    const name = gw.label || `gateway ${gw.fingerprint}`;
    const bits = [];
    bits.push(gw.trusted
      ? '<span class="gw-on">trusted</span>'
      : '<span class="gw-warn">not trusted</span>');
    bits.push(gw.uplinkUp
      ? 'internet uplink up'
      : '<span class="gw-warn">no internet uplink</span>');
    if (gw.queue) bits.push(`${gw.queue} in its queue`);
    if (!gw.fresh) bits.push(`<span class="gw-off">gone quiet, last heard ${esc(gw.lastHeard)}</span>`);

    // The pin button carries the FINGERPRINT, not a key: the node pins only
    // what it has actually heard, so this cannot be used to trust something
    // that never announced itself.
    const action = gw.trusted
      ? `<button class="btn-plain" onclick="gwUnpin('${esc(gw.link)}')">unpin</button>`
      : `<button class="btn-tinted" onclick="gwPin('${esc(gw.fingerprint)}')">pin this gateway</button>`;
    return `<div class="gw-row"><div><b>${esc(name)}</b>
      <span class="dim mono">${esc(gw.fingerprint)}</span><br>
      <span class="hint">${bits.join(' · ')}</span></div>${action}</div>`;
  });
  return section('gateways', rows.join(''));
}

function gwPinsBlock(g) {
  const rows = (g.pins || []).map(p => {
    let line = `<b>${esc(p.link)}</b> <span class="mono">${esc(p.fingerprint)}</span>`;
    if (p.label) line += ` — ${esc(p.label)}`;
    if (p.pinnedAt) line += `<br><span class="hint">pinned ${esc(p.pinnedAt.slice(0, 16).replace('T', ' '))}`;
    // A replaced pin is a trust decision that CHANGED. Saying so is the
    // whole point of keeping the old fingerprint.
    if (p.replaced) line += `, replacing ${esc(p.replaced)}`;
    if (p.pinnedAt) line += '</span>';
    return `<div class="gw-row"><div>${line}</div></div>`;
  });
  const warn = (g.pinWarnings || []).map(w =>
    `<p class="gw-off">${esc(w)}</p>`).join('');
  if (!rows.length && !warn) {
    // Same rule as the presence block: nothing is pinned and there is no
    // radio, so there is nothing this could be about. A pin that DOES exist
    // is shown either way — it outlives the radio it was made for.
    if (!g.radio || !g.radio.attached) return '';
    return section('trusted gateways', '<p class="hint">None. Until a gateway ' +
      'is pinned, its receipts prove nothing: messages still travel, but ' +
      'nothing can confirm anyone took them on.</p>');
  }
  return section('trusted gateways', warn + rows.join(''));
}

async function gwPin(fingerprint) {
  try {
    await api('/api/gateway/pin', {
      method: 'POST', body: JSON.stringify({ fingerprint }),
    });
    refreshGateway();
  } catch (err) { alert(err.message); }
}

async function gwUnpin(link) {
  if (!confirm(`Stop trusting the gateway on "${link}"?\n\n` +
    'Its custody receipts will go back to proving nothing. Messages still ' +
    'travel; nothing will confirm anyone took them on.')) return;
  try {
    await api('/api/gateway/unpin', {
      method: 'POST', body: JSON.stringify({ link }),
    });
    refreshGateway();
  } catch (err) { alert(err.message); }
}

function section(title, inner) {
  return `<div class="gw-section"><h4>${esc(title)}</h4>${inner}</div>`;
}

function row(label, value) {
  return `<div class="gw-kv"><span class="dim">${esc(label)}</span><span>${value}</span></div>`;
}


// ---- Prepare this device for a segment ----
//
// Mints a channel and shows it as a link and QR. It does NOT write to the
// radio: importing is one tap in the Meshtastic app, and we then verify what
// actually landed by reading the radio back.

function gwPrepareBlock(g) {
  if (!g.radio.connected) return '';
  if (GW_PREPARED) return section('segment channel', '<div id="gwPrepared"></div>');
  const named = (g.profile && g.profile.length)
    ? '<p class="hint">A segment profile is already installed. Preparing again mints a NEW channel with a new key — every radio in the segment would have to import it.</p>'
    : '<p class="hint">Create a channel of your own for this segment, so its traffic is not on the public channel everyone in range shares.</p>';
  return section('prepare this device', named +
    `<div class="row">
       <input id="gwSegName" placeholder="channel name" maxlength="11" value="pinelover">
       <button class="btn-tinted" onclick="gwPrepare()">Prepare</button>
     </div>
     <p class="hint">Up to 11 characters — the radio firmware refuses longer names rather than shortening them.</p>`);
}

async function gwPrepare() {
  const el = document.getElementById('gwSegName');
  const name = (el ? el.value : '').trim();
  if (!name) return;
  try {
    GW_PREPARED = await api('/api/gateway/prepare', {
      method: 'POST', body: JSON.stringify({ name }),
    });
    refreshGateway();
  } catch (err) { alert(err.message); }
}

function renderPrepared() {
  const host = document.getElementById('gwPrepared');
  if (!host || !GW_PREPARED) return;
  const p = GW_PREPARED;
  host.innerHTML = `
    <p><b>${esc(p.channel)}</b> \u00b7 slot ${p.index} \u00b7 ${esc(p.region)} \u00b7 ${esc(p.preset)} \u00b7 hop ${p.hopLimit}</p>
    ${p.warnings.map(w => `<p class="gw-secret">${esc(w)}</p>`).join('')}
    ${p.qrPngBase64 ? `<div class="gw-qr"><img alt="channel QR" src="data:image/png;base64,${p.qrPngBase64}"></div>` : ''}
    <div class="gw-fix"><code id="gwUrl">${esc(p.url)}</code></div>
    <div class="row">
      <button onclick="gwCopy('gwUrl')">copy link</button>
      <button onclick="gwAdoptProfile()">use this segment on this node</button>
      <button class="btn-plain" onclick="gwForget()">forget</button>
    </div>
    <h4>steps</h4>
    <ol class="gw-steps">${p.steps.map(s => `<li>${esc(s)}</li>`).join('')}</ol>
    ${cmdBlock('or from a terminal (adds, never replaces)', p.addCommands)}
    ${cmdBlock('radio settings, if this one does not match', p.regionCommands)}
    <h4>segment profile</h4>
    <p class="hint">Safe to send: it carries the fingerprint, not the key. Whoever joins checks their radio against it.</p>
    <div class="gw-fix"><code id="gwProf">${esc(p.profile)}</code></div>
    <div class="row"><button onclick="gwCopy('gwProf')">copy profile</button></div>`;
}

function cmdBlock(title, cmds) {
  if (!cmds || !cmds.length) return '';
  return `<h4>${esc(title)}</h4>` +
    cmds.map(c => `<div class="gw-fix"><code>${esc(c)}</code></div>`).join('');
}

async function gwAdoptProfile() {
  if (!GW_PREPARED) return;
  try {
    const r = await api('/api/gateway/profile', {
      method: 'POST', body: JSON.stringify({ profile: GW_PREPARED.profile }),
    });
    alert(`This node now uses segment "${r.name}" on channel ${r.channel}.\n\n` +
      'Import the link on the radio too, then press refresh to verify.');
    refreshGateway();
  } catch (err) { alert(err.message); }
}

// Forget drops the key from memory. It was never stored anywhere else.
function gwForget() {
  if (!confirm('Forget this link?\n\nThe key is only held on this screen and is ' +
    'not stored anywhere. Once forgotten it cannot be shown again, and any radio ' +
    'that has not imported it yet will need a newly prepared channel.')) return;
  GW_PREPARED = null;
  refreshGateway();
}

function gwCopy(id) {
  const el = document.getElementById(id);
  if (!el) return;
  navigator.clipboard.writeText(el.textContent).catch(() => {
    const r = document.createRange();
    r.selectNodeContents(el);
    const sel = window.getSelection();
    sel.removeAllRanges();
    sel.addRange(r);
  });
}


// One-button setup: write to the radio, reboot, re-read, report.
//
// "Applied" and "verified" are shown as separate facts. Having sent a write
// is not evidence that it took, and a button that conflates the two is how a
// person ends up trusting a radio that is not configured.
async function gwApply() {
  const el = document.getElementById('gwSegName');
  const name = (el ? el.value : '').trim();
  if (!name) return;
  if (!confirm(`Set up this radio for segment "${name}"?\n\n` +
    'A new channel with a fresh private key is added to the first free slot. ' +
    'Channels already on the radio are left alone. The radio then reboots, ' +
    'which takes about half a minute.')) return;

  const host = document.getElementById('gwBody');
  host.innerHTML = `<p class="hint">Writing to the radio, then waiting for it to
    reboot and come back. This takes up to a minute \u2014 leave it plugged in.</p>`;
  clearInterval(gwTimer); // do not let the poll redraw over the progress
  try {
    const r = await api('/api/gateway/apply', {
      method: 'POST', body: JSON.stringify({ name }),
    });
    GW_APPLIED = r;
  } catch (err) {
    GW_APPLIED = { applied: false, verified: false, note: err.message, report: [] };
  }
  gwTimer = setInterval(refreshGateway, 4000);
  refreshGateway();
}

let GW_APPLIED = null;

function gwAppliedBlock() {
  if (!GW_APPLIED) return '';
  const r = GW_APPLIED;
  const verdict = r.verified
    ? '<span class="gw-on">the radio reports exactly what was asked for</span>'
    : (r.applied
        ? '<span class="gw-warn">written, but not confirmed</span>'
        : '<span class="gw-off">not applied</span>');
  return section('setup result', `<p>${verdict}</p>` +
    (r.note ? `<p class="hint">${esc(r.note)}</p>` : '') +
    (r.report || []).map(line => {
      const cls = line.startsWith('ok') ? 'gw-on'
        : line.startsWith('WRONG') ? 'gw-off' : 'dim';
      return `<div class="gw-kv"><span class="${cls}">${esc(line)}</span></div>`;
    }).join('') +
    (r.profile ? `<h4>segment profile \u2014 send this to whoever joins</h4>
       <div class="gw-fix"><code id="gwAppliedProf">${esc(r.profile)}</code></div>
       <div class="row"><button onclick="gwCopy('gwAppliedProf')">copy profile</button>
       <button class="btn-plain" onclick="GW_APPLIED=null;refreshGateway()">dismiss</button></div>`
      : `<div class="row"><button class="btn-plain" onclick="GW_APPLIED=null;refreshGateway()">dismiss</button></div>`));
}


// ---- Finding the radio ----
//
// Device paths are not stable: a T3-S3 that enumerates by its MAC comes back
// by USB location after a reset. Typing a path is asking someone to track
// something that changes underneath them, so we look instead — and show what
// answered on every port, including the ones we chose not to open.

let GW_SCAN = null;
let GW_SCANNING = false;

function gwScanBlock(g) {
  const head = g.radio.attached
    ? ''
    // The state line above already says a radio is needed and what kind. All
    // that is left to say here is the one thing this button does for you.
    : `<p class="hint">The device path is found for you \u2014 it changes on its
       own after a reset.</p>`;
  const btn = `<div class="row">
      <button class="btn-tinted" onclick="gwScan()" ${GW_SCANNING ? 'disabled' : ''}>
        ${GW_SCANNING ? 'scanning\u2026' : 'Scan for radios'}</button>
    </div>`;
  return section('find a radio', head + btn + '<div id="gwScanOut">' +
    // Say the real number. Measured at seven to eight seconds with two boards
    // on the desk: every port is opened, given a window to identify itself,
    // and the quiet ones are asked a second time before being called quiet.
    // "A few seconds" was the old text, and a wait that runs to double what
    // was promised is how a working thing gets reported as hung.
    (GW_SCANNING ? '<p class="hint">Opening every serial port and waiting for '
      + 'each to say what it is. Around ten seconds — a port that stays quiet '
      + 'is asked twice before it is called quiet.</p>'
                 : gwScanResults(!!(g.radio && g.radio.knownSegment))) + '</div>');
}

// knownSegment is PASSED IN, not reached for.
//
// It was read off a `g` that does not exist in this scope, which threw inside
// a .map() — so the results block never rendered and the button sat on
// "scanning…" forever. The scan itself had already come back. A person cannot
// tell that apart from a hung radio, and reported it as one.
function gwScanResults(knownSegment) {
  if (!GW_SCAN) return '';
  const icon = { radio: '\u25cf', rnode: '\u25cf', attached: '\u25cf', busy: '\u25cb',
    foreign: '\u25cb', silent: '\u25cb', skipped: '\u00b7' };
  const cls  = { radio: 'gw-on', rnode: 'gw-on', attached: 'gw-on', busy: 'gw-warn',
    foreign: 'dim', silent: 'dim', skipped: 'dim' };
  const rows = (GW_SCAN.ports || []).map(p => {
    const bits = [];
    if (p.firmware) bits.push('firmware ' + p.firmware);
    if (p.region) bits.push(p.region);
    if (p.preset) bits.push(p.preset);
    if (p.channels) bits.push(p.channels + ' channels');
    if (p.primaryKey) bits.push('primary key ' + p.primaryKey);
    // An RNode is attached with a SEGMENT PHRASE, a Meshtastic node without
    // one — two carriers, two different questions, one row shape.
    const action = (p.kind === 'radio')
      ? `<button class="btn-tinted" onclick="gwAttach('${esc(p.port)}')">attach</button>`
      : (p.kind === 'rnode')
        ? `<button class="btn-tinted" onclick="gwAttachRNode('${esc(p.port)}', ${
            knownSegment ? 'true' : 'false'})">attach</button>`
        : '';
    return `<div class="gw-row"><div>
      <span class="${cls[p.kind] || 'dim'}">${icon[p.kind] || '\u00b7'}</span>
      <b class="mono">${esc(p.port)}</b><br>
      <span class="hint">${esc(p.detail || p.kind)}</span>
      ${bits.length ? `<br><span class="hint">${esc(bits.join(' \u00b7 '))}</span>` : ''}
    </div>${action}</div>`;
  }).join('');
  return (GW_SCAN.note ? `<p class="hint">${esc(GW_SCAN.note)}</p>` : '') + rows;
}

async function gwScan() {
  GW_SCANNING = true;
  refreshGateway();
  try {
    GW_SCAN = await api('/api/gateway/scan', { method: 'POST', body: '{}' });
  } catch (err) {
    GW_SCAN = { ports: [], note: 'scan failed: ' + err.message };
  }
  GW_SCANNING = false;
  refreshGateway();
}

// Attaching a modem needs the words its segment shares.
//
// The phrase is asked for HERE rather than stored in settings, because it is
// not a preference: it is the secret that makes two radios the same segment,
// and every radio on one derives the same frame key from it. A prompt is
// plain, and being plain is the point — nothing about this is guessable, and
// a wrong phrase produces a node that hears nobody and is told nothing.
async function gwAttachRNode(port, knownSegment) {
  // Nothing is asked when the segment already arrived with an invitation.
  // That is the payoff of carrying it: the person being invited never had the
  // words, and should never be made to go and get them — least of all at the
  // moment the internet is the thing that stopped working.
  let phrase = '';
  if (!knownSegment) {
    phrase = prompt(t('radio.attach.ask'));
    if (phrase === null) return;
  }
  try {
    await api('/api/radio/attach', {
      method: 'POST', body: JSON.stringify({ port, phrase }),
    });
    GW_SCAN = null;
    refreshGateway();
  } catch (err) { alert(err.message); }
}

// Detach releases the port and FORGETS the radio, so the next start does not
// bring back something somebody just switched off.
async function gwDetachRadio() {
  if (!confirm(t('radio.detach.confirm'))) return;
  try {
    await api('/api/radio/detach', { method: 'POST', body: '{}' });
    GW_SCAN = null;
    refreshGateway();
  } catch (err) { alert(err.message); }
}

async function gwAttach(port) {
  try {
    await api('/api/gateway/attach', {
      method: 'POST', body: JSON.stringify({ port }),
    });
    GW_SCAN = null;
    refreshGateway();
  } catch (err) { alert(err.message); }
}

// The Radio tab's poll, armed and disarmed by whatever reveals or hides it.
// Separate from openGateway so the tab behaves the same however it was
// reached — from the header chip, from the Settings tabs, or by a restored
// tab on a later open.
function armGatewayPoll() {
  clearInterval(gwTimer);
  gwTimer = setInterval(refreshGateway, 4000);
  dlgSettings.addEventListener('close', stopGatewayPoll, { once: true });
}

function stopGatewayPoll() { clearInterval(gwTimer); }
