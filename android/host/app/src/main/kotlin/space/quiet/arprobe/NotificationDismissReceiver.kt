package space.quiet.arprobe

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

/**
 * The swipe, arriving back where the decision lives.
 *
 * POSTED, DISMISSED AND READ ARE THREE EVENTS, and this is the only way the
 * middle one is observable at all: the platform tells an app that its
 * notification was cleared through a delete intent, or not at all. Without
 * this receiver the coordinator's `onDismissed` would be a method nothing ever
 * calls — the live aggregation would keep growing behind a shade a person had
 * emptied, and the next single message would re-post the whole conversation
 * they just swiped away.
 *
 * It deliberately does NOT mark anything read. A swipe is "not now", not "I
 * have seen it": the dedup memory survives, so the same event is never
 * announced twice, and the read frontier only moves when the interface has
 * actually shown the conversation.
 */
class NotificationDismissReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        val spaceId = intent.getStringExtra(QuietActivity.EXTRA_SPACE) ?: return
        RuntimeController.get(context).notifications.onDismissed(spaceId)
    }
}
