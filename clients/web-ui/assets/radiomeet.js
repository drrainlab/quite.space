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

/** Read a button's own data- value instead of baking it into the handler.
 *
 * Everything on this screen — device ids, space ids, the name a peer calls
 * itself — arrived over the air and is attacker-shaped by definition. An
 * attribute value is escaped HTML and stays data; a handler body is code
 * the browser HTML-decodes before compiling, which is exactly how an
 * escaped quote turns back into a real one. So the values live in the
 * attribute and the handler reads them out.
 *
 * @param {Element} el
 * @param {string} name
 * @returns {string} */
function rmArg(el, name) {
  return (el instanceof HTMLElement && el.dataset[name]) || '';
}

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
  let near, offers, st, spaces;
  try {
    // The spaces list is fetched HERE rather than read from spacesCache: this
    // sheet refreshes on its own four-second timer, and the moment a line
    // actually arrives is the moment worth reporting. Reading a cache filled
    // by somebody else's schedule would announce it late or not at all.
    [near, offers, st, spaces] = await Promise.all([
      api('/api/radio/neighbours'),
      api('/api/radio/invitations'),
      api('/api/status'),
      api('/api/spaces'),
    ]);
  } catch (err) {
    box.innerHTML = `<p class="hint">${esc(t('radio.meet.unavailable', { why: err.message }))}</p>`;
    return;
  }
  const held = new Set((spaces || []).map(s => s.id));
  const html = [
    rmArrivedBlock(held),
    rmOffersBlock(offers.invitations || []),
    rmNeighboursBlock(near.neighbours || [], held),
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

// What this device accepted and is still waiting to receive.
//
// Keyed by space id, so a second accept of the same offer is not a second
// wait. Memory only: if the sheet is closed the space still arrives, and the
// sidebar is where it shows up — this is an announcement, not the mechanism.
const RM_AWAITING = new Map();

// SAY WHEN IT LANDED.
//
// Accepting answers "your answer is on the air. The space appears here when
// they grant it" — and then, when it did appear, the screen said nothing at
// all. A person who has been told to wait for a thing must be told when the
// thing happens; otherwise the only way to find out is to go and look
// somewhere else, which is exactly what they were asked not to do.
function rmArrivedBlock(held) {
  const arrived = [];
  for (const [space, who] of RM_AWAITING) {
    if (held.has(space)) {
      arrived.push({ space, who });
      RM_AWAITING.delete(space);
    }
  }
  if (!arrived.length) return '';
  // Ids and names here came off the air — a radio offer is whatever a
  // neighbour transmitted. They ride in data- attributes and are read back
  // with rmArg(this), never interpolated into the handler: esc() escapes
  // for HTML, and an onclick body is HTML-decoded BEFORE it is compiled as
  // JavaScript, so an escaped quote would have become a real one there.
  // The data-dev/startLineOverRadio(this) rows below already worked this
  // way; these three did not.
  return arrived.map(a => `<div class="gw-section"><h4>${
    esc(t('radio.meet.arrived_title'))}</h4>
    <p class="hint">${esc(t('radio.meet.arrived', { who: a.who }))}</p>
    <div class="row"><button class="btn-tinted" data-space="${esc(a.space)}"
      onclick="rmOpenSpace(rmArg(this,'space'))">${
      esc(t('radio.meet.open_line'))}</button></div></div>`).join('');
}

// Invitations come FIRST, because an invitation is somebody waiting for an
// answer and a neighbour list is not.
function rmOffersBlock(offers) {
  if (!offers.length) return '';
  const rows = offers.map(o => `
    <div class="gw-row">
      <span>${esc(t('radio.meet.offer', { from: o.from || '?', space: o.title || o.space.slice(0, 8) }))}</span>
      <button class="btn-tinted" data-oid="${esc(o.id)}" data-space="${
        esc(o.space || '')}" data-from="${esc(o.from || '')}"
        onclick="acceptRadioOffer(rmArg(this,'oid'),rmArg(this,'space'),rmArg(this,'from'))">${
        esc(t('radio.meet.accept'))}</button>
    </div>`).join('');
  return `<div class="gw-section"><h4>${esc(t('radio.meet.offers'))}</h4>
    <p class="hint">${esc(t('radio.meet.offers_hint'))}</p>${rows}</div>`;
}

// ONE line per neighbour, and it says the furthest thing that is true.
//
// There used to be two independent renderers — one for the link, one for the
// invitation — and whichever ran last won. So the most important fact a person
// could be told, that the other side had ACCEPTED, was liable to be replaced by
// a sentence about a probe. The order below is the order of consequence: what
// the people did outranks what the carrier saw, which outranks whether we can
// reach them at all.
//
// "Radio link established" and "Direct radio link" stay different sentences and
// only the second requires observed zero hops — an addressed packet is what a
// mesh forwards, so its arrival proves who, never how far.
function rmStatusLine(n, held) {
  const inv = n.invitation;
  if (inv && inv.state === 'accepted') {
    return held.has(inv.space)
      ? t('radio.meet.line_ready')
      : t('radio.meet.granting');
  }
  if (inv && inv.state === 'offered') {
    if (inv.delivery === 'unheard') return t('radio.meet.unheard');
    if (inv.delivery === 'unconfirmed') return t('radio.meet.unconfirmed');
    if (inv.delivery === 'heard') return t('radio.meet.delivered');
    return t('radio.meet.offering');
  }
  const link = n.link;
  if (!link) return t('radio.meet.link_unknown');
  switch (link.state) {
    case 'up': return link.direct ? t('radio.meet.link_direct') : t('radio.meet.link_up');
    case 'probing': return t('radio.meet.link_probing');
    case 'no_answer': return t('radio.meet.link_none');
    case 'gone': return t('radio.meet.link_gone');
  }
  return t('radio.meet.link_unknown');
}

// The button says what THE NEXT PRESS DOES, and the two presses are two
// different acts rather than one button with a hidden mode.
//
// Establishing the link costs one frame and asks whether they can hear us at
// all. Offering a line costs six and cannot be taken back. Calling both "start
// a line" made the first press look like a failure and the second like a
// repeat, when in fact the first had worked and the second was the real thing.
function rmAction(n) {
  const who = n.name || t('radio.meet.unnamed');
  const inv = n.invitation;
  if (inv && inv.state === 'accepted') return null;      // nothing left to press
  if (inv && inv.state === 'offered') {
    return { label: t('radio.meet.offer_again'), disabled: false };
  }
  const st = n.link && n.link.state;
  if (st === 'probing') return { label: t('radio.meet.checking'), disabled: true };
  if (st === 'up') return { label: t('radio.meet.start_line', { who }), disabled: false };
  return { label: t('radio.meet.establish'), disabled: false };
}

function rmNeighboursBlock(list, held) {
  const head = `<div class="gw-section"><h4>${esc(t('radio.meet.heard'))}</h4>`;
  if (!list.length) {
    return head + `<p class="hint">${esc(t('radio.meet.nobody'))}</p></div>`;
  }
  // The space that is OPEN is the secondary option, never the primary one.
  // Meeting somebody over the radio means the two of you now have a line —
  // QL-1's rule (a link opens a NEW place, never one picked implicitly) and
  // QL-3's (a space with one other person in it IS that person).
  const sp = spacesCache.find(s => s.id === current);
  const canAlsoInvite = sp && !(sp.visibility && sp.visibility !== 'private');
  const alsoName = canAlsoInvite
    ? ((sp.title && sp.title.trim()) ? sp.title.trim() : null) : null;

  const rows = list.map(n => {
    const who = n.name || t('radio.meet.unnamed');
    const act = rmAction(n);
    const inv = n.invitation;
    // The space is OFFERED here — so once it exists, the useful thing is a
    // way into it, not another button that would offer it again.
    const done = inv && inv.state === 'accepted' && held.has(inv.space);
    const button = done
      ? `<button class="btn-tinted" data-space="${esc(inv.space)}"
          onclick="rmOpenSpace(rmArg(this,'space'))">${
          esc(t('radio.meet.open_line'))}</button>`
      : act
        ? `<button class="btn-tinted" data-dev="${esc(n.device)}" ${act.disabled ? 'disabled' : ''}
            onclick="startLineOverRadio(this)">${esc(act.label)}</button>`
        : '';
    const also = canAlsoInvite && !inv
      ? `<div class="hint"><a href="#" data-dev="${esc(n.device)}"
          onclick="inviteOverRadio(this); return false">${
            esc(alsoName ? t('radio.meet.also_into', { space: alsoName })
                         : t('radio.meet.also_here'))}</a></div>`
      : '';
    const line = rmStatusLine(n, held);
    return `<div class="gw-row">
      <span>${esc(who)} <span class="dim mono">${esc(n.device.slice(0, 8))}</span>${also}${
        line ? `<div class="hint">${esc(line)}</div>` : ''}</span>
      ${button}</div>`;
  }).join('');
  return head + `<p class="hint">${esc(t('radio.meet.heard_hint'))}</p>${rows}</div>`;
}

// Open the line this screen just built. Closing the sheet is part of it: the
// thing a person wanted was the conversation, not the dialog about it.
function rmOpenSpace(space) {
  const dlg = /** @type {HTMLDialogElement} */ (document.getElementById('dlgRadioMeet'));
  if (dlg) dlg.close();
  current = space;
  refresh();
}

// One handler, TWO acts, and the person is told which one just happened.
//
// The node decides: with no link yet it sends a one-frame probe, and with one
// up it spends the six-frame invitation. Both used to be behind a button
// labelled "start a line", so the first press looked like a failure and the
// second like a repeat. The label now names the next act (see rmAction) and
// the sentence below names the one that just occurred.
async function startLineOverRadio(btn) {
  const info = document.getElementById('rmInfo');
  const device = btn && btn.dataset ? btn.dataset.dev : btn;
  if (info) info.textContent = t('radio.meet.reaching');
  if (btn && btn.disabled !== undefined) {
    btn.disabled = true;
    btn.textContent = t('radio.meet.working');
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
      // Not a failure and not a line: one frame asking whether they can hear
      // us, because an invitation is six and is not worth spending blind.
      if (info) info.textContent = t('radio.meet.probe_sent');
    } else if (r && r.space) {
      // The line exists HERE already; it becomes theirs when they accept.
      // Deliberately not switching to it — the person is still standing in
      // this screen watching for an answer, and moving them to an empty room
      // would hide the very thing they are waiting for.
      if (info) info.textContent = t('radio.meet.offering');
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

async function acceptRadioOffer(id, space, from) {
  const info = document.getElementById('rmInfo');
  try {
    await api('/api/radio/invitations/accept', {
      method: 'POST',
      body: JSON.stringify({ id }),
    });
    // Accepting is an ANSWER, not a join. The space arrives when the other
    // side grants it, which on this carrier is seconds away and sometimes
    // more — so this must not land anybody in a room that is not there yet.
    //
    // Remembered here so the arrival can be ANNOUNCED. Without this the
    // screen told somebody to wait and then never mentioned that the waiting
    // was over.
    if (space) RM_AWAITING.set(space, from || t('radio.meet.unnamed'));
    if (info) info.textContent = t('radio.meet.answered');
    await refresh();
  } catch (err) {
    if (info) info.textContent = t('radio.meet.failed', { why: err.message });
    return;
  }
  refreshRadioMeet();
}
