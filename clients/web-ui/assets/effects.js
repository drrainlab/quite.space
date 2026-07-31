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

  // ---- glyphs (RA-1) ----
  // Glyph sets are an ALLOWLIST: the primary glyph of an arrival is the
  // reaction's own symbol — what was pressed is what flies — and filler
  // glyphs texture the higher ladder rungs. Only 'constellation' is active
  // in v1; the rest are reserved data (per-space selection arrives with the
  // MA wave). User text never enters the stream: a glyph that can spell can
  // forge system copy.
  const GLYPH_SETS = {
    constellation: ['✦', '·', '*', '✧', '+'],
    terminal: ['+', '-', '/', '\\', '|', ':', '.'],
    soft: ['·', '○', '◌', '｡', '°'],
    botanical: ['⌁', '❋', '˚', '∿'],
  };
  const FILLER_SET = 'constellation';
  // Per-meaning glyph colors ride the same CSS vars the residue uses;
  // anything unmapped (and every unicode reaction) falls to the accent.
  const KEY_COLOR = {
    warmth: 'var(--fx-warm)', join: 'var(--fx-join)',
    support: 'var(--fx-hold)', spark: 'var(--signal)',
  };
  function glyphFor(group) {
    // resGlyph is the node-resolved symbol (authoritative fallback or the
    // unicode char itself); mirror its last-resort marker.
    return (typeof resGlyph === 'function' && resGlyph(group)) || '◈';
  }
  function colorFor(group) {
    if (group.kind === 'semantic') return KEY_COLOR[group.key] || 'var(--acc)';
    return null; // emoji carry their own color; CSS default covers the rest
  }

  // The viewport glyph budget. CSS keyframes drive all motion (compositor-
  // owned — no rAF anywhere in this file, deliberately); the only live cost
  // is DOM nodes, so the cap is a node count. Excess is DROPPED, never
  // queued: a burst that outruns the budget stays a smaller burst.
  let liveGlyphs = 0;
  const GLYPH_BUDGET_FULL = 120, GLYPH_BUDGET_SUBTLE = 40;
  function glyphBudget() {
    return effectsMode() === 'full' ? GLYPH_BUDGET_FULL : GLYPH_BUDGET_SUBTLE;
  }

  // spawnGlyphs: DOM glyphs inside the fx layer — the generalisation of the
  // old spawnPluses. Self-removing on animationend with a timeout net; the
  // live counter is decremented exactly once per glyph either way. The
  // timeout scales with each glyph's stagger delay, so a paced Stream's
  // last glyph is not reaped mid-flight.
  function spawnGlyphs(layer, spec) {
    const n = Math.max(0, Math.min(spec.count, glyphBudget() - liveGlyphs));
    for (let i = 0; i < n; i++) {
      const p = document.createElement('span');
      p.className = spec.cls;
      p.textContent = spec.glyphs[i % spec.glyphs.length];
      const c = spec.colorAt ? spec.colorAt(i) : spec.color;
      if (c) p.style.color = c;
      p.style.left = (spec.left ? spec.left(i) : 12 + Math.random() * 76) + '%';
      p.style.animationDelay = (i * (spec.stagger || 90)) + 'ms';
      p.style.fontSize = (10 + Math.random() * 6) + 'px';
      if (spec.prep) spec.prep(p, i);
      layer.appendChild(p);
      liveGlyphs++;
      let gone = false;
      const drop = () => { if (gone) return; gone = true; liveGlyphs--; p.remove(); };
      p.addEventListener('animationend', drop, { once: true });
      setTimeout(drop, (spec.ttl || 1900) + i * (spec.stagger || 90));
    }
    return n;
  }

  // spawnPluses: the join meaning's little fountain, now a spawnGlyphs case.
  function spawnPluses(layer) {
    spawnGlyphs(layer, { glyphs: ['+'], count: 7, cls: 'fx-plus', stagger: 70, ttl: 1600 });
  }

  // Echo (RA-1): every arrival also releases the reaction's own symbol —
  // 1–4 glyphs scaled by how many arrived this tick. At 'subtle' a single
  // muted glyph rises (no inline color → the CSS default).
  function spawnEcho(layer, group, delta, mode) {
    const n = mode === 'subtle' ? 1 : Math.max(1, Math.min(4, delta || 1));
    spawnGlyphs(layer, {
      glyphs: [glyphFor(group)],
      count: n,
      cls: 'fx-glyph fx-glyph-echo',
      color: mode === 'subtle' ? null : colorFor(group),
      stagger: 110,
    });
  }

  // weightedTick: the tick's arrivals as a flat symbol list — each group's
  // symbol repeated by its delta, so Stream/Fountain color mixes are
  // PROPORTIONAL to what actually arrived.
  function weightedTick(ds) {
    const out = [];
    for (const d of ds) {
      for (let k = 0; k < (d.delta || 1); k++) {
        out.push({ glyph: glyphFor(d.group), color: colorFor(d.group) });
      }
    }
    return out;
  }

  // Stream (RA-2): a paced procession along the card's bottom edge —
  // primary symbols interleaved with constellation fillers, one glyph per
  // ~170ms so a burst reads as a flow, not a flash.
  function spawnStream(layer, ds) {
    const prim = weightedTick(ds);
    const fill = GLYPH_SETS[FILLER_SET];
    const total = Math.min(8, prim.length * 2);
    spawnGlyphs(layer, {
      count: total,
      glyphs: Array.from({ length: total }, (_, i) =>
        i % 2 === 0 ? prim[(i / 2) % prim.length].glyph : fill[i % fill.length]),
      colorAt: (i) => (i % 2 === 0 ? prim[(i / 2) % prim.length].color : null),
      cls: 'fx-glyph fx-glyph-stream',
      left: (i) => 6 + ((i * 11) % 80),
      stagger: 170,
      ttl: 2600,
    });
  }

  // Fountain (RA-2): ONE emitter per card — the shower rises from a
  // clustered source near the middle, each glyph arcing sideways by its
  // own --fxdx. Emission is a bounded burst (≤24), never per-event: the
  // rate became a density, exactly so a hot moment cannot multiply DOM.
  function spawnFountain(layer, ds) {
    const prim = weightedTick(ds);
    const fill = GLYPH_SETS[FILLER_SET];
    const total = Math.min(24, prim.length * 2 + 4);
    spawnGlyphs(layer, {
      count: total,
      glyphs: Array.from({ length: total }, (_, i) =>
        i % 3 === 2 ? fill[i % fill.length] : prim[i % prim.length].glyph),
      colorAt: (i) => (i % 3 === 2 ? null : prim[i % prim.length].color),
      cls: 'fx-glyph fx-glyph-fount',
      left: () => 38 + Math.random() * 24,
      prep: (p) => p.style.setProperty('--fxdx', (Math.random() * 88 - 44).toFixed(0) + 'px'),
      stagger: 70,
      ttl: 2100,
    });
  }

  // ---- the ladder (RA-2) ----
  // Thresholds are RENDERER POLICY, not contract: an author cannot demand
  // a Fountain and a weak client never owes one. The window slides over
  // the recent poll ticks; when it drains, the level walks back down on
  // its own — there is no state machine to unwind.
  const LADDER_WINDOW_MS = 6000;
  const STREAM_AT = 3, FOUNTAIN_AT = 9;
  const hostWindow = new WeakMap(); // .bubble → [{at, n}] (dies with the DOM)
  function ladderLevel(host, ds) {
    const now = Date.now();
    const arr = (hostWindow.get(host) || []).filter(e => now - e.at < LADDER_WINDOW_MS);
    arr.push({ at: now, n: ds.reduce((s, d) => s + (d.delta || 1), 0) });
    hostWindow.set(host, arr);
    const sum = arr.reduce((s, e) => s + e.n, 0);
    return sum >= FOUNTAIN_AT ? 'fountain' : sum >= STREAM_AT ? 'stream' : 'echo';
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

  // firePulse: the one-shot CSS-class effect plus whatever glyph spawner
  // the ladder chose. Glyphs spawn ONLY when the pulse is admitted — the
  // pulse budget (per-host 1 running + 1 pending, MAX_CONCURRENT global)
  // is therefore also the glyph-burst rate limiter per card.
  function firePulse(host, group, delta, spawnFx) {
    const mode = effectsMode();
    if (mode === 'off' || mode === 'static' || !host || !group) return;
    if (!inViewport(host)) return;      // offscreen: residue only
    if (running.has(host)) {            // per-target: 1 running + 1 pending
      // Coalesce into the pending slot: the latest group wins the effect,
      // but the magnitudes ADD — five people arriving during a running
      // effect are five, not one. The re-fire is a plain Echo of the
      // merged magnitude; the next tick's window decides the level again.
      const prev = running.get(host);
      running.set(host, { group, delta: (delta || 1) + (prev ? prev.delta : 0) });
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
    if (spawnFx) spawnFx(layer, mode);
    let finished = false;
    const done = () => {
      if (finished) return;
      finished = true;
      host.classList.remove(...cls.split(' '));
      activeCount--;
      const pending = running.get(host);
      running.delete(host);
      if (pending) setTimeout(() => fireArrival(host, pending.group, pending.delta), 60);
    };
    host.addEventListener('animationend', done, { once: true });
    setTimeout(done, 1600); // safety net if animationend never fires
  }

  function fireArrival(host, group, delta) {
    firePulse(host, group, delta, (layer, mode) => spawnEcho(layer, group, delta, mode));
  }

  // onAggregateChange: called from the feed's delta paths on a rendered
  // entry. Residue always refreshes; arrival plays only for the groups
  // that actually GREW — the caller computes deltas against its previous
  // snapshot (computeResDeltas). No deltas — first paint, catch-up, full
  // rebuild — means residue only. That asymmetry IS the no-replay guard:
  // history may repaint a trace, but only an observed delta may animate.
  function onAggregateChange(node, res, deltas) {
    const host = node.querySelector('.bubble') || node;
    applyResidue(host, res);
    const ds = deltas || [];
    if (!ds.length) return;
    // The ladder runs only at 'full'; 'subtle' stays a small Echo, and
    // off/static are refused inside firePulse. The loudest group of the
    // tick carries the pulse; the glyph spawner carries the whole tick.
    const level = effectsMode() === 'full' ? ladderLevel(host, ds) : 'echo';
    if (level === 'echo') {
      for (const d of ds) fireArrival(host, d.group, d.delta);
      return;
    }
    let top = ds[0];
    for (const d of ds) if (d.delta > top.delta) top = d;
    const total = ds.reduce((s, d) => s + (d.delta || 1), 0);
    const spawnFx = level === 'fountain'
      ? (layer) => spawnFountain(layer, ds)
      : (layer) => spawnStream(layer, ds);
    firePulse(host, top.group, total, spawnFx);
  }

  return { applyResidue, fireArrival, onAggregateChange, effectsMode, intensity };
})();
