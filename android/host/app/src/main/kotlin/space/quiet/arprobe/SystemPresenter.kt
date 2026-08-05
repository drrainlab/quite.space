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
 * somebody without ever seeing them arrive. The space id is already unique, so
 * it IS the tag and the id is a constant.
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
 * WHAT IT CANNOT SAY YET, and the reason is a boundary rather than an
 * omission. The candidate carries `space_id`, `device` and `schema` — no space
 * title, no sender name, no preview. The host may not invent them: naming the
 * space means reading the journal, which is exactly what this architecture
 * refuses, and asking the API from the emit path would put an HTTP round trip
 * inside a notification. So the text is a count, honestly, until the CORE
 * carries a name — which is the prerequisite AR-1b.7's three privacy modes
 * (Hidden / Space / Preview) are built on, and the point at which the
 * distinction between them becomes real at all.
 *
 * The content intent opens the space and no further. The EXACT message — and
 * the immutable, per-target-unique PendingIntent that carries it — is
 * AR-1b.8, with its two tests: a warm process with an existing Activity, and a
 * cold Activity with the runtime alive.
 */
internal class SystemPresenter(context: Context) : NotificationCoordinator.Presenter {

    private val app = context.applicationContext
    private val nm = app.getSystemService(NotificationManager::class.java)

    private val shown = LinkedHashMap<String, JSONObject>()
    private var presentCalls = 0
    private var clearCalls = 0

    @Synchronized
    override fun present(p: NotificationCoordinator.Presentation) {
        presentCalls++
        record(p)

        val n = p.items.size
        val notification = Notification.Builder(app, NotificationPolicy.CHANNEL_MESSAGES)
            .setSmallIcon(R.drawable.ic_stat_quiet)
            .setContentTitle("Quiet")
            .setContentText(if (n == 1) "A new message" else "$n new messages")
            // The AUTHOR's clock, and it is not ours: used for ordering the
            // shade, never for deciding what is new. That decision belongs to
            // the coordinator's own memory, where a skewed remote clock cannot
            // reach it.
            //
            // SECONDS BECOME MILLIS HERE. The protocol says unix SECONDS
            // (protocol/signal: "advisory only"); Android says millis. Handing
            // the number over unconverted put every notification in January
            // 1970 and the shade rendered it as "56y" — seen on the first live
            // run, which is the only place a units mismatch of this kind ever
            // shows up.
            .setWhen(p.items.last().createdAt * 1000L)
            .setShowWhen(true)
            .setAutoCancel(false)
            .setContentIntent(openSpace(p.spaceId))
            .setDeleteIntent(dismissed(p.spaceId))
            .build()

        try {
            nm?.notify(p.tag, NOTIFICATION_ID, notification)
        } catch (t: Throwable) {
            // A refused permission is an ordinary state and the coordinator
            // already stops presenting when it knows — but the person can
            // switch notifications off between the check and the post, and
            // that must not become a crash on a Go goroutine's stack.
            Log.w(TAG, "notify refused for ${p.tag}", t)
        }
    }

    @Synchronized
    override fun clear(spaceId: String) {
        clearCalls++
        shown.remove(spaceId)
        try {
            nm?.cancel("space:$spaceId", NOTIFICATION_ID)
        } catch (t: Throwable) {
            Log.w(TAG, "cancel failed for $spaceId", t)
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
                        .put("created_at", it.createdAt)
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
