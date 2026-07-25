// Mentions (AT-0A.1): addressing someone is a SIGNED STRUCTURAL field, not
// markup inside the text. The composer collects principals as you type "@",
// sends them alongside the text, and the feed highlights them from the
// node's resolved projection — never by re-parsing the message.

const MENTION = {
  // principals staged for the message currently being typed
  staged: new Map(), // hex -> name
  // Implicit addressees come from an ACT rather than from typing — replying
  // to someone addresses them. They are not tied to the text, so editing the
  // message cannot silently drop the person you are answering.
  implicit: new Set(), // hex
  members: [],       // [{principal, name}] for the open space
  active: -1,        // highlighted row in the picker
};

// mentionsLoadMembers refreshes the candidate list for the open space.
async function mentionsLoadMembers(spaceID) {
  MENTION.members = [];
  if (!spaceID) return;
  try {
    const r = await api(`/api/spaces/${spaceID}/members`);
    MENTION.members = (r || [])
      .filter(m => m.principal && m.name)
      .map(m => ({ principal: m.principal, name: m.name }));
  } catch (e) { /* members are optional for typing */ }
}

// mentionsReset clears staging (called after send and on space switch).
function mentionsReset() {
  MENTION.staged.clear();
  MENTION.implicit.clear();
  mentionsClosePicker();
  mentionsRenderChips();
}

// mentionsPayload returns the principals to attach to the outgoing message.
// Only people actually still named in the text stay attached: deleting the
// "@name" text should not silently keep addressing them.
function mentionsPayload(text) {
  const out = [];
  for (const [hex, name] of MENTION.staged) {
    if (MENTION.implicit.has(hex) || !name || text.includes('@' + name)) out.push(hex);
  }
  return out;
}

// ---- typing: "@" opens a picker over the member list ----

function mentionsOnInput(ev) {
  const inp = ev.target;
  const frag = mentionsFragment(inp);
  if (frag === null) { mentionsClosePicker(); return; }
  const q = frag.query.toLowerCase();
  const hits = MENTION.members
    .filter(m => m.name.toLowerCase().includes(q))
    .slice(0, 6);
  if (!hits.length) { mentionsClosePicker(); return; }
  MENTION.active = 0;
  mentionsOpenPicker(hits, inp);
}

// mentionsFragment finds an in-progress "@word" immediately before the caret.
function mentionsFragment(inp) {
  const pos = inp.selectionStart ?? inp.value.length;
  const before = inp.value.slice(0, pos);
  const at = before.lastIndexOf('@');
  if (at < 0) return null;
  // "@" must start a word, and the fragment must not span whitespace.
  if (at > 0 && !/\s/.test(before[at - 1])) return null;
  const query = before.slice(at + 1);
  if (/\s/.test(query)) return null;
  return { at, query };
}

function mentionsOpenPicker(hits, inp) {
  let box = document.getElementById('mentionPicker');
  if (!box) {
    box = document.createElement('div');
    box.id = 'mentionPicker';
    box.className = 'mention-picker';
    document.body.appendChild(box);
  }
  box.innerHTML = '';
  hits.forEach((m, i) => {
    const row = document.createElement('div');
    row.className = 'mention-row' + (i === MENTION.active ? ' sel' : '');
    row.innerHTML = `<span class="glyph g18">${glyphSVG(m.principal, 'human', 18)}</span>` +
      `<span class="mention-name"></span>`;
    row.querySelector('.mention-name').textContent = m.name;
    row.onmousedown = (e) => { e.preventDefault(); mentionsPick(m, inp); };
    box.appendChild(row);
  });
  const r = inp.getBoundingClientRect();
  box.style.left = Math.round(r.left) + 'px';
  box.style.bottom = Math.round(window.innerHeight - r.top + 6) + 'px';
  box.style.display = '';
  box.dataset.hits = JSON.stringify(hits);
}

function mentionsClosePicker() {
  const box = document.getElementById('mentionPicker');
  if (box) box.style.display = 'none';
  MENTION.active = -1;
}

