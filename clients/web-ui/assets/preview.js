// @ts-check
// The transient post reader (PS-4b/4c).
//
// A card invites a look; pressing Read post is the FIRST moment anything
// touches the network. What opens is a temporary verified reading — the
// node persisted nothing, and closing it forgets it. Following or joining
// the space is always a separate button, pressed after reading, never a
// side effect of it.
//
// The reader deliberately does not reuse renderArticle: that renderer is
// current-space-relative through openPub and assetURL, and pointing it at
// a space the node does not hold is how a preview quietly becomes a
// replica. This one renders from the preview payload alone, and its asset
// URLs go to the session route.

const PREV = (() => {
  /** @type {{reference:string, data:any}|null} */
  let open_ = null;

  function screen() { return document.getElementById('previewScreen'); }

  function show() {
    const c = document.getElementById('content');
    if (c) c.style.display = 'none';
    const cat = document.getElementById('catalogScreen');
    if (cat) cat.style.display = 'none';
    screen().style.display = '';
  }

  function close() {
    open_ = null;
    screen().style.display = 'none';
    const c = document.getElementById('content');
    if (c) c.style.display = '';
  }

  /** Open a shared card's reference. */
  async function open(reference) {
    let r;
    try {
      r = await api('/api/public/preview', {
        method: 'POST', body: JSON.stringify({ reference }),
      });
    } catch (err) {
      alert(err.message || String(err));
      return;
    }
    // The space is already here: plain navigation, nothing created. The
    // ORDER matters — openPub and assetURL are current-relative, so the
    // space switches first, then the view, then the post.
    if (r.state === 'existing_local') {
      close();
      current = r.space_id;
      await refresh();
      switchView('posts');
      await openPub(r.document_id);
      return;
    }
    open_ = { reference, data: r };
    render();
  }

  function render() {
    if (!open_) return;
    const r = open_.data;
    show();
    const box = document.getElementById('prevBody');
    box.innerHTML = '';
    const head = document.getElementById('prevSpace');
    head.textContent = r.space_title || t('prev.unnamed_space');

    if (r.state === 'preview_waiting' || r.state === 'publication_missing') {
      const p = document.createElement('p');
      p.className = 'hint';
      p.textContent = r.state === 'preview_waiting'
        ? t('prev.waiting') : t('prev.missing');
      box.appendChild(p);
      const retry = document.createElement('button');
      retry.textContent = t('prev.retry');
      retry.onclick = () => open(open_.reference);
      box.appendChild(retry);
      renderFoot(r, false);
      return;
    }

    const pub = r.publication;
    if (r.state === 'publication_archived') {
      const note = document.createElement('div');
      note.className = 'prev-archived';
      note.textContent = t('prev.archived');
      box.appendChild(note);
    }
    if (pub) renderPost(box, r, pub);
    renderFoot(r, r.state === 'preview_resolved');
  }

  /** @param {HTMLElement} box */
  function renderPost(box, r, pub) {
    const doc = pub.document || {};
    if (doc.cover) {
      const img = document.createElement('img');
      img.className = 'pub-hero';
      img.src = prevAssetURL(r.preview_id, doc.cover);
      img.onerror = () => {
        // The honest sentence, not a broken image: the bytes may be
        // unreachable by construction for an old post.
        const note = document.createElement('div');
        note.className = 'hint';
        note.textContent = t('prev.media_unavailable');
        img.replaceWith(note);
      };
      box.appendChild(img);
    }
    const h = document.createElement('h2');
    h.className = 'pub-h1';
    h.textContent = doc.title || t('prev.untitled');
    box.appendChild(h);
    if (doc.summary) {
      const s = document.createElement('p');
      s.className = 'pub-summary';
      s.textContent = doc.summary;
      box.appendChild(s);
    }
    for (const b of doc.blocks || []) renderBlock(box, r, b);
  }

  /** A deliberately small block renderer: text shows, images come from the
   *  session route, everything else degrades to a quiet line. */
  function renderBlock(box, r, b) {
    const props = b.props || {};
    switch (b.type) {
      case 'heading': {
        const h = document.createElement('h3');
        h.textContent = props.text || '';
        box.appendChild(h);
        break;
      }
      case 'text': case 'callout': {
        const p = document.createElement('p');
        p.textContent = props.text || '';
        box.appendChild(p);
        break;
      }
      case 'quote': {
        const q = document.createElement('blockquote');
        q.textContent = props.text || '';
        box.appendChild(q);
        break;
      }
      case 'code': {
        const pre = document.createElement('pre');
        pre.textContent = props.text || '';
        box.appendChild(pre);
        break;
      }
      case 'separator': {
        box.appendChild(document.createElement('hr'));
        break;
      }
      case 'image': {
        if (!props.asset) break;
        const img = document.createElement('img');
        img.className = 'pub-img';
        img.alt = props.text || '';
        img.src = prevAssetURL(r.preview_id, props.asset);
        img.onerror = () => {
          const note = document.createElement('div');
          note.className = 'hint';
          note.textContent = t('prev.media_unavailable');
          img.replaceWith(note);
        };
        box.appendChild(img);
        if (props.caption) {
          const c = document.createElement('div');
          c.className = 'hint';
          c.textContent = props.caption;
          box.appendChild(c);
        }
        break;
      }
      case 'stack': case 'section': case 'hero': case 'columns': {
        for (const child of b.children || []) renderBlock(box, r, child);
        break;
      }
      default: {
        const d = document.createElement('div');
        d.className = 'hint';
        d.textContent = t('prev.block_skipped', { kind: b.type });
        box.appendChild(d);
      }
    }
  }

  function prevAssetURL(pid, asset) {
    return `/api/public/previews/${pid}/assets/${asset}?token=${token}`;
  }

  /** The explicit continuation — after reading, never as a side effect. */
  function renderFoot(r, resolved) {
    const foot = document.getElementById('prevFoot');
    foot.innerHTML = '';
    const backB = document.createElement('button');
    backB.className = 'btn-plain';
    backB.textContent = t('prev.back');
    backB.onclick = close;
    foot.appendChild(backB);
    if (!resolved && r.state !== 'publication_archived') return;

    // Broadcast: Follow. Community: Follow read-only / Join and
    // participate. The word is never "Join" for a broadcast space — the
    // reader is not becoming an author.
    const community = r.publish !== 'curated' && r.join === 'open';
    const follow = document.createElement('button');
    follow.textContent = community ? t('prev.follow_ro') : t('prev.follow');
    follow.onclick = () => doFollow(r);
    foot.appendChild(follow);
    if (community) {
      const join = document.createElement('button');
      join.textContent = t('prev.join');
      join.onclick = () => doJoin(r);
      foot.appendChild(join);
    }
  }

  async function doFollow(r) {
    try {
      const res = await api('/api/public/follow', {
        method: 'POST', body: JSON.stringify({ reference: open_.reference }),
      });
      // Follow = replica + Navigator, in one motion.
      await NAV.addRef(res.id, r.space_title || '', 'pins');
      close();
      current = res.id;
      await refresh();
      switchView('posts');
    } catch (err) {
      alert(err.message || String(err));
    }
  }

  async function doJoin(r) {
    try {
      const res = await api('/api/public/follow', {
        method: 'POST', body: JSON.stringify({ reference: open_.reference }),
      });
      await api(`/api/spaces/${res.id}/join`, { method: 'POST' });
      close();
      current = res.id;
      await refresh();
    } catch (err) {
      alert(err.message || String(err));
    }
  }

  return { open, close };
})();

if (typeof window !== 'undefined') window.PREV = PREV;
