// @ts-check
// Catalogs, walked (CAT-0a).
//
// A catalog is an ordinary public broadcast space whose posts are
// publications of kind "space", each carrying a share link in a plain link
// block. Nothing says that link's target cannot itself be a catalog — so
// descending a level is exactly "open the next space and look at its cards".
// No tree protocol, no index, no recursive sync.
//
// Two honesty rules run through the whole file.
//
// WHETHER AN ENTRY IS A CATALOG CANNOT BE KNOWN BEFORE OPENING IT. A card
// may carry a "catalog" tag, but that is the author's word: an ordinary
// space can be tagged and a real catalog can be unmarked. So the tag is
// rendered as a claim, and what a level actually contains is only learned
// by going there.
//
// NOTHING IS FETCHED UNTIL SOMEBODY OPENS IT. Quietly loading every
// advertised catalog would fan out relay traffic and broadcast which
// catalogs a person browses. This is the same refusal spaceCardStatus makes
// about probing, applied to the level above.

/** @typedef {{id:string,title:string}} Level */

const CAT = (() => {
  /** @type {Level[]} The path walked so far. Also the cycle guard. */
  let path = [];
  /** @type {Record<string,{at:number,cards:any[]}>} */
  const cache = {};
  const CACHE_MS = 60000;

  function screen() { return document.getElementById('catalogScreen'); }

  /** Resolve a share link to a space this node holds a replica of. */
  async function openTarget(link) {
    let share = String(link || '').trim();
    if (share.startsWith('qs:')) share = share.slice(3);
    const r = await api('/api/public/open', {
      method: 'POST', body: JSON.stringify({ link: share }),
    });
    return r.id;
  }

  /**
   * Enter a catalog level.
   * @param {string} link a share link, or '' when spaceId is given
   * @param {string} title what the card called it
   * @param {string} [spaceId] when the space is already open
   */
  async function enter(link, title, spaceId) {
    let id = spaceId;
    try {
      if (!id) id = await openTarget(link);
    } catch (err) {
      note(t('cat.unreachable', { why: err.message }));
      return;
    }
    // The cycle guard is scoped to THIS PATH and says so. There is no global
    // graph here and pretending to detect one would be a claim about the
    // network that a client walking one branch cannot make.
    if (path.some(l => l.id === id)) {
      note(t('cat.cycle', { title }));
      return;
    }
    path.push({ id, title: title || id.slice(0, 8) });
    await render();
  }

  function back() {
    path.pop();
    if (!path.length) { close(); return; }
    render();
  }

  function close() {
    path = [];
    const s = screen();
    if (s) s.style.display = 'none';
    const c = document.getElementById('content');
    if (c) c.style.display = '';
  }

  function note(text) {
    const box = document.getElementById('catNote');
    if (!box) return;
    box.textContent = text;
    box.hidden = !text;
  }

  /** Cards of one level, cached so re-entering does not re-fetch. */
  async function cardsOf(id) {
    const hit = cache[id];
    if (hit && Date.now() - hit.at < CACHE_MS) return hit.cards;
    const r = await api(`/api/spaces/${id}/publications`);
    const cards = (r.publications || []).filter(p => p.kind === 'space');
    cache[id] = { at: Date.now(), cards };
    return cards;
  }

  async function render() {
    const s = screen();
    if (!s) return;
    const c = document.getElementById('content');
    if (c) c.style.display = 'none';
    s.style.display = '';
    note('');

    const here = path[path.length - 1];
    const crumbs = document.getElementById('catCrumbs');
    crumbs.innerHTML = '';
    path.forEach((l, i) => {
      const b = document.createElement('button');
      b.className = 'btn-plain';
      b.textContent = l.title;
      b.disabled = i === path.length - 1;
      b.onclick = () => { path = path.slice(0, i + 1); render(); };
      crumbs.appendChild(b);
      if (i < path.length - 1) {
        const sep = document.createElement('span');
        sep.className = 'cat-sep'; sep.textContent = '›';
        crumbs.appendChild(sep);
      }
    });

    const list = document.getElementById('catList');
    list.innerHTML = `<div class="nav-note">${esc(t('cat.loading'))}</div>`;
    let cards;
    try {
      cards = await cardsOf(here.id);
    } catch (err) {
      list.innerHTML = '';
      note(t('cat.unreachable', { why: err.message }));
      return;
    }
    list.innerHTML = '';
    if (!cards.length) {
      list.innerHTML = `<div class="nav-note">${esc(t('cat.empty'))}</div>`;
      return;
    }
    for (const card of cards) list.appendChild(row(card));
  }

  /** One entry. Every claim on it is labelled as one. */
  function row(card) {
    // The list carries the target directly; spaceCardLink is the fallback
    // for a full document, which is where it is still used.
    const link = card.link || spaceCardLink(card);
    const claimsCatalog = (card.tags || []).includes('catalog');
    const st = spaceCardStatus(link);

    const d = document.createElement('div');
    d.className = 'cat-row';
    const head = document.createElement('div');
    head.className = 'cat-head';
    const title = document.createElement('span');
    title.className = 'cat-title';
    title.textContent = card.title || t('cat.untitled');
    head.appendChild(title);
    if (claimsCatalog) {
      const chip = document.createElement('span');
      chip.className = 'tag-chip cat-claim';
      // "says" — the author's word, not a fact this node checked.
      chip.textContent = t('cat.claims_catalog');
      chip.title = t('cat.claims_catalog_why');
      head.appendChild(chip);
    }
    const status = document.createElement('span');
    status.className = 'cat-status';
    status.textContent = st.text;
    head.appendChild(status);
    d.appendChild(head);

    if (card.summary) {
      const sum = document.createElement('div');
      sum.className = 'cat-sum';
      sum.textContent = card.summary;
      d.appendChild(sum);
    }

    const acts = document.createElement('div');
    acts.className = 'cat-acts';
    // Both actions are always offered, because which one is right cannot be
    // known from here: "look inside" is what proves an entry is a catalog.
    const into = document.createElement('button');
    into.className = 'btn-plain';
    into.textContent = t('cat.look_inside');
    into.onclick = () => enter(link, card.title || '');
    const open = document.createElement('button');
    open.className = 'btn-plain';
    open.textContent = t('cat.open_space');
    open.onclick = async () => {
      try {
        const id = await openTarget(link);
        close();
        NAV.onOpen(id);
      } catch (err) { note(t('cat.unreachable', { why: err.message })); }
    };
    const add = document.createElement('button');
    add.className = 'btn-plain';
    add.textContent = t('cat.add');
    add.onclick = async () => {
      try {
        const id = await openTarget(link);
        await NAV.addRef(id, card.title || '', claimsCatalog ? 'catalogs' : 'pins');
        add.textContent = t('cat.added');
        add.disabled = true;
      } catch (err) { note(t('cat.unreachable', { why: err.message })); }
    };
    acts.append(into, open, add);
    d.appendChild(acts);
    return d;
  }

  /** Discover and the Catalogs section are the same door. */
  async function openRoot(link, title) {
    path = [];
    await enter(link, title || t('cat.root'));
  }

  return { openRoot, enter, back, close, get path() { return path.slice(); } };
})();

if (typeof window !== 'undefined') window.CAT = CAT;
