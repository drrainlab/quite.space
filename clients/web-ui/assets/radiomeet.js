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
    rmAirBlock(st && st.mesh),
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
function rmAirBlock(mesh) {
  if (!mesh) return '';
  const head = `<div class="gw-section"><h4>${esc(t('radio.meet.air'))}</h4>`;
  if (!mesh.connected) {
    return head + `<p class="hint">${esc(t('radio.meet.no_radio'))}</p></div>`;
  }
  const tr = mesh.transfer;
  if (!tr) {
    return head + `<p class="hint">${esc(t('radio.meet.no_seed'))}</p></div>`;
  }
  const moving = tr.attempted > tr.completed + tr.gaveUp;
  const line = moving
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

function rmNeighboursBlock(list) {
  const head = `<div class="gw-section"><h4>${esc(t('radio.meet.heard'))}</h4>`;
  if (!list.length) {
    return head + `<p class="hint">${esc(t('radio.meet.nobody'))}</p></div>`;
  }
  // The space an invitation would be into is the one that is open. Saying so
  // on every row is what stops somebody inviting a stranger into the wrong
  // room and finding out afterwards.
  const sp = spacesCache.find(s => s.id === current);
  const rows = list.map(n => {
    const who = n.name || t('radio.meet.unnamed');
    // The space's DISPLAY name can be a whole sentence — an unnamed space
    // reads "waiting for someone" — and interpolating a sentence into a
    // sentence produced "invite into waiting for someone". A title is used
    // when there is one, and the button says plainly what it does when
    // there is not.
    //
    // The label is built INSIDE the guard. Reading sp.title before checking
    // that sp exists threw, and a throw here takes the whole list with it:
    // with no space open the screen showed nothing at all rather than the
    // neighbour plus a line saying to open one.
    // A PUBLIC space cannot be offered this way, and the reason is worth
    // saying before the press rather than as an error after it. A sealed
    // invitation carries epoch key material; a public space has none,
    // because its content is signed plaintext anybody with the address may
    // read. The way in is the address, and that route wants a relay.
    const isPublic = sp && sp.visibility && sp.visibility !== 'private';
    let btn;
    if (sp && isPublic) {
      btn = `<span class="hint">${esc(t('radio.meet.public_space'))}</span>`;
    } else if (sp) {
      const label = (sp.title && sp.title.trim())
        ? t('radio.meet.invite_into', { space: sp.title.trim() })
        : t('radio.meet.invite_here');
      btn = `<button class="btn-tinted" data-dev="${esc(n.device)}"
          onclick="inviteOverRadio(this)">${esc(label)}</button>`;
    } else {
      btn = `<span class="hint">${esc(t('radio.meet.open_a_space'))}</span>`;
    }
    return `<div class="gw-row">
      <span>${esc(who)} <span class="dim mono">${esc(n.device.slice(0, 8))}</span></span>
      ${btn}</div>`;
  }).join('');
  return head + `<p class="hint">${esc(t('radio.meet.heard_hint'))}</p>${rows}</div>`;
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
    const r = await api('/api/radio/invitations/accept', {
      method: 'POST',
      body: JSON.stringify({ id }),
    });
    if (info) info.textContent = t('radio.meet.joined');
    // Land in the space they were let into, the way every other join does.
    current = r.joined;
    await refresh();
  } catch (err) {
    if (info) info.textContent = t('radio.meet.failed', { why: err.message });
    return;
  }
  refreshRadioMeet();
}
