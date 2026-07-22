// Publications client (PUB-1): the Posts projection of a space — feed cards,
// full article with threaded comments + reactions on the STABLE document
// target, a block composer (up/down reorder), and an AI draft that only fills
// the editor. All user strings are set via textContent; unknown blocks render
// the honest fallback node.

let pubView = 'chat';        // chat | posts (per open space)
let openDocID = null;        // article currently open
let composerDoc = null;      // documentJSON being edited
let composerBase = '';       // base_revision_event_id when editing

function randHex16() {
  const b = new Uint8Array(16);
  crypto.getRandomValues(b);
  return [...b].map(x => x.toString(16).padStart(2, '0')).join('');
}

function switchView(v) {
  pubView = v;
  document.querySelectorAll('#viewSwitch button').forEach(b =>
    b.classList.toggle('sel', b.dataset.v === v));
  const posts = v === 'posts', shelf = v === 'shelf';
  const chat = !posts && !shelf;
  document.getElementById('log').style.display = chat ? '' : 'none';
  document.getElementById('composer').style.display = chat ? '' : 'none';
  document.getElementById('cards').style.display = chat ? '' : 'none';
  document.getElementById('posts').style.display = posts ? '' : 'none';
  document.getElementById('shelf').style.display = shelf ? '' : 'none';
  if (posts) refreshPosts();
  if (shelf) refreshShelf();
}

async function refreshPosts() {
  if (!current) return;
  const feed = document.getElementById('pubFeed');
  try {
    const r = await api(`/api/spaces/${current}/publications`);
    feed.innerHTML = '';
    if (!r.publications.length) {
      const e = document.createElement('div');
      e.className = 'empty-spaces';
      e.textContent = 'No posts yet — write the first one.';
      feed.appendChild(e);
    }
    for (const p of r.publications) {
      const card = document.createElement('div');
      card.className = 'pub-card';
      card.setAttribute('role', 'button');
      card.tabIndex = 0;
      if (p.cover) {
        const cov = document.createElement('div');
        cov.className = 'pub-card-cover';
        cov.style.backgroundImage =
          `url(/api/spaces/${current}/assets/${p.cover}?token=${token})`;
        card.appendChild(cov);
      }
      const t = document.createElement('div');
      t.className = 'pub-title'; t.textContent = p.title;
      card.appendChild(t);
      if (p.summary) {
        const s = document.createElement('div');
        s.className = 'pub-summary'; s.textContent = p.summary;
        card.appendChild(s);
      }
      const m = document.createElement('div');
      m.className = 'pub-meta';
      m.textContent = `${p.kind} · ${p.comment_count} ${p.comment_count === 1 ? 'comment' : 'comments'}`;
      card.appendChild(m);
      card.onclick = () => openPub(p.document_id);
      card.onkeydown = (e) => { if (e.key === 'Enter') openPub(p.document_id); };
      feed.appendChild(card);
    }
  } catch (e) { console.error(e); }
  document.getElementById('pubArticle').style.display = 'none';
  document.getElementById('pubFeed').style.display = '';
}

// ---- article view ----

async function openPub(docID) {
  openDocID = docID;
  try {
    const p = await api(`/api/spaces/${current}/publications/${docID}`);
    renderArticle(p);
  } catch (e) { console.error(e); }
}

