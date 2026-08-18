// Resonance client (RP-1/RP-2). Meaning comes from the node (palette
// endpoint carries the Go core registry — nothing is mirrored by hand);
// this module renders compact aggregates, the picker, and talks to the
// reactions endpoint. Quick tap = the palette's default meaning; tapping
// your own chip releases it; long-press/hover opens the semantic palette
// with "More…" (unicode) when the space allows it.

const RES = {
  space: null,
  palette: null,   // {palette_id, default_key, slots[], policy{}, registry[]}
  paletteRev: 0,   // bumped on every palette change → forces feed rebuild

  async load(sid) {
    if (!sid || (this.space === sid && this.palette)) return;
    try {
      const p = await api(`/api/spaces/${sid}/resonance/palette`);
      const changed = JSON.stringify(p) !== JSON.stringify(this.palette);
      this.space = sid;
      this.palette = p;
      if (changed) this.paletteRev++;
    } catch (e) { /* keep the previous palette; generic fallback covers */ }
  },

  // refresh re-fetches even for the same space (owner edited the palette).
  async refresh(sid) { this.space = null; await this.load(sid); },

  slots() { return this.palette?.slots || []; },
  defaultKey() { return this.palette?.default_key || 'resonates'; },
  allowUnicode() { return this.palette?.policy?.allow_unicode !== false; },
  showCounts() { return this.palette?.policy?.show_counts !== false; },
  surfaceEffects() { return this.palette?.policy?.surface_effects !== false; },
};

// resIdentity is the canonical group identity (matches the reducer).
function resIdentity(g) {
  return g.kind === 'semantic' ? 's:' + (g.key || '') : 'u:' + (g.value || '');
}

// resSig: canonical signature of one entry's resonance for the feed diff.
// Groups arrive in canonical order from the node; count and revision are
// INSIDE the signature (identity order only drives sorting stability).
function resSig(res) {
  if (!res) return '';
  const gs = (res.groups || []).map(g => resIdentity(g) + '#' + g.count).join(',');
  return gs + '|o:' + (res.own ? resIdentity(res.own) : '') + '|r:' + (res.revision || '') +
    '|p:' + RES.paletteRev;
}

// resCounts: one entry's aggregate as a plain {identity → count} map —
// the feed keeps the previous tick's map per entry so arrivals can be
// computed as honest per-group deltas instead of guessing from the
// dominant group.
function resCounts(res) {
  const out = {};
  for (const g of (res?.groups || [])) out[resIdentity(g)] = g.count || 0;
  return out;
}

// computeResDeltas: which groups GREW since the previous snapshot, with
// magnitudes. prev === null/undefined means the row was never seen before
// — that is history arriving, not a reaction happening, so no deltas.
// A count going DOWN (clear) corrects the residue but is never an
// arrival: removing a reaction does not animate in reverse.
function computeResDeltas(prev, res) {
  if (!prev) return [];
  const deltas = [];
  for (const g of (res?.groups || [])) {
    const d = (g.count || 0) - (prev[resIdentity(g)] || 0);
    if (d > 0) deltas.push({ group: g, delta: d });
  }
  return deltas;
}

function resGlyph(g) {
  // The node already resolved the authoritative fallback; last-resort
  // generic marker if even that is missing.
  return g.fallback || g.value || '◈';
}

async function sendResonance(targetId, op, reaction, onDone) {
  try {
    await api(`/api/spaces/${current}/reactions`, {
      method: 'POST',
      body: JSON.stringify({ target: targetId, op, reaction }),
    });
    if (onDone) onDone(); else refreshSpace();
  } catch (e) { console.warn('resonance:', e.message); }
}

function setSemantic(targetId, key, onDone) {
  sendResonance(targetId, 'set', { kind: 'semantic', key }, onDone);
}
function setUnicode(targetId, value, onDone) {
  sendResonance(targetId, 'set', { kind: 'unicode', value }, onDone);
}
function clearResonance(targetId, onDone) {
  sendResonance(targetId, 'clear', null, onDone);
}

