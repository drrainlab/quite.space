package space.quiet.arprobe

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * AR-1b.2's decisions, on the JVM, with no device involved.
 *
 * These are the assertions the wave is judged on, and every one of them is a
 * case a plausible implementation gets wrong:
 *
 * ```
 *   suppressing on "the process is foreground"  silences every other space
 *   dropping the dedup on dismiss               the next sync brings it back
 *   marking read on tap                         loses a message nobody saw
 *   keeping the dedup only in memory            a restart re-announces
 *   a denylist instead of an allowlist          announces epoch rotations
 * ```
 */
class NotificationCoordinatorTest {

    /** The store, in memory. Same contract, no SharedPreferences. */
    private class FakeStore : NotificationCoordinator.Store {
        var ids: List<String> = emptyList()
        override fun rememberedIds(): List<String> = ids.toList()
        override fun writeRememberedIds(ids: List<String>) {
            this.ids = ids.toList()
        }
    }

    private class FakePresenter : NotificationCoordinator.Presenter {
        val presented = mutableListOf<NotificationCoordinator.Presentation>()
        val cleared = mutableListOf<String>()
        override fun present(p: NotificationCoordinator.Presentation) {
            presented.add(p)
        }

        override fun clear(spaceId: String) {
            cleared.add(spaceId)
        }
    }

    private lateinit var presenter: FakePresenter
    private lateinit var store: FakeStore
    private lateinit var co: NotificationCoordinator

    private fun fresh() {
        presenter = FakePresenter()
        store = FakeStore()
        co = NotificationCoordinator(presenter, store)
    }

    private fun arrive(eventId: String, spaceId: String) =
        co.onCandidate(eventId, spaceId, "device-b", "message.text.v1", 1000L, false)

    // ------------------------------------------------------------- the gates

    @Test
    fun ourOwnEventNeverNotifies() {
        fresh()
        val d = co.onCandidate("e1", "space-a", "device-me", "message.text.v1", 1L, true)
        assertEquals(NotificationCoordinator.Decision.SUPPRESSED_AUTHORED_LOCALLY, d)
        assertEquals(0, presenter.presented.size)
    }

    @Test
    fun carriersEditsAndMachineryDoNotNotify() {
        fresh()
        val quiet = listOf(
            "block.attached.v1",       // an asset carrier, not a message
            "message.revised.v1",      // an edit is not news
            "message.tombstoned.v1",   // neither is a withdrawal
            "membership.epoch.v1",     // key rotation
            "receipt.delivery.v1",     // a receipt about our own message
            "presence.update.v1",
            "some.future.v1",          // unknown: silence, not a guess
        )
        for (schema in quiet) {
            val d = co.onCandidate("e-$schema", "space-a", "device-b", schema, 1L, false)
            assertEquals(
                "$schema woke somebody",
                NotificationCoordinator.Decision.SUPPRESSED_SCHEMA_NOT_NOTIFIABLE, d,
            )
        }
        assertEquals(0, presenter.presented.size)
    }

    @Test
    fun anOrdinaryMessageFromSomebodyElseNotifiesExactlyOnce() {
        fresh()
        assertEquals(NotificationCoordinator.Decision.PRESENTED, arrive("e1", "space-a"))
        assertEquals(1, presenter.presented.size)
        assertEquals("space:space-a", presenter.presented[0].tag)
    }

    // ------------------------------------------------- foreground suppression

    @Test
    fun theSpaceOnScreenDoesNotBuzz() {
        fresh()
        co.onForeground(true)
        co.onVisibleSpace("space-a")
        assertEquals(
            NotificationCoordinator.Decision.SUPPRESSED_SPACE_ON_SCREEN,
            arrive("e1", "space-a"),
        )
        assertEquals(0, presenter.presented.size)
    }

    @Test
    fun anotherSpaceStillNotifiesWhileReadingOne() {
        // The reason suppression keys on the VISIBLE SPACE and not on "the
        // process is foreground": otherwise reading one conversation silences
        // every other one.
        fresh()
        co.onForeground(true)
        co.onVisibleSpace("space-a")
        assertEquals(NotificationCoordinator.Decision.PRESENTED, arrive("e1", "space-b"))
        assertEquals(1, presenter.presented.size)
    }

    @Test
    fun aSettingsScreenDoesNotSilenceEverything() {
        fresh()
        co.onForeground(true)
        co.onVisibleSpace(null)          // in the app, but in no conversation
        assertEquals(NotificationCoordinator.Decision.PRESENTED, arrive("e1", "space-a"))
    }

    @Test
    fun aBackgroundedAppNotifiesForTheSpaceItLastShowed() {
        fresh()
        co.onForeground(true)
        co.onVisibleSpace("space-a")
        co.onForeground(false)           // HOME pressed; the space is still "visible"
        assertEquals(NotificationCoordinator.Decision.PRESENTED, arrive("e1", "space-a"))
    }

