const token = new URLSearchParams(location.search).get('token') || '';
const api = (path, opts = {}) =>
  fetch(path, { ...opts, headers: { 'X-QP-Token': token, 'Content-Type': 'application/json', ...(opts.headers||{}) } })
    .then(async r => { if (!r.ok) throw new Error((await r.json()).error || r.status); return r.json(); });

let current = null;
let status = null;
let spacesCache = [];

// ---- archetypes, palettes, moods ----

const ARCHETYPES = {
  campfire:   { name: 'Campfire',   desc: 'friends, quiet presence, talk',           acc: '#e8a862', bg: '#14100b', human: '#e8a862' },
  forest:     { name: 'Forest',     desc: 'an organic space grown from shared life', acc: '#64d8a4', bg: '#0c1210', human: '#64d8a4' },
  studio:     { name: 'Studio',     desc: 'music, listening sessions, demos, notes', acc: '#c88ce0', bg: '#100c14', human: '#c88ce0' },
  workshop:   { name: 'Workshop',   desc: 'projects, tasks, devices, telemetry',     acc: '#7ab4e0', bg: '#0c1014', human: '#7ab4e0' },
  orbit:      { name: 'Orbit',      desc: 'a minimal scene for distributed teams',   acc: '#9cc2e8', bg: '#0a0d12', human: '#9cc2e8' },
  home:       { name: 'Home',       desc: 'close people, photos, family memory',     acc: '#e0b49c', bg: '#12100d', human: '#e0b49c' },
  radio_room: { name: 'Radio Room', desc: 'mesh, LoRa, BBS and node status',         acc: '#6ce07a', bg: '#0a0f0a', human: '#6ce07a' },
};
const MOOD_LIGHT = { dawn: 1.5, day: 1.9, dusk: 1.0, night: 0.62 };
const RITUALS = [
  'sunday_capsule', 'shared_hour_of_silence', 'listening_session',
  'one_photo_a_day', 'field_recording_of_the_week', 'question_of_the_week',
  'show_what_you_made', 'evening_by_the_fire', 'vote_on_the_next_step',
];
const MEMORY_OPTS = [
  { v: 'everything',      poetic: 'This place remembers everything that happens in it.',
    tech: 'full log kept on member devices; invites carry full history keys' },
  { v: 'manual',          poetic: 'This place remembers only what you decide to keep.',
    tech: 'log kept; UI will favor deliberate saves (capsules arrive later)' },
  { v: 'private_history', poetic: 'The past stays with those who lived it.',
    tech: 'newcomers get current keys only — earlier epochs stay sealed to them (enforced by cryptography)' },
];

function shade(hex, f) {
  const n = parseInt(hex.slice(1), 16);
  const ch = (x) => Math.max(0, Math.min(255, Math.round(x * f)));
  return `rgb(${ch(n>>16)}, ${ch((n>>8)&255)}, ${ch(n&255)})`;
}

function applyTheme(char) {
  const a = ARCHETYPES[char?.archetype] || ARCHETYPES.forest;
  const f = MOOD_LIGHT[char?.mood] ?? 1.0;
  const root = document.documentElement.style;
  root.setProperty('--acc', a.acc);
  root.setProperty('--human', a.human);
  root.setProperty('--bg', shade(a.bg, f));
  root.setProperty('--panel', shade(a.bg, f * 1.55));
  root.setProperty('--line', shade(a.bg, f * 2.6));
  root.setProperty('--glow', a.acc + '10');
}

// ---- relic: the growing shared object ----
// Deterministic from (space id, event count): every replica draws the same
// form. It grows from shared life; nobody draws it by hand.

function mulberry(seed) {
  return () => {
    seed |= 0; seed = seed + 0x6D2B79F5 | 0;
    let t = Math.imul(seed ^ seed >>> 15, 1 | seed);
    t = t + Math.imul(t ^ t >>> 7, 61 | t) ^ t;
    return ((t ^ t >>> 14) >>> 0) / 4294967296;
  };
}

