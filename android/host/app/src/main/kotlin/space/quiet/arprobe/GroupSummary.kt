package space.quiet.arprobe

/**
 * AR-1b.6b.3 — what the one line above several conversations is allowed to
 * say.
 *
 * A SUMMARY IS NOT A DIGEST. The tempting shape is to join what the children
 * hold — three space names, the last line from each — and it is a second
 * privacy surface built by accident: the summary is visible where the children
 * are collapsed, on a lock screen, and to every notification listener. So it
 * carries COUNTS and nothing else. The details are already in the children,
 * where the policy has already decided what may be in them.
 *
 * ```
 *   HIDDEN    Quiet · 5 new signals
 *   SPACE     3 spaces · 5 new messages
 *   PREVIEW   3 spaces · 5 new messages
 * ```
 *
 * PREVIEW says the same as SPACE deliberately. It is allowed to say more, and
 * there is nothing worth saying: a person expanding the group sees the
 * conversations themselves, and a summary that repeated one of them would put
 * a name and a line where the collapsed view shows them by default.
 *
 * IT EXISTS ONLY FOR TWO OR MORE. One conversation with a summary above it is
 * a person being told the same thing twice, and Android already renders a
 * lone child on its own. So the summary appears at two and goes away again at
 * one — which is also why it is recomputed after every change rather than
 * posted once and forgotten.
 */
object GroupSummary {

    /** One group for every live conversation this app has. */
    const val KEY = "quite.active_conversations"

    /**
     * The summary's own tag, constant like the children's id. It cannot
     * collide with a space's tag: those are "space:<opaque key>".
     */
    const val TAG = "quite:summary"

    /** Below two, Android renders the child alone and a summary is noise. */
    fun needed(activeSpaces: Int): Boolean = activeSpaces >= 2

    fun title(policy: PresentationPolicy): String = "Quiet"

    fun text(policy: PresentationPolicy, activeSpaces: Int, messages: Int): String {
        val m = if (messages == 1) "1 new message" else "$messages new messages"
        if (!policy.mayNameSpace) {
            // HIDDEN does not say how the messages are distributed either:
            // "three spaces" is itself a fact about somebody's life.
            return if (messages == 1) "A new signal" else "$messages new signals"
        }
        val s = if (activeSpaces == 1) "1 space" else "$activeSpaces spaces"
        return "$s · $m"
    }
}
