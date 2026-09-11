// UX-3 — THE INVITE HUB and the one-field door.
//
// Two intents, then the carrier. "Into this space" is a Space Pass (the
// person asks to enter, this device confirms — ADR-012); "a new
// conversation" is a quick link (five words that open a NEW place —
// QL-0). Both existed; the person used to be asked to pick the protocol
// entity first. The dialog keeps every id the pass and quick-link code
// already wire to, so the mechanics are untouched: this file only decides
// what is visible when.

let hubIntentNow = '';
let hubCarrierNow = 'link';

function hubOpen(intent) {
  const dlg = document.getElementById('dlgPass');
  if (!dlg) return;
  if (intent === 'space') {
    const s = typeof currentSpace === 'function' ? currentSpace() : null;
    if (s && typeof openPass === 'function') { openPass(s); return; } // openPass ends in hubIntent('space')
    intent = 'new';
  }
  hubIntent(intent);
  if (!dlg.open) dlg.showModal();
}

function hubIntent(intent) {
  hubIntentNow = intent;
  const space = typeof currentSpace === 'function' ? currentSpace() : null;
  const canSpace = !!(space && space.owned && !space.visibility);
  const bSpace = document.getElementById('hubIntentSpace');
  const bNew = document.getElementById('hubIntentNew');
  if (bSpace) { bSpace.hidden = !canSpace; bSpace.classList.toggle('sel', intent === 'space'); }
  if (bNew) bNew.classList.toggle('sel', intent === 'new');
  const paneSpace = document.getElementById('hubSpace');
  const paneNew = document.getElementById('hubNew');
  if (paneSpace) paneSpace.hidden = intent !== 'space';
  if (paneNew) paneNew.hidden = intent !== 'new';
  if (intent === 'space') hubCarrier(hubCarrierNow);
}

// The carriers of a minted pass: link (the code), QR, sound. One shown at
// a time; the tabs appear only once there is something to carry.
function hubCarrier(c) {
  hubCarrierNow = c;
  const ready = document.getElementById('passReady');
  const tabs = document.getElementById('hubCarriers');
  if (!ready || !tabs) return;
  const minted = ready.style.display !== 'none';
  tabs.hidden = !minted;
  if (!minted) return;
  tabs.querySelectorAll('button').forEach(b => b.classList.toggle('sel', b.dataset.c === c));
  const code = ready.querySelector('.pass-code');
  const qr = ready.querySelector('.pass-qr-view');
  const snd = document.getElementById('apSend');
  if (code) code.hidden = c !== 'link';
  if (qr) qr.hidden = c !== 'qr';
  if (snd) snd.hidden = c !== 'sound';
}

// THE ONE FIELD. A quick link is five words (with or without quiet://);
// a pass is a long opaque string. Tell them apart here, feed the engine
// that already handles each, and never make the person choose a mode.
function joinAnyInput() {
  const el = document.getElementById('joinAny');
  if (!el) return;
  const v = el.value.trim();
  const words = v.replace(/^quiet:\/\//, '');
  const looksLikeWords = v === '' || /^[a-zA-Z][a-zA-Z .\-]{0,80}$/.test(words) && words.split(/[ .]+/).filter(Boolean).length <= 8;
  const mode = looksLikeWords ? 'link' : 'pass';
  if (typeof joinSetMode === 'function') joinSetMode(mode);
  if (mode === 'link') {
    const w = document.getElementById('joinWords');
    if (w) { w.value = v; if (typeof qlOnWordsInput === 'function') qlOnWordsInput(); }
  } else {
    const pz = document.getElementById('joinPass');
    if (pz) pz.value = v;
  }
}

function meToggle(id) {
  const el = document.getElementById(id);
  if (el) el.hidden = !el.hidden;
}

if (typeof window !== 'undefined') {
  window.hubOpen = hubOpen; window.hubIntent = hubIntent; window.hubCarrier = hubCarrier;
  window.joinAnyInput = joinAnyInput; window.meToggle = meToggle;
}
