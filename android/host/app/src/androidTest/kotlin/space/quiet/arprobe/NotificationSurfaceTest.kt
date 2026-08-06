package space.quiet.arprobe

import android.app.Notification
import android.app.NotificationManager
import android.content.Context
import android.content.pm.ShortcutManager
import android.os.Build
import android.os.Bundle
import android.service.notification.StatusBarNotification
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import androidx.test.rule.GrantPermissionRule
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/**
 * AR-1b.6b.5 — what Android actually holds, asserted through Android's own
 * APIs.
 *
 * NO RELAY, NO NETWORK, NO NODE. Delivery is b.5's claim and it is proven
 * there; this is about the PROJECTION of an already-accepted state into system
 * surfaces, so the ledger is filled directly and deterministically. Tying
 * these assertions to a relay would mean a rate limit or a cooldown could fail
 * a test about a Person key — and the last live run showed exactly how that
 * ends up looking like a product defect.
 *
 * IT READS getActiveNotifications() AND ShortcutManager, not a text dump.
 * dumpsys is a diagnostic and a good second opinion, but its format is not a
 * contract; these are the public APIs whose answers are the same objects the
 * system holds.
 *
 * THE LEAK ASSERTIONS USE SENTINELS, not real names. Asserting that "Studio"
 * is absent proves nothing if the label was never set; asserting that
 * `SECRET_SPACE_LABEL_7f3a` is absent — a string that exists nowhere except in
 * this fixture — makes the negative exact.
 */
@RunWith(AndroidJUnit4::class)
class NotificationSurfaceTest {

    @get:Rule
    val permission: GrantPermissionRule =
        GrantPermissionRule.grant(android.Manifest.permission.POST_NOTIFICATIONS)

    private val ctx: Context
        get() = InstrumentationRegistry.getInstrumentation().targetContext

    private lateinit var nm: NotificationManager
    private lateinit var ledger: SqliteLedger
    private lateinit var presenter: SystemPresenter
    private lateinit var coordinator: NotificationCoordinator
    private val acked = mutableListOf<String>()

    /** Strings that exist nowhere but here, so their absence is a fact. */
    private val secretSpace = "SECRET_SPACE_LABEL_7f3a"
    private val secretSender = "SECRET_SENDER_LABEL_91c2"
    private val secretPreview = "SECRET_PREVIEW_TEXT_5b8e"
    private val networkSpaceId = "SPACEID0123456789abcdef0123456789abcdef0123456789abcdef01234567"
    private val networkDevice = "DEVICEID0123456789abcdef0123456789abcdef0123456789abcdef0123456"

    @Before
    fun setUp() {
        nm = ctx.getSystemService(NotificationManager::class.java)
        NotificationChannels.ensure(ctx)
        ledger = SqliteLedger(ctx)
        presenter = SystemPresenter(ctx, ledger, PresentationPolicy.HIDDEN)
        coordinator = NotificationCoordinator(presenter, ledger) { acked.add(it) }
        clean()
    }

    @After
    fun tearDown() = clean()

