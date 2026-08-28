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
    // One switch owns the middle column (showScreen). Hiding siblings by
    // hand is how two screens ended up on it at once.
    showScreen('preview');
  }

  // leaving: another screen is taking the column. Drop what was being
  // read and touch no display — whoever is arriving owns that.
  function leaving() {
    open_ = null;
    if (typeof ATMO !== 'undefined') ATMO.unmount();
    if (prevObserver) prevObserver.disconnect();
  }

  // close: the reader's own way out. A preview is only ever opened FROM
  // Discover, so back goes back there — landing in the conversation would
  // be a different place than the one the person left.
  function close() {
    leaving();
    showScreen('catalog');
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
      // Walking INTO the space this device already holds — so Discover is
      // left, not merely covered: showScreen hides the catalog and lets
      // CAT release the session the leaf was holding. Doing this by hand
      // left both screens on the column at once, and the first press of
      // "← Posts" landed in a layout with no way back.
      leaving();
      showScreen('conversation');
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
    if (typeof ATMO !== 'undefined') ATMO.unmount();
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
    // Atmosphere mounts through the URL seam: bed and poster come from
    // the SESSION routes, the sound door first fetches the bytes (the
    // gate), and the remembered "always with sound" consent — recorded
    // for held spaces — deliberately does not apply here.
    if (doc.atmosphere && typeof ATMO !== 'undefined') {
      const atmoBar = document.createElement('div');
      atmoBar.className = 'atmo-bar';
      box.appendChild(atmoBar);
      ATMO.mount(box, doc.atmosphere, 'prev:' + r.document_id, {
        bar: atmoBar, enter: 'still',
        ignoreRemembered: true,
        srcFor: (aid) => prevAssetURL(r.preview_id, aid),
        posterInto: (img, aid) => fetchInto(r.preview_id, aid, img),
        // A stage plate takes the quiet path, NOT fetchInto: that one
        // reports failure by inserting a note beside the image and
        // replacing it, which inside an aria-hidden background layer would
        // mean visible prose and a deleted plate. A stage that never
        // arrives simply never takes over, and the previous picture holds.
        stageInto: (img, aid) => prevFetch(r.preview_id, aid, (ok) => {
          if (ok) img.src = prevAssetURL(r.preview_id, aid);
        }),
        soundGate: (aid, proceed) => prevFetch(r.preview_id, aid,
          (ok, state, reason) => proceed(ok, prevStateLine(state, reason))),
      });
    }
    if (doc.cover) {
      // Eager, through the swarm (PM-3): Read post was the consent for
      // the post's face, and the session fetches the bytes from whoever
      // holds them — the honest states replace the old "not loaded in a
      // preview" sentence, which stopped being true this gate.
      const img = document.createElement('img');
      img.className = 'pub-hero';
      img.alt = '';
      box.appendChild(img);
      fetchInto(r.preview_id, doc.cover, img);
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

  /**
   * Each block gets its own container carrying the block id, matching the
   * held reader. An atmosphere sequence anchors its stages to block ids, and
   * a preview whose blocks are anonymous could show the post but never its
   * story. The wrapper is deliberately class-less: margins collapse through
   * it, so the reading rhythm is exactly what it was.
   */
  function renderBlock(box, r, b) {
    const host = document.createElement('div');
    if (b.id) host.dataset.blockId = String(b.id);
    box.appendChild(host);
    renderBlockInto(host, r, b);
  }

  /** A deliberately small block renderer: text shows, images come from the
   *  session route, everything else degrades to a quiet line. */
  function renderBlockInto(box, r, b) {
    const props = b.props || {};
    switch (b.type) {
      case 'heading': {
        const h = document.createElement('h3');
        h.textContent = props.text || '';
        box.appendChild(h);
        break;
      }
      case 'text': case 'callout': {
        // The transient reader renders the same language as the held one —
        // two renderers that disagree about what a post SAYS would be worse
        // than either being wrong alone.
        const p = document.createElement('div');
        p.className = 'md';
        if (typeof MD !== 'undefined') MD.into(p, props.text || '');
        else p.textContent = props.text || '';
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
      case 'gallery': {
        const grid = document.createElement('div');
        grid.className = 'pub-gallery';
        for (const it of props.items || []) {
          if (!it) continue;
          const img = document.createElement('img');
          img.className = 'pub-img';
          img.alt = '';
          grid.appendChild(img);
          nearViewport(img, () => fetchInto(r.preview_id, it, img));
        }
        box.appendChild(grid);
        break;
      }
      case 'image': {
        if (!props.asset) break;
        const img = document.createElement('img');
        img.className = 'pub-img';
        img.alt = props.text || '';
        box.appendChild(img);
        // Near-viewport: interest is announced when the image approaches
        // the screen, not when the post opens.
        nearViewport(img, () => fetchInto(r.preview_id, props.asset, img));
        if (props.caption) {
          const c = document.createElement('div');
          c.className = 'hint';
          c.textContent = props.caption;
          box.appendChild(c);
        }
        break;
      }
      case 'audio': {
        if (!props.asset) break;
        // On play, whole-file (PM-4): fetched completely, verified, THEN
        // played — progressive playback of unverified bytes would play
        // what the digest check might later refuse.
        const btn = document.createElement('button');
        btn.textContent = t('prev.load_audio');
        box.appendChild(btn);
        btn.onclick = () => {
          btn.disabled = true;
          btn.textContent = t('prev.loading');
          prevFetch(r.preview_id, props.asset, (ok, state, reason) => {
            if (!ok) {
              const note = document.createElement('div');
              note.className = 'hint';
              note.textContent = prevStateLine(state, reason);
              btn.replaceWith(note);
              return;
            }
            const el = document.createElement('audio');
            el.controls = true;
            el.src = prevAssetURL(r.preview_id, props.asset);
            el.onplay = () => {
              if (typeof AUDIO !== 'undefined') {
                AUDIO.request('player', 'player', () => el.pause());
              }
            };
            btn.replaceWith(el);
          }, (p2) => {
            if (p2.state === 'partial' && p2.total) {
              btn.textContent = t('prev.progress', { got: p2.total - p2.missing, total: p2.total });
            }
          });
        };
        break;
      }
      case 'video': {
        if (!props.asset) break;
        // Explicit, whole-file (video v0): fetched completely, verified,
        // then played over Range/206 from the session's assembled bytes.
        const btn = document.createElement('button');
        btn.textContent = t('prev.load_video');
        box.appendChild(btn);
        btn.onclick = () => {
          btn.disabled = true;
          btn.textContent = t('prev.loading');
          prevFetch(r.preview_id, props.asset, (ok, state, reason) => {
            if (!ok) {
              const note = document.createElement('div');
              note.className = 'hint';
              note.textContent = prevStateLine(state, reason);
              btn.replaceWith(note);
              return;
            }
            const v = document.createElement('video');
            v.controls = true;
            v.src = prevAssetURL(r.preview_id, props.asset);
            v.onplay = () => {
              if (typeof AUDIO !== 'undefined') {
                AUDIO.request('player', 'player', () => v.pause());
              }
            };
            btn.replaceWith(v);
          }, (p2) => {
            if (p2.state === 'partial' && p2.total) {
              btn.textContent = t('prev.progress', { got: p2.total - p2.missing, total: p2.total });
            }
          });
        };
        break;
      }
      case 'file': {
        // Explicit only (PM-5 v0): a download is a choice.
        const btn = document.createElement('button');
        btn.className = 'btn-plain';
        btn.textContent = t('prev.download_file');
        box.appendChild(btn);
        if (!props.asset) { btn.disabled = true; break; }
        btn.onclick = () => {
          btn.disabled = true;
          btn.textContent = t('prev.loading');
          prevFetch(r.preview_id, props.asset, (ok, state, reason) => {
            if (!ok) {
              btn.textContent = prevStateLine(state, reason);
              return;
            }
            const a = document.createElement('a');
            a.href = prevAssetURL(r.preview_id, props.asset);
            a.textContent = t('prev.open_file');
            btn.replaceWith(a);
          });
        };
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
    // Encoded for the same reason assetURL encodes: the asset id here came
    // out of a STRANGER'S publication — this is the preview path, the one
    // reachable from a public directory nobody has joined — and it is
    // pasted into a URL before anything has checked it.
    return `/api/public/previews/${encodeURIComponent(pid)}/assets/${
      encodeURIComponent(asset)}?token=${token}`;
  }

  /** prevFetch drives one session asset: POST the consent, poll the
   *  honest states, and answer cb(ok, state, reason) exactly once. */
  async function prevFetch(pid, asset, cb, onTick) {
    let last = { state: 'requesting', reason: '' };
    try {
      for (let i = 0; i < 55; i++) {
        const r = await api(`/api/public/previews/${encodeURIComponent(pid)}/assets/${
          encodeURIComponent(asset)}/fetch`,
          { method: 'POST' });
        last = r;
        if (onTick) onTick(r);
        if (r.state === 'ready') { cb(true, r.state, ''); return; }
        if (r.state === 'no_response' || r.state === 'descriptor_unavailable' ||
            r.state === 'budget_exceeded' || r.state === 'integrity_error' ||
            r.state === 'cancelled') {
          cb(false, r.state, r.reason || '');
          return;
        }
        await new Promise(res => setTimeout(res, 1200));
        if (!open_) { cb(false, 'cancelled', ''); return; } // reader closed
      }
    } catch (err) {
      cb(false, 'no_response', err.message || String(err));
      return;
    }
    cb(false, last.state || 'no_response', last.reason || '');
  }

  /** A preview-local near-viewport trigger: the fetch starts when the
   *  element approaches the screen, not when the post opens — the person
   *  does not announce interest in every attachment at once. */
  const prevObserver = typeof IntersectionObserver !== 'undefined'
    ? new IntersectionObserver((entries) => {
        for (const e of entries) {
          if (e.isIntersecting && e.target._prevLazy) {
            prevObserver.unobserve(e.target);
            const fn = e.target._prevLazy;
            e.target._prevLazy = null;
            fn();
          }
        }
      }, { rootMargin: '300px' })
    : null;

  function nearViewport(el, fn) {
    if (!prevObserver) { fn(); return; }
    el._prevLazy = fn;
    prevObserver.observe(el);
  }

  /** fetchInto points an <img> at a session asset once its bytes are
   *  ready, or replaces it with the honest sentence. */
  function fetchInto(pid, asset, img) {
    // The loader is a visible sibling, not a blank rectangle: a person
    // watching an empty spot cannot tell "loading" from "broken".
    const load = document.createElement('div');
    load.className = 'prev-loading';
    load.textContent = t('prev.looking');
    img.after(load);
    // AN <img> WITH NO SRC IS A BROKEN IMAGE, and the browser draws it as
    // one: a torn-page icon with the alt text beside it, which here is the
    // camera's own filename. So a post being fetched showed a column of
    // "14A7402D-CDD7-4946-A0F3-DEF63EACED03.jpg" in error icons. The element
    // stays in the flow — it is where the picture will go — but nothing is
    // drawn for it until there are bytes to draw.
    img.style.display = 'none';
    prevFetch(pid, asset, (ok, state, reason) => {
      load.remove();
      if (ok) {
        img.style.display = '';
        img.src = prevAssetURL(pid, asset);
        return;
      }
      const note = document.createElement('div');
      note.className = 'hint';
      note.textContent = prevStateLine(state, reason);
      img.replaceWith(note);
    }, (r) => {
      load.textContent = (r.state === 'partial' && r.total)
        ? t('prev.progress', { got: r.total - r.missing, total: r.total })
        : t('prev.looking');
    });
  }

  /** The honest sentences, one per observed state. */
  function prevStateLine(state, reason) {
    switch (state) {
      case 'no_response': return t('prev.no_response');
      case 'descriptor_unavailable': return t('prev.descriptor');
      case 'budget_exceeded': return t('prev.budget');
      case 'integrity_error': return t('prev.integrity');
      case 'cancelled': return t('prev.cancelled');
    }
    return reason || t('prev.no_response');
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
      // Following LANDS IN THE SPACE, not back in Discover: the person
      // asked for this place, and the reader's own back button is the
      // only thing that means "I was just looking".
      leaving();
      showScreen('conversation');
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
      leaving();
      showScreen('conversation');
      current = res.id;
      await refresh();
    } catch (err) {
      alert(err.message || String(err));
    }
  }

  return { open, close, leaving };
})();

if (typeof window !== 'undefined') window.PREV = PREV;
