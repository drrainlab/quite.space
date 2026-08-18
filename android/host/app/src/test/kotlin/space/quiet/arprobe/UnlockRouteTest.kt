package space.quiet.arprobe

import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * The launch decision, checked without a handset — which is the point of it
 * being a class rather than an if in onCreate.
 */
class UnlockRouteTest {

    @Test
    fun `nothing set up asks`() {
        assertEquals(
            UnlockRoute.ASK,
            UnlockRoute.of(biometric = false, code = false, remembered = false),
        )
    }

    @Test
    fun `a code outranks a remembered passphrase`() {
        // The lesson a real handset taught: an auto-open in front of the
        // keypad meant the node was alive before the code could be asked.
        assertEquals(
            UnlockRoute.CODE,
            UnlockRoute.of(biometric = false, code = true, remembered = true),
        )
    }

    @Test
    fun `a face outranks everything`() {
        assertEquals(
            UnlockRoute.BIOMETRIC,
            UnlockRoute.of(biometric = true, code = true, remembered = true),
        )
    }

    @Test
    fun `a remembered passphrase opens when it is the only thing set up`() {
        assertEquals(
            UnlockRoute.REMEMBERED,
            UnlockRoute.of(biometric = false, code = false, remembered = true),
        )
    }

    /**
     * THE LOOP THAT MUST NOT EXIST. If a refusal could route back to the
     * prompt, cancelling it would raise it again, and the app would be a
     * device somebody cannot get into by any door — including the ones they
     * set up themselves.
     */
    @Test
    fun `a refused face never routes back to the face`() {
        for (code in listOf(true, false)) {
            for (remembered in listOf(true, false)) {
                val r = UnlockRoute.refused(code, remembered)
                if (r == UnlockRoute.BIOMETRIC) {
                    throw AssertionError("refused($code, $remembered) looped back to the prompt")
                }
            }
        }
    }

    @Test
    fun `a refused face falls through to the other doors in the same order`() {
        assertEquals(UnlockRoute.CODE, UnlockRoute.refused(code = true, remembered = true))
        assertEquals(UnlockRoute.REMEMBERED, UnlockRoute.refused(code = false, remembered = true))
        assertEquals(UnlockRoute.ASK, UnlockRoute.refused(code = false, remembered = false))
    }
}
