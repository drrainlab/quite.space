'use strict';
// The flash analyser: WCAG 2.3.1's general flash threshold, applied to a
// sequence of mean-luminance samples.
//
//   A flash is a PAIR OF OPPOSING CHANGES in relative luminance of 10% or
//   more, where the darker of the two states is below 0.80. Three flashes in
//   any one-second window is the limit.
//
// Two deliberate choices, both toward strictness:
//
//   - Every adjacent opposing pair counts. up-down-up is read as two flashes,
//     not one, because pairing them off non-overlapping would let a scene sit
//     at twice the rate by alternating asymmetrically.
//   - The window slides by frame, not by second. A burst that straddles a
//     second boundary is still a burst.
//
// EPS gives the run detector hysteresis, so ordinary per-frame dither inside a
// rising ramp does not chop it into a hundred tiny transitions.

const THRESHOLD = 0.10; // minimum amplitude of a qualifying change
const DARK = 0.80;      // ...and the darker state must be below this
const EPS = 0.02;       // reversal smaller than this is noise, not a turn
const LIMIT = 3;        // flashes per second

/**
 * @param {number[]} series mean relative luminance per frame, 0..1
 * @param {number} fps
 */
function analyse(series, fps) {
  const n = series.length;
  const out = {
    frames: n, fps,
    flashesPerSecond: 0,
    transitions: 0,
    maxAmplitude: 0,
    maxFrameStep: 0,
    worstFrame: 0,
    ok: true,
  };
  if (n < 2) return out;

  for (let i = 1; i < n; i++) {
    const d = Math.abs(series[i] - series[i - 1]);
    if (d > out.maxFrameStep) out.maxFrameStep = d;
  }

  /** @type {{frame:number, sign:number, amp:number}[]} */
  const transitions = [];
  let anchor = series[0];   // where the current monotone run started
  let extreme = series[0];  // the furthest it has reached
  let extremeAt = 0;
  let dir = 0;

  function close(at) {
    const amp = Math.abs(extreme - anchor);
    if (amp >= THRESHOLD && Math.min(extreme, anchor) < DARK) {
      transitions.push({ frame: at, sign: Math.sign(extreme - anchor), amp });
      if (amp > out.maxAmplitude) out.maxAmplitude = amp;
    }
    anchor = extreme;
  }

  for (let i = 1; i < n; i++) {
    const v = series[i];
    if (dir === 0) {
      if (v !== extreme) { dir = v > extreme ? 1 : -1; extreme = v; extremeAt = i; }
      continue;
    }
    const advancing = dir > 0 ? v >= extreme : v <= extreme;
    if (advancing) { extreme = v; extremeAt = i; continue; }
    if (Math.abs(v - extreme) < EPS) continue; // dither inside the run
    close(extremeAt);
    dir = -dir;
    extreme = v;
    extremeAt = i;
  }
  close(extremeAt);
  out.transitions = transitions.length;

  // Flashes: each opposing pair. Transitions alternate by construction, so
  // every one after the first closes a pair with its predecessor.
  const flashes = [];
  for (let i = 1; i < transitions.length; i++) flashes.push(transitions[i].frame);

  const window = Math.max(1, Math.round(fps));
  let best = 0, bestAt = 0, lo = 0;
  for (let hi = 0; hi < flashes.length; hi++) {
    while (flashes[hi] - flashes[lo] >= window) lo++;
    const count = hi - lo + 1;
    if (count > best) { best = count; bestAt = flashes[hi]; }
  }
  out.flashesPerSecond = best;
  out.worstFrame = bestAt;
  out.ok = best <= LIMIT;
  return out;
}

module.exports = { analyse, THRESHOLD, DARK, LIMIT };
