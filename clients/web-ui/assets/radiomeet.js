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
  let near, offers;
  try {
    [near, offers] = await Promise.all([
      api('/api/radio/neighbours'),
      api('/api/radio/invitations'),
    ]);
  } catch (err) {
    box.innerHTML = `<p class="hint">${esc(t('radio.meet.unavailable', { why: err.message }))}</p>`;
    return;
  }
  box.innerHTML = [
    rmOffersBlock(offers.invitations || []),
    rmNeighboursBlock(near.neighbours || []),
  ].join('');
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
    const canInvite = !!sp;
    const btn = canInvite
      ? `<button class="btn-tinted" onclick="inviteOverRadio('${esc(n.device)}')">${
          esc(t('radio.meet.invite_into', { space: spaceName(sp) }))}</button>`
      : `<span class="hint">${esc(t('radio.meet.open_a_space'))}</span>`;
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
    if (info) info.textContent = t('radio.meet.announced');
  } catch (err) {
    if (info) info.textContent = t('radio.meet.failed', { why: err.message });
    return;
  }
  refreshRadioMeet();
}

async function inviteOverRadio(device) {
  const info = document.getElementById('rmInfo');
  if (!current) {
    if (info) info.textContent = t('radio.meet.open_a_space');
    return;
  }
  try {
    await api('/api/radio/invite', {
      method: 'POST',
      body: JSON.stringify({ space: current, device }),
    });
    if (info) info.textContent = t('radio.meet.offered');
  } catch (err) {
    if (info) info.textContent = t('radio.meet.failed', { why: err.message });
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
