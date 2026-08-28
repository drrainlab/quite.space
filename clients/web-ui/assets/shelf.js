// Shelf client (LR-1): the space's kept memory. Objects render through the
// runtime registry (registry.js) — the first wave-native renderer. Semantics
// mirror the reducer: OR across people, LWW within one person; un-keep only
// your own state from here.

let keepTargetID = null; // entry id pending in the keep dialog

// keepToggle is the hover affordance on a feed entry: not kept → open the
// note dialog; kept by me → un-keep immediately.
function keepToggle(e) {
  if (e.kept) {
    api(`/api/spaces/${current}/unkeep`, {
      method: 'POST',
      body: JSON.stringify({ target: e.id }),
    }).then(() => refreshSpace()).catch(err => console.error(err));
    return;
  }
  keepTargetID = e.id;
  const note = document.getElementById('keepNote');
  note.value = '';
  document.getElementById('keepMsg').textContent = '';
  dlgKeep.showModal();
  setTimeout(() => note.focus(), 50);
}

async function confirmKeep() {
  if (!keepTargetID) { dlgKeep.close(); return; }
  const msg = document.getElementById('keepMsg');
  const note = document.getElementById('keepNote').value.trim();
  try {
    await api(`/api/spaces/${current}/keep`, {
      method: 'POST',
      body: JSON.stringify({ target: keepTargetID, note }),
    });
    dlgKeep.close();
    keepTargetID = null;
    refreshSpace();
    if (typeof pubView !== 'undefined' && pubView === 'shelf') refreshShelf();
  } catch (e) {
    msg.textContent = e.message || 'keep failed';
  }
}

async function refreshShelf() {
  if (!current) return;
  const box = document.getElementById('shelf');
  try {
    const items = await api(`/api/spaces/${current}/shelf`);
    box.innerHTML = '';
    if (!items.length) {
      const e = document.createElement('div');
      e.className = 'empty-spaces';
      e.textContent = 'Nothing kept yet — hover a message and press ✦ keep.';
      box.appendChild(e);
      return;
    }
    const grid = document.createElement('div');
    grid.className = 'shelf-grid';
    for (const it of items) {
      grid.appendChild(RUNTIME_REGISTRY.render('space.kept.v1', it, { space: current }));
    }
    box.appendChild(grid);
  } catch (e) { console.error(e); }
}

// ---- the shelf-item renderer (registered in the runtime registry) ----

function shelfObjectBody(it) {
  if (it.removed) {
    const d = document.createElement('div');
    d.className = 'shelf-removed';
    d.textContent = 'kept moment · original removed';
    return d;
  }
  if (it.publication) {
    const d = document.createElement('div');
    d.className = 'shelf-pub';
    const t = document.createElement('div');
    t.className = 'pub-title'; t.textContent = it.publication.title || 'publication';
    d.appendChild(t);
    const m = document.createElement('div');
    m.className = 'pub-meta';
    m.textContent = 'post · ' + (it.publication.author_name || 'member');
    d.appendChild(m);
    d.style.cursor = 'pointer';
    d.onclick = () => { switchView('posts'); openPub(it.publication.id); };
    return d;
  }
  if (it.object) {
    const d = document.createElement('div');
    d.className = 'shelf-pub';
    const t = document.createElement('div');
    t.className = 'pub-title'; t.textContent = it.object.name || 'object';
    d.appendChild(t);
    const m = document.createElement('div');
    m.className = 'pub-meta';
    m.textContent = (it.object.kind || 'object') +
      (it.object.status ? ' · ' + it.object.status : '');
    d.appendChild(m);
    d.style.cursor = 'pointer';
    d.onclick = () => { switchView('objects'); openObject(it.object.object_id); };
    return d;
  }
  if (it.app) {
    const d = document.createElement('div');
    d.className = 'shelf-app';
    d.textContent = '⚙ ' + (it.app.title || it.app.app_id || 'app');
    return d;
  }
  if (it.entry) {
    // Reuse the feed renderers — same trusted DOM builders as the chat.
    const wrap = document.createElement('div');
    wrap.className = 'shelf-entry';
    const fn = FEED_RENDERERS[it.entry.kind];
    wrap.appendChild(fn ? fn(it.entry) : textNode('txt', it.entry.fallback || it.kind));
    wrap.appendChild(renderResonanceRow(it.entry.resonance, it.target,
      () => { refreshShelf(); refreshSpace(); }));
    return wrap;
  }
  return textNode('txt', it.kind || 'kept object');
}

function renderShelfItem(it) {
  const card = document.createElement('div');
  card.className = 'shelf-card';
  card.appendChild(shelfObjectBody(it));

  const keepers = document.createElement('div');
  keepers.className = 'shelf-keepers';
  for (const k of it.keepers || []) {
    const row = document.createElement('div');
    row.className = 'shelf-keeper';
    const who = document.createElement('span');
    who.className = 'shelf-keeper-name';
    who.textContent = '✦ ' + (k.mine ? 'you' : (k.author_name || 'member'));
    row.appendChild(who);
    if (k.note) {
      const note = document.createElement('span');
      note.className = 'shelf-note';
      note.textContent = k.note;
      row.appendChild(note);
    }
    if (k.mine) {
      const un = document.createElement('button');
      un.className = 'btn-plain shelf-unkeep';
      un.textContent = 'un-keep';
      un.onclick = async (ev) => {
        ev.stopPropagation();
        try {
          await api(`/api/spaces/${current}/unkeep`, {
            method: 'POST',
            body: JSON.stringify({ target: it.target }),
          });
          refreshShelf();
          refreshSpace();
        } catch (e) { console.error(e); }
      };
      row.appendChild(un);
    }
    keepers.appendChild(row);
  }
  card.appendChild(keepers);
  return card;
}

RUNTIME_REGISTRY.register('space.kept.v1', renderShelfItem);
