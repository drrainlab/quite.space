// Glyph Core v1: deterministic identity marks (plan §14). Same id → same
// glyph, always; distinguishable by form (not colour alone); legible at
// 24px; generated locally as SVG; a glyph is NEVER proof of identity —
// it is a memory aid, like a face.
//
// Types: human (soft open form), device (precise geometry), space (a small
// composition), pass (an open orbit / broken seal), bot (crisp hexagon +
// pulse dot), iot (sensor square + emanation rings). Forms are STATIC by
// default — motion is quiet-by-default and event-triggered in the shell
// (arrival pulse, real signal), never an idle loop inside the glyph.

// Cache by (id,type,size): SVG strings are reused across renders.
const _glyphCache = new Map();

function _glyphRng(seed) {
  let s = seed >>> 0;
  return () => {
    s = (s + 0x6D2B79F5) | 0;
    let t = Math.imul(s ^ (s >>> 15), 1 | s);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

function _seedFromId(id) {
  // id is a hex string; fold the first bytes into a 32-bit seed.
  let h = 2166136261 >>> 0;
  const s = String(id || '0');
  for (let i = 0; i < s.length; i++) { h ^= s.charCodeAt(i); h = Math.imul(h, 16777619) >>> 0; }
  return h >>> 0;
}

// A calm, id-derived accent that still belongs to the palette (soft
// orchid→mint band). Colour is secondary; form carries identity.
function glyphColor(id) {
  const rnd = _glyphRng(_seedFromId(id) ^ 0x9e3779b9);
  const hue = 150 + rnd() * 130;   // 150 (mint) .. 280 (orchid)
  return `hsl(${Math.round(hue)}, 42%, 64%)`;
}

// glyphSVG(id, type, size) → an <svg> string. Content is data-driven; no
// user text ever enters it.
function glyphSVG(id, type = 'human', size = 24) {
  const key = `${id}|${type}|${size}`;
  const hit = _glyphCache.get(key);
  if (hit) return hit;

  const rnd = _glyphRng(_seedFromId(id) + type.charCodeAt(0));
  const col = glyphColor(id);
  const V = 48; // internal viewBox
  const c = V / 2;
  let inner = '';

  if (type === 'device') {
    // precise geometry: nested rounded squares + a mark
    const r = 8 + rnd() * 5;
    inner += `<rect x="${c - 14}" y="${c - 14}" width="28" height="28" rx="6"
      fill="none" stroke="${col}" stroke-width="2" opacity="0.9"/>`;
    inner += `<rect x="${c - r / 2}" y="${c - r / 2}" width="${r}" height="${r}" rx="2"
      fill="${col}" opacity="0.7"/>`;
  } else if (type === 'space') {
    // a small composition: a ring of nodes around a centre
    const n = 3 + Math.floor(rnd() * 4);
    inner += `<circle cx="${c}" cy="${c}" r="4" fill="${col}" opacity="0.85"/>`;
    for (let i = 0; i < n; i++) {
      const a = (i / n) * Math.PI * 2 + rnd();
      const rr = 13 + rnd() * 4;
      const x = c + Math.cos(a) * rr, y = c + Math.sin(a) * rr;
      inner += `<circle cx="${x.toFixed(1)}" cy="${y.toFixed(1)}" r="${(1.6 + rnd() * 1.6).toFixed(1)}"
        fill="${col}" opacity="${(0.4 + rnd() * 0.5).toFixed(2)}"/>`;
    }
  } else if (type === 'bot') {
    // a deterministic bot: a crisp hexagon with an inner pulse dot — a made
    // thing, not a person. Distinguishable from human by hard edges.
    const pts = [];
    for (let i = 0; i < 6; i++) {
      const a = (i / 6) * Math.PI * 2 - Math.PI / 2;
      pts.push(`${(c + Math.cos(a) * 15).toFixed(1)},${(c + Math.sin(a) * 15).toFixed(1)}`);
    }
    inner += `<polygon points="${pts.join(' ')}" fill="none" stroke="${col}" stroke-width="2" opacity="0.9"/>`;
    inner += `<circle class="pulse" cx="${c}" cy="${c}" r="4.5" fill="${col}" opacity="0.75"/>`;
  } else if (type === 'iot') {
    // a sensor / IoT device: a small square with concentric emanation arcs —
    // a thing that senses and emits.
    inner += `<rect x="${c - 4}" y="${c - 4}" width="8" height="8" rx="1.5" fill="${col}" opacity="0.8"/>`;
    for (let i = 1; i <= 3; i++) {
      inner += `<circle class="ring r${i}" cx="${c}" cy="${c}" r="${5 + i * 5}"
        fill="none" stroke="${col}" stroke-width="1.4" opacity="${(0.6 - i * 0.14).toFixed(2)}"/>`;
    }
  } else if (type === 'pass') {
    // an open orbit / broken seal: a not-quite-closed arc + a spark
    const a0 = rnd() * Math.PI * 2, a1 = a0 + Math.PI * (1.2 + rnd() * 0.5);
    const R = 15;
    const x0 = c + Math.cos(a0) * R, y0 = c + Math.sin(a0) * R;
    const x1 = c + Math.cos(a1) * R, y1 = c + Math.sin(a1) * R;
    const large = a1 - a0 > Math.PI ? 1 : 0;
    inner += `<path d="M${x0.toFixed(1)} ${y0.toFixed(1)} A${R} ${R} 0 ${large} 1 ${x1.toFixed(1)} ${y1.toFixed(1)}"
      fill="none" stroke="${col}" stroke-width="2" stroke-linecap="round" opacity="0.9"/>`;
    inner += `<circle cx="${x1.toFixed(1)}" cy="${y1.toFixed(1)}" r="2.4" fill="${col}"/>`;
  } else {
    // human: a soft open form — an off-centre blob outline with an orbit dot
    const pts = [];
    const lobes = 5 + Math.floor(rnd() * 3);
    for (let i = 0; i < lobes; i++) {
      const a = (i / lobes) * Math.PI * 2;
      const rr = 11 + rnd() * 7;
      pts.push([c + Math.cos(a) * rr, c + Math.sin(a) * rr]);
    }
    let d = `M${pts[0][0].toFixed(1)} ${pts[0][1].toFixed(1)}`;
    for (let i = 1; i <= pts.length; i++) {
      const p = pts[i % pts.length], prev = pts[(i - 1) % pts.length];
      const mx = (prev[0] + p[0]) / 2, my = (prev[1] + p[1]) / 2;
      d += ` Q${prev[0].toFixed(1)} ${prev[1].toFixed(1)} ${mx.toFixed(1)} ${my.toFixed(1)}`;
    }
    d += 'Z';
    inner += `<path d="${d}" fill="${col}" opacity="0.16"/>`;
    inner += `<path d="${d}" fill="none" stroke="${col}" stroke-width="1.6" opacity="0.85"/>`;
  }

  const svg = `<svg viewBox="0 0 ${V} ${V}" width="${size}" height="${size}" ` +
    `xmlns="http://www.w3.org/2000/svg" role="img" aria-hidden="true">${inner}</svg>`;
  _glyphCache.set(key, svg);
  return svg;
}

// verificationPhrase(id, words) — a short human-comparable phrase derived
// from an id hash (plan §12.3, §13). NOT a secret and NOT proof; used to
// compare two glyphs out-of-band. Deterministic per id.
const PHRASE_WORDS = [
  'moss', 'echo', 'violet', 'pine', 'dusk', 'orbit', 'seven', 'ember',
  'quiet', 'north', 'ridge', 'slate', 'amber', 'fern', 'tide', 'lark',
  'cedar', 'drift', 'harbor', 'lumen', 'nova', 'reed', 'sable', 'thaw',
  'umber', 'vale', 'wren', 'flint', 'glow', 'haze', 'iris', 'jade',
];
function verificationPhrase(id, words = 3) {
  const rnd = _glyphRng(_seedFromId(id) ^ 0x51ed270b);
  const out = [];
  const used = new Set();
  while (out.length < words) {
    const w = PHRASE_WORDS[Math.floor(rnd() * PHRASE_WORDS.length)];
    if (!used.has(w)) { used.add(w); out.push(w); }
  }
  return out.join(' · ');
}

// ---- kind pictograms (MD-2 UI) ----
// A glyph says WHO (id-derived, memory aid); the pictogram says WHAT KIND
// of participant this is — human, agent, bot, sensor, gateway, space —
// mirroring protocol/manifest.Kind. Small, monochrome, currentColor, so it
// sits beside a name at 12–14px without shouting. Distinguishable by form,
// not colour: the badge colour classes stay, the shape carries the meaning.
const KIND_PICTO = {
  human:   // a head-and-shoulders arc, the oldest sign there is
    '<circle cx="12" cy="8" r="3.6"/><path d="M4.5 20c1.2-4 4-6 7.5-6s6.3 2 7.5 6"/>',
  agent:   // a spark: agency that acts, not just answers
    '<path d="M12 3l1.8 5.2L19 10l-5.2 1.8L12 17l-1.8-5.2L5 10l5.2-1.8z"/>',
  bot:     // a crisp box with two eyes and an antenna
    '<rect x="6" y="9" width="12" height="9" rx="2"/><path d="M12 9V5"/><circle cx="12" cy="4" r="1"/><circle cx="9.5" cy="13.5" r="1"/><circle cx="14.5" cy="13.5" r="1"/>',
  sensor:  // a dot with two emanation arcs — one ring reads at 12px, two do not
    '<circle cx="12" cy="14" r="2"/><path d="M8 10a5.7 5.7 0 0 1 8 0M5.5 7.5a9.2 9.2 0 0 1 13 0"/>',
  actuator:// a lever on a pivot
    '<circle cx="12" cy="17" r="2"/><path d="M12 15L7 6M12 15l5-9"/>',
  gateway: // a doorway with a path through it
    '<path d="M6 20V6a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v14"/><path d="M3 20h18M12 12h.01"/>',
  relay:   // two arrows passing
    '<path d="M4 9h13l-3-3M20 15H7l3 3"/>',
  archive: // a lidded box
    '<rect x="4" y="4" width="16" height="4" rx="1"/><path d="M6 8v11h12V8M10 12h4"/>',
  space:   // a planet with a tilted ring: a place, orbited
    '<circle cx="12" cy="12" r="4"/><path d="M4.5 15.5c3 1.5 12 1 15-3.5M19.5 8.5c-3-1.5-12-1-15 3.5"/>',
  unknown: '<circle cx="12" cy="12" r="8"/><path d="M12 8v5M12 16h.01"/>',
};

// kindPicto renders the pictogram for a member card. Agency refines kind:
// a "bot" that acts with AI agency shows the spark, not the box, because
// what a person needs to know is whether there is a mind behind the name.
function kindPicto(kind, agency, size = 13) {
  let k = String(kind || 'unknown').toLowerCase();
  if (agency === 'ai_agent' && (k === 'bot' || k === 'agent' || k === 'unknown')) k = 'agent';
  const body = KIND_PICTO[k] || KIND_PICTO.unknown;
  return `<svg class="kpicto k-${k}" width="${size}" height="${size}" viewBox="0 0 24 24" ` +
    `fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" ` +
    `stroke-linejoin="round" aria-hidden="true">${body}</svg>`;
}

if (typeof window !== 'undefined') { window.kindPicto = kindPicto; }
