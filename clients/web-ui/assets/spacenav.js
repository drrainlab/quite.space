// Space navigation: the views a space offers, in the order that space
// thinks about them.
//
// The old form was a segmented control of five glass pills, sitting
// beside the title and looking like a <select>: it hid the top-level
// architecture (a person could not see what a space can even do), and it
// put the space's OWN way of being — the map for a field space, the
// bench for a workshop — on the same footing as views that space may
// never open.
//
// SPACE MODE DECIDES (ADR-029, extended here from order to visibility).
// A space declares what it is centred on; that declaration orders the
// tabs, chooses which are worth a tab at all, and names the one creation
// act that belongs to the view on screen. Nothing below the UI reads any
// of it — the protocol has no idea these tabs exist, and a space whose
// mode this build cannot read simply gets the ordinary order.
//
// The composition is computed WHEN A SPACE OPENS, never while somebody
// works in it: navigation that rearranges itself under a moving hand is
// worse than navigation that is merely imperfect.

// navBuiltFor is the space the current strip was built for. The rebuild
// is keyed on it so a poll tick — which calls the header refresh every
// couple of seconds — cannot rearrange the tabs mid-gesture.
let navBuiltFor = null;

// Order per declared centre. The centre's own view leads; the rest
// follow in the order that mode tends to reach for them.
const NAV_ORDER = {
  chat: ['chat', 'posts', 'shelf', 'objects', 'field'],
  objects: ['objects', 'chat', 'posts', 'shelf', 'field'],
  field: ['field', 'chat', 'objects', 'posts', 'shelf'],
  // A studio's works ARE objects (releases → tracks → sessions), so the
  // objects view is what "audio" centres on.
  audio: ['objects', 'chat', 'shelf', 'posts', 'field'],
  telemetry: ['chat', 'objects', 'posts', 'shelf', 'field'],
  members: ['chat', 'posts', 'shelf', 'objects', 'field'],
};

// How many views earn a tab; the rest live behind "···". Four is what
// fits a phone without wrapping, and the fifth is nearly always the one
// this space does not use.
const NAV_TABS = 4;

// The one creation act that belongs to each view. Chat has none — its
// composer is already at the bottom of the screen, and a second "write"
// button above would be a decoy. The shelf has none either: things
// arrive on it from elsewhere.
const NAV_PRIMARY = {
  posts: { label: 'ui.nav.act.post', act: () => openComposer() },
  objects: { label: 'ui.nav.act.object', act: () => openObjectEditor(null) },
  field: { label: 'ui.nav.act.marker', act: () => fieldArmMarker() },
};

// A quiet glyph per centre, used in the header and in the space list.
// Marks, not stickers: they take the text's colour and weight.
const NAV_MODE_GLYPH = {
  chat: '', objects: '▣', field: '⌖', audio: '♫', telemetry: '∿', members: '◇',
};

function navCentre(char) {
  const c = char && char.central;
  return NAV_ORDER[c] ? c : 'chat';
}

function navModeGlyph(char) {
  return NAV_MODE_GLYPH[navCentre(char)] || '';
}

// applySpaceNav rebuilds the tab strip for the space being opened.
// Called from the space-header refresh, which already holds the
// character; it returns the landing view so the caller can decide
// whether a fresh space should open on it.
function applySpaceNav(char) {
  const sw = document.getElementById('viewSwitch');
  if (!sw) return 'chat';
  const order = NAV_ORDER[navCentre(char)];
  const shown = order.slice(0, NAV_TABS);
  const hidden = order.slice(NAV_TABS);

  sw.innerHTML = '';
  for (const v of shown) sw.appendChild(navTab(v));

  if (hidden.length) {
    // "···" is the overflow, not a menu of extras: everything in it is a
    // real view, and opening one moves it into the strip for as long as
    // this space is open — the person's own use outranks the mode's
    // guess about what they need.
    const more = document.createElement('button');
    more.type = 'button';
    more.className = 'space-tab space-more';
    more.textContent = '···';
    more.dataset.hidden = hidden.join(',');
    more.title = hidden.map(v => t('ui.convbar.view.' + v)).join(' · ');
    more.onclick = (ev) => {
      ev.stopPropagation();
      navOpenOverflow(more, hidden);
    };
    sw.appendChild(more);
  }
  navMarkActive(typeof pubView !== 'undefined' ? pubView : 'chat');
  return order[0];
}

