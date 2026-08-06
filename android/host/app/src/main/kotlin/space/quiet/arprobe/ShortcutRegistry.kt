package space.quiet.arprobe

import android.content.Context
import android.content.Intent
import android.content.LocusId
import android.content.pm.ShortcutInfo
import android.content.pm.ShortcutManager
import android.os.Build
import android.util.Log

/**
 * AR-1b.6b.1 — who owns a conversation's system identity, and who takes it
 * back.
 *
 * The renderer builds a notification; it must not also decide how long a
 * shortcut lives. A shortcut outlives the notification that introduced it, it
 * is visible in the launcher and the sharesheet, and Android keeps a person's
 * own conversation settings against it — priority, bubbles, silencing. That is
 * a lifetime, and lifetimes belong to something that can answer questions
 * about them.
 *
 * SO THIS OWNS: the opaque shortcut id, the LocusId, the published label and
 * Person, and the teardown. The renderer asks for a shortcut and gets one.
 *
 * A SHORTCUT IS NOT DELETED ON DISMISS. Deleting it throws away settings the
 * person made, and a swipe is not a request to forget a conversation. It goes
 * when the space does, or when the privacy mode tightens.
 *
 * AND TIGHTENING IS NOT ONE CALL, because a PINNED shortcut cannot be deleted
 * by the app that made it. A person may have put this conversation on their
 * home screen; `removeLongLivedShortcuts` reaches the dynamic and cached
 * copies and leaves that one, with its label, its Person and the space's name
 * still sitting on a launcher that reads them. So the order is:
 *
 * ```
 *   1  sanitise   the shortcut is UPDATED to a generic label with no space
 *                 name, no sender, no Person — because whatever survives the
 *                 next step must already be harmless
 *   2  remove     the long-lived (dynamic and cached) copies go
 *   3  disable    anything still pinned is disabled with a neutral message,
 *                 since the app cannot take it off a home screen
 * ```
 *
 * Sanitising FIRST is the whole point: a surface that cannot be removed must
 * be made safe before it is abandoned, not after.
 */
internal class ShortcutRegistry(context: Context) {

    private val app = context.applicationContext

