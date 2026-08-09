// QuietRank — the Signals Inbox (AT-0A.3).
//
// A cross-space screen rather than a fourth tab inside one space: the whole
// point is not to miss something ACROSS the places you belong to. Every card
// points back at its source; a signal is a pointer, never a replacement for
// the message.
//
// Two kinds of action live here and must not be confused:
//   learning — useful / not mine / should have caught this
//   policy   — digest only, stop watching, mute this space
// Muting a room is not a claim that its content is worthless.

const QR = {
  signals: [],
  unseen: 0,
  mode: 'minimal',
  filter: 'all', // all | priority | digest
  open: false,
};

// qrRefreshBadge runs on the normal poll tick — cheap, no scan forced.
//
// While the inbox is open it re-renders ONLY when the set of signals really
// changed. Otherwise every tick would repaint the list under the reader's
// cursor and undo the "already read" styling they just earned.
async function qrRefreshBadge() {
  try {
    const r = await api('/api/signals');
    const next = r.signals || [];
    const changed = qrSignature(next) !== qrSignature(QR.signals);
    QR.signals = next;
    QR.unseen = r.unseen || 0;
    QR.mode = r.mode || 'minimal';
    qrPaintBadge();
    if (QR.open && changed) qrRenderInbox();
  } catch (e) { /* attention is optional; never break the shell */ }
}

// qrSignature is the identity of the list, not its read state — so marking
// things read never counts as "the list changed".
function qrSignature(list) {
  return (list || []).map(s => s.id + ':' + s.delivery).join('|');
}

function qrPaintBadge() {
  const btn = document.getElementById('signalsBtn');
  if (!btn) return;
  let dot = btn.querySelector('.sig-count');
  if (!dot) {
    dot = document.createElement('span');
    dot.className = 'sig-count';
    btn.appendChild(dot);
  }
  if (QR.mode === 'off' || QR.unseen === 0) {
    dot.style.display = 'none';
    return;
  }
  dot.style.display = '';
  dot.textContent = QR.unseen > 99 ? '99+' : String(QR.unseen);
}

// ---- the screen ----

// Opening the inbox IS reading it: the badge clears immediately, but the
// cards keep their unread styling for this visit so nothing appears to
// vanish while the person is still looking at it.
async function openSignals() {
  QR.open = true;
  showScreen('signals');
  await qrRefreshBadge();
  qrRenderInbox();
  if (QR.unseen > 0) {
    try {
      await api('/api/signals/all/seen', { method: 'POST' });
      QR.unseen = 0;
      QR.signals = QR.signals.map(s => ({ ...s, seen: true }));
      qrPaintBadge();
    } catch (e) { /* the badge simply stays until the next tick */ }
  }
}

function closeSignals() {
  showScreen('conversation');
}

/** Called when something else takes the column. State only, no display. */
function qrLeaving() { QR.open = false; }

function qrSetFilter(f) {
  QR.filter = f;
  document.querySelectorAll('#sigFilter button').forEach(b =>
    b.classList.toggle('sel', b.dataset.f === f));
  qrRenderInbox();
}

function qrRenderInbox() {
  const box = document.getElementById('sigList');
  if (!box) return;
  box.innerHTML = '';

  if (QR.mode === 'off') {
    box.appendChild(qrNote('Local attention is off. Turn it on in Settings → Attention.'));
    return;
  }
  const rows = QR.signals.filter(s =>
    QR.filter === 'all' ? true : s.delivery === QR.filter);
  if (!rows.length) {
    box.appendChild(qrNote(QR.filter === 'all'
      ? 'Nothing needs you right now.'
      : 'Nothing in this tier.'));
    return;
  }
  // Group by space so the eye lands on a place, not a stream.
  const bySpace = new Map();
  for (const s of rows) {
    if (!bySpace.has(s.space)) bySpace.set(s.space, []);
    bySpace.get(s.space).push(s);
  }
  for (const [spaceHex, list] of bySpace) {
    const h = document.createElement('div');
    h.className = 'sig-group';
    h.textContent = list[0].space_title || spaceHex.slice(0, 8);
    box.appendChild(h);
    for (const s of list) box.appendChild(qrCard(s));
  }
}

