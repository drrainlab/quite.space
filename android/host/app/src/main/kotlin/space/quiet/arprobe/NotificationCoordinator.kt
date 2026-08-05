package space.quiet.arprobe

import java.util.ArrayDeque

/**
 * AR-1b.2 — the one place that decides whether a verified event becomes a
 * notification.
 *
 * NO ANDROID TYPES APPEAR IN THIS FILE, and that is the design rather than a
 * preference. Every hazard this class defends against — historical replay,
 * reconnect, process restart, an event arriving for the space already on
 * screen — is a DECISION, and a decision that can only be exercised on a
 * device is a decision nobody tests. Channels, `MessagingStyle`, shortcuts and
 * `PendingIntent`s live behind [Presenter] and land in AR-1b.3–b.6; storage
 * lives behind [Store]. What is left is ordinary Kotlin that a JVM test drives
 * through a thousand candidates in milliseconds.
 *
 * The semantics come from the core, never from here: a candidate exists only
 * after the event was decrypted, signature-checked and applied to the log.
 * This class translates a verified meaning into an Android surface. It does
 * not decide what an event MEANS, and it never reads the DOM, the WebView or
 * the screen to find out.
 *
 * POSTED, DISMISSED AND READ ARE THREE DIFFERENT EVENTS (AR-1b.8), and
 * collapsing any two produces a specific, familiar bug:
 *
 * ```
 *   posted     we asked the system to show something
 *   dismissed  the person swiped the shade clear. They have NOT read it, and
 *              the dedup must survive — or the next sync brings it straight
 *              back, which reads as the phone arguing with them.
 *   read       the UI actually SHOWED the conversation. Only this clears the
 *              notification. A TAP does not read: it opens a screen that may
 *              never finish loading.
 * ```
 *
 * WHAT THE DURABLE DEFENCE ACTUALLY IS, stated because it is weaker than the
 * plan's word "frontier" suggests. AR-1b.1 shipped a candidate carrying
 * `event_id · space_id · device · schema · created_at · authored_locally` and
 * no `applied_frontier`. So there is no monotonic number to persist here, and
 * `created_at` cannot stand in for one: it is the author's own clock,
 * unverified, and a device with a skewed clock could then silence or
 * resurrect notifications on somebody else's phone.
 *
 * What is left is honest and bounded: a durable set of recently PRESENTED
 * event ids, FIFO, capped. It closes the two hazards it was pointed at —
 * reconnect and process restart both re-deliver RECENT events — and it does
 * not close a re-delivery older than the cap. That case is recorded rather
 * than papered over; closing it properly needs the frontier in the DTO, which
 * is a core change and belongs to AR-1b.5.
 */
