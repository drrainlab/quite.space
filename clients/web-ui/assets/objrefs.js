// Object references (SP-2.1): saying WHAT a message is about is a SIGNED
// STRUCTURAL field, the object twin of mentions. The composer collects
// object ids as you type "#", sends them alongside the text, and the feed
// renders chips from the node's resolved projection — never by re-parsing
// the message. The object's NAME stays in the text, so a client that
// predates the field still reads a whole sentence.

const OBJREF = {
  staged: new Map(), // hex object id -> name
  objects: [],       // [{object_id, name}] for the open space
  active: -1,
};

// objrefsLoadObjects refreshes the candidate list for the open space.
async function objrefsLoadObjects(spaceID) {
  OBJREF.objects = [];
  if (!spaceID) return;
  try {
    const r = await api(`/api/spaces/${spaceID}/objects`);
    OBJREF.objects = (r.objects || [])
      .filter(o => o.object_id && o.name)
      .map(o => ({ object_id: o.object_id, name: o.name, kind: o.kind }));
  } catch (e) { /* objects are optional for typing */ }
}

// objrefsReset clears staging (after send and on space switch).
function objrefsReset() {
  OBJREF.staged.clear();
  objrefsClosePicker();
  objrefsRenderChips();
}

// objrefsPayload: only objects still NAMED in the text stay attached —
// deleting "#Winter Song" should not silently keep the claim. An entry
// staged from the object card (no "#" typed) survives by its chip until
// the chip is dismissed.
function objrefsPayload(text) {
  const out = [];
  for (const [hex, entry] of OBJREF.staged) {
    if (entry.pinned || text.includes('#' + entry.name)) out.push(hex);
  }
  return out;
}

// ---- typing: "#" opens a picker over the space's objects ----

function objrefsOnInput(ev) {
  const inp = ev.target;
  const frag = objrefsFragment(inp);
  if (frag === null) { objrefsClosePicker(); return; }
  // "#" alone never opens the picker — markdown's heading lives there
  // ("# Заголовок"), and a picker on the bare hash would eat the Enter
  // meant for a newline. One typed character is the signal of intent.
  // (Rendering cannot collide at all: the pick inserts "#Name" with no
  // space, and the markdown heading rule requires "#\s".)
  if (!frag.query) { objrefsClosePicker(); return; }
  const q = frag.query.toLowerCase();
  const hits = OBJREF.objects
    .filter(o => o.name.toLowerCase().includes(q))
    .slice(0, 6);
  if (!hits.length) { objrefsClosePicker(); return; }
  OBJREF.active = 0;
  objrefsOpenPicker(hits, inp);
}

function objrefsFragment(inp) {
  const pos = inp.selectionStart ?? inp.value.length;
  const before = inp.value.slice(0, pos);
  const at = before.lastIndexOf('#');
  if (at < 0) return null;
  if (at > 0 && !/\s/.test(before[at - 1])) return null;
  const query = before.slice(at + 1);
  if (/\s/.test(query)) return null;
  return { at, query };
}

function objrefsOpenPicker(hits, inp) {
  let box = document.getElementById('objrefPicker');
  if (!box) {
    box = document.createElement('div');
    box.id = 'objrefPicker';
    box.className = 'mention-picker';
    document.body.appendChild(box);
  }
  box.innerHTML = '';
  hits.forEach((o, i) => {
    const row = document.createElement('div');
    row.className = 'mention-row' + (i === OBJREF.active ? ' sel' : '');
    const g = document.createElement('span');
    g.className = 'objref-glyph';
    g.textContent = (typeof OBJ_KIND_GLYPHS !== 'undefined' && OBJ_KIND_GLYPHS[o.kind]) || '✦';
    row.appendChild(g);
    const n = document.createElement('span');
    n.className = 'mention-name';
    n.textContent = o.name;
    row.appendChild(n);
    row.onmousedown = (e) => { e.preventDefault(); objrefsPick(o, inp); };
    box.appendChild(row);
  });
  const r = inp.getBoundingClientRect();
  box.style.left = Math.round(r.left) + 'px';
  box.style.bottom = Math.round(window.innerHeight - r.top + 6) + 'px';
  box.style.display = '';
  box.dataset.hits = JSON.stringify(hits);
}

