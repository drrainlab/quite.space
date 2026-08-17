// @ts-check
// Devices & pairing (MD-1/MD-2): the screen over /api/devices and
// /api/pairing. The parent's whole ceremony is four verbs — begin, poll,
// approve, cancel — and the child never appears here: it pairs from its own
// lock screen, before a runtime exists.

let pairPollTimer = null;

async function loadDevices() {
  const box = document.getElementById('devList');
  if (!box) return;
  try {
    const { devices } = await api('/api/devices');
    box.innerHTML = '';
    for (const d of devices) {
      const row = document.createElement('div');
      row.className = 'list-row';
      const label = document.createElement('span');
      label.className = 'lr-label';
      label.textContent = d.device.slice(0, 12) + '…' +
        (d.this ? ' — this device' : '') + (d.revoked ? ' — revoked' : '');
      row.appendChild(label);
      // The one revocation that is always a mistake is not offered at all.
      if (!d.this && !d.revoked) {
        const btn = document.createElement('button');
        btn.className = 'btn-plain';
        btn.textContent = 'Revoke';
        btn.addEventListener('click', async () => {
          if (!confirm('Revoke this device? It stops speaking at once and ' +
            'loses your owned spaces at the next message. This cannot be undone.')) return;
          try {
            await api('/api/devices/' + d.device + '/revoke', { method: 'POST' });
          } catch (e) { alert(String(e.message || e)); }
          loadDevices();
        });
        row.appendChild(btn);
      }
      box.appendChild(row);
    }
  } catch (e) {
    box.textContent = 'Could not load devices: ' + String(e.message || e);
  }
}

// std base64 (the API's) → base64url (what the sound and the lock screen eat).
function offerToUrlB64(std) {
  return std.replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

async function beginPairingUI() {
  try {
    const { offer } = await api('/api/pairing', { method: 'POST' });
    const panel = document.getElementById('pairPanel');
    const out = /** @type {HTMLTextAreaElement} */ (document.getElementById('pairOffer'));
    if (panel) panel.style.display = '';
    if (out) out.value = offerToUrlB64(offer);
    setPairStage('Waiting for the other device… paste the code there, or play it as sound.');
    armPairPoll();
  } catch (e) { setPairStage('Could not start: ' + String(e.message || e)); }
}

function setPairStage(text) {
  const el = document.getElementById('pairStage');
  if (el) el.textContent = text;
}

function armPairPoll() {
  if (pairPollTimer) clearInterval(pairPollTimer);
  pairPollTimer = setInterval(async () => {
    try {
      const st = await api('/api/pairing');
      const digitsEl = document.getElementById('pairDigits');
      const approveBtn = document.getElementById('pairApprove');
      if (st.stage === 'digits') {
        if (digitsEl) digitsEl.textContent = st.digits;
        if (approveBtn) approveBtn.style.display = '';
        setPairStage('Compare the six digits on BOTH screens. Approve only if they match.');
      } else if (st.stage === 'done') {
        clearInterval(pairPollTimer); pairPollTimer = null;
        setPairStage('Paired. The new device is opening as you.');
        if (digitsEl) digitsEl.textContent = '';
        if (approveBtn) approveBtn.style.display = 'none';
        loadDevices();
      } else if (st.stage === 'failed') {
        clearInterval(pairPollTimer); pairPollTimer = null;
        setPairStage('Failed: ' + (st.error || 'unknown'));
      }
    } catch { /* transient; keep polling */ }
  }, 500);
}

function copyPairOffer() {
  const out = /** @type {HTMLTextAreaElement} */ (document.getElementById('pairOffer'));
  if (!out || !out.value) return;
  navigator.clipboard?.writeText(out.value);
  setPairStage('Copied. Paste it on the new device’s first screen.');
}

function playPairOffer() {
  const out = /** @type {HTMLTextAreaElement} */ (document.getElementById('pairOffer'));
  if (!out || !out.value) return;
  // The same carrier the passes sing over; looping IS the error correction.
  // @ts-ignore
  window.apPlay(out.value, (n) => setPairStage('Playing the offer… loop ' + n));
}

async function approvePairingUI() {
  try { await api('/api/pairing/approve', { method: 'POST' }); }
  catch (e) { setPairStage('Approve failed: ' + String(e.message || e)); }
}

async function cancelPairingUI() {
  if (pairPollTimer) { clearInterval(pairPollTimer); pairPollTimer = null; }
  try { await api('/api/pairing', { method: 'DELETE' }); } catch { /* gone is gone */ }
  const panel = document.getElementById('pairPanel');
  if (panel) panel.style.display = 'none';
  setPairStage('');
}

if (typeof window !== 'undefined') {
  // @ts-ignore
  window.loadDevices = loadDevices; window.beginPairingUI = beginPairingUI;
  // @ts-ignore
  window.copyPairOffer = copyPairOffer; window.playPairOffer = playPairOffer;
  // @ts-ignore
  window.approvePairingUI = approvePairingUI; window.cancelPairingUI = cancelPairingUI;
}
