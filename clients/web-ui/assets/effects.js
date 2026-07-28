// Resonant Surfaces (RP-2A). A reaction may briefly touch the OBJECT it
// landed on (arrival) and leave a calm accumulated trace (residue). Effects
// are derived presentation: the wire carries meaning only; effect ids
// resolve exclusively through this client-owned allowlist. Invariants: no
// layout shift (overlay layer), alpha-capped residue, nothing painted over
// media pixels, static fallback, fully disableable.

const RESFX = (() => {
  // ---- effect registry (allowlist) ----
  // key → {arrival css class (on the HOST — transforms are paint-level,
  // never layout), optional spawn(layer) for DOM particles}. Everything
  // unknown → tint.
  const EFFECTS = {
    'heartbeat.v1': { arrival: 'fx-arrive-heartbeat' }, // resonates: two beats
    'sunwash.v1':   { arrival: 'fx-arrive-sunwash' },   // warmth: golden wave
    'particles.v1': { arrival: 'fx-arrive-spark' },     // spark: rising sparks
    'zoom.v1':      { arrival: 'fx-arrive-zoom' },      // curious: lean closer
    'plusfountain.v1': { arrival: 'fx-arrive-plus', spawn: spawnPluses }, // join
    'hold.v1':      { arrival: 'fx-arrive-hold' },      // support: embracing ring
    'settle.v1':    { arrival: 'fx-arrive-weight' },    // weight: grounded settle
    'tint.v1':      { arrival: 'fx-arrive-tint' },      // unicode / fallback
  };
  const KEY_EFFECT = {
    resonates: 'heartbeat.v1', warmth: 'sunwash.v1', spark: 'particles.v1',
    curious: 'zoom.v1', join: 'plusfountain.v1', support: 'hold.v1',
    weight: 'settle.v1',
  };

  // spawnPluses: a small fountain of "+" rising from the bottom edge —
  // DOM particles inside the fx layer, self-removing.
  function spawnPluses(layer) {
    const n = 7;
    for (let i = 0; i < n; i++) {
      const p = document.createElement('span');
      p.className = 'fx-plus';
      p.textContent = '+';
      p.style.left = (12 + Math.random() * 76) + '%';
      p.style.animationDelay = (i * 70) + 'ms';
      p.style.fontSize = (10 + Math.random() * 6) + 'px';
      layer.appendChild(p);
      p.addEventListener('animationend', () => p.remove(), { once: true });
      setTimeout(() => p.remove(), 1600);
    }
  }

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
  // The ladder itself lives in modes.js now — this used to be one of three
  // copies of the same rule, and three copies of a rule drift.
  const LADDER = ['off', 'static', 'subtle', 'full'];
  function userMode() { return localStorage.getItem('qp.effects') || 'full'; }
  function effectsMode() {
    return MODES.resolve({
      ladder: LADDER,
      user: userMode(),
      // A space saying "no effects here" is a fact about the space, not a
      // preference to be weighed against yours — hence a clamp, not a floor.
      spaceAllows: typeof RES === 'undefined' || RES.surfaceEffects(),
      floorWhenReduced: 'static',
      floorWhenMinimal: 'static',
      floorWhenHidden: 'static',
    });
  }
  function minMode(a, b) { return MODES.min(LADDER, a, b); }

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
    // The class lives on the HOST: transform effects (heartbeat, zoom,
    // settle) move the whole object; paint effects target its .fx-layer.
    const cls = eff.arrival + (mode === 'subtle' ? ' fx-subtle' : '');
    running.set(host, null);
    activeCount++;
    host.classList.add(...cls.split(' '));
    if (eff.spawn && mode !== 'subtle') eff.spawn(layer);
    let finished = false;
    const done = () => {
      if (finished) return;
      finished = true;
      host.classList.remove(...cls.split(' '));
      activeCount--;
      const pending = running.get(host);
      running.delete(host);
      if (pending) setTimeout(() => fireArrival(host, pending), 60);
    };
    host.addEventListener('animationend', done, { once: true });
    setTimeout(done, 1600); // safety net if animationend never fires
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