function qrNote(text) {
  const d = document.createElement('div');
  d.className = 'empty-spaces';
  d.textContent = text;
  return d;
}

function qrCard(s) {
  const card = document.createElement('div');
  card.className = 'sig-card' + (s.seen ? ' seen' : '') +
    (s.delivery === 'priority' ? ' priority' : '');

  const head = document.createElement('div');
  head.className = 'sig-head';
  const who = document.createElement('span');
  who.className = 'sig-who';
  who.textContent = s.author || 'someone';
  head.appendChild(who);
  const tier = document.createElement('span');
  tier.className = 'sig-tier';
  tier.textContent = s.delivery;
  head.appendChild(tier);
  card.appendChild(head);

  const quote = document.createElement('div');
  quote.className = 'sig-excerpt';
  quote.textContent = s.excerpt;
  card.appendChild(quote);

  // Reason chips. Approximate reasons are labelled as such — hashed
  // features collide, so claiming an exact term would be a lie.
  const chips = document.createElement('div');
  chips.className = 'sig-chips';
  for (const r of s.reasons || []) {
    const c = document.createElement('span');
    c.className = 'sig-chip' + (r.exact ? '' : ' approx');
    c.textContent = qrReasonLabel(r);
    if (!r.exact) c.title = 'a pattern learned from your feedback — approximate';
    chips.appendChild(c);
  }
  card.appendChild(chips);

  const acts = document.createElement('div');
  acts.className = 'sig-acts';
  acts.appendChild(qrAct('open', () => qrOpenSource(s)));
  acts.appendChild(qrAct('useful', () => qrFeedback(s, 'useful')));
  acts.appendChild(qrAct('not mine', () => qrFeedback(s, 'not_mine')));
  // Policy, not learning: silencing a room says nothing about its content.
  acts.appendChild(qrAct('mute space', () => qrMuteSpace(s), 'policy'));
  card.appendChild(acts);

  const foot = document.createElement('div');
  foot.className = 'sig-foot';
  foot.textContent = 'processed locally · ' + (s.layer || 'rules');
  card.appendChild(foot);
  return card;
}

function qrAct(label, fn, kind) {
  const b = document.createElement('button');
  b.className = 'sig-act' + (kind === 'policy' ? ' policy' : '');
  b.textContent = label;
  b.onclick = fn;
  return b;
}

function qrReasonLabel(r) {
  const map = {
    direct_mention: 'mentioned you',
    reply_to_me: 'reply to you',
    protocol_alert: 'network alert',
    name_in_text: 'your name',
    direct_question: 'a question',
    action_required: 'asks for something',
    watched_phrase: 'watching',
    lexical_pattern: 'learned',
    matches_anchor: 'matches',
  };
  const base = map[r.code] || r.code;
  return r.detail ? `${base}: ${r.detail}` : base;
}

// ---- actions ----

async function qrOpenSource(s) {
  closeSignals();
  current = s.space;
  await refresh();
  await refreshSpace();
  // Mark read once the person actually goes there.
  try { await api(`/api/signals/${s.id}/seen`, { method: 'POST' }); } catch (e) {}
  qrRefreshBadge();
  setTimeout(() => {
    const row = document.querySelector(`[data-eid="${s.event}"]`);
    if (row) {
      row.scrollIntoView({ block: 'center' });
      row.classList.add('sig-target');
      setTimeout(() => row.classList.remove('sig-target'), 2200);
    }
  }, 400);
}

async function qrFeedback(s, verdict) {
  try {
    await api(`/api/signals/${s.id}/feedback`, {
      method: 'POST', body: JSON.stringify({ verdict }),
    });
    await api(`/api/signals/${s.id}/seen`, { method: 'POST' });
    qrRefreshBadge();
  } catch (e) { alert(e.message); }
}