function renderArticle(p) {
  const box = document.getElementById('pubArticle');
  box.innerHTML = '';
  const doc = p.document;

  const bar = document.createElement('div');
  bar.className = 'row';
  const back = document.createElement('button');
  back.className = 'btn-plain'; back.textContent = '← Posts';
  back.onclick = () => refreshPosts();
  bar.appendChild(back);
  const edit = document.createElement('button');
  edit.className = 'btn-plain'; edit.textContent = 'Edit';
  edit.onclick = () => openComposer(doc, p.revision_event_id);
  bar.appendChild(edit);
  box.appendChild(bar);

  if (doc.cover) {
    const cov = document.createElement('img');
    cov.className = 'pub-hero';
    cov.alt = '';
    cov.src = `/api/spaces/${current}/assets/${doc.cover}?token=${token}`;
    box.appendChild(cov);
  }
  const h = document.createElement('h2');
  h.className = 'pub-h1'; h.textContent = doc.title;
  box.appendChild(h);
  const meta = document.createElement('div');
  meta.className = 'pub-meta';
  meta.textContent = `${doc.kind}` + (doc.tags?.length ? ' · ' + doc.tags.join(', ') : '');
  box.appendChild(meta);
  if (doc.summary) {
    const s = document.createElement('p');
    s.className = 'pub-summary'; s.textContent = doc.summary;
    box.appendChild(s);
  }
  for (const b of doc.blocks || []) box.appendChild(renderPubBlock(b));

  // Reactions on the stable target (survive revisions).
  const rrow = document.createElement('div');
  rrow.className = 'reactrow';
  const emojis = new Set(['👍', '🔥', '🌿', ...Object.keys(p.reactions || {})]);
  for (const em of emojis) {
    const chip = document.createElement('span');
    chip.className = 'react' + (p.my_reactions?.[em] ? ' mine' : '');
    const n = p.reactions?.[em] || 0;
    chip.textContent = n > 0 ? `${em} ${n}` : em;
    chip.onclick = async () => {
      await api(`/api/spaces/${current}/reactions`, { method: 'POST',
        body: JSON.stringify({ target: p.reaction_target, emoji: em, active: !p.my_reactions?.[em] }) });
      openPub(openDocID);
    };
    rrow.appendChild(chip);
  }
  box.appendChild(rrow);

  // Comments (thread-shaped model; flat render in v1).
  const ch = document.createElement('h3');
  ch.textContent = 'Comments';
  box.appendChild(ch);
  for (const c of p.comments || []) {
    const row = document.createElement('div');
    row.className = 'pub-comment';
    const who = document.createElement('span');
    who.className = 'pub-comment-author';
    who.textContent = PROTOCOL ? c.author : t('conv.member');
    row.appendChild(who);
    const txt = document.createElement('span');
    txt.textContent = ' ' + c.text;
    row.appendChild(txt);
    box.appendChild(row);
  }
  const crow = document.createElement('div');
  crow.className = 'row';
  const inp = document.createElement('input');
  inp.placeholder = 'write a comment…';
  crow.appendChild(inp);
  const send = document.createElement('button');
  send.className = 'btn-tinted'; send.textContent = 'Send';
  send.onclick = async () => {
    if (!inp.value.trim()) return;
    await api(`/api/spaces/${current}/publications/${openDocID}/comments`,
      { method: 'POST', body: JSON.stringify({ text: inp.value.trim() }) });
    openPub(openDocID);
  };
  crow.appendChild(send);
  box.appendChild(crow);

  document.getElementById('pubFeed').style.display = 'none';
  box.style.display = '';
}

