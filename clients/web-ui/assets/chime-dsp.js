'use strict';

// Quiet Chimes — the voice itself (DSP only).
//
// ONE MATHEMATICAL MODEL, TWO MOUTHS. The browser plays these samples
// through WebAudio (chime.js); the Android build renders the very same
// samples into WAV files for notification channels
// (scripts/sound/render-chimes.cjs). Every design constant lives HERE and
// nowhere else — changing the sonic identity is a patch to this file, and
// both platforms follow.
//
// The voice: a struck, physical timbre — sine partials with a fast attack,
// exponential decay, and a breath of filtered noise in the strike. Nothing
// loops, nothing rises, and importance NEVER maps to loudness: every tier
// is normalized to the same peak budget. The more important the event, the
// more DISTINCT the sound — longer, richer — never louder.
//
// Module shape: a plain script (the web-ui is classic-script, and an
// `export` keyword would refuse to parse there), published on globalThis
// and, when a CommonJS loader is present, on module.exports. The Node
// renderer and the harness require() this same file.

(function (g) {
  const CHIME_CONST = {
    // THE PEAK BUDGET. One ceiling for every tier — the anti-escalation
    // law as a number. No tier may buy a higher one by being "important".
    PEAK: 0.5,

    // tier 1 — tick: almost tactile, a fingernail on glass.
    TICK: { dur: 0.065, freq: 2100, decay: 0.016, noise: 0.25, noiseDur: 0.004 },

    // tier 2 — room: two soft partials and a hint of the walls.
    ROOM: {
      dur: 0.20, freq: 700, ratio: 1.26, decay: 0.06,
      reflections: [[0.040, 0.35], [0.095, 0.20]],
    },

    // tier 3 — the signature: a warm strike and its answer a fifth above,
    // gliding home. This IS the quite.space sound.
    SIGNAL: {
      dur: 0.45,
      strike: { freq: 520, inharm: 1.38, inharmAmp: 0.35, decay: 0.11 },
      answer: { at: 0.090, freq: 780, glide: 0.015, glideDur: 0.20, decay: 0.14, amp: 0.8 },
      breath: { amp: 0.18, dur: 0.008 },
    },

    // tier 4 — personal: the same motif joined by a low octave answer.
    // Richer and longer than the signature — measured, never louder.
    PERSONAL: { at: 0.160, freq: 260, decay: 0.20, amp: 0.55, dur: 0.65 },

    // Modifiers: parameter tweaks on the same voice, never new melodies.
    MOD: {
      person:     { freqScale: 0.85, lowpassHz: 1400 },            // warmer
      instrument: { attack: 0.001, inharm: 1.52, decayScale: 0.7 }, // drier
      space:      { decayScale: 1.25, reflections: [[0.050, 0.30], [0.120, 0.18]] }, // roomier
    },

    ATTACK: 0.004, // default strike attack — fast, but never a click
  };

  // ---- primitives -------------------------------------------------------

  /** One decaying partial added into `out`. */
  function partial(out, sr, { at = 0, freq, amp = 1, decay, attack = CHIME_CONST.ATTACK,
    glide = 0, glideDur = 0.001 }) {
    const start = Math.floor(at * sr);
    let phase = 0;
    for (let i = start; i < out.length; i++) {
      const t = (i - start) / sr;
      // The glide bends the frequency DOWN by its fraction over glideDur —
      // integrated as phase so the bend is smooth, never a chirp artifact.
      const bent = freq * (1 - glide * Math.min(1, t / glideDur));
      phase += (2 * Math.PI * bent) / sr;
      const env = Math.min(1, t / attack) * Math.exp(-t / decay);
      if (env < 1e-4 && t > attack) break;
      out[i] += amp * env * Math.sin(phase);
    }
  }

  /** The breath: a short burst of lowpassed noise in the strike. */
  function breath(out, sr, { at = 0, amp, dur }) {
    const start = Math.floor(at * sr);
    // Deterministic noise: the same breath every time. A chime that varies
    // per play would read as a different sound, not a texture.
    let seed = 0x9e3779b9;
    const rnd = () => {
      seed ^= seed << 13; seed ^= seed >>> 17; seed ^= seed << 5;
      return ((seed >>> 0) / 0xffffffff) * 2 - 1;
    };
    let lp = 0;
    for (let i = start; i < out.length; i++) {
      const t = (i - start) / sr;
      if (t > dur * 6) break;
      lp += 0.25 * (rnd() - lp); // one-pole lowpass — breath, not hiss
      out[i] += amp * lp * Math.exp(-t / dur);
    }
  }

  /** Early reflections: the sound of the room agreeing, twice, quietly. */
  function reflect(buf, sr, pairs) {
    const dry = buf.slice();
    for (const [delay, gain] of pairs) {
      const d = Math.floor(delay * sr);
      for (let i = d; i < buf.length; i++) buf[i] += dry[i - d] * gain;
    }
  }

  /** One-pole lowpass over the finished buffer (the "warmer" modifier). */
  function lowpass(buf, sr, hz) {
    const a = 1 - Math.exp((-2 * Math.PI * hz) / sr);
    let y = 0;
    for (let i = 0; i < buf.length; i++) { y += a * (buf[i] - y); buf[i] = y; }
  }

  /** Normalize to the ONE peak budget. Loudness is not a hierarchy. */
  function normalize(buf, peak, sr) {
    let max = 0;
    for (let i = 0; i < buf.length; i++) max = Math.max(max, Math.abs(buf[i]));
    if (max > 0) { const k = peak / max; for (let i = 0; i < buf.length; i++) buf[i] *= k; }
    // A 3 ms fade-out so no buffer ever ends on a step.
    const fade = Math.floor(0.003 * sr);
    for (let i = 0; i < fade && i < buf.length; i++) {
      buf[buf.length - 1 - i] *= i / fade;
    }
  }

  // ---- the four tiers ---------------------------------------------------

  function renderChime(tier, modifier, sampleRate) {
    const sr = sampleRate || 44100;
    const C = CHIME_CONST;
    const mod = (modifier && C.MOD[modifier]) || {};
    const fScale = mod.freqScale || 1;
    const dScale = mod.decayScale || 1;
    const attack = mod.attack || C.ATTACK;

    let dur, build;
    switch (tier) {
      case 'tick': {
        dur = C.TICK.dur;
        build = (out) => {
          partial(out, sr, { freq: C.TICK.freq * fScale, decay: C.TICK.decay * dScale, attack });
          breath(out, sr, { amp: C.TICK.noise, dur: C.TICK.noiseDur });
        };
        break;
      }
      case 'room': {
        dur = C.ROOM.dur;
        build = (out) => {
          partial(out, sr, { freq: C.ROOM.freq * fScale, decay: C.ROOM.decay * dScale, attack });
          partial(out, sr, { freq: C.ROOM.freq * C.ROOM.ratio * fScale, amp: 0.6,
            decay: C.ROOM.decay * 0.8 * dScale, attack });
          reflect(out, sr, C.ROOM.reflections);
        };
        break;
      }
      case 'signal':
      case 'personal': {
        const S = C.SIGNAL;
        const inharm = mod.inharm || S.strike.inharm;
        dur = tier === 'personal' ? C.PERSONAL.dur : S.dur;
        build = (out) => {
          partial(out, sr, { freq: S.strike.freq * fScale, decay: S.strike.decay * dScale, attack });
          partial(out, sr, { freq: S.strike.freq * inharm * fScale, amp: S.strike.inharmAmp,
            decay: S.strike.decay * 0.6 * dScale, attack });
          partial(out, sr, { at: S.answer.at, freq: S.answer.freq * fScale, amp: S.answer.amp,
            decay: S.answer.decay * dScale, attack, glide: S.answer.glide, glideDur: S.answer.glideDur });
          breath(out, sr, { amp: S.breath.amp, dur: S.breath.dur });
          if (tier === 'personal') {
            // The low answer: the same voice reaching down an octave —
            // what makes it richer is REACH, not force.
            partial(out, sr, { at: C.PERSONAL.at, freq: C.PERSONAL.freq * fScale,
              amp: C.PERSONAL.amp, decay: C.PERSONAL.decay * dScale, attack });
          }
        };
        break;
      }
      default:
        throw new Error('chime-dsp: unknown tier ' + tier);
    }

    const out = new Float32Array(Math.ceil(dur * dScale * sr));
    build(out);
    if (mod.reflections) reflect(out, sr, mod.reflections);
    if (mod.lowpassHz) lowpass(out, sr, mod.lowpassHz);
    normalize(out, C.PEAK, sr);
    return out;
  }

  const ChimeDSP = { renderChime, CHIME_CONST };
  g.ChimeDSP = ChimeDSP;
  if (typeof module !== 'undefined' && module.exports) module.exports = ChimeDSP;
})(typeof globalThis !== 'undefined' ? globalThis : this);