function drawRelic(canvas, spaceId, relic, events, acc) {
  const ctx = canvas.getContext('2d');
  const W = canvas.width, H = canvas.height;
  ctx.clearRect(0, 0, W, H);
  const seed = parseInt(spaceId.slice(0, 8), 16) || 1;
  const rnd = mulberry(seed);
  const n = Math.min(4 + Math.floor(events * 1.5), 90);
  ctx.strokeStyle = acc; ctx.fillStyle = acc;
  if (relic === 'tree') {
    ctx.lineWidth = 1;
    const branch = (x, y, ang, len, depth) => {
      if (depth <= 0 || len < 3) return;
      const nx = x + Math.cos(ang) * len, ny = y - Math.sin(ang) * len;
      ctx.globalAlpha = .25 + depth * .12;
      ctx.beginPath(); ctx.moveTo(x, y); ctx.lineTo(nx, ny); ctx.stroke();
      branch(nx, ny, ang + .35 + rnd() * .3, len * .72, depth - 1);
      branch(nx, ny, ang - .35 - rnd() * .3, len * .72, depth - 1);
    };
    branch(W / 2, H - 6, Math.PI / 2, H / 3.4, Math.min(2 + Math.floor(events / 4), 7));
  } else if (relic === 'constellation') {
    const pts = [];
    for (let i = 0; i < Math.min(n, 40); i++)
      pts.push([10 + rnd() * (W - 20), 8 + rnd() * (H - 16)]);
    ctx.globalAlpha = .3; ctx.lineWidth = .5;
    for (let i = 1; i < pts.length; i++) {
      ctx.beginPath(); ctx.moveTo(...pts[i - 1]); ctx.lineTo(...pts[i]); ctx.stroke();
    }
    ctx.globalAlpha = .9;
    for (const [x, y] of pts) { ctx.beginPath(); ctx.arc(x, y, 1.2, 0, 7); ctx.fill(); }
  } else if (relic === 'structure') {
    ctx.globalAlpha = .5;
    for (let i = 0; i < Math.min(n, 30); i++) {
      const w = 6 + rnd() * 18, h = 4 + rnd() * 10;
      ctx.strokeRect(W / 2 + (rnd() - .5) * (W - 40), H - 8 - i * 2.4 - h, w, h);
    }
  } else if (relic === 'garden') {
    for (let i = 0; i < Math.min(n, 50); i++) {
      const x = 8 + rnd() * (W - 16), h = 4 + rnd() * (6 + Math.min(events, 30));
      ctx.globalAlpha = .35; ctx.beginPath(); ctx.moveTo(x, H - 4); ctx.lineTo(x, H - 4 - h); ctx.stroke();
      ctx.globalAlpha = .8; ctx.beginPath(); ctx.arc(x, H - 4 - h, 1.4, 0, 7); ctx.fill();
    }
  } else if (relic === 'waveform') {
    ctx.globalAlpha = .7; ctx.lineWidth = 1;
    ctx.beginPath();
    for (let x = 0; x < W; x++) {
      let y = H / 2;
      for (let k = 1; k <= Math.min(1 + events / 6, 6); k++)
        y += Math.sin(x / (9 + k * 7) + seed % 100 + k) * (2.4 + k);
      x ? ctx.lineTo(x, y) : ctx.moveTo(x, y);
    }
    ctx.stroke();
  } else { // ember_ring
    const cx = W / 2, cy = H / 2 + 6, R = Math.min(W, H) / 2.9;
    for (let i = 0; i < Math.min(n, 60); i++) {
      const a = rnd() * Math.PI * 2, r = R * (.75 + rnd() * .4);
      ctx.globalAlpha = .2 + rnd() * .7;
      ctx.beginPath(); ctx.arc(cx + Math.cos(a) * r, cy + Math.sin(a) * r * .5, 1 + rnd() * 1.5, 0, 7);
      ctx.fill();
    }
  }
  ctx.globalAlpha = 1;
}

// ---- main refresh ----

// Protocol view (§18): human by default; the technical layer is opt-in and
// remembered. It only changes how things RENDER — same data underneath.
let PROTOCOL = localStorage.getItem('qp.protocol') === '1';
function toggleProtocol() {
  PROTOCOL = !PROTOCOL;
  localStorage.setItem('qp.protocol', PROTOCOL ? '1' : '0');
  document.body.classList.toggle('protocol', PROTOCOL);
  refresh();
}

// Compact human connection summary (§17): "● Direct · 2 here" — the raw
// LAN/mesh/relay strings live behind the chip and in Protocol view.
function connectionSummary(status) {
  const lan = status.lan, mesh = status.mesh;
  const peers = (lan.peers || 0) + (mesh.connected ? 1 : 0);
  let state = t('conn.off'), cls = 'off';
  if (mesh.connected && peers > 0) { state = t('conn.mesh'); cls = 'mesh'; }
  else if (lan.peers > 0) { state = t('conn.direct'); cls = ''; }
  else if (lan.listening) { state = t('conn.lan'); cls = ''; }
  return { text: peers > 0 ? t('conn.summary', { state, count: peers }) : state, cls };
}

async function refresh() {
  try {
    status = await api('/api/status');
    document.body.classList.toggle('protocol', PROTOCOL);
    document.getElementById('protoToggle').textContent = PROTOCOL ? t('protocol.on') : t('protocol.toggle');

    // Connection chip: human by default, raw string only in Protocol view.
    const conn = document.getElementById('conn');
    const cText = document.getElementById('connText');
    if (PROTOCOL) {
      const lan = status.lan;
      cText.textContent = lan.listening ? `LAN :${lan.port} · ${lan.peers} peers` : 'LAN off';
      conn.className = 'conn-chip' + (lan.peers ? '' : ' off');
    } else {
      const s = connectionSummary(status);
      cText.textContent = s.text;
      conn.className = 'conn-chip ' + s.cls;
    }
    document.getElementById('fp').textContent = PROTOCOL ? status.fingerprint : '';

    const mesh = status.mesh;
    document.getElementById('mesh').textContent = mesh.connected
      ? t('conn.mesh_node', { node: mesh.node_num.toString(16) }) : '';
    document.getElementById('meshInfo').textContent = mesh.connected
      ? `connected as node ${mesh.node_num} · sent ${mesh.tx} · received ${mesh.rx}`
      : (mesh.err ? `disconnected: ${mesh.err}` : 'not connected');

    spacesCache = await api('/api/spaces');
    const box = document.getElementById('spaces');
    box.innerHTML = '';
    for (const s of spacesCache) {
      const d = document.createElement('div');
      d.className = 'space' + (s.id === current ? ' active' : '');
      d.setAttribute('role', 'button');
      d.tabIndex = 0;
      const title = s.title || t('conv.member');
      const an = ARCHETYPES[s.character?.archetype]?.name || '';
      const head = document.createElement('div');
      head.className = 'space-head';
      head.innerHTML = `<span class="glyph g24">${glyphSVG(s.id, 'space', 24)}</span>` +
        `<span class="t">${esc(title)}</span>` +
        (s.owned ? `<span class="owner-tag">${esc(t('spaces.owner'))}</span>` : '');
      d.appendChild(head);
      const meta = document.createElement('div');
      meta.className = 'm';
      const bits = [];
      if (an) bits.push(esc(an));
      bits.push(esc(t('spaces.activity.events', { count: s.events })));
      if (s.undecryptable) bits.push(`<span class="undec">${s.undecryptable}✦</span>`);
      meta.innerHTML = bits.join(' · ');
      d.appendChild(meta);
      d.onclick = () => { current = s.id; refresh(); };
      d.onkeydown = (e) => { if (e.key === 'Enter') { current = s.id; refresh(); } };
      if (s.owned) {
        const b = document.createElement('button');
        b.textContent = 'invite'; b.style.marginTop = '6px'; b.style.fontSize = '12px';
        b.onclick = (e) => { e.stopPropagation(); openInvite(s); };
        d.appendChild(b);
      }
      box.appendChild(d);
    }
    if (!current && spacesCache.length) { current = spacesCache[0].id; }
    if (current) await refreshSpace();
  } catch (e) { console.error(e); }
}