// qrMuteSpace is a POLICY change and deliberately does not train the model.
async function qrMuteSpace(s) {
  if (!confirm(`Stop signalling about “${s.space_title || 'this space'}”? Its messages stay exactly as they are — you just will not be told about them.`)) return;
  try {
    const pol = await api('/api/attention/policy');
    pol.spaces = pol.spaces || {};
    pol.spaces[s.space] = 'off';
    await api('/api/attention/policy', { method: 'POST', body: JSON.stringify(pol) });
    qrRefreshBadge();
  } catch (e) { alert(e.message); }
}

// qrNotice is the reverse channel: "you should have caught this", raised
// from an ordinary message in the feed.
async function qrNotice(eventID) {
  if (!current || !eventID) return;
  try {
    await api('/api/attention/notice', {
      method: 'POST', body: JSON.stringify({ space: current, event: eventID }),
    });
  } catch (e) { alert(e.message); }
}

async function qrMarkAllSeen() {
  try {
    await api('/api/signals/all/seen', { method: 'POST' });
    qrRefreshBadge();
  } catch (e) { alert(e.message); }
}

// ---- settings ----

async function qrLoadSettings() {
  try {
    const p = await api('/api/attention/policy');
    document.querySelectorAll('#attnMode button').forEach(b =>
      b.classList.toggle('sel', b.dataset.v === (p.mode || 'minimal')));
    document.getElementById('attnAliases').value = (p.aliases || []).join(', ');
    document.getElementById('attnWatched').value = (p.watched || []).join('\n');
    const b = p.budget || {};
    document.getElementById('attnMaxDay').value = b.max_per_day ?? 12;
    document.getElementById('attnGap').value = b.min_gap_secs ?? 120;
    document.getElementById('attnQuiet').checked = !!b.quiet_enabled;
    document.getElementById('attnQuietFrom').value = b.quiet_from_hour ?? 22;
    document.getElementById('attnQuietTo').value = b.quiet_to_hour ?? 8;
    document.getElementById('attnMsg').textContent = '';
  } catch (e) { /* settings panel is optional */ }
}

async function qrSaveSettings() {
  const mode = document.querySelector('#attnMode button.sel')?.dataset.v || 'minimal';
  const split = (s) => s.split(/[,\n]/).map(x => x.trim()).filter(Boolean);
  const pol = {
    mode,
    aliases: split(document.getElementById('attnAliases').value),
    watched: split(document.getElementById('attnWatched').value),
    spaces: (await api('/api/attention/policy')).spaces || {},
    budget: {
      max_per_day: +document.getElementById('attnMaxDay').value || 0,
      min_gap_secs: +document.getElementById('attnGap').value || 0,
      quiet_enabled: document.getElementById('attnQuiet').checked,
      quiet_from_hour: +document.getElementById('attnQuietFrom').value || 0,
      quiet_to_hour: +document.getElementById('attnQuietTo').value || 0,
    },
  };
  try {
    await api('/api/attention/policy', { method: 'POST', body: JSON.stringify(pol) });
    document.getElementById('attnMsg').textContent = 'saved';
    qrRefreshBadge();
  } catch (e) { document.getElementById('attnMsg').textContent = e.message; }
}

function qrPickMode(m) {
  document.querySelectorAll('#attnMode button').forEach(b =>
    b.classList.toggle('sel', b.dataset.v === m));
  qrSaveSettings();
}

async function qrForget() {
  if (!confirm('Delete everything QuietRank learned about you, and every stored signal? The rules keep working; only the learned part is erased.')) return;
  try {
    await api('/api/attention/forget', { method: 'POST' });
    document.getElementById('attnMsg').textContent = 'profile deleted';
    qrRefreshBadge();
  } catch (e) { document.getElementById('attnMsg').textContent = e.message; }
}
