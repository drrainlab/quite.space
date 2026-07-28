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

  // ✧ quick affordance: tap = default meaning (or release own);
  // hover/long-press = the palette picker.
  const add = document.createElement('span');
  add.className = 'res-add';
  add.textContent = '✧';
  add.title = 'resonate';
  let pressT = null, hoverT = null, pickerOpened = false;
  add.onclick = () => {
    if (pickerOpened) { pickerOpened = false; return; }
    if (res?.own) clearResonance(targetId, onChange);
    else setSemantic(targetId, RES.defaultKey(), onChange);
  };
  add.onmouseenter = () => {
    hoverT = setTimeout(() => openResPicker(targetId, add, res, onChange), 350);
  };
  add.onmouseleave = () => clearTimeout(hoverT);
  add.addEventListener('touchstart', () => {
    pressT = setTimeout(() => {
      pickerOpened = true;
      openResPicker(targetId, add, res, onChange);
    }, 450);
  }, { passive: true });
  add.addEventListener('touchend', () => clearTimeout(pressT));
  row.appendChild(add);
  // Residue (RP-2A) refreshes on every render — calm state, no arrival here.
  //
  // Deliberately NOT the article: a message bubble wearing a faint ring
  // reads as a touched object, but the same ring around a whole post drew a
  // contour around the entire reading surface — and since AM-6 that surface
  // IS the atmosphere, which carries the post's mood by itself. The chips in
  // the resonance row still say who resonated; the post's aesthetics get
  // their own pass later.
  requestAnimationFrame(() => {
    if (typeof RESFX === 'undefined') return;
    const host = row.closest('.bubble, .shelf-card');
    if (host) RESFX.applyResidue(host, res);
  });
  return row;
}

function resChipTitle(g) {
  const label = g.label || g.key || g.value || 'reaction';
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
  const ownId = res?.own ? resIdentity(res.own) : null;

  const slotRow = document.createElement('div');
  slotRow.className = 'res-picker-slots';
  for (const s of RES.slots()) {
    const b = document.createElement('button');
    b.className = 'res-slot' + (ownId === 's:' + s.key ? ' res-own' : '');
    b.type = 'button';
    const sym = document.createElement('span');
    sym.className = 'res-sym'; sym.textContent = s.fallback;
    const lbl = document.createElement('span');
    lbl.className = 'res-lbl'; lbl.textContent = s.label;
    b.append(sym, lbl);
    b.onclick = () => { closeResPicker(); setSemantic(targetId, s.key, onChange); };
    slotRow.appendChild(b);
  }
  pop.appendChild(slotRow);

  const actions = document.createElement('div');
  actions.className = 'res-picker-actions';
  if (RES.allowUnicode()) {
    const more = document.createElement('button');
    more.className = 'btn-plain'; more.type = 'button';
    more.textContent = 'More…';
    more.onclick = () => renderEmojiGrid(pop, targetId, onChange);
    actions.appendChild(more);
  }
  if (res?.own) {
    const rm = document.createElement('button');
    rm.className = 'btn-plain'; rm.type = 'button';
    rm.textContent = 'Remove';
    rm.onclick = () => { closeResPicker(); clearResonance(targetId, onChange); };
    actions.appendChild(rm);
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

function renderEmojiGrid(pop, targetId, onChange) {
  let grid = pop.querySelector('.res-emoji-grid');
  if (grid) { grid.remove(); return; }
  grid = document.createElement('div');
  grid.className = 'res-emoji-grid';
  for (const em of RES_EMOJI) {
    if (!em) continue;
    const b = document.createElement('button');
    b.type = 'button'; b.textContent = em;
    b.onclick = () => { closeResPicker(); setUnicode(targetId, em, onChange); };
    grid.appendChild(b);
  }
  const free = document.createElement('input');
  free.placeholder = 'any emoji…'; free.maxLength = 8;
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
    const label = g.label || g.key || g.value || 'reaction';
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
