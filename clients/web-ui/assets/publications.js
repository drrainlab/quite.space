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

// pubNav is a navigation token: refreshPosts (the LIST) and openPub (an
// ARTICLE) both bump it, and refreshPosts only shows the list at the end
// if nothing navigated past it meanwhile. Without this, a refresh started
// before openPub finished by force-hiding the article it had just drawn —
// found live: opening a shared post in a held space landed on the feed.
let pubNav = 0;

async function refreshPosts() {
  if (!current) return;
  const nav = ++pubNav;
  // Leaving the article takes its atmosphere with it — the scene and the bed
  // both. renderArticle() wipes the container, but a canvas removed from the
  // DOM keeps its rAF loop and an <audio> removed from the DOM keeps playing,
  // so the teardown has to be asked for rather than assumed.
  if (typeof ATMO !== 'undefined') ATMO.unmount();
  const feed = document.getElementById('pubFeed');
  // Cards may hold disposers (a still-frame observer each) registered under
  // the registry.js __unmount contract. innerHTML='' would drop the nodes
  // but leave every IntersectionObserver watching a detached element.
  feed.querySelectorAll('.pub-card').forEach(c => {
    try { if (c.__unmount) c.__unmount(); } catch { /* not this card's day */ }
  });
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
        autoMediaBg(cov, p.cover); // fetch bytes on view (media on-demand)
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
      // A post in the feed stays an ordinary readable post: NO live loops,
      // ever. What an atmospheric card gets is a deterministic still as its
      // background (drawn once, only near the viewport, disposed when far —
      // see ATMO.cardStill) and two doors that carry the chosen mode into
      // the article. The card itself opens still and silent.
      if (typeof ATMO !== 'undefined') {
        const mk = ATMO.marker(p.atmosphere);
        if (mk) m.appendChild(mk);
      }
      card.appendChild(m);
      if (typeof ATMO !== 'undefined' && p.atmosphere) {
        ATMO.cardStill(card, p.atmosphere);
        // A door has to open onto something. A KNOWN SCENE has motion to
        // enter, so it gets the quiet door; SOUND is a consent, so it gets
        // its own. A sequence has neither a scene nor motion — its pictures
        // change by reading, not by entering — so it shows a door only when
        // there is sound to say yes to. Offering "Open quiet" there would be
        // a door onto nothing, which is the rule the marker already follows.
        const known = typeof SCENES !== 'undefined' && p.atmosphere.visual &&
          SCENES.get(String(p.atmosphere.visual.scene || ''));
        const hasSound = !!(p.atmosphere.audio && p.atmosphere.audio.asset) &&
          ATMO.soundMode() !== 'never';
        if (known || hasSound) {
          const doors = document.createElement('div');
          doors.className = 'atmo-card-doors';
          const door = (long, short, mode, cls) => {
            const b = document.createElement('button');
            b.className = cls;
            b.innerHTML = `<span class="door-long"></span><span class="door-short"></span>`;
            b.firstChild.textContent = long;
            b.lastChild.textContent = short;
            b.onclick = (e) => { e.stopPropagation(); openPub(p.document_id, mode); };
            doors.appendChild(b);
          };
          if (known) door('Open quiet', 'Quiet', 'quiet', 'btn-tinted');
          if (hasSound) door('Open with sound', 'Sound', 'sound', 'btn-filled');
          card.appendChild(doors);
        }
      }
      // PA-1 space-cards: categories render as chips on the card.
      if (p.kind === 'space' && p.tags?.length) {
        const chips = document.createElement('div');
        chips.className = 'card-tags';
        for (const tg of p.tags) {
          const c = document.createElement('span');
          c.className = 'tag-chip'; c.textContent = tg;
          chips.appendChild(c);
        }
        card.appendChild(chips);
      }
      card.onclick = () => openPub(p.document_id);
      card.onkeydown = (e) => { if (e.key === 'Enter') openPub(p.document_id); };
      feed.appendChild(card);
    }
  } catch (e) { console.error(e); }
  if (nav !== pubNav) return; // somebody opened an article while we fetched
  document.getElementById('pubArticle').style.display = 'none';
  document.getElementById('pubFeed').style.display = '';
}

// ---- article view ----

async function openPub(docID, mode) {
  openDocID = docID;
  pubNav++;
  try {
    const p = await api(`/api/spaces/${current}/publications/${docID}`);
    // mode is the entry chosen in the feed: 'quiet' | 'sound' | undefined
    // (a plain click — still and silent). The door click there is the user
    // gesture that satisfies audio autoplay; it must ride along rather than
    // be asked for a second time on arrival.
    renderArticle(p, mode);
  } catch (e) { console.error(e); }
}