class NotificationCoordinator(
    private val presenter: Presenter,
    private val store: Store,
) {

    /** What the system layer is asked to do. Implemented for real in AR-1b.3+. */
    interface Presenter {
        /** Show or update the one live notification for a space. */
        fun present(p: Presentation)

        /**
         * Take the space's notification down. Not a read, not a dismissal.
         *
         * The TAG is passed rather than derived: it is an opaque local key,
         * and a presenter that rebuilt it from the space id would post under
         * one identity and cancel under another — a notification nothing can
         * ever take down again.
         */
        fun clear(spaceId: String, tag: String)
    }

    /**
     * The durable half. An interface rather than SharedPreferences directly,
     * so the decisions above can be tested against an in-memory implementation
     * with no device — and so the persistence can change without touching a
     * single decision.
     */
    interface Store {
        /** Ids presented recently, oldest first. Bounded by the caller. */
        fun rememberedIds(): List<String>

        fun writeRememberedIds(ids: List<String>)

        /**
         * A DEVICE-LOCAL opaque key for a space, created once and remembered.
         *
         * The notification tag must not be the space id. A tag is not private:
         * StatusBarNotification carries it verbatim to every
         * NotificationListenerService the person has granted access to — a
         * launcher, a smartwatch companion, a backup tool. In the strictest
         * privacy mode the title and the text are hidden precisely so that
         * nothing identifies the conversation, and a raw protocol identifier
         * in the tag would hand it over anyway, as metadata, to software that
         * was never told anything else.
         *
         * The key is random, means nothing outside this install, and is stable
         * so that the SAME space keeps updating the SAME notification.
         */
        fun notificationKey(spaceId: String): String
    }

    /** What a host needs in order to render. Data, never behaviour. */
    class Presentation(val spaceId: String, val tag: String, items: List<Item>) {
        val items: List<Item> = items.toList()
    }

    /** One message inside a conversation notification, oldest first. */
    class Item(
        val eventId: String,
        val device: String,
        val schema: String,
        val occurredAtUnixMs: Long,
        val senderLabel: String,
        val previewText: String,
    )

    /**
     * What the core hands over, as one value rather than eight parameters.
     *
     * The presentation half — the labels — is deliberately separate from the
     * half the decisions run on. Nothing below depends on a label, so a space
     * whose manifest has not arrived still deduplicates correctly and still
     * notifies exactly once.
     */
    data class Candidate(
        val eventId: String,
        val spaceId: String,
        val device: String,
        val schema: String,
        val occurredAtUnixMs: Long,
        val presentationCursor: Long,
        val authoredLocally: Boolean,
        val spaceLabel: String = "",
        val senderLabel: String = "",
        val previewText: String = "",
    )

    /**
     * Why a candidate did or did not become a notification. Returned rather
     * than logged, because "nothing appeared" has six different causes and a
     * harness that cannot tell them apart cannot prove any of them.
     */
    enum class Decision {
        PRESENTED,
        SUPPRESSED_AUTHORED_LOCALLY,
        SUPPRESSED_SCHEMA_NOT_NOTIFIABLE,
        SUPPRESSED_ALREADY_PRESENTED,
        SUPPRESSED_SPACE_ON_SCREEN,
        SUPPRESSED_DISABLED,
    }

    /** Durable, bounded, oldest first. Mirrors [Store] in memory. */
    private val presented = ArrayDeque<String>()

    /** Live aggregation per space — what a single notification is showing. */
    private val live = HashMap<String, ArrayDeque<Item>>()

    /** Set by the host when a screen resumes and cleared when it pauses. */
    private var foreground = false
    private var visibleSpace: String? = null

    /** False when the person refused the permission (AR-1b.3 asks for it). */
    private var enabled = true

    private val counts = LinkedHashMap<Decision, Int>()

    /** The highest presentation cursor seen, scoped to one runtime. */
    private var highestCursor = 0L

    /** space id -> opaque local tag, resolved once and cached. */
    private val tags = HashMap<String, String>()

    init {
        presented.addAll(store.rememberedIds())
        trimRemembered()
    }

    /**
     * The whole decision, in the order the gates must run.
     *
     * Synchronized because candidates arrive on a Go goroutine while the host
     * calls the lifecycle methods from the main thread. It returns promptly by
     * construction: a set lookup, a bounded write, one call into the
     * presenter.
     */
    @Synchronized
    fun onCandidate(c: Candidate): Decision {
        if (!enabled) return count(Decision.SUPPRESSED_DISABLED)

        // A person is not told about the thing they just did — and the core
        // says so, so the host does not have to work out who they are.
        if (c.authoredLocally) return count(Decision.SUPPRESSED_AUTHORED_LOCALLY)

        if (!NotificationPolicy.notifiable(c.schema)) {
            return count(Decision.SUPPRESSED_SCHEMA_NOT_NOTIFIABLE)
        }
        val eventId = c.eventId
        val spaceId = c.spaceId

        // The cursor is the core's own count of applied events, and it only
        // ever moves forward within one runtime. Remembering the highest one
        // seen is what lets a host say "nothing has happened since" — AR-1b.5
        // builds its durable ledger on it.
        if (c.presentationCursor > highestCursor) highestCursor = c.presentationCursor

        // The durable gate, and it runs BEFORE the on-screen gate on purpose:
        // an event this phone has already presented must not be re-presented
        // by a reconnect, whatever is on screen right now.
        if (presented.contains(eventId)) {
            return count(Decision.SUPPRESSED_ALREADY_PRESENTED)
        }

        // AR-1b.7: keyed on the VISIBLE SPACE and a resumed screen — never on
        // "the process is foreground". A person reading space A must still be
        // told about space B, and a person in Settings must be told about
        // both. Remembered anyway: the event was seen, so a later reconnect
        // must not resurrect it as news.
        if (foreground && spaceId == visibleSpace) {
            remember(eventId)
            return count(Decision.SUPPRESSED_SPACE_ON_SCREEN)
        }

        remember(eventId)
        val items = live.getOrPut(spaceId) { ArrayDeque() }
        items.addLast(Item(eventId, c.device, c.schema, c.occurredAtUnixMs, c.senderLabel, c.previewText))
        while (items.size > MESSAGES_PER_SPACE) items.removeFirst()

        presenter.present(Presentation(spaceId, tagFor(spaceId), items.toList()))
        return count(Decision.PRESENTED)
    }

    /** A screen resumed. Which space it is showing is a separate fact. */
    @Synchronized
    fun onForeground(resumed: Boolean) {
        foreground = resumed
    }

    /**
     * The space currently on screen, or null. Setting it does NOT mark
     * anything read: showing a conversation is what reads it, and that is
     * [onRead], called by the code that actually rendered the messages.
     */
    @Synchronized
    fun onVisibleSpace(spaceId: String?) {
        visibleSpace = spaceId
    }

    /**
     * The person swiped the shade clear. The live aggregation goes; the dedup
     * memory stays. Losing the dedup here is what makes a notification come
     * back on the next sync tick — the phone arguing with the person.
     */
    @Synchronized
    fun onDismissed(spaceId: String) {
        live.remove(spaceId)
    }

    /**
     * The UI showed this conversation. Only now does the notification come
     * down. Not on tap: a tap opens a screen that may never finish loading,
     * and clearing there would lose the notification for a message nobody saw.
     */
    @Synchronized
    fun onRead(spaceId: String) {
        live.remove(spaceId)
        presenter.clear(spaceId, tagFor(spaceId))
    }

    /** The permission was refused, or the person switched notifications off. */
    @Synchronized
    fun setEnabled(on: Boolean) {
        enabled = on
        if (!on) {
            for (spaceId in live.keys.toList()) presenter.clear(spaceId, tagFor(spaceId))
            live.clear()
        }
    }

    /** Decision counts, for the rig's status line and for AR-1b.11. */
    @Synchronized
    fun stats(): Map<String, Int> {
        val out = LinkedHashMap<String, Int>()
        for (d in Decision.entries) {
            out[d.name.lowercase()] = counts[d] ?: 0
        }
        out["live_spaces"] = live.size
        out["remembered_ids"] = presented.size
        out["highest_cursor"] = highestCursor.toInt()
        return out
    }

    private fun tagFor(spaceId: String): String =
        tags.getOrPut(spaceId) { "space:" + store.notificationKey(spaceId) }

    private fun remember(eventId: String) {
        presented.addLast(eventId)
        trimRemembered()
        store.writeRememberedIds(presented.toList())
    }

    private fun trimRemembered() {
        while (presented.size > REMEMBERED_IDS) presented.removeFirst()
    }

    private fun count(d: Decision): Decision {
        counts[d] = (counts[d] ?: 0) + 1
        return d
    }

    companion object {
        /**
         * How many presented event ids survive a restart. Sized for the
         * hazard: reconnect and process restart re-deliver what sync is
         * currently carrying — hundreds at most — not the whole journal.
         * Larger would buy an ever rarer case at the cost of a bigger write
         * on every notification.
         */
        const val REMEMBERED_IDS = 512

        /** Messages kept per space for aggregation (AR-1b.3's MessagingStyle). */
        const val MESSAGES_PER_SPACE = 8
    }
}
