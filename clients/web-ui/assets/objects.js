// Objects client (SP-1): the space's domain objects — machines, parts,
// orders — with tasks and human observations on each. One view, two levels:
// the list, and an open object card. All user strings land via textContent.
//
// The revision contract, repeated where the request is built: a revision
// sends the FULL record — props included — and an omitted prop is a deleted
// prop. The editor therefore always round-trips the whole record.

let openObjectID = null;   // object card currently open (hex), else the list
let objEditBase = '';      // base_revision_event_id when editing
let objEditID = '';        // object being edited ('' = creating)
let objShowArchived = false; // list mode: live | archived
let objLastSpace = null;   // which space the view state above belongs to
let objEditParent = '';    // parent object id when creating a child

// Kinds the UI SUGGESTS. The protocol does not close this vocabulary —
// anything slug-shaped is a legal kind (ADR-029 discipline: suggestions are
// mode/UI territory, validity is protocol territory).
const OBJ_KINDS = ['machine', 'part', 'project', 'order', 'material', 'tool',
  'release', 'track', 'session', 'other'];

// Placeholder examples per kind: the editor speaks the vocabulary of the
// thing being made, not always the workshop's.
const OBJ_KIND_HINTS = {
  machine: { name: 'CNC-01', status: 'operational' },
  release: { name: 'Night Signals · EP', status: 'production' },
  track: { name: 'Winter Song', status: 'mixing' },
  session: { name: 'Vocal · Katya · 26 Aug', status: '' },
};

// The picker's glyphs — one per suggested kind, an unknown kind gets ✦.
const OBJ_KIND_GLYPHS = {
  machine: '🛠️', part: '⚙️', tool: '🔧', material: '🧱',
  project: '📋', order: '📦',
  release: '💿', track: '🎵', session: '🎙️',
  other: '✦',
};

// kindSuggestionsFor curates the picker BY SPACE: a studio offers the
// creative set, a workshop the physical one, anywhere else the whole
// vocabulary. Curation only — the protocol accepts any slug regardless,
// and «other» keeps the door open (ADR-029: suggestions are mode
// territory, validity is not).
function kindSuggestionsFor(char) {
  switch (char && char.archetype) {
    case 'studio':
      return ['release', 'track', 'session', 'project', 'other'];
    case 'workshop':
      return ['machine', 'part', 'tool', 'material', 'project', 'order', 'other'];
    default:
      return OBJ_KINDS;
  }
}

function objKindLabel(k) {
  const key = 'objects.kind.' + k;
  const s = t(key);
  return s === key ? k : s;
}

async function refreshObjects() {
  if (!current) return;
  // View state is per-space: an object card left open in the workshop must
  // not reappear over another space's list.
  if (objLastSpace !== current) {
    objLastSpace = current;
    openObjectID = null;
    objShowArchived = false;
  }
  const box = document.getElementById('objects');
  if (openObjectID) { openObject(openObjectID); return; }
  try {
    const r = await api(`/api/spaces/${current}/objects${objShowArchived ? '?archived=1' : ''}`);
    box.innerHTML = '';
    const bar = document.createElement('div');
    bar.className = 'obj-bar';
    const archT = document.createElement('button');
    archT.className = 'btn-plain' + (objShowArchived ? ' sel' : '');
    archT.textContent = t('objects.archived_toggle');
    archT.onclick = () => { objShowArchived = !objShowArchived; refreshObjects(); };
    bar.appendChild(archT);
    const add = document.createElement('button');
    add.className = 'btn-tinted';
    add.textContent = t('objects.new');
    add.onclick = () => openObjectEditor(null);
    bar.appendChild(add);
    box.appendChild(bar);
    if (!r.objects.length) {
      const e = document.createElement('div');
      e.className = 'empty-spaces';
      e.textContent = t('objects.empty');
      box.appendChild(e);
      return;
    }
    const grid = document.createElement('div');
    grid.className = 'obj-grid';
    for (const o of r.objects) {
      const card = document.createElement('div');
      card.className = 'obj-card';
      card.dataset.oid = o.object_id;
      const name = document.createElement('div');
      name.className = 'obj-name'; name.textContent = o.name;
      card.appendChild(name);
      const meta = document.createElement('div');
      meta.className = 'obj-meta';
      const kindChip = document.createElement('span');
      kindChip.className = 'obj-chip'; kindChip.textContent = objKindLabel(o.kind);
      meta.appendChild(kindChip);
      if (o.status) {
        const st = document.createElement('span');
        st.className = 'obj-chip obj-status'; st.textContent = o.status;
        meta.appendChild(st);
      }
      card.appendChild(meta);
      if (o.task_open || o.task_done) {
        const tk = document.createElement('div');
        tk.className = 'obj-tasks';
        tk.textContent = '☑ ' + (o.task_done || 0) + '/' + ((o.task_open || 0) + (o.task_done || 0));
        card.appendChild(tk);
      }
      card.onclick = () => openObject(o.object_id);
      grid.appendChild(card);
    }
    box.appendChild(grid);
  } catch (e) { console.error(e); }
}

