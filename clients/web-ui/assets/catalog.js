// @ts-check
// Discover: places, not machinery (CAT-0b).
//
// A directory is an ordinary public space that DECLARES it is for finding
// other places — a signed `kind` in its policy, not a tag somebody typed.
// Its entries are ordinary publications of kind "space", each carrying a
// share link, and nothing says that link's target cannot itself be a
// directory. So descending a level is still "look at the next space", and
// there is no tree protocol, no index and no recursive sync.
//
// Three rules run through this file, and each replaces something CAT-0a did.
//
// LOOKING NEVER SUBSCRIBES. CAT-0a browsed by OPENING: every glance left a
// durable reader replica, a keystore write, an adopted relay and a standing
// sync obligation. Every read here is an INSPECT — verified, transient,
// closed as soon as its answer is drawn — and the only thing that persists
// anything is a person pressing "Add to my spaces".
//
// ONE BROWSE ACTION PER CARD. CAT-0a offered "look inside" beside "open": a
// true distinction (transient against replica) and an unanswerable question
// for a person. The card itself is the affordance; what comes back decides
// whether it is a level or a leaf.
//
// THE MACHINERY STAYS OFF THE SCREEN. No source, replica, inspect,
// broadcast, curated, publish mode, kind, relay or projection appears in
// the ordinary path. The one place the word "directory" is explained lives
// behind the ⋯ , for somebody running their own.

/**
 * OFFICIAL_SOURCES holds alternate BOOTSTRAP ADDRESSES of ONE official
 * home, tried in order — the first that answers is the home.
 *
 * They are never merged. A share link is relay-bound and irrevocable, so a
 * second address exists to survive a relay change, not to add a second
 * front page. They are compiled in, and a person may switch them OFF but
 * cannot delete them: an act that silently undoes itself with the next
 * build is worse than no act.
 *
 * @type {string[]}
 */
const OFFICIAL_SOURCES = []; // set when the official directory space exists

/** @typedef {{space:string,title:string,link:string}} Level */

