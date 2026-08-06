package space.quiet.arprobe

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * AR-1b.2 + AR-1b.5.3c's decisions, on the JVM, with no device involved.
 *
 * Every one of these is a case a plausible implementation gets wrong:
 *
 * ```
 *   suppressing on "the process is foreground"  silences every other space
 *   dropping the dedup on dismiss               the next sync brings it back
 *   marking read on tap                         loses a message nobody saw
 *   acknowledging before the durable insert     a crash drops it from both
 *   queueing while the permission is refused    a week of cards on grant day
 *   a denylist instead of an allowlist          announces epoch rotations
 * ```
 */
class NotificationCoordinatorTest {

    /**
     * The ledger, in memory, behaving like the SQLite one: insert-or-ignore on
     * the event id, generations that close on dismiss and read, and an opaque
     * key per space.
     */
    private class FakeLedger : NotificationCoordinator.Ledger {
        class Row(
            val c: NotificationCoordinator.Candidate,
            val generation: Int,
            var state: String,
        )

        val rows = LinkedHashMap<String, Row>()
        val keys = mutableMapOf<String, String>()
        val generations = mutableMapOf<String, Int>()

        override fun insertPending(c: NotificationCoordinator.Candidate):
            NotificationCoordinator.Insert? {
            if (rows.containsKey(c.eventId)) return null
            keys.getOrPut(c.spaceId) { "k${keys.size}" }
            val gen = generations.getOrPut(c.spaceId) { 0 }
            rows[c.eventId] = Row(c, gen, NotificationDb.STATE_PENDING)
            return NotificationCoordinator.Insert(tagFor(c.spaceId)!!, c.spaceLabel, live(c.spaceId))
        }

        override fun markPresented(eventId: String) {
            rows[eventId]?.state = NotificationDb.STATE_PRESENTED
        }

        override fun markSuppressed(eventId: String, reason: String) {
            rows[eventId]?.state = reason
        }

        override fun dismiss(spaceId: String) = close(spaceId, NotificationDb.STATE_DISMISSED)

        override fun read(spaceId: String) = close(spaceId, NotificationDb.STATE_READ)

        override fun pendingBySpace(): Map<String, NotificationCoordinator.Recovery> =
            rows.values.filter { it.state == NotificationDb.STATE_PENDING }
                .groupBy { it.c.spaceId }
                .mapValues { (space, _) ->
                    NotificationCoordinator.Recovery(tagFor(space)!!, "Room", live(space))
                }

        override fun activeBySpace(): Map<String, NotificationCoordinator.Recovery> =
            rows.values.filter {
                it.state == NotificationDb.STATE_PENDING ||
                    it.state == NotificationDb.STATE_PRESENTED
            }.groupBy { it.c.spaceId }
                .mapValues { (space, _) ->
                    NotificationCoordinator.Recovery(tagFor(space)!!, "Room", live(space))
                }

        override fun presentedBySpace(): Map<String, NotificationCoordinator.Recovery> =
            rows.values.filter { it.state == NotificationDb.STATE_PRESENTED }
                .groupBy { it.c.spaceId }
                .mapValues { (space, _) ->
                    NotificationCoordinator.Recovery(tagFor(space)!!, "Room", live(space))
                }

        override fun forgetSpace(spaceId: String) {
            rows.values.filter { it.c.spaceId == spaceId }.map { it.c.eventId }
                .forEach { rows.remove(it) }
            keys.remove(spaceId)
            generations.remove(spaceId)
        }

        override fun tagFor(spaceId: String): String? = keys[spaceId]?.let { "space:$it" }

        val personKeys = mutableMapOf<String, String>()
        override fun personKey(device: String, label: String): String =
            personKeys.getOrPut(device) { "person${personKeys.size}" }

        override fun compact(retainFrom: Map<String, Map<String, Long>>): Int {
            val terminal = setOf(
                NotificationDb.STATE_DISMISSED, NotificationDb.STATE_READ,
                NotificationDb.STATE_SUPPRESSED_ON_SCREEN,
                NotificationDb.STATE_SUPPRESSED_PERMISSION,
            )
            val gone = rows.filterValues { r ->
                val floor = retainFrom[r.c.spaceId]?.get(r.c.device) ?: 0L
                r.state in terminal && floor > 0 && r.c.sourceSequence < floor
            }.keys.toList()
            gone.forEach { rows.remove(it) }
            return gone.size
        }