function objrefsClosePicker() {
  const box = document.getElementById('objrefPicker');
  if (box) box.style.display = 'none';
  OBJREF.active = -1;
}

// objrefsOnKeydown drives the picker; returns true when it ate the key.
function objrefsOnKeydown(ev) {
  const box = document.getElementById('objrefPicker');
  if (!box || box.style.display === 'none') return false;
  const hits = JSON.parse(box.dataset.hits || '[]');
  if (!hits.length) return false;
  if (ev.key === 'ArrowDown' || ev.key === 'ArrowUp') {
    ev.preventDefault();
    OBJREF.active = (OBJREF.active + (ev.key === 'ArrowDown' ? 1 : hits.length - 1)) % hits.length;
    objrefsOpenPicker(hits, ev.target);
    return true;
  }
  if (ev.key === 'Enter' || ev.key === 'Tab') {
    ev.preventDefault();
    objrefsPick(hits[Math.max(0, OBJREF.active)], ev.target);
    return true;
  }
  if (ev.key === 'Escape') { objrefsClosePicker(); return true; }
  return false;
}

// objrefsPick swaps the typed fragment for the object's name and stages
// the id — the name is display, the id is what gets signed.
function objrefsPick(o, inp) {
  const frag = objrefsFragment(inp);
  if (!frag) { objrefsClosePicker(); return; }
  const pos = inp.selectionStart ?? inp.value.length;
  const before = inp.value.slice(0, frag.at);
  const after = inp.value.slice(pos);
  inp.value = before + '#' + o.name + ' ' + after;
  const caret = (before + '#' + o.name + ' ').length;
  inp.setSelectionRange(caret, caret);
  OBJREF.staged.set(o.object_id, { name: o.name, pinned: false });
  objrefsClosePicker();
  objrefsRenderChips();
  inp.focus();
}

// objrefsStage attaches an object without typing "#" — the card's "to
// chat" gesture. Pinned: it survives until its chip is dismissed.
function objrefsStage(objectID, name) {
  if (!objectID) return;
  OBJREF.staged.set(objectID, { name: name || objectID.slice(0, 8), pinned: true });
  objrefsRenderChips();
}

// objrefsRenderChips shows what this message will be about.
function objrefsRenderChips() {
  let bar = document.getElementById('objrefChips');
  if (!bar) {
    const composer = document.getElementById('composer');
    if (!composer) return;
    bar = document.createElement('div');
    bar.id = 'objrefChips';
    bar.className = 'mention-chips';
    composer.parentNode.insertBefore(bar, composer);
  }
  bar.innerHTML = '';
  const text = document.getElementById('text')?.value || '';
  const live = objrefsPayload(text);
  if (!live.length) { bar.style.display = 'none'; return; }
  bar.style.display = '';
  const label = document.createElement('span');
  label.className = 'mention-chips-label';
  label.textContent = t('objref.about');
  bar.appendChild(label);
  for (const hex of live) {
    const entry = OBJREF.staged.get(hex);
    const c = document.createElement('span');
    c.className = 'mention-chip objref-chip';
    c.textContent = '#' + entry.name;
    if (entry.pinned) {
      const x = document.createElement('button');
      x.className = 'objref-chip-x';
      x.textContent = '×';
      x.onclick = () => { OBJREF.staged.delete(hex); objrefsRenderChips(); };
      c.appendChild(x);
    }
    bar.appendChild(c);
  }
}

// ---- feed: chips from the node's resolved projection ----

// objrefChips renders the "about" chips under a message body. The node
// resolved the names (signed ids stay authoritative); an object this
// replica has not materialized yet shows a dimmed stub, honestly.
function objrefChips(e) {
  const refs = e.object_refs || [];
  if (!refs.length) return null;
  const row = document.createElement('div');
  row.className = 'objref-row';
  refs.forEach((hex, i) => {
    const name = (e.object_ref_names || [])[i] || '';
    const chip = document.createElement('button');
    chip.className = 'objref-feed-chip' + (name ? '' : ' unknown');
    chip.textContent = name ? '#' + name + ' →' : t('objref.unknown');
    if (name && typeof openObject === 'function') {
      chip.onclick = () => { switchView('objects'); openObject(hex); };
    }
    row.appendChild(chip);
  });
  return row;
}