// renderPubBlock builds DOM for one block; unknown types degrade honestly.
function renderPubBlock(b) {
  const p = b.props || {};
  const el = document.createElement('div');
  el.className = 'pub-block pub-' + b.type.replace(/[^a-z-]/g, '');
  switch (b.type) {
    case 'heading': {
      const h = document.createElement('h3');
      h.textContent = p.text || ''; el.appendChild(h); break;
    }
    case 'text': {
      const t = document.createElement('p');
      t.className = 'txt'; t.textContent = p.text || ''; el.appendChild(t); break;
    }
    case 'quote': {
      const q = document.createElement('blockquote');
      q.textContent = p.text || '';
      if (p.extra) {
        const cite = document.createElement('cite');
        cite.textContent = '— ' + p.extra; q.appendChild(cite);
      }
      el.appendChild(q); break;
    }
    case 'callout': {
      const c = document.createElement('div');
      c.className = 'pub-callout'; c.textContent = p.text || '';
      el.appendChild(c); break;
    }
    case 'code': {
      const pre = document.createElement('pre');
      pre.textContent = p.text || ''; el.appendChild(pre); break;
    }
    case 'link': {
      const a = document.createElement('a');
      a.href = p.text || '#'; a.target = '_blank'; a.rel = 'noopener noreferrer';
      a.textContent = p.more || p.text || 'link'; el.appendChild(a); break;
    }
    case 'separator': {
      el.appendChild(document.createElement('hr')); break;
    }
    case 'credits': {
      const table = document.createElement('div');
      table.className = 'pub-credits-table';
      const items = p.items || [];
      for (let i = 0; i + 1 < items.length; i += 2) {
        const row = document.createElement('div');
        const role = document.createElement('span');
        role.className = 'pub-credit-role'; role.textContent = items[i];
        const name = document.createElement('span');
        name.textContent = items[i + 1];
        row.appendChild(role); row.appendChild(name);
        table.appendChild(row);
      }
      el.appendChild(table); break;
    }
    case 'image': case 'audio': case 'file': {
      // Media via the existing lazy asset path.
      el.appendChild(pubAssetNode(b.type, p)); break;
    }
    case 'video-link': {
      const a = document.createElement('a');
      a.href = p.asset || '#'; a.target = '_blank'; a.rel = 'noopener noreferrer';
      a.textContent = '▶ ' + (p.text || 'video'); el.appendChild(a); break;
    }
    case 'section': case 'stack': case 'columns': case 'hero': {
      if (b.type === 'section' && p.text) {
        const h = document.createElement('h3'); h.textContent = p.text;
        el.appendChild(h);
      }
      for (const c of b.children || []) el.appendChild(renderPubBlock(c));
      break;
    }
    case 'app': {
      // APP-1: the block references an instance; the runtime renders it.
      const holder = document.createElement('div');
      holder.className = 'unknownblk';
      holder.textContent = 'Loading interactive block…';
      el.appendChild(holder);
      if (p.asset && typeof renderAppInstance === 'function') {
        renderAppInstance(p.asset).then(node => holder.replaceWith(node));
      } else {
        holder.textContent = 'Interactive block unavailable.';
      }
      break;
    }
    default: {
      const f = document.createElement('div');
      f.className = 'unknownblk';
      f.textContent = 'Unsupported content';
      if (typeof PROTOCOL !== 'undefined' && PROTOCOL) f.textContent += ': ' + b.type;
      el.appendChild(f);
    }
  }
  return el;
}

function pubAssetNode(kind, p) {
  const wrap = document.createElement('div');
  if (kind === 'audio' && p.asset) {
    const au = document.createElement('audio');
    au.controls = true;
    au.src = `/api/spaces/${current}/assets/${p.asset}?token=${token}`;
    wrap.appendChild(au);
  } else if (kind === 'image' && p.asset) {
    const img = document.createElement('img');
    img.alt = p.text || '';
    const src = `/api/spaces/${current}/assets/${p.asset}?token=${token}`;
    img.src = src;
    img.onclick = () => window.open(src, '_blank'); // zoom: original full-size
    wrap.appendChild(img);
  } else {
    const a = document.createElement('a');
    a.href = p.asset ? `/api/spaces/${current}/assets/${p.asset}?token=${token}` : '#';
    a.textContent = '📄 ' + (p.text || 'file');
    wrap.appendChild(a);
  }
  if (p.caption) {
    const c = document.createElement('div');
    c.className = 'pub-meta'; c.textContent = p.caption;
    wrap.appendChild(c);
  }
  return wrap;
}

// ---- composer ----
// Notion-flavored: one-click chips add blocks, media blocks are drop zones
// with live previews (upload → block.attached.v1 carrier → asset id), text
// areas auto-grow, up/down/remove per block.

