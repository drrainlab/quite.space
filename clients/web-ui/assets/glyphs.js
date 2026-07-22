// Glyph Core v1: deterministic identity marks (plan §14). Same id → same
// glyph, always; distinguishable by form (not colour alone); legible at
// 24px; generated locally as SVG; a glyph is NEVER proof of identity —
// it is a memory aid, like a face.
//
// Types: human (soft open form), device (precise geometry), space (a small
// composition), pass (an open orbit / broken seal). Living types (bot/iot)
// and animation arrive in UI-3.

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
