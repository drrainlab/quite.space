'use strict';

// Quiet Chimes — the player and the LAW (chime-dsp.js is the voice).
//
// The product rule this file enforces: the system voices the MEANING of
// what happened, not the count of things that happened. Concretely:
//
//   • candidates collect for a short arbitration window, and only the
//     highest of personal > signal > room > tick sounds;
//   • the winner's FAMILY then cools down — message={tick},
//     presence={room}, attention={signal, personal} — one family, one
//     clock: after a personal chime, a plain signal a second later is
//     silent, but a tick still speaks (its own family, its own clock);
//   • importance never touches gain. The master volume is one hard-capped
//     constant; distinctness comes from the DSP, not the fader.
//
// Deference, not ownership: a chime never takes the AUDIO arbiter's stage.
// It mixes over beds and players, but yields entirely to the listening
// room (music outranks chimes) and to a live recorder.
//
// Visibility is ROUTING POLICY, not an invariant: visible → in-app chime;
// hidden → nothing FOR NOW, because no browser/platform notification
// exists on web/desktop yet — that branch is exactly where one would plug
// in. Hidden must never be read as "guaranteed silence by design".

const CHIME = (() => {
  const RANK = { personal: 4, signal: 3, room: 2, tick: 1 };
  const FAMILY = { tick: 'message', room: 'presence', signal: 'attention', personal: 'attention' };
  const WINDOW_MS = 180;   // the arbitration window
  const COOLDOWN_MS = 3000; // per FAMILY, from the winner's play
  const MASTER_GAIN = 0.12; // hard cap — never loud, never sudden (playDrone's law)

  const KEY = (tier) => 'qp.chime.' + tier;
  // Defaults from the product sketch: ordinary messages are quiet by
  // default; the person decides what has the right to sound.
  const DEFAULTS = { tick: false, room: true, signal: true, personal: true };

  function enabled(tier) {
    try {
      const v = localStorage.getItem(KEY(tier));
      return v === null ? DEFAULTS[tier] : v === 'on';
    } catch (e) { return DEFAULTS[tier]; }
  }
  function setEnabled(tier, on) {
    try { localStorage.setItem(KEY(tier), on ? 'on' : 'off'); } catch (e) { /* device-local nicety */ }
  }

  // ---- the pure arbitration core (driven directly by the harness) ------
  //
  // arbitrate(candidates, familyUntil, now) → { winner, familyUntil }
  // candidates: [{tier, modifier}]; familyUntil: {family: untilMs}.
  // Rule order matters: a cooling family's candidates are OUT of the race
  // first, THEN the highest rank of what remains wins. A personal during
  // attention-cooldown must not shadow a tick that was free to play.
  function arbitrate(candidates, familyUntil, now) {
    const free = candidates.filter((c) => (familyUntil[FAMILY[c.tier]] || 0) <= now);
    if (!free.length) return { winner: null, familyUntil };
    let winner = free[0];
    for (const c of free) if (RANK[c.tier] > RANK[winner.tier]) winner = c;
    const next = { ...familyUntil };
    next[FAMILY[winner.tier]] = now + COOLDOWN_MS;
    return { winner, familyUntil: next };
  }

  // ---- the player -------------------------------------------------------

  let ctx = null;
  let armed = false;

  // Autoplay law: a context is only reliable after a user gesture. The
  // first pointer/key anywhere arms one shared context for the life of
  // the page; a chime requested before that is simply not owed to anybody.
  function arm() {
    if (armed || typeof document === 'undefined') return;
    armed = true;
    const wake = () => {
      try {
        if (!ctx) ctx = new (window.AudioContext || window.webkitAudioContext)();
        if (ctx.state === 'suspended') ctx.resume();
      } catch (e) { /* no audio here — a silent device is a valid device */ }
      document.removeEventListener('pointerdown', wake, true);
      document.removeEventListener('keydown', wake, true);
    };
    document.addEventListener('pointerdown', wake, true);
    document.addEventListener('keydown', wake, true);
  }
  arm();

  const bufferCache = new Map(); // tier:modifier:sr → AudioBuffer

  function bufferFor(tier, modifier) {
    const key = tier + ':' + (modifier || '') + ':' + ctx.sampleRate;
    let buf = bufferCache.get(key);
    if (!buf) {
      const pcm = ChimeDSP.renderChime(tier, modifier || null, ctx.sampleRate);
      buf = ctx.createBuffer(1, pcm.length, ctx.sampleRate);
      buf.copyToChannel(pcm, 0);
      bufferCache.set(key, buf);
    }
    return buf;
  }

  function deferred() {
    // The listening room is somebody's music; a live recorder is
    // somebody's voice. Both outrank a chime completely.
    try {
      if (typeof AUDIO !== 'undefined' && String(AUDIO.current() || '').startsWith('room:')) return true;
    } catch (e) { /* arbiter optional */ }
    if (typeof window !== 'undefined' && window._recorderLive) return true;
    return false;
  }

  function sound(tier, modifier) {
    if (!ctx || ctx.state !== 'running') return;
    if (deferred()) return;
    try {
      const src = ctx.createBufferSource();
      src.buffer = bufferFor(tier, modifier);
      const g = ctx.createGain();
      g.gain.value = MASTER_GAIN;
      src.connect(g).connect(ctx.destination);
      src.start();
    } catch (e) { /* a chime that failed is a chime that didn't matter */ }
  }

  // ---- the window -------------------------------------------------------

  let pending = [];
  let windowTimer = null;
  let familyUntil = {};

  function resolve() {
    windowTimer = null;
    const batch = pending;
    pending = [];
    const now = Date.now();
    const r = arbitrate(batch, familyUntil, now);
    familyUntil = r.familyUntil;
    if (!r.winner) return;
    // Routing policy (see header): visible plays in-app; hidden is the
    // future platform-notification branch, empty today.
    if (typeof document !== 'undefined' && document.visibilityState !== 'visible') return;
    sound(r.winner.tier, r.winner.modifier);
  }

  function play(tier, modifier) {
    if (!RANK[tier] || !enabled(tier)) return;
    pending.push({ tier, modifier: modifier || null });
    if (!windowTimer) windowTimer = setTimeout(resolve, WINDOW_MS);
  }

  /** The settings rows demo themselves: bypasses window, cooldown and the
   *  enabled check (the row was JUST turned on), honors only the master
   *  gain and the deference rules. */
  function preview(tier, modifier) {
    if (!ctx) return false;
    if (ctx.state !== 'running') { try { ctx.resume(); } catch (e) { } }
    sound(tier, modifier);
    return true;
  }

  return { play, preview, enabled, setEnabled, _arbitrate: arbitrate,
    _families: FAMILY, _windowMs: WINDOW_MS, _cooldownMs: COOLDOWN_MS };
})();

if (typeof module !== 'undefined' && module.exports) module.exports = CHIME;
