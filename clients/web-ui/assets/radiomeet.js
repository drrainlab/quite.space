// Meeting somebody over the radio — the screen for a field, not a flat.
//
// Every other way into a space needs a rendezvous somewhere else: a relay to
// leave a pass at, or a link pasted between machines. Standing outdoors with
// two radios there is neither, so the radio carries the introduction as well
// as the conversation.
//
// The screen is written around one honesty that must not be softened:
// ANNOUNCING IS PUBLIC. It puts this device's name and public key on the air,
// where everyone in range hears it. That is fine — it is what somebody needs
// in order to seal an invitation, and a radio exchange is observable anyway —
// but it is a decision, so it is a button and never a background behaviour.
//
// @ts-check

let rmTimer = null;

function openRadioMeet() {
  const dlg = /** @type {HTMLDialogElement} */ (document.getElementById('dlgRadioMeet'));
  if (!dlg) return;
  dlg.showModal();
  const intro = document.getElementById('rmIntro');
  if (intro) intro.textContent = t('radio.meet.intro');
  const info = document.getElementById('rmInfo');
  if (info) info.textContent = '';
  refreshRadioMeet();
  clearInterval(rmTimer);
  // Somebody on the other side is pressing buttons while this is open, so the
  // list has to move on its own. Four seconds, the same cadence the gateway
  // screen already uses.
  rmTimer = setInterval(refreshRadioMeet, 4000);
  dlg.addEventListener('close', () => clearInterval(rmTimer), { once: true });
}

async function refreshRadioMeet() {
  const box = document.getElementById('rmBody');
  if (!box) return;
  let near, offers, st;
  try {
    [near, offers, st] = await Promise.all([
      api('/api/radio/neighbours'),
      api('/api/radio/invitations'),
      api('/api/status'),
    ]);
  } catch (err) {
    box.innerHTML = `<p class="hint">${esc(t('radio.meet.unavailable', { why: err.message }))}</p>`;
    return;
  }
  const html = [
    rmOffersBlock(offers.invitations || []),
    rmNeighboursBlock(near.neighbours || []),
    rmAirBlock(st && st.radio),
  ].join('');
  // ONLY when it changed.
  //
  // This redrew everything every four seconds, so a click that landed in the
  // same instant hit a button that no longer existed — pressing "invite" and
  // getting nothing at all. The sidebar learned this in NAV-1 and wrote it
  // down ("a wholesale innerHTML every two seconds destroys focus, drag
  // state, an open menu"); this screen repeated it anyway.
  if (html !== box.dataset.sig) {
    box.innerHTML = html;
    box.dataset.sig = html;
  }
}

// What the radio is actually doing.
//
// Without this the screen was silent between pressing a button and something
// appearing — and on LoRa that gap is half a minute, which is long enough to
// conclude nothing happened. A person waiting deserves to see the difference
// between "in flight" and "nothing is moving", and those are exactly the two
// states the transfer counters distinguish.
//
// It takes the CARRIER-NEUTRAL status, not the Meshtastic one: this screen is
// where somebody standing in a field looks to find out whether anything is
// moving, and on an RNode the Meshtastic diagnostic is empty by construction —
// so it answered "no radio is connected" while the radio was transmitting.
function rmAirBlock(radio) {
  if (!radio) return '';
  const head = `<div class="gw-section"><h4>${esc(t('radio.meet.air'))}</h4>`;
  if (!radio.connected) {
    return head + `<p class="hint">${esc(t('radio.meet.no_radio'))}</p></div>`;
  }
  const tr = radio.transfer;
  if (!tr) {
    return head + `<p class="hint">${esc(t('radio.meet.no_seed'))}</p></div>`;
  }
  // The denominator is the OUTCOMES — confirmed and unconfirmed. gaveUp is a
  // REASON that sums into unconfirmed, so subtracting it here would count a
  // finished transfer twice and leave one that stopped on a deadline looking
  // like it was still in flight, forever.
  const done = tr.completed + (tr.unconfirmed || 0);
  const line = tr.attempted > done
    ? t('radio.meet.in_flight', { done: tr.completed, all: tr.attempted })
    : t('radio.meet.idle', { done: tr.completed });
  return head + `<div class="gw-kv"><span>${esc(t('radio.meet.messages'))}</span>
    <span>${esc(line)}</span></div>
    <div class="gw-kv"><span>${esc(t('radio.meet.frames'))}</span>
    <span class="mono">${tr.framesOut} out · ${tr.framesIn} in</span></div>
    ${tr.gaveUp ? `<p class="hint">${esc(t('radio.meet.gave_up', { n: tr.gaveUp }))}</p>` : ''}
    </div>`;
}

