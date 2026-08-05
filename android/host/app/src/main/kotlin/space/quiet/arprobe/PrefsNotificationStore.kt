package space.quiet.arprobe

import android.content.Context
import android.content.SharedPreferences

/**
 * The durable half of [NotificationCoordinator], and the only file in the pair
 * that knows what Android is.
 *
 * `commit()`, NOT `apply()`. The whole point of this store is to survive a
 * process Android killed without warning — `apply()` returns before the write
 * reaches disk, which is exactly the window the low-memory killer lives in.
 * The write is a couple of kilobytes on the notification path, not the message
 * path, so paying for it synchronously costs nothing a person can perceive.
 *
 * Ids are stored as one newline-joined string rather than a `StringSet`: a set
 * has no order, and the bound here is FIFO — the OLDEST id is the one that
 * must go. A set would have made "bounded" mean "arbitrary".
 */
internal class PrefsNotificationStore(context: Context) : NotificationCoordinator.Store {

    private val prefs: SharedPreferences = context.applicationContext
        .getSharedPreferences(FILE, Context.MODE_PRIVATE)

    override fun rememberedIds(): List<String> {
        val raw = prefs.getString(KEY_PRESENTED, "").orEmpty()
        return if (raw.isEmpty()) emptyList() else raw.split("\n")
    }

    /**
     * Created once per space and never derived from the space id — a derived
     * key would be as much of a giveaway as the id itself to anything reading
     * tags. 64 random bits: enough that two spaces on one device will not
     * collide, short enough to stay a tag.
     */
    override fun notificationKey(spaceId: String): String {
        val stored = prefs.getString(KEY_TAG_PREFIX + spaceId, null)
        if (stored != null) return stored
        val bytes = ByteArray(8).also { java.security.SecureRandom().nextBytes(it) }
        val key = bytes.joinToString("") { "%02x".format(it) }
        prefs.edit().putString(KEY_TAG_PREFIX + spaceId, key).commit()
        return key
    }

    override fun writeRememberedIds(ids: List<String>) {
        prefs.edit().putString(KEY_PRESENTED, ids.joinToString("\n")).commit()
    }

    private companion object {
        const val FILE = "quiet-notifications"
        const val KEY_PRESENTED = "presented_ids"
        const val KEY_TAG_PREFIX = "tag:"
    }
}