// renderResonanceRow builds the compact aggregate + the ✧ affordance.
// res may be null (no reactions yet). onChange refreshes the surrounding
// view (defaults to the feed poll).
function renderResonanceRow(res, targetId, onChange) {
  const row = document.createElement('div');
  row.className = 'res-row';
  row.dataset.target = targetId;
  const ownId = res?.own ? resIdentity(res.own) : null;

  for (const g of (res?.groups || [])) {
    const chip = document.createElement('span');
    const mine = ownId === resIdentity(g);
    chip.className = 'res-chip' + (mine ? ' res-own' : '');
    // The meaning key rides on the chip so CSS can give each branded
    // reaction its own colour voice. Unicode reactions carry none and
    // keep the neutral fallback.
    if (g.kind === 'semantic' && g.key) chip.dataset.key = g.key;
    const sym = document.createElement('span');
    sym.className = 'res-sym';
    sym.textContent = resGlyph(g);
    chip.appendChild(sym);
    if (RES.showCounts() && g.count > 1) {
      const n = document.createElement('span');
      n.className = 'res-n';
      n.textContent = g.count;
      chip.appendChild(n);
    }
    chip.title = resChipTitle(g);
    chip.onclick = () => {
      if (mine) { clearResonance(targetId, onChange); return; }
      if (g.kind === 'semantic') setSemantic(targetId, g.key, onChange);
      else setUnicode(targetId, g.value, onChange);
    };
    // Expand: actors on demand (bounded preview travels with the feed).
    chip.oncontextmenu = (ev) => { ev.preventDefault(); openResActors(targetId, chip); };
    row.appendChild(chip);
  }

  // In a space this device cannot write to, there is no affordance at all.
  // A resonance is an emitted event like any other, so the node refuses it —
  // and offering the gesture anyway meant a reader tapped, nothing happened,
  // and nothing said why. Existing resonances still show: they are what
  // other people said, and they are worth seeing whether or not you can add
  // to them.
  const mayResonate =
    !(typeof currentSpace === 'function' && currentSpace()?.can_write === false);

  // ✧ ONE GESTURE, ONE EXPECTATION: a tap opens the palette, on every
  // device. It used to be "tap = the default meaning, long-press = the
  // palette", which read fine with a mouse and badly under a thumb — on a
  // phone the tap won the race against the long-press timer and stamped the
  // first slot before anybody saw a choice. Hover still opens it early on
  // a desktop, as a shortcut rather than a second model.
  const add = document.createElement('span');
  add.className = 'res-add';
  add.textContent = '✧';
  add.title = t('res.resonate');
  let hoverT = null;
  add.onclick = (ev) => {
    ev.stopPropagation();
    clearTimeout(hoverT);
    if (resPickerEl && resPickerEl.dataset.target === targetId) { closeResPicker(); return; }
    openResPicker(targetId, add, res, onChange);
  };
  add.onmouseenter = () => {
    hoverT = setTimeout(() => openResPicker(targetId, add, res, onChange), 350);
  };
  add.onmouseleave = () => clearTimeout(hoverT);
  if (mayResonate) row.appendChild(add);
  // No residue on the host — the owner's call: the chips with their counts
  // ARE the accumulated state, and a touched message hangs in space exactly
  // like an untouched one. Arrival flashes still play from the feed's delta
  // paths (effects.js), but the steady state paints nothing on the surface.
  return row;
}

// THE PROTOCOL OWNS THE KEYS; THIS OWNS THE WORDS FOR THEM.
//
// CoreRegistry is the one source of truth and is deliberately never
// mirrored in JS — so this does not mirror it. It translates a key the
// node already sent, and only for the core vocabulary every client can be
// sure about. A space's own palette keeps its author's label untouched:
// translating somebody else's words would be putting words in their mouth.
function resLabel(slotOrGroup) {
  const key = slotOrGroup.key;
  if (key) {
    const k = t('res.k.' + key);
    if (k && k !== 'res.k.' + key) return k;
  }
  return slotOrGroup.label || key || slotOrGroup.value || 'reaction';
}

// resUsage is how the word is USED, not what it means. A beta tester read
// the palette as "not quite clear, and unfamiliar" — a definition would
// have answered neither, and an example answers both.
function resUsage(key) {
  if (!key) return '';
  const u = t('res.u.' + key);
  return u && u !== 'res.u.' + key ? u : '';
}

function resChipTitle(g) {
  const label = resLabel(g);
  return g.count === 1 ? `${label} · one of us` : `${label} · ${g.count} of us`;
}

// ---- picker popover ----

let resPickerEl = null;

function closeResPicker() {
  if (resPickerEl) { resPickerEl.remove(); resPickerEl = null; }
  document.removeEventListener('click', onResPickerOutside, true);
}
function onResPickerOutside(e) {
  if (resPickerEl && !resPickerEl.contains(e.target)) closeResPicker();
}