// Invitations come FIRST, because an invitation is somebody waiting for an
// answer and a neighbour list is not.
function rmOffersBlock(offers) {
  if (!offers.length) return '';
  const rows = offers.map(o => `
    <div class="gw-row">
      <span>${esc(t('radio.meet.offer', { from: o.from || '?', space: o.title || o.space.slice(0, 8) }))}</span>
      <button class="btn-tinted" onclick="acceptRadioOffer('${esc(o.id)}')">${esc(t('radio.meet.accept'))}</button>
    </div>`).join('');
  return `<div class="gw-section"><h4>${esc(t('radio.meet.offers'))}</h4>
    <p class="hint">${esc(t('radio.meet.offers_hint'))}</p>${rows}</div>`;
}

// What we can honestly say about reaching one neighbour.
//
// "Radio link established" and "Direct radio link" are DIFFERENT sentences and
// only the second requires observed zero hops — an addressed Meshtastic packet
// is exactly what a mesh forwards, so its arrival proves who, never how far.
// And "nobody answered" is not "they were here and went quiet": a person
// standing in a field is owed the one that is true.
function rmLinkLine(link) {
  if (!link) return '';
  switch (link.state) {
    case 'up':
      return link.direct ? t('radio.meet.link_direct') : t('radio.meet.link_up');
    case 'probing': return t('radio.meet.link_probing');
    case 'no_answer': return t('radio.meet.link_none');
    case 'gone': return t('radio.meet.link_gone');
  }
  return '';
}

// What became of an invitation already sent to this person.
//
// "queued" is deliberately absent: on this carrier queued and gone look
// identical for minutes, and a screen that shows one while meaning the other
// is the screen this wave exists because of.
function rmDeliveryLine(inv) {
  if (!inv) return '';
  if (inv.delivery === 'unheard') return t('radio.meet.unheard');
  if (inv.delivery === 'unconfirmed') return t('radio.meet.unconfirmed');
  if (inv.delivery === 'heard') return t('radio.meet.delivered');
  return '';
}

function rmNeighboursBlock(list) {
  const head = `<div class="gw-section"><h4>${esc(t('radio.meet.heard'))}</h4>`;
  if (!list.length) {
    return head + `<p class="hint">${esc(t('radio.meet.nobody'))}</p></div>`;
  }
  // The space that is OPEN is the secondary option, never the primary one.
  //
  // Meeting somebody over the radio means the two of you now have a line —
  // that is QL-1's rule (a link opens a NEW place, never one picked
  // implicitly) and QL-3's (a space with one other person in it IS that
  // person). Offering "whatever you happen to be looking at" made the result
  // depend on where a cursor was, and collapsed entirely when that space was
  // public, which has no epoch to seal an invitation to.
  const sp = spacesCache.find(s => s.id === current);
  const canAlsoInvite = sp && !(sp.visibility && sp.visibility !== 'private');
  const alsoName = canAlsoInvite
    ? ((sp.title && sp.title.trim()) ? sp.title.trim() : null) : null;

  const rows = list.map(n => {
    const who = n.name || t('radio.meet.unnamed');
    const also = canAlsoInvite
      ? `<div class="hint"><a href="#" data-dev="${esc(n.device)}"
          onclick="inviteOverRadio(this); return false">${
            esc(alsoName ? t('radio.meet.also_into', { space: alsoName })
                         : t('radio.meet.also_here'))}</a></div>`
      : '';
    const linkLine = rmLinkLine(n.link) || rmDeliveryLine(n.invitation);
    return `<div class="gw-row">
      <span>${esc(who)} <span class="dim mono">${esc(n.device.slice(0, 8))}</span>${also}${
        linkLine ? `<div class="hint">${esc(linkLine)}</div>` : ''}</span>
      <button class="btn-tinted" data-dev="${esc(n.device)}"
        onclick="startLineOverRadio(this)">${
          esc(t('radio.meet.start_line', { who }))}</button></div>`;
  }).join('');
  return head + `<p class="hint">${esc(t('radio.meet.heard_hint'))}</p>${rows}</div>`;
}

