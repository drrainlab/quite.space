package space.quiet.arprobe

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Test

/**
 * AR-1c.1 — the sentence on a card that sits in somebody's shade for hours.
 *
 * IT IS THE MOST PUBLIC NOTIFICATION THIS APP HAS. It is permanent, it is on
 * the lock screen, and every notification listener on the phone can read it —
 * so what it may say is a privacy decision, not copy. It carries a STATE, not
 * a life: how the device is reachable and when it last heard anything.
 *
 * The tests below are mostly negative for that reason. "2 relays · just now"
 * is a state; "3 messages from Anna" would be a conversation, and it would sit
 * there all day where the message notifications' policy cannot reach it.
 */
class AvailabilityTextTest {

    private fun state(
        relays: Int = 1,
        since: Long = 10,
        alive: Boolean = true,
    ) = AvailabilityText.State(relays, since, alive)

    @Test
    fun itSaysHowItIsReachableAndWhenItLastHeard() {
        assertEquals("1 relay · just now", AvailabilityText.summary(state()))
        assertEquals("2 relays · 5 min ago", AvailabilityText.summary(state(relays = 2, since = 300)))
        assertEquals("No relay · 2 h ago", AvailabilityText.summary(state(relays = 0, since = 7200)))
    }

    /**
     * NEVER "NOTHING IS WRONG" WHEN THE CORE IS SHUT. The mode can be on while
     * the node is locked — that is an ordinary state after a restart — and a
     * card claiming a connection then would be telling somebody they are
     * reachable when they are not.
     */
    @Test
    fun aClosedCoreIsSaidPlainly() {
        assertEquals("Waiting to be unlocked", AvailabilityText.summary(state(alive = false)))
        assertEquals(
            "and the relay count is not smuggled into it either",
            "Waiting to be unlocked",
            AvailabilityText.summary(state(relays = 2, since = 1, alive = false)),
        )
    }

    /** Nothing heard yet is its own answer, not "just now". */
    @Test
    fun havingHeardNothingIsNotTheSameAsHavingHeardSomething() {
        assertEquals("1 relay · nothing yet", AvailabilityText.summary(state(since = -1)))
    }

    /**
     * The whole point, asserted as a negative: no space, no sender, no text,
     * no counts of who wrote. If this ever needs to change, it needs a privacy
     * decision first — the same one PresentationPolicy exists for.
     */
    @Test
    fun itNeverCarriesAnythingAboutAConversation() {
        val said = buildList {
            for (r in 0..3) for (s in listOf(-1L, 0L, 100L, 100_000L)) {
                add(AvailabilityText.summary(state(relays = r, since = s)))
            }
            add(AvailabilityText.summary(state(alive = false)))
            add(AvailabilityService.TITLE)
            add(AvailabilityService.STOP_LABEL)
        }
        for (line in said) {
            for (word in listOf("message", "wrote", "from", "space", "@")) {
                assertFalse(
                    "the permanent card said \"$line\", which reads as a conversation",
                    line.lowercase().contains(word),
                )
            }
        }
    }
}
