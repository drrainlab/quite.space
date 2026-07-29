const token = new URLSearchParams(location.search).get('token') || '';
const api = (path, opts = {}) =>
  fetch(path, { ...opts, headers: { 'X-QP-Token': token, 'Content-Type': 'application/json', ...(opts.headers||{}) } })
    .then(async r => { if (!r.ok) throw new Error((await r.json()).error || r.status); return r.json(); });

let current = null;
let status = null;
let spacesCache = [];
// Entry-motion tracking (UI-3): remember which entry ids we've already shown
// for the active space so re-renders don't re-animate the whole feed.
let seenEntries = new Set();
let seenSpace = null;
// Members known to be "here" last render — used to fire the arrival pulse
// exactly once, on the transition into presence (never as an idle loop).
let hereMembers = new Set();
// feedSig: per-entry signatures from the last render — the incremental feed
// only touches the DOM when something actually changed (keeps players alive).
let feedSig = [];
let feedContentSig = [];
// Space Composition Contract (SC-0): the appearance snapshot last applied.
let appearanceSpace = null;
// The connection chip's last painted state — see refresh() for why repaints
// must be gated on actual change.
let connChipSig = '';

// loadSpaceAppearance fetches the space's signed appearance snapshot and
// applies its palette to a SCOPED container (never :root). Fetched once per
// space change; a foreign holder would verify the signed frame first.
async function loadSpaceAppearance(sid) {
  if (!sid || sid === appearanceSpace) return;
  appearanceSpace = sid;
  try {
    const snap = await api(`/api/spaces/${sid}/appearance`);
    applyScopedAppearance(document.getElementById('content'), snap.appearance, renderMode());
    // MotionPolicy has been in the signed appearance since SC-0 and read by
    // nothing; atmosphere is its first consumer (AM-3). A space asking for
    // stillness caps every scene in it, and cannot raise anyone's setting.
    if (typeof STAGE !== 'undefined') STAGE.setPolicy((snap.appearance || {}).motion || '');
  } catch (e) { /* space may not project a contract (not owned) — ignore */ }
}

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