        fun state(eventId: String): String? = rows[eventId]?.state

        private fun close(spaceId: String, terminal: String) {
            rows.values.filter {
                it.c.spaceId == spaceId &&
                    (it.state == NotificationDb.STATE_PENDING ||
                        it.state == NotificationDb.STATE_PRESENTED)
            }.forEach { it.state = terminal }
            generations[spaceId] = (generations[spaceId] ?: 0) + 1
        }

        private fun live(spaceId: String): List<NotificationCoordinator.Item> {
            val gen = generations[spaceId] ?: 0
            return rows.values
                .filter {
                    it.c.spaceId == spaceId && it.generation == gen &&
                        (it.state == NotificationDb.STATE_PENDING ||
                            it.state == NotificationDb.STATE_PRESENTED)
                }
                .map {
                    NotificationCoordinator.Item(
                        it.c.eventId, it.c.device, it.c.schema,
                        it.c.occurredAtUnixMs, it.c.senderLabel, it.c.previewText,
                    )
                }
                .takeLast(NotificationCoordinator.MESSAGES_PER_SPACE)
        }
    }

    private class FakePresenter : NotificationCoordinator.Presenter {
        val presented = mutableListOf<NotificationCoordinator.Presentation>()
        val cleared = mutableListOf<String>()
        override fun present(p: NotificationCoordinator.Presentation) {
            presented.add(p)
        }

        override fun clear(spaceId: String, tag: String) {
            cleared.add(spaceId)
        }

        val retired = mutableListOf<String>()
        override fun retireConversation(spaceId: String, tag: String) {
            retired.add(spaceId)
        }
    }

    private lateinit var presenter: FakePresenter
    private lateinit var ledger: FakeLedger
    private lateinit var co: NotificationCoordinator
    private lateinit var acked: MutableList<String>
    private var cursor = 0L

    private fun fresh() {
        presenter = FakePresenter()
        ledger = FakeLedger()
        acked = mutableListOf()
        co = NotificationCoordinator(presenter, ledger) { acked.add(it) }
    }

    private fun candidate(
        eventId: String,
        spaceId: String,
        schema: String = "message.text.v1",
        authoredLocally: Boolean = false,
        device: String = "device-b",
    ) = NotificationCoordinator.Candidate(
        eventId = eventId,
        spaceId = spaceId,
        device = device,
        schema = schema,
        sourceSequence = cursor + 1,
        occurredAtUnixMs = 1_700_000_000_000L + (++cursor),
        presentationCursor = cursor,
        authoredLocally = authoredLocally,
        spaceLabel = "Room",
        senderLabel = "bob",
        previewText = "a line of text",
    )

    private fun arrive(eventId: String, spaceId: String) = co.onCandidate(candidate(eventId, spaceId))

    // ------------------------------------------------------------- the gates

    @Test
    fun ourOwnEventNeverNotifiesAndIsStillAcknowledged() {
        fresh()
        val d = co.onCandidate(candidate("e1", "space-a", authoredLocally = true, device = "me"))
        assertEquals(NotificationCoordinator.Decision.SUPPRESSED_AUTHORED_LOCALLY, d)
        assertEquals(0, presenter.presented.size)
        // Acknowledged anyway: an unacknowledged event pins the core's
        // watermark, and the ones behind it would never be forgotten either.
        assertEquals(listOf("e1"), acked)
    }

    @Test
    fun carriersEditsAndMachineryDoNotNotifyAndAreAcknowledged() {
        fresh()
        val quiet = listOf(
            "block.attached.v1",       // an asset carrier, not a message
            "message.revised.v1",      // an edit is not news
            "message.tombstoned.v1",   // neither is a withdrawal
            "membership.epoch.v1",     // key rotation
            "receipt.delivery.v1",
            "presence.update.v1",
            "some.future.v1",          // unknown: silence, not a guess
        )
        for (schema in quiet) {
            val d = co.onCandidate(candidate("e-$schema", "space-a", schema = schema))
            assertEquals(
                "$schema woke somebody",
                NotificationCoordinator.Decision.SUPPRESSED_SCHEMA_NOT_NOTIFIABLE, d,
            )
        }
        assertEquals(0, presenter.presented.size)
        assertEquals(quiet.size, acked.size)
    }

