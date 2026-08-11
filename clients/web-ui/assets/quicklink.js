// @ts-check
// Quick links in the UI (SP-1).
//
// Two jobs, deliberately kept apart the way the node keeps them apart:
// minting a link (in "me", because it is about you rather than about a room)
// and opening one (in join, next to the pass it is an alternative to).
//
// The one thing this file must never do is smooth over what a quick link
// costs. It is the only sharing path in the app that puts anything on a
// relay, and it is the only one that expires in minutes. Both facts are shown
// at the moment of the choice, not buried in a tooltip.

/** @typedef {{link:string, phrase:string, hint:string, space:string, title:string, expires_at:number, relay_note:string}} QuickLinkInfo */

/** Disable a button for the length of an await. */
async function qlBusy(btn, fn) {
  if (!btn) return fn();
  const was = btn.textContent;
  btn.disabled = true;
  btn.textContent = '…';
  try { return await fn(); } finally { btn.disabled = false; btn.textContent = was; }
}

/** The words currently on screen. Held only to copy them; never stored. */
let qlCurrent = null;

// The link's kind, chosen here and described in DEVICES — see the markup
// for why that word and not "people".
let qlKind = 'just-us';
let qlApproval = '';

function qlSetKind(kind) {
  qlKind = kind;
  document.querySelectorAll('#qlKind button').forEach((b) =>
    b.classList.toggle('sel', b.dataset.v === kind));
  const group = kind === 'a-few';
  document.getElementById('qlGroupOpts').hidden = !group;
  document.getElementById('qlKindNote').textContent = group
    ? 'Several devices can enter with these words, until they expire.'
    : 'One device can enter, then the link closes.';
}

function qlSetApproval(mode) {
  qlApproval = mode;
  document.querySelectorAll('#qlApproval button').forEach((b) =>
    b.classList.toggle('sel', b.dataset.v === mode));
}

async function mintQuickLink() {
  const btn = document.getElementById('qlMintBtn');
  await qlBusy(btn, async () => {
    try {
      const body = { title: document.getElementById('qlTitle').value.trim() };
      if (qlKind === 'a-few') {
        body.max_uses = Number(document.getElementById('qlMaxUses').value);
        body.ttl_hours = Number(document.getElementById('qlTtl').value);
        body.approval = qlApproval;
      }
      /** @type {QuickLinkInfo} */
      const info = await api('/api/quicklinks', { method: 'POST', body: JSON.stringify(body) });
      qlCurrent = info;
      document.getElementById('qlPhrase').textContent = info.phrase;
      document.getElementById('qlLink').textContent = info.link;
      document.getElementById('qlNote').textContent = info.relay_note;
      qlTickExpiry();
      document.getElementById('qlOut').style.display = '';
      qlLoadIssued();
    } catch (err) {
      // The node's refusal explains itself — a missing relay, mainly — so
      // show it rather than a generic failure.
      alert(err.message);
    }
  });
}

/** Count the link down, because ten minutes is short enough to matter. */
let qlExpiryTimer = null;
function qlTickExpiry() {
  const el = document.getElementById('qlExpiry');
  if (!el || !qlCurrent) return;
  const left = qlCurrent.expires_at - Math.floor(Date.now() / 1000);
  if (left <= 0) {
    el.textContent = 'These words have expired. Make another when you need one.';
    document.getElementById('qlPhrase').classList.add('spent');
    if (qlExpiryTimer) { clearInterval(qlExpiryTimer); qlExpiryTimer = null; }
    return;
  }
  const m = Math.floor(left / 60), s = left % 60;
  el.textContent = `Good for ${m}:${String(s).padStart(2, '0')}.`;
  if (!qlExpiryTimer) qlExpiryTimer = setInterval(qlTickExpiry, 1000);
}

function qlCopy() {
  if (!qlCurrent) return;
  navigator.clipboard?.writeText(qlCurrent.link);
}

