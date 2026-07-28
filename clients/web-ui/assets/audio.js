// @ts-check
// Who is allowed to make noise.
//
// There are seven independent playback owners in this client — the asset
// player, the listening room, the drone, voice recording's analyser, the
// acoustic pass transmitter and receiver, and bare <audio controls> inside
// publications — and the only coordination between any of them is two lines
// inside makeAudioPlayer that pause the previous asset player. Those two lines
// are never cleared, and they know nothing about the other six. Start a
// listening session and open something else with sound, and two worlds play at
// once.
//
// An ambient bed is the piece that must never do that. So: one arbiter, a
// deliberately minimal contract, and a rule that resolves every case it does
// not know about.
//
//   request(owner, priority) -> true | false
//   release(owner)
//   current() -> owner | null
//
// PRIORITY, weakest last:
//   room    a shared listening session — other people are hearing this too
//   player  something the person pressed play on
//   bed     a post's atmosphere, which nobody asked for out loud
//
// THE BED NEVER PREEMPTS. It asks, and if anything else holds the floor it
// declines and says why. Skipping a background is always better than
// accidentally running two worlds at once — and for the four owners not yet
// migrated, it also checks for any <audio>/<video> actually playing, so an
// unmigrated source still wins by default rather than being talked over.

const AUDIO = (() => {
  const RANK = { room: 3, player: 2, bed: 1 };

  let holder = null; // { owner, priority, onEvicted }

  /** Anything audible that has not been migrated to the arbiter yet. */
  function foreignSound() {
    for (const el of document.querySelectorAll('audio, video')) {
      const m = /** @type {HTMLMediaElement} */ (el);
      if (!m.paused && !m.ended && m.currentTime > 0 && !m.muted) return true;
    }
    return false;
  }

  /**
   * Ask for the floor.
   *
   * @param {string} owner a stable id, e.g. "bed:<postId>"
   * @param {'room'|'player'|'bed'} priority
   * @param {() => void} [onEvicted] called if something louder takes over
   * @returns {boolean} false means "do not play, and say so"
   */
  function request(owner, priority, onEvicted) {
    const rank = RANK[priority] || 0;

    // The bed's extra rule: it yields to anything at all, including the four
    // owners this arbiter does not manage yet.
    if (priority === 'bed' && (holder || foreignSound())) return false;

    if (holder && holder.owner !== owner) {
      if (RANK[holder.priority] >= rank) return false;
      // Louder wins, and the evicted owner is TOLD rather than discovering it
      // by having its sound quietly overlap somebody else's.
      const evicted = holder;
      holder = null;
      try { evicted.onEvicted?.(); } catch { /* not our problem to fix */ }
    }
    holder = { owner, priority, onEvicted };
    return true;
  }

  /** Give the floor back. Safe to call when you never held it. */
  function release(owner) {
    if (holder && holder.owner === owner) holder = null;
  }

  /** Who holds it, or null. */
  function current() { return holder ? holder.owner : null; }

  /** Why a bed cannot play right now, in words a person can act on. */
  function busyReason() {
    if (holder) {
      if (holder.priority === 'room') return 'a listening session is playing';
      return 'something else is playing';
    }
    if (foreignSound()) return 'something else is playing';
    return '';
  }

  return { request, release, current, busyReason };
})();

if (typeof window !== 'undefined') window.AUDIO = AUDIO;