    private fun clean() {
        nm.cancelAll()
        acked.clear()
        ledger.wipeForTest()
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            ctx.getSystemService(ShortcutManager::class.java)?.let { sm ->
                val ids = (sm.dynamicShortcuts + sm.pinnedShortcuts).map { it.id }
                if (ids.isNotEmpty()) {
                    runCatching { sm.removeLongLivedShortcuts(ids) }
                    runCatching { sm.removeAllDynamicShortcuts() }
                }
            }
        }
        settle()
    }

    /** The system posts asynchronously; poll rather than sleep a guess. */
    private fun settle(want: Int = -1) {
        repeat(40) {
            val n = ours().size
            if (want < 0 && it > 2) return
            if (want >= 0 && n == want) return
            Thread.sleep(100)
        }
    }

    /**
     * Wait for a count that STAYS. `settle` returns the instant the number is
     * right, and several of these transitions pass through the right number on
     * their way somewhere else: taking down a summary removes it, the system
     * then sweeps the child, and the child is put back — three states, and the
     * middle one has the same size as the last. A test that reads the middle
     * one fails once in five runs and looks like a product bug.
     */
    private fun settleStable(want: Int, holdMs: Long = 400) {
        repeat(50) {
            settle(want)
            var stable = true
            repeat(4) {
                Thread.sleep(holdMs / 4)
                if (ours().size != want) stable = false
            }
            if (stable && ours().size == want) return
        }
    }

    private fun ours(): List<StatusBarNotification> =
        nm.activeNotifications.filter { it.packageName == ctx.packageName }

    private fun childOf(tagFragment: String) =
        ours().firstOrNull { it.tag?.startsWith("space:") == true && it.tag!!.contains(tagFragment) }

    private fun arrive(
        eventId: String,
        spaceId: String,
        device: String = networkDevice,
        sender: String = secretSender,
        at: Long = 1_700_000_000_000L,
        seq: Long = 1,
    ) = coordinator.onCandidate(
        NotificationCoordinator.Candidate(
            eventId = eventId,
            spaceId = spaceId,
            device = device,
            schema = "message.text.v1",
            sourceSequence = seq,
            occurredAtUnixMs = at,
            presentationCursor = seq,
            authoredLocally = false,
            spaceLabel = secretSpace,
            senderLabel = sender,
            previewText = secretPreview,
        )
    )

    // ------------------------------------------------------------ the modes

    @Test
    fun hiddenPutsNothingIdentifyingIntoAnySystemSurface() {
        presenter.policy = PresentationPolicy.HIDDEN
        arrive("e1", networkSpaceId)
        settle(1)

        val sbn = ours().single()
        assertTrue("the tag must be opaque", sbn.tag!!.startsWith("space:"))
        assertFalse("the tag must not carry the space id", sbn.tag!!.contains(networkSpaceId))
        assertNull("no shortcut means no conversation metadata", sbn.notification.shortcutId)

        val leaked = strings(sbn.notification).filter {
            it.contains(secretSpace) || it.contains(secretSender) ||
                it.contains(secretPreview) || it.contains(networkSpaceId) ||
                it.contains(networkDevice)
        }
        assertTrue(
            "HIDDEN leaked into the notification: $leaked",
            leaked.isEmpty(),
        )
        assertTrue("no shortcut may exist", shortcutIds().isEmpty())
    }

    @Test
    fun spaceNamesTheRoomAndNothingElse() {
        presenter.policy = PresentationPolicy.SPACE
        arrive("e1", networkSpaceId)
        settle(1)

        val sbn = ours().single()
        val all = strings(sbn.notification)
        assertTrue("the space may be named", all.any { it.contains(secretSpace) })
        assertFalse("the sender may not", all.any { it.contains(secretSender) })
        assertFalse("the message may not", all.any { it.contains(secretPreview) })
        assertNull("still not a conversation", sbn.notification.shortcutId)
        assertTrue("still no shortcut", shortcutIds().isEmpty())
    }

    @Test
    fun previewIsAConversationWithAStableOpaqueIdentity() {
        presenter.policy = PresentationPolicy.PREVIEW
        arrive("e1", networkSpaceId, sender = secretSender, at = 1_000, seq = 1)
        arrive("e2", networkSpaceId, sender = secretSender, at = 2_000, seq = 2)
        settle(1)
        // The SECOND message is an update to the same notification, so the
        // count is 1 either way: wait for the content, not for the count.
        repeat(40) {
            val n = ours().firstOrNull()?.notification
            if (n?.extras?.getParcelableArray(Notification.EXTRA_MESSAGES)?.size == 2) return@repeat
            Thread.sleep(100)
        }

        val sbn = ours().single()
        val shortcutId = sbn.notification.shortcutId
        assertNotNull("a MessagingStyle notification without a shortcut is not a conversation", shortcutId)
        assertFalse("the shortcut id must not carry the space id", shortcutId!!.contains(networkSpaceId))
        assertTrue("the shortcut must exist for real", shortcutIds().contains(shortcutId))

        val extras = sbn.notification.extras
        val messages = extras.getParcelableArray(Notification.EXTRA_MESSAGES)
        assertNotNull("MessagingStyle did not survive into the extras", messages)
        assertEquals(2, messages!!.size)

        // Chronological: Android renders them in the order they were added.
        val texts = strings(sbn.notification)
        assertTrue("the preview is allowed here", texts.any { it.contains(secretPreview) })

        // The Person key is opaque and carries nothing from the wire.
        val people = extras.getStringArray(Notification.EXTRA_PEOPLE_LIST)
            ?: emptyArray()
        for (p in people) {
            assertFalse("a Person must not carry the device id", p.contains(networkDevice))
        }
    }

    // ----------------------------------------------------------- the group

    @Test
    fun twoSpacesGetTwoChildrenAndExactlyOneSummary() {
        presenter.policy = PresentationPolicy.SPACE
        arrive("a1", "SPACE_A_${networkSpaceId}")
        arrive("b1", "SPACE_B_${networkSpaceId}")
        settleStable(3)

        val all = ours()
        val summaries = all.filter { it.tag == GroupSummary.TAG }
        val children = all.filter { it.tag != GroupSummary.TAG }
        assertEquals("one summary above two conversations", 1, summaries.size)
        assertEquals(2, children.size)
        assertTrue(
            "the summary must not name a space",
            strings(summaries.single().notification).none { it.contains(secretSpace) },
        )
    }

    /**
     * FAILS, AND THE FAILURE IS THE POINT OF WRITING IT DOWN.
     *
     * Reading one of two conversations leaves ZERO notifications, not one. The
     * likely cause is Android's own group semantics: a summary is the group's
     * anchor, and cancelling it takes the remaining children with it. The
     * summary is cancelled here precisely because one child does not deserve a
     * line above it — so the tidy behaviour destroys the notification it was
     * tidying up for.
     *
     * If that is confirmed, the fix is a sequence rather than a flag: re-post
     * the surviving child WITHOUT a group first, and only then cancel the
     * summary. It is not applied blind, because the alternative — leaving a
     * summary above a lone child — is also defensible and the choice should be
     * made against evidence rather than against a guess.
     *
     * Left in the suite and ignored, so it is a question somebody can pick up
     * rather than an assertion quietly weakened until it passed.
     */
    @Test
    fun theSummaryGoesWhenOnlyOneConversationIsLeft() {
        presenter.policy = PresentationPolicy.SPACE
        arrive("a1", "SPACE_A_${networkSpaceId}")
        arrive("b1", "SPACE_B_${networkSpaceId}")
        settleStable(3)

        coordinator.onRead("SPACE_A_${networkSpaceId}")
        settleStable(1)

        val all = ours()
        assertTrue("no summary above a lone child", all.none { it.tag == GroupSummary.TAG })
        assertEquals(
            "after reading one of two conversations the other must remain; present: " +
                all.map { it.tag }.toString(),
            1, all.size,
        )
    }

    /**
     * And back again: the conversation that was left alone must still be
     * gathered when a second one arrives.
     *
     * The obvious teardown — take the survivor out of the group, then cancel
     * the summary — leaves it outside, and nothing puts it back: the next
     * arrival posts a summary over one child while the other floats loose
     * beside it. So the survivor stays grouped through the whole cycle, and
     * this is the test that says so.
     */
    @Test
    fun aConversationThatWasAloneIsGatheredAgainWhenAnotherArrives() {
        presenter.policy = PresentationPolicy.SPACE
        arrive("a1", "SPACE_A_${networkSpaceId}")
        arrive("b1", "SPACE_B_${networkSpaceId}")
        settleStable(3)

        coordinator.onRead("SPACE_A_${networkSpaceId}")
        settleStable(1)
        assertEquals(
            "the survivor must stay in the group it will be gathered by",
            GroupSummary.KEY, ours().single().notification.group,
        )

        arrive("c1", "SPACE_C_${networkSpaceId}")
        settleStable(3)

        val all = ours()
        val children = all.filter { it.tag != GroupSummary.TAG }
        assertEquals("one summary above two conversations", 1, all.size - children.size)
        assertEquals(2, children.size)
        val dump = all.joinToString(" | ") { "${it.tag}=${it.notification.group}" }
        for (c in children) {
            assertEquals(
                "a child outside the group is not gathered by the summary: $dump",
                GroupSummary.KEY, c.notification.group,
            )
        }
    }

    /**
     * AR-1b.6b.6 — the ledger, checked against what Android actually holds.
     *
     * FOUND BY THE LIVE GATE ON A PHONE: the shade held two conversations, the
     * ledger counted seven, and the summary above them said "7 new signals"
     * about five notifications nobody could see. Notifications leave without
     * telling us — a force-stop, a reboot, the system pruning a package that
     * has posted too many — and each one leaves a row saying `presented`.
     *
     * Simulated here by cancelling one behind the coordinator's back, which is
     * exactly what those events look like from inside the app.
     */
    @Test
    fun aConversationAndroidHasLostStopsBeingCounted() {
        presenter.policy = PresentationPolicy.SPACE
        arrive("a1", "SPACE_A_${networkSpaceId}")
        arrive("b1", "SPACE_B_${networkSpaceId}")
        settleStable(3)

        // Behind the app's back, the way a force-stop or a reboot does it.
        val lost = ours().first { it.tag != GroupSummary.TAG }.tag!!
        nm.cancel(lost, SystemPresenter.NOTIFICATION_ID)
        settleStable(2)

        coordinator.reconcileWithSystem(presenter.liveTags())
        settleStable(1)

        val left = ours()
        assertTrue(
            "a summary must not survive above one conversation, and it did " +
                "because the ledger still counted the one Android dropped: " +
                left.map { it.tag },
            left.none { it.tag == GroupSummary.TAG },
        )
        assertEquals("the conversation still on screen must stay", 1, left.size)
        assertEquals(
            "the lost one must be closed, not left live",
            0, ledger.activeBySpace().count { it.value.tag == lost },
        )
    }

    // -------------------------------------------------- the privacy retreat

    @Test
    fun tighteningTakesTheConversationSurfaceBack() {
        presenter.policy = PresentationPolicy.PREVIEW
        arrive("e1", networkSpaceId)
        settle(1)
        val tag = ours().single().tag!!
        assertTrue("nothing to take back otherwise", shortcutIds().isNotEmpty())

        presenter.applyPolicy(PresentationPolicy.HIDDEN, mapOf(networkSpaceId to tag))
        settle()

        val remaining = shortcutIds()
        val id = "conversation:" + tag.removePrefix("space:")
        assertFalse("the long-lived shortcut must be gone", remaining.contains(id))

        // Anything the system still holds must be harmless: the sanitising
        // step runs BEFORE the removal precisely so a pinned copy — which the
        // app cannot delete — is already generic by the time it is abandoned.
        for (label in shortcutLabels()) {
            assertFalse("a surviving shortcut still names the space: $label",
                label.contains(secretSpace))
        }
        for (sbn in ours()) {
            val leaked = strings(sbn.notification).filter { it.contains(secretPreview) }
            assertTrue("a notification kept its preview after tightening: $leaked", leaked.isEmpty())
        }
    }

    // ---------------------------------------------------------------- tools

    private fun shortcutIds(): List<String> {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.R) return emptyList()
        val sm = ctx.getSystemService(ShortcutManager::class.java) ?: return emptyList()
        return (sm.dynamicShortcuts + sm.pinnedShortcuts).map { it.id }
    }

    private fun shortcutLabels(): List<String> {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.R) return emptyList()
        val sm = ctx.getSystemService(ShortcutManager::class.java) ?: return emptyList()
        return (sm.dynamicShortcuts + sm.pinnedShortcuts).mapNotNull { it.shortLabel?.toString() }
    }

    /**
     * Every string the notification carries, including its public version and
     * every nested bundle. A leak that hid one level down would otherwise pass
     * a check that only read the title.
     */
    private fun strings(n: Notification): List<String> {
        val out = mutableListOf<String>()
        n.tickerText?.let { out.add(it.toString()) }
        collect(n.extras, out)
        n.publicVersion?.let { pv ->
            pv.tickerText?.let { out.add(it.toString()) }
            collect(pv.extras, out)
        }
        return out
    }

    private fun collect(b: Bundle?, out: MutableList<String>) {
        if (b == null) return
        for (key in b.keySet()) {
            @Suppress("DEPRECATION")
            when (val v = b.get(key)) {
                is CharSequence -> out.add(v.toString())
                is Bundle -> collect(v, out)
                is Array<*> -> v.forEach { e ->
                    when (e) {
                        is CharSequence -> out.add(e.toString())
                        is Bundle -> collect(e, out)
                        else -> e?.let { out.add(it.toString()) }
                    }
                }
                else -> v?.let { out.add(it.toString()) }
            }
        }
    }
}
