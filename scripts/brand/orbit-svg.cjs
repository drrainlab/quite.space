// The quite.space orbit as a vector — the ASCII generator's twin.
//
// GitHub's README font broke the ASCII grid on ambiguous-width glyphs
// (✦ renders double-wide in some stacks), so the front door gets the
// same MODEL rendered where geometry cannot be re-kerned: an SVG. Same
// discipline as everywhere else — the mark is computed, the files are
// the script's verbatim output, and the identical contour can later
// feed the enclosure engraving and the OLED splash.
//
//   node scripts/brand/orbit-svg.cjs   # writes docs/brand/orbit-{dark,light}.svg
'use strict';

const fs = require('fs');
const path = require('path');

const W = 640, H = 340;
const CX = W / 2, CY = 150;
const A = 212, B = 46;          // ring semi-axes
const TILT = -14;               // degrees; negative = right side rises
const PR = 38;                  // planet radius

// mulberry32 — the same deterministic scatter the ASCII twin uses
function prng(seed) {
  let s = seed >>> 0;
  return () => {
    s = (s + 0x6d2b79f5) >>> 0;
    let t = Math.imul(s ^ (s >>> 15), 1 | s);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

// stars: small dots and four-point sparkles, kept clear of the mark
const rand = prng(0x51e7);
const stars = [];
while (stars.length < 26) {
  const x = 20 + rand() * (W - 40);
  const y = 16 + rand() * (H - 120);
  const dx = (x - CX) / (A + 30), dy = (y - CY) / (B + 46);
  if (dx * dx + dy * dy < 1.0) continue;   // margin around the ring
  const kind = rand();
  stars.push({ x: x.toFixed(1), y: y.toFixed(1), kind });
}

function sparkle(x, y, r) {
  // a four-point star: two thin quads on the axes
  return `M ${x} ${y - r} L ${x + r * 0.22} ${y} L ${x} ${y + r} ` +
         `L ${x - r * 0.22} ${y} Z ` +
         `M ${x - r} ${y} L ${x} ${y - r * 0.22} L ${x + r} ${y} ` +
         `L ${x} ${y + r * 0.22} Z`;
}

const starDots = stars.filter(s => s.kind < 0.7)
  .map(s => `<circle cx="${s.x}" cy="${s.y}" r="${(1.1 + s.kind).toFixed(1)}" class="dim"/>`)
  .join('\n    ');
const starSparks = stars.filter(s => s.kind >= 0.7)
  .map(s => `<path d="${sparkle(+s.x, +s.y, 5.5)}" class="spark"/>`)
  .join('\n    ');

// the ring, split along its major axis so the far half passes BEHIND
// the planet and the near half in front — Saturn draw order, exactly
// the ASCII twin's rule
const halfArc = (sweep) =>
  `M ${-A} 0 A ${A} ${B} 0 0 ${sweep} ${A} 0`;

const svg = (theme) => `<svg xmlns="http://www.w3.org/2000/svg"
  viewBox="0 0 ${W} ${H}" width="${W}" height="${H}" role="img"
  aria-label="quite.space — a planet on a quiet orbit">
  <style>
    .ink   { stroke: ${theme.ink}; fill: none; stroke-width: 2.6;
             stroke-linecap: round; }
    .body  { fill: ${theme.body}; }
    .core  { fill: ${theme.core}; }
    .dim   { fill: ${theme.dim}; }
    .spark { fill: ${theme.spark}; }
    .word  { fill: ${theme.ink};
             font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
             letter-spacing: 0.55em; font-size: 24px; }
    .motto { fill: ${theme.dim};
             font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
             letter-spacing: 0.18em; font-size: 13px; }
  </style>

  <g>
    ${starDots}
    ${starSparks}
  </g>

  <g transform="translate(${CX} ${CY}) rotate(${TILT})">
    <path class="ink" d="${halfArc(1)}"/>            <!-- far half -->
  </g>
  <g transform="translate(${CX} ${CY})">
    <circle class="body" r="${PR}"/>
    <circle class="core" r="${PR * 0.62}" cx="${-PR * 0.18}" cy="${-PR * 0.18}"/>
  </g>
  <g transform="translate(${CX} ${CY}) rotate(${TILT})">
    <path class="ink" d="${halfArc(0)}"/>            <!-- near half -->
  </g>

  <text class="word" x="${CX}" y="${H - 62}" text-anchor="middle">quite·space</text>
  <text class="motto" x="${CX}" y="${H - 28}" text-anchor="middle">the space between us belongs to us</text>
</svg>
`;

const THEMES = {
  dark: {
    ink: '#d7a4ef', body: '#2a2233', core: '#4a3a5c',
    dim: '#8f86a0', spark: '#e8d9f5',
  },
  light: {
    ink: '#5b3d78', body: '#e9e2f2', core: '#c9b8dd',
    dim: '#8a8296', spark: '#6f5a8a',
  },
};

const outDir = path.join(__dirname, '..', '..', 'docs', 'brand');
fs.mkdirSync(outDir, { recursive: true });
for (const [name, theme] of Object.entries(THEMES)) {
  const file = path.join(outDir, `orbit-${name}.svg`);
  fs.writeFileSync(file, svg(theme));
  console.log('wrote', path.relative(process.cwd(), file));
}