const COMPOSER_CHIPS = [
  { t: 'text', label: 'Text' }, { t: 'heading', label: 'Heading' },
  { t: 'image', label: '🖼 Image' }, { t: 'audio', label: '♫ Audio' },
  { t: 'file', label: '📄 File' }, { t: 'quote', label: '❝ Quote' },
  { t: 'callout', label: 'Callout' }, { t: 'code', label: 'Code' },
  { t: 'link', label: 'Link' }, { t: 'credits', label: 'Credits' },
  { t: 'separator', label: '—' },
  { t: 'poll', label: '📊 Poll', app: true }, { t: 'form', label: '📝 Form', app: true },
  { t: 'listening', label: '🎧 Listening room', app: true },
];

function renderComposerChips() {
  const box = document.getElementById('compChips');
  box.innerHTML = '';
  for (const c of COMPOSER_CHIPS) {
    const chip = document.createElement('button');
    chip.className = 'comp-chip';
    chip.textContent = '+ ' + c.label;
    chip.onclick = () => {
      if (c.app) { insertAppBlock(c.t); return; }
      composerDoc.blocks.push({ id: 'b' + randHex16().slice(0, 8), type: c.t, props: {} });
      renderComposerBlocks();
      // Focus the freshly added block's first field.
      setTimeout(() => {
        const rows = document.querySelectorAll('#composerBlocks .comp-block');
        rows[rows.length - 1]?.querySelector('textarea, input')?.focus();
      }, 30);
    };
    box.appendChild(chip);
  }
}

function openComposer(doc, baseRevision) {
  composerDoc = doc ? JSON.parse(JSON.stringify(doc)) : {
    document_id: randHex16(), kind: 'article', title: '', summary: '',
    visibility_intent: 'space', blocks: [],
  };
  composerBase = baseRevision || '';
  document.getElementById('compTitle').value = composerDoc.title || '';
  document.getElementById('compSummary').value = composerDoc.summary || '';
  document.getElementById('compKind').value = composerDoc.kind || 'article';
  document.getElementById('compMsg').textContent = '';
  renderCompCover();
  renderComposerChips();
  renderComposerBlocks();
  dlgComposer.showModal();
}

// ---- cover: a slim drop strip above the title ----
function renderCompCover() {
  const box = document.getElementById('compCover');
  box.innerHTML = '';
  const zone = document.createElement('div');
  zone.className = 'comp-cover' + (composerDoc.cover ? ' has' : '');
  const hint = document.createElement('span');
  hint.className = 'comp-drop-hint';
  if (composerDoc.cover) {
    zone.style.backgroundImage =
      `url(/api/spaces/${current}/assets/${composerDoc.cover}?token=${token})`;
    hint.textContent = 'cover · click to replace';
    const rm = document.createElement('button');
    rm.className = 'icon-btn comp-cover-rm'; rm.textContent = '✕';
    rm.title = 'Remove cover';
    rm.onclick = (e) => { e.stopPropagation(); composerDoc.cover = ''; renderCompCover(); };
    zone.appendChild(rm);
  } else {
    hint.textContent = 'Drop a cover image here — or click to browse';
  }
  zone.appendChild(hint);
  const fileInput = document.createElement('input');
  fileInput.type = 'file'; fileInput.accept = 'image/*'; fileInput.style.display = 'none';
  zone.appendChild(fileInput);
  async function takeCover(file) {
    if (!file || !file.type.startsWith('image/')) return;
    hint.textContent = 'Uploading…';
    try {
      const up = await uploadAssetFile(file);
      composerDoc.cover = up.asset_id;
      renderCompCover();
    } catch (err) { hint.textContent = '✗ ' + err.message; }
  }
  zone.onclick = () => fileInput.click();
  fileInput.onchange = () => takeCover(fileInput.files[0]);
  zone.ondragover = (e) => { e.preventDefault(); zone.classList.add('over'); };
  zone.ondragleave = () => zone.classList.remove('over');
  zone.ondrop = (e) => { e.preventDefault(); zone.classList.remove('over');
    takeCover(e.dataTransfer.files[0]); };
  box.appendChild(zone);
}