function openResPicker(targetId, anchor, res, onChange) {
  closeResPicker();
  const pop = document.createElement('div');
  pop.className = 'res-picker';
  pop.dataset.target = targetId;
  const ownId = res?.own ? resIdentity(res.own) : null;
  // A COARSE POINTER HAS NO HOVER, so it gets a two-beat choice instead:
  // the first tap on a slot plays its effect and selects it, the second
  // tap (or Send) commits. A fine pointer keeps hover-to-preview and
  // click-to-commit — same palette, same words, one fewer beat.
  const coarse = typeof matchMedia === 'function' && matchMedia('(pointer: coarse)').matches;
  let picked = null; // {kind:'semantic', key} | {kind:'unicode', value}
  let sendBtn = null;
  const preview = (el, spec) => {
    if (typeof RESFX?.fireArrival !== 'function') return;
    RESFX.fireArrival(el, spec, 1);
  };
  const commit = () => {
    if (!picked) return;
    closeResPicker();
    if (picked.kind === 'semantic') setSemantic(targetId, picked.key, onChange);
    else setUnicode(targetId, picked.value, onChange);
  };
  const select = (btn, spec) => {
    for (const b of pop.querySelectorAll('.res-picked')) b.classList.remove('res-picked');
    btn.classList.add('res-picked');
    picked = spec;
    if (sendBtn) sendBtn.hidden = false;
  };

  const slotRow = document.createElement('div');
  slotRow.className = 'res-picker-slots';
  for (const s of RES.slots()) {
    const b = document.createElement('button');
    b.className = 'res-slot' + (ownId === 's:' + s.key ? ' res-own' : '');
    b.dataset.key = s.key;
    b.type = 'button';
    const sym = document.createElement('span');
    sym.className = 'res-sym'; sym.textContent = s.fallback;
    const lbl = document.createElement('span');
    lbl.className = 'res-lbl'; lbl.textContent = resLabel(s);
    b.append(sym, lbl);
    const usage = resUsage(s.key);
    if (usage) {
      const use = document.createElement('span');
      use.className = 'res-use'; use.textContent = usage;
      b.appendChild(use);
    }
    const spec = { kind: 'semantic', key: s.key, fallback: s.fallback };
    // SHOW WHAT IT DOES, NOT ONLY WHAT IT IS CALLED. Every meaning already
    // owns a surface effect — a heartbeat, a golden wash, rising sparks,
    // a settling weight — and a person should meet it BEFORE choosing.
    // RESFX is reused as-is and refuses on its own when effects are off.
    b.onmouseenter = () => { if (!coarse) preview(b, spec); };
    b.onclick = (ev) => {
      ev.stopPropagation();
      if (!coarse) { closeResPicker(); setSemantic(targetId, s.key, onChange); return; }
      if (picked && picked.kind === 'semantic' && picked.key === s.key) { commit(); return; }
      preview(b, spec);
      select(b, spec);
    };
    slotRow.appendChild(b);
  }
  pop.appendChild(slotRow);

  const actions = document.createElement('div');
  actions.className = 'res-picker-actions';
  const act = (label, cls, onclick) => {
    const b = document.createElement('button');
    b.className = 'res-act ' + cls; b.type = 'button';
    b.textContent = label;
    b.onclick = (ev) => { ev.stopPropagation(); onclick(); };
    actions.appendChild(b);
    return b;
  };
  if (RES.allowUnicode()) {
    act(t('res.more'), 'res-act-more', () => renderEmojiGrid(pop, targetId, onChange, coarse ? select : null));
  }
  if (res?.own) {
    act(t('res.remove'), 'res-act-remove', () => { closeResPicker(); clearResonance(targetId, onChange); });
  }
  if (coarse) {
    sendBtn = act(t('res.send'), 'res-act-send', commit);
    sendBtn.hidden = true;
  }
  if (actions.childElementCount) pop.appendChild(actions);

  document.body.appendChild(pop);
  const r = anchor.getBoundingClientRect();
  pop.style.left = Math.max(8, Math.min(window.innerWidth - pop.offsetWidth - 8, r.left)) + 'px';
  pop.style.top = Math.max(8, r.top - pop.offsetHeight - 6) + 'px';
  resPickerEl = pop;
  setTimeout(() => document.addEventListener('click', onResPickerOutside, true), 0);
}

// A small curated grid — the open Unicode language without a dependency.
const RES_EMOJI = [
  '❤️','🧡','💛','💚','💙','💜','🖤','🤍','✨','🔥','🌊','🌫️','⭐','🌙','☀️','🌈',
  '🌲','🌿','🍃','🌸','🌻','🍂','🪵','🍄','🫧','❄️','⚡','🌍',
  '😀','😂','🥲','😊','😉','😍','🤩','😎','🤔','😴','🥹','😭','😤','🤯','😱','🙃',
  '👍','👎','👏','🙌','🤝','✌️','🤘','👌','🫶','💪','🙏','👀',
  '🎵','🎶','🎧','🎸','🎹','🥁','🎤','📻','💿','🎬','📷','🎨',
  '🚀','💡','🧠','🧭','🗝️','📌','🏔️','🏕️','🌋','🎈','🎁','🏆',
];

