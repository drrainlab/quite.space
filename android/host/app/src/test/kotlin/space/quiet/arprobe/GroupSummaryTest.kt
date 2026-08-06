package space.quiet.arprobe

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * AR-1b.6b.3 — the one line above several conversations.
 *
 * A summary is the surface it is easiest to leak through: it shows where the
 * children are collapsed, on a lock screen, and to every notification
 * listener the person has allowed. So what it may say is a decision, and it
 * is made here rather than in the builder.
 */
class GroupSummaryTest {

    @Test
    fun aSummaryExistsOnlyForTwoOrMore() {
        // One child with a line above it is a person told the same thing
        // twice, and Android already renders a lone child on its own.
        assertFalse(GroupSummary.needed(0))
        assertFalse(GroupSummary.needed(1))
        assertTrue(GroupSummary.needed(2))
        assertTrue(GroupSummary.needed(9))
    }

    @Test
    fun hiddenDoesNotEvenSayHowManyConversations() {
        // "three spaces" is itself a fact about somebody's life, and the
        // strict mode is the one place that has to be counted as such.
        val text = GroupSummary.text(PresentationPolicy.HIDDEN, 3, 5)
        assertEquals("5 new signals", text)
        assertFalse("no count of conversations", text.contains("3"))
        assertFalse("no word for a space", text.contains("space"))
    }

    @Test
    fun spaceCountsConversationsWithoutNamingOne() {
        assertEquals("3 spaces · 5 new messages", GroupSummary.text(PresentationPolicy.SPACE, 3, 5))
    }

    @Test
    fun previewSaysNoMoreThanSpaceDoes() {
        // It is ALLOWED to say more, and there is nothing worth saying: the
        // conversations themselves are one tap away, and repeating one of them
        // here would put a name and a line into the collapsed view.
        assertEquals(
            GroupSummary.text(PresentationPolicy.SPACE, 3, 5),
            GroupSummary.text(PresentationPolicy.PREVIEW, 3, 5),
        )
    }

    @Test
    fun oneMessageReadsAsOne() {
        assertEquals("A new signal", GroupSummary.text(PresentationPolicy.HIDDEN, 2, 1))
        assertEquals("2 spaces · 1 new message", GroupSummary.text(PresentationPolicy.SPACE, 2, 1))
    }

    @Test
    fun theSummaryTagCannotCollideWithASpacesTag() {
        // Space tags are "space:<opaque key>"; a collision would make one
        // conversation and the summary overwrite each other.
        assertFalse(GroupSummary.TAG.startsWith("space:"))
    }
}