// uploadAssetFile sends one file to the space's asset store; the node emits a
// block.attached.v1 carrier so every replica can index + decrypt it.
async function uploadAssetFile(file) {
  const fd = new FormData();
  fd.append('metadata', new Blob([JSON.stringify({
    filename: file.name, media_type: file.type || 'application/octet-stream',
    size: file.size,
  })], { type: 'application/json' }));
  fd.append('file', file);
  const r = await fetch(`/api/spaces/${current}/assets`, {
    method: 'POST', headers: { 'X-QP-Token': token }, body: fd,
  });
  const j = await r.json();
  if (!r.ok) throw new Error(j.error || 'upload failed');
  return j; // { asset_id, media_type, size, filename }
}

// mediaAccept maps a media block type to its file-picker accept filter.
function mediaAccept(type) {
  return type === 'image' ? 'image/*' : type === 'audio' ? 'audio/*' : '';
}

// mediaDropZone builds the drop/browse surface with a live preview.
function mediaDropZone(b) {
  const zone = document.createElement('div');
  zone.className = 'comp-drop';
  const preview = document.createElement('div');
  preview.className = 'comp-drop-preview';
  const hint = document.createElement('div');
  hint.className = 'comp-drop-hint';
  const fileInput = document.createElement('input');
  fileInput.type = 'file';
  fileInput.style.display = 'none';
  const acc = mediaAccept(b.type);
  if (acc) fileInput.accept = acc;

  function showPreview() {
    preview.innerHTML = '';
    if (!b.props.asset) {
      hint.textContent = `Drop ${b.type === 'image' ? 'an image' : b.type === 'audio' ? 'an audio file' : 'a file'} here — or click to browse`;
      return;
    }
    hint.textContent = (b.props.text || 'uploaded') + ' · click to replace';
    if (b.type === 'image') {
      const img = document.createElement('img');
      img.alt = b.props.text || '';
      img.src = `/api/spaces/${current}/assets/${b.props.asset}?token=${token}`;
      preview.appendChild(img);
    } else if (b.type === 'audio') {
      const au = document.createElement('audio');
      au.controls = true;
      au.src = `/api/spaces/${current}/assets/${b.props.asset}?token=${token}`;
      preview.appendChild(au);
    } else {
      const f = document.createElement('div');
      f.className = 'pub-meta';
      f.textContent = '📄 ' + (b.props.text || 'file');
      preview.appendChild(f);
    }
  }

  async function takeFile(file) {
    if (!file) return;
    hint.textContent = 'Uploading…';
    zone.classList.add('busy');
    try {
      const up = await uploadAssetFile(file);
      b.props.asset = up.asset_id;
      if (!b.props.text) b.props.text = up.filename;
      showPreview();
    } catch (err) {
      hint.textContent = '✗ ' + err.message;
    } finally { zone.classList.remove('busy'); }
  }

  zone.onclick = () => fileInput.click();
  fileInput.onchange = () => takeFile(fileInput.files[0]);
  zone.ondragover = (e) => { e.preventDefault(); zone.classList.add('over'); };
  zone.ondragleave = () => zone.classList.remove('over');
  zone.ondrop = (e) => {
    e.preventDefault(); zone.classList.remove('over');
    takeFile(e.dataTransfer.files[0]);
  };

  zone.appendChild(preview);
  zone.appendChild(hint);
  zone.appendChild(fileInput);
  showPreview();
  return zone;
}

// autoGrow keeps a textarea sized to its content.
function autoGrow(ta) {
  const fit = () => { ta.style.height = 'auto'; ta.style.height = (ta.scrollHeight + 2) + 'px'; };
  ta.addEventListener('input', fit);
  setTimeout(fit, 0);
  return ta;
}

const BLOCK_PLACEHOLDERS = {
  text: 'Write…', heading: 'Heading', quote: 'A quote…',
  callout: 'Something worth highlighting…', code: '// code', link: 'https://…',
};

