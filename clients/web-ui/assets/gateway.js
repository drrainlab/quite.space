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

function openGateway() {
  dlgMesh.showModal();
  refreshGateway();
  clearInterval(gwTimer);
  // A radio that is reconnecting changes state on its own; a screen that
  // only updated on open would show a stale problem.
  gwTimer = setInterval(refreshGateway, 4000);
  dlgMesh.addEventListener('close', () => clearInterval(gwTimer), { once: true });
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
  box.innerHTML = [
    gwAdvice(g), gwRadioBlock(g), gwProfileBlock(g),
    gwPresenceBlock(g), gwPinsBlock(g),
  ].filter(Boolean).join('');
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
  let state, cls;
  if (!r.attached) { state = 'no radio attached'; cls = 'off'; }
  else if (r.connected) { state = 'connected'; cls = 'on'; }
  else if (r.reconnecting) {
    state = 'reconnecting' + (r.nextRetryIn ? ` — next attempt in ${r.nextRetryIn}` : '');
    cls = 'warn';
  } else { state = 'not responding'; cls = 'off'; }

  const rows = [row('radio', `<span class="gw-${cls}">${esc(state)}</span>`)];
  if (r.nodeNum) rows.push(row('node', `<code>${esc(r.nodeNum)}</code>`));
  if (r.attached) rows.push(row('packets', `${r.tx} out · ${r.rx} in`));
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