function renderEmojiGrid(pop, targetId, onChange, select) {
  let grid = pop.querySelector('.res-emoji-grid');
  if (grid) { grid.remove(); return; }
  grid = document.createElement('div');
  grid.className = 'res-emoji-grid';
  const choose = (btn, em) => {
    // Under a thumb the emoji joins the two-beat choice; with a mouse it
    // commits at once, as it always did.
    if (select) { select(btn, { kind: 'unicode', value: em }); return; }
    closeResPicker(); setUnicode(targetId, em, onChange);
  };
  for (const em of RES_EMOJI) {
    if (!em) continue;
    const b = document.createElement('button');
    b.type = 'button'; b.textContent = em;
    b.onclick = (ev) => { ev.stopPropagation(); choose(b, em); };
    grid.appendChild(b);
  }
  const free = document.createElement('input');
  free.placeholder = t('res.anyEmoji'); free.maxLength = 8;
  free.onclick = (ev) => ev.stopPropagation();
  free.onkeydown = (ev) => {
    if (ev.key === 'Enter' && free.value.trim()) {
      const v = free.value.trim();
      closeResPicker();
      setUnicode(targetId, v, onChange);
    }
  };
  grid.appendChild(free);
  pop.appendChild(grid);
}

// ---- actors popover (full list on demand) ----

async function openResActors(targetId, anchor) {
  closeResPicker();
  let data;
  try {
    data = await api(`/api/spaces/${current}/resonance/${targetId}/actors`);
  } catch (e) { return; }
  const pop = document.createElement('div');
  pop.className = 'res-picker res-actors';
  for (const g of (data.groups || [])) {
    const line = document.createElement('div');
    line.className = 'res-actor-line';
    const head = document.createElement('span');
    head.className = 'res-sym';
    head.textContent = (g.fallback || g.value || '◈');
    line.appendChild(head);
    const who = document.createElement('span');
    who.className = 'res-actor-names';
    const names = (g.actors || []).map(a => a.mine ? 'you' : (a.name || 'member'));
    const label = resLabel(g);
    who.textContent = names.length === 1
      ? `${label} · ${names[0]}`
      : `${label} · ${names.join(', ')}`;
    line.appendChild(who);
    pop.appendChild(line);
  }
  if (!pop.childElementCount) return;
  document.body.appendChild(pop);
  const r = anchor.getBoundingClientRect();
  pop.style.left = Math.max(8, Math.min(window.innerWidth - pop.offsetWidth - 8, r.left)) + 'px';
  pop.style.top = (r.bottom + 6) + 'px';
  resPickerEl = pop;
  setTimeout(() => document.addEventListener('click', onResPickerOutside, true), 0);
}

// ---- owner palette editor ----

function openResPaletteEditor() {
  const box = document.getElementById('resPalSlots');
  box.innerHTML = '';
  const pal = RES.palette || {};
  const slots = (pal.slots || []).slice(0, 6);
  for (let i = 0; i < 6; i++) {
    const s = slots[i] || { key: '', label: '', fallback: '' };
    const row = document.createElement('div');
    row.className = 'res-pal-row';
    row.innerHTML =
      `<input class="rp-key" placeholder="key" value="${esc(s.key)}">` +
      `<input class="rp-label" placeholder="label" value="${esc(s.label)}">` +
      `<input class="rp-fb" placeholder="✦" maxlength="8" value="${esc(s.fallback)}">`;
    box.appendChild(row);
  }
  document.getElementById('resPalDefault').value = pal.default_key || 'resonates';
  document.getElementById('resPalUnicode').checked = pal.policy?.allow_unicode !== false;
  document.getElementById('resPalCounts').checked = pal.policy?.show_counts !== false;
  document.getElementById('resPalEffects').checked = pal.policy?.surface_effects !== false;
  document.getElementById('resPalMsg').textContent = '';
  dlgResPalette.showModal();
}

async function saveResPalette() {
  const msg = document.getElementById('resPalMsg');
  const slots = [];
  document.querySelectorAll('#resPalSlots .res-pal-row').forEach(row => {
    const key = row.querySelector('.rp-key').value.trim();
    const label = row.querySelector('.rp-label').value.trim();
    const fb = row.querySelector('.rp-fb').value.trim();
    if (key && label && fb) slots.push({ key, label, fallback: fb });
  });
  try {
    await api(`/api/spaces/${current}/resonance/palette`, {
      method: 'PUT',
      body: JSON.stringify({
        palette_id: (RES.palette?.palette_id || 'space-palette') + '',
        default_key: document.getElementById('resPalDefault').value.trim(),
        slots,
        policy: {
          allow_unicode: document.getElementById('resPalUnicode').checked,
          show_counts: document.getElementById('resPalCounts').checked,
          surface_effects: document.getElementById('resPalEffects').checked,
        },
      }),
    });
    dlgResPalette.close();
    await RES.refresh(current);
    refreshSpace();
  } catch (e) { msg.textContent = e.message; }
}