// The space's character accent is SCOPED to the conversation (#content) — it
// tints bubbles/persona for that space without overriding the shell theme +
// preset, which own the global palette (PUI-1). Full per-space canvas theming
// flows through the signed SC appearance (loadSpaceAppearance → #content).
function applyTheme(char) {
  const a = ARCHETYPES[char?.archetype] || ARCHETYPES.forest;
  const el = document.getElementById('content');
  if (!el) return;
  el.style.setProperty('--acc', a.acc);
  el.style.setProperty('--human', a.human);
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

// ---- Appearance: theme (light/dark/auto) + preset (PUI-1) ----
// Local, per-device shell look — the client's "last word". Distinct from a
// space's shared SC appearance (scoped to #content). Persisted in localStorage;
// the keystore mirror arrives with Settings (PUI-2).
const PRESETS = ['quiet-glass', 'daylight', 'minimal-mono', 'comfort'];
function prefersDark() {
  return !window.matchMedia || window.matchMedia('(prefers-color-scheme: dark)').matches;
}
function applyAppearance() {
  const theme = localStorage.getItem('qp.theme') || 'dark';
  const effective = theme === 'auto' ? (prefersDark() ? 'dark' : 'light') : theme;
  const preset = localStorage.getItem('qp.preset') || 'quiet-glass';
  const r = document.documentElement;
  r.setAttribute('data-theme', effective);
  r.setAttribute('data-preset', PRESETS.includes(preset) ? preset : 'quiet-glass');
}
function setTheme(t) { localStorage.setItem('qp.theme', t); applyAppearance(); }
function setPreset(p) { localStorage.setItem('qp.preset', p); applyAppearance(); }
function setRenderMode(m) {
  localStorage.setItem('qp.rendermode', m); syncSettingsUI();
  appearanceSpace = null; if (current) loadSpaceAppearance(current); // re-apply at new detail
}
function setEffectsMode(m) {
  localStorage.setItem('qp.effects', m); syncSettingsUI();
  feedSig = []; feedContentSig = []; refreshSpace(); // re-apply residue at the new mode
}
function pickSeg(groupId, v) {
  document.querySelectorAll('#' + groupId + ' button').forEach(b =>
    b.classList.toggle('sel', b.dataset.v === v));
}
function segValue(groupId) {
  const b = document.querySelector('#' + groupId + ' button.sel');
  return b ? b.dataset.v : '';
}

// ---- Quick Customize (SC-3): manual appearance → new signed revision ----
function hexOr(v, fb) { return /^#[0-9a-fA-F]{6}$/.test(v) ? v : fb; }
async function openCustomize() {
  const sid = current; if (!sid) return;
  document.getElementById('custMsg').textContent = '';
  try {
    const snap = await api(`/api/spaces/${sid}/appearance`);
    const ap = snap.appearance || {};
    const pal = Object.fromEntries((ap.palette || []).map(p => [p.name, p.hex]));
    document.getElementById('custAccent').value = hexOr(pal.accent, '#c48ae4');
    document.getElementById('custTint').value = hexOr(ap.background?.tint, '#241730');
    document.getElementById('custDim').value = ap.background?.dim ?? 380;
    pickSeg('custMotion', ap.motion || 'quiet');
    pickSeg('custDensity', ap.density || 'calm');
  } catch (e) { /* defaults stand */ }
  dlgCustomize.showModal();
}
async function applyCustomize() {
  const sid = current; if (!sid) return;
  const body = {
    accent: document.getElementById('custAccent').value,
    tint: document.getElementById('custTint').value,
    dim: parseInt(document.getElementById('custDim').value, 10) || 0,
    motion: segValue('custMotion') || 'quiet',
    density: segValue('custDensity') || 'calm',
  };
  try {
    const snap = await api(`/api/spaces/${sid}/appearance`, { method: 'PATCH', body: JSON.stringify(body) });
    // Re-apply the freshly signed appearance to the scoped canvas.
    appearanceSpace = null; await loadSpaceAppearance(sid);
    dlgCustomize.close();
  } catch (err) { document.getElementById('custMsg').textContent = err.message; }
}
let llmSettings = { provider: '', model: '', base_url: '', has_key: false };
async function openSettings() {
  syncSettingsUI();
  try {
    const s = await api('/api/settings');
    llmSettings = s.llm || llmSettings;
    document.getElementById('llmModel').value = llmSettings.model || '';
    document.getElementById('llmBaseURL').value = llmSettings.base_url || '';
    document.getElementById('llmKey').value = '';
    document.getElementById('llmKey').placeholder = llmSettings.has_key
      ? 'key saved — leave blank to keep' : 'stored on this device only';
    pickProvider(llmSettings.provider || 'anthropic', true);
    document.getElementById('llmMsg').textContent = '';
    document.getElementById('relaySetAddr').value = s.relay || '';
    document.getElementById('relaySetInterval').value = s.relay_sync_seconds || 2;
    document.getElementById('relaySetMsg').textContent = '';
    document.getElementById('relayInfo').textContent = '';
    document.getElementById('catalogLink').value = localStorage.getItem('qp.catalog') || '';
    document.getElementById('catalogMsg').textContent = '';
    renderRelayPublicPanel();
  } catch (e) { /* settings unavailable */ }
  showSettingsCat(settingsCat);
  dlgSettings.showModal();
}

// Settings categories (Appearance / Relay / AI): show one panel at a time and
// remember the last one opened for the session.
let settingsCat = 'appearance';
function showSettingsCat(cat) {
  settingsCat = cat;
  document.querySelectorAll('#dlgSettings .settings-cat').forEach(el =>
    el.classList.toggle('active', el.dataset.cat === cat));
  document.querySelectorAll('#setTabs button').forEach(b =>
    b.classList.toggle('sel', b.dataset.cat === cat));
}

// saveRelay persists the relay address; the node (re)starts background sync.
async function saveRelay() {
  const addr = document.getElementById('relaySetAddr').value.trim();
  const msg = document.getElementById('relaySetMsg');
  try {
    await api('/api/settings', { method: 'POST',
      body: JSON.stringify({ ...currentSettingsBody(), relay: addr }) });
    msg.textContent = addr ? 'saved — syncing through ' + addr : 'relay sync off';
  } catch (e) { msg.textContent = e.message; }
}

// currentSettingsBody snapshots the LLM fields so a relay save doesn't wipe
// them (the settings POST is a full replace; the key stays blank = keep).
function currentSettingsBody() {
  return {
    theme: localStorage.getItem('qp.theme') || 'dark',
    preset: localStorage.getItem('qp.preset') || 'quiet-glass',
    render_mode: localStorage.getItem('qp.rendermode') || 'auto',
    relay_sync_seconds: relayIntervalField(),
    llm: {
      provider: llmSettings.provider || '', model: llmSettings.model || '',
      base_url: llmSettings.base_url || '', api_key: '',
    },
  };
}
// relayIntervalField reads the sync-cadence input, clamped [1,3600]; blank
// or invalid falls back to the 2s default (the node clamps too).
function relayIntervalField() {
  const n = parseInt(document.getElementById('relaySetInterval').value, 10);
  if (!Number.isFinite(n) || n < 1) return 2;
  return Math.min(n, 3600);
}
function syncSettingsUI() {
  const theme = localStorage.getItem('qp.theme') || 'dark';
  const preset = localStorage.getItem('qp.preset') || 'quiet-glass';
  document.querySelectorAll('#setTheme button').forEach(b =>
    b.classList.toggle('sel', b.dataset.v === theme));
  document.querySelectorAll('#setPreset button').forEach(b =>
    b.classList.toggle('sel', b.dataset.v === preset));
  const rm = localStorage.getItem('qp.rendermode') || 'auto';
  document.querySelectorAll('#setRenderMode button').forEach(b =>
    b.classList.toggle('sel', b.dataset.v === rm));
  const fx = localStorage.getItem('qp.effects') || 'full';
  const sky = (typeof SKY !== 'undefined') ? SKY.userMode() : 'drift';
  document.querySelectorAll('#setStarfield button').forEach(b =>
    b.classList.toggle('sel', b.dataset.v === sky));
  document.querySelectorAll('#setEffects button').forEach(b =>
    b.classList.toggle('sel', b.dataset.v === fx));
  const atmo = (typeof STAGE !== 'undefined') ? STAGE.userMode() : 'full';
  document.querySelectorAll('#setAtmosphere button').forEach(b =>
    b.classList.toggle('sel', b.dataset.v === atmo));
  const snd = (typeof ATMO !== 'undefined') ? ATMO.soundMode() : 'ask';
  document.querySelectorAll('#setAtmosphereSound button').forEach(b =>
    b.classList.toggle('sel', b.dataset.v === snd));
}

// ---- AI composition (SC-4): prompt → validated proposal → accept ----
let genProposal = null; // { override, appearance } once generated
async function openGenerate() {
  dlgCustomize.close();
  genProposal = null;
  document.getElementById('genMsg').textContent = '';
  document.getElementById('genAcceptBtn').style.display = 'none';
  let provider = '';
  try { provider = (await api('/api/settings')).llm.provider || ''; } catch (e) {}
  document.getElementById('genProviderNote').textContent = provider
    ? `A proposal from your ${provider} model — you approve it before it's saved.`
    : 'Set up an AI provider in Settings first.';
  dlgGenerate.showModal();
}
function closeGenerate() {
  // Drop any un-accepted preview by re-applying the committed appearance.
  if (genProposal && current) { appearanceSpace = null; loadSpaceAppearance(current); }
  genProposal = null;
  dlgGenerate.close();
}
async function doGenerate() {
  const sid = current; if (!sid) return;
  const prompt = document.getElementById('genPrompt').value.trim();
  if (!prompt) return;
  const msg = document.getElementById('genMsg');
  const btn = document.getElementById('genRunBtn');
  msg.textContent = 'Thinking…'; btn.disabled = true;
  try {
    const r = await api(`/api/spaces/${sid}/appearance/proposals`,
      { method: 'POST', body: JSON.stringify({ prompt }) });
    if (!r.ok) { msg.textContent = r.error || 'generation failed'; return; }
    genProposal = r;
    // Live preview: apply the proposed appearance to the scoped canvas.
    applyScopedAppearance(document.getElementById('content'), r.appearance, renderMode());
    msg.textContent = 'Preview applied. Keep it, or generate again.';
    msg.className = 'hint';
    document.getElementById('genAcceptBtn').style.display = '';
    document.getElementById('genRunBtn').textContent = 'Regenerate';
  } catch (err) { msg.textContent = err.message; }
  finally { btn.disabled = false; }
}
async function acceptGenerate() {
  const sid = current; if (!sid || !genProposal) return;
  try {
    await api(`/api/spaces/${sid}/appearance`, { method: 'PATCH', body: JSON.stringify(genProposal.override) });
    appearanceSpace = null; await loadSpaceAppearance(sid);
    genProposal = null;
    dlgGenerate.close();
  } catch (err) { document.getElementById('genMsg').textContent = err.message; }
}
function pickProvider(p, keepModel) {
  llmSettings.provider = p;
  document.querySelectorAll('#setProvider button').forEach(b =>
    b.classList.toggle('sel', b.dataset.v === p));
  // Custom needs a base URL + key; Local needs a base URL, no key.
  document.getElementById('rowBaseURL').style.display =
    (p === 'openai-compatible' || p === 'local') ? '' : 'none';
  document.getElementById('rowKey').style.display = (p === 'local') ? 'none' : '';
}
async function persistPrefs() {
  // Mirror shell prefs into the keystore alongside the LLM config.
  try {
    await api('/api/settings', { method: 'POST', body: JSON.stringify({
      theme: localStorage.getItem('qp.theme') || 'dark',
      preset: localStorage.getItem('qp.preset') || 'quiet-glass',
      render_mode: localStorage.getItem('qp.rendermode') || 'auto',
      llm: { provider: llmSettings.provider, model: llmSettings.model,
        base_url: llmSettings.base_url }, // no api_key → keep stored
    }) });
  } catch (e) { /* offline-friendly: localStorage already holds prefs */ }
}
async function saveLLM() {
  const msg = document.getElementById('llmMsg');
  llmSettings.model = document.getElementById('llmModel').value.trim();
  llmSettings.base_url = document.getElementById('llmBaseURL').value.trim();
  const key = document.getElementById('llmKey').value;
  try {
    await api('/api/settings', { method: 'POST', body: JSON.stringify({
      theme: localStorage.getItem('qp.theme') || 'dark',
      preset: localStorage.getItem('qp.preset') || 'quiet-glass',
      render_mode: localStorage.getItem('qp.rendermode') || 'auto',
      relay: (document.getElementById('relaySetAddr').value || '').trim(),
      relay_sync_seconds: relayIntervalField(),
      llm: { provider: llmSettings.provider, model: llmSettings.model,
        base_url: llmSettings.base_url, api_key: key || undefined },
    }) });
    document.getElementById('llmKey').value = '';
    msg.textContent = 'Saved.'; msg.className = 'hint';
  } catch (err) { msg.textContent = err.message; }
}
async function testLLM() {
  const msg = document.getElementById('llmMsg');
  msg.textContent = 'Testing…';
  await saveLLM();
  try {
    const r = await api('/api/settings/llm/test', { method: 'POST' });
    msg.textContent = r.ok ? '✓ Connected.' : ('✗ ' + (r.error || 'failed'));
  } catch (err) { msg.textContent = '✗ ' + err.message; }
}
applyAppearance();
if (window.matchMedia) {
  window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
    if ((localStorage.getItem('qp.theme') || 'dark') === 'auto') applyAppearance();
  });
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

let authDead = false; // wrong/missing token → stop polling, keep the console quiet
async function refresh() {
  if (authDead) return;
  try {
    status = await api('/api/status');
    document.body.classList.toggle('protocol', PROTOCOL);
    document.getElementById('protoToggle').textContent = PROTOCOL ? t('protocol.on') : t('protocol.toggle');

    // Connection chip: human by default, raw string only in Protocol view.
    //
    // COMPUTED FULLY, THEN PAINTED ONCE, AND ONLY ON CHANGE. The old shape
    // painted the base summary, awaited /api/relay/status over the network,
    // and painted again — so every refresh flashed "Not connected" before
    // settling on "relay", the text length changed, and the whole header
    // twitched in rhythm with the poll. A chip that reports a steady state
    // must not itself be unsteady.
    let chipText, chipCls, pulseMs = 0;
    if (PROTOCOL) {
      const lan = status.lan;
      chipText = lan.listening ? `LAN :${lan.port} · ${lan.peers} peers` : 'LAN off';
      chipCls = 'conn-chip' + (lan.peers ? ' up' : ' off');
    } else {
      const s = connectionSummary(status);
      chipText = s.text;
      chipCls = 'conn-chip ' + s.cls + (s.cls === 'off' ? '' : ' up');
      // Relay auto-sync: when there's no direct peer, show that the relay
      // is carrying us (honest — store-and-forward, not live).
      const peers = (status.lan.peers || 0) + (status.mesh.connected ? 1 : 0);
      if (peers === 0) {
        try {
          const rs = await api('/api/relay/status');
          if (rs.active && !rs.last_error) {
            chipText = 'relay';
            chipCls = 'conn-chip up';
            // The light breathes in the sync's own rhythm — the pulse IS the
            // cadence, not decoration. Clamped so a 2s cycle does not strobe
            // and a 5min one does not look dead.
            pulseMs = Math.min(12000, Math.max(2400, rs.interval_ms || 4000));
          } else if (rs.active) {
            chipText = 'relay · issue';
            chipCls = 'conn-chip off';
          }
        } catch (e) { /* relay status optional */ }
      }
    }
    const conn = document.getElementById('conn');
    const cText = document.getElementById('connText');
    // One write, and only when something actually changed: a chip repainted
    // with identical content still restarts its CSS animation, which reads
    // as a stutter in the breathing.
    if (connChipSig !== chipText + '|' + chipCls + '|' + pulseMs) {
      connChipSig = chipText + '|' + chipCls + '|' + pulseMs;
      cText.textContent = chipText;
      conn.className = chipCls;
      if (pulseMs) conn.style.setProperty('--sync-pulse', pulseMs + 'ms');
      else conn.style.removeProperty('--sync-pulse');
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
    if (spacesCache.length === 0) {
      const empty = document.createElement('div');
      empty.className = 'empty-spaces';
      empty.innerHTML = `<div class="et">${esc(t('spaces.empty.title'))}</div>` +
        `<div class="eb">${esc(t('spaces.empty.body'))}</div>`;
      box.appendChild(empty);
    }
    for (const s of spacesCache) {
      const d = document.createElement('div');
      d.className = 'space' + (s.id === current ? ' active' : '');
      d.setAttribute('role', 'button');
      d.tabIndex = 0;
      // display_title, not title: an unnamed line reads as the other person
      // on each side, which one shared title could never do. The node
      // projects it; the real title is still what the space is called.
      const title = s.display_title || s.title || t('conv.member');
      const an = ARCHETYPES[s.character?.archetype]?.name || '';
      const bits = [];
      if (an) bits.push(esc(an));
      bits.push(esc(t('spaces.activity.events', { count: s.events })));
      if (s.undecryptable) bits.push(`<span class="undec">${s.undecryptable}✦</span>`);
      d.innerHTML =
        `<span class="space-av"><span class="glyph g24">${glyphSVG(s.id, 'space', 24)}</span></span>` +
        `<span class="space-main"><span class="space-top">` +
          `<span class="t">${esc(title)}</span>` +
          (s.owned ? `<span class="owner-tag">${esc(t('spaces.owner'))}</span>` : '') +
        `</span><span class="m">${bits.join(' · ')}</span></span>`;
      d.onclick = () => { current = s.id; refresh(); };
      d.onkeydown = (e) => { if (e.key === 'Enter') { current = s.id; refresh(); } };
      box.appendChild(d);
    }
    if (!current && spacesCache.length) { current = spacesCache[0].id; }
    if (current) await refreshSpace();
  } catch (e) {
    if (String(e.message || e).includes('token')) {
      // No valid session token in the URL: stop polling instead of filling
      // the console — the node prints the tokened URL on startup.
      authDead = true;
      console.warn('quiet spaces: open the tokened URL printed by the node (…/?token=…)');
      return;
    }
    console.error(e);
  }
}

function currentSpace() { return spacesCache.find(s => s.id === current); }

// PA-0: the public-access surface of the open space — badge, share link,
// and the honest composer state for readers.
function renderPublicSurface(sp) {
  const badge = document.getElementById('visBadge');
  const share = document.getElementById('shareLinkBtn');
  const composer = document.getElementById('composer');
  const readerBar = document.getElementById('readerBar');
  const joinBtn = document.getElementById('joinWriteBtn');
  const note = document.getElementById('readOnlyNote');
  if (!sp || !sp.visibility) {
    badge.style.display = 'none';
    share.style.display = 'none';
    composer.style.display = '';
    readerBar.style.display = 'none';
    return;
  }
  const pub = sp.visibility === 'public';
  let label;
  if (sp.publish === 'curated') {
    label = (pub ? 'PUBLIC' : 'UNLISTED') + ' · READ ONLY';
  } else if (sp.can_write) {
    label = (pub ? 'PUBLIC' : 'UNLISTED') + ' · OPEN';
  } else {
    label = (pub ? 'PUBLIC' : 'UNLISTED') + ' · JOIN TO WRITE';
  }
  if (!pub) label = 'UNLISTED · ANYONE WITH LINK';
  if (sp.frozen) label = 'FROZEN · ' + (pub ? 'PUBLIC' : 'UNLISTED');
  badge.textContent = label;
  badge.title = sp.frozen
    ? 'Publication is paused by the owner. Everything already published stays readable.'
    : 'Anyone who obtains this link can read the space. Access cannot currently be revoked.';
  badge.style.display = '';
  // The badge doubles as the owner's Access sheet entry point (PA-1).
  badge.style.cursor = sp.owned ? 'pointer' : '';
  badge.onclick = sp.owned ? () => openAccess(sp) : null;
  share.style.display = '';
  if (sp.frozen) {
    // Frozen overrides everything below: read-only for all, honestly.
    composer.style.display = 'none';
    readerBar.style.display = '';
    joinBtn.style.display = 'none';
    note.style.display = '';
    note.textContent = 'frozen by the owner — publication is paused';
    return;
  }
  if (sp.can_write) {
    composer.style.display = '';
    readerBar.style.display = 'none';
    return;
  }
  composer.style.display = 'none';
  readerBar.style.display = '';
  const joinable = sp.join === 'open' && sp.role === 'reader';
  joinBtn.style.display = joinable ? '' : 'none';
  note.style.display = joinable ? 'none' : '';
  note.textContent = sp.publish === 'curated'
    ? 'broadcast — read only (owner and curators post)'
    : 'read only';
}

async function copyPublicLink() {
  if (!current) return;
  // Once per device, spell out that a public link is irrevocable before the
  // first copy — after that the badge tooltip carries the same warning.
  if (!localStorage.getItem('qp.linkWarned')) {
    const ok = confirm(
      'This link is irrevocable. Anyone who obtains it can read this space forever — you cannot take it back. Copy it?');
    if (!ok) return;
    localStorage.setItem('qp.linkWarned', '1');
  }
  try {
    const r = await api(`/api/spaces/${current}/link`);
    await navigator.clipboard.writeText(r.link);
    const b = document.getElementById('shareLinkBtn');
    b.textContent = 'Copied · irrevocable';
    setTimeout(() => { b.textContent = 'Share link'; }, 1800);
  } catch (e) { alert(e.message); }
}

async function joinCurrentPublic() {
  if (!current) return;
  try {
    await api(`/api/spaces/${current}/join`, { method: 'POST' });
    await refresh();
  } catch (e) { alert(e.message); }
}

// ---- PA-1: Discover (federated catalog) ----

// defaultCatalog is the official catalog's share link, baked into the
// client. A catalog is just a public broadcast space whose posts are
// space-cards; anyone can run their own and point the client at it.
const defaultCatalog = ''; // set when the official catalog space exists

function catalogLink() {
  return (localStorage.getItem('qp.catalog') || defaultCatalog).trim();
}

// renderRelayPublicPanel shows per-public-space checkpoint/ingress health
// (PA-1.3 monitoring): publisher seq + last-publish age, reader accepted
// seq, frozen state, and how many frames policy has ignored.
async function renderRelayPublicPanel() {
  const box = document.getElementById('relayPublicPanel');
  if (!box) return;
  box.innerHTML = '';
  try {
    const rs = await api('/api/relay/status');
    const rows = rs.public || [];
    if (!rows.length) return;
    const h = document.createElement('p');
    h.className = 'hint'; h.textContent = 'Public spaces';
    box.appendChild(h);
    for (const s of rows) {
      const row = document.createElement('div');
      row.className = 'relay-public-row';
      const parts = [`${s.role}`, `seq ${s.seq || 0}`];
      if (s.role === 'publisher' && s.seconds_since_publish != null)
        parts.push(`published ${s.seconds_since_publish}s ago`);
      if (s.frozen) parts.push('FROZEN');
      if (s.ignored_total) parts.push(`${s.ignored_total} ignored`);
      row.innerHTML = `<span class="rp-title">${esc(s.title || s.space_id.slice(0, 8))}</span>` +
        `<span class="rp-meta">${esc(parts.join(' · '))}</span>`;
      box.appendChild(row);
    }
  } catch (e) { /* relay status optional */ }
}

function saveCatalog() {
  const v = document.getElementById('catalogLink').value.trim();
  if (v) localStorage.setItem('qp.catalog', v);
  else localStorage.removeItem('qp.catalog');
  document.getElementById('catalogMsg').textContent = v ? 'saved' : 'using the official catalog';
}

async function openDiscover() {
  const link = catalogLink();
  if (!link) {
    alert('No catalog configured yet. Paste a catalog link in Settings → Relay → Catalog.');
    return;
  }
  try {
    const r = await api('/api/public/open', { method: 'POST', body: JSON.stringify({ link }) });
    current = r.id;
    await refresh();
    if (typeof switchView === 'function') switchView('posts');
  } catch (e) { alert(e.message); }
}

// spaceCardLink extracts a space-card's target: the first link block's URL.
function spaceCardLink(doc) {
  for (const b of doc.blocks || []) {
    if (b.type === 'link' && b.props?.text) return b.props.text;
  }
  return '';
}

// openSpaceCard opens the space referenced by a space-card document: the
// share link travels in the card's first link block under the "qs:" scheme.
async function openSpaceCard(link) {
  let share = (link || '').trim();
  if (share.startsWith('qs:')) share = share.slice(3);
  try {
    const r = await api('/api/public/open', { method: 'POST', body: JSON.stringify({ link: share }) });
    current = r.id;
    dlgJoin.close?.();
    await refresh();
  } catch (e) { alert(e.message); }
}

// ---- PA-1: owner Access sheet (policy revisions + freeze) ----

let accessSpace = null;
function openAccess(sp) {
  accessSpace = sp.id;
  document.querySelectorAll('#accVis button').forEach(b =>
    b.classList.toggle('sel', b.dataset.v === sp.visibility));
  const mode = sp.publish === 'curated' ? 'curated' : 'all';
  document.querySelectorAll('#accMode button').forEach(b =>
    b.classList.toggle('sel', b.dataset.v === mode));
  document.getElementById('accFreezeBtn').textContent =
    sp.frozen ? 'Unfreeze publication' : 'Freeze publication';
  document.getElementById('accMsg').textContent = '';
  document.getElementById('accCurPrin').value = '';
  document.getElementById('accCurDev').value = '';
  renderCurators(sp);
  dlgAccess.showModal();
}

function renderCurators(sp) {
  // The curator list is parsed client-side from nothing yet — v1 shows a
  // hint; removals go through the exact pair below.
  const box = document.getElementById('accCurators');
  box.innerHTML = '';
  const row = document.createElement('div');
  row.className = 'list-row';
  row.innerHTML = `<span class="lr-label"><span class="lr-sub">Paste the exact principal + device pair to remove a curator; adding uses the fields below.</span></span>`;
  const rm = document.createElement('button');
  rm.className = 'btn-plain';
  rm.textContent = 'Remove pair';
  rm.onclick = () => reviseAccess({ remove_curator: curatorFields() });
  row.appendChild(rm);
  box.appendChild(row);
}

function curatorFields() {
  return {
    principal: document.getElementById('accCurPrin').value.trim(),
    device: document.getElementById('accCurDev').value.trim(),
  };
}

async function reviseAccess(delta) {
  if (!accessSpace) return;
  const msg = document.getElementById('accMsg');
  try {
    await api(`/api/spaces/${accessSpace}/policy`, {
      method: 'POST', body: JSON.stringify(delta) });
    msg.textContent = 'revised — readers pick it up with the next projection';
    await refresh();
    const sp = spacesCache.find(s => s.id === accessSpace);
    if (sp) openAccess(sp); // re-sync the sheet controls
  } catch (e) { msg.textContent = e.message; }
}

function setAccessVis(v) { reviseAccess({ visibility: v }); }
function setAccessMode(m) {
  if (m === 'curated' && !confirm(
    'Switch to broadcast? Only you and curators will be able to post. Existing posts remain.')) return;
  reviseAccess({ publish: m });
}
function addCurator() { reviseAccess({ add_curator: curatorFields() }); }
function toggleFreeze() {
  const sp = spacesCache.find(s => s.id === accessSpace);
  const next = !(sp && sp.frozen);
  if (next && !confirm(
    'Freeze publication? EVERYONE stops posting — including you — until you unfreeze. Readers keep what is already published.')) return;
  reviseAccess({ frozen: next });
}

// Conversation header actions (PUI-1).
function openPassCurrent() { const s = currentSpace(); if (s) openPass(s); }
function toggleMembers() {
  document.getElementById('members').classList.toggle('hidden');
}

async function refreshSpace() {
  const sp = currentSpace();
  const char = sp?.character;
  applyTheme(char);
  loadSpaceAppearance(current);
  // Conversation header: title + invite (owned private) + info toggle.
  document.getElementById('convTitle').textContent =
    sp ? (sp.display_title || sp.title || t('conv.member')) : '';
  document.getElementById('inviteBtn').style.display = (sp?.owned && !sp?.visibility) ? '' : 'none';
  document.getElementById('customizeBtn').style.display = sp?.owned ? '' : 'none';
  document.getElementById('resPalBtn').style.display = sp?.owned ? '' : 'none';
  renderPublicSurface(sp);

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
  presenceSetStates([...new Set(char?.presence || [])]);

  await RES.load(current); // palette must exist before rows render
  const entries = await api(`/api/spaces/${current}/entries`);
  const log = document.getElementById('log');
  const stick = log.scrollTop + log.clientHeight >= log.scrollHeight - 40;
  if (seenSpace !== current) {
    seenSpace = current; seenEntries = new Set(); hereMembers = new Set();
    feedSig = []; feedContentSig = [];
    // Presence is per-space, so the countdown does not follow you into a
    // room where it was never posted.
    presenceForget();
    RES.refresh(current);
    if (typeof mentionsReset === 'function') {
      mentionsReset();
      mentionsLoadMembers(current);
    }
    // Tell the node which room is open: QuietRank stays quiet about the one
    // you are already reading.
    api('/api/attention/viewing', { method: 'POST', body: JSON.stringify({ space: current }) })
      .catch(() => {});
    if (typeof pubView !== 'undefined' && pubView !== 'chat') switchView('chat');
  }
  // Incremental feed render, three paths: (1) nothing changed → nothing
  // re-renders (a playing <video>/<audio> is never torn down); (2) appended
  // only → add the new rows; (3) ONLY resonance changed → patch the res
  // rows in place and hand the deltas to effects. Anything else rebuilds.
  const contentSig = entries.map(e => e.id + ':' + (e.revised ? 1 : 0) + ':' +
    (e.kept ? 'k' : '') + (e.keep_count || 0));
  // Asset fetch state is part of what a row shows (poster → progress →
  // playable); without it here, a fetch completing never re-renders and you
  // only see the media after a full page reload.
  const aSig = entries.map(e => e.asset ? (e.asset.state + '/' + (e.asset.missing || 0) + '/' + (e.asset.total || 0)) : '');
  const rSig = entries.map(e => resSig(e.resonance));
  const sig = contentSig.map((c, i) => c + '|' + aSig[i] + '|' + rSig[i]);
  const unchanged = sig.length === feedSig.length && sig.every((s, i) => s === feedSig[i]);
  const appendOnly = sig.length > feedSig.length && feedSig.every((s, i) => s === sig[i]);
  // Same rows/order — only asset progress and/or resonance moved. Patch just
  // the changed rows in place so unrelated playing media is never torn down.
  const structSame = !unchanged && !appendOnly &&
    contentSig.length === feedContentSig.length &&
    contentSig.every((c, i) => c === feedContentSig[i]);
  const firstPaint = seenEntries.size === 0;
  if (structSame) {
    for (let i = 0; i < entries.length; i++) {
      if (sig[i] === feedSig[i]) continue;
      const e = entries[i];
      const node = log.querySelector(`[data-eid="${e.id}"]`);
      if (!node) { feedSig = []; feedContentSig = []; refreshSpace(); return; }
      const prevA = (feedSig[i] || '').split('|')[1] || '';
      if (aSig[i] !== prevA) {
        // Asset fetch state changed (progress/complete): re-render this row so
        // its card and progress update live. Safe — a fetching asset has no
        // playing <video> to tear down.
        const grouped = i > 0 && entries[i - 1].author === e.author &&
          entries[i - 1].produced_by === e.produced_by;
        const tmp = document.createElement('div');
        renderEntry(tmp, e, false, grouped);
        const fresh = tmp.firstElementChild;
        if (fresh) node.replaceWith(fresh);
      } else {
        // Resonance-only change: patch just the res row, never the media.
        const fresh = renderResonanceRow(e.resonance, e.id);
        const old = node.querySelector('.res-row');
        if (old) old.replaceWith(fresh); else node.querySelector('.bubble')?.appendChild(fresh);
        if (typeof RESFX !== 'undefined') RESFX.onAggregateChange(node, e.resonance);
      }
    }
    feedSig = sig; feedContentSig = contentSig;
  } else if (!unchanged) {
    let prev = null;
    if (appendOnly && !firstPaint) {
      prev = entries[feedSig.length - 1] || null;
      for (let i = feedSig.length; i < entries.length; i++) {
        const e = entries[i];
        if (e.id) seenEntries.add(e.id);
        const grouped = prev && prev.author === e.author && prev.produced_by === e.produced_by;
        renderEntry(log, e, true, grouped);
        prev = e;
      }
    } else {
      log.innerHTML = '';
      for (const e of entries) {
        const fresh = !firstPaint && e.id && !seenEntries.has(e.id);
        if (e.id) seenEntries.add(e.id);
        const grouped = prev && prev.author === e.author && prev.produced_by === e.produced_by;
        renderEntry(log, e, fresh, grouped);
        prev = e;
      }
    }
    feedSig = sig; feedContentSig = contentSig;
    if (stick) log.scrollTop = log.scrollHeight;
  }

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
  // Our own card is the source of truth for the composer's presence button,
  // including on a tab that has only just opened.
  presenceAdopt(members);
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
  const summary = presenceSummary(members);
  if (summary) {
    const ps = document.createElement('div');
    ps.className = 'presence-summary'; ps.setAttribute('aria-live', 'polite');
    ps.textContent = summary;
    mbox.appendChild(ps);
  }
  const nextHere = new Set();
  for (const m of members) {
    const d = document.createElement('div');
    d.className = 'member';
    let pres;
    const here = m.presence.known && m.presence.current;
    if (here) nextHere.add(m.terminal);
    if (!m.presence.known) pres = `<div class="pres stale">${esc(t('presence.unknown'))}</div>`;
    else if (m.presence.current) pres = `<div class="pres current">● ${esc(m.presence.state)}</div>`;
    else pres = `<div class="pres stale">${esc(m.presence.state)} · ${esc(relTime(m.presence.age_seconds))}</div>`;
    const glyphType = memberGlyphType(m);
    const name = m.name || m.terminal.slice(0, 10);
    const kindBadge = m.kind === 'human' ? '' :
      `<span class="badge b-${m.agency === 'ai_agent' ? 'ai_agent' : 'deterministic_bot'}">${esc(m.kind)}</span>`;
    // Quiet-by-default: a member who is here gets a steady "present" glyph
    // (static). The gentle pulse fires ONCE, only on the transition into
    // presence — never as an idle loop. Reduced-motion drops the pulse.
    const arriving = here && !hereMembers.has(m.terminal);
    const glyphCls = 'glyph g24' + (here ? ' present' : '') + (arriving ? ' pulse-in' : '');
    let inner =
      `<div class="mhead"><span class="${glyphCls}">${glyphSVG(m.principal || m.terminal, glyphType, 24)}</span>` +
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
  hereMembers = nextHere;
}

function fmtAge(s) {
  if (s < 90) return `${s}s`;
  if (s < 5400) return `${Math.round(s / 60)}m`;
  return `${Math.round(s / 3600)}h`;
}

// A living space has living members: humans, deterministic bots, sensors —
// each gets a distinct glyph form (UI-3). Form carries the difference; the
// badge still names the kind honestly.
function memberGlyphType(m) {
  if (m.kind === 'human') return 'human';
  if (m.kind === 'space') return 'space';
  const k = (m.kind || '').toLowerCase();
  if (k.includes('sensor') || k.includes('iot')) return 'iot';
  if (m.ai_present || m.agency === 'ai_agent' || k.includes('bot') || k.includes('agent')) return 'bot';
  return 'device';
}

// A single human line above the member list: who is here, who was recently.
function presenceSummary(members) {
  const here = members.filter(m => m.presence.known && m.presence.current);
  const recent = members
    .filter(m => m.presence.known && !m.presence.current && m.presence.age_seconds < 3600)
    .sort((a, b) => a.presence.age_seconds - b.presence.age_seconds);
  const parts = [];
  for (const m of here) parts.push(t('presence.is_here', { who: m.name || m.terminal.slice(0, 8) }));
  for (const m of recent.slice(0, 2))
    parts.push(t('presence.last_seen', { who: m.name || m.terminal.slice(0, 8), ago: relTime(m.presence.age_seconds) }));
  return parts.join(' · ');
}

// ---- actions ----

// replyTarget holds what the composer is answering, if anything. A reply is
// a real structural edge (reply_to on the signed message), not a quote
// pasted into the text.
let replyTarget = null; // { id, author, name }

// startReply aims the composer at a message and stages its author as a
// mention, so answering someone always reaches them.
function startReply(e) {
  replyTarget = {
    id: e.id,
    author: e.author_full || e.author,
    name: e.author_name || t('conv.member'),
  };
  if (typeof mentionsStage === 'function' && replyTarget.author) {
    mentionsStage(replyTarget.author, replyTarget.name);
  }
  renderReplyBar();
  document.getElementById('text')?.focus();
}

function cancelReply() {
  // Cancelling a reply also stops addressing that person: the mention came
  // from the act of replying, so it leaves with it.
  if (replyTarget && typeof mentionsUnstage === 'function') {
    mentionsUnstage(replyTarget.author);
  }
  replyTarget = null;
  renderReplyBar();
}

function renderReplyBar() {
  const bar = document.getElementById('replyBar');
  if (!bar) return;
  if (!replyTarget) {
    bar.style.display = 'none';
    bar.innerHTML = '';
    return;
  }
  bar.style.display = '';
  bar.innerHTML = '';
  const label = document.createElement('span');
  label.className = 'reply-to';
  label.textContent = '↩ ' + t('conv.replying_to', { name: replyTarget.name });
  bar.appendChild(label);
  const x = document.createElement('button');
  x.type = 'button';
  x.className = 'reply-cancel';
  x.textContent = '✕';
  x.title = t('conv.cancel_reply');
  x.onclick = cancelReply;
  bar.appendChild(x);
}

async function say(e) {
  e.preventDefault();
  const inp = document.getElementById('text');
  if (!inp.value.trim() || !current) return false;
  // Mentions travel as a signed structural field, not as text markup.
  const mentions = typeof mentionsPayload === 'function' ? mentionsPayload(inp.value) : [];
  const body = { text: inp.value, mentions };
  if (replyTarget) body.reply_to = replyTarget.id;
  try {
    await api(`/api/spaces/${current}/messages`, {
      method: 'POST',
      body: JSON.stringify(body),
    });
    inp.value = '';
    cancelReply();
    if (typeof mentionsReset === 'function') mentionsReset();
    await refreshSpace();
  } catch (err) { alert(err.message); }
  return false;
}

// ---- presence picker ----
//
// A native <select> would be less code, but its popup is drawn by the
// operating system and cannot be themed: it arrives white-and-blue in the
// middle of a dark room. This is the same control drawn by us — and since
// choosing a presence posts an event rather than setting a lasting form
// value, a menu is also the more honest shape for it.

const PRESENCE_TTL = 300;
let presenceStates = [];
let presenceLive = '';   // the state the node reports for us, echoed on the button
let presenceUntil = 0;   // epoch seconds, derived from the signed claim's expiry
let presenceTimer = null;

/** Rebuild the menu for the space's declared vocabulary. */
function presenceSetStates(states) {
  presenceStates = states;
  const menu = document.getElementById('presenceMenu');
  menu.innerHTML = states.map(s =>
    `<button type="button" role="menuitem" onclick="setPresence('${esc(s)}')">${esc(s)}</button>`
  ).join('') || '<div class="picker-empty">this space declares no presence states</div>';
  document.getElementById('presencePicker').classList.toggle('none', !states.length);
}

function presenceOpen() { return !document.getElementById('presenceMenu').hidden; }

/** @param {'first'|'last'} [focus] where to land when opened by keyboard */
function presenceToggle(focus) {
  const menu = document.getElementById('presenceMenu');
  const btn = document.getElementById('presenceBtn');
  const open = menu.hidden;
  menu.hidden = !open;
  btn.setAttribute('aria-expanded', String(open));
  if (open) {
    document.addEventListener('mousedown', presenceOutside, true);
    const items = menu.querySelectorAll('[role=menuitem]');
    if (focus === 'first') items[0]?.focus();
    else if (focus === 'last') items[items.length - 1]?.focus();
  } else {
    document.removeEventListener('mousedown', presenceOutside, true);
  }
}

function presenceClose(refocus) {
  if (!presenceOpen()) return;
  presenceToggle();
  if (refocus) document.getElementById('presenceBtn').focus();
}

function presenceOutside(e) {
  if (!document.getElementById('presencePicker').contains(e.target)) presenceClose();
}

function presenceBtnKey(e) {
  if (presenceOpen()) { if (e.key === 'Escape') presenceClose(); return; }
  const open = { ArrowDown: 'first', ArrowUp: 'last', Enter: 'first', ' ': 'first' }[e.key];
  if (!open) return;
  e.preventDefault();
  // The same keydown bubbles on to the document handler below, which would
  // then treat the freshly focused item as "already there" and step past it.
  // Opening and moving are one gesture, not two.
  e.stopPropagation();
  presenceToggle(open);
}

// Arrow keys move within the menu and wrap; Escape returns to the button.
// Without this a custom menu is strictly worse than the native select it
// replaced, whatever it looks like.
document.addEventListener('keydown', e => {
  if (!presenceOpen()) return;
  const items = [...document.querySelectorAll('#presenceMenu [role=menuitem]')];
  const at = items.indexOf(document.activeElement);
  if (e.key === 'Escape') { e.preventDefault(); presenceClose(true); }
  else if (e.key === 'ArrowDown') { e.preventDefault(); items[(at + 1) % items.length]?.focus(); }
  else if (e.key === 'ArrowUp') { e.preventDefault(); items[(at - 1 + items.length) % items.length]?.focus(); }
  else if (e.key === 'Home') { e.preventDefault(); items[0]?.focus(); }
  else if (e.key === 'End') { e.preventDefault(); items[items.length - 1]?.focus(); }
  else if (e.key === 'Tab') { presenceClose(); }
});

/**
 * Adopt whatever the node reports as OUR presence in this space.
 *
 * This is what makes the button survive a reload: the state is not something
 * the tab remembered, it is read back from our own member card, and the
 * remaining time comes from the expiry we signed into the claim. A tab that
 * has just opened knows exactly as much as one that has been sitting here.
 * @param {Array<any>} members
 */
function presenceAdopt(members) {
  const me = members.find(m => m.mine);
  const p = me && me.presence;
  if (!p || !p.known || !p.current) { presenceForget(); return; }
  presenceLive = p.state;
  presenceUntil = Math.floor(Date.now() / 1000) + (p.remaining_seconds || 0);
  if (!presenceTimer) presenceTimer = setInterval(presenceRender, 1000);
  presenceRender();
}

/**
 * Draw the button from that state, and stop when it runs out. The countdown
 * is local only between refreshes — every refresh replaces it with what the
 * node says, so the tab can never drift into claiming presence it does not
 * have.
 */
function presenceRender() {
  const label = document.getElementById('presenceLabel');
  const picker = document.getElementById('presencePicker');
  const left = presenceUntil - Math.floor(Date.now() / 1000);
  if (left <= 0) {
    presenceUntil = 0;
    label.textContent = 'presence…';
    picker.classList.remove('live');
    if (presenceTimer) { clearInterval(presenceTimer); presenceTimer = null; }
    return;
  }
  label.textContent = `${presenceLive} · ${Math.ceil(left / 60)}m`;
  picker.classList.add('live');
}

function presenceForget() {
  presenceUntil = 0; presenceLive = '';
  if (presenceTimer) { clearInterval(presenceTimer); presenceTimer = null; }
  presenceRender();
}

async function setPresence(state) {
  if (!state || !current) return;
  presenceClose(true);
  try {
    await api(`/api/spaces/${current}/presence`, { method: 'POST', body: JSON.stringify({ state, ttl_seconds: PRESENCE_TTL }) });
    // Paint immediately so the click has an answer, then let the refresh
    // below replace it with the node's own reading.
    presenceLive = state;
    presenceUntil = Math.floor(Date.now() / 1000) + PRESENCE_TTL;
    if (!presenceTimer) presenceTimer = setInterval(presenceRender, 1000);
    presenceRender();
    refreshSpace();
  } catch (err) { alert(err.message); }
}

// ---- Enter with a pass: async state machine (UI-2) ----
//
// A pass is a right to ask, not a key. We send a request to the rendezvous
// and poll the owner's confirmation; the space opens only when confirmed.
// A pending join is honestly pending — no replica exists until "ready".

let joinPoll = null;
let joinedSpaceId = null;

function joinView(which) {
  document.getElementById('joinInput').style.display = which === 'input' ? 'block' : 'none';
  document.getElementById('joinWaiting').style.display = which === 'waiting' ? 'block' : 'none';
}

function resetJoin() {
  if (joinPoll) { clearInterval(joinPoll); joinPoll = null; }
  joinedSpaceId = null;
  document.getElementById('joinPass').value = '';
  document.getElementById('joinOpenBtn').style.display = 'none';
  joinView('input');
  setTimeout(() => document.getElementById('joinPass').focus(), 50);
}

async function requestEntry() {
  const pass = document.getElementById('joinPass').value.trim();
  if (!pass) return;
  // Legacy device-id/xpub invites (base64 std) still work — they open at once.
  // A Space Pass link is base64url of "relay\nenvelope"; try the pass path
  // first, fall back to the classic invite if the backend rejects it.
  document.getElementById('joinEnterBtn').disabled = true;
  try {
    const r = await api('/api/join-requests', { method: 'POST', body: JSON.stringify({ pass }) });
    // The link fragment can linger in history/screenshots — drop it now.
    if (location.hash) history.replaceState(null, '', location.pathname + location.search);
    startJoinWait(r.request_id);
  } catch (err) {
    // Fall back to the classic full-history invite (device already known).
    try {
      const j = await api('/api/invites/accept', { method: 'POST', body: JSON.stringify({ invite: pass }) });
      current = j.id; dlgJoin.close(); refresh();
    } catch (_) {
      showJoinState('rejected', err.message);
    }
  } finally {
    document.getElementById('joinEnterBtn').disabled = false;
  }
}

function startJoinWait(reqID) {
  joinView('waiting');
  document.getElementById('joinGlyph').innerHTML = glyphSVG(reqID, 'pass', 56);
  showJoinState('waiting_for_owner');
  if (joinPoll) clearInterval(joinPoll);
  joinPoll = setInterval(async () => {
    try {
      const s = await api(`/api/join-requests/${reqID}`);
      if (s.status === 'ready') {
        clearInterval(joinPoll); joinPoll = null;
        joinedSpaceId = s.space;
        showJoinState('ready');
        document.getElementById('joinOpenBtn').style.display = '';
      } else if (['expired', 'expired_while_waiting', 'revoked', 'rejected'].includes(s.status)) {
        clearInterval(joinPoll); joinPoll = null;
        showJoinState(s.status);
      }
    } catch (e) { /* transient — keep polling */ }
  }, 1500);
}

function showJoinState(state, detail) {
  const map = {
    waiting_for_owner: 'join.waiting', offline: 'join.offline',
    ready: 'join.ready', expired: 'join.expired',
    expired_while_waiting: 'join.expired_waiting', revoked: 'join.revoked',
    rejected: 'join.rejected',
  };
  const el = document.getElementById('joinStateText');
  el.textContent = detail || t(map[state] || 'join.rejected');
  el.className = 'pass-state' + (state === 'ready' ? ' ok' : (state === 'waiting_for_owner' ? '' : ' warn'));
  joinView('waiting');
}

function openJoined() {
  if (!joinedSpaceId) return;
  current = joinedSpaceId;
  resetJoin();
  dlgJoin.close();
  refresh();
}

function openJoinDialog() {
  resetJoin();
  dlgJoin.showModal();
}

// ---- First run & identity (UI-1) ----

let onboardInfo = null;

async function checkOnboarding() {
  try {
    onboardInfo = await api('/api/onboarding');
    if (onboardInfo.needs_name && !dlgWelcome.open) {
      obStep('welcome');
      dlgWelcome.showModal();
    }
  } catch (e) {
    if (!String(e.message || e).includes('token')) console.error(e);
  }
}

function obStep(which) {
  document.getElementById('obStep1').style.display = which === 'welcome' ? 'block' : 'none';
  document.getElementById('obStep2').style.display = which === 'name' ? 'block' : 'none';
  document.getElementById('obStep3').style.display = which === 'done' ? 'block' : 'none';
  if (which === 'name') setTimeout(() => document.getElementById('obName').focus(), 50);
}

async function obSubmitName() {
  const name = document.getElementById('obName').value.trim();
  if (!name) { alert('please choose a name'); return; }
  try {
    await api('/api/identity/name', { method: 'POST', body: JSON.stringify({ name }) });
    onboardInfo = await api('/api/onboarding');
    document.getElementById('obGlyph').innerHTML =
      `<span class="glyph" style="width:56px;height:56px">${glyphSVG(onboardInfo.fingerprint, 'human', 56)}</span>`;
    document.getElementById('obWho').textContent = onboardInfo.name;
    document.getElementById('obDevice').textContent = onboardInfo.device_name;
    obStep('done');
  } catch (err) { alert(err.message); }
}

function obFinish() { dlgWelcome.close(); refresh(); }

function showIdentity() {
  const fp = status.fingerprint;
  document.getElementById('meGlyph').innerHTML =
    `<span class="glyph" style="width:56px;height:56px">${glyphSVG(fp, 'human', 56)}</span>`;
  if (typeof refreshBackupState === 'function') refreshBackupState();
  document.getElementById('meName').textContent = (onboardInfo && onboardInfo.name) || 'me';
  document.getElementById('meDeviceName').textContent = (onboardInfo && onboardInfo.device_name) || 'this device';
  document.getElementById('meVerify').textContent = verificationPhrase(fp, 4);
  document.getElementById('meDevice').textContent = status.device_id;
  document.getElementById('meXpub').textContent = status.device_xpub;
  document.getElementById('meFp').textContent = fp;
  document.getElementById('meTech').style.display = 'none';
  dlgMe.showModal();
}

async function renameSelf() {
  const name = prompt('Change your name to:', (onboardInfo && onboardInfo.name) || '');
  if (!name || !name.trim()) return;
  try {
    await api('/api/identity/name', { method: 'POST', body: JSON.stringify({ name: name.trim() }) });
    onboardInfo = await api('/api/onboarding');
    dlgMe.close();
    refresh();
  } catch (err) { alert(err.message); }
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

// ---- Space Pass card (UI-2) ----

let passSpace = null;
let mintedPass = null; // { pass_id, link }

function openPass(s) {
  passSpace = s.id;
  mintedPass = null;
  const me = (onboardInfo && onboardInfo.name) || 'someone';
  document.getElementById('passTitle').textContent = t('pass.title');
  document.getElementById('passFrom').textContent = t('pass.invite_from', { who: me });
  document.getElementById('passHint').textContent = t('pass.card_hint');
  document.getElementById('passVerifyHint').textContent = t('pass.verify_hint');
  document.getElementById('passCreateBtn').textContent = t('pass.create');
  document.getElementById('passCopyBtn').textContent = t('pass.copy');
  document.getElementById('passRevokeBtn').textContent = t('pass.revoke');
  document.getElementById('passTechInvite').textContent = t('pass.tech_invite');
  // A pass glyph seeded by the space id — recognisable, not sensitive.
  document.getElementById('passGlyph').innerHTML = glyphSVG(s.id, 'pass', 48);
  document.getElementById('passReady').style.display = 'none';
  document.getElementById('passConfig').style.display = 'block';
  document.getElementById('passCreateBtn').style.display = '';
  document.getElementById('passCopyBtn').style.display = 'none';
  document.getElementById('passRevokeBtn').style.display = 'none';
  document.getElementById('passMsg').style.display = 'none';
  dlgPass.showModal();
}

function passMsg(text, warn) {
  const el = document.getElementById('passMsg');
  el.textContent = text;
  el.className = 'pass-state' + (warn ? ' warn' : ' ok');
  el.style.display = text ? 'block' : 'none';
}

async function mintPass() {
  // The rendezvous relay is the one configured in Settings — no separate field.
  let relay = '';
  try { relay = ((await api('/api/settings')).relay || '').trim(); } catch (_) {}
  if (!relay) { passMsg(t('pass.need_relay'), true); return; }
  const maxUses = parseInt(document.getElementById('passUses').value, 10);
  const ttlHours = parseInt(document.getElementById('passTtl').value, 10);
  document.getElementById('passCreateBtn').disabled = true;
  try {
    const r = await api(`/api/spaces/${passSpace}/passes`, {
      method: 'POST',
      body: JSON.stringify({ max_uses: maxUses, ttl_hours: ttlHours, relay }),
    });
    mintedPass = r;
    document.getElementById('passPhrase').textContent = verificationPhrase(r.pass_id, 3);
    document.getElementById('passTerms').textContent = maxUses === 1
      ? t('pass.terms_one', { hours: ttlHours })
      : t('pass.terms_many', { uses: maxUses, hours: ttlHours });
    document.getElementById('passQr').innerHTML = r.qr_png_base64
      ? `<img alt="pass QR" src="data:image/png;base64,${r.qr_png_base64}">` : '';
    document.getElementById('passLink').textContent = r.link;
    document.getElementById('passReady').style.display = 'block';
    document.getElementById('passConfig').style.display = 'none';
    document.getElementById('passCreateBtn').style.display = 'none';
    document.getElementById('passCopyBtn').style.display = '';
    document.getElementById('passRevokeBtn').style.display = '';
    passMsg('', false);
  } catch (err) { passMsg(err.message, true); }
  finally { document.getElementById('passCreateBtn').disabled = false; }
}

async function copyPass() {
  if (!mintedPass) return;
  try {
    await navigator.clipboard.writeText(mintedPass.link);
    document.getElementById('passCopyBtn').textContent = t('pass.copied');
    setTimeout(() => { document.getElementById('passCopyBtn').textContent = t('pass.copy'); }, 1500);
  } catch (_) { /* clipboard blocked — the link is visible to select manually */ }
}

async function revokeCurrentPass() {
  if (!mintedPass) return;
  try {
    await api(`/api/spaces/${passSpace}/passes/${mintedPass.pass_id}`, { method: 'DELETE' });
    passMsg(t('pass.revoked'), true);
    document.getElementById('passRevokeBtn').style.display = 'none';
  } catch (err) { passMsg(err.message, true); }
}

function openInviteFromPass() {
  dlgPass.close();
  openInvite({ id: passSpace, title: currentSpace()?.title });
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
  const addr = document.getElementById('relaySetAddr').value.trim();
  const info = document.getElementById('relayInfo');
  if (!addr) { info.textContent = 'set a relay address first.'; return; }
  if (!current) { info.textContent = 'open a space first.'; return; }
  try {
    const r = await api('/api/relay/push', { method: 'POST',
      body: JSON.stringify({ addr, space: current }) });
    const until = new Date(r.relay_holds_until * 1000).toLocaleString();
    info.textContent = `relay accepted ${r.events_pushed} events and holds them until ${until}. ` +
      `Status: accepted by relay — this is not delivery.`;
  } catch (err) { info.textContent = 'push failed: ' + err.message; }
}

async function relayPull() {
  const addr = document.getElementById('relaySetAddr').value.trim();
  const info = document.getElementById('relayInfo');
  if (!addr) { info.textContent = 'set a relay address first.'; return; }
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

let wizTemplate = null;

function openWizard() {
  wizTemplate = null;
  const wg = document.getElementById('whoGrid');
  wg.innerHTML = '';
  // Release space (LR-4): the one-click studio preset for listening
  // sessions — the flagship path gets a front door.
  const tpl = document.createElement('div');
  tpl.className = 'arch-card release-tpl';
  tpl.innerHTML = `<div class="an">🎧 Release space</div>` +
    `<div class="ad">studio preset for listening sessions — one click</div>`;
  tpl.onclick = () => applyReleaseTemplate();
  wg.appendChild(tpl);
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

function applyReleaseTemplate() {
  wizTemplate = 'release';
  selectArch('studio');
  document.getElementById('wMood').value = 'dusk';
  document.getElementById('wMaterial').value = 'light';
  document.getElementById('wMotion').value = 'slow';
  updSeed();
  document.querySelectorAll('#ritualList input').forEach(i => {
    i.checked = i.value === 'listening_session';
  });
  wizStep(4); // straight to the name — everything else is the preset
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

// PA-0 visibility choice on the final wizard step.
let wizVis = 'private';
let wizMode = 'community';
function pickWizVis(v) {
  wizVis = v;
  document.querySelectorAll('#wVisibility button').forEach(b =>
    b.classList.toggle('sel', b.dataset.v === v));
  const pub = v === 'public';
  document.getElementById('wPublicMode').style.display = pub ? '' : 'none';
  document.getElementById('wVisHint').style.display = pub ? '' : 'none';
}
function pickWizMode(m) {
  wizMode = m;
  document.querySelectorAll('#wPublicMode button').forEach(b =>
    b.classList.toggle('sel', b.dataset.v === m));
}
// wizPolicy maps the choice onto the API policy fields.
function wizPolicy() {
  if (wizVis !== 'public') return {};
  return wizMode === 'broadcast'
    ? { visibility: 'public', publish: 'curated' }
    : { visibility: 'public', join: 'open' };
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
      ...wizPolicy(),
    })});
    current = r.id; dlgWiz.close(); refresh();
    if (wizTemplate === 'release') {
      // First-post hint: open the composer pre-filled for the first listen.
      wizTemplate = null;
      setTimeout(() => {
        openComposer(null, '');
        composerDoc.title = 'First listen';
        composerDoc.summary = 'Drop the track, gather the room.';
        document.getElementById('compTitle').value = composerDoc.title;
        document.getElementById('compSummary').value = composerDoc.summary;
        composerDoc.blocks.push({ id: 'b' + randHex16().slice(0, 8), type: 'text',
          props: { text: 'Tonight we listen together. Add a 🎧 Listening room below and press play when everyone is here.' } });
        renderComposerBlocks();
      }, 350);
    }
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

function renderEntry(log, e, fresh, grouped) {
  const d = document.createElement('div');
  const own = !!e.mine;
  d.className = 'entry msg' + (own ? ' own' : ' other') +
    (grouped ? ' grouped' : '') + (fresh ? ' enter' : '');

  // Author head (shown once per group, hidden for grouped follow-ons via CSS).
  const who = document.createElement('div');
  who.className = 'who';
  if (!PROTOCOL && !own) {
    const g = document.createElement('span');
    g.className = 'glyph g18';
    g.innerHTML = glyphSVG(e.author, e.produced_by === 'human' ? 'human' : 'device', 18);
    who.appendChild(g);
  }
  const b = badge(e.produced_by);
  if (b) who.appendChild(b);
  const meta = document.createElement('span');
  meta.textContent = (own ? t('conv.you') : authorLabel(e)) +
    (e.revised ? ' · ' + t('conv.edited') : '') + (PROTOCOL ? ' · ' + e.kind : '');
  who.appendChild(meta);
  d.appendChild(who);

  const bubble = document.createElement('div');
  bubble.className = 'bubble';
  bubble.appendChild(renderBody(e));
  bubble.appendChild(renderResonanceRow(e.resonance, e.id));
  d.appendChild(bubble);
  d.dataset.eid = e.id;

  const acts = document.createElement('span');
  acts.className = 'mk';
  // Reply: a real structural edge on the signed message, and it addresses
  // the author so the answer actually reaches them.
  if (!own) {
    const rp = document.createElement('span');
    rp.className = 'reply-act';
    rp.textContent = '↩ ' + t('conv.reply');
    rp.title = t('conv.reply_hint');
    rp.onclick = () => startReply(e);
    acts.appendChild(rp);
  }
  const mk = document.createElement('span');
  mk.textContent = t('conv.save_card');
  mk.onclick = () => makeCardFrom(e);
  acts.appendChild(mk);
  // Keep in space (LR-1): kept state comes from the API, never guessed.
  // "QuietRank should have caught this" — the recall side of feedback.
  // Without it the layer could only ever learn to go quiet.
  if (!own && typeof qrNotice === 'function') {
    const nt = document.createElement('span');
    nt.className = 'notice-act';
    nt.textContent = '◎ should have noticed';
    nt.title = 'teach QuietRank that messages like this matter to you';
    nt.onclick = async () => {
      await qrNotice(e.id);
      nt.textContent = '◎ noted';
    };
    acts.appendChild(nt);
  }
  const kp = document.createElement('span');
  kp.className = 'keep-act' + (e.kept ? ' kept' : '');
  kp.textContent = e.kept ? `✦ kept${e.keep_count > 1 ? ' · ' + e.keep_count : ''}`
    : (e.keep_count > 0 ? `✦ keep · ${e.keep_count}` : '✦ keep');
  kp.title = e.kept ? 'un-keep (remove from the Shelf)' : 'keep in space (Shelf)';
  kp.onclick = () => keepToggle(e);
  acts.appendChild(kp);
  d.appendChild(acts);
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
    n.appendChild(makeWaterfall());
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

// ---- media on-demand: fetch an asset's bytes when it scrolls into view ----
// A joiner holds the manifest but not the bytes; nothing renders a cover or an
// article image until they arrive. So when such an element enters the viewport
// we kick the same relay fetch the "fetch original" button uses, then re-point
// the element once the bytes land. Idempotent — safe to observe every media el.
const _mediaObserver = ('IntersectionObserver' in window)
  ? new IntersectionObserver((entries) => {
      for (const ent of entries) {
        if (!ent.isIntersecting) continue;
        _mediaObserver.unobserve(ent.target);
        const m = ent.target._autoMedia;
        if (m) autoFetchAsset(m.assetId, m.onReady);
      }
    }, { rootMargin: '300px' })
  : null;

function observeMedia(el, assetId, onReady) {
  if (!assetId) return;
  el._autoMedia = { assetId, onReady };
  if (_mediaObserver) _mediaObserver.observe(el);
  else autoFetchAsset(assetId, onReady); // no observer support: fetch eagerly
}

// autoFetchAsset drives the relay fetch to completion, polling the idempotent
// /fetch endpoint (RequestAsset dedups in-flight). Bails if the user navigates.
async function autoFetchAsset(assetId, onReady) {
  const sp = current;
  try {
    for (let i = 0; i < 90; i++) {
      const r = await api(`/api/spaces/${sp}/assets/${assetId}/fetch`, { method: 'POST' });
      if (r.state === 'complete') { onReady && onReady(); return; }
      if (r.state === 'failed') return;
      await new Promise((res) => setTimeout(res, 1200));
      if (current !== sp) return; // moved to another space; stop
    }
  } catch (e) { /* asset unknown here, or transient — leave the placeholder */ }
}

// autoMediaSrc / autoMediaBg point an <img>/<audio> or a background element at
// an asset, optimistically now (works if already local) and again with a
// cache-buster once an on-view fetch completes (forces the browser to reload
// the previously-missing bytes).
function assetURL(assetId) {
  return `/api/spaces/${current}/assets/${assetId}?token=${token}`;
}
function autoMediaSrc(el, assetId) {
  const base = assetURL(assetId);
  el.src = base;
  observeMedia(el, assetId, () => { el.src = base + '&v=' + Date.now(); });
}
function autoMediaBg(el, assetId) {
  const base = assetURL(assetId);
  el.style.backgroundImage = `url("${base}")`;
  observeMedia(el, assetId, () => { el.style.backgroundImage = `url("${base}&v=${Date.now()}")`; });
}

// makeWaterfall: a tiny ASCII "waterfall" with a flowing gradient, to sit next
// to a fetch-progress readout. Pure CSS animation (no timer), so it is safe to
// recreate on every re-render without leaking intervals.
function makeWaterfall() {
  const el = document.createElement('span');
  el.className = 'wfall';
  el.setAttribute('aria-hidden', 'true');
  el.textContent = '╽┃╿║┃╽║┃╿';
  return el;
}

// The message feed dispatches through the same registry seam as the
// composition contract (§34): a kind→renderer map with a semantic fallback to
// the honest "unsupported" node. Behaviour is unchanged from the old switch;
// the indirection is the point.
const FEED_RENDERERS = {
  // Mentions come resolved from the node (signed field); mentionText only
  // decorates the names, it never re-parses the message for addressing.
  text: (e) => (typeof mentionText === 'function' ? mentionText(e) : textNode('txt', e.text)),
  visual: (e) => renderVisual(e),
  video: (e) => renderVideo(e),
  voice: (e) => renderVoiceAudio(e),
  audio: (e) => renderVoiceAudio(e),
  file: (e) => renderFile(e),
  link: (e) => renderLink(e),
  live_signal: (e) => renderSignal(e),
};

// Telegram-style video card: poster + ▶ overlay; the player mounts on tap
// once the original is local (Range-served, so seeking works).
function renderVideo(e) {
  const wrap = document.createElement('div');
  if (e.caption) wrap.appendChild(textNode('txt', e.caption));
  const src = e.asset ? `/api/spaces/${current}/assets/${e.asset.id}?token=${token}` : null;
  const complete = e.asset?.state === 'complete';
  if (e.thumb_b64) {
    const holder = document.createElement('div');
    holder.className = 'video-holder';
    const img = document.createElement('img');
    img.className = 'thumb'; img.alt = e.alt;
    img.src = `data:${e.thumb_mime};base64,${e.thumb_b64}`;
    holder.appendChild(img);
    const play = document.createElement('div');
    play.className = 'video-play';
    play.textContent = '▶';
    holder.appendChild(play);
    if (e.duration_ms) {
      const d = document.createElement('span');
      d.className = 'video-dur';
      d.textContent = fmtDur(e.duration_ms);
      holder.appendChild(d);
    }
    holder.onclick = () => {
      if (!complete) {
        // Tapped play before the bytes are local: kick the relay fetch (media
        // on-demand) and show it's working — the card mounts the player once
        // the original lands.
        if (e.asset && e.asset.id) autoFetchAsset(e.asset.id, () => refreshSpace());
        play.textContent = '⏳';
        return;
      }
      const v = document.createElement('video');
      v.controls = true; v.autoplay = true; v.playsInline = true;
      v.src = src;
      v.poster = img.src;
      v.className = 'video-player';
      holder.replaceWith(v);
    };
    wrap.appendChild(holder);
  } else if (complete) {
    const v = document.createElement('video');
    v.controls = true; v.playsInline = true; v.src = src;
    v.className = 'video-player';
    wrap.appendChild(v);
  } else {
    wrap.appendChild(textNode('txt', '🎬 ' + e.alt));
  }
  wrap.appendChild(assetNote(e));
  return wrap;
}

function renderBody(e) {
  const r = FEED_RENDERERS[e.kind];
  if (r) return r(e);
  const wrap = document.createElement('div');
  wrap.appendChild(textNode('unknownblk', e.fallback || ('unsupported block: ' + e.schema)));
  return wrap;
}

function renderVisual(e) {
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

function renderVoiceAudio(e) {
  const wrap = document.createElement('div');
  const title = e.kind === 'audio'
    ? (e.title || 'audio') + (e.bpm ? ` · ${e.bpm} BPM` : '')
    : `voice · ${fmtDur(e.duration_ms)}`;
  const head = textNode('txt', (e.kind === 'audio' ? '♫ ' : '🎙 ') + title);
  head.classList.add('aplayer-title');
  wrap.appendChild(head);
  if (e.transcript) wrap.appendChild(textNode('meta', '“' + e.transcript + '”'));
  if (e.asset?.state === 'complete') {
    wrap.appendChild(makeAudioPlayer(
      `/api/spaces/${current}/assets/${e.asset.id}?token=${token}`,
      e.waveform_b64, e.duration_ms));
  } else {
    if (e.waveform_b64) wrap.appendChild(renderWave(e.waveform_b64)); // static preview
    wrap.appendChild(assetNote(e));
  }
  return wrap;
}

// One audio plays at a time across the whole feed — starting a new player
// pauses whichever was going.
let nowPlayingAudio = null;

// SVG glyphs, currentColor so the accent theme flows through.
const PLAY_ICON = '<svg viewBox="0 0 24 24" width="15" height="15" aria-hidden="true"><path d="M8 5v14l11-7z" fill="currentColor"/></svg>';
const PAUSE_ICON = '<svg viewBox="0 0 24 24" width="15" height="15" aria-hidden="true"><path d="M7 5h3.5v14H7zM13.5 5H17v14h-3.5z" fill="currentColor"/></svg>';

// waveBytes normalizes a waveform into an array of 0-255 amplitudes. Accepts
// a base64 string (feed entries), an array (live recording), or null.
function waveBytes(wave) {
  if (!wave) return null;
  if (Array.isArray(wave)) return wave.length ? wave : null;
  const s = atob(wave);
  const out = new Array(s.length);
  for (let i = 0; i < s.length; i++) out[i] = s.charCodeAt(i);
  return out.length ? out : null;
}

// makeAudioPlayer builds the house audio player: a play toggle, the waveform
// itself as the scrubber (bars light up to the playhead, click/drag to seek;
// a plain fill line when no waveform exists), and a mono time readout — all
// in the Quiet Terminal Glass palette.
function makeAudioPlayer(src, wave, durationMs) {
  const audio = new Audio();
  audio.preload = 'metadata';
  audio.src = src;

  const player = document.createElement('div');
  player.className = 'aplayer';

  const btn = document.createElement('button');
  btn.type = 'button';
  btn.className = 'aplay';
  btn.setAttribute('aria-label', 'play');
  btn.innerHTML = PLAY_ICON;
  player.appendChild(btn);

  // Scrubber: waveform bars if we have them, else a slim fill line.
  const scrub = document.createElement('div');
  scrub.className = 'ascrub';
  scrub.setAttribute('role', 'slider');
  scrub.setAttribute('aria-label', 'seek');
  scrub.tabIndex = 0;
  let bars = null, fill = null;
  const amps = waveBytes(wave);
  if (amps) {
    scrub.classList.add('wavebars');
    bars = [];
    for (let i = 0; i < amps.length; i++) {
      const bar = document.createElement('i');
      bar.style.height = Math.max(2, (amps[i] / 255) * 30) + 'px';
      scrub.appendChild(bar);
      bars.push(bar);
    }
  } else {
    scrub.classList.add('barline');
    fill = document.createElement('span');
    fill.className = 'afill';
    scrub.appendChild(fill);
  }
  player.appendChild(scrub);

  const time = document.createElement('span');
  time.className = 'atime';
  const total = () => audio.duration && isFinite(audio.duration)
    ? audio.duration * 1000 : (durationMs || 0);
  time.textContent = fmtDur(total());
  player.appendChild(time);

  function paint() {
    const dur = total() / 1000;
    const frac = dur > 0 ? Math.min(1, audio.currentTime / dur) : 0;
    if (bars) {
      const upto = Math.round(frac * bars.length);
      for (let i = 0; i < bars.length; i++) {
        bars[i].classList.toggle('on', i < upto);
        // The leading bar is the playhead — it glows and pulses while live.
        bars[i].classList.toggle('head', i === upto - 1);
      }
    } else if (fill) {
      fill.style.width = (frac * 100) + '%';
    }
    const cur = audio.currentTime ? audio.currentTime * 1000 : 0;
    time.textContent = (audio.currentTime ? fmtDur(cur) + ' / ' : '') + fmtDur(total());
  }

  // The arbiter replaces the two-line pairwise pause that used to live here:
  // that version was never cleared and knew nothing about the listening room,
  // the drone, or a bare <audio controls> in a publication.
  const owner = 'player:' + Math.random().toString(36).slice(2);
  btn.onclick = () => {
    if (audio.paused) {
      if (!AUDIO.request(owner, 'player', () => audio.pause())) return;
      if (nowPlayingAudio && nowPlayingAudio !== audio) nowPlayingAudio.pause();
      nowPlayingAudio = audio;
      audio.play().catch(() => AUDIO.release(owner));
    } else {
      audio.pause();
    }
  };
  audio.addEventListener('pause', () => AUDIO.release(owner));
  audio.addEventListener('ended', () => AUDIO.release(owner));
  audio.onplay = () => { btn.innerHTML = PAUSE_ICON; btn.setAttribute('aria-label', 'pause'); player.classList.add('playing'); };
  audio.onpause = () => { btn.innerHTML = PLAY_ICON; btn.setAttribute('aria-label', 'play'); player.classList.remove('playing'); };
  audio.onended = () => { btn.innerHTML = PLAY_ICON; player.classList.remove('playing'); paint(); };
  audio.ontimeupdate = paint;
  audio.onloadedmetadata = paint;

  function seekTo(clientX) {
    const r = scrub.getBoundingClientRect();
    const frac = Math.max(0, Math.min(1, (clientX - r.left) / r.width));
    const dur = total() / 1000;
    if (dur > 0) { audio.currentTime = frac * dur; paint(); }
  }
  let dragging = false;
  scrub.addEventListener('pointerdown', (ev) => {
    dragging = true; scrub.setPointerCapture(ev.pointerId); seekTo(ev.clientX);
  });
  scrub.addEventListener('pointermove', (ev) => { if (dragging) seekTo(ev.clientX); });
  scrub.addEventListener('pointerup', () => { dragging = false; });
  scrub.addEventListener('keydown', (ev) => {
    const dur = total() / 1000; if (!dur) return;
    if (ev.key === 'ArrowRight') { audio.currentTime = Math.min(dur, audio.currentTime + 5); paint(); }
    else if (ev.key === 'ArrowLeft') { audio.currentTime = Math.max(0, audio.currentTime - 5); paint(); }
    else if (ev.key === ' ' || ev.key === 'Enter') { ev.preventDefault(); btn.click(); }
  });

  return player;
}

function renderFile(e) {
  const wrap = document.createElement('div');
  const card = document.createElement('div');
  card.className = 'filecard';
  card.appendChild(textNode('txt', '📄 ' + e.filename));
  card.appendChild(assetNote(e));
  wrap.appendChild(card);
  return wrap;
}

function renderLink(e) {
  const card = document.createElement('div');
  card.className = 'linkcard';
  const a = document.createElement('a');
  a.href = e.url; a.target = '_blank'; a.rel = 'noopener noreferrer';
  a.textContent = e.title || e.url;
  card.appendChild(a);
  if (e.description) card.appendChild(textNode('meta', e.description));
  return card;
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

// ---- Telegram-style attach: 📎 opens Photo / Video / Document ----
let attachMode = 'doc';
let pendingVideoMeta = null; // { duration_ms, width, height } for video sends

function toggleAttachMenu(e) {
  e.stopPropagation();
  const m = document.getElementById('attachMenu');
  m.style.display = m.style.display === 'none' ? '' : 'none';
}
document.addEventListener('click', () => {
  const m = document.getElementById('attachMenu');
  if (m) m.style.display = 'none';
});

function attachPick(mode) {
  attachMode = mode;
  fileInput.accept = mode === 'photo' ? 'image/*' : mode === 'video' ? 'video/*'
    : mode === 'audio' ? 'audio/*' : '';
  fileInput.click();
}

// Drag a file anywhere over the window to attach it: routed to the right kind
// by MIME (image → photo, video → video, audio → audio, else document), then
// the normal attach dialog opens for alt/caption + send.
function dragHasFiles(e) {
  return e.dataTransfer && Array.from(e.dataTransfer.types || []).includes('Files');
}
function initDropZone() {
  if (window._dropInit) return;
  window._dropInit = true;
  const ov = document.createElement('div');
  ov.id = 'dropOverlay';
  ov.innerHTML = '<div class="dh">⬇ Drop to send into this space</div>';
  document.body.appendChild(ov);
  let depth = 0;
  const show = (on) => ov.classList.toggle('on', on);
  window.addEventListener('dragenter', (e) => {
    if (!dragHasFiles(e) || !current) return;
    e.preventDefault(); depth++; show(true);
  });
  window.addEventListener('dragover', (e) => {
    if (!dragHasFiles(e) || !current) return;
    e.preventDefault(); e.dataTransfer.dropEffect = 'copy';
  });
  window.addEventListener('dragleave', (e) => {
    if (!dragHasFiles(e)) return;
    depth = Math.max(0, depth - 1); if (!depth) show(false);
  });
  window.addEventListener('drop', (e) => {
    if (!dragHasFiles(e)) return;
    e.preventDefault(); depth = 0; show(false);
    const file = e.dataTransfer.files && e.dataTransfer.files[0];
    if (!file) return;
    if (!current) { alert('open a space first'); return; }
    attachMode = file.type.startsWith('image/') ? 'photo'
      : file.type.startsWith('video/') ? 'video'
      : file.type.startsWith('audio/') ? 'audio' : 'doc';
    onFilePicked(file);
  });
}

// captureVideoPoster grabs a frame + duration/dimensions from a local file.
function captureVideoPoster(file) {
  return new Promise((resolve) => {
    const url = URL.createObjectURL(file);
    const v = document.createElement('video');
    v.muted = true; v.playsInline = true; v.preload = 'metadata';
    const fail = () => { URL.revokeObjectURL(url); resolve(null); };
    v.onerror = fail;
    v.onloadedmetadata = () => { v.currentTime = Math.min(0.5, (v.duration || 1) / 2); };
    v.onseeked = async () => {
      try {
        const cv = document.createElement('canvas');
        const scale = Math.min(1, 480 / (v.videoWidth || 480));
        cv.width = Math.max(1, Math.round(v.videoWidth * scale));
        cv.height = Math.max(1, Math.round(v.videoHeight * scale));
        cv.getContext('2d').drawImage(v, 0, 0, cv.width, cv.height);
        const { blob, dataURL } = await encodeThumbUnder(cv, 36 << 10);
        URL.revokeObjectURL(url);
        resolve({
          blob, dataURL,
          duration_ms: Math.round((v.duration || 0) * 1000),
          width: v.videoWidth, height: v.videoHeight,
        });
      } catch (e) { fail(); }
    };
    v.src = url;
  });
}

async function onFilePicked(file) {
  if (!file) return;
  pendingFile = file;
  pendingPreview = null;
  pendingVideoMeta = null;
  const isImage = attachMode === 'photo' || (attachMode === 'doc' && file.type.startsWith('image/'));
  const isVideo = attachMode === 'video' || (attachMode === 'doc' && file.type.startsWith('video/'));
  const isAudio = attachMode === 'audio' || (attachMode === 'doc' && file.type.startsWith('audio/'));
  pendingKind = isImage ? 'visual' : isVideo ? 'video' : isAudio ? 'audio' : 'file';
  document.getElementById('attachTitle').textContent =
    isImage ? 'Send a photo' : isVideo ? 'Send a video'
      : isAudio ? 'Send audio' : 'Send a document';
  // alt is required only for the visual kinds (it is how they reach text
  // terminals); caption is offered for any media.
  document.getElementById('attachAlt').style.display = (isImage || isVideo) ? '' : 'none';
  document.getElementById('attachCaption').style.display = (isImage || isVideo || isAudio) ? '' : 'none';
  const prev = document.getElementById('attachPreview');
  prev.innerHTML = '';
  document.getElementById('attachWarn').textContent = '';
  // The shared caption field doubles as the TITLE for audio (which has no
  // caption); pre-fill it with the filename stem and relabel it.
  const capField = document.getElementById('attachCaption');
  if (isAudio) {
    capField.value = file.name.replace(/\.[^/.]+$/, '');
    capField.placeholder = 'title';
  } else {
    capField.value = '';
    capField.placeholder = 'caption (optional)';
  }
  if (isAudio) {
    pendingVideoMeta = { duration_ms: (typeof audioDurationMs === 'function' ? await audioDurationMs(file) : 0) };
    prev.appendChild(makeAudioPlayer(URL.createObjectURL(file), null, pendingVideoMeta.duration_ms));
    const meta = document.createElement('div');
    meta.className = 'hint';
    meta.textContent = `${file.name} · ${fmtBytes(file.size)}` +
      (pendingVideoMeta.duration_ms ? ` · ${fmtDur(pendingVideoMeta.duration_ms)}` : '');
    prev.appendChild(meta);
  } else if (isImage) {
    const { blob, dataURL } = await makeThumb(file);
    pendingPreview = blob;
    const img = document.createElement('img'); img.src = dataURL;
    img.style.maxWidth = '240px'; img.style.borderRadius = '8px';
    prev.appendChild(img);
    document.getElementById('attachWarn').textContent =
      'note: the original may carry EXIF/GPS metadata; the thumbnail is re-encoded and stripped.';
  } else if (isVideo) {
    const cap = await captureVideoPoster(file);
    if (cap) {
      pendingPreview = cap.blob;
      pendingVideoMeta = { duration_ms: cap.duration_ms, width: cap.width, height: cap.height };
      const img = document.createElement('img'); img.src = cap.dataURL;
      img.style.maxWidth = '240px'; img.style.borderRadius = '8px';
      prev.appendChild(img);
      const meta = document.createElement('div');
      meta.className = 'hint';
      meta.textContent = `${file.name} · ${fmtBytes(file.size)} · ${fmtDur(cap.duration_ms)}`;
      prev.appendChild(meta);
    } else {
      prev.textContent = `${file.name} · ${fmtBytes(file.size)} (no poster — codec not decodable here)`;
    }
  } else {
    prev.textContent = `${file.name} · ${fmtBytes(file.size)}`;
  }
  dlgAttach.showModal();
  fileInput.value = '';
}

// Downscale + re-encode through a canvas: strips metadata, bounds size.
function blobToDataURL(blob) {
  return new Promise((res) => { const fr = new FileReader(); fr.onload = () => res(fr.result); fr.readAsDataURL(blob); });
}

// encodeThumbUnder re-encodes a canvas to webp under maxBytes: it lowers
// quality, then downscales, until it fits. The inline preview is a hard cap
// (schemas.MaxInlinePreview = 40 KiB) that a detailed frame at fixed quality
// can blow past ("preview too large") — this guarantees it never does.
async function encodeThumbUnder(canvas, maxBytes) {
  const toBlob = (c, q) => new Promise((r) => c.toBlob(r, 'image/webp', q));
  let c = canvas;
  for (let round = 0; round < 4; round++) {
    for (const q of [0.8, 0.6, 0.45, 0.3, 0.2]) {
      const blob = await toBlob(c, q);
      if (blob && blob.size <= maxBytes) return { blob, dataURL: await blobToDataURL(blob) };
    }
    const nc = document.createElement('canvas');
    nc.width = Math.max(1, Math.round(c.width * 0.7));
    nc.height = Math.max(1, Math.round(c.height * 0.7));
    nc.getContext('2d').drawImage(c, 0, 0, nc.width, nc.height);
    c = nc;
  }
  const blob = await toBlob(c, 0.2);
  return { blob, dataURL: await blobToDataURL(blob) };
}

function makeThumb(file) {
  return new Promise((resolve) => {
    const img = new Image();
    img.onload = async () => {
      const max = 480;
      let { width: w, height: h } = img;
      if (w > max || h > max) { const s = max / Math.max(w, h); w = Math.round(w*s); h = Math.round(h*s); }
      const c = document.createElement('canvas'); c.width = w; c.height = h;
      c.getContext('2d').drawImage(img, 0, 0, w, h);
      resolve(await encodeThumbUnder(c, 36 << 10)); // stay under the 40 KiB cap
    };
    img.src = URL.createObjectURL(file);
  });
}

async function sendAttachment() {
  const meta = {
    kind: pendingKind, size: pendingFile.size, media_type: pendingFile.type || 'application/octet-stream',
  };
  if (pendingKind === 'visual' || pendingKind === 'video') {
    const alt = document.getElementById('attachAlt').value.trim();
    if (!alt) { alert('alt text is required — it is how the media reaches text terminals'); return; }
    meta.alt = alt;
    meta.caption = document.getElementById('attachCaption').value.trim();
    if (pendingPreview) meta.preview_mime = 'image/webp';
    if (pendingKind === 'video' && pendingVideoMeta) {
      meta.duration_ms = pendingVideoMeta.duration_ms;
      meta.width = pendingVideoMeta.width;
      meta.height = pendingVideoMeta.height;
    }
  } else if (pendingKind === 'audio') {
    // Audio blocks carry a TITLE (there is no caption field); default it to
    // the filename stem so it is never empty.
    const stem = pendingFile.name.replace(/\.[^/.]+$/, '');
    meta.title = document.getElementById('attachCaption').value.trim() || stem;
    meta.filename = pendingFile.name;
    if (pendingVideoMeta && pendingVideoMeta.duration_ms) meta.duration_ms = pendingVideoMeta.duration_ms;
  } else {
    meta.filename = pendingFile.name;
  }
  await postBlock(meta, pendingPreview, pendingFile);
  dlgAttach.close();
  refreshSpace();
}

// postMultipart frames the multipart body BY HAND and sends raw bytes.
//
// Not an optimisation — a survival requirement inside the desktop shell:
// WKWebView's custom-scheme handler drops any request body that is BACKED BY
// A BLOB (measured in cmd/wails-probe: a five-byte Blob inside FormData
// arrives empty, a 512KiB FormData of strings arrives fine). Every upload
// here is FormData with a File, and a File IS a Blob — so in the shell every
// upload silently sent nothing. Raw Uint8Array bodies survive, and a
// hand-framed multipart is a perfectly ordinary multipart to the server, so
// this ONE code path serves browser and shell alike; no capability forks.
//
// The cost is honesty about memory: each part is read fully before sending,
// bounded by the asset cap (64 MiB) that the server enforces anyway.
//
// parts: [{ name, data: Blob|string, filename?, type? }]
async function postMultipart(url, parts) {
  const enc = new TextEncoder();
  const boundary = '----qs' + Array.from(crypto.getRandomValues(new Uint8Array(12)),
    b => b.toString(16).padStart(2, '0')).join('');
  const chunks = [];
  for (const p of parts) {
    const type = p.type || (p.data && p.data.type) || 'application/octet-stream';
    const disp = p.filename
      ? `form-data; name="${p.name}"; filename="${p.filename.replace(/"/g, '')}"`
      : `form-data; name="${p.name}"`;
    chunks.push(enc.encode(
      `--${boundary}\r\nContent-Disposition: ${disp}\r\nContent-Type: ${type}\r\n\r\n`));
    chunks.push(typeof p.data === 'string'
      ? enc.encode(p.data)
      : new Uint8Array(await p.data.arrayBuffer()));
    chunks.push(enc.encode('\r\n'));
  }
  chunks.push(enc.encode(`--${boundary}--\r\n`));
  const body = new Uint8Array(chunks.reduce((n, c) => n + c.length, 0));
  let off = 0;
  for (const c of chunks) { body.set(c, off); off += c.length; }
  return fetch(url, {
    method: 'POST',
    headers: {
      'X-QP-Token': token,
      'Content-Type': 'multipart/form-data; boundary=' + boundary,
    },
    body,
  });
}

async function postBlock(meta, preview, file) {
  const parts = [
    { name: 'metadata', data: JSON.stringify(meta), type: 'application/json' },
  ];
  if (preview) parts.push({ name: 'preview', data: preview, filename: 'preview.webp' });
  if (file) parts.push({ name: 'file', data: file, filename: meta.filename || 'file' });
  const resp = await postMultipart(`/api/spaces/${current}/blocks`, parts);
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
    // Review the take with the house player — the recorded waveform is its
    // scrubber, same as it will look in the feed.
    const host = document.getElementById('voicePlayerHost');
    host.innerHTML = '';
    host.appendChild(makeAudioPlayer(URL.createObjectURL(voiceBlob),
      compactWave(), Date.now() - voiceStart));
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

// Deep link: a pass link arriving as #p=<token> opens the join flow, then the
// fragment is scrubbed from history (it may hold the bearer secret).
function checkPassDeepLink() {
  const m = location.hash.match(/[#&]p=([^&]+)/);
  if (!m) return;
  history.replaceState(null, '', location.pathname + location.search);
  openJoinDialog();
  document.getElementById('joinPass').value = decodeURIComponent(m[1]);
}

checkOnboarding();
checkPassDeepLink();
refresh();
setInterval(refresh, 2000);
// QuietRank rides a slower tick of its own: attention is not a live feed,
// and scanning every two seconds would burn CPU for nothing.
if (typeof qrRefreshBadge === 'function') {
  qrRefreshBadge();
  setInterval(qrRefreshBadge, 8000);
}
initDropZone();