    @Test
    fun anOrdinaryMessageIsDurableBeforeItIsAcknowledgedAndPosted() {
        fresh()
        assertEquals(NotificationCoordinator.Decision.PRESENTED, arrive("e1", "space-a"))
        assertEquals(1, presenter.presented.size)
        assertEquals("space:k0", presenter.presented[0].tag)
        assertEquals(listOf("e1"), acked)
        assertEquals(NotificationDb.STATE_PRESENTED, ledger.state("e1"))
    }

    @Test
    fun aTagNeverCarriesTheSpaceId() {
        fresh()
        arrive("e1", "space-a")
        arrive("e2", "space-b")
        assertFalse(
            "every notification listener the person has granted access to reads " +
                "the tag verbatim",
            presenter.presented[0].tag.contains("space-a"),
        )
        assertEquals("space:k1", presenter.presented[1].tag)
    }

    // ------------------------------------------------- foreground suppression

    @Test
    fun theSpaceOnScreenDoesNotBuzzAndIsRecordedAnyway() {
        fresh()
        co.onForeground(true)
        co.onVisibleSpace("space-a")
        assertEquals(
            NotificationCoordinator.Decision.SUPPRESSED_SPACE_ON_SCREEN,
            arrive("e1", "space-a"),
        )
        assertEquals(0, presenter.presented.size)
        assertEquals(NotificationDb.STATE_SUPPRESSED_ON_SCREEN, ledger.state("e1"))
    }

    @Test
    fun anotherSpaceStillNotifiesWhileReadingOne() {
        fresh()
        co.onForeground(true)
        co.onVisibleSpace("space-a")
        assertEquals(NotificationCoordinator.Decision.PRESENTED, arrive("e1", "space-b"))
    }

    @Test
    fun aSettingsScreenDoesNotSilenceEverything() {
        fresh()
        co.onForeground(true)
        co.onVisibleSpace(null)
        assertEquals(NotificationCoordinator.Decision.PRESENTED, arrive("e1", "space-a"))
    }

    @Test
    fun whatWasSeenOnScreenIsNotResurrectedByAReconnect() {
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
        assertEquals(NotificationDb.STATE_DISMISSED, ledger.state("e1"))
    }

    @Test
    fun aNewMessageAfterADismissStartsTheNotificationAfresh() {
        fresh()
        arrive("e1", "space-a")
        co.onDismissed("space-a")
        assertEquals(NotificationCoordinator.Decision.PRESENTED, arrive("e2", "space-a"))
        assertEquals(1, presenter.presented[1].items.size)
    }

    @Test
    fun readingTheConversationIsWhatClearsIt() {
        fresh()
        arrive("e1", "space-a")
        co.onRead("space-a")
        assertEquals(listOf("space-a"), presenter.cleared)
        assertEquals(NotificationDb.STATE_READ, ledger.state("e1"))
    }

    // ------------------------------------------------------ crash recovery

    @Test
    fun aCandidateStoredButNeverPostedIsPostedOnTheNextStart() {
        // Gate 7: acknowledged to the core, the row written, and the process
        // died before the system was told. The core will not replay it — it
        // was acknowledged — so this side has to.
        fresh()
        val c = candidate("e1", "space-a")
        ledger.insertPending(c)      // durable, but nothing was posted

        val after = FakePresenter()
        val restarted = NotificationCoordinator(after, ledger) { acked.add(it) }
        restarted.recoverPending()

        assertEquals(1, after.presented.size)
        assertEquals("space:k0", after.presented[0].tag)
        assertEquals(NotificationDb.STATE_PRESENTED, ledger.state("e1"))
    }

    @Test
    fun aCandidateAlreadyPostedIsNotPostedTwiceByRecovery() {
        // Gate 8: the notification WAS posted and the process died before the
        // row said so. Recovery posts it again under the same (tag, id), which
        // updates one system entry rather than making a second — and once
        // marked presented, a second recovery does nothing at all.
        fresh()
        arrive("e1", "space-a")
        val after = FakePresenter()
        val restarted = NotificationCoordinator(after, ledger) { acked.add(it) }
        restarted.recoverPending()
        assertEquals("nothing was pending; recovery should have posted nothing",
            0, after.presented.size)
    }

