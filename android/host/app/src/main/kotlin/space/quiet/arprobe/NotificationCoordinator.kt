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
    /**
     * AR-1c.6 — how a retry gets back here later.
     *
     * INJECTED so a test can drive it without a clock, and placed BEFORE the
     * acker so `NotificationCoordinator(presenter, ledger) { ack(it) }` still
     * means what it always did: the trailing lambda is the acknowledgement,
     * and a caller that does not care about retries does not mention them.
     * The default does nothing, which is exactly the old behaviour — the core
     * keeps the candidate and replays it on the next attach.
     */
    private val schedule: (delayMs: Long, run: () -> Unit) -> Unit = { _, _ -> },
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

        /**
         * The space is gone for good: take down the conversation surface too
         * (SD-0). Separate from [clear] because clearing is ordinary — a read
         * conversation keeps its shortcut so the next message threads onto it
         * — and this one is not: there will be no next message.
         */
        fun retireConversation(spaceId: String, tag: String)
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

        /** Only what we believe the system is holding: presented, not pending. */
        fun presentedBySpace(): Map<String, Recovery>

        /**
         * The space was deleted from this device (SD-0): remove its rows and
         * its opaque key. Nothing is retained — see NotificationDb.forgetSpace
         * for why a tombstone here would be a record of something somebody
         * asked to be rid of, kept against a replay that cannot happen.
         */
        fun forgetSpace(spaceId: String)

        /** The opaque tag for a space, or null when it has never had one. */
        fun tagFor(spaceId: String): String?

        /**
         * The device-local opaque key for a sender, minted on first sight.
         *
         * Never the device id and never the name: Android expects a stable,
         * distinguishable Person identity across republications, a name merges
         * two strangers who share one, and a device id would turn the system's
         * conversation metadata into a map of who this person talks to.
         */
        fun personKey(device: String, label: String): String

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

    class Insert(val tag: String, val spaceLabel: String, val items: List<Item>)
    class Recovery(val tag: String, val spaceLabel: String, val items: List<Item>)

    /**
     * What a host needs in order to render. Data, never behaviour.
     *
     * The space's LABEL rides here rather than being looked up: a renderer
     * asking storage for a name would be a second place that decides what may
     * be shown, and the privacy policy has to have exactly one.
     */
    class Presentation(
        val spaceId: String,
        val tag: String,
        val spaceLabel: String,
        items: List<Item>,
    ) {
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
        // The core's word: this event carries a signed mention of the
        // person. Routes its presentation to the personal signals channel.
        val personal: Boolean = false,
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
        val personal: Boolean = false,
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
    private var reads = 0

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
            // AND TRY AGAIN WITHOUT WAITING FOR A RESTART. A database that
            // was locked for a second should cost a second, not a session:
            // the core's replay is a real guarantee but it only fires when
            // something re-attaches, which on a phone can be hours.
            deferForRetry(c)
            return count(Decision.DEFERRED_STORAGE_UNAVAILABLE)
        }
        acker.ack(c.eventId)

        if (inserted == null) {
            // Already known: a redelivery after a crash between the insert and
            // the acknowledgement. The count must not move, and the
            // acknowledgement above is exactly what this pass was for.
            return count(Decision.SUPPRESSED_ALREADY_PRESENTED)
        }

        return count(presentInserted(c, inserted))
    }

    /**
     * Everything after a candidate is safely stored: the rules that decide
     * whether it is SHOWN.
     *
     * Shared with the storage retry, so a candidate that arrived a second
     * late is treated exactly like one that arrived on time — a second path
     * with its own copy of these rules is how a suppression stops applying to
     * half the messages.
     */
    private fun presentInserted(c: Candidate, inserted: Insert): Decision {
        if (!enabled) {
            // Terminal, not queued: turning notifications on next week must
            // not produce last week's messages.
            ledger.markSuppressed(c.eventId, NotificationDb.STATE_SUPPRESSED_PERMISSION)
            return Decision.SUPPRESSED_DISABLED
        }

        // AR-1b.7: keyed on the VISIBLE SPACE and a resumed screen — never on
        // "the process is foreground". A person reading space A must still be
        // told about space B, and a person in Settings must be told about
        // both. Recorded anyway, so a later reconnect cannot resurrect it.
        if (foreground && c.spaceId == visibleSpace) {
            ledger.markSuppressed(c.eventId, NotificationDb.STATE_SUPPRESSED_ON_SCREEN)
            return Decision.SUPPRESSED_SPACE_ON_SCREEN
        }

        presenter.present(Presentation(c.spaceId, inserted.tag, inserted.spaceLabel, inserted.items))
        ledger.markPresented(c.eventId)
        return Decision.PRESENTED
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
            presenter.present(Presentation(spaceId, r.tag, r.spaceLabel, r.items))
            for (item in r.items) ledger.markPresented(item.eventId)
        }
    }

    /**
     * AR-1b.6b.6 — what the ledger believes, checked against what Android
     * actually holds.
     *
     * FOUND BY THE LIVE GATE, and it is the summary that made it visible: the
     * shade held two conversations while the ledger counted seven, so the line
     * above them said "7 new signals" about five notifications that were not
     * there. Notifications leave the shade in ways nothing tells us about — a
     * force-stop, a reboot, the system pruning a package that has posted too
     * many — and every one of those leaves a row saying `presented` for
     * something nobody can see.
     *
     * WHAT IS *NOT* DONE HERE is re-posting them. A notification that vanished
     * because the person cleared it, or because the phone restarted, must stay
     * gone: bringing seven old conversations back after a reboot is the phone
     * arguing with the person. So a vanished notification is treated exactly
     * as a swipe — the aggregation closes, the DEDUP MEMORY STAYS, and nothing
     * is announced twice.
     *
     * PENDING ROWS ARE NOT TOUCHED. Pending means not posted yet, so its
     * absence from the shade is correct rather than evidence of anything.
     */
    @Synchronized
    fun reconcileWithSystem(liveTags: Set<String>) {
        if (!enabled) return
        for ((spaceId, r) in ledger.presentedBySpace()) {
            if (liveTags.contains(r.tag)) continue
            ledger.dismiss(spaceId)
            // Cancels nothing — it is already gone — but recomputes the line
            // above the ones that are left, which is the point.
            presenter.clear(spaceId, r.tag)
        }
    }

    /**
     * SD-0 — the space is gone from this device, so what is on the screen for
     * it goes now.
     *
     * NOT AT THE NEXT RECONCILIATION. A person said "forget this space" and
     * the core forgot it; a shade still showing the conversation as a live
     * thing is the two halves disagreeing in the direction the person can see.
     * The core says so the moment the deletion commits, and this is the other
     * end of that seam.
     *
     * IT RUNS EVEN WHEN NOTIFICATIONS ARE OFF, unlike everything else here.
     * `enabled` guards PRODUCING notifications; taking one down is the
     * opposite kind of act, and skipping it because the person had already
     * turned notifications off would leave the one card they cannot explain.
     */
    @Synchronized
    fun forgetSpace(spaceId: String) {
        // THE TAG IS READ FIRST AND THE ROWS GO SECOND, because the opaque key
        // is stored with them and cancelling needs it — a presenter that
        // rebuilt the tag from the space id would cancel under an identity it
        // never posted under.
        val tag = ledger.tagFor(spaceId)
        ledger.forgetSpace(spaceId)
        if (tag != null) {
            // AFTER the rows are gone: clear() recomputes the line above the
            // conversations that are left, and it reads the ledger to do it.
            // Called the other way round, the summary counts the space that
            // was just deleted and goes on hanging over a single conversation.
            presenter.clear(spaceId, tag)
            presenter.retireConversation(spaceId, tag)
        }
    }

    // --------------------------------------------------- storage that failed

    /**
     * Candidates the disk refused, waiting to be offered again.
     *
     * BOUNDED, AND SMALL. This is a bridge over a moment, not a queue: if the
     * storage is gone for good, holding a thousand candidates in memory helps
     * nobody and the process that dies holding them loses nothing anyway —
     * NONE of them were acknowledged, so the core still has every one. Past
     * the bound a candidate simply is not retried, which is exactly the
     * behaviour that existed before.
     */
    private val awaitingStorage = LinkedHashMap<String, Candidate>()
    private var retryAttempt = 0
    private var retryArmed = false

    private fun deferForRetry(c: Candidate) {
        if (awaitingStorage.size >= MAX_DEFERRED) return
        awaitingStorage[c.eventId] = c
        armRetry()
    }

    /**
     * ONE TIMER FOR ALL OF THEM, backing off. Ten candidates arriving into a
     * full disk are ten symptoms of one problem, and ten timers would hammer
     * the thing that is already failing.
     *
     * The backoff doubles from a second and stops at a minute; after
     * [MAX_ATTEMPTS] rounds the whole set is dropped from memory. Dropped is
     * not lost: nothing here was ever acknowledged, so the core replays all
     * of it on the next attach — which is where this started and where it
     * ends if the disk really is gone.
     */
    private fun armRetry() {
        if (retryArmed) return
        retryArmed = true
        val delay = RETRY_BASE_MS shl minOf(retryAttempt, 6)
        schedule(minOf(delay, RETRY_MAX_MS)) { retryStorage() }
    }

    @Synchronized
    private fun retryStorage() {
        retryArmed = false
        if (awaitingStorage.isEmpty()) {
            retryAttempt = 0
            return
        }
        retryAttempt++
        val pending = awaitingStorage.values.toList()
        var stored = 0
        for (c in pending) {
            val inserted = try {
                ledger.insertPending(c)
            } catch (t: Throwable) {
                continue // still unavailable; the rest of this round will fail too
            }
            awaitingStorage.remove(c.eventId)
            stored++
            // From here it is an ordinary arrival: acknowledged because it is
            // durable, and shown unless a rule says otherwise.
            acker.ack(c.eventId)
            if (inserted != null) count(presentInserted(c, inserted))
        }
        if (stored > 0) retryAttempt = 0
        when {
            awaitingStorage.isEmpty() -> retryAttempt = 0
            retryAttempt >= MAX_ATTEMPTS -> {
                // Give up in memory, deliberately and quietly: the core is
                // still holding every one of these.
                awaitingStorage.clear()
                retryAttempt = 0
                count(Decision.DEFERRED_STORAGE_UNAVAILABLE)
            }
            else -> armRetry()
        }
    }

    /** How many candidates are waiting for storage right now. */
    @Synchronized
    fun deferredForStorage(): Int = awaitingStorage.size

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
     * Whether the interface says a conversation is on screen — for
     * diagnostics, and deliberately NOT which one. A harness needs to know the
     * bridge is wired; the space on screen is the person's business and would
     * be a second place a conversation's identity is written down.
     */
    @Synchronized
    fun hasVisibleSpace(): Boolean = visibleSpace != null

    /** How many times the interface has said it showed a conversation. */
    @Synchronized
    fun readCount(): Int = reads

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
        reads++
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
        /** A bridge over a moment, not a queue: see awaitingStorage. */
        const val MAX_DEFERRED = 32
        const val MAX_ATTEMPTS = 6
        const val RETRY_BASE_MS = 1_000L
        const val RETRY_MAX_MS = 60_000L

        /** Messages kept per space for aggregation (AR-1b.6's MessagingStyle). */
        const val MESSAGES_PER_SPACE = 8
    }
}