function renderArticle(p, mode) {
  const box = document.getElementById('pubArticle');
  // A re-render of the SAME post with the SAME recipe must not stop the
  // atmosphere: reacting to a post refetches it and lands here, and tearing
  // the scene and the bed down over a reaction reads as the post breaking.
  // The live pieces are lifted out, the article is rebuilt, and they go
  // back — nothing stops, nothing restarts, the audio never gaps. Any other
  // case (different post, edited recipe, no atmosphere) tears down as
  // before: innerHTML='' drops nodes but stops nothing, so an explicit
  // unmount must precede the wipe.
  const doc0 = p.document;
  const preserved = (typeof ATMO !== 'undefined' && doc0.atmosphere &&
    ATMO.holds(String(p.document_id || openDocID), doc0.atmosphere))
    ? ATMO.detach() : null;
  if (!preserved && typeof ATMO !== 'undefined') ATMO.unmount();
  box.innerHTML = '';
  const doc = p.document;

  const bar = document.createElement('div');
  bar.className = 'row';
  const back = document.createElement('button');
  back.className = 'btn-plain'; back.textContent = '← Posts';
  back.onclick = () => refreshPosts();
  bar.appendChild(back);
  // A withdrawn post is not an ordinary live article: no Edit, no
  // Forward, and a plain sentence — the promise made at send time holds
  // at read time too (PS-4b).
  if (!p.archived) {
    const sp = currentSpace();
    // Edit only where this device can WRITE: a reader replica draws no
    // affordance that opens a form just to refuse at the end of it.
    // can_write is absent for ordinary private member spaces (undefined
    // !== false), and explicitly false for a public reader.
    if (sp?.can_write !== false) {
      const edit = document.createElement('button');
      edit.className = 'btn-plain'; edit.textContent = 'Edit';
      edit.onclick = () => openComposer(doc, p.revision_event_id);
      bar.appendChild(edit);
    }
    if (sp && sp.character?.memory !== 'private_history') {
      const fwd = document.createElement('button');
      fwd.className = 'btn-plain';
      fwd.textContent = t('share.card.forward');
      fwd.onclick = () => forwardPost(p.document_id || (doc && doc.document_id));
      bar.appendChild(fwd);
    }
  }
  box.appendChild(bar);
  if (p.archived) {
    const note = document.createElement('div');
    note.className = 'prev-archived';
    note.textContent = t('prev.archived');
    box.appendChild(note);
  }

  if (doc.cover) {
    const cov = document.createElement('img');
    cov.className = 'pub-hero';
    cov.alt = '';
    autoMediaSrc(cov, doc.cover); // fetch bytes on view (media on-demand)
    box.appendChild(cov);
  }
  // AM-6: the atmosphere is a LAYER behind the whole article, not a block
  // inside it. What goes into the content flow is only the slim control bar;
  // the stage and scrim are prepended into `box` itself by ATMO.mount at the
  // very bottom of this function — `box` is display:none until the last
  // line, and a host with no layout gives back a zero-size rectangle, so
  // mounting now would size the background canvas to 1x1.
  let atmoBar = null;
  if (doc.atmosphere && typeof ATMO !== 'undefined') {
    if (preserved) {
      // The SAME bar node, so every closure inside the running mount keeps
      // pointing at a live element rather than an orphan.
      atmoBar = preserved.bar;
      box.appendChild(atmoBar);
    } else {
      atmoBar = document.createElement('div');
      atmoBar.className = 'atmo-bar';
      box.appendChild(atmoBar);
    }
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
  // PA-1 space-card: a prominent "Open space" action from the first link
  // block's target (the qs share link).
  if (doc.kind === 'space') {
    const link = spaceCardLink(doc);
    if (link) {
      const row = document.createElement('div');
      row.className = 'row';
      const open = document.createElement('button');
      open.className = 'btn-filled'; open.textContent = 'Open space';
      open.onclick = () => openSpaceCard(link);
      row.appendChild(open);
      // Say only what this node already knows. An unopened card reads "not
      // checked" rather than a green dot we did not earn.
      const st = document.createElement('span');
      st.className = 'card-status';
      const s = spaceCardStatus(link);
      st.textContent = s.text;
      st.title = s.known
        ? 'From what this node already holds — nothing was probed to show it.'
        : 'This node has not opened this space. Opening one is your choice, so nothing is checked in advance.';
      row.appendChild(st);
      box.appendChild(row);
    }
  }
  for (const b of doc.blocks || []) {
    // Space-cards surface their target via the prominent button above;
    // skip the raw link block so it isn't offered twice.
    if (doc.kind === 'space' && b.type === 'link' && (b.props?.text || '').startsWith('qs:')) continue;
    box.appendChild(renderPubBlock(b));
  }

  // Resonance on the STABLE target (survives revisions).
  box.appendChild(renderResonanceRow(p.resonance, p.reaction_target,
    () => openPub(openDocID)));

  // Comments (thread-shaped model; flat render in v1). The list lives in its
  // own container so it can update LIVE without tearing down the listening
  // room or the comment box the reader is typing in.
  const ch = document.createElement('h3');
  ch.className = 'pub-comments-h'; ch.textContent = 'Comments';
  box.appendChild(ch);
  const clist = document.createElement('div');
  clist.className = 'pub-comments'; clist.id = 'pubComments';
  renderCommentList(clist, p.comments || []);
  commentSig = commentsSignature(p.comments); // seed so polling only reacts to change
  box.appendChild(clist);

  const form = document.createElement('form');
  form.className = 'pub-comment-form';
  const inp = document.createElement('input');
  inp.className = 'pub-comment-input';
  inp.placeholder = 'Write a comment…';
  form.appendChild(inp);
  const send = document.createElement('button');
  send.type = 'submit';
  send.className = 'btn-tinted'; send.textContent = 'Send';
  form.appendChild(send);
  form.onsubmit = async (e) => {
    e.preventDefault();
    const text = inp.value.trim();
    if (!text) return;
    inp.value = ''; send.disabled = true;
    try {
      await api(`/api/spaces/${current}/publications/${openDocID}/comments`,
        { method: 'POST', body: JSON.stringify({ text }) });
      await refreshArticleComments();
    } catch (err) { inp.value = text; }
    send.disabled = false; inp.focus();
  };
  box.appendChild(form);

  document.getElementById('pubFeed').style.display = 'none';
  box.style.display = '';
  // Now that the article has layout, the atmosphere can measure its shell.
  if (preserved) {
    ATMO.reattach(box);
  } else if (atmoBar) {
    ATMO.mount(box, doc.atmosphere, String(openDocID),
               { bar: atmoBar, enter: mode || 'still' });
  }
  startCommentPoll(); // receive others' comments live while this article is open
}

// renderCommentList paints the flat comment thread into a container.
function renderCommentList(container, comments) {
  container.innerHTML = '';
  if (!comments || !comments.length) {
    const empty = document.createElement('div');
    empty.className = 'pub-comment-empty';
    empty.textContent = 'No comments yet — be the first.';
    container.appendChild(empty);
    return;
  }
  // Newest first: the list is stored oldest→newest (deterministic), we render
  // it reversed so the latest comment sits at the top.
  for (const c of [...comments].reverse()) {
    const row = document.createElement('div');
    row.className = 'pub-comment' + (c.mine ? ' mine' : '');
    const av = document.createElement('span');
    av.className = 'pub-comment-av glyph g18';
    if (typeof glyphSVG === 'function') av.innerHTML = glyphSVG(c.author || 'x', 'human', 18);
    row.appendChild(av);
    const body = document.createElement('div');
    body.className = 'pub-comment-body';
    const who = document.createElement('span');
    who.className = 'pub-comment-author';
    // Real display name when we have it; the protocol view shows the raw
    // principal; otherwise a neutral fallback.
    who.textContent = c.mine ? t('conv.you')
      : PROTOCOL ? (c.author || '')
      : (c.author_name || t('conv.member'));
    body.appendChild(who);
    const txt = document.createElement('div');
    txt.className = 'pub-comment-text'; txt.textContent = c.text;
    body.appendChild(txt);
    row.appendChild(body);
    container.appendChild(row);
  }
}

let commentSig = '';
let commentTimer = null;
function commentsSignature(comments) {
  return (comments || []).map(c => (c.author || '') + '' + c.text).join('');
}
async function refreshArticleComments() {
  if (!openDocID) return;
  try {
    const p = await api(`/api/spaces/${current}/publications/${openDocID}`);
    const sig = commentsSignature(p.comments);
    const c = document.getElementById('pubComments');
    if (c && sig !== commentSig) { commentSig = sig; renderCommentList(c, p.comments || []); }
  } catch (e) { /* transient — keep the current view */ }
}
function stopCommentPoll() { if (commentTimer) { clearInterval(commentTimer); commentTimer = null; } }
function startCommentPoll() {
  stopCommentPoll();
  commentTimer = setInterval(() => {
    const box = document.getElementById('pubArticle');
    if (!box || box.style.display === 'none' || !openDocID) { stopCommentPoll(); return; }
    refreshArticleComments();
  }, 3500);
}

// renderPubBlock builds DOM for one block; unknown types degrade honestly.
function renderPubBlock(b) {
  const p = b.props || {};
  const el = document.createElement('div');
  el.className = 'pub-block pub-' + b.type.replace(/[^a-z-]/g, '');
  // The block's own id, in the DOM. An atmosphere sequence anchors its
  // stages to block ids (AM-7), and a document position that exists only in
  // the data cannot be found by anything that watches the page.
  if (b.id) el.dataset.blockId = String(b.id);
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
      // PA-1: a "qs:" bearer link opens a space in-app, not a dead href.
      if ((p.text || '').startsWith('qs:')) {
        const b = document.createElement('button');
        b.className = 'btn-plain';
        b.textContent = p.more || 'Open space';
        b.onclick = () => openSpaceCard(p.text);
        el.appendChild(b); break;
      }
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
    case 'image': case 'audio': case 'video': case 'file': {
      // Media via the existing lazy asset path.
      el.appendChild(pubAssetNode(b.type, p)); break;
    }
    case 'gallery': {
      // A grid of lazy images — it used to fall into "Unsupported
      // content", which for a first-class block type was simply a gap.
      const grid = document.createElement('div');
      grid.className = 'pub-gallery';
      for (const it of p.items || []) {
        if (it) grid.appendChild(pubAssetNode('image', { asset: it }));
      }
      el.appendChild(grid); break;
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
    autoMediaSrc(au, p.asset); // fetch bytes on view (media on-demand)
    wrap.appendChild(au);
  } else if (kind === 'video' && p.asset) {
    const v = document.createElement('video');
    v.controls = true;
    v.preload = 'metadata';
    autoMediaSrc(v, p.asset);
    wrap.appendChild(v);
  } else if (kind === 'image' && p.asset) {
    const img = document.createElement('img');
    img.alt = p.text || '';
    autoMediaSrc(img, p.asset); // fetch bytes on view (media on-demand)
    img.onclick = () => window.open(assetURL(p.asset), '_blank'); // zoom: full-size
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
  { t: 'video', label: '🎞 Video' },
  { t: 'file', label: '📄 File' }, { t: 'quote', label: '❝ Quote' },
  { t: 'callout', label: 'Callout' }, { t: 'code', label: 'Code' },
  { t: 'link', label: 'Link' }, { t: 'credits', label: 'Credits' },
  { t: 'separator', label: '—' },
  { t: 'poll', label: '📊 Poll', app: true }, { t: 'form', label: '📝 Form', app: true },
  { t: 'listening', label: '🎧 Listening room', app: true },
  // Not a block on the wire — a stage of the post's atmosphere, edited here
  // because it marks a PLACE, and places live between the blocks. See the
  // note above ATMO_EDIT.stageRow for why it is not a block type.
  { t: 'atmosphere', label: '🖼 Background', stage: true },
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
      if (c.stage) {
        const msg = document.getElementById('compMsg');
        const why = typeof ATMO_EDIT === 'undefined' ? 'atmosphere is unavailable'
          : ATMO_EDIT.addStage(composerDoc, refreshComposerBody);
        if (msg) msg.textContent = why;
        return;
      }
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

// onKindChange: picking "space card" seeds the space-link template so the
// author only has to paste the share link. A catalog is any public
// broadcast space whose posts are these cards — no separate marker.
function onKindChange() {
  const kind = document.getElementById('compKind').value;
  composerDoc.kind = kind;
  if (kind === 'space' && !(composerDoc.blocks || []).some(b => b.type === 'link')) {
    composerDoc.blocks = composerDoc.blocks || [];
    composerDoc.blocks.push({
      id: 'b' + randHex16().slice(0, 8), type: 'link',
      props: { text: '', more: 'Open this space' },
    });
    renderComposerBlocks();
    const msg = document.getElementById('compMsg');
    if (msg) msg.textContent = 'Paste the space’s share link into the link block below.';
  }
}

function openComposer(doc, baseRevision) {
  composerDoc = doc ? JSON.parse(JSON.stringify(doc)) : {
    document_id: randHex16(), kind: 'article', title: '', summary: '',
    visibility_intent: 'space', blocks: [],
  };
  // A post with no blocks serializes "blocks": null, and every chip does
  // composerDoc.blocks.push(...) — which threw, and the whole palette
  // looked dead when EDITING while working fine on a fresh draft.
  composerDoc.blocks = composerDoc.blocks || [];
  composerBase = baseRevision || '';
  document.getElementById('compTitle').value = composerDoc.title || '';
  document.getElementById('compSummary').value = composerDoc.summary || '';
  document.getElementById('compKind').value = composerDoc.kind || 'article';
  document.getElementById('compMsg').textContent = '';
  renderCompCover();
  renderComposerChips();
  renderComposerBlocks();
  renderComposerAtmosphere();
  dlgComposer.showModal();
}

// The atmosphere editor writes straight into composerDoc.atmosphere, in the
// contract's own shape — permille integers and hex strings — so what the
// sliders move is exactly what gets signed. It is re-rendered rather than
// patched because the parameter list depends on which scene is selected.
function renderComposerAtmosphere() {
  const box = document.getElementById('compAtmosphere');
  if (!box || typeof ATMO_EDIT === 'undefined') return;
  // The panel and the block list show two halves of one thing — the recipe
  // and the places it changes — so either one moving redraws both.
  ATMO_EDIT.render(box, composerDoc, renderComposerBlocks);
}

/** Both halves of the composer body: the atmosphere panel and the flow. */
function refreshComposerBody() {
  renderComposerBlocks();
  renderComposerAtmosphere();
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
  // Hand-framed multipart via postMultipart — see the note there: FormData
  // with a Blob arrives EMPTY inside the desktop shell (WKWebView drops
  // blob-backed bodies), and this path is identical for the browser.
  const r = await postMultipart(`/api/spaces/${current}/assets`, [
    { name: 'metadata', type: 'application/json',
      data: JSON.stringify({ filename: file.name,
        media_type: file.type || 'application/octet-stream', size: file.size }) },
    { name: 'file', data: file, filename: file.name },
  ]);
  const j = await r.json();
  if (!r.ok) throw new Error(j.error || 'upload failed');
  return j; // { asset_id, media_type, size, filename }
}

// mediaAccept maps a media block type to its file-picker accept filter.
function mediaAccept(type) {
  return type === 'image' ? 'image/*' : type === 'audio' ? 'audio/*'
    : type === 'video' ? 'video/*' : '';
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
      hint.textContent = `Drop ${b.type === 'image' ? 'an image'
        : b.type === 'audio' ? 'an audio file'
        : b.type === 'video' ? 'a video' : 'a file'} here — or click to browse`;
      return;
    }
    hint.textContent = (b.props.text || 'uploaded') + ' · click to replace';
    if (b.type === 'image') {
      const img = document.createElement('img');
      img.alt = b.props.text || '';
      img.src = `/api/spaces/${current}/assets/${b.props.asset}?token=${token}`;
      preview.appendChild(img);
    } else if (b.type === 'video') {
      const v = document.createElement('video');
      v.controls = true;
      v.src = `/api/spaces/${current}/assets/${b.props.asset}?token=${token}`;
      preview.appendChild(v);
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
    // A background change is anchored to this block, so it is drawn just
    // above it: everything from here down has that picture.
    const stage = typeof ATMO_EDIT !== 'undefined' && b.id
      ? ATMO_EDIT.stageFor(composerDoc, b.id) : null;
    if (stage) box.appendChild(ATMO_EDIT.stageRow(composerDoc, stage, refreshComposerBody));

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
    } else if (['image', 'audio', 'video', 'file'].includes(b.type)) {
      row.appendChild(mediaDropZone(b));
      const alt = document.createElement('input');
      alt.placeholder = b.type === 'image' ? 'alt text — what is in the image'
        : b.type === 'video' ? 'alt text — what happens in the video' : 'title';
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
  // PA-1 space-card: normalize the pasted share link into the "qs:" scheme
  // so it validates as a bearer link (not an http URL) and openSpaceCard
  // can round-trip it back to /api/public/open.
  if (composerDoc.kind === 'space') {
    for (const b of composerDoc.blocks || []) {
      if (b.type === 'link' && b.props?.text) {
        const raw = b.props.text.trim();
        b.props.text = raw && !raw.startsWith('qs:') && !/^https?:/.test(raw)
          ? 'qs:' + raw : raw;
      }
    }
  }
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

// Closing the composer stops the preview. The stage holds exactly one scene,
// so a preview left running would go on drawing behind whatever the reader
// opens next — and would be stopped only by accident, when that post started
// its own.
function closeComposer() {
  if (typeof STAGE !== 'undefined') STAGE.clear();
  dlgComposer.close();
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