function renderComposerBlocks() {
  const box = document.getElementById('composerBlocks');
  box.innerHTML = '';
  if (!(composerDoc.blocks || []).length) {
    const empty = document.createElement('div');
    empty.className = 'comp-empty';
    empty.textContent = 'Add blocks below — text, media, a poll…';
    box.appendChild(empty);
  }
  (composerDoc.blocks || []).forEach((b, i) => {
    const row = document.createElement('div');
    row.className = 'comp-block';
    row.dataset.index = i;
    b.props = b.props || {};

    // Drag-to-reorder: the ⠿ handle arms draggability so text selection in
    // the editors keeps working; rows re-render on drop.
    const grip = document.createElement('span');
    grip.className = 'comp-grip'; grip.textContent = '⠿'; grip.title = 'Drag to reorder';
    grip.onmousedown = () => { row.draggable = true; };
    row.addEventListener('dragend', () => { row.draggable = false;
      document.querySelectorAll('.comp-block.drop-above').forEach(x => x.classList.remove('drop-above')); });
    row.addEventListener('dragstart', (e) => {
      e.dataTransfer.setData('text/qp-block', String(i));
      e.dataTransfer.effectAllowed = 'move';
    });
    row.addEventListener('dragover', (e) => {
      if (![...e.dataTransfer.types].includes('text/qp-block')) return;
      e.preventDefault();
      row.classList.add('drop-above');
    });
    row.addEventListener('dragleave', () => row.classList.remove('drop-above'));
    row.addEventListener('drop', (e) => {
      const from = parseInt(e.dataTransfer.getData('text/qp-block'), 10);
      if (Number.isNaN(from)) return;
      e.preventDefault();
      const to = i;
      if (from === to) { row.classList.remove('drop-above'); return; }
      const [moved] = composerDoc.blocks.splice(from, 1);
      composerDoc.blocks.splice(to > from ? to - 1 : to, 0, moved);
      renderComposerBlocks();
    });
    row.appendChild(grip);

    // Controls appear on hover; the type label stays quiet.
    const head = document.createElement('div');
    head.className = 'comp-block-head';
    const label = document.createElement('span');
    label.className = 'comp-block-type'; label.textContent = b.type;
    head.appendChild(label);
    const mk = (txt, title, fn, disabled) => {
      const btn = document.createElement('button');
      btn.className = 'icon-btn comp-ctl'; btn.textContent = txt; btn.title = title;
      btn.disabled = !!disabled; btn.onclick = fn;
      return btn;
    };
    head.appendChild(mk('↑', 'Move up', () => { const t2 = composerDoc.blocks[i - 1];
      composerDoc.blocks[i - 1] = b; composerDoc.blocks[i] = t2; renderComposerBlocks(); }, i === 0));
    head.appendChild(mk('↓', 'Move down', () => { const t2 = composerDoc.blocks[i + 1];
      composerDoc.blocks[i + 1] = b; composerDoc.blocks[i] = t2; renderComposerBlocks(); },
      i === composerDoc.blocks.length - 1));
    head.appendChild(mk('✕', 'Remove', () => { composerDoc.blocks.splice(i, 1); renderComposerBlocks(); }));
    row.appendChild(head);

    if (b.type === 'separator') {
      row.appendChild(document.createElement('hr'));
    } else if (b.type === 'credits') {
      const inp = document.createElement('input');
      inp.placeholder = 'role, name, role, name…';
      inp.value = (b.props.items || []).join(', ');
      inp.oninput = () => { b.props.items = inp.value.split(',').map(s => s.trim()).filter(Boolean); };
      row.appendChild(inp);
    } else if (['image', 'audio', 'file'].includes(b.type)) {
      row.appendChild(mediaDropZone(b));
      const alt = document.createElement('input');
      alt.placeholder = b.type === 'image' ? 'alt text — what is in the image' : 'title';
      alt.value = b.props.text || '';
      alt.oninput = () => { b.props.text = alt.value; };
      row.appendChild(alt);
    } else if (b.type === 'app') {
      const note = document.createElement('div');
      note.className = 'pub-meta';
      note.textContent = '📊 interactive block · ' + (b.props.asset || '').slice(0, 8);
      row.appendChild(note);
    } else {
      const ta = document.createElement('textarea');
      ta.rows = 1;
      ta.className = b.type === 'heading' ? 'comp-ta-heading' : b.type === 'code' ? 'comp-ta-code' : '';
      ta.placeholder = BLOCK_PLACEHOLDERS[b.type] || b.type;
      ta.value = b.props.text || '';
      ta.oninput = () => { b.props.text = ta.value; };
      row.appendChild(autoGrow(ta));
      if (['quote', 'callout', 'code'].includes(b.type)) {
        const extra = document.createElement('input');
        extra.className = 'comp-extra';
        extra.placeholder = b.type === 'quote' ? 'attribution' : b.type === 'code' ? 'language' : 'tone (info | warning)';
        extra.value = b.props.extra || '';
        extra.oninput = () => { b.props.extra = extra.value; };
        row.appendChild(extra);
      }
      if (b.type === 'link') {
        const more = document.createElement('input');
        more.className = 'comp-extra';
        more.placeholder = 'link title';
        more.value = b.props.more || '';
        more.oninput = () => { b.props.more = more.value; };
        row.appendChild(more);
      }
    }
    box.appendChild(row);
  });
}

