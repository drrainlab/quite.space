package space.quiet.arprobe

import android.app.Notification
import android.app.Person
import android.content.Context
import android.content.Intent
import android.content.pm.ShortcutInfo
import android.content.pm.ShortcutManager
import android.os.Build
import android.util.Log

/**
 * AR-1b.6a — the conversation renderer, built and NOT yet reachable.
 *
 * It exists behind [PresentationPolicy], whose default is HIDDEN, so nothing
 * here publishes until AR-1b.7 gives a person a way to choose. That ordering
 * is the point rather than caution: a long-lived shortcut is itself a system
 * surface — it carries the space's name and its people into the launcher and
 * the sharesheet, and from Android 11 the conversation's title comes from the
 * shortcut's label rather than from MessagingStyle. Publishing one "early, so
 * the rest is easy" would break the strict mode before the strict mode
 * existed.
 *
 * WHAT IT DOES NOT TOUCH: absorb, pending/presented, the acknowledgement,
 * dismissal, generations, the notification tag, dedup, or crash recovery. It
 * receives an aggregation that is already decided and builds a different
 * Android object out of it. Everything that can be wrong about WHICH messages
 * and WHOSE names is in ConversationProjection, where a JVM test can reach it.
 *
 * THE SHORTCUT IS NOT DELETED ON DISMISS. Android keeps a person's own
 * conversation settings — priority, bubbles, silencing — against the cached
 * shortcut, and deleting it throws those away. It goes only when the space
 * does, or when the privacy mode tightens and the metadata must not survive.
 */
internal class ConversationRenderer(context: Context) {

    private val app = context.applicationContext

    /**
     * Builds the conversation notification. Returns null when the platform is
     * too old for conversations, which is an ordinary state: the caller falls
     * back to the generic renderer rather than showing nothing.
     */
    fun build(
        p: NotificationCoordinator.Presentation,
        r: ConversationProjection.Rendering,
        contentIntent: android.app.PendingIntent,
        deleteIntent: android.app.PendingIntent,
    ): Notification? {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.R) return null
        if (!r.useConversationSurface) return null

        val shortcutId = shortcutId(p.tag)
        val people = HashMap<String, Person>()
        for (line in r.lines) {
            people.getOrPut(line.personKey) {
                Person.Builder()
                    // The KEY is what Android threads a conversation by; the
                    // name is only what it shows. Keeping them separate is
                    // what lets somebody rename themselves without becoming a
                    // second person in the shade.
                    .setKey(line.personKey)
                    .setName(line.senderLabel.ifEmpty { "…" })
                    .setBot(false)
                    .build()
            }
        }

        // "me" is required by MessagingStyle and is deliberately anonymous: the
        // person holding the phone does not need to be told their own name, and
        // it would be one more label in system metadata for nothing.
        val style = Notification.MessagingStyle(
            Person.Builder().setKey("self").setName("").build()
        )
        r.conversationTitle?.let { style.conversationTitle = it }
        // Chronological, oldest first — Android renders them in the order they
        // are added, and a reversed list reads as a conversation running
        // backwards.
        for (line in r.lines) {
            style.addMessage(
                Notification.MessagingStyle.Message(
                    line.text, line.occurredAtUnixMs, people[line.personKey],
                )
            )
        }

        if (r.publishShortcut) publishShortcut(p, r, shortcutId)

        return Notification.Builder(app, NotificationPolicy.CHANNEL_MESSAGES)
            .setSmallIcon(R.drawable.ic_stat_quiet)
            .setStyle(style)
            .setShortcutId(shortcutId)      // without this it is not a conversation
            .setAutoCancel(false)
            .setContentIntent(contentIntent)
            .setDeleteIntent(deleteIntent)
            .setGroup(GROUP_KEY)
            .build()
    }

    /**
     * The shortcut id carries the space's OPAQUE key, never its id — a
     * shortcut is readable by the launcher, and the tag's whole reason for
     * being opaque applies here identically.
     */
    private fun shortcutId(tag: String) = "conversation:" + tag.removePrefix("space:")

    private fun publishShortcut(
        p: NotificationCoordinator.Presentation,
        r: ConversationProjection.Rendering,
        id: String,
    ) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.R) return
        val sm = app.getSystemService(ShortcutManager::class.java) ?: return
        val label = r.conversationTitle ?: "Quiet"
        try {
            val intent = Intent(app, QuietActivity::class.java)
                .setAction(Intent.ACTION_VIEW)
                .putExtra(QuietActivity.EXTRA_SPACE, p.spaceId)
            val shortcut = ShortcutInfo.Builder(app, id)
                .setLongLived(true)
                .setShortLabel(label)
                .setLongLabel(label)
                .setIntent(intent)
                .setPerson(
                    Person.Builder()
                        .setKey(r.lines.lastOrNull()?.personKey ?: id)
                        .setName(r.lines.lastOrNull()?.senderLabel?.ifEmpty { label } ?: label)
                        .build()
                )
                .build()
            sm.pushDynamicShortcut(shortcut)
        } catch (t: Throwable) {
            Log.w(TAG, "could not publish the conversation shortcut", t)
        }
    }

    /**
     * Removes a space's conversation metadata. Called when the privacy mode
     * TIGHTENS, in which case the system must not keep names it was told
     * under a looser one — and privacy outranks the conversation settings a
     * person may have made against the shortcut.
     */
    fun removeShortcut(tag: String) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.R) return
        val sm = app.getSystemService(ShortcutManager::class.java) ?: return
        try {
            sm.removeLongLivedShortcuts(listOf(shortcutId(tag)))
        } catch (t: Throwable) {
            Log.w(TAG, "could not remove the conversation shortcut", t)
        }
    }

    companion object {
        private const val TAG = "quiet-notify"

        /** One group for every live conversation; the summary lands in b.6b. */
        const val GROUP_KEY = "quite.active_conversations"
    }
}
