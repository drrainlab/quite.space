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
    const { devices, grant_refusals } = await api('/api/devices');
    box.innerHTML = '';
    // What THIS device would not take from its siblings, if anything —
    // the other half of the convergence picture (pending is what it owes).
    if (grant_refusals && grant_refusals.length) {
      const warn = document.createElement('p');
      warn.className = 'hint warn';
      warn.textContent = t('ui.dev.refusals') + ' ' + grant_refusals.join(' · ');
      box.appendChild(warn);
    }
    for (const d of devices) {
      const row = document.createElement('div');
      row.className = 'list-row';
      const label = document.createElement('span');
      label.className = 'lr-label';
      label.textContent = (d.label || d.device.slice(0, 12) + '…') +
        (d.this ? ' — this device' : '') + (d.revoked ? ' — revoked' : '');
      label.title = d.device;
      // THE HOLD IS REPORTED, NOT SILENT: spaces this device still owes
      // the sibling, and where the offer is being left. "Guessed" means
      // our own relay as a courtesy — the first suspect when it never
      // clears.
      if (d.pending && d.pending.length) {
        const owed = document.createElement('span');
        owed.className = 'hint';
        owed.style.display = 'block';
        const vias = [...new Set(d.pending.map(p => p.via + (p.guessed ? ' (' + t('ui.dev.guessed') + ')' : '')))];
        owed.textContent = t('ui.dev.pending', { n: d.pending.length }) + ' · ' + vias.join(', ');
        owed.title = d.pending.map(p => p.title || p.space.slice(0, 12)).join('\n');
        label.appendChild(owed);
      }
      row.appendChild(label);
      // The one revocation that is always a mistake is not offered at all.
      if (!d.this && !d.revoked) {
        // TWO CLICKS, IN THE PAGE. The first arms the button and says the
        // cost out loud; the second, within five seconds, acts. No native
        // confirm() — the desktop WebView swallows those silently, which a
        // live tester measured as "Revoke does nothing".
        const btn = document.createElement('button');
        btn.className = 'btn-plain';
        btn.textContent = 'Revoke';
        let armed = null;
        btn.addEventListener('click', async () => {
          if (!armed) {
            btn.textContent = 'Revoke — forever. Sure?';
            label.textContent = 'It stops speaking at once and loses your owned spaces. Click again to revoke.';
            armed = setTimeout(() => {
              armed = null;
              btn.textContent = 'Revoke';
              loadDevices();
            }, 5000);
            return;
          }
          clearTimeout(armed);
          btn.disabled = true;
          try {
            await api('/api/devices/' + d.device + '/revoke', { method: 'POST' });
          } catch (e) { label.textContent = 'Revoke failed: ' + String(e.message || e); }
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

// A DIALOG THAT CLOSES STOPS ASKING. The pair poll runs at 500ms; before
// this, closing Settings mid-ceremony left it running for the life of the
// page — two requests a second, forever, for a screen nobody could see.
// (gateway.js and radiomeet.js already did exactly this; devices.js did not.)
if (typeof document !== 'undefined') {
  document.addEventListener('DOMContentLoaded', () => {
    const dlg = document.getElementById('dlgSettings');
    if (dlg) dlg.addEventListener('close', () => {
      if (pairPollTimer) { clearInterval(pairPollTimer); pairPollTimer = null; }
    });
  });
}

async function cancelPairingUI() {
  if (pairPollTimer) { clearInterval(pairPollTimer); pairPollTimer = null; }
  try { await api('/api/pairing', { method: 'DELETE' }); } catch { /* gone is gone */ }
  const panel = document.getElementById('pairPanel');
  if (panel) panel.style.display = 'none';
  setPairStage('');
}

async function loadPasscode() {
  const st = document.getElementById('pcState');
  const form = document.getElementById('pcForm');
  const bound = document.getElementById('pcBound');
  if (!st) return;
  // INSIDE THE ANDROID HOST THE CODE LIVES IN HARDWARE — the phone's own
  // vault, on the "This device" tab — not in the data dir this form binds.
  // Two locks with one name confused the first tester who set both; here
  // the file-backed one simply steps aside.
  if (typeof window !== 'undefined' && window.QuietHost) {
    if (form) form.style.display = 'none';
    if (bound) bound.style.display = 'none';
    st.textContent = t('ui.dev.pcPhone');
    // THE TWO CONTROLS THE OLD SENTENCE ONLY POINTED AT. Both raise a native
    // dialog on the phone's own screen; nothing about the key ever enters
    // this page (HOST doctrine: the passphrase has no getter, ever).
    const box = document.getElementById('pcHost');
    if (box) {
      box.style.display = '';
      box.innerHTML = '';
      const mk = (label, verb) => {
        const b = document.createElement('button');
        b.className = 'btn-plain';
        b.textContent = label;
        b.onclick = () => {
          if (!HOST[verb]()) st.textContent = t('ui.dev.pcHostStale');
        };
        box.appendChild(b);
      };
      mk(t('ui.dev.pcReveal'), 'revealPassphrase');
      mk(t('ui.dev.pcChange'), 'changeCode');
    }
    return;
  }
  const hostBox = document.getElementById('pcHost');
  if (hostBox) hostBox.style.display = 'none';
  try {
    const info = await api('/api/passcode');
    // ONE STATE ON SCREEN AT A TIME: bound shows the fact and a way out;
    // unbound shows the form. Both at once read as "did it work?".
    if (form) form.style.display = info.bound ? 'none' : '';
    if (bound) bound.style.display = info.bound ? '' : 'none';
    st.textContent = info.bound
      ? 'A ' + info.digits + '-digit code opens this device. Remove it to set another.'
      : 'No code is bound — the lock screen asks for the passphrase.';
  } catch { st.textContent = ''; }
}

async function forgetPasscode() {
  const st = document.getElementById('pcState');
  try {
    await api('/api/passcode', { method: 'DELETE' });
    st.textContent = 'Removed. Bind a new one below, or leave it — the passphrase always works.';
  } catch (e) { st.textContent = String(e.message || e); }
  loadPasscode();
}

async function bindPasscode() {
  const code = document.getElementById('pcCode');
  const pass = document.getElementById('pcPass');
  const st = document.getElementById('pcState');
  if (!/^[0-9]{4,8}$/.test(code.value)) { st.textContent = 'A code is 4–8 digits.'; return; }
  try {
    await api('/api/passcode', { method: 'POST',
      body: JSON.stringify({ code: code.value, passphrase: pass.value }) });
    code.value = ''; pass.value = '';
    st.textContent = 'Bound. The lock screen offers the code from the next launch.';
  } catch (e) { st.textContent = String(e.message || e); return; }
  loadPasscode();
}

if (typeof window !== 'undefined') {
  // @ts-ignore
  window.loadPasscode = loadPasscode; window.bindPasscode = bindPasscode;
  // @ts-ignore
  window.forgetPasscode = forgetPasscode;
  // @ts-ignore
  window.loadDevices = loadDevices; window.beginPairingUI = beginPairingUI;
  // @ts-ignore
  window.copyPairOffer = copyPairOffer; window.playPairOffer = playPairOffer;
  // @ts-ignore
  window.approvePairingUI = approvePairingUI; window.cancelPairingUI = cancelPairingUI;
}