function currentSpace() { return spacesCache.find(s => s.id === current); }

async function refreshSpace() {
  const sp = currentSpace();
  const char = sp?.character;
  applyTheme(char);

  // persona line: the declared character, plus its exact meaning.
  const persona = document.getElementById('persona');
  if (char) {
    const mem = MEMORY_OPTS.find(m => m.v === char.memory) || MEMORY_OPTS[0];
    persona.innerHTML =
      `<span class="arch">${esc(ARCHETYPES[char.archetype]?.name || char.archetype)}</span>` +
      `<span>${esc(char.mood)} · ${esc(char.material)} · ${esc(char.motion)}</span>` +
      `<span>geometry: ${esc(char.geometry)}</span>` +
      (char.rituals?.length ? `<span>rituals: ${esc(char.rituals.join(', '))}</span>` : '') +
      `<span title="${esc(mem.tech)}">“${esc(mem.poetic)}”</span>`;
  } else { persona.innerHTML = ''; }

  // presence selector fed by the space's declared vocabulary.
  const sel = document.getElementById('presence');
  const states = [...new Set(char?.presence || [])];
  sel.innerHTML = '<option value="">presence…</option>' +
    states.map(s => `<option>${esc(s)}</option>`).join('');

  const entries = await api(`/api/spaces/${current}/entries`);
  const log = document.getElementById('log');
  const stick = log.scrollTop + log.clientHeight >= log.scrollHeight - 40;
  log.innerHTML = '';
  for (const e of entries) renderEntry(log, e);
  if (stick) log.scrollTop = log.scrollHeight;

  const st = await api(`/api/spaces/${current}/state`);
  const cards = document.getElementById('cards');
  cards.innerHTML = '';
  for (const c of st.cards) {
    const el = document.createElement('span');
    el.className = 'card' + (c.status === 'done' ? ' done' : '');
    el.textContent = `[${c.status}] ${c.title}`;
    el.title = 'click to toggle status';
    el.onclick = () => toggleCard(c);
    cards.appendChild(el);
  }
  const parts = [];
  if (st.undecryptable > 0)
    parts.push(`<span class="warn">${esc(t('honesty.undecryptable', { count: st.undecryptable }))}</span>`);
  if (st.observation) {
    const o = st.observation;
    parts.push(`${esc(o.display)}${o.simulated ? ' <span class="warn">' + t('honesty.simulated') + '</span>' : ''}` +
      ` · ${esc(o.freshness)}${o.freshness !== 'current' ? ` (${esc(relTime(o.age_seconds))})` : ''}`);
  }
  document.getElementById('honesty').innerHTML = parts.join(' · ');

  const members = await api(`/api/spaces/${current}/members`);
  const mbox = document.getElementById('members');
  mbox.innerHTML = '';
  // the relic: grown from shared life, identical on every replica.
  const canvas = document.createElement('canvas');
  canvas.id = 'relic'; canvas.width = 220; canvas.height = 90;
  mbox.appendChild(canvas);
  const acc = getComputedStyle(document.documentElement).getPropertyValue('--acc').trim();
  drawRelic(canvas, current, char?.relic || 'ember_ring', st.events, acc);
  const note = document.createElement('div');
  note.id = 'relicNote';
  note.textContent = `grown from ${st.events} shared events`;
  mbox.appendChild(note);

  const h = document.createElement('h3'); h.textContent = t('conv.member') + 's'; mbox.appendChild(h);
  for (const m of members) {
    const d = document.createElement('div');
    d.className = 'member';
    let pres;
    if (!m.presence.known) pres = `<div class="pres stale">${esc(t('presence.unknown'))}</div>`;
    else if (m.presence.current) pres = `<div class="pres current">● ${esc(m.presence.state)}</div>`;
    else pres = `<div class="pres stale">${esc(m.presence.state)} · ${esc(relTime(m.presence.age_seconds))}</div>`;
    const glyphType = m.kind === 'human' ? 'human' : m.kind === 'space' ? 'space' : 'device';
    const name = m.name || m.terminal.slice(0, 10);
    const kindBadge = m.kind === 'human' ? '' :
      `<span class="badge b-${m.agency === 'ai_agent' ? 'ai_agent' : 'deterministic_bot'}">${esc(m.kind)}</span>`;
    let inner =
      `<div class="mhead"><span class="glyph g24">${glyphSVG(m.principal || m.terminal, glyphType, 24)}</span>` +
      `<span class="mname">${esc(name)}</span>${kindBadge}</div>` + pres;
    if (m.ai_present)
      inner += `<div class="ai-note">AI · ${esc(m.autonomy)} · model ${esc(m.model)}</div>`;
    // Technical capabilities only in Protocol view — human path stays calm.
    if (PROTOCOL)
      inner += `<div class="caps">${esc(m.io_mode)} · ${esc((m.capabilities || []).join(' '))}` +
        `${m.commandable ? '' : ' · no command surface'}</div>`;
    d.innerHTML = inner;
    mbox.appendChild(d);
  }
}

function fmtAge(s) {
  if (s < 90) return `${s}s`;
  if (s < 5400) return `${Math.round(s / 60)}m`;
  return `${Math.round(s / 3600)}h`;
}

// ---- actions ----

