package space.quiet.arprobe

import android.app.Notification
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.util.Log
import org.json.JSONArray
import org.json.JSONObject

/**
 * AR-1b.4 — one live notification per space, and the rules that make it one.
 *
 * A STABLE TAG PLUS A CONSTANT ID, never a hash of the space id into an int.
 * Android identifies a notification by the pair (tag, id), and the tempting
 * shortcut — `spaceId.hashCode()` as the id — carries a silent collision
 * probability across a 32-bit space. A collision here is not a cosmetic
 * defect: two different conversations would share one notification, each
 * update overwriting the other's, and a person would lose messages from
 * somebody without ever seeing them arrive. So uniqueness lives in the TAG and
 * the id is a constant.
 *
 * The tag is an OPAQUE DEVICE-LOCAL KEY, not the space id (AR-1b.5). A tag is
 * not private: StatusBarNotification carries it verbatim to every
 * NotificationListenerService the person has granted access to. Putting a
 * protocol identifier there would hand the conversation's identity to that
 * software as metadata — including in the privacy mode whose entire purpose is
 * that nothing identifies the conversation.
 *
 * NO autoCancel, AND THAT IS THE MODEL SHOWING THROUGH. A tap does not read a
 * conversation: it opens a screen that may never finish loading. The
 * notification comes down when the UI has actually SHOWN the space — the
 * coordinator's `onRead` — and not before. Letting the platform cancel it on
 * tap would lose the notification for a message nobody saw.
 *
 * A SWIPE IS REPORTED BACK. The delete intent tells the coordinator the shade
 * was cleared, which drops the live aggregation and keeps the dedup: without
 * it the next message would re-post the whole conversation a person just
 * swiped away, and the dedup memory would have no way to learn the difference
 * between dismissed and never-shown.
 *
 * WHAT IT DOES NOT SAY YET, and the restraint is deliberate. The candidate now
 * carries a space label, a sender label and a preview (AR-1b.5.1) — resolved
 * by the CORE from state it already holds, because naming a space from up here
 * would mean reading the journal, which this architecture refuses. They are
 * carried and not shown: choosing between Hidden, Space and Preview is a
 * privacy decision with its own gate (AR-1b.7), and rendering a preview by
 * default would be taking that decision by omission.
 *
 * The content intent opens the space and no further. The EXACT message — and
 * the immutable, per-target-unique PendingIntent that carries it — is
 * AR-1b.8, with its two tests: a warm process with an existing Activity, and a
 * cold Activity with the runtime alive.
 */