async function openObject(oid) {
  if (!current) return;
  openObjectID = oid;
  const box = document.getElementById('objects');
  let o;
  try {
    o = await api(`/api/spaces/${current}/objects/${oid}`);
  } catch (e) {
    // The object may live on a peer this device has not synced yet, or the
    // deep link may be stale. Say so and fall back to the list.
    openObjectID = null;
    refreshObjects();
    return;
  }
  box.innerHTML = '';

  const head = document.createElement('div');
  head.className = 'obj-head';
  const back = document.createElement('button');
  back.className = 'btn-plain';
  back.textContent = '← ' + t('objects.back');
  back.onclick = () => { openObjectID = null; refreshObjects(); };
  head.appendChild(back);
  // Containment breadcrumb: one level up, by NAME — "↑ Night Signals",
  // never eight hex characters.
  if (o.parent) {
    const up = document.createElement('button');
    up.className = 'btn-plain obj-crumb';
    up.textContent = '↑ ' + (o.parent_name || t('objects.parent'));
    up.onclick = () => openObject(o.parent);
    head.appendChild(up);
  }
  const title = document.createElement('div');
  title.className = 'obj-title'; title.textContent = o.name;
  head.appendChild(title);
  const chips = document.createElement('div');
  chips.className = 'obj-chips';
  const kind = document.createElement('span');
  kind.className = 'obj-chip'; kind.textContent = objKindLabel(o.kind);
  chips.appendChild(kind);
  if (o.status) {
    const st = document.createElement('span');
    st.className = 'obj-chip obj-status'; st.textContent = o.status;
    chips.appendChild(st);
  }
  head.appendChild(chips);
  const actions = document.createElement('div');
  actions.className = 'obj-actions';
  head.appendChild(actions);
  // Keep on the STABLE target — survives every revision by construction.
  const keepBtn = document.createElement('button');
  keepBtn.className = 'btn-plain';
  keepBtn.textContent = o.kept ? '✦ ' + t('objects.kept') : '✦ ' + t('objects.keep');
  keepBtn.onclick = () => {
    if (o.kept) {
      api(`/api/spaces/${current}/unkeep`, { method: 'POST', body: JSON.stringify({ target: o.target }) })
        .then(() => openObject(oid)).catch(err => console.error(err));
    } else if (typeof keepToggle === 'function') {
      keepTargetID = o.target;
      document.getElementById('keepNote').value = '';
      document.getElementById('keepMsg').textContent = '';
      dlgKeep.showModal();
    }
  };
  actions.appendChild(keepBtn);
  // «в чат»: stage a signed reference and land in the composer — the
  // reverse of the feed chip's jump here.
  const toChat = document.createElement('button');
  toChat.className = 'btn-plain';
  toChat.textContent = '# ' + t('objref.to_chat');
  toChat.onclick = () => {
    if (typeof objrefsStage === 'function') objrefsStage(oid, o.name);
    switchView('chat');
    document.getElementById('text')?.focus();
  };
  actions.appendChild(toChat);
  const edit = document.createElement('button');
  edit.className = 'btn-tinted';
  edit.textContent = t('objects.edit');
  edit.onclick = () => openObjectEditor(o);
  actions.appendChild(edit);
  const arch = document.createElement('button');
  arch.className = 'btn-plain';
  arch.textContent = o.archived ? t('objects.restore') : t('objects.archive');
  arch.onclick = async () => {
    try {
      if (o.archived) {
        // The restore needs the archive event; the list endpoint does not
        // carry it, so v1 restores via re-open from the archived listing.
        await api(`/api/spaces/${current}/objects/${oid}/restore`,
          { method: 'POST', body: JSON.stringify({ archive_event_id: o.archive_event_id || '' }) });
      } else {
        const r = await api(`/api/spaces/${current}/objects/${oid}/archive`, { method: 'POST', body: '{}' });
        o.archive_event_id = r.archive_event_id;
      }
      openObject(oid);
    } catch (e) { console.error(e); }
  };
  actions.appendChild(arch);
  box.appendChild(head);

  // Reaction row on the stable target (same renderer as the feed).
  if (typeof renderResonanceRow === 'function') {
    const row = renderResonanceRow(o.resonance || null, o.target, () => openObject(oid));
    if (row) { row.classList.add('obj-res'); box.appendChild(row); }
  }

  if (o.summary) {
    const sum = document.createElement('div');
    sum.className = 'obj-summary'; sum.textContent = o.summary;
    box.appendChild(sum);
  }
  if (o.props && o.props.length) {
    const tbl = document.createElement('div');
    tbl.className = 'obj-props';
    for (const p of o.props) {
      const row = document.createElement('div');
      row.className = 'obj-prop';
      const k = document.createElement('span');
      k.className = 'obj-prop-k'; k.textContent = p.key;
      const v = document.createElement('span');
      v.className = 'obj-prop-v'; v.textContent = p.value;
      row.appendChild(k); row.appendChild(v);
      tbl.appendChild(row);
    }
    box.appendChild(tbl);
  }

  // ---- the kind's own section (SP-2 Studio renderer): release/track/
  // session get their shape here; every other kind skips straight to
  // tasks. Kind steers RENDERING only — the vocabulary the user sees is
  // human (tracks, takes, versions), never the kernel's.
  const kindRenderer = OBJ_RENDERERS[o.kind];
  if (kindRenderer) kindRenderer(box, o, oid);

  // ---- tasks: the checklist. The edge is derived server-side from
  // Card.ObjectID; the object record owns nothing. ----
  const tHead = document.createElement('div');
  tHead.className = 'obj-sect'; tHead.textContent = t('objects.tasks');
  box.appendChild(tHead);
  const tList = document.createElement('div');
  tList.className = 'obj-task-list';
  for (const c of o.tasks || []) {
    if (c.status === 'dropped') continue;
    const row = document.createElement('label');
    row.className = 'obj-task' + (c.status === 'done' ? ' done' : '');
    const cb = document.createElement('input');
    cb.type = 'checkbox'; cb.checked = c.status === 'done';
    cb.onchange = async () => {
      try {
        await api(`/api/spaces/${current}/cards/${c.id}/status`,
          { method: 'POST', body: JSON.stringify({ status: cb.checked ? 'done' : 'open' }) });
        openObject(oid);
      } catch (e) { console.error(e); cb.checked = !cb.checked; }
    };
    row.appendChild(cb);
    const txt = document.createElement('span');
    txt.textContent = c.title;
    row.appendChild(txt);
    tList.appendChild(row);
  }
  box.appendChild(tList);
  const tAdd = document.createElement('form');
  tAdd.className = 'obj-task-add';
  const tIn = document.createElement('input');
  tIn.type = 'text'; tIn.placeholder = t('objects.task_ph'); tIn.maxLength = 512;
  tAdd.appendChild(tIn);
  tAdd.appendChild(objSubmitBtn());
  tAdd.onsubmit = async (ev) => {
    ev.preventDefault();
    const title = tIn.value.trim();
    if (!title) return;
    try {
      await api(`/api/spaces/${current}/cards`,
        { method: 'POST', body: JSON.stringify({ title, object_id: oid }) });
      tIn.value = '';
      openObject(oid);
    } catch (e) { console.error(e); }
  };
  box.appendChild(tAdd);

  // ---- observations: the timeline. The same events are feed rows — one
  // event, two projections, so nothing here is a copy. ----
  const oHead = document.createElement('div');
  oHead.className = 'obj-sect'; oHead.textContent = t('objects.observations');
  box.appendChild(oHead);
  const oList = document.createElement('div');
  oList.className = 'obj-obs-list';
  const obs = (o.observations || []).slice().reverse(); // newest first on the card
  for (const n of obs) {
    const row = document.createElement('div');
    row.className = 'obj-obs';
    const when = document.createElement('span');
    when.className = 'obj-obs-when';
    when.textContent = n.observed_at
      ? new Date(n.observed_at * 1000).toLocaleString([], {
          day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' })
      : '';
    row.appendChild(when);
    const txt = document.createElement('span');
    txt.className = 'obj-obs-text'; txt.textContent = '🔎 ' + n.text;
    row.appendChild(txt);
    oList.appendChild(row);
  }
  if (!obs.length) {
    const e = document.createElement('div');
    e.className = 'obj-obs-empty'; e.textContent = t('objects.obs_empty');
    oList.appendChild(e);
  }
  box.appendChild(oList);
  const oAdd = document.createElement('form');
  oAdd.className = 'obj-obs-add';
  const oIn = document.createElement('input');
  oIn.type = 'text'; oIn.placeholder = t('objects.obs_ph'); oIn.maxLength = 1000;
  oAdd.appendChild(oIn);
  oAdd.appendChild(objSubmitBtn());
  oAdd.onsubmit = async (ev) => {
    ev.preventDefault();
    const text = oIn.value.trim();
    if (!text) return;
    try {
      await api(`/api/spaces/${current}/objects/${oid}/observations`,
        { method: 'POST', body: JSON.stringify({ text }) });
      oIn.value = '';
      openObject(oid);
    } catch (e) { console.error(e); }
  };
  box.appendChild(oAdd);
}

// objSubmitBtn: the visible way in. Enter still submits (native form
// behavior); the button makes the affordance discoverable — an input with
// no button reads as a search box.
function objSubmitBtn() {
  const b = document.createElement('button');
  b.type = 'submit';
  b.className = 'obj-add-btn';
  b.textContent = '+';
  return b;
}

// ---- the editor dialog (create + revise share one form) ----

let objEditKind = '';      // the picker's selection ('' = custom slug typed)

function renderKindPicker(selected, char) {
  const pick = document.getElementById('objKindPick');
  const custom = document.getElementById('objKindCustom');
  const kinds = kindSuggestionsFor(char).slice();
  // An existing object whose kind is outside this space's suggestions
  // still shows ITS kind as a card — editing must never hide the truth.
  if (selected && !kinds.includes(selected)) kinds.unshift(selected);
  pick.innerHTML = '';
  custom.style.display = 'none';
  custom.value = '';
  const mark = () => pick.querySelectorAll('.kind-card').forEach(c =>
    c.classList.toggle('sel', c.dataset.k === objEditKind));
  for (const k of kinds) {
    const card = document.createElement('button');
    card.type = 'button';
    card.className = 'kind-card';
    card.dataset.k = k;
    const g = document.createElement('span');
    g.className = 'kind-glyph';
    g.textContent = OBJ_KIND_GLYPHS[k] || '✦';
    card.appendChild(g);
    const l = document.createElement('span');
    l.className = 'kind-label';
    l.textContent = objKindLabel(k);
    card.appendChild(l);
    card.onclick = () => {
      objEditKind = k;
      applyKindHint(k);
      // «other» is a door, not a kind: it opens the free slug field. The
      // field pre-fills with "other" so pressing Save without typing
      // still does the obvious thing.
      if (k === 'other') {
        custom.style.display = '';
        if (!custom.value) custom.value = 'other';
        custom.focus();
        custom.select();
      } else {
        custom.style.display = 'none';
      }
      mark();
    };
    pick.appendChild(card);
  }
  objEditKind = selected || kinds[0];
  if (objEditKind === 'other') custom.style.display = '';
  applyKindHint(objEditKind);
  mark();
}

function applyKindHint(k) {
  const hint = OBJ_KIND_HINTS[k] || OBJ_KIND_HINTS.machine;
  document.getElementById('objName').placeholder = hint.name;
  document.getElementById('objStatus').placeholder = hint.status;
}

function openObjectEditor(o, opts) {
  objEditID = o ? o.object_id : '';
  objEditBase = o ? o.revision_event_id : '';
  objEditParent = o ? (o.parent || '') : ((opts && opts.parent) || '');
  const char = typeof currentSpace === 'function' ? currentSpace()?.character : null;
  renderKindPicker(o ? o.kind : ((opts && opts.kind) || ''), char);
  document.getElementById('objName').value = o ? o.name : '';
  document.getElementById('objStatus').value = o ? (o.status || '') : '';
  document.getElementById('objSummary').value = o ? (o.summary || '') : '';
  document.getElementById('objProps').value =
    o && o.props ? o.props.map(p => p.key + ' = ' + p.value).join('\n') : '';
  document.getElementById('objMsg').textContent = '';
  dlgObject.showModal();
}

function parseObjProps(text) {
  const props = [];
  for (const line of text.split('\n')) {
    const s = line.trim();
    if (!s) continue;
    const i = s.indexOf('=');
    if (i < 1) throw new Error(t('objects.props_bad'));
    props.push({ key: s.slice(0, i).trim(), value: s.slice(i + 1).trim() });
  }
  // The wire wants sorted-unique; sorting is presentation-neutral, but a
  // DUPLICATE key is a real question only the author can answer — let the
  // node refuse it rather than silently keeping one.
  props.sort((a, b) => a.key < b.key ? -1 : a.key > b.key ? 1 : 0);
  return props;
}

async function saveObject() {
  const msg = document.getElementById('objMsg');
  let props;
  try { props = parseObjProps(document.getElementById('objProps').value); }
  catch (e) { msg.textContent = e.message; return; }
  const customKind = document.getElementById('objKindCustom');
  const object = {
    kind: (objEditKind === 'other' && customKind.value.trim())
      ? customKind.value.trim().toLowerCase()
      : objEditKind,
    name: document.getElementById('objName').value.trim(),
    status: document.getElementById('objStatus').value.trim(),
    summary: document.getElementById('objSummary').value.trim(),
    props,
  };
  if (objEditParent) object.parent = objEditParent;
  if (!object.name) { msg.textContent = t('objects.name_required'); return; }
  try {
    if (objEditID) {
      const r = await fetch(`/api/spaces/${current}/objects/${objEditID}`, {
        method: 'POST',
        headers: { 'X-QP-Token': token, 'Content-Type': 'application/json' },
        body: JSON.stringify({ object, base_revision_event_id: objEditBase }),
      });
      const j = await r.json().catch(() => ({}));
      if (r.status === 409) { msg.textContent = t('objects.conflict'); return; }
      if (!r.ok) { msg.textContent = j.error || 'save failed'; return; }
      dlgObject.close();
      openObject(objEditID);
    } else {
      const r = await api(`/api/spaces/${current}/objects`,
        { method: 'POST', body: JSON.stringify({ object }) });
      dlgObject.close();
      openObject(r.object_id);
    }
  } catch (e) { msg.textContent = e.message; }
}

// ---- SP-2: the Studio renderers, keyed by KIND ----
//
// Kind steers rendering and nothing else (ADR-029/030 discipline): a
// release shows its tracks, a track shows its current mix with the notes
// riding the scrubber, a session shows its takes. Any other kind falls
// through to the generic card untouched. The words on screen are human —
// tracks, takes, versions, notes — never edge/register/LWW.

function objSectHead(box, label) {
  const h = document.createElement('div');
  h.className = 'obj-sect';
  h.textContent = label;
  box.appendChild(h);
}

// uploadAndAttach: one gesture — pick a file, bare-upload it (the node
// emits the block.attached.v1 carrier), then emit the edge. For a track's
// mix the new upload supersedes the current version: that IS the "new
// version" gesture from the groom.
function uploadAndAttach(oid, role, supersedes, onDone) {
  const inp = document.createElement('input');
  inp.type = 'file';
  inp.accept = 'audio/*';
  inp.onchange = async () => {
    const file = inp.files && inp.files[0];
    if (!file) return;
    try {
      const up = await uploadAssetFile(file);
      const body = { asset: up.asset_id, role, label: file.name };
      if (supersedes) { body.supersedes = supersedes; body.candidate = 'set'; }
      await api(`/api/spaces/${current}/objects/${oid}/assets`,
        { method: 'POST', body: JSON.stringify(body) });
      onDone();
    } catch (e) { console.error(e); }
  };
  inp.click();
}

function objMarkTime(ms) {
  const s = Math.floor(ms / 1000);
  return String(Math.floor(s / 60)).padStart(2, '0') + ':' + String(s % 60).padStart(2, '0');
}

// annotatedPlayer builds the house audio player for an asset and rides
// its notes on the scrubber: percentage-positioned pins, click = seek.
function annotatedPlayer(assetHex, notes) {
  // A replica may hold the edge before the bytes: ask the node to pull
  // the asset. Fire-and-forget — the GET answers 409 until complete, and
  // reopening the card picks the bytes up.
  api(`/api/spaces/${current}/assets/${assetHex}/fetch`, { method: 'POST', body: '{}' })
    .catch(() => {});
  const player = makeAudioPlayer(assetURL(assetHex), null, 0);
  const timed = (notes || []).filter(n => n.position_ms !== undefined);
  if (timed.length) {
    const place = () => {
      const dur = player.durationMs();
      if (!dur) return;
      player.scrubEl.querySelectorAll('.obj-mark').forEach(m => m.remove());
      for (const n of timed) {
        const pin = document.createElement('button');
        pin.className = 'obj-mark';
        pin.style.left = Math.min(100, (n.position_ms / dur) * 100) + '%';
        pin.title = objMarkTime(n.position_ms) + '  ' + n.text;
        pin.onclick = (ev) => { ev.stopPropagation(); player.seekMs(n.position_ms); };
        player.scrubEl.appendChild(pin);
      }
    };
    player.onMeta(place);
  }
  return player;
}

function renderNotesList(box, notes, player) {
  const list = document.createElement('div');
  list.className = 'obj-obs-list';
  for (const n of notes || []) {
    const row = document.createElement('div');
    row.className = 'obj-obs';
    const when = document.createElement('span');
    when.className = 'obj-obs-when';
    if (n.position_ms !== undefined) {
      when.textContent = objMarkTime(n.position_ms);
      when.classList.add('obj-obs-seek');
      when.onclick = () => { if (player) player.seekMs(n.position_ms); };
    }
    row.appendChild(when);
    const txt = document.createElement('span');
    txt.className = 'obj-obs-text';
    txt.textContent = n.text;
    row.appendChild(txt);
    list.appendChild(row);
  }
  if (!(notes || []).length) {
    const e = document.createElement('div');
    e.className = 'obj-obs-empty';
    e.textContent = t('studio.notes_empty');
    list.appendChild(e);
  }
  box.appendChild(list);
}

// noteComposer: the "at 01:42" gesture — the chip captures the player's
// position when toggled; without it the note is about the whole asset.
function noteComposer(box, assetHex, player, oid, reload) {
  const form = document.createElement('form');
  form.className = 'obj-obs-add';
  let atMs = null;
  const chip = document.createElement('button');
  chip.type = 'button';
  chip.className = 'obj-at-chip';
  chip.textContent = '⏱';
  chip.title = t('studio.at_position');
  chip.onclick = () => {
    if (atMs === null && player) {
      atMs = Math.round(player.currentMs());
      chip.textContent = '⏱ ' + objMarkTime(atMs);
      chip.classList.add('sel');
    } else {
      atMs = null;
      chip.textContent = '⏱';
      chip.classList.remove('sel');
    }
  };
  form.appendChild(chip);
  const inp = document.createElement('input');
  inp.type = 'text';
  inp.placeholder = t('studio.note_ph');
  inp.maxLength = 1000;
  form.appendChild(inp);
  form.appendChild(objSubmitBtn());
  form.onsubmit = async (ev) => {
    ev.preventDefault();
    const text = inp.value.trim();
    if (!text) return;
    const body = { text, object_id: oid };
    if (atMs !== null) body.position_ms = atMs;
    try {
      await api(`/api/spaces/${current}/assets/${assetHex}/annotations`,
        { method: 'POST', body: JSON.stringify(body) });
      inp.value = '';
      reload();
    } catch (e) { console.error(e); }
  };
  box.appendChild(form);
}

async function setCandidate(oid, assetHex, reload) {
  try {
    await api(`/api/spaces/${current}/objects/${oid}/assets/${assetHex}`,
      { method: 'POST', body: JSON.stringify({ candidate: 'set' }) });
    reload();
  } catch (e) { console.error(e); }
}

const OBJ_RENDERERS = {
  release(box, o, oid) {
    objSectHead(box, t('studio.tracks'));
    const list = document.createElement('div');
    list.className = 'obj-child-list';
    for (const c of o.children || []) {
      const row = document.createElement('div');
      row.className = 'obj-child';
      const name = document.createElement('span');
      name.className = 'obj-child-name';
      name.textContent = c.name;
      row.appendChild(name);
      const meta = document.createElement('span');
      meta.className = 'obj-child-meta';
      meta.textContent = (c.status ? c.status : '') +
        (c.task_open ? ' · ☐ ' + c.task_open : '');
      row.appendChild(meta);
      row.onclick = () => openObject(c.object_id);
      list.appendChild(row);
    }
    if (!(o.children || []).length) {
      const e = document.createElement('div');
      e.className = 'obj-obs-empty';
      e.textContent = t('studio.tracks_empty');
      list.appendChild(e);
    }
    box.appendChild(list);
    const add = document.createElement('button');
    add.className = 'btn-plain obj-sect-act';
    add.textContent = t('studio.new_track');
    add.onclick = () => openObjectEditor(null, { kind: 'track', parent: oid });
    box.appendChild(add);
  },

  track(box, o, oid) {
    const reload = () => openObject(oid);
    // Annotation counts live on the edge list; the version chain reuses
    // them by asset id.
    const annCount = {};
    for (const e of o.assets || []) annCount[e.asset] = e.annotations || 0;

    objSectHead(box, t('studio.current'));
    // The stage: shows the CURRENT mix by default, or whichever version
    // the person opened from the list below — with that version's own
    // notes riding the scrubber. Katya's note on mix-11 must be one tap
    // away even after mix-12 took the star.
    const stage = document.createElement('div');
    stage.className = 'obj-stage';
    box.appendChild(stage);
    let stagedAsset = null;
    const labelOf = (asset) => {
      for (const e of o.assets || []) if (e.asset === asset) return e.label || asset.slice(0, 8);
      return asset.slice(0, 8);
    };
    async function showOnStage(asset, notes) {
      stagedAsset = asset;
      stage.innerHTML = '';
      if (!asset) {
        const e = document.createElement('div');
        e.className = 'obj-obs-empty';
        e.textContent = t('studio.no_mix');
        stage.appendChild(e);
        return;
      }
      if (notes === undefined) {
        try {
          const r = await api(`/api/spaces/${current}/assets/${asset}/annotations`);
          notes = r.annotations;
        } catch (e) { notes = []; }
        if (stagedAsset !== asset) return; // somebody staged another version meanwhile
      }
      if (asset !== o.current_asset) {
        const which = document.createElement('div');
        which.className = 'obj-stage-which';
        which.textContent = labelOf(asset);
        stage.appendChild(which);
      }
      const player = annotatedPlayer(asset, notes);
      stage.appendChild(player);
      renderNotesList(stage, notes, player);
      noteComposer(stage, asset, player, oid, reload);
    }
    showOnStage(o.current_asset || null, o.current_asset ? o.current_annotations : []);
    const up = document.createElement('button');
    up.className = 'btn-tinted obj-sect-act';
    up.textContent = t('studio.upload_mix');
    up.onclick = () => uploadAndAttach(oid, 'mix', o.current_asset || '', reload);
    box.appendChild(up);

    objSectHead(box, t('studio.versions'));
    const vs = document.createElement('div');
    vs.className = 'obj-ver-list';
    for (const ch of o.version_chains || []) {
      for (const v of ch.versions) {
        const row = document.createElement('div');
        row.className = 'obj-ver';
        row.style.cursor = 'pointer';
        row.onclick = () => showOnStage(v.asset);
        const name = document.createElement('span');
        name.className = 'obj-ver-name';
        name.textContent = v.label || v.asset.slice(0, 8);
        row.appendChild(name);
        if (v.asset === o.current_asset) {
          const b = document.createElement('span');
          b.className = 'obj-chip obj-status';
          b.textContent = t('studio.current_badge');
          row.appendChild(b);
        }
        const tail = document.createElement('span');
        tail.className = 'obj-ver-tail';
        row.appendChild(tail);
        if (annCount[v.asset]) {
          const n = document.createElement('span');
          n.className = 'obj-ver-notes';
          n.textContent = '💬 ' + annCount[v.asset];
          tail.appendChild(n);
        }
        if (v.candidate) {
          const s = document.createElement('span');
          s.className = 'obj-ver-star';
          s.textContent = '★';
          tail.appendChild(s);
        } else {
          const s = document.createElement('button');
          s.className = 'obj-ver-star dim';
          s.textContent = '☆';
          s.title = t('studio.make_candidate');
          s.onclick = (ev) => { ev.stopPropagation(); setCandidate(oid, v.asset, reload); };
          tail.appendChild(s);
        }
        vs.appendChild(row);
      }
    }
    box.appendChild(vs);

    const sessions = (o.children || []).filter(() => true);
    if (sessions.length) {
      objSectHead(box, t('studio.sessions'));
      for (const c of sessions) {
        const row = document.createElement('div');
        row.className = 'obj-child';
        const name = document.createElement('span');
        name.className = 'obj-child-name';
        name.textContent = c.name;
        row.appendChild(name);
        row.onclick = () => openObject(c.object_id);
        box.appendChild(row);
      }
    }
    const addS = document.createElement('button');
    addS.className = 'btn-plain obj-sect-act';
    addS.textContent = t('studio.new_session');
    addS.onclick = () => openObjectEditor(null, { kind: 'session', parent: oid });
    box.appendChild(addS);
  },

  session(box, o, oid) {
    const reload = () => openObject(oid);
    objSectHead(box, t('studio.takes'));
    const list = document.createElement('div');
    list.className = 'obj-take-list';
    const edges = (o.assets || []).filter(e => !e.detached);
    for (const e of edges) {
      const row = document.createElement('div');
      row.className = 'obj-take';
      const head = document.createElement('div');
      head.className = 'obj-take-head';
      const name = document.createElement('span');
      name.className = 'obj-child-name';
      name.textContent = (e.label || e.asset.slice(0, 8)) + (e.candidate ? ' ★' : '');
      head.appendChild(name);
      if (!e.candidate) {
        const s = document.createElement('button');
        s.className = 'obj-ver-star dim';
        s.textContent = '☆';
        s.title = t('studio.make_candidate');
        s.onclick = () => setCandidate(oid, e.asset, reload);
        head.appendChild(s);
      }
      row.appendChild(head);
      row.appendChild(makeAudioPlayer(assetURL(e.asset), null, 0));
      list.appendChild(row);
    }
    if (!edges.length) {
      const em = document.createElement('div');
      em.className = 'obj-obs-empty';
      em.textContent = t('studio.takes_empty');
      list.appendChild(em);
    }
    box.appendChild(list);
    const up = document.createElement('button');
    up.className = 'btn-tinted obj-sect-act';
    up.textContent = t('studio.upload_take');
    up.onclick = () => uploadAndAttach(oid, 'take', '', reload);
    box.appendChild(up);
  },
};

// Space Mode's view ordering moved to spacenav.js, where it grew from
// "put objects first" into order + visibility + the view's own act.