async function say(e) {
  e.preventDefault();
  const inp = document.getElementById('text');
  if (!inp.value.trim() || !current) return false;
  try {
    await api(`/api/spaces/${current}/messages`, { method: 'POST', body: JSON.stringify({ text: inp.value }) });
    inp.value = '';
    await refreshSpace();
  } catch (err) { alert(err.message); }
  return false;
}

async function setPresence(state) {
  if (!state || !current) return;
  try {
    await api(`/api/spaces/${current}/presence`, { method: 'POST', body: JSON.stringify({ state, ttl_seconds: 300 }) });
    document.getElementById('presence').value = '';
    refreshSpace();
  } catch (err) { alert(err.message); }
}

async function joinSpace() {
  const inv = document.getElementById('joinInvite').value.trim();
  if (!inv) return;
  try {
    const r = await api('/api/invites/accept', { method: 'POST', body: JSON.stringify({ invite: inv }) });
    current = r.id; dlgJoin.close(); refresh();
  } catch (err) { alert(err.message); }
}

function showIdentity() {
  document.getElementById('meDevice').textContent = status.device_id;
  document.getElementById('meXpub').textContent = status.device_xpub;
  document.getElementById('meFp').textContent = status.fingerprint;
  dlgMe.showModal();
}

let inviteSpace = null;
function openInvite(s) {
  inviteSpace = s.id;
  document.getElementById('invSpace').textContent = s.title || s.id.slice(0,12);
  document.getElementById('invOut').textContent = '';
  document.getElementById('qrBox').innerHTML = '';
  dlgInvite.showModal();
}

async function mintInvite() {
  try {
    const r = await api(`/api/spaces/${inviteSpace}/invites`, {
      method: 'POST',
      body: JSON.stringify({
        device: document.getElementById('invDevice').value.trim(),
        xpub: document.getElementById('invXpub').value.trim(),
      }),
    });
    document.getElementById('invOut').textContent = r.invite;
    const qr = document.getElementById('qrBox');
    qr.innerHTML = r.qr_png_base64
      ? `<img alt="invite QR" src="data:image/png;base64,${r.qr_png_base64}">` : '';
  } catch (err) { alert(err.message); }
}


async function toggleCard(c) {
  const next = c.status === 'open' ? 'done' : 'open';
  await api(`/api/spaces/${current}/cards/${c.id}/status`,
    { method: 'POST', body: JSON.stringify({ title: c.title, status: next }) });
  refreshSpace();
}

async function lanConnect() {
  const addr = document.getElementById('lanAddr').value.trim();
  if (!addr) return;
  const info = document.getElementById('lanInfo');
  try {
    await api('/api/lan/connect', { method: 'POST', body: JSON.stringify({ addr }) });
    info.textContent = 'connected — spaces will converge within a few seconds.';
    refresh();
  } catch (err) { info.textContent = 'connect failed: ' + err.message; }
}

async function meshConnect() {
  const target = document.getElementById('meshTarget').value.trim();
  if (!target) return;
  try {
    await api('/api/mesh/connect', { method: 'POST', body: JSON.stringify({ target }) });
    refresh();
  } catch (err) { alert(err.message); }
}

// ---- relay ----

async function relayPush() {
  const addr = document.getElementById('relayAddr').value.trim();
  if (!addr || !current) return;
  const info = document.getElementById('relayInfo');
  try {
    const r = await api('/api/relay/push', { method: 'POST',
      body: JSON.stringify({ addr, space: current }) });
    const until = new Date(r.relay_holds_until * 1000).toLocaleString();
    info.textContent = `relay accepted ${r.events_pushed} events and holds them until ${until}. ` +
      `Status: accepted by relay — this is not delivery.`;
  } catch (err) { info.textContent = 'push failed: ' + err.message; }
}

async function relayPull() {
  const addr = document.getElementById('relayAddr').value.trim();
  if (!addr) return;
  const info = document.getElementById('relayInfo');
  try {
    const r = await api('/api/relay/pull', { method: 'POST', body: JSON.stringify({ addr }) });
    info.textContent = r.events_applied > 0
      ? `collected and applied ${r.events_applied} new events.`
      : 'the relay had nothing new for your spaces.';
    refresh();
  } catch (err) { info.textContent = 'pull failed: ' + err.message; }
}

// ---- creation wizard ----

const WHO = [
  ['friends', 'campfire'], ['a creative group', 'studio'], ['a project', 'workshop'],
  ['family', 'home'], ['a community', 'forest'], ['a local net', 'radio_room'],
];
let wizArch = 'campfire';

function openWizard() {
  const wg = document.getElementById('whoGrid');
  wg.innerHTML = '';
  for (const [label, arch] of WHO) {
    const d = document.createElement('div');
    d.className = 'arch-card';
    d.innerHTML = `<div class="an">${esc(label)}</div>`;
    d.onclick = () => { selectArch(arch); wizStep(2); };
    wg.appendChild(d);
  }
  const ag = document.getElementById('archGrid');
  ag.innerHTML = '';
  for (const [k, a] of Object.entries(ARCHETYPES)) {
    const d = document.createElement('div');
    d.className = 'arch-card' + (k === wizArch ? ' sel' : '');
    d.dataset.arch = k;
    d.innerHTML = `<div class="an">${esc(a.name)}</div><div class="ad">${esc(a.desc)}</div>`;
    d.onclick = () => selectArch(k);
    ag.appendChild(d);
  }
  const rl = document.getElementById('ritualList');
  rl.innerHTML = RITUALS.map(r =>
    `<label class="ritual"><input type="checkbox" value="${r}" onchange="limitRituals(this)">${r.replaceAll('_',' ')}</label>`).join('');
  const mo = document.getElementById('memOpts');
  mo.innerHTML = '';
  MEMORY_OPTS.forEach((m, i) => {
    const d = document.createElement('div');
    d.className = 'mem-opt' + (i === 0 ? ' sel' : '');
    d.dataset.v = m.v;
    d.innerHTML = `<div class="poetic">“${esc(m.poetic)}”</div><div class="tech">${esc(m.tech)}</div>`;
    d.onclick = () => {
      document.querySelectorAll('.mem-opt').forEach(x => x.classList.remove('sel'));
      d.classList.add('sel');
    };
    mo.appendChild(d);
  });
  updSeed();
  wizStep(1);
  dlgWiz.showModal();
}