// mentionsOnKeydown drives the picker: arrows move, Enter/Tab pick, Esc closes.
// Returns true when it consumed the key (so the composer must not submit).
function mentionsOnKeydown(ev) {
  const box = document.getElementById('mentionPicker');
  if (!box || box.style.display === 'none') return false;
  const hits = JSON.parse(box.dataset.hits || '[]');
  if (!hits.length) return false;
  if (ev.key === 'ArrowDown' || ev.key === 'ArrowUp') {
    ev.preventDefault();
    MENTION.active = (MENTION.active + (ev.key === 'ArrowDown' ? 1 : hits.length - 1)) % hits.length;
    mentionsOpenPicker(hits, ev.target);
    return true;
  }
  if (ev.key === 'Enter' || ev.key === 'Tab') {
    ev.preventDefault();
    mentionsPick(hits[Math.max(0, MENTION.active)], ev.target);
    return true;
  }
  if (ev.key === 'Escape') { mentionsClosePicker(); return true; }
  return false;
}

// mentionsPick swaps the typed fragment for the member's name and stages the
// principal — the name is display, the principal is what gets signed.
function mentionsPick(m, inp) {
  const frag = mentionsFragment(inp);
  if (!frag) { mentionsClosePicker(); return; }
  const pos = inp.selectionStart ?? inp.value.length;
  const before = inp.value.slice(0, frag.at);
  const after = inp.value.slice(pos);
  inp.value = before + '@' + m.name + ' ' + after;
  const caret = (before + '@' + m.name + ' ').length;
  inp.setSelectionRange(caret, caret);
  MENTION.staged.set(m.principal, m.name);
  mentionsClosePicker();
  mentionsRenderChips();
  inp.focus();
}

// mentionsStage adds someone to the outgoing message without typing "@" —
// used when replying, so answering a person always reaches them. The chip
// still shows up, because addressing someone is never silent.
function mentionsStage(principalHex, name) {
  if (!principalHex) return;
  MENTION.staged.set(principalHex, name || principalHex.slice(0, 8));
  MENTION.implicit.add(principalHex);
  mentionsRenderChips();
}

// mentionsUnstage removes an implicit addressee — used when a reply is
// cancelled, so the person is no longer addressed either.
function mentionsUnstage(principalHex) {
  MENTION.staged.delete(principalHex);
  MENTION.implicit.delete(principalHex);
  mentionsRenderChips();
}

// mentionsRenderChips shows who this message will address — visible, because
// addressing someone is an act, not a hidden side effect of typing.
function mentionsRenderChips() {
  let bar = document.getElementById('mentionChips');
  if (!bar) {
    const composer = document.getElementById('composer');
    if (!composer) return;
    bar = document.createElement('div');
    bar.id = 'mentionChips';
    bar.className = 'mention-chips';
    composer.parentNode.insertBefore(bar, composer);
  }
  bar.innerHTML = '';
  const text = document.getElementById('text')?.value || '';
  const live = mentionsPayload(text);
  if (!live.length) { bar.style.display = 'none'; return; }
  bar.style.display = '';
  const label = document.createElement('span');
  label.className = 'mention-chips-label';
  label.textContent = 'to';
  bar.appendChild(label);
  for (const hex of live) {
    const c = document.createElement('span');
    c.className = 'mention-chip' + (MENTION.implicit.has(hex) ? ' implicit' : '');
    // An implicit addressee is marked, so it is obvious this came from
    // replying rather than from something typed.
    c.textContent = (MENTION.implicit.has(hex) ? '↩ @' : '@') + MENTION.staged.get(hex);
    bar.appendChild(c);
  }
}

// ---- feed: highlight what the node already resolved ----

// mentionNode renders a text body with the addressed names highlighted. The
// node tells us WHO was mentioned (signed field); we only decorate the names
// where they appear. Text is inserted as text nodes — never innerHTML.
function mentionText(e) {
  const el = document.createElement('div');
  el.className = 'txt' + (e.mentions_me ? ' addressed' : '');
  const names = (e.mention_names || []).filter(Boolean);
  if (!names.length) { el.textContent = e.text; return el; }
  // Longest first so "@ann" inside "@anna" cannot win.
  const sorted = [...names].sort((a, b) => b.length - a.length);
  let rest = e.text;
  while (rest) {
    let best = -1, bestName = '';
    for (const n of sorted) {
      const i = rest.indexOf('@' + n);
      if (i >= 0 && (best < 0 || i < best)) { best = i; bestName = n; }
    }
    if (best < 0) { el.appendChild(document.createTextNode(rest)); break; }
    if (best > 0) el.appendChild(document.createTextNode(rest.slice(0, best)));
    const tag = document.createElement('span');
    tag.className = 'mention';
    tag.textContent = '@' + bestName;
    el.appendChild(tag);
    rest = rest.slice(best + bestName.length + 1);
  }
  return el;
}
