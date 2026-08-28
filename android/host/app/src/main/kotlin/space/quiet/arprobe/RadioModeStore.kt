package space.quiet.arprobe

import android.content.Context

/**
 * SR-0 — Radio Mode is a DEVICE-LOCAL, PER-SPACE preference (groom D3):
 * it never enters the append-only log, never syncs to anybody, and one
 * member using a space as a radio changes nothing for the others.
 *
 * SharedPreferences rather than a table beside NotificationDb on purpose:
 * these are a few booleans per space with no aggregation and no
 * crash-ordering contract, and the notification DB's migration ceremony
 * buys nothing here. Single-writer commit(), the KEY_POLICY pattern.
 */
internal class RadioModeStore(context: Context) {
    private val prefs = context.getSharedPreferences("quiet-radio", Context.MODE_PRIVATE)

    fun enabled(spaceId: String): Boolean = prefs.getBoolean(key(spaceId, "enabled"), false)
    fun autoplay(spaceId: String): Boolean = prefs.getBoolean(key(spaceId, "autoplay"), true)

    fun setMode(spaceId: String, enabled: Boolean, autoplay: Boolean) {
        prefs.edit()
            .putBoolean(key(spaceId, "enabled"), enabled)
            .putBoolean(key(spaceId, "autoplay"), autoplay)
            .commit()
    }

    /** Background listening is one device-wide switch (groom §7). */
    fun backgroundListen(): Boolean = prefs.getBoolean("radio.background.listen", true)
    fun setBackgroundListen(on: Boolean) {
        prefs.edit().putBoolean("radio.background.listen", on).commit()
    }

    fun anyEnabled(): Boolean = prefs.all.any { (k, v) -> k.endsWith(".enabled") && v == true }

    /** SD-0 parity: a forgotten space leaves no orphaned keys behind. */
    fun forgetSpace(spaceId: String) {
        val e = prefs.edit()
        for (k in prefs.all.keys) if (k.startsWith("radio.$spaceId.")) e.remove(k)
        e.commit()
    }

    private fun key(spaceId: String, leaf: String) = "radio.$spaceId.$leaf"
}