    @Test
    fun aRedeliveryFromTheCoreIsAcknowledgedAgainAndChangesNothing() {
        // Gate 6: the row was written and the process died before the core
        // heard the acknowledgement, so the log replays the same event. The
        // count must not move and the acknowledgement must happen.
        fresh()
        arrive("e1", "space-a")
        acked.clear()

        assertEquals(
            NotificationCoordinator.Decision.SUPPRESSED_ALREADY_PRESENTED,
            arrive("e1", "space-a"),
        )
        assertEquals(listOf("e1"), acked)
        assertEquals(1, presenter.presented.size)
    }

    // ----------------------------------------------------- a refused permission

    @Test
    fun aRefusedPermissionIsTerminalRatherThanAQueue() {
        fresh()
        co.setEnabled(false)
        assertEquals(NotificationCoordinator.Decision.SUPPRESSED_DISABLED, arrive("e1", "space-a"))
        assertEquals(NotificationDb.STATE_SUPPRESSED_PERMISSION, ledger.state("e1"))
        assertEquals("it must still be acknowledged, or the core keeps replaying it",
            listOf("e1"), acked)

        // Turning notifications back on must not produce what arrived while
        // they were off. A person who enables notifications wants the next
        // message, not a week of them.
        co.setEnabled(true)
        co.recoverPending()
        assertEquals(0, presenter.presented.size)
    }

    // ------------------------------------------------- storage that will not

    @Test
    fun aFailedTransactionIsNotAcknowledgedSoTheCoreKeepsIt() {
        // Gate 5, the host half. A full disk or a locked database must cost a
        // LATE notification, never a missing one — and the only thing standing
        // between those two outcomes is that the acknowledgement does not
        // happen.
        fresh()
        val failing = object : NotificationCoordinator.Ledger by ledger {
            override fun insertPending(c: NotificationCoordinator.Candidate) =
                throw IllegalStateException("disk full")
        }
        val co2 = NotificationCoordinator(presenter, failing) { acked.add(it) }

        assertEquals(
            NotificationCoordinator.Decision.DEFERRED_STORAGE_UNAVAILABLE,
            co2.onCandidate(candidate("e1", "space-a")),
        )
        assertTrue("an unstored candidate must NOT be acknowledged", acked.isEmpty())
        assertEquals(0, presenter.presented.size)

        // And when storage comes back, the core's redelivery lands normally.
        assertEquals(NotificationCoordinator.Decision.PRESENTED, arrive("e1", "space-a"))
        assertEquals(listOf("e1"), acked)
    }

    // ---------------------------------------------------------- compaction

    @Test
    fun compactionDropsOnlyWhatCanNeverComeBack() {
        fresh()
        arrive("e1", "space-a")     // sequence 1, presented
        arrive("e2", "space-a")     // sequence 2, presented
        co.onDismissed("space-a")   // both terminal
        arrive("e3", "space-a")     // sequence 3, live again

        // The core says: everything below sequence 3 is unreachable.
        val removed = co.compact(mapOf("space-a" to mapOf("device-b" to 3L)))

        assertEquals("the two dismissed rows behind the floor", 2, removed)
        assertEquals(
            "a live row must never be compacted, whatever the floor says",
            NotificationDb.STATE_PRESENTED, ledger.state("e3"),
        )
        // And the dedup memory that was dropped is genuinely unreachable: the
        // core cannot replay it, so nothing can bring it back to ask about.
        assertEquals(null, ledger.state("e1"))
    }

    @Test
    fun aTombstoneAtTheFloorSurvivesBecauseARollbackCanStillReplayIt() {
        fresh()
        arrive("e1", "space-a")     // sequence 1
        co.onDismissed("space-a")

        // The floor is the OLDER generation's line: sequence 1 is still
        // replayable if the current checkpoint is damaged, so its tombstone
        // must stay — otherwise the replay would look like news and the
        // dismissed notification would return.
        assertEquals(0, co.compact(mapOf("space-a" to mapOf("device-b" to 1L))))
        assertEquals(NotificationDb.STATE_DISMISSED, ledger.state("e1"))
    }

    @Test
    fun compactionNeverTouchesTheSpacesOpaqueKey() {
        fresh()
        arrive("e1", "space-a")
        val tag = presenter.presented[0].tag
        co.onDismissed("space-a")
        co.compact(mapOf("space-a" to mapOf("device-b" to 99L)))

        arrive("e2", "space-a")
        assertEquals(
            "re-minting the key would split one conversation's notifications in two",
            tag, presenter.presented[1].tag,
        )
    }

