'use strict';

// SP-3.2 — the reader for field.track.v1, the sweep's canonical trajectory.
//
// A MINIMAL CBOR READER, NOT A CBOR LIBRARY. It reads exactly the profile
// the Go encoder emits (canonical integer-keyed maps, definite lengths) and
// skips everything else without dying: unknown top-level keys and unknown
// sample tags are FORWARD COMPATIBILITY, and the law for them is stated in
// protocol/field/track.go — an unknown sample is never rendered as a point.
// The whole reader is pinned to the same golden vector the Go side asserts
// (TestTrackGoldenVector ↔ scripts/webui/trackvector.cjs): if the two ever
// disagree, the format changed, which for a sealed v1 asset is an event.
//
// THE API IS SEGMENTS, DELIBERATELY. A gap is a sample the reader must
// consume (the brief's law), so this decoder never hands back one long
// polyline: it hands back the segments BETWEEN gaps, and the gaps beside
// them. A renderer drawing segment by segment physically cannot join across
// a gap — the honesty lives in the shape of the data, not in a comment
// asking renderers to be careful.

// ---- CBOR primitives (definite-length subset) ----

function cborHead(u8, at) {
  // Returns {major, value, next} for the item head at `at`.
  const b = u8[at];
  const major = b >> 5;
  const info = b & 0x1f;
  if (info < 24) return { major, value: info, next: at + 1 };
  if (info === 24) return { major, value: u8[at + 1], next: at + 2 };
  if (info === 25) return { major, value: (u8[at + 1] << 8) | u8[at + 2], next: at + 3 };
  if (info === 26) {
    return {
      major,
      value: ((u8[at + 1] << 24) >>> 0) + (u8[at + 2] << 16) + (u8[at + 3] << 8) + u8[at + 4],
      next: at + 5,
    };
  }
  if (info === 27) {
    // 64-bit: composed via Number — timestamps and e7-coordinates all fit
    // far inside 2^53, and nothing in this format needs more.
    let v = 0;
    for (let i = 1; i <= 8; i++) v = v * 256 + u8[at + i];
    return { major, value: v, next: at + 9 };
  }
  throw new Error('indefinite length at ' + at);
}

// Skip one complete item (for unknown keys/tags) and return the offset after.
function cborSkip(u8, at) {
  const h = cborHead(u8, at);
  switch (h.major) {
    case 0: case 1: case 7: return h.next;
    case 2: case 3: return h.next + h.value;
    case 4: {
      let p = h.next;
      for (let i = 0; i < h.value; i++) p = cborSkip(u8, p);
      return p;
    }
    case 5: {
      let p = h.next;
      for (let i = 0; i < h.value; i++) { p = cborSkip(u8, p); p = cborSkip(u8, p); }
      return p;
    }
    case 6: return cborSkip(u8, h.next);
    default: throw new Error('unskippable major ' + h.major);
  }
}

function cborUint(u8, at) {
  const h = cborHead(u8, at);
  if (h.major !== 0) throw new Error('expected uint at ' + at + ', got major ' + h.major);
  return h;
}

// ---- field.track.v1 ----

const TRACK_TAG_POINT = 1;
const TRACK_TAG_GAP = 2;
const TRACK_GAP_REASONS = { 1: 'no_fix', 2: 'suspended', 3: 'unknown' };

/**
 * Decode field.track.v1 bytes into segments and gaps.
 *
 * Returns:
 *   {
 *     startedAt,                    // unix seconds
 *     segments: [ [ {lat, lon, accuracyM, unixMs}, ... ], ... ],
 *     gaps:     [ {afterSegment, unixMs, durationMs, reason}, ... ],
 *     points, unknownSamples
 *   }
 *
 * Timeline: dt values are cumulative from started_at; a gap ADVANCES the
 * clock by its duration (the Go sealer's arithmetic, mirrored).
 */
function trackDecode(bytes) {
  const u8 = bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes);
  const top = cborHead(u8, 0);
  if (top.major !== 5) throw new Error('not a track: top-level is not a map');
  let p = top.next;
  let startedAt = 0;
  let samplesAt = -1;
  let samplesLen = 0;
  for (let i = 0; i < top.value; i++) {
    const k = cborHead(u8, p);
    if (k.major !== 0) { p = cborSkip(u8, p); p = cborSkip(u8, p); continue; }
    p = k.next;
    if (k.value === 1) {
      const v = cborUint(u8, p);
      startedAt = v.value;
      p = v.next;
    } else if (k.value === 2) {
      const arr = cborHead(u8, p);
      if (arr.major !== 4) throw new Error('samples is not an array');
      samplesAt = arr.next;
      samplesLen = arr.value;
      p = cborSkip(u8, p);
    } else {
      // Unknown top-level key: preserved by the Go side, irrelevant to a
      // renderer — skipped, never guessed at.
      p = cborSkip(u8, p);
    }
  }

  const segments = [];
  const gaps = [];
  let seg = [];
  let clockMs = startedAt * 1000;
  let points = 0;
  let unknownSamples = 0;
  let sp = samplesAt;
  for (let i = 0; i < samplesLen && sp >= 0; i++) {
    const s = cborHead(u8, sp);
    const after = cborSkip(u8, sp);
    if (s.major !== 4 || s.value < 1) { sp = after; unknownSamples++; continue; }
    const tag = cborHead(u8, s.next);
    if (tag.major !== 0) { sp = after; unknownSamples++; continue; }
    if (tag.value === TRACK_TAG_POINT && s.value >= 5) {
      const dt = cborUint(u8, tag.next);
      const lat = cborUint(u8, dt.next);
      const lon = cborUint(u8, lat.next);
      const acc = cborUint(u8, lon.next);
      clockMs += dt.value;
      seg.push({
        lat: lat.value / 1e7 - 90,
        lon: lon.value / 1e7 - 180,
        accuracyM: acc.value,
        unixMs: clockMs,
      });
      points++;
    } else if (tag.value === TRACK_TAG_GAP && s.value >= 4) {
      const dt = cborUint(u8, tag.next);
      const dur = cborUint(u8, dt.next);
      const reason = cborUint(u8, dur.next);
      clockMs += dt.value;
      if (seg.length) { segments.push(seg); seg = []; }
      gaps.push({
        afterSegment: segments.length - 1,
        unixMs: clockMs,
        durationMs: dur.value,
        reason: TRACK_GAP_REASONS[reason.value] || 'unknown',
      });
      clockMs += dur.value;
    } else {
      // An unknown tag is a sample from a future recorder. It is COUNTED
      // and stepped over — never turned into a point, never into a line.
      unknownSamples++;
    }
    sp = after;
  }
  if (seg.length) segments.push(seg);
  return { startedAt, segments, gaps, points, unknownSamples };
}

// The node harness (scripts/webui/trackvector.cjs) loads this file outside
// a browser to pin the decoder against the Go golden vector.
if (typeof module !== 'undefined' && module.exports) {
  module.exports = { trackDecode };
}
