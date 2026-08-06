package space.quiet.arprobe

import android.util.Log
import android.webkit.JavascriptInterface

/**
 * AR-1b.8 — the one channel from the interface back to the host.
 *
 * EVERYTHING SO FAR RUNS ONE WAY: the core produces a candidate, the host
 * decides, Android shows it. Three facts only the interface knows have had
 * nowhere to go, and their absence is not theoretical — each is a specific way
 * the app behaves worse than a person expects:
 *
 *   which conversation is ON SCREEN — without it, Quiet posts a notification
 *       about a message somebody is reading at that moment
 *   that a conversation was actually SHOWN — without it, a notification stays
 *       in the shade after the person has read every word of it
 *   the privacy policy they chose — without it, the strict default is the only
 *       setting there is, and nobody can change their own mind
 *
 * A BRIDGE IS A HOLE, AND THIS ONE IS SHAPED TO BE A SMALL ONE.
 * `addJavascriptInterface` exposes this object to every page the WebView ends
 * up holding — not only the one that was loaded, and not only the main frame.
 * So there are three layers, and none of them is "the WebView only loads our
 * origin":
 *
 *   THE SESSION TOKEN IS THE PASS. Every call carries the node's own session
 *       token, which the page has because the node put it in the URL it
 *       served. A page from anywhere else does not have it and cannot obtain
 *       it by asking this object anything.
 *   NOTHING IS RETURNED. Not the token, not the policy, not a space id —
 *       every method answers with a boolean "accepted" at most. A getter here
 *       would be the exfiltration route the token check exists to prevent.
 *   THE VERBS ARE ALL THE PAGE'S OWN FACTS. Nothing here reaches the core,
 *       opens a screen, or touches a permission. The worst a stolen token
 *       could do is silence notifications for a conversation and mark it read
 *       — which is what a person sitting in front of the app can do anyway.
 *
 * Calls arrive on the WebView's own Java-bridge thread, never the main one.
 * The coordinator is synchronized and the policy write is a single
 * SharedPreferences commit, so nothing here needs a hop or a lock of its own.
 */
internal class HostBridge(
    private val token: () -> String,
    private val coordinator: NotificationCoordinator,
    private val policy: (PresentationPolicy) -> Unit,
) {

    /** True when the caller proved it is the page our own node served. */
    private fun admitted(pass: String?): Boolean {
        val expected = token()
        // An empty expectation admits nobody. Before the node is open there is
        // no session, and "no token yet" must never mean "any token will do".
        //
        // The first clause is REDUNDANT with the length check below — an empty
        // expectation can only match an empty pass, which the second clause
        // already refuses — and it is kept anyway, stated rather than trusted.
        // It is also the reason no test can drive it alone: there is no input
        // it is the only thing standing in front of.
        if (expected.isEmpty() || pass.isNullOrEmpty()) return false
        if (pass.length != expected.length) return false
        // Constant time over the length, out of habit rather than necessity:
        // the value is local, but a comparison that returns early is a
        // comparison somebody can learn from.
        var diff = 0
        for (i in expected.indices) diff = diff or (pass[i].code xor expected[i].code)
        return diff == 0
    }

    /**
     * The conversation currently on screen, or null when none is.
     *
     * NOT A READ. Being on screen suppresses the notification for what arrives
     * WHILE it is; it does not mark anything as seen, and it does not take
     * down a notification posted before the person got there. Those are
     * [read]'s job, and conflating them is how a notification disappears for a
     * message nobody looked at.
     */
    @JavascriptInterface
    fun visibleSpace(pass: String?, spaceId: String?): Boolean {
        if (!admitted(pass)) return refuse("visibleSpace")
        coordinator.onVisibleSpace(spaceId?.ifEmpty { null })
        return true
    }

    /**
     * The interface actually rendered this conversation. Now the notification
     * comes down.
     *
     * SAID BY WHATEVER DREW THE MESSAGES, not by the tap that opened the app:
     * a tap opens a screen that may never finish loading, and clearing there
     * would lose the notification for a message nobody saw.
     */
    @JavascriptInterface
    fun read(pass: String?, spaceId: String?): Boolean {
        if (!admitted(pass)) return refuse("read")
        if (spaceId.isNullOrEmpty()) return false
        coordinator.onRead(spaceId)
        return true
    }

    /**
     * The person's answer to what Android may know about their conversations.
     *
     * An unknown name is REFUSED rather than coerced to a default: a typo that
     * silently became PREVIEW would publish names and messages to every
     * notification listener on the phone, and nobody would have chosen it.
     */
    @JavascriptInterface
    fun setNotificationPolicy(pass: String?, name: String?): Boolean {
        if (!admitted(pass)) return refuse("setNotificationPolicy")
        val next = when (name) {
            "hidden" -> PresentationPolicy.HIDDEN
            "space" -> PresentationPolicy.SPACE
            "preview" -> PresentationPolicy.PREVIEW
            else -> return false
        }
        policy(next)
        return true
    }

    private fun refuse(what: String): Boolean {
        // Logged without the pass. A refused call is either a bug in our own
        // page or something that has no business here; both are worth seeing,
        // neither is worth printing a credential for.
        Log.w(TAG, "refused $what: the caller did not present the session token")
        return false
    }

    private companion object {
        const val TAG = "quiet-bridge"
    }
}
