package space.quiet.arprobe

import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * SP-3.2 — the law under test: a recorder never invents a cause. Each word
 * of the closed vocabulary has an explicit bar, and the one assertion that
 * matters most is negative: silence alone NEVER earns `no_fix`.
 */
class GapClassifierTest {

    private val interval = 15_000L

    @Test
    fun silenceAloneIsNeverNoFix() {
        // A long quiet stretch, no provider claim, no measured sleep — the
        // phone might have been under trees, or a callback merely late.
        // The recorder does not know, and says so.
        assertEquals(
            GapClassifier.UNKNOWN,
            GapClassifier.classify(
                providerOff = false, spanMs = 10 * 60_000, sleepMs = 0, intervalMs = interval,
            ),
        )
    }

    @Test
    fun theProvidersOwnClaimEarnsNoFix() {
        assertEquals(
            GapClassifier.NO_FIX,
            GapClassifier.classify(
                providerOff = true, spanMs = 60_000, sleepMs = 0, intervalMs = interval,
            ),
        )
    }

    @Test
    fun provenSleepEarnsSuspended() {
        // 5 minutes silent, 4 min 40 s of it measured asleep by the clock
        // pair — the awake remainder fits inside two sampling intervals.
        assertEquals(
            GapClassifier.SUSPENDED,
            GapClassifier.classify(
                providerOff = false, spanMs = 300_000, sleepMs = 280_000, intervalMs = interval,
            ),
        )
    }

    @Test
    fun partialSleepStaysUnknown() {
        // The phone dozed for a minute of a ten-minute silence: sleep does
        // not COVER the span, so it does not explain it — promoting it
        // would let one nap excuse nine quiet minutes.
        assertEquals(
            GapClassifier.UNKNOWN,
            GapClassifier.classify(
                providerOff = false, spanMs = 600_000, sleepMs = 60_000, intervalMs = interval,
            ),
        )
    }

    @Test
    fun aPlatformClaimOutranksASleepMeasurement() {
        // Dozed AND the provider was off: the person's own act (location
        // disabled) is the stronger fact — there was no fix to sleep through.
        assertEquals(
            GapClassifier.NO_FIX,
            GapClassifier.classify(
                providerOff = true, spanMs = 300_000, sleepMs = 300_000, intervalMs = interval,
            ),
        )
    }

    @Test
    fun zeroSleepNeverRidesTheSlack() {
        // A span shorter than the 2-interval slack must not turn sleepMs=0
        // into "suspended" by arithmetic: the sleepMs > 0 guard is load-bearing.
        assertEquals(
            GapClassifier.UNKNOWN,
            GapClassifier.classify(
                providerOff = false, spanMs = 20_000, sleepMs = 0, intervalMs = interval,
            ),
        )
    }
}
