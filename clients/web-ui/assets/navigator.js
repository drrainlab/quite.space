// @ts-check
// The Navigator (NAV-1): the left panel, in sections a person arranged.
//
// Two properties are load-bearing and everything else follows from them.
//
// ONE. Rows are references, never copies. A row's name, glyph and meta come
// from the LIVE space list every time it is drawn; the stored arrangement
// holds terminal ids and an order. So a pinned conversation that grows into
// a group simply renders as a group — a projection changing is not a
// reference breaking — and a pin to a space you left renders as a dangling
// row rather than disappearing.
//
// TWO. The panel is never rebuilt. The shell polls, and the old sidebar did
// `innerHTML = ''` on every tick, which is fine for a list nobody touches
// and fatal for one with a search field, a drag in progress, collapsed
// sections and keyboard focus. So render() reconciles: stable nodes keyed by
// (section, terminal), replaced only when their signature changes. The same
// idea as refreshSpace()'s incremental feed, for the same reason — do not
// tear down something a person is in the middle of using.

/** @typedef {{terminal:string,label?:string}} NavRef */
/** @typedef {{id:string,title:string,children:NavRef[],collapsed?:boolean}} NavGroup */
/** @typedef {{version:number,pins:NavRef[],groups:NavGroup[],catalogs:NavRef[],recent:NavRef[],collapsed:string[]}} NavState */