    @Test
    fun switchingNotificationsOffTakesTheLiveOnesDown() {
        fresh()
        arrive("e1", "space-a")
        co.setEnabled(false)
        assertEquals(listOf("space-a"), presenter.cleared)
    }

    // ------------------------------- what the ledger believes, against Android

    /**
     * A notification the ledger calls `presented` and Android does not hold —
     * a force-stop, a reboot, the system pruning a package with too many — is
     * treated as a swipe: the aggregation closes and the DEDUP MEMORY STAYS.
     *
     * The live gate is what found this. The shade held two conversations, the
     * ledger counted seven, and the summary above them said so.
     */
    @Test
    fun aNotificationAndroidNoLongerHoldsIsTreatedAsSwipedAway() {
        fresh()
        arrive("e1", "a")
        arrive("e2", "b")
        assertEquals(NotificationDb.STATE_PRESENTED, ledger.state("e1"))

        // Android kept b and lost a.
        co.reconcileWithSystem(setOf(ledger.tagFor("b")!!))

        assertEquals(
            "a conversation Android is not holding must stop being counted",
            NotificationDb.STATE_DISMISSED, ledger.state("e1"),
        )
        assertEquals(
            "and the one it IS holding must be left alone",
            NotificationDb.STATE_PRESENTED, ledger.state("e2"),
        )

        // The dedup memory survives: the same event must not come back.
        assertEquals(
            NotificationCoordinator.Decision.SUPPRESSED_ALREADY_PRESENTED,
            arrive("e1", "a"),
        )
    }

    /**
     * PENDING IS NOT MISSING. A row that has not been posted yet is absent
     * from the shade because that is correct, and dismissing it would throw
     * away the one thing recovery exists to put back.
     */
    @Test
    fun aPendingRowIsNotDismissedForBeingAbsentFromTheShade() {
        fresh()
        // Insert succeeds; the row stays pending because presenting threw.
        val breaking = object : NotificationCoordinator.Presenter {
            override fun present(p: NotificationCoordinator.Presentation) =
                throw IllegalStateException("the shade is unavailable")
            override fun clear(spaceId: String, tag: String) = Unit
            override fun retireConversation(spaceId: String, tag: String) = Unit
        }
        val c2 = NotificationCoordinator(breaking, ledger) { acked.add(it) }
        runCatching { c2.onCandidate(candidate("e1", "a")) }
        assertEquals(NotificationDb.STATE_PENDING, ledger.state("e1"))

        c2.reconcileWithSystem(emptySet())

        assertEquals(
            "reconciling against the system must not touch what was never posted",
            NotificationDb.STATE_PENDING, ledger.state("e1"),
        )
    }


    // ------------------------------------------ a space that is gone for good

    /**
     * SD-0 — "forget this space" reaches the shade at once.
     *
     * NOT AT THE NEXT RECONCILIATION, which is what the reverse pass does for
     * notifications ANDROID lost. This is the other direction: the person is
     * the one who decided, and a card still sitting there afterwards is the
     * two halves disagreeing in the only direction they can see.
     */
    @Test
    fun forgettingASpaceTakesItsNotificationDownNow() {
        fresh()
        arrive("e1", "a")
        arrive("e2", "b")
        assertEquals(NotificationDb.STATE_PRESENTED, ledger.state("e1"))

        co.forgetSpace("a")

        assertTrue("the notification must be cancelled", presenter.cleared.contains("a"))
        assertTrue("the conversation surface must be retired", presenter.retired.contains("a"))
        assertNull("nothing may be left saying this space existed", ledger.state("e1"))
        assertEquals(
            "and the space that was not deleted must be untouched",
            NotificationDb.STATE_PRESENTED, ledger.state("e2"),
        )
    }

    /**
     * IT RUNS WITH NOTIFICATIONS OFF TOO. `enabled` guards producing them;
     * taking one down is the opposite act. A person who had already turned
     * notifications off and then deleted a space would otherwise be left with
     * the one card nothing explains — posted before they switched off, and
     * now belonging to nothing.
     */
    @Test
    fun forgettingWorksEvenWhenNotificationsAreOff() {
        fresh()
        arrive("e1", "a")
        co.setEnabled(false)
        presenter.cleared.clear()

        co.forgetSpace("a")

        assertTrue("a disabled plane must still take its cards down",
            presenter.cleared.contains("a"))
        assertNull(ledger.state("e1"))
    }

}