internal class SystemPresenter(
    context: Context,
    private val ledger: SqliteLedger,
    @Volatile var policy: PresentationPolicy = PresentationPolicy.DEFAULT,
) : NotificationCoordinator.Presenter {

    private val app = context.applicationContext
    private val nm = app.getSystemService(NotificationManager::class.java)
    private val shortcuts = ShortcutRegistry(app)
    private val conversations = ConversationRenderer(app, shortcuts)

    private val shown = LinkedHashMap<String, JSONObject>()
    private var presentCalls = 0
    private var clearCalls = 0

    @Synchronized
    override fun present(p: NotificationCoordinator.Presentation) {
        presentCalls++
        record(p)

        // WHICH RENDERER IS A PRIVACY DECISION, not a capability one. The
        // conversation surface carries names and people into system metadata
        // that a generic notification does not, so it is reachable only
        // through a policy a person has chosen — and until AR-1b.7 asks them,
        // the default is the strict mode and this branch is never taken.
        val rendering = ConversationProjection.of(
            policy, p.spaceLabel, p.items,
        ) { device -> ledger.personKey(device, senderOf(p, device)) }

        if (rendering.useConversationSurface) {
            val conversation = conversations.build(
                p, rendering, openSpace(p.spaceId), dismissed(p.spaceId),
            )
            if (conversation != null) {
                post(p, conversation)
                return
            }
            // Too old for conversations: fall through to the generic one
            // rather than show nothing.
        }

        val n = p.items.size
        val notification = Notification.Builder(app, NotificationPolicy.CHANNEL_MESSAGES)
            .setSmallIcon(R.drawable.ic_stat_quiet)
            .setContentTitle(rendering.conversationTitle ?: "Quiet")
            .setContentText(rendering.genericText)
            // The AUTHOR's clock, and it is not ours: used for ordering the
            // shade, never for deciding what is new. That decision belongs to
            // the coordinator's own memory, where a skewed remote clock cannot
            // reach it.
            //
            // Milliseconds, and the field says so. The conversion lives at the
            // source now (AR-1b.5.1): a number crossing a language boundary
            // carries its unit in its name, or it eventually carries the wrong
            // one — which is how every notification once claimed to be from
            // January 1970 and the shade rendered "56y".
            .setWhen(p.items.last().occurredAtUnixMs)
            .setShowWhen(true)
            .setAutoCancel(false)
            .setContentIntent(openSpace(p.spaceId))
            .setDeleteIntent(dismissed(p.spaceId))
            // Grouped whichever renderer built it: a summary that could only
            // gather conversation-style children would leave the strict modes
            // with a flat list and no way to collapse it.
            .setGroup(GroupSummary.KEY)
            .setGroupAlertBehavior(Notification.GROUP_ALERT_CHILDREN)
            .build()

        post(p, notification)
    }

    private fun post(p: NotificationCoordinator.Presentation, n: Notification) {
        try {
            nm?.notify(p.tag, NOTIFICATION_ID, n)
            updateSummary()
        } catch (t: Throwable) {
            // A refused permission is an ordinary state and the coordinator
            // already stops presenting when it knows — but the person can
            // switch notifications off between the check and the post, and
            // that must not become a crash on a Go goroutine's stack.
            Log.w(TAG, "notify refused for ${p.tag}", t)
        }
    }

    /**
     * A person changed what Android may know. Tightening takes the system
     * surfaces back for every space that has one; loosening publishes nothing
     * by itself, because publication belongs to the next notification and to
     * the policy, never to a setting screen.
     */
    @Synchronized
    fun applyPolicy(next: PresentationPolicy, spaceTags: Map<String, String>) {
        val previous = policy
        policy = next
        if (!next.tightensFrom(previous)) return
        for ((spaceId, tag) in spaceTags) {
            try {
                nm?.cancel(tag, NOTIFICATION_ID)     // the PREVIEW card first
                shortcuts.teardownConversationSurface(spaceId, tag)
            } catch (t: Throwable) {
                Log.w(TAG, "could not take back the conversation surface for $spaceId", t)
            }
        }
    }

    /** What the system still holds for a space, for tests and diagnostics. */
    fun publishedState(tag: String): Map<String, Any> = shortcuts.inspectPublishedState(tag)

    /** The last label seen for a device in this aggregation, or empty. */
    private fun senderOf(p: NotificationCoordinator.Presentation, device: String): String =
        p.items.lastOrNull { it.device == device && it.senderLabel.isNotEmpty() }?.senderLabel ?: ""

    @Synchronized
    override fun clear(spaceId: String, tag: String) {
        clearCalls++
        shown.remove(spaceId)
        try {
            nm?.cancel(tag, NOTIFICATION_ID)
            updateSummary()
        } catch (t: Throwable) {
            Log.w(TAG, "cancel failed for $spaceId", t)
        }
    }

    /**
     * Recomputed after every change rather than posted once and forgotten.
     *
     * The summary exists only for two or more conversations — one child with a
     * line above it is a person told the same thing twice — so it appears at
     * two and is cancelled again at one. Doing that by recomputation is what
     * makes dismiss, read, suppression and a closed generation all correct
     * without any of them knowing the summary exists.
     *
     * GROUP_ALERT_CHILDREN on both sides: the children make the sound, and a
     * summary that alerted too would double every arrival.
     */
    private fun updateSummary() {
        val active = try {
            ledger.activeBySpace()
        } catch (t: Throwable) {
            Log.w(TAG, "could not read the active set for the summary", t)
            return
        }
        if (!GroupSummary.needed(active.size)) {
            try {
                nm?.cancel(GroupSummary.TAG, NOTIFICATION_ID)
            } catch (t: Throwable) {
                Log.w(TAG, "could not take the summary down", t)
            }
            return
        }
        val messages = active.values.sumOf { it.items.size }
        val summary = Notification.Builder(app, NotificationPolicy.CHANNEL_MESSAGES)
            .setSmallIcon(R.drawable.ic_stat_quiet)
            .setContentTitle(GroupSummary.title(policy))
            .setContentText(GroupSummary.text(policy, active.size, messages))
            .setGroup(GroupSummary.KEY)
            .setGroupSummary(true)
            .setGroupAlertBehavior(Notification.GROUP_ALERT_CHILDREN)
            .setAutoCancel(false)
            .build()
        try {
            nm?.notify(GroupSummary.TAG, NOTIFICATION_ID, summary)
        } catch (t: Throwable) {
            Log.w(TAG, "could not post the summary", t)
        }
    }

    /**
     * Opens the space. FLAG_IMMUTABLE because nothing outside this app has any
     * business rewriting the target, and the request code is derived from the
     * space so two spaces cannot end up sharing one intent — the failure that
     * sends a person to somebody else's conversation.
     */
    private fun openSpace(spaceId: String): PendingIntent {
        val intent = Intent(app, QuietActivity::class.java)
            .setAction(Intent.ACTION_VIEW)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP)
            .putExtra(QuietActivity.EXTRA_SPACE, spaceId)
        return PendingIntent.getActivity(
            app, spaceId.hashCode(), intent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
    }

    private fun dismissed(spaceId: String): PendingIntent {
        val intent = Intent(app, NotificationDismissReceiver::class.java)
            .putExtra(QuietActivity.EXTRA_SPACE, spaceId)
        return PendingIntent.getBroadcast(
            app, spaceId.hashCode(), intent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
    }

    private fun record(p: NotificationCoordinator.Presentation) {
        try {
            val items = JSONArray()
            for (it in p.items) {
                items.put(
                    JSONObject()
                        .put("event_id", it.eventId)
                        .put("device", it.device)
                        .put("schema", it.schema)
                        .put("occurred_at_unix_ms", it.occurredAtUnixMs)
                )
            }
            shown[p.spaceId] = JSONObject()
                .put("tag", p.tag)
                .put("space_id", p.spaceId)
                .put("channel", NotificationPolicy.CHANNEL_MESSAGES)
                .put("items", items)
        } catch (e: Exception) {
            Log.w(TAG, "record failed", e)
        }
    }

    /**
     * What a harness reads. A record of DECISIONS beside the rendering, so the
     * wave's central claim stays assertable without a person looking at a
     * shade — and so a rendering that silently failed can be told apart from a
     * decision that was never taken.
     */
    @Synchronized
    fun snapshot(): JSONObject = JSONObject()
        .put("present_calls", presentCalls)
        .put("clear_calls", clearCalls)
        .put("live", JSONArray().also { arr -> shown.values.forEach { arr.put(it) } })

    private companion object {
        const val TAG = "quiet-notify"

        /**
         * Constant on purpose. Uniqueness lives in the TAG, which is the space
         * id — a value that is already unique and cannot collide, unlike any
         * int derived from it.
         */
        const val NOTIFICATION_ID = 1
    }
}
