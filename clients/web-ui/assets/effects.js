// Resonant Surfaces (RP-2A). A reaction may briefly touch the OBJECT it
// landed on (arrival) and leave a calm accumulated trace (residue). Effects
// are derived presentation: the wire carries meaning only; effect ids
// resolve exclusively through this client-owned allowlist. Invariants: no
// layout shift (overlay layer), alpha-capped residue, nothing painted over
// media pixels, static fallback, fully disableable.

const RESFX = (() => {
  // ---- effect registry (allowlist) ----
  // key → {arrival css class, residue painter}. Everything unknown → tint.
  const EFFECTS = {
    'ripple.v1':    { arrival: 'fx-arrive-ripple' },
    'halo.v1':      { arrival: 'fx-arrive-wash' },
    'particles.v1': { arrival: 'fx-arrive-spark' },
    'tint.v1':      { arrival: 'fx-arrive-tint' },
  };
  const KEY_EFFECT = { resonates: 'ripple.v1', warmth: 'halo.v1', spark: 'particles.v1' };

  function effectFor(group) {
    if (group.kind === 'semantic') {
      // Palette may pin an effect id per slot; only allowlisted ids resolve.
      const slot = (RES.palette?.slots || []).find(s => s.key === group.key);
      const id = (slot?.effect_id && EFFECTS[slot.effect_id]) ? slot.effect_id
        : (KEY_EFFECT[group.key] || 'tint.v1');
      return id;
    }
    return 'tint.v1';
  }

  // ---- modes ----
  // effectiveMode = min(space policy, user setting, accessibility, client).
  const MODES = ['off', 'static', 'subtle', 'full'];
  function userMode() { return localStorage.getItem('qp.effects') || 'full'; }
  function effectsMode() {
    let m = userMode();
    if (typeof RES !== 'undefined' && !RES.surfaceEffects()) m = 'off';
    if (window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
      m = minMode(m, 'static');
    }
    if (typeof renderMode === 'function' && renderMode() === 'minimal') {
      m = minMode(m, 'static');
    }
    if (document.hidden) m = minMode(m, 'static'); // background tab: no motion
    return m;
  }
  function minMode(a, b) { return MODES[Math.min(MODES.indexOf(a), MODES.indexOf(b))]; }

  // ---- intensity: bounded logarithmic curve (1→0.12, 3→~0.19, 10→0.27, cap 0.30)
  function intensity(count) {
    return Math.min(0.30, 0.12 + 0.15 * Math.log10(Math.max(1, count)));
  }

  // ---- fx layer ----
  function ensureLayer(host) {
    let layer = host.querySelector(':scope > .fx-layer');
    if (!layer) {
      host.classList.add('fx-host');
      layer = document.createElement('div');
      layer.className = 'fx-layer';
      host.appendChild(layer);
    }
    return layer;
  }

  // applyResidue paints the calm accumulated state: ONE dominant residue +
  // at most one secondary accent (effect mixer). Idempotent — safe to call
  // on every render; arrival never fires from here.
  function applyResidue(host, res) {
    if (!host) return;
    const mode = effectsMode();
    const layer = host.querySelector(':scope > .fx-layer');
    if (mode === 'off' || !res || !(res.groups || []).length) {
      if (layer) { layer.remove(); host.classList.remove('fx-host'); }
      host.style.removeProperty('--fx-i');
      host.style.removeProperty('--fx-i2');
      host.classList.remove('fx-residue', 'fx-residue-2');
      return;
    }
    // Dominant = max count, canonical-order tie-break (groups arrive in
    // canonical order, so the first maximum is deterministic).
    let dom = null, sec = null;
    for (const g of res.groups) {
      if (!dom || g.count > dom.count) { sec = dom && dom.count > 0 ? dom : sec; dom = g; }
      else if (!sec || g.count > sec.count) sec = g;
    }
    const l = ensureLayer(host);
    l.dataset.effect = effectFor(dom);
    host.classList.add('fx-residue');
    host.style.setProperty('--fx-i', intensity(dom.count).toFixed(3));
    if (sec && res.groups.length > 1) {
      l.dataset.effect2 = effectFor(sec);
      host.classList.add('fx-residue-2');
      host.style.setProperty('--fx-i2', (intensity(sec.count) * 0.6).toFixed(3));
    } else {
      host.classList.remove('fx-residue-2');
      delete l.dataset.effect2;
    }
  }

  // ---- arrival: one-shot, budgeted ----
  const running = new Map(); // host → pending effect id | null
  let activeCount = 0;
  const MAX_CONCURRENT = 4;

  const seen = ('IntersectionObserver' in window)
    ? new IntersectionObserver(() => {}) : null; // presence only; we query rects

  function inViewport(host) {
    const r = host.getBoundingClientRect();
    return r.bottom > 0 && r.top < window.innerHeight && r.width > 0;
  }

  function fireArrival(host, group) {
    const mode = effectsMode();
    if (mode === 'off' || mode === 'static' || !host || !group) return;
    if (!inViewport(host)) return;      // offscreen: residue only
    if (running.has(host)) {            // per-target: 1 running + 1 pending
      running.set(host, group);         // coalesce into the pending slot
      return;
    }
    if (activeCount >= MAX_CONCURRENT) return; // global viewport budget
    const eff = EFFECTS[effectFor(group)];
    if (!eff) return;
    const layer = ensureLayer(host);
    const cls = eff.arrival + (mode === 'subtle' ? ' fx-subtle' : '');
    running.set(host, null);
    activeCount++;
    layer.classList.add(...cls.split(' '));
    const done = () => {
      layer.classList.remove(...cls.split(' '));
      activeCount--;
      const pending = running.get(host);
      running.delete(host);
      if (pending) setTimeout(() => fireArrival(host, pending), 60);
    };
    layer.addEventListener('animationend', done, { once: true });
    setTimeout(done, 1400); // safety net if animationend never fires
  }

  // onAggregateChange: called from the feed's resonance-only diff path — a
  // REVISION change on a rendered entry. Residue refreshes; arrival plays
  // for the dominant group (coalesced, budgeted). Never called on first
  // paint or catch-up, by construction of the diff path.
  function onAggregateChange(node, res) {
    const host = node.querySelector('.bubble') || node;
    applyResidue(host, res);
    const groups = res?.groups || [];
    if (!groups.length) return;
    let dom = groups[0];
    for (const g of groups) if (g.count > dom.count) dom = g;
    fireArrival(host, dom);
  }

  return { applyResidue, fireArrival, onAggregateChange, effectsMode, intensity };
})();
