package space.quiet.arprobe

/**
 * SP-3.2 — naming the reason a recorder went quiet, without inventing one.
 *
 * THE LAW (brief, folded from the owner's review): silence is a TRIGGER for
 * recording a gap, never its cause. A stretch with no callbacks has at least
 * four honest readings — the sky went dark, the phone slept, a callback is
 * merely late, the buffer lied — and a recorder that picks the flattering
 * one is manufacturing evidence. So the vocabulary is closed and the bar for
 * each word is explicit:
 *
 *   `no_fix`     — the PLATFORM said so: onProviderDisabled fired, or the
 *                  provider reported unavailable, during the span. Nothing
 *                  else earns this word. Silence alone NEVER does.
 *   `suspended`  — the host PROVED sleep locally, with the same clock pair
 *                  the core uses (core.go mono_ns/boot_ns):
 *                  elapsedRealtime() advances through suspend,
 *                  uptimeMillis() does not — their divergence over the span
 *                  IS the time the phone spent asleep. The word is earned
 *                  when that measured sleep covers the silence, up to the
 *                  slack of two sampling intervals (a callback that was
 *                  merely queued across the doze is ordinary latency, not a
 *                  second cause).
 *   `unknown`    — everything else. A recorder that does not know why it
 *                  stopped hearing says so; `unknown` is not a failure of
 *                  the classifier, it is the classifier working.
 *
 * Order matters: an explicit platform claim outranks a sleep measurement —
 * a phone that dozed while the provider was switched off has no fix to
 * miss, and the person's own act (disabling location) is the stronger fact.
 */
internal object GapClassifier {

    const val NO_FIX = "no_fix"
    const val SUSPENDED = "suspended"
    const val UNKNOWN = "unknown"

    /**
     * @param providerOff  the platform explicitly reported the provider
     *                     disabled/unavailable at some point inside the span
     * @param spanMs       the silent stretch being classified
     * @param sleepMs      measured sleep inside the span:
     *                     Δ elapsedRealtime − Δ uptimeMillis
     * @param intervalMs   the sampling interval, for the latency slack
     */
    fun classify(providerOff: Boolean, spanMs: Long, sleepMs: Long, intervalMs: Long): String {
        if (providerOff) return NO_FIX
        if (sleepMs > 0 && sleepMs >= spanMs - 2 * intervalMs) return SUSPENDED
        return UNKNOWN
    }
}
