package space.quiet.arprobe

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * AR-1b.6a — what a conversation notification would say, tested where it is
 * still ordinary Kotlin.
 *
 * The renderer that consumes this is a handful of platform calls; everything
 * that can be WRONG is here — which messages, in what order, attributed to
 * whom, and what a privacy mode is allowed to let out of the process.
 */
class ConversationProjectionTest {

    private var minted = 0
    private val keys = mutableMapOf<String, String>()
    private fun personKey(device: String) = keys.getOrPut(device) { "p${minted++}" }

    private fun item(
        id: String,
        device: String = "device-b",
        sender: String = "bob",
        text: String = "a line",
        at: Long = 1_700_000_000_000L,
    ) = NotificationCoordinator.Item(id, device, "message.text.v1", at, sender, text)

    // ------------------------------------------------------------ the modes

    @Test
    fun hiddenLetsNothingOutOfTheProcess() {
        val r = ConversationProjection.of(
            PresentationPolicy.HIDDEN, "Studio", listOf(item("e1")), ::personKey,
        )
        assertFalse("HIDDEN must not reach the conversation surface", r.useConversationSurface)
        assertFalse("a shortcut carries the name into the launcher", r.publishShortcut)
        assertNull("the space must not be named", r.conversationTitle)
        assertTrue("no message may be projected at all", r.lines.isEmpty())
        assertEquals("A new message", r.genericText)
    }

    @Test
    fun spaceNamesTheRoomAndNobodyInIt() {
        val r = ConversationProjection.of(
            PresentationPolicy.SPACE, "Studio", listOf(item("e1")), ::personKey,
        )
        assertEquals("Studio", r.conversationTitle)
        assertFalse(
            "the conversation APIs are built around PEOPLE, and an anonymous " +
                "Person invented to reach them would be working around Android's " +
                "model rather than using it",
            r.useConversationSurface,
        )
        assertFalse(r.publishShortcut)
        assertTrue(r.lines.isEmpty())
    }

    @Test
    fun previewIsTheOnlyModeThatMayNameAPerson() {
        val r = ConversationProjection.of(
            PresentationPolicy.PREVIEW, "Studio", listOf(item("e1")), ::personKey,
        )
        assertTrue(r.useConversationSurface)
        assertTrue(r.publishShortcut)
        assertEquals("Studio", r.conversationTitle)
        assertEquals(1, r.lines.size)
        assertEquals("bob", r.lines[0].senderLabel)
        assertEquals("a line", r.lines[0].text)
    }

    // ------------------------------------------------------- the projection

    @Test
    fun messagesAreChronologicalOldestFirst() {
        // Android renders them in the order they are added, so a reversed list
        // reads as a conversation running backwards.
        val items = listOf(
            item("e3", text = "third", at = 3_000),
            item("e1", text = "first", at = 1_000),
            item("e2", text = "second", at = 2_000),
        )
        val r = ConversationProjection.of(PresentationPolicy.PREVIEW, "Studio", items, ::personKey)
        assertEquals(listOf("first", "second", "third"), r.lines.map { it.text })
    }

    @Test
    fun onlyTheLastFewReachTheSystemObjectAndTheCountStaysHonest() {
        val items = (1..40).map { item("e$it", text = "line $it", at = it.toLong()) }
        val r = ConversationProjection.of(PresentationPolicy.PREVIEW, "Studio", items, ::personKey)

        assertEquals(ConversationProjection.MESSAGES_SHOWN, r.lines.size)
        assertEquals("line 40", r.lines.last().text)
        // The generic text still counts everything the ledger holds: the
        // display limit is presentation policy, never a claim about how much
        // arrived.
        assertEquals("40 new messages", r.genericText)
    }

    @Test
    fun aSpaceWithNoNameIsNotGivenOne() {
        val r = ConversationProjection.of(
            PresentationPolicy.PREVIEW, "", listOf(item("e1")), ::personKey,
        )
        assertNull(
            "a manifest that has not arrived is ordinary; inventing a title " +
                "would put a guess in the launcher",
            r.conversationTitle,
        )
    }

    // ----------------------------------------------------------- identities

