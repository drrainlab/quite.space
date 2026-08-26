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

// Kinds the UI SUGGESTS. The protocol does not close this vocabulary —
// anything slug-shaped is a legal kind (ADR-029 discipline: suggestions are
// mode/UI territory, validity is protocol territory).
const OBJ_KINDS = ['machine', 'part', 'project', 'order', 'material', 'tool', 'other'];

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
      meta.textContent = objKindLabel(o.kind) + (o.status ? ' · ' + o.status : '');
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
  const spread = document.createElement('span'); spread.style.flex = '1';
  head.appendChild(spread);
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
  head.appendChild(keepBtn);
  const edit = document.createElement('button');
  edit.className = 'btn-tinted';
  edit.textContent = t('objects.edit');
  edit.onclick = () => openObjectEditor(o);
  head.appendChild(edit);
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
  head.appendChild(arch);
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
    when.textContent = n.observed_at ? new Date(n.observed_at * 1000).toLocaleString() : '';
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

function openObjectEditor(o) {
  objEditID = o ? o.object_id : '';
  objEditBase = o ? o.revision_event_id : '';
  const kindSel = document.getElementById('objKind');
  kindSel.innerHTML = '';
  for (const k of OBJ_KINDS) {
    const opt = document.createElement('option');
    opt.value = k; opt.textContent = objKindLabel(k);
    kindSel.appendChild(opt);
  }
  if (o && !OBJ_KINDS.includes(o.kind)) {
    const opt = document.createElement('option');
    opt.value = o.kind; opt.textContent = o.kind;
    kindSel.appendChild(opt);
  }
  kindSel.value = o ? o.kind : 'machine';
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
  const object = {
    kind: document.getElementById('objKind').value,
    name: document.getElementById('objName').value.trim(),
    status: document.getElementById('objStatus').value.trim(),
    summary: document.getElementById('objSummary').value.trim(),
    props,
  };
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

// ---- Space Mode v1: reading qp.central (ADR-029: view order only) ----
//
// central=objects puts the Objects tab FIRST. The LANDING view is decided
// by refreshSpace's space-switch block in app.js — the one place a switch
// already resets the view, so mode and reset cannot fight. This function
// only orders the buttons.
function applyCentralView(char) {
  const sw = document.getElementById('viewSwitch');
  const objBtn = sw && sw.querySelector('button[data-v="objects"]');
  if (!sw || !objBtn || !char) return;
  const objectsCentral = char.central === 'objects';
  const first = sw.querySelector('button');
  if (objectsCentral && first !== objBtn) sw.insertBefore(objBtn, first);
  if (!objectsCentral && first === objBtn) sw.appendChild(objBtn);
}