function navTab(v) {
  const b = document.createElement('button');
  b.type = 'button';
  b.className = 'space-tab';
  b.dataset.v = v;
  b.textContent = t('ui.convbar.view.' + v);
  b.onclick = () => switchView(v);
  return b;
}

function navOpenOverflow(anchor, hidden) {
  document.querySelectorAll('.space-more-menu').forEach(m => m.remove());
  const menu = document.createElement('div');
  menu.className = 'space-more-menu';
  for (const v of hidden) {
    const item = document.createElement('button');
    item.type = 'button';
    item.textContent = t('ui.convbar.view.' + v);
    item.onclick = () => {
      menu.remove();
      // Promote it: a view somebody actually opened stops being an
      // afterthought for the rest of this visit.
      const sw = document.getElementById('viewSwitch');
      const more = sw.querySelector('.space-more');
      if (sw && more && !sw.querySelector(`.space-tab[data-v="${v}"]`)) {
        sw.insertBefore(navTab(v), more);
      }
      switchView(v);
    };
    menu.appendChild(item);
  }
  const r = anchor.getBoundingClientRect();
  menu.style.left = Math.round(r.left) + 'px';
  menu.style.top = Math.round(r.bottom + 4) + 'px';
  document.body.appendChild(menu);
  setTimeout(() => {
    const off = () => { menu.remove(); document.removeEventListener('click', off); };
    document.addEventListener('click', off);
  }, 0);
}

// navRelabel: the strip is built once per space with the language of that
// moment, and a language switch used to leave it there — "чат · посты"
// under an English interface, "Chat · Posts" under a Russian one, with
// only a tab promoted later wearing the new tongue. Relabel in place:
// the tabs, the "···" hint, and the view's creation button.
function navRelabel() {
  document.querySelectorAll('#viewSwitch .space-tab[data-v]').forEach(b => {
    b.textContent = t('ui.convbar.view.' + b.dataset.v);
  });
  const more = document.querySelector('#viewSwitch .space-more');
  if (more && more.dataset.hidden) {
    more.title = more.dataset.hidden.split(',').filter(Boolean).map(v => t('ui.convbar.view.' + v)).join(' · ');
  }
  const sel = document.querySelector('#viewSwitch .space-tab.sel');
  if (sel && sel.dataset.v) navApplyPrimary(sel.dataset.v);
}

// navMarkActive: the active tab is brighter text and a thin line, not a
// filled pill. The strip should read as part of the space, not as a row
// of controls stuck on top of it.
function navMarkActive(v) {
  document.querySelectorAll('#viewSwitch .space-tab').forEach(b =>
    b.classList.toggle('sel', b.dataset.v === v));
  navApplyPrimary(v);
}

// navApplyPrimary swaps the header's one creation button to whatever the
// view on screen actually creates. A "New post" button hanging over a
// map was an action from somebody else's context.
function navApplyPrimary(v) {
  const btn = document.getElementById('viewPrimary');
  if (!btn) return;
  // The directory's own act outranks the view's: adding a space IS what
  // a catalog is for (CAT-0b), and it is offered wherever you stand.
  const addSpace = document.getElementById('addSpaceBtn');
  if (addSpace && addSpace.dataset.live === '1') {
    btn.style.display = 'none';
    return;
  }
  const p = NAV_PRIMARY[v];
  // Read-only is not a reason to hide the map or the objects — only the
  // acts. A visitor still sees everything; they are simply not offered a
  // door that answers 403.
  const writable = !document.body.classList.contains('space-readonly');
  if (!p || !writable) { btn.style.display = 'none'; return; }
  btn.style.display = '';
  btn.textContent = '+ ' + t(p.label);
  btn.onclick = () => p.act();
}