    private val manager: ShortcutManager?
        get() = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R)
            app.getSystemService(ShortcutManager::class.java) else null

    /**
     * The id a conversation is known by, derived from the space's OPAQUE local
     * key and never from its protocol id. A shortcut id is readable by the
     * launcher, so the tag's reason for being opaque applies here identically.
     */
    fun idFor(tag: String): String = ID_PREFIX + tag.removePrefix("space:")

    /**
     * Publishes or refreshes the conversation shortcut for a PREVIEW
     * rendering. Returns the id to attach to the notification, or null when
     * the platform is too old or the policy does not permit one — in which
     * case the caller falls back to a generic notification rather than
     * publishing something the policy has not allowed.
     */
    fun ensurePreviewShortcut(
        spaceId: String,
        tag: String,
        r: ConversationProjection.Rendering,
    ): String? {
        if (!r.publishShortcut) return null
        val sm = manager ?: return null
        val id = idFor(tag)
        val label = r.conversationTitle ?: GENERIC_LABEL
        return try {
            sm.pushDynamicShortcut(
                ShortcutInfo.Builder(app, id)
                    .setLongLived(true)
                    .setShortLabel(label)
                    .setLongLabel(label)
                    .setLocusId(LocusId(id))
                    .setIntent(openIntent(spaceId))
                    .also { b ->
                        // The Person is what makes this a CONVERSATION rather
                        // than a link; its key is the same opaque identity the
                        // notification uses, so the two never disagree about
                        // who is speaking.
                        r.lines.lastOrNull()?.let { line ->
                            b.setPerson(
                                android.app.Person.Builder()
                                    .setKey(line.personKey)
                                    .setName(line.senderLabel.ifEmpty { label })
                                    .build()
                            )
                        }
                    }
                    .build()
            )
            id
        } catch (t: Throwable) {
            // Shortcut publication is rate limited and can simply refuse. A
            // conversation notification without a shortcut is not a
            // conversation, so the caller must know rather than post something
            // half-configured.
            Log.w(TAG, "could not publish the conversation shortcut", t)
            null
        }
    }

    /**
     * Takes a conversation's system identity back, in the only order that is
     * safe when something may be pinned. See the class comment.
     */
    fun teardownConversationSurface(spaceId: String, tag: String) {
        val sm = manager ?: return
        val id = idFor(tag)
        try {
            // 1 — sanitise. Whatever survives the next two steps is now
            // generic: no space name, no sender, no Person.
            sm.updateShortcuts(
                listOf(
                    ShortcutInfo.Builder(app, id)
                        .setShortLabel(GENERIC_LABEL)
                        .setLongLabel(GENERIC_LABEL)
                        .setIntent(openIntent(spaceId))
                        .build()
                )
            )
        } catch (t: Throwable) {
            Log.w(TAG, "could not sanitise the shortcut before removing it", t)
        }
        try {
            // 2 — remove the copies the app owns.
            sm.removeLongLivedShortcuts(listOf(id))
        } catch (t: Throwable) {
            Log.w(TAG, "could not remove the long-lived shortcut", t)
        }
        try {
            // 3 — anything the person pinned cannot be removed by us. It is
            // disabled with a neutral sentence: naming the space here would
            // put back exactly what step one took away.
            val pinned = sm.pinnedShortcuts.any { it.id == id }
            if (pinned) sm.disableShortcuts(listOf(id), DISABLED_MESSAGE)
        } catch (t: Throwable) {
            Log.w(TAG, "could not disable the pinned shortcut", t)
        }
    }

    /**
     * Every conversation surface this app has published, retired at once.
     *
     * TIGHTENING HAS TO REACH THE ONES NOBODY IS LOOKING AT. Taking the
     * surfaces back space by space only covers conversations that still have a
     * live notification — and a conversation that was READ has none. Its
     * shortcut is long-lived by design, so it sat in the system's shortcut
     * store carrying the space's name, visible in the share sheet and to
     * Direct Share, after a person had asked for nothing to be shown at all.
     *
     * Found by the visual gate: after PREVIEW → HIDDEN, a sentinel space name
     * was still in `dumpsys shortcut`, on a conversation whose notification
     * had been taken down twenty seconds earlier.
     *
     * The intents are LEFT ALONE rather than rebuilt: an id encodes the space
     * it opens and this method does not know which space each one was for.
     * Sanitising the labels is what the privacy choice is about; a surviving
     * pinned shortcut that still opens its own conversation is correct.
     */
    fun retireEveryConversationSurface() {
        val sm = manager ?: return
        val ids = try {
            (sm.dynamicShortcuts + sm.pinnedShortcuts)
                .map { it.id }
                .filter { it.startsWith(ID_PREFIX) }
                .distinct()
        } catch (t: Throwable) {
            Log.w(TAG, "could not list the published shortcuts", t)
            return
        }
        if (ids.isEmpty()) return
        try {
            sm.updateShortcuts(
                ids.map {
                    ShortcutInfo.Builder(app, it)
                        .setShortLabel(GENERIC_LABEL)
                        .setLongLabel(GENERIC_LABEL)
                        .build()
                }
            )
        } catch (t: Throwable) {
            Log.w(TAG, "could not sanitise the published shortcuts", t)
        }
        try {
            sm.removeLongLivedShortcuts(ids)
        } catch (t: Throwable) {
            Log.w(TAG, "could not remove the published shortcuts", t)
        }
        try {
            val pinned = sm.pinnedShortcuts.map { it.id }.filter { it in ids }
            if (pinned.isNotEmpty()) sm.disableShortcuts(pinned, DISABLED_MESSAGE)
        } catch (t: Throwable) {
            Log.w(TAG, "could not disable the pinned shortcuts", t)
        }
    }

    /**
     * What the system currently holds for this conversation, across all three
     * surfaces. For tests and for a diagnostic line — a teardown that only
     * cleared the surface it happened to know about would look identical to
     * one that worked.
     */
    fun inspectPublishedState(tag: String): Map<String, Any> {
        val sm = manager ?: return mapOf("available" to false)
        val id = idFor(tag)
        return try {
            val dynamic = sm.dynamicShortcuts.filter { it.id == id }
            val pinned = sm.pinnedShortcuts.filter { it.id == id }
            mapOf(
                "available" to true,
                "dynamic" to dynamic.size,
                "pinned" to pinned.size,
                "labels" to (dynamic + pinned).map { it.shortLabel?.toString() ?: "" },
                "enabled" to (dynamic + pinned).all { it.isEnabled },
            )
        } catch (t: Throwable) {
            mapOf("available" to false, "error" to (t.message ?: "unknown"))
        }
    }

    private fun openIntent(spaceId: String) =
        Intent(app, QuietActivity::class.java)
            .setAction(Intent.ACTION_VIEW)
            .putExtra(QuietActivity.EXTRA_SPACE, spaceId)

    companion object {
        private const val TAG = "quiet-notify"

        /** No space name, no sender: what a sanitised surface is allowed to say. */
        /** Every conversation shortcut this app publishes starts with it. */
        const val ID_PREFIX = "conversation:"

        const val GENERIC_LABEL = "Quiet"

        /** Neutral on purpose — see step three. */
        const val DISABLED_MESSAGE = "This shortcut is no longer available"
    }
}
