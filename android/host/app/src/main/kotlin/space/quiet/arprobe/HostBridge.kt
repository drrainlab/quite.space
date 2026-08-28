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
    private val stayConnected: (Boolean) -> Unit,
    private val unlockRemembered: () -> Boolean,
    private val forgetPassphrase: () -> Unit,
    private val stayRefused: () -> Boolean,
    private val revealPassphrase: () -> Boolean = { false },
    private val changeCode: () -> Boolean = { false },
    // SR-0 — Radio Mode + push-to-talk. All still the page's own facts:
    // a local preference, and the person's finger on a button. The
    // transcript never crosses this bridge — it is pushed to the page by
    // the host (window.quietPtt), and the page cannot ask for it.
    private val radioMode: (space: String, enabled: Boolean, autoplay: Boolean) -> Boolean =
        { _, _, _ -> false },
    private val radioBackground: (Boolean) -> Boolean = { false },
    private val pttPress: (space: String) -> Boolean = { false },
    private val pttRelease: () -> Boolean = { false },
    private val pttCancel: () -> Boolean = { false },
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

    /**
     * AR-1c — the person switched the "stay connected" mode on or off.
     *
     * IT REACHES A FOREGROUND SERVICE, which is why it may only be called
     * from the page inside a VISIBLE Activity: Android 12+ forbids starting
     * one from the background, and the restriction is the right shape — a
     * mode that spends somebody's battery has no business starting itself.
     *
     * This is the one bridge verb with a cost attached, and it is still the
     * person's own fact: they pressed a switch that is about their device.
     */
    @JavascriptInterface
    fun setStayConnected(pass: String?, on: Boolean): Boolean {
        if (!admitted(pass)) return refuse("setStayConnected")
        stayConnected(on)
        return true
    }

    /**
     * Whether the platform refused to let "stay connected" run.
     *
     * A boolean about this device, in the same family as [unlockRemembered]
     * — see there for why answering one is not the getter the bridge refuses
     * to have. It exists so a switch sitting at Off can say who put it there.
     */
    @JavascriptInterface
    fun stayRefused(pass: String?): Boolean {
        if (!admitted(pass)) return refuse("stayRefused")
        return stayRefused()
    }

    /**
     * Whether this device opens without being asked for the passphrase.
     *
     * A BOOLEAN, WHICH IS THE ONLY REASON IT MAY BE ANSWERED AT ALL. The rule
     * above — nothing is returned — is about facts a stolen token could carry
     * away, and "there is a saved passphrase" is not one: anybody who can call
     * this is already looking at a node that opened, so they can see that it
     * did. The passphrase itself has no getter anywhere and never will.
     */
    @JavascriptInterface
    fun unlockRemembered(pass: String?): Boolean {
        if (!admitted(pass)) return refuse("unlockRemembered")
        return unlockRemembered()
    }

    /**
     * Stop remembering it: the next launch asks again.
     *
     * The person's own fact about their own device, in the same family as the
     * notification policy — and the reason storing one is defensible at all.
     * A convenience that cannot be switched off is not a convenience.
     */
    @JavascriptInterface
    fun forgetPassphrase(pass: String?): Boolean {
        if (!admitted(pass)) return refuse("forgetPassphrase")
        forgetPassphrase()
        return true
    }

    /**
     * Show the recovery passphrase — ON THE PHONE'S SCREEN, in a native
     * dialog. The rule above holds exactly because of calls like this one:
     * nothing is returned but "the dialog is up". The page that asked never
     * sees a word of it, so a stolen token buys a dialog in front of the
     * person holding the phone — which is who it is for.
     *
     * False when this instance no longer holds the opened passphrase (the
     * Activity was recreated under a live core); the page says "lock and
     * unlock first" instead of pretending.
     */
    @JavascriptInterface
    fun revealPassphrase(pass: String?): Boolean {
        if (!admitted(pass)) return refuse("revealPassphrase")
        return revealPassphrase()
    }

    /**
     * Re-run the native choose-a-code flow. Until now the one offer per
     * install was the only door — the settings page even pointed at a control
     * that did not exist. Same shape as [revealPassphrase]: a Boolean and a
     * native screen, nothing into the page.
     */
    @JavascriptInterface
    fun changeCode(pass: String?): Boolean {
        if (!admitted(pass)) return refuse("changeCode")
        return changeCode()
    }

    /**
     * SR-0 — the person flipped Radio Mode for one space, on this device
     * only (groom D3: never shared state, never in the log). The host
     * answers by pushing the authoritative state back into the page.
     */
    @JavascriptInterface
    fun setRadioMode(pass: String?, spaceId: String?, enabled: Boolean, autoplay: Boolean): Boolean {
        if (!admitted(pass)) return refuse("setRadioMode")
        if (spaceId.isNullOrEmpty()) return false
        return radioMode(spaceId, enabled, autoplay)
    }

    /** The device-wide "listen in background" switch. */
    @JavascriptInterface
    fun setRadioBackground(pass: String?, on: Boolean): Boolean {
        if (!admitted(pass)) return refuse("setRadioBackground")
        return radioBackground(on)
    }

    /**
     * SR-0 — push-to-talk. The finger landed; false means "not now" (no
     * mic permission yet — the host raises the native request — or the
     * engine is not ready). The page renders only what the host pushes.
     */
    @JavascriptInterface
    fun pttPress(pass: String?, spaceId: String?): Boolean {
        if (!admitted(pass)) return refuse("pttPress")
        if (spaceId.isNullOrEmpty()) return false
        return pttPress(spaceId)
    }

    @JavascriptInterface
    fun pttRelease(pass: String?): Boolean {
        if (!admitted(pass)) return refuse("pttRelease")
        return pttRelease()
    }

    @JavascriptInterface
    fun pttCancel(pass: String?): Boolean {
        if (!admitted(pass)) return refuse("pttCancel")
        return pttCancel()
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
