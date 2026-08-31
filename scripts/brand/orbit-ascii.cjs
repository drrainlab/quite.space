// The quite.space orbit, computed rather than drawn.
//
// The hand-drawn README mark came out lopsided the moment GitHub set it
// in a real monospace grid — hand-kerned ASCII always does. This renders
// the same mark the way the rest of the project makes shapes: from a
// model. An ellipse as a signed-distance field, the stroke character
// chosen from the local tangent; a planet as a lit disc; stars from a
// deterministic PRNG so the art never wobbles between runs.
//
//   node scripts/brand/orbit-ascii.cjs            # the README block
//   node scripts/brand/orbit-ascii.cjs --tilt 18  # play with the model
//
// The README carries this script's output verbatim; regenerating and
// pasting is the whole update path — one model, one mouth.
'use strict';

const args = process.argv.slice(2);
const opt = (name, dflt) => {
  const i = args.indexOf('--' + name);
  return i >= 0 ? parseFloat(args[i + 1]) : dflt;
};

const W = opt('width', 64);        // canvas, chars
const H = opt('height', 15);       // canvas, rows
const ASPECT = opt('aspect', 2.1); // char cell height / width
const A = opt('a', 24);            // ring semi-major, chars
const B = opt('b', 7.2);           // ring semi-minor, chars (pre-aspect)
const TILT = (opt('tilt', 0) * Math.PI) / 180;
const PR = opt('planet', 3.4);     // planet radius, chars
const STROKE = 0.55;               // ring half-thickness

// deterministic scatter — mulberry32, seeded once, forever
function prng(seed) {
  let s = seed >>> 0;
  return () => {
    s = (s + 0x6d2b79f5) >>> 0;
    let t = Math.imul(s ^ (s >>> 15), 1 | s);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const cx = W / 2, cy = H / 2;
const grid = Array.from({ length: H }, () => Array(W).fill(' '));

// ---- stars (drawn first; everything else overwrites them) -------------
const rand = prng(opt('seed', 0x51e7));
const STARS = [['·', 10], ['✦', 4], ['.', 5]];
for (const [ch, n] of STARS) {
  for (let i = 0; i < n; i++) {
    const x = Math.floor(rand() * W);
    const y = Math.floor(rand() * H);
    // keep the middle clear for the mark itself
    // clear of the mark AND of a margin around the ring, so no star
    // ever kisses a stroke
    const dx = (x - cx) / A, dy = ((y - cy) * ASPECT) / A;
    if (dx * dx + dy * dy < 1.25) { i--; continue; }
    grid[y][x] = ch;
  }
}

// ---- the model, sampled per cell --------------------------------------
// world coords: x right, y down, y stretched by the char aspect
const cos = Math.cos(TILT), sin = Math.sin(TILT);
for (let r = 0; r < H; r++) {
  for (let c = 0; c < W; c++) {
    const x = c - cx;
    const y = (r - cy) * ASPECT;

    // planet: a lit disc, light from the upper left
    const pd = Math.hypot(x, y);
    let planetCh = null;
    if (pd <= PR * 1.15) {
      const lx = (x / PR + 0.45), ly = (y / PR + 0.45);
      const lit = 1 - Math.min(1, Math.hypot(lx, ly) / 1.6);
      const shades = ['@', '@', '0', 'o', ':', '.'];
      planetCh = shades[Math.min(5, Math.floor((1 - lit) * 5.99))];
    }

    // ring: rotate into ellipse frame, signed distance, slope → glyph
    const u = x * cos + y * sin;
    const v = -x * sin + y * cos;
    const f = (u * u) / (A * A) + (v * v) / (B * B); // 1 on the ellipse
    // gradient of f gives the normal; distance ≈ |f-1| / |grad f|
    const gu = (2 * u) / (A * A), gv = (2 * v) / (B * B);
    const gx = gu * cos - gv * sin, gy = gu * sin + gv * cos;
    const grad = Math.hypot(gx, gy) || 1e-9;
    const dist = Math.abs(f - 1) / grad;
    // the stroke must be at least half a CELL thick along the normal, or
    // steep arcs fall between samples — the cell is 1 wide, ASPECT tall
    const nx = gx / grad, ny = gy / grad;
    const cellAlong = (Math.abs(nx) * 1 + (Math.abs(ny) * ASPECT)) / 2;
    let ringCh = null;
    if (dist < Math.max(STROKE, cellAlong * 0.62)) {
      // tangent slope in CHAR space picks the glyph
      const ty = -gx, tx = gy; // tangent ⟂ normal
      const slope = Math.abs(tx) < 1e-6 ? 99 : (ty / tx) / ASPECT;
      const sl = Math.abs(slope);
      if (sl < 0.28) ringCh = '-';
      else if (sl > 2.2) ringCh = '|';
      else ringCh = slope > 0 ? '\\' : '/';
    }

    // Saturn draw order: the NEAR half of the ring (v >= 0) passes in
    // front of the planet; the FAR half hides where the disc covers it.
    if (ringCh && (v >= 0 || pd > PR + 0.8)) grid[r][c] = ringCh;
    else if (planetCh) grid[r][c] = planetCh;
  }
}

const art = grid.map((row) => row.join('').replace(/\s+$/, '')).join('\n');

const caption = [
  '',
  centered('q  u  i  t  e  ·  s  p  a  c  e', W),
  '',
  centered('the space between us belongs to us', W),
];

function centered(text, width) {
  const pad = Math.max(0, Math.floor((width - text.length) / 2));
  return ' '.repeat(pad) + text;
}

console.log(art + '\n' + caption.join('\n'));