function collectComposerDoc() {
  composerDoc.title = document.getElementById('compTitle').value.trim();
  composerDoc.summary = document.getElementById('compSummary').value.trim();
  composerDoc.kind = document.getElementById('compKind').value;
  return composerDoc;
}

async function saveComposerDraft() {
  const doc = collectComposerDoc();
  const msg = document.getElementById('compMsg');
  try {
    await api(`/api/spaces/${current}/drafts`, { method: 'POST',
      body: JSON.stringify({ document_id: doc.document_id, document: doc }) });
    msg.textContent = 'Draft saved on this device.'; msg.className = 'hint';
  } catch (err) { msg.textContent = err.message; msg.className = 'hint warn'; }
}

async function publishComposer() {
  const doc = collectComposerDoc();
  const msg = document.getElementById('compMsg');
  try {
    const r = await fetch(`/api/spaces/${current}/publications`, {
      method: 'POST',
      headers: { 'X-QP-Token': token, 'Content-Type': 'application/json' },
      body: JSON.stringify({ document: doc, base_revision_event_id: composerBase }),
    });
    const j = await r.json();
    if (r.status === 409) {
      msg.textContent = 'The post changed elsewhere — your draft is safe. Reload the post and merge by hand.';
      msg.className = 'hint warn';
      await saveComposerDraft();
      return;
    }
    if (!r.ok) { msg.textContent = j.error || 'publish failed'; msg.className = 'hint warn'; return; }
    dlgComposer.close();
    switchView('posts');
  } catch (err) { msg.textContent = err.message; msg.className = 'hint warn'; }
}

async function aiDraftPost() {
  const prompt = document.getElementById('compAIPrompt').value.trim();
  const msg = document.getElementById('compMsg');
  if (!prompt) return;
  msg.textContent = 'Drafting…'; msg.className = 'hint';
  try {
    const r = await api(`/api/spaces/${current}/publications/proposals`,
      { method: 'POST', body: JSON.stringify({ prompt }) });
    if (!r.ok) { msg.textContent = r.error; msg.className = 'hint warn'; return; }
    // The proposal fills the editor; the user edits and publishes.
    const keep = composerDoc.document_id;
    composerDoc = r.document;
    composerDoc.document_id = keep;
    document.getElementById('compTitle').value = composerDoc.title || '';
    document.getElementById('compSummary').value = composerDoc.summary || '';
    document.getElementById('compKind').value = composerDoc.kind || 'article';
    renderComposerBlocks();
    msg.textContent = 'Draft filled from the proposal — edit freely, then publish.';
  } catch (err) { msg.textContent = err.message; msg.className = 'hint warn'; }
}