// The primary action: a new line, for the two of you.
async function startLineOverRadio(btn) {
  const info = document.getElementById('rmInfo');
  const device = btn && btn.dataset ? btn.dataset.dev : btn;
  if (info) info.textContent = t('radio.meet.offering');
  if (btn && btn.disabled !== undefined) {
    btn.disabled = true;
    btn.textContent = t('radio.meet.offering_short');
  }
  try {
    const r = await api('/api/radio/meet', {
      method: 'POST',
      body: JSON.stringify({ device }),
    });
    // PROBING is not a failure and not a line: the node asked, in one frame,
    // whether they can hear us, because an invitation is six frames and is
    // not worth spending blind. The neighbour row shows the answer when it
    // arrives, and pressing again then sends the offer.
    if (r && r.state === 'probing') {
      if (info) info.textContent = t('radio.meet.link_probing');
    } else if (r && r.space) {
      // The line exists here already; it becomes theirs when they accept.
      current = r.space;
      await refresh();
    }
  } catch (err) {
    if (info) info.textContent = t('radio.meet.failed', { why: err.message });
  }
  // The button is NEVER left disabled by this function.
  //
  // Its label used to be set here and then wiped by the four-second re-render,
  // so "going out…" reverted to "start a line" with nothing to show for it —
  // state living in a variable the renderer does not know about. What the
  // person needs to see lives in the MODEL now (the link state on the row and
  // the invitation's delivery state), and the renderer reads it.
  await refreshRadioMeet();
}

async function announceOnRadio() {
  const info = document.getElementById('rmInfo');
  try {
    await api('/api/radio/announce', { method: 'POST' });
    // QUEUED, not sent. The node hands it to the radio and the radio takes
    // its time — claiming it happened would be the same overstatement as
    // reporting handed-to-transport as delivery.
    if (info) info.textContent = t('radio.meet.announcing');
  } catch (err) {
    if (info) info.textContent = t('radio.meet.failed', { why: err.message });
    return;
  }
  refreshRadioMeet();
}

async function inviteOverRadio(btn) {
  const info = document.getElementById('rmInfo');
  const device = btn && btn.dataset ? btn.dataset.dev : btn;
  if (!current) {
    if (info) info.textContent = t('radio.meet.open_a_space');
    return;
  }
  // Say something BEFORE the request, not after. On this carrier the gap
  // between pressing and anything visible is tens of seconds, and a button
  // that looks inert is a button somebody presses again.
  if (info) info.textContent = t('radio.meet.offering');
  if (btn && btn.disabled !== undefined) {
    btn.disabled = true;
    btn.textContent = t('radio.meet.offering_short');
  }
  try {
    await api('/api/radio/invite', {
      method: 'POST',
      body: JSON.stringify({ space: current, device }),
    });
  } catch (err) {
    if (info) info.textContent = t('radio.meet.failed', { why: err.message });
    if (btn && btn.disabled !== undefined) btn.disabled = false;
  }
}

async function acceptRadioOffer(id) {
  const info = document.getElementById('rmInfo');
  try {
    await api('/api/radio/invitations/accept', {
      method: 'POST',
      body: JSON.stringify({ id }),
    });
    // Accepting is an ANSWER, not a join. The space arrives when the other
    // side grants it, which on this carrier is seconds away and sometimes
    // more — so this must not land anybody in a room that is not there yet.
    if (info) info.textContent = t('radio.meet.answered');
    await refresh();
  } catch (err) {
    if (info) info.textContent = t('radio.meet.failed', { why: err.message });
    return;
  }
  refreshRadioMeet();
}