const NAV = (() => {
  /** @type {NavState} */
  let state = { version: 0, pins: [], groups: [], catalogs: [], recent: [], collapsed: [] };
  /**
   * A VIEW is one place the Navigator is drawn. There are two: the sidebar,
   * and the share picker. They are the same code with a different section
   * list and one flag — which is the whole reason the Navigator existed
   * before sharing did. A second list would drift from this one by the
   * second week.
   * @typedef {{root:HTMLElement, sections:string[], select:boolean,
   *            query:string, picked:Set<string>, onPick?:()=>void}} View
   */
  /** @type {View[]} */
  let views = [];
  /** @type {any[]} */
  let spaces = [];
  let currentId = '';
  // Interaction locks. While any of these is true a poll must not reconcile:
  // the browser owns the drag, and a menu or an inline input is a thing
  // somebody is looking at.
  let dragging = false, menuOpen = false, editing = false;
  let pendingPaint = false;

  const SIDEBAR_SECTIONS = ['pinned', 'groups', 'spaces', 'people', 'catalogs'];
  // The picker offers where a message can GO, so: no catalogs (a catalog is
  // for finding places, not for putting things in), and Recent first,
  // because the last few destinations are most of the answer.
  const PICKER_SECTIONS = ['recent', 'pinned', 'groups', 'spaces', 'people', 'ai'];

  // ---- state plumbing ----

  /** @param {any} s */
  function normalize(s) {
    return {
      version: s.version || 0,
      pins: s.pins || [], groups: (s.groups || []).map(g => ({ ...g, children: g.children || [] })),
      catalogs: s.catalogs || [], recent: s.recent || [], collapsed: s.collapsed || [],
    };
  }

  async function load() {
    try { state = normalize(await api('/api/navigator')); } catch { /* first run */ }
  }

  // save() is optimistic: the local mutation has already painted, and this
  // only makes it durable. A 409 means another window moved first — the only
  // honest answer is to take their arrangement rather than clobber it.
  //
  // Writes are SERIALISED through one chain. The base version exists to
  // catch another window; it should never fire on this one, and it did:
  // two clicks in quick succession sent two PUTs from the same version, the
  // second lost the race and its change was thrown away with it. A queue is
  // the fix — the guard stays for the case it is actually for.
  let writing = Promise.resolve();
  function save() {
    writing = writing.then(async () => {
      try {
        state = normalize(await api('/api/navigator',
          { method: 'PUT', body: JSON.stringify(state) }));
      } catch (err) {
        if (String(err && err.message).includes('changed since')) await load();
        else console.warn('navigator: ' + (err && err.message));
      }
      paintAll();
    });
    return writing;
  }

  // ---- the reconciler ----
  //
  // Keys are SECTION-PREFIXED because one space legitimately appears in
  // Pinned, in two groups and in Spaces at once. Bodies are reconciled
  // separately, so a collision is already impossible in practice; the prefix
  // makes it impossible in principle and makes the DOM readable.

  /**
   * @param {HTMLElement} box
   * @param {{key:string,sig:string,build:()=>HTMLElement}[]} rows
   */
  function reconcile(box, rows) {
    const index = new Map();
    for (const el of Array.from(box.children)) {
      index.set(/** @type {HTMLElement} */(el).dataset.navKey, el);
    }
    const want = new Set(rows.map(r => r.key));
    for (const [k, el] of index) {
      if (!want.has(k)) { el.remove(); index.delete(k); }
    }
    let cursor = box.firstElementChild;
    for (const r of rows) {
      let el = index.get(r.key);
      if (el && /** @type {HTMLElement} */(el).dataset.navSig !== r.sig) {
        // Refocus after replacing: without this, arrow-key navigation dies
        // the moment a message arrives anywhere.
        const hadFocus = el.contains(document.activeElement);
        const fresh = r.build();
        if (el === cursor) cursor = cursor.nextElementSibling;
        el.replaceWith(fresh);
        el = fresh; index.set(r.key, fresh);
        if (hadFocus) fresh.focus();
      } else if (!el) {
        el = r.build(); index.set(r.key, el);
      }
      if (el === cursor) cursor = /** @type {any} */(cursor).nextElementSibling;
      else box.insertBefore(el, cursor);
    }
  }

  // ---- resolving refs against live data ----

  /** @param {string} tid */
  function spaceOf(tid) { return spaces.find(s => s.id === tid); }

  /** @param {NavRef} ref */
  function refName(ref) {
    const sp = spaceOf(ref.terminal);
    return sp ? spaceName(sp) : (ref.label || ref.terminal.slice(0, 8));
  }

  /** @param {any} sp @param {string} q */
  function matches(sp, q) {
    if (!q) return true;
    const hay = [spaceName(sp), sp.title || '', sp.display_title || '', sp.id]
      .join(' ').toLowerCase();
    return hay.includes(q);
  }

  /** @param {NavRef} ref @param {string} q */
  function refMatches(ref, q) {
    if (!q) return true;
    const sp = spaceOf(ref.terminal);
    if (sp) return matches(sp, q);
    return (ref.label || '').toLowerCase().includes(q) ||
      ref.terminal.toLowerCase().startsWith(q);
  }

  // ---- rows ----

  /**
   * A live row. Its signature is everything the row SHOWS, so an unrelated
   * message elsewhere repaints nothing.
   * @param {View} v @param {any} sp @param {string} section @param {string} owner
   */
  function spaceRow(v, sp, section, owner) {
    const key = section + (owner ? '/' + owner : '') + '/' + sp.id;
    const name = spaceName(sp);
    const picked = v.select && v.picked.has(sp.id);
    // A place you cannot write to is not a destination — and saying so
    // where the choice is made beats refusing after somebody has decided.
    const closed = v.select && sp.can_write === false;
    const sig = [name, sp.events, sp.undecryptable || 0, sp.owned ? 1 : 0,
      sp.frozen ? 1 : 0, sp.id === currentId ? 1 : 0,
      picked ? 1 : 0, closed ? 1 : 0, v.select ? 1 : 0, LOCALE].join('');
    return {
      key, sig, build: () => {
        const d = document.createElement('div');
        d.dataset.navKey = key; d.dataset.navSig = sig;
        d.className = 'space' + (!v.select && sp.id === currentId ? ' active' : '') +
          (picked ? ' picked' : '') + (closed ? ' nav-closed' : '');
        d.setAttribute('role', v.select ? 'checkbox' : 'button');
        if (v.select) d.setAttribute('aria-checked', String(picked));
        d.tabIndex = 0;
        const an = ARCHETYPES[sp.character?.archetype]?.name || '';
        const bits = [];
        if (an) bits.push(esc(an));
        bits.push(esc(t('spaces.activity.events', { count: sp.events })));
        if (sp.undecryptable) bits.push(`<span class="undec">${sp.undecryptable}✦</span>`);
        d.innerHTML =
          (v.select ? `<span class="nav-tick">${picked ? '✓' : ''}</span>` : '') +
          (!v.select && (section === 'pinned' || owner)
            ? '<span class="nav-grip" title="drag to reorder">⠿</span>' : '') +
          `<span class="space-av"><span class="glyph g24">${glyphSVG(sp.id, sp.ai ? 'bot' : 'space', 24)}</span></span>` +
          `<span class="space-main"><span class="space-top">` +
            `<span class="t">${esc(name)}</span>` +
            (sp.owned ? `<span class="owner-tag">${esc(t('spaces.owner'))}</span>` : '') +
          `</span><span class="m">${closed ? esc(t('share.closed')) : bits.join(' · ')}</span></span>` +
          (v.select ? '' : `<button class="nav-more" aria-label="${esc(t('nav.more'))}">⋯</button>`);
        if (v.select) {
          d.onclick = () => { if (!closed) togglePick(v, sp.id); };
          d.onkeydown = (e) => {
            if (e.key !== 'Enter' && e.key !== ' ') return;
            e.preventDefault(); if (!closed) togglePick(v, sp.id);
          };
          return d;
        }
        d.onclick = (e) => {
          if (/** @type {HTMLElement} */(e.target).closest('.nav-more, .nav-grip')) return;
          NAV.onOpen(sp.id);
        };
        d.onkeydown = (e) => onRowKey(e, sp.id, section, owner);
        const more = /** @type {HTMLElement} */(d.querySelector('.nav-more'));
        more.onclick = (e) => { e.stopPropagation(); openRowMenu(more, sp.id, section, owner); };
        if (section === 'pinned' || owner) wireDrag(d, section, owner);
        return d;
      },
    };
  }

  /**
   * A row whose terminal this node no longer has. It is NOT swept: a relay
   * catching up or a restored backup brings the space back, and the pin
   * should still be there. So it says what it is and offers one action.
   * @param {NavRef} ref @param {string} section @param {string} owner
   */
  function danglingRow(v, ref, section, owner) {
    const key = section + (owner ? '/' + owner : '') + '/' + ref.terminal;
    const sig = 'gone' + (ref.label || '') + '' + LOCALE;
    return {
      key, sig, build: () => {
        const d = document.createElement('div');
        d.dataset.navKey = key; d.dataset.navSig = sig;
        // Never selectable: you cannot send anything to a space you left.
        d.className = 'space nav-dangling' + (v.select ? ' nav-closed' : '');
        d.innerHTML =
          `<span class="space-av"><span class="glyph g24">${glyphSVG(ref.terminal, 'space', 24)}</span></span>` +
          `<span class="space-main"><span class="space-top">` +
            `<span class="t">${esc(ref.label || ref.terminal.slice(0, 8))}</span>` +
          `</span><span class="m">${esc(t('nav.gone'))}</span></span>` +
          `<button class="nav-more" aria-label="${esc(t('nav.remove'))}">✕</button>`;
        /** @type {HTMLElement} */(d.querySelector('.nav-more')).onclick = (e) => {
          e.stopPropagation();
          removeRef(ref.terminal, section, owner);
        };
        return d;
      },
    };
  }

  /**
   * A saved catalog. It opens the WALKER rather than the space: a catalog
   * is a place you look inside, and opening it as an ordinary room is the
   * dead end this replaces.
   * @param {NavRef} ref
   */
  function catalogRow(v, ref) {
    const base = refRow(v, ref, 'catalogs', '');
    return {
      key: base.key, sig: base.sig + '|cat', build: () => {
        const el = base.build();
        el.onclick = (e) => {
          if (/** @type {HTMLElement} */(e.target).closest('.nav-more')) return;
          CAT.enter('', refName(ref), ref.terminal);
        };
        return el;
      },
    };
  }

  /** @param {NavRef} ref @param {string} section @param {string} owner */
  function refRow(v, ref, section, owner) {
    const sp = spaceOf(ref.terminal);
    return sp ? spaceRow(v, sp, section, owner) : danglingRow(v, ref, section, owner);
  }

  // ---- mutations ----

  function isPinned(tid) { return state.pins.some(p => p.terminal === tid); }

  function togglePin(tid) {
    if (isPinned(tid)) state.pins = state.pins.filter(p => p.terminal !== tid);
    else state.pins.push({ terminal: tid, label: refName({ terminal: tid }) });
    paintAll(); save();
  }

  function removeRef(tid, section, owner) {
    if (section === 'pinned') state.pins = state.pins.filter(p => p.terminal !== tid);
    else if (section === 'catalogs') state.catalogs = state.catalogs.filter(p => p.terminal !== tid);
    else if (owner) {
      const g = state.groups.find(x => x.id === owner);
      if (g) g.children = g.children.filter(c => c.terminal !== tid);
    }
    paintAll(); save();
  }

  function addToGroup(tid, gid) {
    const g = state.groups.find(x => x.id === gid);
    if (!g || g.children.some(c => c.terminal === tid)) return;
    g.children.push({ terminal: tid, label: refName({ terminal: tid }) });
    paintAll(); save();
  }

  function newGroup(firstMember) {
    const title = prompt(t('nav.group.name'));
    if (title === null) return;
    const clean = title.trim();
    if (!clean) return;
    const gid = 'g' + Math.random().toString(16).slice(2, 10);
    state.groups.push({
      id: gid, title: clean,
      children: firstMember ? [{ terminal: firstMember, label: refName({ terminal: firstMember }) }] : [],
    });
    paintAll(); save();
  }

  function renameGroup(gid) {
    const g = state.groups.find(x => x.id === gid);
    if (!g) return;
    const title = prompt(t('nav.group.name'), g.title);
    if (title === null) return;
    const clean = title.trim();
    if (!clean) return;
    g.title = clean;
    paintAll(); save();
  }

  function deleteGroup(gid) {
    const g = state.groups.find(x => x.id === gid);
    if (!g) return;
    // Say what is actually being destroyed: the list, not the rooms.
    if (!confirm(t('nav.group.delete_confirm', { title: g.title }))) return;
    state.groups = state.groups.filter(x => x.id !== gid);
    paintAll(); save();
  }

  /** @param {string} listId @param {number} from @param {number} to */
  function reorder(listId, from, to) {
    const list = listId === 'pinned' ? state.pins : (state.groups.find(g => g.id === listId) || {}).children;
    if (!list || from === to || from < 0 || from >= list.length) return;
    const [item] = list.splice(from, 1);
    list.splice(to > from ? to - 1 : to, 0, item);
    paintAll(); save();
  }

  function moveGroup(gid, delta) {
    const i = state.groups.findIndex(g => g.id === gid);
    const j = i + delta;
    if (i < 0 || j < 0 || j >= state.groups.length) return;
    const [g] = state.groups.splice(i, 1);
    state.groups.splice(j, 0, g);
    paintAll(); save();
  }

  function toggleSection(id) {
    state.collapsed = state.collapsed.includes(id)
      ? state.collapsed.filter(x => x !== id) : state.collapsed.concat(id);
    paintAll(); save();
  }

  function toggleGroup(gid) {
    const g = state.groups.find(x => x.id === gid);
    if (!g) return;
    g.collapsed = !g.collapsed;
    paintAll(); save();
  }

  /**
   * Put a terminal into the panel. The one entry point for anything outside
   * this file — the catalog walker uses it, and so will the share picker.
   * @param {string} tid @param {string} label @param {'pins'|'catalogs'} list
   */
  async function addRef(tid, label, list) {
    const target = list === 'catalogs' ? state.catalogs : state.pins;
    if (target.some(r => r.terminal === tid)) return;
    target.push({ terminal: tid, label: label || refName({ terminal: tid }) });
    paintAll();
    await save();
  }

  /** @param {View} v @param {string} tid */
  function togglePick(v, tid) {
    if (v.picked.has(tid)) v.picked.delete(tid);
    else v.picked.add(tid);
    paintView(v);
    if (v.onPick) v.onPick();
  }

  /** Remember where things were sent, for the share picker (SHARE-0). */
  function noteRecent(tid) {
    state.recent = [{ terminal: tid, label: refName({ terminal: tid }) }]
      .concat(state.recent.filter(r => r.terminal !== tid)).slice(0, 8);
    save();
  }

  // ---- drag, and the two things that are not drag ----

  /** @param {HTMLElement} row @param {string} section @param {string} owner */
  function wireDrag(row, section, owner) {
    const listId = owner || section;
    const grip = /** @type {HTMLElement} */(row.querySelector('.nav-grip'));
    if (!grip) return;
    // Grip-gated: the row is only draggable while the grip is held, so an
    // ordinary click or a text selection never starts a drag. Same shape as
    // the publication composer's block reorder.
    grip.onmousedown = () => { row.draggable = true; };
    grip.onclick = (e) => {
      e.stopPropagation();
      // Touch has no HTML5 drag at all, so the grip must also be a button.
      if (window.matchMedia('(hover: none)').matches) openMoveMenu(grip, row, listId);
    };
    row.addEventListener('dragstart', (e) => {
      dragging = true;
      /** @type {DragEvent} */(e).dataTransfer?.setData('text/qp-nav',
        listId + ':' + rowIndex(row));
    });
    row.addEventListener('dragend', () => {
      dragging = false; row.draggable = false;
      row.parentElement?.querySelectorAll('.drop-above')
        .forEach(n => n.classList.remove('drop-above'));
      if (pendingPaint) { pendingPaint = false; paintAll(); }
    });
    row.addEventListener('dragover', (e) => {
      e.preventDefault(); row.classList.add('drop-above');
    });
    row.addEventListener('dragleave', () => row.classList.remove('drop-above'));
    row.addEventListener('drop', (e) => {
      e.preventDefault(); row.classList.remove('drop-above');
      const raw = /** @type {DragEvent} */(e).dataTransfer?.getData('text/qp-nav') || '';
      const [srcList, idx] = raw.split(':');
      if (srcList !== listId) return; // moving BETWEEN lists is not reordering
      reorder(listId, Number(idx), rowIndex(row));
    });
  }

  /** @param {HTMLElement} row */
  function rowIndex(row) {
    return Array.from(row.parentElement?.children || []).indexOf(row);
  }

  /** Keyboard equivalent, so reordering never needs a mouse. */
  function onRowKey(e, tid, section, owner) {
    if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); NAV.onOpen(tid); return; }
    const listId = owner || section;
    if ((section !== 'pinned' && !owner) || !e.altKey) return;
    const row = /** @type {HTMLElement} */(e.currentTarget);
    if (e.key === 'ArrowUp') { e.preventDefault(); reorder(listId, rowIndex(row), rowIndex(row) - 1); }
    if (e.key === 'ArrowDown') { e.preventDefault(); reorder(listId, rowIndex(row), rowIndex(row) + 2); }
  }

  // ---- menus ----

  function closeMenus() {
    document.querySelectorAll('.nav-menu').forEach(n => n.remove());
    menuOpen = false;
    if (pendingPaint) { pendingPaint = false; paintAll(); }
  }

  /** @param {HTMLElement} anchor @param {{label:string,run:()=>void}[]} items */
  function openMenu(anchor, items) {
    closeMenus();
    const m = document.createElement('div');
    m.className = 'nav-menu';
    for (const it of items) {
      const b = document.createElement('button');
      b.textContent = it.label;
      b.onclick = (e) => { e.stopPropagation(); closeMenus(); it.run(); };
      m.appendChild(b);
    }
    const r = anchor.getBoundingClientRect();
    m.style.top = Math.round(r.bottom + 4) + 'px';
    m.style.left = Math.round(Math.min(r.left, window.innerWidth - 220)) + 'px';
    document.body.appendChild(m);
    menuOpen = true;
    setTimeout(() => document.addEventListener('click', closeMenus, { once: true }), 0);
  }

  function openRowMenu(anchor, tid, section, owner) {
    const items = [];
    items.push({
      label: isPinned(tid) ? t('nav.unpin') : t('nav.pin'),
      run: () => togglePin(tid),
    });
    for (const g of state.groups) {
      if (g.children.some(c => c.terminal === tid)) continue;
      items.push({ label: t('nav.add_to', { title: g.title }), run: () => addToGroup(tid, g.id) });
    }
    items.push({ label: t('nav.group.new'), run: () => newGroup(tid) });
    if (owner) items.push({ label: t('nav.remove_from_group'), run: () => removeRef(tid, section, owner) });
    openMenu(anchor, items);
  }

  function openMoveMenu(anchor, row, listId) {
    const i = rowIndex(row);
    openMenu(anchor, [
      { label: t('nav.move_up'), run: () => reorder(listId, i, i - 1) },
      { label: t('nav.move_down'), run: () => reorder(listId, i, i + 2) },
      { label: t('nav.move_top'), run: () => reorder(listId, i, 0) },
    ]);
  }

  /** Is this a place somebody is typing into? @param {any} el */
  function isTextField(el) {
    if (!el || !el.tagName) return false;
    return /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName) || el.isContentEditable === true;
  }

  // ---- the static chrome, built ONCE ----

  /**
   * Build one view's chrome, ONCE. Nothing here is rebuilt by a paint: the
   * search field keeps its text, caret and focus across every tick because
   * it is never touched again.
   * @param {View} v
   */
  function buildChrome(v) {
    v.root.innerHTML = '';
    // The field lives in a small wrapper so the magnifier and the clear
    // button can be OURS: both inherit the theme, where the browser's own
    // search chrome does not and showed up as a white box on a dark panel.
    const wrap = document.createElement('div');
    wrap.className = 'nav-search-wrap';
    const search = document.createElement('input');
    search.className = 'nav-search';
    // type=text, not search: the native widget brings a cancel button that
    // cannot be themed, and we render our own below.
    search.type = 'text';
    search.autocomplete = 'off';
    search.spellcheck = false;
    search.placeholder = v.select ? t('share.search') : t('nav.search');
    if (!v.select) search.id = 'navSearch';
    const clear = document.createElement('button');
    clear.className = 'nav-search-clear';
    clear.type = 'button';
    clear.textContent = '×';
    clear.setAttribute('aria-label', t('nav.search.clear'));
    clear.hidden = true;
    const apply = () => {
      v.query = search.value.trim().toLowerCase();
      clear.hidden = search.value === '';
      paintView(v);
    };
    // Filtering is synchronous over cached data. It must never wait on the
    // poll — especially once the shell's tick slows down.
    search.oninput = apply;
    search.onkeydown = (e) => {
      if (e.key === 'Escape') { search.value = ''; apply(); search.blur(); }
    };
    clear.onclick = () => { search.value = ''; apply(); search.focus(); };
    wrap.append(search, clear);
    v.root.appendChild(wrap);

    for (const id of v.sections) {
      const sec = document.createElement('div');
      sec.className = 'nav-sec';
      sec.dataset.sec = id;
      const head = document.createElement('div');
      head.className = 'nav-sec-head';
      head.innerHTML = `<span class="nav-caret">▾</span><span class="nav-sec-t"></span>` +
        `<span class="nav-count"></span>`;
      head.onclick = () => { if (v.select) toggleLocal(v, id); else toggleSection(id); };
      if (id === 'groups' && !v.select) {
        const add = document.createElement('button');
        add.className = 'nav-sec-add';
        add.textContent = '+';
        add.onclick = (e) => { e.stopPropagation(); newGroup(''); };
        head.appendChild(add);
      }
      const body = document.createElement('div');
      body.className = 'nav-sec-body';
      sec.append(head, body);
      v.root.appendChild(sec);
    }
    return search;
  }

  // A picker's collapse is a passing preference, not part of somebody's
  // arrangement — so it lives on the view and is never persisted.
  /** @param {View} v @param {string} id */
  function toggleLocal(v, id) {
    v.localCollapsed = v.localCollapsed || new Set();
    if (v.localCollapsed.has(id)) v.localCollapsed.delete(id);
    else v.localCollapsed.add(id);
    paintView(v);
  }

  function mount(el) {
    /** @type {View} */
    const v = { root: el, sections: SIDEBAR_SECTIONS, select: false,
      query: '', picked: new Set() };
    views = views.filter(x => x.select);
    views.unshift(v);
    const search = buildChrome(v);

    // "/" focuses search from anywhere that is not already a text field.
    //
    // The guard asks about the event's TARGET as well as the focused
    // element: the target is what would actually receive the character, and
    // stealing a slash out of somebody's sentence is exactly the failure a
    // single-key shortcut invites.
    document.addEventListener('keydown', (e) => {
      if (e.key !== '/' || e.metaKey || e.ctrlKey) return;
      if (isTextField(e.target) || isTextField(document.activeElement)) return;
      e.preventDefault(); search.focus();
    });

    load().then(paintAll);
  }

  /**
   * Draw the Navigator somewhere else, in selection mode. The share picker
   * is this and nothing more.
   * @param {HTMLElement} el @param {()=>void} onPick
   */
  function mountPicker(el, onPick) {
    /** @type {View} */
    const v = { root: el, sections: PICKER_SECTIONS, select: true,
      query: '', picked: new Set(), onPick };
    views = views.filter(x => !x.select);
    views.push(v);
    const search = buildChrome(v);
    paintView(v);
    return {
      view: v,
      focus: () => search.focus(),
      picked: () => Array.from(v.picked),
      clear: () => { v.picked.clear(); paintView(v); },
      // Drop the view AND its rows: a closed picker holding a stale list
      // would flash it on the way back in.
      destroy: () => { views = views.filter(x => x !== v); v.root.innerHTML = ''; },
    };
  }

  // ---- painting ----

  function busy() { return dragging || menuOpen || editing; }

  /** @param {any[]} nextSpaces @param {string} nextCurrent */
  function render(nextSpaces, nextCurrent) {
    spaces = nextSpaces || [];
    currentId = nextCurrent || '';
    if (busy()) { pendingPaint = true; return; }
    paintAll();
  }

  function paintAll() { for (const v of views) paintView(v); }

  /** @param {View} v */
  function paintView(v) {
    if (!v.root) return;
    const q = v.query;
    // The AI is not a person and not an ordinary room, so it gets its own
    // line rather than hiding among the spaces.
    const ai = spaces.filter(s => s.ai && matches(s, q));
    /** @type {Record<string, {rows:any[]}>} */
    const content = {
      recent: { rows: state.recent.filter(r => refMatches(r, q)).map(r => refRow(v, r, 'recent', '')) },
      pinned: { rows: state.pins.filter(r => refMatches(r, q)).map(r => refRow(v, r, 'pinned', '')) },
      groups: { rows: [] },
      spaces: { rows: spaces.filter(s => !s.dyad && !s.ai && matches(s, q)).map(s => spaceRow(v, s, 'spaces', '')) },
      people: { rows: spaces.filter(s => s.dyad && matches(s, q)).map(s => spaceRow(v, s, 'people', '')) },
      ai: { rows: ai.map(s => spaceRow(v, s, 'ai', '')) },
      catalogs: { rows: state.catalogs.filter(r => refMatches(r, q)).map(r => catalogRow(v, r)) },
    };

    for (const id of v.sections) {
      const sec = /** @type {HTMLElement} */(v.root.querySelector(`.nav-sec[data-sec="${id}"]`));
      const head = /** @type {HTMLElement} */(sec.querySelector('.nav-sec-head'));
      const body = /** @type {HTMLElement} */(sec.querySelector('.nav-sec-body'));
      /** @type {HTMLElement} */(head.querySelector('.nav-sec-t')).textContent = t('nav.sec.' + id);

      if (id === 'groups') { paintGroups(v, body, q); }
      else { reconcile(body, content[id].rows); }

      const n = id === 'groups' ? state.groups.length : content[id].rows.length;
      /** @type {HTMLElement} */(head.querySelector('.nav-count')).textContent = n ? String(n) : '';
      // A query never hides a section: somebody must be able to see WHERE
      // their thing is not. It dims and shows a zero instead.
      sec.classList.toggle('nav-empty', n === 0);
      // While searching, anything with a match opens — and the stored
      // collapse state is not touched, so clearing the query restores it.
      const collapsed = v.select
        ? (v.localCollapsed && v.localCollapsed.has(id))
        : state.collapsed.includes(id);
      const open = q ? n > 0 : !collapsed;
      sec.classList.toggle('nav-collapsed', !open);
    }

    paintEmptyStates(v);
  }

  /** @param {View} v @param {HTMLElement} body @param {string} q */
  function paintGroups(v, body, q) {
    // Each group is its own titled sub-list with its own body, so a drag
    // inside one group cannot reorder another.
    const wanted = new Set(state.groups.map(g => 'grp:' + g.id));
    for (const el of Array.from(body.children)) {
      const k = /** @type {HTMLElement} */(el).dataset.navKey || '';
      if (!wanted.has(k)) el.remove();
    }
    let cursor = body.firstElementChild;
    for (const g of state.groups) {
      const key = 'grp:' + g.id;
      let box = /** @type {HTMLElement|null} */(body.querySelector(`[data-nav-key="${key}"]`));
      if (!box) {
        box = document.createElement('div');
        box.dataset.navKey = key;
        box.className = 'nav-group';
        box.innerHTML = `<div class="nav-group-head"><span class="nav-caret">▾</span>` +
          `<span class="nav-group-t"></span><button class="nav-more">⋯</button></div>` +
          `<div class="nav-group-body"></div>`;
        /** @type {HTMLElement} */(box.querySelector('.nav-group-head')).onclick = (e) => {
          if (/** @type {HTMLElement} */(e.target).closest('.nav-more')) return;
          toggleGroup(g.id);
        };
      }
      /** @type {HTMLElement} */(box.querySelector('.nav-group-t')).textContent = g.title;
      const more = /** @type {HTMLElement} */(box.querySelector('.nav-more'));
      more.hidden = v.select;
      more.onclick = (e) => {
        e.stopPropagation();
        openMenu(more, [
          { label: t('nav.group.rename'), run: () => renameGroup(g.id) },
          { label: t('nav.move_up'), run: () => moveGroup(g.id, -1) },
          { label: t('nav.move_down'), run: () => moveGroup(g.id, 1) },
          { label: t('nav.group.delete'), run: () => deleteGroup(g.id) },
        ]);
      };
      const gbody = /** @type {HTMLElement} */(box.querySelector('.nav-group-body'));
      const rows = g.children.filter(r => refMatches(r, q)).map(r => refRow(v, r, 'groups', g.id));
      reconcile(gbody, rows);
      box.classList.toggle('nav-collapsed', q ? rows.length === 0 : !!g.collapsed);
      if (box === cursor) cursor = cursor.nextElementSibling;
      else body.insertBefore(box, cursor);
    }
  }

  /** @param {View} v */
  function paintEmptyStates(v) {
    if (!v.root) return;
    const note = (secId, text) => {
      const body = /** @type {HTMLElement|null} */(
        v.root.querySelector(`.nav-sec[data-sec="${secId}"] .nav-sec-body`));
      if (!body) return;
      let n = /** @type {HTMLElement|null} */(body.querySelector('.nav-note'));
      if (!text) { if (n) n.remove(); return; }
      if (!n) { n = document.createElement('div'); n.className = 'nav-note'; body.appendChild(n); }
      n.textContent = text;
      body.appendChild(n); // keep it last
    };
    for (const id of v.sections) note(id, '');
    if (v.query) {
      const any = v.root.querySelectorAll('.nav-sec-body .space').length;
      note('spaces', any ? '' : t('nav.search.none'));
      return;
    }
    note('pinned', state.pins.length ? '' : t('nav.pinned.empty'));
    note('groups', state.groups.length ? '' : t('nav.groups.empty'));
    note('people', spaces.some(s => s.dyad) ? '' : t('nav.people.empty'));
    note('catalogs', state.catalogs.length ? '' : t('nav.catalogs.empty'));
    note('spaces', spaces.length ? '' : t('spaces.empty.body'));
    note('recent', state.recent.length ? '' : t('share.recent.empty'));
    // No assistant yet is a different sentence from an empty list: it is
    // something a person can act on, and it says where to go.
    note('ai', spaces.some(s => s.ai) ? '' : t('share.ai.empty'));
  }

  return {
    mount, mountPicker, render, busy, addRef,
    /** @type {(id:string)=>void} */
    onOpen: () => {},
    noteRecent,
    get state() { return state; },
    reload: load,
  };
})();

if (typeof window !== 'undefined') window.NAV = NAV;
