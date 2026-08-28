package space.quiet.arprobe

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.util.Log
import java.util.UUID

/**
 * EN-3 — the UnifiedPush half of the doorbell, hand-rolled.
 *
 * UnifiedPush is small enough to speak without a library, and this project
 * has already paid twice for dependencies that did less than they weighed:
 * registration is ONE broadcast to a distributor app, and everything the
 * distributor ever says back arrives as broadcasts to [UnifiedPushReceiver]
 * below. What we do with the endpoint is the part that matters, and it is
 * three lines: hand it to the core, which hands it to the relays, which
 * POST a contentless ping there when mail arrives and no parked connection
 * is left to hear it.
 *
 * WHAT A PING WAKES. The receiver runs with the app process possibly dead.
 * If the runtime can open itself (the remembered-passphrase posture that
 * "stay connected" already relies on), it is started and the sync kicked —
 * the ping carried nothing, the drain fetches everything. If the node is
 * locked at rest, honesty over cleverness: a quiet local notification says
 * something is waiting, names nothing, and the person opens the app.
 *
 * NO DISTRIBUTOR IS AN ANSWER, NOT AN ERROR: the switch reports "nothing
 * on this phone can carry the doorbell", and the parked connection plus
 * the background poll remain exactly as good as they were.
 */
object UnifiedPushConnector {
    private const val TAG = "quiet-up"
    private const val PREFS = "quiet-unifiedpush"
    private const val KEY_TOKEN = "token"
    private const val KEY_ENDPOINT = "endpoint"
    private const val KEY_DISTRIBUTOR = "distributor"
    private const val KEY_WANTED = "wanted"

    const val ACTION_REGISTER = "org.unifiedpush.android.distributor.REGISTER"
    const val ACTION_UNREGISTER = "org.unifiedpush.android.distributor.UNREGISTER"
    const val ACTION_NEW_ENDPOINT = "org.unifiedpush.android.connector.NEW_ENDPOINT"
    const val ACTION_MESSAGE = "org.unifiedpush.android.connector.MESSAGE"
    const val ACTION_UNREGISTERED = "org.unifiedpush.android.connector.UNREGISTERED"
    const val ACTION_REGISTRATION_FAILED = "org.unifiedpush.android.connector.REGISTRATION_FAILED"

    private fun prefs(ctx: Context) = ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    /** Distributor apps present on this phone, by package name. */
    fun distributors(ctx: Context): List<String> {
        val intent = Intent(ACTION_REGISTER)
        return ctx.packageManager.queryBroadcastReceivers(intent, 0)
            .mapNotNull { it.activityInfo?.packageName }
            .distinct()
    }

    /**
     * Turn the doorbell on: pick a distributor (the first one — a phone
     * with several is a phone whose owner knows how to have opinions, and
     * a chooser can come when somebody asks) and request registration.
     * The endpoint arrives later as a broadcast; [wanted] survives the
     * wait so the answer knows it is still welcome.
     */
    fun register(ctx: Context): Boolean {
        val dist = distributors(ctx).firstOrNull() ?: return false
        val token = prefs(ctx).getString(KEY_TOKEN, null)
            ?: UUID.randomUUID().toString().also {
                prefs(ctx).edit().putString(KEY_TOKEN, it).apply()
            }
        prefs(ctx).edit()
            .putBoolean(KEY_WANTED, true)
            .putString(KEY_DISTRIBUTOR, dist)
            .apply()
        val i = Intent(ACTION_REGISTER).apply {
            `package` = dist
            putExtra("token", token)
            putExtra("application", ctx.packageName)
        }
        ctx.sendBroadcast(i)
        return true
    }

    /** Turn the doorbell off: tell the distributor, clear the core. */
    fun unregister(ctx: Context) {
        val p = prefs(ctx)
        p.edit().putBoolean(KEY_WANTED, false).remove(KEY_ENDPOINT).apply()
        val dist = p.getString(KEY_DISTRIBUTOR, null)
        val token = p.getString(KEY_TOKEN, null)
        if (dist != null && token != null) {
            ctx.sendBroadcast(Intent(ACTION_UNREGISTER).apply {
                `package` = dist
                putExtra("token", token)
            })
        }
        pushEndpointToCore("")
    }

    /** The switch's current truth, for the settings screen. */
    fun status(ctx: Context): String {
        val p = prefs(ctx)
        return when {
            !p.getBoolean(KEY_WANTED, false) -> "off"
            p.getString(KEY_ENDPOINT, "").isNullOrEmpty() -> "registering"
            else -> "on"
        }
    }

    fun hasDistributor(ctx: Context): Boolean = distributors(ctx).isNotEmpty()

    internal fun onNewEndpoint(ctx: Context, endpoint: String) {
        val p = prefs(ctx)
        if (!p.getBoolean(KEY_WANTED, false)) return // an answer to a question we withdrew
        p.edit().putString(KEY_ENDPOINT, endpoint).apply()
        pushEndpointToCore(endpoint)
    }

    internal fun onUnregistered(ctx: Context) {
        prefs(ctx).edit().remove(KEY_ENDPOINT).apply()
        pushEndpointToCore("")
    }

    /** Replays the stored endpoint into a freshly opened core, so a
     * registration made while the node was locked is not lost. */
    fun replay(ctx: Context) {
        val p = prefs(ctx)
        if (p.getBoolean(KEY_WANTED, false)) {
            val ep = p.getString(KEY_ENDPOINT, "") ?: ""
            if (ep.isNotEmpty()) pushEndpointToCore(ep)
        }
    }

    private fun pushEndpointToCore(endpoint: String) {
        try {
            space.quiet.quietcore.Quietcore.setPushEndpoint(endpoint)
        } catch (t: Throwable) {
            // The core is not running or refused the URL; the stored copy
            // replays on the next open (see replay), and a malformed
            // endpoint from a distributor is a log line, not a crash.
            Log.w(TAG, "push endpoint not delivered to core", t)
        }
    }
}

/**
 * The distributor's side of the conversation arrives here — including on a
 * DEAD process, which is the entire reason the doorbell exists.
 */
class UnifiedPushReceiver : BroadcastReceiver() {
    override fun onReceive(ctx: Context, intent: Intent) {
        when (intent.action) {
            UnifiedPushConnector.ACTION_NEW_ENDPOINT -> {
                val ep = intent.getStringExtra("endpoint") ?: return
                UnifiedPushConnector.onNewEndpoint(ctx, ep)
            }
            UnifiedPushConnector.ACTION_MESSAGE -> {
                // The ping carries nothing; the drain fetches everything.
                // ensureStarted opens with the remembered passphrase where
                // that posture is on; a locked node stays locked and the
                // person gets a nameless nudge instead.
                val controller = RuntimeController.get(ctx)
                if (controller.isAlive()) {
                    try {
                        space.quiet.quietcore.Quietcore.kickSync()
                    } catch (t: Throwable) {
                        Log.w("quiet-up", "kick failed", t)
                    }
                } else {
                    controller.wakeForDoorbell()
                }
            }
            UnifiedPushConnector.ACTION_UNREGISTERED -> {
                UnifiedPushConnector.onUnregistered(ctx)
            }
            UnifiedPushConnector.ACTION_REGISTRATION_FAILED -> {
                Log.w("quiet-up", "distributor refused registration")
            }
        }
    }
}
