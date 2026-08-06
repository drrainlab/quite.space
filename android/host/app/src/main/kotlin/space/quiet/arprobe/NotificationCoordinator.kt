package space.quiet.arprobe

/**
 * AR-1b.2 + AR-1b.5.3c — the one place that decides whether a verified event
 * becomes a notification, and the order in which that becomes durable.
 *
 * NO ANDROID TYPES APPEAR IN THIS FILE, and that is the design rather than a
 * preference. Every hazard here is a DECISION, and a decision that can only be
 * exercised on a device is a decision nobody tests. Storage lives behind
 * [Ledger], the system surface behind [Presenter], and the core behind
 * [Acker]; what is left is ordinary Kotlin a JVM test drives through a
 * thousand candidates in milliseconds.
 *
 * THE ORDER IS THE CONTRACT, and every step of it answers a crash:
 *
 * ```
 *   durable insert as pending   the host now holds it, whatever happens next
 *   acknowledge to the core     the log may stop replaying it
 *   post to the system          the person is told
 *   mark presented              and now we know they were
 * ```
 *
 * Reversing any two loses something. Acknowledging before the insert means a
 * crash in between drops the candidate from both sides at once. Posting before
 * the insert means a notification nothing remembers, which a swipe then cannot
 * deduplicate. Marking presented before posting means recovery believes a
 * shade was told something it never saw.
 *
 * Everything that lands mid-sequence is recoverable, and recovery is
 * idempotent because the pair (tag, id) updates one system entry rather than
 * making a second.
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
 * A REFUSED PERMISSION IS TERMINAL, NOT A QUEUE. A candidate that arrives
 * while notifications are off is recorded, acknowledged and closed — never
 * held back for the day the person changes their mind. Somebody who enables
 * notifications after a week should get the next message, not a week of them.
 */