function selectArch(k) {
  wizArch = k;
  document.querySelectorAll('#archGrid .arch-card').forEach(d =>
    d.classList.toggle('sel', d.dataset.arch === k));
  updSeed();
}

function updSeed() {
  const mood = document.getElementById('wMood').value;
  const mat = document.getElementById('wMaterial').value;
  const mot = document.getElementById('wMotion').value;
  document.getElementById('moodSeed').textContent =
    `${mood} ${ARCHETYPES[wizArch].name.toLowerCase()} · ${mat} · ${mot} growth`;
  applyTheme({ archetype: wizArch, mood });
}

function limitRituals(box) {
  const checked = document.querySelectorAll('#ritualList input:checked');
  if (checked.length > 3) { box.checked = false; alert('up to three rituals — they define life here; choose'); }
}

function wizStep(n) {
  for (let i = 1; i <= 4; i++)
    document.getElementById('wiz' + i).classList.toggle('on', i === n);
}

async function wizCreate() {
  const title = document.getElementById('wTitle').value.trim();
  if (!title) { alert('name the place'); return; }
  const rituals = [...document.querySelectorAll('#ritualList input:checked')].map(i => i.value);
  const presence = document.getElementById('wPresence').value
    .split(',').map(s => s.trim().replaceAll(' ', '_')).filter(Boolean);
  const memory = document.querySelector('.mem-opt.sel')?.dataset.v || 'everything';
  try {
    const r = await api('/api/spaces', { method: 'POST', body: JSON.stringify({
      title, archetype: wizArch,
      mood: document.getElementById('wMood').value,
      material: document.getElementById('wMaterial').value,
      motion: document.getElementById('wMotion').value,
      central: document.getElementById('wCentral').value,
      memory, rituals, presence,
    })});
    current = r.id; dlgWiz.close(); refresh();
  } catch (err) { alert(err.message); }
}