    @Test
    fun oneSenderKeepsOnePersonKeyEvenAfterARename() {
        val items = listOf(
            item("e1", device = "device-b", sender = "bob"),
            item("e2", device = "device-b", sender = "Robert"),
        )
        val r = ConversationProjection.of(PresentationPolicy.PREVIEW, "Studio", items, ::personKey)
        assertEquals(
            "a renamed person must stay the same conversation, not become a second one",
            r.lines[0].personKey, r.lines[1].personKey,
        )
        assertEquals("Robert", r.lines[1].senderLabel)
    }

    @Test
    fun twoSendersSharingANameKeepTwoIdentities() {
        val items = listOf(
            item("e1", device = "device-b", sender = "Sam"),
            item("e2", device = "device-c", sender = "Sam"),
        )
        val r = ConversationProjection.of(PresentationPolicy.PREVIEW, "Studio", items, ::personKey)
        assertFalse(
            "a name is not an identity: two people share one often enough, and " +
                "merging them would put somebody's messages under another's face",
            r.lines[0].personKey == r.lines[1].personKey,
        )
    }

    // ------------------------------------------------------- privacy moves

    @Test
    fun tighteningIsTheDirectionThatMustTakeSurfacesBack() {
        // A shortcut published under PREVIEW carries the space's name and a
        // Person into the launcher, and those do not expire on their own.
        assertTrue(PresentationPolicy.HIDDEN.tightensFrom(PresentationPolicy.PREVIEW))
        assertTrue(PresentationPolicy.SPACE.tightensFrom(PresentationPolicy.PREVIEW))
        assertTrue(PresentationPolicy.HIDDEN.tightensFrom(PresentationPolicy.SPACE))
    }

    @Test
    fun looseningPublishesNothingByItself() {
        // Publication belongs to the next notification and to the policy —
        // never to a settings screen deciding to be helpful.
        assertFalse(PresentationPolicy.PREVIEW.tightensFrom(PresentationPolicy.HIDDEN))
        assertFalse(PresentationPolicy.SPACE.tightensFrom(PresentationPolicy.HIDDEN))
        assertFalse(PresentationPolicy.PREVIEW.tightensFrom(PresentationPolicy.PREVIEW))
    }

    @Test
    fun anUnknownStoredModeFallsBackToTheStrictestOne() {
        assertEquals(PresentationPolicy.HIDDEN, PresentationPolicy.parse(null))
        assertEquals(PresentationPolicy.HIDDEN, PresentationPolicy.parse("whatever"))
        assertEquals(PresentationPolicy.PREVIEW, PresentationPolicy.parse("preview"))
    }

    @Test
    fun aSenderWithNoNameStillHasAnIdentity() {
        val r = ConversationProjection.of(
            PresentationPolicy.PREVIEW, "Studio",
            listOf(item("e1", sender = "")), ::personKey,
        )
        assertTrue(r.lines[0].personKey.isNotEmpty())
        assertEquals("", r.lines[0].senderLabel)
    }

    /**
     * A PERSON IS NAMED ONCE AND EVERY MESSAGE OF THEIRS SHOWS UNDER IT, so
     * the name has to be the one they use now.
     *
     * Found on a phone: somebody renamed themselves, the device applied the
     * rename — its member list said the new name, and so did the notification
     * title — while the messages underneath were still attributed to the old
     * one, because the Person was built from whichever line came first.
     */
    @Test
    fun aPersonIsCalledByTheirLatestName() {
        val r = ConversationProjection.of(
            PresentationPolicy.PREVIEW, "Room",
            listOf(
                item("e1", device = "dev-a", sender = "Old Name", text = "before"),
                item("e2", device = "dev-a", sender = "New Name", text = "after"),
            ),
        ) { "person:$it" }

        assertEquals("New Name", r.namesByPerson["person:dev-a"])
    }

    /** An empty label is not a rename: it is a candidate that carried none. */
    @Test
    fun anEmptyLabelDoesNotEraseAKnownName() {
        val r = ConversationProjection.of(
            PresentationPolicy.PREVIEW, "Room",
            listOf(
                item("e1", device = "dev-a", sender = "Known", text = "one"),
                item("e2", device = "dev-a", sender = "", text = "two"),
            ),
        ) { "person:$it" }

        assertEquals("Known", r.namesByPerson["person:dev-a"])
    }
}