class NotificationCoordinator(
    private val presenter: Presenter,
    private val ledger: Ledger,
    private val acker: Acker,
) {

    /** What the system layer is asked to do. */
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
     * The durable half. An interface rather than SQLite directly, so the
     * decisions can be tested against an in-memory implementation with no
     * device — and so the storage can change without touching one of them.
     */
    interface Ledger {
        /** Durable insert as pending; null when the event was already known. */
        fun insertPending(c: Candidate): Insert?

        fun markPresented(eventId: String)
        fun markSuppressed(eventId: String, reason: String)

        /** Closes the live aggregation; the dedup memory stays. */
        fun dismiss(spaceId: String)

        /** The interface showed the conversation. */
        fun read(spaceId: String)

        /** Acknowledged to the core but never confirmed as posted. */
        fun pendingBySpace(): Map<String, Recovery>

        /**
         * Everything still LIVE — pending or presented. Distinct from
         * pendingBySpace on purpose: recovery must post only what was never
         * posted, while taking notifications down has to reach the ones
         * already on screen. Conflating them left a refused permission with
         * every existing card still showing.
         */
        fun activeBySpace(): Map<String, Recovery>

        /** The opaque tag for a space, or null when it has never had one. */
        fun tagFor(spaceId: String): String?

        /**
         * Drops terminal rows the core can no longer replay. Returns how many
         * went, so a harness can tell "nothing was eligible" from "compaction
         * never ran".
         */
        fun compact(retainFrom: Map<String, Map<String, Long>>): Int
    }

    /** How the core is told it may stop replaying a candidate. */
    fun interface Acker {
        fun ack(eventId: String)
    }

    class Insert(val tag: String, val items: List<Item>)
    class Recovery(val tag: String, val items: List<Item>)

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
     * What the core hands over, as one value rather than ten parameters.
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
        val sourceSequence: Long,
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

        /**
         * The durable insert failed. NOT a suppression and not a loss: the
         * candidate was never acknowledged, so the core still holds it and
         * will offer it again on the next attach. A full disk or a locked
         * database becomes a late notification rather than a missing one.
         */
        DEFERRED_STORAGE_UNAVAILABLE,
    }

    /** Set by the host when a screen resumes and cleared when it pauses. */
    private var foreground = false
    private var visibleSpace: String? = null

    /** False when the person refused the permission (AR-1b.3 asks for it). */
    private var enabled = true

    private val counts = LinkedHashMap<Decision, Int>()

    /** The highest presentation cursor seen, scoped to one runtime. */
    private var highestCursor = 0L

    /**
     * The whole decision, in the order the gates must run.
     *
     * Synchronized because candidates arrive on a Go goroutine while the host
     * calls the lifecycle methods from the main thread.
     */
    @Synchronized
    fun onCandidate(c: Candidate): Decision {
        // A person is not told about the thing they just did — and the core
        // says so, so the host does not have to work out who they are.
        //
        // Acknowledged and NOT recorded: these can never need recovery, and a
        // ledger row for every membership event and every one of our own
        // messages would be a database of things nobody will ever look at.
        // The acknowledgement still matters — without it, an ordinary event at
        // sequence 38 would pin the core's watermark there forever while 39
        // and 40 sat acknowledged behind it.
        if (c.authoredLocally) {
            acker.ack(c.eventId)
            return count(Decision.SUPPRESSED_AUTHORED_LOCALLY)
        }
        if (!NotificationPolicy.notifiable(c.schema)) {
            acker.ack(c.eventId)
            return count(Decision.SUPPRESSED_SCHEMA_NOT_NOTIFIABLE)
        }
        if (c.presentationCursor > highestCursor) highestCursor = c.presentationCursor

        // DURABLE FIRST. From here the host owns this candidate whatever
        // happens next, and only then is the core told it may stop replaying.
        //
        // A FAILED TRANSACTION IS NOT ACKNOWLEDGED, and that is the whole
        // safety property: the core keeps the candidate, replays it on the
        // next attach, and a database that could not be written costs a
        // delay rather than a message. Acknowledging first — or catching the
        // failure and carrying on — would drop it from both sides at once,
        // silently, on the day a phone ran out of storage.
        val inserted = try {
            ledger.insertPending(c)
        } catch (t: Throwable) {
            return count(Decision.DEFERRED_STORAGE_UNAVAILABLE)
        }
        acker.ack(c.eventId)

        if (inserted == null) {
            // Already known: a redelivery after a crash between the insert and
            // the acknowledgement. The count must not move, and the
            // acknowledgement above is exactly what this pass was for.
            return count(Decision.SUPPRESSED_ALREADY_PRESENTED)
        }

        if (!enabled) {
            // Terminal, not queued: turning notifications on next week must
            // not produce last week's messages.
            ledger.markSuppressed(c.eventId, NotificationDb.STATE_SUPPRESSED_PERMISSION)
            return count(Decision.SUPPRESSED_DISABLED)
        }

        // AR-1b.7: keyed on the VISIBLE SPACE and a resumed screen — never on
        // "the process is foreground". A person reading space A must still be
        // told about space B, and a person in Settings must be told about
        // both. Recorded anyway, so a later reconnect cannot resurrect it.
        if (foreground && c.spaceId == visibleSpace) {
            ledger.markSuppressed(c.eventId, NotificationDb.STATE_SUPPRESSED_ON_SCREEN)
            return count(Decision.SUPPRESSED_SPACE_ON_SCREEN)
        }

        presenter.present(Presentation(c.spaceId, inserted.tag, inserted.items))
        ledger.markPresented(c.eventId)
        return count(Decision.PRESENTED)
    }

    /**
     * What the process died in the middle of. Called once at startup: rows
     * acknowledged to the core but never confirmed as posted are posted now.
     *
     * Idempotent by construction — the same (tag, id) updates one system entry
     * rather than making a second — which is what makes it safe to run after a
     * crash that happened AFTER the notification was already showing.
     */
    @Synchronized
    fun recoverPending() {
        if (!enabled) return
        for ((spaceId, r) in ledger.pendingBySpace()) {
            if (r.items.isEmpty()) continue
            presenter.present(Presentation(spaceId, r.tag, r.items))
            for (item in r.items) ledger.markPresented(item.eventId)
        }
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
     * The person swiped the shade clear. The live aggregation closes; the
     * dedup memory stays. Losing the dedup here is what makes a notification
     * come back on the next sync tick — the phone arguing with the person.
     */
    @Synchronized
    fun onDismissed(spaceId: String) {
        ledger.dismiss(spaceId)
    }

    /**
     * The UI showed this conversation. Only now does the notification come
     * down. Not on tap: a tap opens a screen that may never finish loading,
     * and clearing there would lose the notification for a message nobody saw.
     */
    @Synchronized
    fun onRead(spaceId: String) {
        ledger.read(spaceId)
        ledger.tagFor(spaceId)?.let { presenter.clear(spaceId, it) }
    }

    /** The permission was refused, or the person switched notifications off. */
    @Synchronized
    fun setEnabled(on: Boolean) {
        enabled = on
        if (!on) {
            for ((spaceId, r) in ledger.activeBySpace()) {
                ledger.tagFor(spaceId)?.let { presenter.clear(spaceId, it) }
                for (item in r.items) {
                    ledger.markSuppressed(item.eventId, NotificationDb.STATE_SUPPRESSED_PERMISSION)
                }
            }
        }
    }

    /**
     * AR-1b.5.5 — forget what can never come back.
     *
     * `retainFrom` is the core's answer to "the oldest position I may still
     * replay", per space and per device, and it is the minimum across BOTH
     * checkpoint generations. Tombstones behind it are unreachable and may go;
     * tombstones at or after it must stay, because a damaged checkpoint falls
     * back to the older generation and replays what the newer one had already
     * confirmed. A host that trimmed to the CURRENT watermark would meet those
     * events again, believe them new, and resurrect notifications a person
     * dismissed a week ago.
     */
    @Synchronized
    fun compact(retainFrom: Map<String, Map<String, Long>>): Int =
        ledger.compact(retainFrom)

    /** Decision counts, for the rig's status line and for AR-1b.11. */
    @Synchronized
    fun stats(): Map<String, Int> {
        val out = LinkedHashMap<String, Int>()
        for (d in Decision.entries) {
            out[d.name.lowercase()] = counts[d] ?: 0
        }
        out["highest_cursor"] = highestCursor.toInt()
        return out
    }

    private fun count(d: Decision): Decision {
        counts[d] = (counts[d] ?: 0) + 1
        return d
    }

    companion object {
        /** Messages kept per space for aggregation (AR-1b.6's MessagingStyle). */
        const val MESSAGES_PER_SPACE = 8
    }
}
