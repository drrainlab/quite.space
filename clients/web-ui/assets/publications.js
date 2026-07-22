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
  const posts = v === 'posts';
  document.getElementById('log').style.display = posts ? 'none' : '';
  document.getElementById('composer').style.display = posts ? 'none' : '';
  document.getElementById('cards').style.display = posts ? 'none' : '';
  document.getElementById('posts').style.display = posts ? '' : 'none';
  if (posts) refreshPosts();
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
      // Reserved: renders fallback until APP-1 wires instances.
      const f = document.createElement('div');
      f.className = 'unknownblk';
      f.textContent = 'Interactive block (arrives with apps)';
      el.appendChild(f); break;
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
    img.className = 'thumb'; img.alt = p.text || '';
    img.src = `/api/spaces/${current}/assets/${p.asset}?token=${token}`;
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

const COMPOSER_TYPES = ['heading', 'text', 'quote', 'callout', 'code', 'link',
  'separator', 'credits', 'image', 'audio', 'file'];

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
  renderComposerBlocks();
  dlgComposer.showModal();
}

function renderComposerBlocks() {
  const box = document.getElementById('composerBlocks');
  box.innerHTML = '';
  (composerDoc.blocks || []).forEach((b, i) => {
    const row = document.createElement('div');
    row.className = 'comp-block';
    const head = document.createElement('div');
    head.className = 'comp-block-head';
    const label = document.createElement('span');
    label.className = 'pub-meta'; label.textContent = b.type;
    head.appendChild(label);
    const up = document.createElement('button');
    up.className = 'icon-btn'; up.textContent = '↑'; up.title = 'Move up';
    up.onclick = () => { if (i > 0) { const t2 = composerDoc.blocks[i - 1];
      composerDoc.blocks[i - 1] = b; composerDoc.blocks[i] = t2; renderComposerBlocks(); } };
    const down = document.createElement('button');
    down.className = 'icon-btn'; down.textContent = '↓'; down.title = 'Move down';
    down.onclick = () => { if (i < composerDoc.blocks.length - 1) {
      const t2 = composerDoc.blocks[i + 1];
      composerDoc.blocks[i + 1] = b; composerDoc.blocks[i] = t2; renderComposerBlocks(); } };
    const del = document.createElement('button');
    del.className = 'icon-btn'; del.textContent = '✕'; del.title = 'Remove';
    del.onclick = () => { composerDoc.blocks.splice(i, 1); renderComposerBlocks(); };
    head.appendChild(up); head.appendChild(down); head.appendChild(del);
    row.appendChild(head);

    b.props = b.props || {};
    if (b.type === 'separator') {
      // no fields
    } else if (b.type === 'credits') {
      row.appendChild(compInput('role, name, role, name…',
        (b.props.items || []).join(', '),
        v => { b.props.items = v.split(',').map(s => s.trim()).filter(Boolean); }));
    } else if (['image', 'audio', 'file'].includes(b.type)) {
      row.appendChild(compInput('asset id (hex — from a shared file in chat)',
        b.props.asset || '', v => { b.props.asset = v.trim(); }));
      row.appendChild(compInput('alt / filename', b.props.text || '',
        v => { b.props.text = v; }));
    } else {
      const ta = document.createElement('textarea');
      ta.rows = b.type === 'text' || b.type === 'code' ? 3 : 1;
      ta.placeholder = b.type === 'link' ? 'https://…' : b.type + ' text';
      ta.value = b.props.text || '';
      ta.oninput = () => { b.props.text = ta.value; };
      row.appendChild(ta);
      if (['quote', 'callout', 'code'].includes(b.type)) {
        row.appendChild(compInput(
          b.type === 'quote' ? 'attribution' : b.type === 'code' ? 'language' : 'tone',
          b.props.extra || '', v => { b.props.extra = v; }));
      }
      if (b.type === 'link') {
        row.appendChild(compInput('link title', b.props.more || '',
          v => { b.props.more = v; }));
      }
    }
    box.appendChild(row);
  });
}

function compInput(placeholder, value, oninput) {
  const inp = document.createElement('input');
  inp.placeholder = placeholder; inp.value = value;
  inp.oninput = () => oninput(inp.value);
  return inp;
}

function addComposerBlock() {
  const type = document.getElementById('compAddType').value;
  composerDoc.blocks.push({ id: 'b' + randHex16().slice(0, 8), type, props: {} });
  renderComposerBlocks();
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