const CAT = (() => {
  /** @type {Level[]} The path walked so far, and the cycle guard. Empty = home. */
  let path = [];
  /** @type {any} The inspection a leaf is drawn from, and its live session. */
  let leaf = null;
  /** @type {Record<string,{at:number,cards:any[],title:string}>} */
  const cache = {};
  // LOAD-BEARING, not a nicety. Every fetch opens a session and closes it,
  // and the node keeps at most previewCap of them: without this, walking
  // back up a path would re-dial every level. Sixty seconds is long enough
  // for a walk and short enough that a refresh is never far away.
  const CACHE_MS = 60000;

  function screen() { return document.getElementById('catalogScreen'); }

  // ---- reading ----

  /** One transient reading of a public space. Nothing here persists. */
  async function inspect(reference, refresh) {
    return api('/api/public/inspect', {
      method: 'POST', body: JSON.stringify({ reference, refresh: !!refresh }),
    });
  }

  /**
   * End a session as soon as its answer is drawn. A five-level walk would
   * otherwise leave five fetchers holding budget behind it.
   */
  function release(pid) {
    if (!pid) return;
    api(`/api/public/previews/${pid}/close`, { method: 'POST' }).catch(() => {});
  }

  /**
   * What a link turns out to be. The ONE place a link becomes either a
   * level, a leaf, or a sentence.
   * @returns {Promise<{kind:'level'|'leaf'|'silent'|'refused', in?:any, why?:string}>}
   */
  async function look(link, refresh) {
    let r;
    try {
      r = await inspect(link, refresh);
    } catch (err) {
      return { kind: 'refused', why: err && err.message };
    }
    // Already held: no session at all, and the space's own state is better
    // than anything a stranger's reading could say about it.
    if (r.state === 'existing_local') {
      const sp = (spacesCache || []).find(s => s.id === r.space_id);
      let cards = [];
      try {
        const pubs = await api(`/api/spaces/${r.space_id}/publications`);
        // The local listing calls a publication's own type `kind`; an
        // inspection calls it `publication_kind`, because `kind` there is
        // the SPACE's declaration. Normalise here, once, so nothing
        // downstream has to know which door the cards came through — the
        // mismatch made a held space's leaf claim nothing was ever posted
        // in it.
        cards = (pubs.publications || []).map(p => ({ ...p, publication_kind: p.kind }));
      } catch { /* a held space with nothing readable is still a leaf */ }
      const held = {
        space_id: r.space_id, cards, cards_total: cards.length,
        space_title: sp ? (sp.display_title || sp.title || '') : '',
        kind: sp && sp.kind, frozen: sp && sp.frozen, publish: sp && sp.publish,
        held: true,
      };
      return { kind: held.kind === 'directory' ? 'level' : 'leaf', in: held };
    }
    if (r.state !== 'inspect_resolved') {
      // preview_waiting and everything like it. The mechanism's word never
      // reaches the screen; what a person is told is that nothing came back.
      release(r.preview_id);
      return { kind: 'silent' };
    }
    if (r.kind === 'directory') return { kind: 'level', in: r };
    return { kind: 'leaf', in: r };
  }

  // ---- moving ----

  /**
   * Follow one entry. This is the whole of browsing: what comes back
   * decides whether it was a level or a leaf.
   */
  async function follow(link, title) {
    note(t('cat.looking'));
    const r = await look(link, false);
    note('');
    if (r.kind === 'refused' || r.kind === 'silent') {
      note(t('cat.silent'));
      return;
    }
    const inn = r.in;
    // The cycle guard is scoped to THIS PATH and says so. There is no
    // global graph here, and a client walking one branch cannot honestly
    // claim to have detected one. It keys on the id this node VERIFIED,
    // never on the link: two links through different relays are one place.
    if (path.some(l => l.space === inn.space_id)) {
      release(inn.preview_id);
      note(t('cat.cycle', { title: title || inn.space_title || '' }));
      return;
    }
    if (r.kind === 'leaf') {
      // A leaf KEEPS its session while it is on screen: reading a post
      // from it is served by the same one.
      leaf = inn;
      path.push({ space: inn.space_id, title: title || inn.space_title || '', link });
      await render();
      return;
    }
    cache[inn.space_id] = {
      at: Date.now(), cards: entriesOf(inn),
      title: inn.space_title || title || '',
    };
    release(inn.preview_id);
    leaf = null;
    path.push({ space: inn.space_id, title: title || inn.space_title || '', link });
    await render();
  }

  // Back at home leaves Discover. One gesture, one meaning: it always
  // goes up, and above home is the room you came from.
  function back() {
    dropLeaf();
    if (!path.length) { close(); return; }
    path.pop();
    render();
  }

  function close() {
    dropLeaf();
    path = [];
    const s = screen();
    if (s) s.style.display = 'none';
    const c = document.getElementById('content');
    if (c) c.style.display = '';
  }

  function dropLeaf() {
    if (leaf && leaf.preview_id) release(leaf.preview_id);
    leaf = null;
  }

  function note(text) {
    const box = document.getElementById('catNote');
    if (!box) return;
    box.textContent = text;
    box.hidden = !text;
  }

  /** The entries of a listing: space-cards, whatever else is in there. */
  function entriesOf(inn) {
    return (inn.cards || []).filter(c => c.publication_kind === 'space');
  }

  /** Everything a listing holds that is NOT an entry — a leaf's own posts. */
  function postsOf(inn) {
    return (inn.cards || []).filter(c => c.publication_kind !== 'space');
  }

  /**
   * A post's own address: the space's share link with the document named.
   * A share link is base64url of "<relay>\nspace:<hex>", and the third
   * field is where a post goes — the same grammar ComposePublicLink writes,
   * assembled here so a leaf can hand PREV a reference it will accept.
   */
  function postReference(spaceLink, documentId) {
    const plain = decodeShare(spaceLink);
    if (!/space:[0-9a-f]+/i.test(plain)) return spaceLink;
    const b64 = btoa(plain + ':' + documentId)
      .replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
    return 'qs:' + b64;
  }

  /** The entries of one level, cached so walking back up does not re-dial. */
  async function cardsOf(level, refresh) {
    const hit = cache[level.space];
    if (!refresh && hit && Date.now() - hit.at < CACHE_MS) return hit.cards;
    const r = await look(level.link, refresh);
    if (r.kind !== 'level') return null;
    cache[level.space] = {
      at: Date.now(), cards: entriesOf(r.in), title: r.in.space_title || level.title,
    };
    release(r.in.preview_id);
    return cache[level.space].cards;
  }

  // ---- home ----

  /**
   * HOME IS NOT A LIST OF SOURCES. It is the official directory's own top
   * level, with whatever the person added listed after it. That single
   * choice is what keeps the machinery invisible: a person opens Discover
   * and sees places, in the order their publisher chose, exactly as they
   * would one level down. Nothing on screen explains where the list came
   * from, because nothing needs to.
   */
  async function homeRows(refresh) {
    const rows = [];
    let officialSilent = false;
    if (!NAV.officialOff() && OFFICIAL_SOURCES.length) {
      let answered = false;
      for (const link of OFFICIAL_SOURCES) {
        const r = await look(link, refresh);
        if (r.kind === 'level') {
          for (const c of entriesOf(r.in)) rows.push(cardRow(c));
          release(r.in.preview_id);
          answered = true;
          break;
        }
        // A bootstrap address that answers as something OTHER than a
        // directory is not the official home, and is not silently used as
        // one. Try the next address.
        if (r.in) release(r.in.preview_id);
      }
      officialSilent = !answered;
    }
    // The person's own, after the official entries, under their own names.
    // This is what "Add to Discover" visibly does.
    for (const s of NAV.sources()) rows.push(directoryRow(s));
    return { rows, officialSilent };
  }

  // ---- drawing ----

  async function render() {
    const s = screen();
    if (!s) return;
    const c = document.getElementById('content');
    if (c) c.style.display = 'none';
    s.style.display = '';
    note('');
    paintCrumbs();

    const list = document.getElementById('catList');
    if (!list) return;
    list.innerHTML = `<div class="nav-note">${esc(t('cat.loading'))}</div>`;

    if (!path.length) {
      const { rows, officialSilent } = await homeRows(false);
      list.innerHTML = '';
      if (officialSilent) list.appendChild(featuredSilent());
      if (!rows.length && !officialSilent) {
        list.appendChild(emptyHome());
        return;
      }
      for (const r of rows) list.appendChild(r);
      return;
    }

    const here = path[path.length - 1];
    if (leaf) { list.innerHTML = ''; list.appendChild(leafView(leaf)); return; }

    const cards = await cardsOf(here, false);
    list.innerHTML = '';
    if (cards === null) { note(t('cat.silent')); return; }
    if (!cards.length) {
      list.innerHTML = `<div class="nav-note">${esc(t('cat.empty'))}</div>`;
      return;
    }
    for (const card of cards) list.appendChild(cardRow(card));
  }

  function paintCrumbs() {
    const crumbs = document.getElementById('catCrumbs');
    if (!crumbs) return;
    crumbs.innerHTML = '';
    // A leading crumb for home, so the way back out is the same gesture as
    // the way back up.
    const trail = [{ space: '', title: t('cat.home'), link: '' }].concat(path);
    trail.forEach((l, i) => {
      const b = document.createElement('button');
      b.className = 'btn-plain';
      b.textContent = l.title || t('cat.untitled');
      b.disabled = i === trail.length - 1;
      b.onclick = () => { dropLeaf(); path = path.slice(0, i); render(); };
      crumbs.appendChild(b);
      if (i < trail.length - 1) {
        const sep = document.createElement('span');
        sep.className = 'cat-sep'; sep.textContent = '›';
        crumbs.appendChild(sep);
      }
    });
  }

  /**
   * One entry, as a row with a chevron rather than a pair of buttons. The
   * hint a card carries changes only the glyph drawn before anybody taps —
   * it settles nothing, and nothing on screen ever says "says".
   */
  function cardRow(card) {
    const link = card.link || spaceCardLink(card);
    const isDir = card.kind_hint === 'directory';
    const d = document.createElement('div');
    d.className = 'cat-row cat-tap';
    d.setAttribute('role', 'button');
    d.tabIndex = 0;

    const head = document.createElement('div');
    head.className = 'cat-head';
    const mark = document.createElement('span');
    mark.className = 'cat-mark';
    mark.textContent = isDir ? '📖' : '◍';
    head.appendChild(mark);
    const title = document.createElement('span');
    title.className = 'cat-title';
    title.textContent = card.title || t('cat.untitled');
    head.appendChild(title);
    // Status only for places this node already knows. Probing every
    // advertised card would fan out relay traffic and broadcast what
    // somebody is browsing — opening a card is a person's act.
    const st = spaceCardStatus(link);
    if (st.known) {
      const status = document.createElement('span');
      status.className = 'cat-status';
      status.textContent = st.text;
      head.appendChild(status);
    }
    const chev = document.createElement('span');
    chev.className = 'cat-chev';
    chev.textContent = '›';
    head.appendChild(chev);
    d.appendChild(head);

    if (card.summary) {
      const sum = document.createElement('div');
      sum.className = 'cat-sum';
      sum.textContent = card.summary;
      d.appendChild(sum);
    }

    const go = () => follow(link, card.title || '');
    d.onclick = (e) => {
      if (/** @type {HTMLElement} */(e.target).closest('.cat-acts')) return;
      go();
    };
    d.onkeydown = (e) => {
      if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); go(); }
    };

    // A directory carries one secondary act, named for its RESULT. The undo
    // carries the same name — one thing must not be "Add to Discover" here
    // and "remove source" somewhere else.
    if (isDir) {
      const acts = document.createElement('div');
      acts.className = 'cat-acts';
      acts.appendChild(discoverToggle(link, card.title || ''));
      d.appendChild(acts);
    }
    return d;
  }

  // ONE NAME IN BOTH DIRECTIONS. A thing called "Add to Discover" here and
  // "remove source" in the sheet is the vocabulary leak wearing a different
  // hat — it is how a person learns the app has a second, truer name for
  // things. The ✓ is an acknowledgement of the press, not a third name.
  function discoverToggle(link, title) {
    const b = document.createElement('button');
    b.className = 'btn-plain';
    const paint = () => {
      const have = NAV.sources().some(s => s.link === link);
      b.textContent = have ? t('cat.remove_discover') : t('cat.add_discover');
      b.classList.toggle('sel', have);
    };
    paint();
    b.onclick = async (e) => {
      e.stopPropagation();
      const have = NAV.sources().some(s => s.link === link);
      if (have) { await NAV.removeSource(link); paint(); return; }
      await NAV.addSource(link, title);
      b.textContent = t('cat.added_discover');
      b.classList.add('sel');
      setTimeout(paint, 1400);
    };
    return b;
  }

  /** A directory the person added, listed on home under their own name. */
  function directoryRow(src) {
    return cardRow({
      title: src.label || t('cat.untitled'),
      link: src.link, kind_hint: 'directory',
    });
  }

  /**
   * A LEAF is an ordinary space, drawn from what the look already returned.
   * It is not a new screen and not a whole-space preview: the space's own
   * name, whether anyone may write, whether it is frozen, its recent posts,
   * and ONE explicit act named for its result.
   */
  function leafView(inn) {
    const here = path[path.length - 1] || { link: '' };
    const d = document.createElement('div');
    d.className = 'cat-leaf';

    const h = document.createElement('h3');
    h.textContent = inn.space_title || t('cat.untitled');
    d.appendChild(h);

    const bits = [];
    if (inn.frozen) bits.push(t('cat.leaf.frozen'));
    bits.push(inn.publish === 'curated' ? t('cat.leaf.owner_posts') : t('cat.leaf.anyone_posts'));
    const sub = document.createElement('p');
    sub.className = 'hint';
    sub.textContent = bits.join(' · ');
    d.appendChild(sub);

    const posts = postsOf(inn);
    if (posts.length) {
      const list = document.createElement('div');
      list.className = 'cat-posts';
      for (const p of posts.slice(0, 12)) {
        const row = document.createElement('div');
        row.className = 'cat-row cat-tap';
        row.textContent = p.title || t('cat.untitled');
        row.onclick = () => PREV.open(postReference(here.link, p.document_id));
        list.appendChild(row);
      }
      d.appendChild(list);
    } else {
      const none = document.createElement('div');
      none.className = 'nav-note';
      none.textContent = t('cat.leaf.nothing');
      d.appendChild(none);
    }

    // The one act, and the first moment anything persists.
    const acts = document.createElement('div');
    acts.className = 'cat-acts';
    if (inn.held) {
      const open = document.createElement('button');
      open.className = 'btn-tinted';
      open.textContent = t('cat.leaf.go');
      open.onclick = () => { const id = inn.space_id; close(); NAV.onOpen(id); };
      acts.appendChild(open);
    } else {
      const add = document.createElement('button');
      add.className = 'btn-tinted';
      add.textContent = t('cat.leaf.keep');
      add.onclick = async () => {
        add.disabled = true;
        try {
          // The same reference that was looked at. Follow promotes the
          // session's blobs rather than fetching them again, so the person
          // does not pay twice for what they just read.
          const r = await api('/api/public/follow', {
            method: 'POST', body: JSON.stringify({ reference: here.link }),
          });
          const id = (r && r.id) || inn.space_id;
          dropLeaf();
          close();
          await refresh();
          NAV.onOpen(id);
        } catch (err) {
          add.disabled = false;
          note(t('cat.silent'));
        }
      };
      acts.appendChild(add);
    }
    d.appendChild(acts);
    return d;
  }

  /**
   * The featured directory could not be reached. Discover must not look
   * broken, must not say a mechanism's word, and must NEVER quietly promote
   * one of the person's own directories to root — that would be two
   * different Discovers on two days with no act of theirs in between.
   */
  function featuredSilent() {
    const d = document.createElement('div');
    d.className = 'cat-note-box';
    const p = document.createElement('p');
    p.textContent = t('cat.featured_silent');
    d.appendChild(p);
    const again = document.createElement('button');
    again.className = 'btn-plain';
    again.textContent = t('cat.try_again');
    again.onclick = () => render();
    d.appendChild(again);
    const where = document.createElement('p');
    where.className = 'hint';
    where.textContent = NAV.sources().length ? t('cat.yours_still_here') : '';
    d.appendChild(where);
    return d;
  }

  function emptyHome() {
    const d = document.createElement('div');
    d.className = 'cat-note-box';
    const p = document.createElement('p');
    p.textContent = t('cat.home_empty');
    d.appendChild(p);
    const add = document.createElement('button');
    add.className = 'btn-tinted';
    add.textContent = t('cat.add_directory');
    add.onclick = () => addDirectory();
    d.appendChild(add);
    return d;
  }

  // ---- the ⋯ sheet: the one place a person may meet the word ----

  function openDirectories() {
    const dlg = /** @type {any} */(document.getElementById('dlgDirs'));
    if (!dlg) return;
    paintDirectories();
    dlg.showModal();
  }

  function paintDirectories() {
    const list = document.getElementById('dirList');
    if (!list) return;
    list.innerHTML = '';
    if (OFFICIAL_SOURCES.length) {
      const row = document.createElement('div');
      row.className = 'list-row';
      const label = document.createElement('span');
      label.className = 'lr-label';
      label.textContent = t('cat.official');
      row.appendChild(label);
      const toggle = document.createElement('button');
      toggle.className = 'btn-plain';
      const paint = () => {
        toggle.textContent = NAV.officialOff() ? t('cat.off') : t('cat.on');
        toggle.classList.toggle('sel', !NAV.officialOff());
      };
      paint();
      // Switched off, never deleted: a compiled-in address is not the
      // person's to remove, and an act that returns with the next build is
      // worse than no act.
      toggle.onclick = async () => {
        await NAV.setOfficialOff(!NAV.officialOff());
        paint();
        render();
      };
      row.appendChild(toggle);
      list.appendChild(row);
    }
    for (const s of NAV.sources()) {
      const row = document.createElement('div');
      row.className = 'list-row';
      const label = document.createElement('span');
      label.className = 'lr-label';
      label.textContent = s.label || t('cat.untitled');
      label.title = s.link;
      row.appendChild(label);
      const copy = document.createElement('button');
      copy.className = 'btn-plain';
      copy.textContent = t('cat.copy_link');
      copy.onclick = () => {
        navigator.clipboard?.writeText(s.link);
        copy.textContent = t('cat.copied');
      };
      row.appendChild(copy);
      const rm = document.createElement('button');
      rm.className = 'btn-plain';
      rm.textContent = t('cat.remove_discover');
      rm.onclick = async () => { await NAV.removeSource(s.link); paintDirectories(); render(); };
      row.appendChild(rm);
      list.appendChild(row);
    }
  }

  async function addDirectory() {
    const pasted = prompt(t('cat.paste'));
    if (!pasted || !pasted.trim()) return;
    const link = pasted.trim();
    note(t('cat.looking'));
    const r = await look(link, false);
    note('');
    if (r.kind !== 'level') {
      note(r.kind === 'leaf' ? t('cat.not_a_directory') : t('cat.silent'));
      if (r.in) release(r.in.preview_id);
      return;
    }
    const title = r.in.space_title || '';
    release(r.in.preview_id);
    await NAV.addSource(link, title);
    paintDirectories();
    await render();
  }

  // ---- adding an entry to a directory you can write to ----

  /** @type {{link:string,kindHint:string}|null} What the last look found. */
  let pending = null;

  function openAddSpace() {
    pending = null;
    for (const id of ['addSpaceTitle', 'addSpaceSummary', 'addSpaceAdd', 'addSpaceFound']) {
      const el = document.getElementById(id);
      if (el) el.style.display = 'none';
    }
    /** @type {HTMLInputElement} */(document.getElementById('addSpaceLink')).value = '';
    /** @type {HTMLInputElement} */(document.getElementById('addSpaceTitle')).value = '';
    /** @type {HTMLInputElement} */(document.getElementById('addSpaceSummary')).value = '';
    document.getElementById('addSpaceMsg').textContent = '';
    /** @type {any} */(document.getElementById('dlgAddSpace')).showModal();
  }

  /**
   * An EXPLICIT press, never debounced on paste. An automatic network act
   * the moment something lands in a field is the authoring-end version of
   * what CAT-0a's browsing did, and it would tell a relay what somebody is
   * considering before they have decided anything.
   */
  async function lookUpSpace() {
    const msg = document.getElementById('addSpaceMsg');
    const link = /** @type {HTMLInputElement} */(
      document.getElementById('addSpaceLink')).value.trim();
    if (!link) return;
    msg.textContent = t('cat.looking');
    const r = await look(link, false);
    const found = document.getElementById('addSpaceFound');
    const title = /** @type {HTMLInputElement} */(document.getElementById('addSpaceTitle'));
    const summary = /** @type {HTMLInputElement} */(document.getElementById('addSpaceSummary'));

    // Refusals first, and only two of them stop the Add.
    if (r.in && r.in.space_id === current) {
      msg.textContent = t('cat.add.itself');
      return;
    }
    // Already listed. Read the directory's OWN entries rather than the
    // browse cache: adding happens from inside the space, where that cache
    // is usually empty, so keying on it would let the same place be listed
    // twice by anybody who had not just walked in through Discover.
    const listed = await heldCards(current);
    // The listing hands back the target directly; spaceCardLink is the
    // fallback for a full document, which is what a card looks like when it
    // arrives any other way.
    if (listed.some(c => qsLink(c.link || spaceCardLink(c)) === qsLink(link))) {
      msg.textContent = t('cat.add.already');
      return;
    }

    if (r.kind === 'silent' || r.kind === 'refused') {
      // UNREACHABLE IS NOT A REFUSAL. A directory that can only list places
      // which happen to be online is a directory that empties itself during
      // an outage — so it goes in with no hint at all, which is exactly
      // what "unknown" means.
      pending = { link, kindHint: '' };
      found.textContent = t('cat.add.unreachable');
      found.style.display = '';
      msg.textContent = '';
    } else {
      pending = { link, kindHint: r.kind === 'level' ? 'directory' : 'space' };
      found.textContent = r.kind === 'level'
        ? t('cat.add.found_directory', { title: r.in.space_title || '' })
        : t('cat.add.found_space', { title: r.in.space_title || '' });
      found.style.display = '';
      msg.textContent = '';
      if (!title.value) title.value = r.in.space_title || '';
      release(r.in.preview_id);
    }
    title.style.display = '';
    summary.style.display = '';
    document.getElementById('addSpaceAdd').style.display = '';
  }

  async function addSpaceToDirectory() {
    const msg = document.getElementById('addSpaceMsg');
    if (!pending) return;
    const title = /** @type {HTMLInputElement} */(
      document.getElementById('addSpaceTitle')).value.trim();
    if (!title) { msg.textContent = t('cat.add.needs_name'); return; }
    const summary = /** @type {HTMLInputElement} */(
      document.getElementById('addSpaceSummary')).value.trim();
    try {
      await api(`/api/spaces/${current}/publications`, {
        method: 'POST',
        body: JSON.stringify({
          document: buildSpaceCard({
            title, summary, link: pending.link, kindHint: pending.kindHint,
          }),
        }),
      });
      /** @type {any} */(document.getElementById('dlgAddSpace')).close();
      delete cache[current];
      switchView('posts');
      await refreshPosts();
    } catch (err) {
      msg.textContent = err.message;
    }
  }

  /** Discover, from the header. */
  async function openHome() {
    dropLeaf();
    path = [];
    await render();
  }

  /**
   * Entering a directory this node already holds — the Navigator's own
   * Directories section. It is the same walk, started one level in.
   */
  async function enterHeld(spaceId, title) {
    dropLeaf();
    path = [];
    const sp = (spacesCache || []).find(s => s.id === spaceId);
    let link = '';
    try {
      const r = await api(`/api/spaces/${spaceId}/link`);
      link = r.link || '';
    } catch { /* a held directory with no shareable link is still walkable */ }
    cache[spaceId] = { at: 0, cards: [], title: title || '' };
    path.push({ space: spaceId, title: title || (sp && sp.title) || '', link });
    const cards = await heldCards(spaceId);
    cache[spaceId] = { at: Date.now(), cards, title: title || '' };
    await render();
  }

  async function heldCards(spaceId) {
    try {
      const pubs = await api(`/api/spaces/${spaceId}/publications`);
      return (pubs.publications || [])
        .map(p => ({ ...p, publication_kind: p.kind }))
        .filter(p => p.publication_kind === 'space');
    } catch { return []; }
  }

  return {
    openHome, follow, back, close, openDirectories, addDirectory, enterHeld,
    openAddSpace, lookUpSpace, addSpaceToDirectory,
    refresh: () => render(),
    get path() { return path.slice(); },
  };
})();

if (typeof window !== 'undefined') window.CAT = CAT;