const esc = (s) => String(s).replace(/[&<>"']/g, ch =>
  ({ '&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;' }[ch]));

// ---- entry rendering ----
// Every card sets textContent for user strings (never innerHTML), so
// caption/alt/title/transcript can never inject markup.

// Authorship badge stays visible for non-human origins (honesty §2.4): AI,
// bot and sensor never masquerade as a person. Plain human authors get no
// badge — the default needs no label.
function badge(producedBy) {
  if (producedBy === 'human') return null;
  const b = document.createElement('span');
  b.className = 'badge b-' + producedBy;
  b.textContent = producedBy.replace(/_/g, ' ');
  return b;
}

// A human display name for an author. In Protocol view the raw principal is
// shown instead; in the human path an unnamed author gets a glyph + "member"
// — never a bare principal:hex.
function authorLabel(e) {
  if (PROTOCOL) return e.author;
  return e.author_name || t('conv.member');
}

function renderEntry(log, e) {
  const d = document.createElement('div');
  d.className = 'entry msg';
  const who = document.createElement('div');
  who.className = 'who';
  if (!PROTOCOL) {
    const g = document.createElement('span');
    g.className = 'glyph g18';
    g.innerHTML = glyphSVG(e.author, e.produced_by === 'human' ? 'human' : 'device', 18);
    who.appendChild(g);
  }
  const b = badge(e.produced_by);
  if (b) who.appendChild(b);
  const meta = document.createElement('span');
  meta.className = 'meta';
  meta.textContent = authorLabel(e) + (e.revised ? ' · ' + t('conv.edited') : '') +
    (PROTOCOL ? ' · ' + e.kind : '');
  who.appendChild(meta);
  const mk = document.createElement('span');
  mk.className = 'mk'; mk.textContent = t('conv.save_card');
  mk.onclick = () => makeCardFrom(e);
  who.appendChild(mk);
  d.appendChild(who);
  d.appendChild(renderBody(e));
  d.appendChild(renderReactions(e));
  log.appendChild(d);
}

function textNode(cls, s) {
  const el = document.createElement('div');
  el.className = cls; el.textContent = s;
  return el;
}

function assetNote(e) {
  const a = e.asset;
  const n = document.createElement('div');
  n.className = 'asset-note';
  if (!a) return n;
  if (a.state === 'complete') {
    n.textContent = `⬇ open original · ${fmtBytes(a.size)}`;
    n.onclick = () => window.open(`/api/spaces/${current}/assets/${a.id}?token=${token}`, '_blank');
  } else if (a.state === 'fetching') {
    n.textContent = `fetching… ${a.total - a.missing}/${a.total}`;
  } else {
    n.textContent = `⬇ fetch original · ${fmtBytes(a.size)} · not local yet`;
    n.onclick = async () => {
      n.textContent = 'requesting…';
      await api(`/api/spaces/${current}/assets/${a.id}/fetch`, { method: 'POST' });
      refreshSpace();
    };
  }
  return n;
}

function renderBody(e) {
  switch (e.kind) {
    case 'text':
      return textNode('txt', e.text);
    case 'visual': {
      const wrap = document.createElement('div');
      if (e.caption) wrap.appendChild(textNode('txt', e.caption));
      if (e.thumb_b64) {
        const img = document.createElement('img');
        img.className = 'thumb'; img.alt = e.alt;
        img.src = `data:${e.thumb_mime};base64,${e.thumb_b64}`;
        img.onclick = () => { if (e.asset?.state === 'complete')
          window.open(`/api/spaces/${current}/assets/${e.asset.id}?token=${token}`, '_blank'); };
        wrap.appendChild(img);
      } else {
        wrap.appendChild(textNode('txt', '🖼 ' + e.alt));
      }
      wrap.appendChild(assetNote(e));
      return wrap;
    }
    case 'voice': case 'audio': {
      const wrap = document.createElement('div');
      const title = e.kind === 'audio'
        ? (e.title || 'audio') + (e.bpm ? ` · ${e.bpm} BPM` : '')
        : `voice · ${fmtDur(e.duration_ms)}`;
      wrap.appendChild(textNode('txt', (e.kind === 'audio' ? '♫ ' : '🎙 ') + title));
      if (e.waveform_b64) wrap.appendChild(renderWave(e.waveform_b64));
      if (e.transcript) wrap.appendChild(textNode('meta', '“' + e.transcript + '”'));
      if (e.asset?.state === 'complete') {
        const au = document.createElement('audio');
        au.controls = true;
        au.src = `/api/spaces/${current}/assets/${e.asset.id}?token=${token}`;
        wrap.appendChild(au);
      } else {
        wrap.appendChild(assetNote(e));
      }
      return wrap;
    }
    case 'file': {
      const wrap = document.createElement('div');
      const card = document.createElement('div');
      card.className = 'filecard';
      card.appendChild(textNode('txt', '📄 ' + e.filename));
      card.appendChild(assetNote(e));
      wrap.appendChild(card);
      return wrap;
    }
    case 'link': {
      const card = document.createElement('div');
      card.className = 'linkcard';
      const a = document.createElement('a');
      a.href = e.url; a.target = '_blank'; a.rel = 'noopener noreferrer';
      a.textContent = e.title || e.url;
      card.appendChild(a);
      if (e.description) card.appendChild(textNode('meta', e.description));
      return card;
    }
    case 'live_signal':
      return renderSignal(e);
    default: {
      const wrap = document.createElement('div');
      wrap.appendChild(textNode('unknownblk', e.fallback || ('unsupported block: ' + e.schema)));
      return wrap;
    }
  }
}

function renderWave(b64) {
  const bytes = atob(b64);
  const w = document.createElement('div');
  w.className = 'wave';
  for (let i = 0; i < bytes.length; i++) {
    const bar = document.createElement('i');
    bar.style.height = Math.max(2, (bytes.charCodeAt(i) / 255) * 32) + 'px';
    w.appendChild(bar);
  }
  return w;
}

function renderReactions(e) {
  const row = document.createElement('div');
  row.className = 'reactrow';
  for (const r of (e.reactions || [])) {
    const el = document.createElement('span');
    el.className = 'react' + (r.mine ? ' mine' : '');
    el.textContent = `${r.emoji} ${r.count}`;
    el.onclick = () => setReaction(e.id, r.emoji, !r.mine);
    row.appendChild(el);
  }
  const add = document.createElement('span');
  add.className = 'addreact'; add.textContent = '+';
  add.onclick = () => {
    const em = prompt('react with one emoji:');
    if (em) setReaction(e.id, em, true);
  };
  row.appendChild(add);
  return row;
}

async function setReaction(target, emoji, active) {
  try {
    await api(`/api/spaces/${current}/reactions`,
      { method: 'POST', body: JSON.stringify({ target, emoji, active }) });
    refreshSpace();
  } catch (err) { alert(err.message); }
}

async function makeCardFrom(e) {
  await api(`/api/spaces/${current}/cards`,
    { method: 'POST', body: JSON.stringify({ title: (e.fallback || e.text || 'card').slice(0, 120), origin: e.id }) });
  refreshSpace();
}

const fmtBytes = (b) => b >= 1<<20 ? (b/(1<<20)).toFixed(1)+' MB'
  : b >= 1<<10 ? Math.round(b/(1<<10))+' KB' : b+' B';
const fmtDur = (ms) => { const s = Math.round(ms/1000); return `${Math.floor(s/60)}:${String(s%60).padStart(2,'0')}`; };

// ---- composer: attachments ----

let pendingFile = null, pendingKind = null, pendingPreview = null, pendingMeta = {};

async function onFilePicked(file) {
  if (!file) return;
  pendingFile = file;
  const isImage = file.type.startsWith('image/');
  pendingKind = isImage ? 'visual' : 'file';
  document.getElementById('attachTitle').textContent = isImage ? 'attach image' : 'attach file';
  document.getElementById('attachAlt').style.display = isImage ? '' : 'none';
  document.getElementById('attachCaption').style.display = isImage ? '' : 'none';
  const prev = document.getElementById('attachPreview');
  prev.innerHTML = '';
  pendingPreview = null;
  document.getElementById('attachWarn').textContent = '';
  if (isImage) {
    const { blob, dataURL } = await makeThumb(file);
    pendingPreview = blob;
    const img = document.createElement('img'); img.src = dataURL;
    img.style.maxWidth = '200px'; img.style.borderRadius = '4px';
    prev.appendChild(img);
    document.getElementById('attachWarn').textContent =
      'note: the original may carry EXIF/GPS metadata; the thumbnail is re-encoded and stripped.';
  } else {
    prev.textContent = `${file.name} · ${fmtBytes(file.size)}`;
  }
  dlgAttach.showModal();
  fileInput.value = '';
}

// Downscale + re-encode through a canvas: strips metadata, bounds size.
function makeThumb(file) {
  return new Promise((resolve) => {
    const img = new Image();
    img.onload = () => {
      const max = 480;
      let { width: w, height: h } = img;
      if (w > max || h > max) { const s = max / Math.max(w, h); w = Math.round(w*s); h = Math.round(h*s); }
      const c = document.createElement('canvas'); c.width = w; c.height = h;
      c.getContext('2d').drawImage(img, 0, 0, w, h);
      c.toBlob(b => resolve({ blob: b, dataURL: c.toDataURL('image/webp', 0.7) }), 'image/webp', 0.7);
    };
    img.src = URL.createObjectURL(file);
  });
}

async function sendAttachment() {
  const meta = {
    kind: pendingKind, size: pendingFile.size, media_type: pendingFile.type || 'application/octet-stream',
  };
  if (pendingKind === 'visual') {
    const alt = document.getElementById('attachAlt').value.trim();
    if (!alt) { alert('alt text is required — it is how the image reaches text terminals'); return; }
    meta.alt = alt;
    meta.caption = document.getElementById('attachCaption').value.trim();
    meta.preview_mime = 'image/webp';
  } else {
    meta.filename = pendingFile.name;
  }
  await postBlock(meta, pendingPreview, pendingFile);
  dlgAttach.close();
  refreshSpace();
}

async function postBlock(meta, preview, file) {
  const fd = new FormData();
  fd.append('metadata', new Blob([JSON.stringify(meta)], { type: 'application/json' }));
  if (preview) fd.append('preview', preview, 'preview.webp');
  if (file) fd.append('file', file, meta.filename || 'file');
  const resp = await fetch(`/api/spaces/${current}/blocks`,
    { method: 'POST', headers: { 'X-QP-Token': token }, body: fd });
  if (!resp.ok) { alert((await resp.json()).error || resp.status); throw new Error('post failed'); }
}

// ---- composer: voice ----

let mediaRecorder = null, voiceChunks = [], voiceBlob = null, voiceMIME = '', voiceStart = 0, voiceWaveData = [];

function pickVoiceMIME() {
  for (const m of ['audio/webm;codecs=opus', 'audio/ogg;codecs=opus', 'audio/mp4', 'audio/webm']) {
    if (window.MediaRecorder && MediaRecorder.isTypeSupported(m)) return m;
  }
  return '';
}

async function toggleVoice() {
  const btn = document.getElementById('voiceBtn');
  if (mediaRecorder && mediaRecorder.state === 'recording') {
    mediaRecorder.stop();
    btn.classList.remove('rec');
    return;
  }
  let stream;
  try { stream = await navigator.mediaDevices.getUserMedia({ audio: true }); }
  catch { alert('microphone unavailable'); return; }
  voiceMIME = pickVoiceMIME();
  if (!voiceMIME) { alert('this browser cannot record audio'); return; }
  voiceChunks = []; voiceWaveData = []; voiceStart = Date.now();
  mediaRecorder = new MediaRecorder(stream, { mimeType: voiceMIME });
  // sample loudness for a compact waveform (≤64 buckets).
  const ac = new AudioContext();
  const an = ac.createAnalyser(); an.fftSize = 256;
  ac.createMediaStreamSource(stream).connect(an);
  const buf = new Uint8Array(an.frequencyBinCount);
  const sample = () => {
    if (!mediaRecorder || mediaRecorder.state !== 'recording') { ac.close(); return; }
    an.getByteTimeDomainData(buf);
    let peak = 0; for (const v of buf) peak = Math.max(peak, Math.abs(v - 128));
    voiceWaveData.push(Math.min(255, peak * 2));
    setTimeout(sample, 120);
  };
  mediaRecorder.ondataavailable = (ev) => voiceChunks.push(ev.data);
  mediaRecorder.onstop = () => {
    stream.getTracks().forEach(t => t.stop());
    voiceBlob = new Blob(voiceChunks, { type: voiceMIME });
    document.getElementById('voicePreview').src = URL.createObjectURL(voiceBlob);
    drawVoiceWave();
    dlgVoiceReview.showModal();
  };
  mediaRecorder.start(); sample();
  btn.classList.add('rec');
}

function compactWave() {
  const target = 48, data = voiceWaveData, out = [];
  if (!data.length) return out;
  const step = data.length / target;
  for (let i = 0; i < target; i++) {
    const a = Math.floor(i * step), b = Math.max(a + 1, Math.floor((i + 1) * step));
    let m = 0; for (let j = a; j < b && j < data.length; j++) m = Math.max(m, data[j]);
    out.push(m);
  }
  return out;
}

function drawVoiceWave() {
  const c = document.getElementById('voiceWave'), ctx = c.getContext('2d');
  ctx.clearRect(0, 0, c.width, c.height);
  const acc = getComputedStyle(document.documentElement).getPropertyValue('--acc').trim();
  ctx.fillStyle = acc;
  const w = compactWave();
  w.forEach((v, i) => { const h = Math.max(2, (v/255)*c.height); ctx.fillRect(i*5, c.height-h, 3, h); });
}

async function sendVoice() {
  const durMs = Date.now() - voiceStart;
  const wave = compactWave();
  const meta = {
    kind: 'audio', size: voiceBlob.size, media_type: voiceMIME.split(';')[0],
    title: 'voice message', duration_ms: durMs,
    waveform: btoa(String.fromCharCode(...wave)),
    transcript: document.getElementById('voiceTranscript').value.trim(),
  };
  // Voice is an AudioBlock with a spoken-word title; transcript is fallback.
  await postBlock(meta, null, voiceBlob);
  dlgVoiceReview.close();
  document.getElementById('voiceTranscript').value = '';
  refreshSpace();
}

// ---- composer: link ----

function openLink() { dlgLink.showModal(); }
async function sendLink() {
  const url = document.getElementById('linkURL').value.trim();
  if (!/^https?:\/\//.test(url)) { alert('only http/https links'); return; }
  await postBlock({ kind: 'link', url,
    title: document.getElementById('linkTitle').value.trim(),
    description: document.getElementById('linkDescr').value.trim() }, null, null);
  dlgLink.close(); refreshSpace();
}

// ---- composer: live signal ----

const SIGNAL_PRESETS = {
  'slow-pines@1': [['density', 420], ['wind', 180], ['tone', 670]],
  'ember-drift@1': [['heat', 500], ['drift', 300]],
};

function openSignal() {
  document.getElementById('sigPreset').value = 'slow-pines@1';
  buildSignalParams();
  dlgSignal.showModal();
}

function buildSignalParams() {
  const preset = document.getElementById('sigPreset').value;
  const box = document.getElementById('sigParams');
  box.innerHTML = '';
  for (const [name, def] of SIGNAL_PRESETS[preset]) {
    const l = document.createElement('label');
    l.textContent = name;
    const inp = document.createElement('input');
    inp.type = 'range'; inp.min = 0; inp.max = 1000; inp.value = def;
    inp.dataset.name = name;
    inp.oninput = renderSignalPreview;
    l.appendChild(inp);
    box.appendChild(l);
  }
  renderSignalPreview();
}

function currentSignalParams() {
  const params = {};
  document.querySelectorAll('#sigParams input').forEach(i => params[i.dataset.name] = +i.value);
  return params;
}

function renderSignalPreview() {
  buildSignalParamsIfNeeded();
  const preset = document.getElementById('sigPreset').value;
  drawSignal(document.getElementById('sigPreview'), preset, 173991, currentSignalParams());
}
let lastPreset = null;
function buildSignalParamsIfNeeded() {
  const p = document.getElementById('sigPreset').value;
  if (p !== lastPreset) { lastPreset = p; buildSignalParams(); }
}

// Deterministic renderer: own PRNG (mulberry32), reduced-motion aware, no
// Math.random. Producer is strict (only known presets/params); this is the
// renderer — an unknown preset would fall back to the text line.
function drawSignal(canvas, preset, seed, params) {
  const ctx = canvas.getContext('2d'), W = canvas.width, H = canvas.height;
  const acc = getComputedStyle(document.documentElement).getPropertyValue('--acc').trim();
  ctx.clearRect(0, 0, W, H);
  const rnd = mulberry(seed ^ hashStr(preset));
  const name = preset.split('@')[0];
  if (name === 'slow-pines') {
    const density = (params.density ?? 420) / 1000;
    const n = Math.round(6 + density * 40);
    ctx.globalAlpha = 0.15; ctx.fillStyle = acc; ctx.fillRect(0, 0, W, H);
    ctx.globalAlpha = 1;
    for (let i = 0; i < n; i++) {
      const x = rnd() * W, base = H - 4, ht = 20 + rnd() * (H * 0.7);
      ctx.strokeStyle = acc; ctx.globalAlpha = 0.3 + rnd() * 0.5;
      ctx.beginPath();
      for (let y = 0; y <= ht; y += 4) {
        const wob = Math.sin(y / 7 + i) * (params.wind ?? 180) / 200;
        ctx.lineTo(x + wob, base - y);
      }
      ctx.stroke();
    }
  } else if (name === 'ember-drift') {
    const heat = (params.heat ?? 500) / 1000;
    ctx.globalAlpha = 1;
    for (let i = 0; i < 60; i++) {
      const x = rnd() * W, y = H - rnd() * H * (0.3 + heat);
      ctx.globalAlpha = 0.2 + rnd() * 0.7; ctx.fillStyle = acc;
      ctx.beginPath(); ctx.arc(x, y, 1 + rnd() * 2, 0, 7); ctx.fill();
    }
  }
  ctx.globalAlpha = 1;
}

function hashStr(s) { let h = 2166136261; for (let i = 0; i < s.length; i++) { h ^= s.charCodeAt(i); h = Math.imul(h, 16777619); } return h >>> 0; }

async function sendSignal() {
  const preset = document.getElementById('sigPreset').value;
  const params = currentSignalParams();
  const density = params.density ?? params.heat ?? 500;
  const fallback = `~ ${preset.split('@')[0]} signal · ${Math.round(density/10)}%`;
  await postBlock({ kind: 'live_signal', preset, seed: 173991, params, fallback }, null, null);
  dlgSignal.close(); refreshSpace();
}

// Render a live signal in the feed: canvas + a static fallback line + an
// optional soft drone that only plays on click (never auto, hard gain cap).
function renderSignal(e) {
  const wrap = document.createElement('div');
  wrap.className = 'sig';
  const c = document.createElement('canvas'); c.width = 300; c.height = 110;
  wrap.appendChild(c);
  const reduce = matchMedia('(prefers-reduced-motion: reduce)').matches;
  drawSignal(c, e.preset || 'slow-pines@1', e.seed || 0, e.params || {});
  const bar = document.createElement('div');
  bar.className = 'sigbar';
  const label = document.createElement('span'); label.textContent = e.fallback;
  const play = document.createElement('span'); play.className = 'play'; play.textContent = '▶ sound';
  play.onclick = () => playDrone(e);
  bar.appendChild(label); bar.appendChild(play);
  wrap.appendChild(bar);
  if (reduce) wrap.appendChild(textNode('meta', 'motion reduced'));
  return wrap;
}

let droneCtx = null;
function playDrone(e) {
  if (droneCtx) { droneCtx.close(); droneCtx = null; return; }
  droneCtx = new AudioContext();
  const g = droneCtx.createGain();
  g.gain.value = 0.04; // hard cap — never loud, never sudden
  g.connect(droneCtx.destination);
  const tone = ((e.params?.tone ?? 500) / 1000) * 120 + 80;
  [tone, tone * 1.5].forEach(f => {
    const o = droneCtx.createOscillator(); o.type = 'sine'; o.frequency.value = f;
    o.connect(g); o.start();
  });
  setTimeout(() => { if (droneCtx) { droneCtx.close(); droneCtx = null; } }, 6000);
}

document.getElementById('sigPreset').addEventListener('change', () => { lastPreset = null; renderSignalPreview(); });

refresh();
setInterval(refresh, 2000);
