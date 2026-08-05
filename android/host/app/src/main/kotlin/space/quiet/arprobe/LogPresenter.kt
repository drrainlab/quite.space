package space.quiet.arprobe

import android.util.Log
import org.json.JSONArray
import org.json.JSONObject

/**
 * The presenter until AR-1b.3 asks for the permission and AR-1b.4 creates the
 * channels: it records what WOULD be shown, and shows nothing.
 *
 * THIS IS DELIBERATE, NOT A STUB LEFT HALF-DONE. Posting a notification needs
 * `POST_NOTIFICATIONS`, a created channel, a published long-lived shortcut for
 * the conversation, and an explicit `PendingIntent` carrying the exact message
 * — four commits, each with its own way of being wrong. Wiring a
 * half-configured `NotificationManager` in now would produce notifications
 * that open the wrong conversation and cannot be told apart, on the commit
 * whose actual subject is the decision logic.
 *
 * What it buys immediately: the wave's central claim — *opening a large
 * journal presents nothing, and the next new event presents exactly one* — is
 * recorded as DECISIONS rather than as pixels, so it can be asserted without a
 * screen, a permission dialog or anybody watching a shade. The counters are
 * already in [RuntimeController.snapshot]; putting them into the harness's own
 * output is AR-1b.11, which is where instrumentation belongs and where the
 * debug rig is allowed to grow a field for them.
 */
internal class LogPresenter : NotificationCoordinator.Presenter {

    /** The last presentation per space, so the harness can read what was shown. */
    private val shown = LinkedHashMap<String, JSONObject>()

    private var presentCalls = 0
    private var clearCalls = 0

    @Synchronized
    override fun present(p: NotificationCoordinator.Presentation) {
        presentCalls++
        val o = JSONObject()
        try {
            o.put("tag", p.tag)
            o.put("space_id", p.spaceId)
            o.put("channel", NotificationPolicy.CHANNEL_MESSAGES)
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
            o.put("items", items)
        } catch (e: Exception) {
            Log.w(TAG, "present encode failed", e)
        }
        shown[p.spaceId] = o
        Log.i(TAG, "present ${p.tag} items=${p.items.size}")
    }

    @Synchronized
    override fun clear(spaceId: String) {
        clearCalls++
        shown.remove(spaceId)
        Log.i(TAG, "clear space:$spaceId")
    }

    /** What the harness reads. Never a rendering — a record of decisions. */
    @Synchronized
    fun snapshot(): JSONObject {
        val out = JSONObject()
        try {
            out.put("present_calls", presentCalls)
            out.put("clear_calls", clearCalls)
            val live = JSONArray()
            for (o in shown.values.toList()) live.put(o)
            out.put("live", live)
        } catch (e: Exception) {
            Log.w(TAG, "snapshot failed", e)
        }
        return out
    }

    private companion object {
        const val TAG = "quiet-notify"
    }
}