/** What this device has handed out. The words are not here — by design. */
async function qlLoadIssued() {
  const box = document.getElementById('qlIssued');
  if (!box) return;
  try {
    const r = await api('/api/quicklinks');
    const list = r.quicklinks || [];
    if (!list.length) { box.innerHTML = ''; return; }
    const now = Math.floor(Date.now() / 1000);
    box.innerHTML = '<p class="hint" style="margin-top:12px">Links you have handed out:</p>' +
      list.slice(0, 6).map(q => {
        const live = !q.withdrawn && q.expires_at > now;
        const state = q.withdrawn ? 'withdrawn' : (live ? 'live' : 'expired');
        return `<div class="ql-row">` +
          `<span class="ql-state ${state}">${state}</span>` +
          `<span class="ql-note">${esc(q.note || 'no note')}</span>` +
          // The hint is locally minted, so this one was never reachable —
          // but it is the same shape as the injectable ones (esc() is an
          // HTML escaper standing in a JavaScript string, which the
          // attribute's own HTML decode undoes), and leaving it would
          // leave a template for the next person who copies a row.
          (live ? `<button class="btn-plain" data-hint="${esc(q.hint)}"
            onclick="qlWithdraw(this.dataset.hint)">withdraw</button>` : '') +
          `</div>`;
      }).join('');
  } catch { /* the list is a courtesy; its absence is not an error worth shouting */ }
}

async function qlWithdraw(hint) {
  try {
    const r = await api('/api/quicklinks/' + encodeURIComponent(hint), { method: 'DELETE' });
    // Say exactly what withdrawing did, including what it could not do.
    if (r && r.note) alert(r.note);
    qlLoadIssued();
  } catch (err) { alert(err.message); }
}

// ---- the other end: opening a link ----

function joinSetMode(m) {
  document.querySelectorAll('#joinMode button').forEach(b =>
    b.classList.toggle('sel', b.dataset.m === m));
  document.getElementById('joinLinkPane').style.display = m === 'link' ? '' : 'none';
  document.getElementById('joinPassPane').style.display = m === 'pass' ? '' : 'none';
  document.getElementById('joinTitle').textContent =
    t(m === 'link' ? 'ui.join.title_link' : 'ui.join.title_pass');
}

/**
 * Tell the person how far along they are, and never correct their words for
 * them. A silently "fixed" word derives a different key and fails as "link
 * not found", which sends someone to debug their network instead of their
 * typing.
 */