    @Test
    fun whatWasSeenOnScreenIsNotResurrectedByAReconnect() {
        // The subtle one. Suppressing on screen must still REMEMBER the id:
        // otherwise the same event, re-delivered by a reconnect an hour later
        // with the app closed, arrives as news for a message already read.
        fresh()
        co.onForeground(true)
        co.onVisibleSpace("space-a")
        arrive("e1", "space-a")

        co.onForeground(false)
        co.onVisibleSpace(null)
        assertEquals(
            NotificationCoordinator.Decision.SUPPRESSED_ALREADY_PRESENTED,
            arrive("e1", "space-a"),
        )
        assertEquals(0, presenter.presented.size)
    }

    // ---------------------------------------------- posted / dismissed / read

    @Test
    fun dismissKeepsTheDedupSoTheNextSyncDoesNotBringItBack() {
        fresh()
        arrive("e1", "space-a")
        co.onDismissed("space-a")

        assertEquals(
            NotificationCoordinator.Decision.SUPPRESSED_ALREADY_PRESENTED,
            arrive("e1", "space-a"),
        )
        assertEquals(1, presenter.presented.size)
        assertTrue(
            "a swipe is not a read — nothing should have been cleared",
            presenter.cleared.isEmpty(),
        )
    }

    @Test
    fun readingTheConversationIsWhatClearsIt() {
        fresh()
        arrive("e1", "space-a")
        co.onRead("space-a")
        assertEquals(listOf("space-a"), presenter.cleared)
    }

    @Test
    fun aNewMessageAfterADismissStartsTheNotificationAfresh() {
        // Dismiss drops the aggregation but not the memory: the next message
        // is one message, not a repeat of the whole conversation.
        fresh()
        arrive("e1", "space-a")
        co.onDismissed("space-a")
        assertEquals(NotificationCoordinator.Decision.PRESENTED, arrive("e2", "space-a"))
        assertEquals(1, presenter.presented[1].items.size)
    }

    // --------------------------------------------------------- durable dedup

    @Test
    fun presentedIdsSurviveAProcessRestart() {
        fresh()
        arrive("e1", "space-a")

        // The process dies. A new coordinator comes up over the SAME store —
        // which is all a restart is, from this class's point of view.
        val after = FakePresenter()
        val restarted = NotificationCoordinator(after, store)

        assertEquals(
            NotificationCoordinator.Decision.SUPPRESSED_ALREADY_PRESENTED,
            restarted.onCandidate("e1", "space-a", "device-b", "message.text.v1", 1L, false),
        )
        assertEquals(0, after.presented.size)
    }

    @Test
    fun theRememberedSetIsBoundedAndEvictsTheOldest() {
        fresh()
        val n = NotificationCoordinator.REMEMBERED_IDS + 100
        for (i in 0 until n) arrive("e$i", "space-a")

        assertEquals(NotificationCoordinator.REMEMBERED_IDS, store.rememberedIds().size)

        // The newest is still remembered; the oldest has been evicted, which
        // is the bound doing exactly what it says. This is the case the class
        // comment records as NOT covered by the id set alone.
        assertEquals(
            NotificationCoordinator.Decision.SUPPRESSED_ALREADY_PRESENTED,
            arrive("e${n - 1}", "space-a"),
        )
        assertEquals(NotificationCoordinator.Decision.PRESENTED, arrive("e0", "space-a"))
    }

    // ------------------------------------------------------------ aggregation

    @Test
    fun aSpaceAggregatesOldestFirstAndIsBounded() {
        fresh()
        for (i in 0 until NotificationCoordinator.MESSAGES_PER_SPACE + 3) {
            arrive("e$i", "space-a")
        }
        val last = presenter.presented.last()
        assertEquals(NotificationCoordinator.MESSAGES_PER_SPACE, last.items.size)
        assertEquals("e3", last.items.first().eventId)
        assertEquals(
            "e${NotificationCoordinator.MESSAGES_PER_SPACE + 2}",
            last.items.last().eventId,
        )
    }

    @Test
    fun twoSpacesAreTwoConversationsWithTwoTags() {
        fresh()
        arrive("e1", "space-a")
        arrive("e2", "space-b")
        assertEquals("space:space-a", presenter.presented[0].tag)
        assertEquals("space:space-b", presenter.presented[1].tag)
    }

    // --------------------------------------------------- a refused permission

    @Test
    fun aRefusedPermissionIsAnOrdinaryStateAndTakesLiveOnesDown() {
        fresh()
        arrive("e1", "space-a")
        co.setEnabled(false)
        assertEquals(1, presenter.cleared.size)
        assertEquals(
            NotificationCoordinator.Decision.SUPPRESSED_DISABLED,
            arrive("e2", "space-a"),
        )
    }
}
