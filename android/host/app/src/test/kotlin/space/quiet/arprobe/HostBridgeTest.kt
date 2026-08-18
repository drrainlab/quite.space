package space.quiet.arprobe

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * AR-1b.8 — the bridge is a hole, and these are the tests that keep it small.
 *
 * `addJavascriptInterface` exposes an object to every page the WebView ends up
 * holding — not only the one that was loaded, and not only the main frame. The
 * origin lock in the WebViewClient covers main-frame navigation and nothing
 * else, so the admission test is the session token: the page has it because
 * the node put it in the URL it served, and nothing else does.
 */
class HostBridgeTest {

    private class Ledger : NotificationCoordinator.Ledger {
        override fun insertPending(c: NotificationCoordinator.Candidate) = null
        override fun markPresented(eventId: String) = Unit
        override fun markSuppressed(eventId: String, reason: String) = Unit
        val dismissed = mutableListOf<String>()
        override fun dismiss(spaceId: String) { dismissed.add(spaceId) }
        val readSpaces = mutableListOf<String>()
        override fun read(spaceId: String) { readSpaces.add(spaceId) }
        override fun pendingBySpace() = emptyMap<String, NotificationCoordinator.Recovery>()
        override fun activeBySpace() = emptyMap<String, NotificationCoordinator.Recovery>()
        override fun presentedBySpace() = emptyMap<String, NotificationCoordinator.Recovery>()
        override fun forgetSpace(spaceId: String) = Unit
        override fun tagFor(spaceId: String): String? = "space:tag"
        override fun personKey(device: String, label: String) = "person"
        override fun compact(retainFrom: Map<String, Map<String, Long>>) = 0
    }

    private class Presenter : NotificationCoordinator.Presenter {
        override fun present(p: NotificationCoordinator.Presentation) = Unit
        val cleared = mutableListOf<String>()
        override fun clear(spaceId: String, tag: String) { cleared.add(spaceId) }
        override fun retireConversation(spaceId: String, tag: String) = Unit
    }

    private val ledger = Ledger()
    private val presenter = Presenter()
    private val coordinator = NotificationCoordinator(presenter, ledger) { }
    private var policy: PresentationPolicy? = null
    private var token = "the-session-token"

    private var staying: Boolean? = null
    private var remembered = true
    private var forgot = false
    private val bridge = HostBridge(
        { token }, coordinator, { policy = it }, { staying = it },
        unlockRemembered = { remembered },
        forgetPassphrase = { forgot = true },
        stayRefused = { false },
    )

    @Test
    fun aCallerWithoutTheTokenIsRefusedAndChangesNothing() {
        assertFalse(bridge.read(null, "space-a"))
        assertFalse(bridge.read("", "space-a"))
        assertFalse(bridge.visibleSpace("guess", "space-a"))
        assertFalse(bridge.setNotificationPolicy("guess", "preview"))
        assertFalse(bridge.setStayConnected("guess", true))

        assertTrue("nothing may have been read", ledger.readSpaces.isEmpty())
        assertNull("and no policy may have been set", policy)
        assertNull("and no mode may have been switched on", staying)
    }

    /**
     * A wrong token of the RIGHT LENGTH, because that is the one a length
     * check alone would let through.
     */
    @Test
    fun aTokenOfTheRightShapeIsStillTheWrongToken() {
        val same = "the-session-tokeX"
        assertEquals("the fixture must compare like with like", token.length, same.length)
        assertFalse(bridge.read(same, "space-a"))
        assertTrue(ledger.readSpaces.isEmpty())
    }

    /**
     * BEFORE THERE IS A SESSION, NOBODY IS ADMITTED. The tempting bug is an
     * empty expectation matching an empty pass: the bridge is registered on
     * the WebView before the node is open, so "no token yet" is a state that
     * really happens, and it must not mean "any token will do".
     */
    @Test
    fun noSessionAdmitsNobodyIncludingAnEmptyPass() {
        token = ""
        assertFalse("an empty pass is not a pass", bridge.read("", "space-a"))
        assertFalse(bridge.read(null, "space-a"))
        // The one that matters: a caller that presents SOMETHING while there
        // is nothing to present it to.
        assertFalse("a guess admitted for want of a session", bridge.read("anything", "space-a"))
        assertFalse(bridge.visibleSpace("anything", "space-a"))
        assertFalse(bridge.setNotificationPolicy("anything", "preview"))
        assertTrue(ledger.readSpaces.isEmpty())
        assertNull(policy)
    }

    @Test
    fun anAdmittedCallerReportsWhatIsOnScreenAndWhatWasRead() {
        assertTrue(bridge.visibleSpace(token, "space-a"))
        assertTrue(bridge.read(token, "space-a"))
        assertEquals(listOf("space-a"), ledger.readSpaces)
        assertTrue("reading takes the notification down", presenter.cleared.contains("space-a"))
    }

    /** An empty space id means "nothing is on screen", not a space called "". */
    @Test
    fun anEmptyVisibleSpaceMeansNothingIsOnScreen() {
        assertTrue(bridge.visibleSpace(token, "space-a"))
        assertTrue(bridge.visibleSpace(token, ""))
        // Nothing observable from here, so the read path proves it instead:
        // reading still requires a real id.
        assertFalse("a read with no space is not a read", bridge.read(token, ""))
        assertTrue(ledger.readSpaces.isEmpty())
    }

    /**
     * AN UNKNOWN NAME IS REFUSED, NOT COERCED. A typo that silently became
     * PREVIEW would publish space names, senders and message text to every
     * notification listener on the phone — a setting nobody chose, arrived at
     * by being helpful about a misspelling.
     */
    @Test
    fun anUnknownPolicyNameIsRefusedRatherThanGuessed() {
        assertFalse(bridge.setNotificationPolicy(token, "Preview"))
        assertFalse(bridge.setNotificationPolicy(token, "full"))
        assertFalse(bridge.setNotificationPolicy(token, ""))
        assertFalse(bridge.setNotificationPolicy(token, null))
        assertNull(policy)

        assertTrue(bridge.setNotificationPolicy(token, "preview"))
        assertEquals(PresentationPolicy.PREVIEW, policy)
        assertTrue(bridge.setNotificationPolicy(token, "hidden"))
        assertEquals(PresentationPolicy.HIDDEN, policy)
        assertTrue(bridge.setNotificationPolicy(token, "space"))
        assertEquals(PresentationPolicy.SPACE, policy)
    }

    /**
     * NOTHING COMES BACK OUT. Every method answers with a boolean at most: a
     * getter here — the current policy, the space on screen, the token itself
     * — would be the exfiltration route the admission test exists to prevent.
     */
    @Test
    fun theBridgeHandsNothingBackToThePage() {
        val readable = HostBridge::class.java.declaredMethods
            .filter { it.isAnnotationPresent(android.webkit.JavascriptInterface::class.java) }
        assertTrue("the bridge must expose something", readable.isNotEmpty())
        for (m in readable) {
            assertEquals(
                "${m.name} returns ${m.returnType}: only a boolean may cross back",
                java.lang.Boolean.TYPE, m.returnType,
            )
        }
    }
}