async function qlOnWordsInput() {
  const el = /** @type {HTMLInputElement} */ (document.getElementById('joinWords'));
  const hint = document.getElementById('joinWordsHint');
  const raw = el.value.trim();
  if (!raw) { hint.textContent = ''; return; }
  const parts = raw.replace(/^quiet:\/\//, '').split(/[.\s\-_]+/).filter(Boolean);
  if (parts.length < 5) {
    const last = parts[parts.length - 1] || '';
    let sug = [];
    if (last.length >= 2) {
      try {
        const r = await api('/api/quicklinks/words?prefix=' + encodeURIComponent(last));
        sug = r.words || [];
      } catch { /* suggestions are optional */ }
    }
    hint.textContent = `${parts.length} of 5 words` +
      (sug.length ? ` · maybe: ${sug.slice(0, 4).join(', ')}` : '');
  } else if (parts.length > 5) {
    hint.textContent = `${parts.length} words — a quick link is exactly 5.`;
  } else {
    hint.textContent = 'Five words. Ready.';
  }
}

/**
 * Resolve the words, then SHOW what they lead to and wait. Fetching and
 * joining are separate on the node for a reason, and the UI keeps that seam:
 * nobody should be signed into a space because they pasted something.
 */
// qlIntentLine says what kind of door this was, now that we know. A
// personal link is already spent; a shared one is still open, and pretending
// otherwise in either direction would be a lie the guest could act on.
function qlIntentLine(p) {
  const many = (p.max_uses || 1) > 1;
  if (!many) return 'These words are spent. This device remembers the way in.';
  const until = p.expires_at
    ? ' until ' + new Date(p.expires_at * 1000).toLocaleString()
    : '';
  return `Shared words — they stay open${until}. Up to ${p.max_uses} devices may be let in.`;
}

async function enterWithLink() {
  const el = /** @type {HTMLInputElement} */ (document.getElementById('joinWords'));
  const btn = document.getElementById('joinLinkBtn');
  const words = el.value.trim();
  if (!words) return;
  // The label swap happens AFTER the busy wrapper, because qlBusy restores
  // whatever the button said before — including anything set inside it.
  const p = await qlBusy(btn, async () => {
    try {
      return await api('/api/quicklinks/resolve',
        { method: 'POST', body: JSON.stringify({ words }) });
    } catch (err) {
      alert(err.message);
      return null;
    }
  });
  if (!p) return;

  // "claims" is the honest word: the pass verifies itself against the space's
  // own key at join time, these two strings do not.
  const box = document.getElementById('joinPreview');
  box.innerHTML =
    '<div class="ql-preview">' +
    '<p class="hint">This link claims to be from:</p>' +
    `<p class="ql-from">${esc(p.from || 'someone who left no name')}</p>` +
    '<p class="hint">into the space</p>' +
    `<p class="ql-space">${esc(p.space || 'a place with no name yet')}</p>` +
    // The FIRST moment the kind of door is knowable is now — the guest
    // could not have been told before resolving, so the button never
    // claimed. Say it here.
    `<p class="hint">${qlIntentLine(p)}</p>` +
    // This used to say the host must accept you. They do not: the owner's
    // device admits a valid request automatically and unattended. Saying
    // otherwise described a security model the code does not implement —
    // and a person deciding whether to enter deserves the real one. When
    // host approval ships, the sentence comes back for links that ask for
    // it, and will then be true.
    `<p class="hint">Nothing has been signed yet. ${
      p.approval === 'host'
        ? 'Entering sends a request. Somebody has to let you in.'
        : 'Entering takes you straight into the space.'}</p>` +
    '</div>';
  box.style.display = '';
  btn.textContent = 'Enter';
  btn.onclick = () => {
    document.getElementById('joinPass').value = p.pass_link;
    requestEntry();
  };
}

if (typeof window !== 'undefined') {
  Object.assign(window, {
    mintQuickLink, qlCopy, qlWithdraw, qlLoadIssued, qlSetKind, qlSetApproval,
    refreshDoor, decideDoor,
    joinSetMode, enterWithLink, qlOnWordsInput,
  });
}

// ---- the door: people waiting for you to decide ----

// Someone knocking is the only place a pending entry is ever visible, and
// accepting them leaves no trace elsewhere — so this shows both who is
// waiting and, briefly, who was just let in.
async function refreshDoor() {
  const strip = document.getElementById('doorStrip');
  if (!strip) return;
  let rows = [];
  try {
    rows = await api('/api/entry-requests');
  } catch { strip.hidden = true; return; }
  const waiting = (rows || []).filter((r) => r.state === 'pending');
  if (!waiting.length) { strip.hidden = true; strip.innerHTML = ''; return; }

  const r = waiting[0];
  const who = esc(r.name || 'somebody');
  strip.innerHTML =
    `<span><b>${who}</b> is at the door</span>` +
    (waiting.length > 1 ? `<span class="hint">+${waiting.length - 1} more</span>` : '') +
    `<button id="doorYes">let in</button>` +
    `<button id="doorNo">not now</button>`;
  strip.hidden = false;
  document.getElementById('doorYes').onclick = () => decideDoor(r.request_id, true);
  document.getElementById('doorNo').onclick = () => decideDoor(r.request_id, false);
}

async function decideDoor(reqID, admit) {
  try {
    await api(`/api/entry-requests/${encodeURIComponent(reqID)}/decide`,
      { method: 'POST', body: JSON.stringify({ admit }) });
  } catch (err) { alert(err.message); }
  refreshDoor();
  if (typeof refresh === 'function') refresh();
}
