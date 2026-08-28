// SP-3.2 — the JS track decoder, pinned to the Go golden vector.
//
// TWO IMPLEMENTATIONS OF ONE SEALED FORMAT. protocol/field/track.go encodes
// field.track.v1; clients/web-ui/assets/track.js reads it back for the map.
// Each side has its own tests, and this harness is the ROPE BETWEEN THEM:
// the exact bytes TestTrackGoldenVector asserts in Go are decoded here by
// the real track.js (require'd, not copied), and every claim a renderer
// would build on is checked — the segment SHAPE above all, because the gap
// law ("a reader cannot join across a gap") lives in that shape.
//
// If this fails after touching track.go: the format changed, which for a
// sealed v1 asset format is an event, not a refactor. If it fails after
// touching track.js: the reader broke, and the map would have lied.
const path = require('path');

const { trackDecode } = require(
  path.join(__dirname, '..', '..', 'clients', 'web-ui', 'assets', 'track.js'));

// The bytes from protocol/field TestTrackGoldenVector, verbatim.
const GOLDEN =
  'a2011a6ab13b8002848501001a516e40a01a7012f2d0088501193a981a516ea248' +
  '1a70135478068402193a9819cb2001850119cb201a516efc201a7013bdf00b';

function fail(msg) {
  console.error('trackvector: ' + msg);
  process.exit(1);
}
function near(a, b, eps, what) {
  if (Math.abs(a - b) > eps) fail(`${what}: ${a} vs ${b}`);
}

const bytes = Uint8Array.from(Buffer.from(GOLDEN, 'hex'));
const tr = trackDecode(bytes);

if (tr.startedAt !== 1_790_000_000) fail('started_at: ' + tr.startedAt);
if (tr.points !== 3) fail('points: ' + tr.points);
if (tr.unknownSamples !== 0) fail('unknown samples: ' + tr.unknownSamples);

// THE SHAPE IS THE LAW: two segments around the gap, never one polyline.
if (tr.segments.length !== 2) fail('segments: ' + tr.segments.length);
if (tr.segments[0].length !== 2 || tr.segments[1].length !== 1) {
  fail('segment shape: ' + tr.segments.map(s => s.length).join(','));
}
if (tr.gaps.length !== 1) fail('gaps: ' + tr.gaps.length);
const gap = tr.gaps[0];
if (gap.reason !== 'no_fix') fail('gap reason: ' + gap.reason);
if (gap.durationMs !== 52_000) fail('gap duration: ' + gap.durationMs);
if (gap.afterSegment !== 0) fail('gap position: ' + gap.afterSegment);

// Coordinates, back from e7-unsigned to degrees (the Go fixture's values).
near(tr.segments[0][0].lat, 46.6180, 1e-6, 'p1 lat');
near(tr.segments[0][0].lon, 8.0290, 1e-6, 'p1 lon');
near(tr.segments[0][1].lat, 46.6205, 1e-6, 'p2 lat');
near(tr.segments[1][0].lat, 46.6228, 1e-6, 'p3 lat');
near(tr.segments[1][0].lon, 8.0342, 1e-6, 'p3 lon');
if (tr.segments[0][0].accuracyM !== 8) fail('p1 accuracy');
if (tr.segments[1][0].accuracyM !== 11) fail('p3 accuracy');

// The timeline mirrors the sealer's arithmetic: dt is measured from the
// previous sample's END, and a gap advances the clock by its duration.
const t0 = 1_790_000_000_000;
if (tr.segments[0][0].unixMs !== t0) fail('p1 clock');
if (tr.segments[0][1].unixMs !== t0 + 15_000) fail('p2 clock');
if (gap.unixMs !== t0 + 30_000) fail('gap clock');
if (tr.segments[1][0].unixMs !== t0 + 30_000 + 52_000 + 52_000) fail('p3 clock');

// Forward compatibility: an unknown sample tag is stepped over and counted,
// never rendered as a point — spliced into the golden samples by hand
// ([9, 1] — a tag from a future recorder).
(function unknownTag() {
  // a2 01 1a 6ab13b80 02 85 <4 samples> + [82 09 01]
  const spliced = Buffer.concat([
    Buffer.from('a2011a6ab13b800285', 'hex'),
    Buffer.from(GOLDEN, 'hex').subarray(9),
    Buffer.from('820901', 'hex'),
  ]);
  const got = trackDecode(Uint8Array.from(spliced));
  if (got.points !== 3) fail('unknown-tag: points ' + got.points);
  if (got.unknownSamples !== 1) fail('unknown-tag: not counted');
  if (got.segments.length !== 2) fail('unknown-tag: segments ' + got.segments.length);
})();

// An unknown top-level key before the known ones must not derail the walk.
(function unknownKey() {
  // a3 07 41 ff <then key 1, key 2 as in golden>
  const spliced = Buffer.concat([
    Buffer.from('a30741ff', 'hex'),
    Buffer.from(GOLDEN, 'hex').subarray(1),
  ]);
  const got = trackDecode(Uint8Array.from(spliced));
  if (got.points !== 3 || got.segments.length !== 2) fail('unknown-key: walk derailed');
})();

console.log('trackvector: the JS reader and the Go golden vector agree ' +
  `(${tr.points} points, ${tr.segments.length} segments, 1 ${gap.reason} gap)`);
