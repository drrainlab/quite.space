package space.quiet.arprobe

import android.app.Notification
import android.app.Person
import android.content.Context
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
internal class ConversationRenderer(
    context: Context,
    private val shortcuts: ShortcutRegistry,
) {

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

        // THE SHORTCUT IS ASKED FOR, NOT ASSUMED. Publication is rate limited
        // and can simply refuse, and a MessagingStyle notification whose
        // shortcut does not exist is not a conversation — it is a notification
        // claiming to be one. So a refusal returns null here and the caller
        // falls back to the generic renderer, which is honest about what it is.
        val shortcutId = shortcuts.ensurePreviewShortcut(p.spaceId, p.tag, r) ?: return null

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


    companion object {
        private const val TAG = "quiet-notify"

        /** One group for every live conversation; the summary lands in b.6b. */
        const val GROUP_KEY = "quite.active_conversations"
    }
}
