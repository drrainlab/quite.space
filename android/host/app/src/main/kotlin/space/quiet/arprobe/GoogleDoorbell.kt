package space.quiet.arprobe

import android.content.Context
import android.util.Log
import com.google.firebase.FirebaseApp
import com.google.firebase.messaging.FirebaseMessaging
import com.google.firebase.messaging.FirebaseMessagingService
import com.google.firebase.messaging.RemoteMessage

/**
 * EN-4 — the doorbell carried by Google (Firebase Cloud Messaging).
 *
 * WHY A SECOND CARRIER. The parked relay connection covers every minute
 * the phone lets it: Doze cuts an app's network within minutes of the
 * screen going dark and closes nothing, so the park looks alive and hears
 * nothing. A tester's phone (2026-09-22) received nothing after locking and
 * everything the moment the app was opened. The one channel Android keeps
 * open through Doze is the platform's own push lane; on phones with Google
 * services that is FCM, and most phones are those. UnifiedPush stays the
 * carrier for phones without Google — [UnifiedPushConnector] — and wins
 * when a distributor is installed: it is the person's own choice of
 * carrier, and it tells Google nothing.
 *
 * WHAT GOOGLE LEARNS, said where the switch is: that this installation had
 * something to check, and when. The relay POSTs a fixed marker to
 * push.quite.space, which forwards a fixed marker to FCM (cmd/quiet-push);
 * no hint, no sender, no space, no count exists anywhere on that path.
 *
 * ON BY DEFAULT, by the owner's decision, made against the measured
 * alternative: a messenger that is silent in a pocket. It is a switch in
 * Settings → This device, with that sentence printed beside it.
 *
 * THIS IS THE ONE GOOGLE DEPENDENCY IN THE HOST, and it is here for this
 * reason alone; nothing else may reach for it.
 */
object GoogleDoorbell {
    private const val TAG = "quiet-fcm"
    private const val PREFS = "quiet-google-doorbell"
    private const val KEY_WANTED = "wanted"
    private const val KEY_DECIDED = "decided"
    private const val KEY_ENDPOINT = "endpoint"

    /** Where the relay's ping lands; the token completes the URL. */
    const val GATEWAY = "https://push.quite.space/fcm/"

    private fun prefs(ctx: Context) = ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    /**
     * Whether this phone can carry it at all: Google services installed,
     * and this build configured for a Firebase project (google-services.json
     * present at build time; without it FirebaseApp never initialises and
     * the row says so honestly instead of a token fetch failing later).
     */
    fun available(ctx: Context): Boolean = try {
        ctx.packageManager.getPackageInfo("com.google.android.gms", 0)
        FirebaseApp.getApps(ctx).isNotEmpty()
    } catch (t: Throwable) {
        false
    }

    /** "off" | "registering" | "on_google" */
    fun status(ctx: Context): String {
        val p = prefs(ctx)
        return when {
            !p.getBoolean(KEY_WANTED, false) -> "off"
            p.getString(KEY_ENDPOINT, "").isNullOrEmpty() -> "registering"
            else -> "on_google"
        }
    }

    fun register(ctx: Context): Boolean {
        if (!available(ctx)) return false
        prefs(ctx).edit().putBoolean(KEY_WANTED, true).putBoolean(KEY_DECIDED, true).apply()
        try {
            val fm = FirebaseMessaging.getInstance()
            fm.isAutoInitEnabled = true
            fm.token
                .addOnSuccessListener { tok -> onToken(ctx, tok) }
                .addOnFailureListener { e -> Log.w(TAG, "no registration token", e) }
        } catch (t: Throwable) {
            Log.w(TAG, "registration could not start", t)
            return false
        }
        return true
    }

    fun unregister(ctx: Context) {
        prefs(ctx).edit().putBoolean(KEY_WANTED, false).putBoolean(KEY_DECIDED, true)
            .remove(KEY_ENDPOINT).apply()
        try {
            val fm = FirebaseMessaging.getInstance()
            fm.isAutoInitEnabled = false
            fm.deleteToken()
        } catch (t: Throwable) {
            Log.w(TAG, "deleteToken", t)
        }
        UnifiedPushConnector.pushEndpointToCore("")
    }

    /**
     * The default: on, once, where it can run — unless the person has
     * already decided either way. Called at application scope so a phone
     * whose owner never opens the settings still gets it.
     */
    fun ensureDefault(ctx: Context) {
        if (prefs(ctx).getBoolean(KEY_DECIDED, false)) return
        if (!available(ctx)) return
        if (UnifiedPushConnector.hasDistributor(ctx)) return // theirs to choose
        register(ctx)
    }

    internal fun onToken(ctx: Context, token: String) {
        if (!prefs(ctx).getBoolean(KEY_WANTED, false)) return
        if (token.isEmpty()) return
        val endpoint = GATEWAY + token
        prefs(ctx).edit().putString(KEY_ENDPOINT, endpoint).apply()
        UnifiedPushConnector.pushEndpointToCore(endpoint)
    }

    /** A freshly opened core learns the standing endpoint (see UnifiedPushConnector.replay). */
    fun replay(ctx: Context) {
        val p = prefs(ctx)
        if (!p.getBoolean(KEY_WANTED, false)) return
        val ep = p.getString(KEY_ENDPOINT, "") ?: ""
        if (ep.isNotEmpty()) UnifiedPushConnector.pushEndpointToCore(ep)
    }
}

/**
 * Which carrier the doorbell rides on this phone, and the three verbs the
 * settings row speaks. UnifiedPush first when a distributor is installed;
 * Google otherwise; nothing when neither can.
 */
object Doorbell {
    fun status(ctx: Context): String = when {
        UnifiedPushConnector.hasDistributor(ctx) -> UnifiedPushConnector.status(ctx)
        GoogleDoorbell.available(ctx) -> GoogleDoorbell.status(ctx)
        else -> "no_distributor"
    }

    fun on(ctx: Context): Boolean = when {
        UnifiedPushConnector.hasDistributor(ctx) -> UnifiedPushConnector.register(ctx)
        else -> GoogleDoorbell.register(ctx)
    }

    fun off(ctx: Context) {
        UnifiedPushConnector.unregister(ctx)
        GoogleDoorbell.unregister(ctx)
    }

    fun replay(ctx: Context) {
        UnifiedPushConnector.replay(ctx)
        GoogleDoorbell.replay(ctx)
    }

    /**
     * THE RING, whichever carrier brought it. The ping carries nothing; the
     * node collects everything. An open node is kicked (the core's kick is
     * the mail-first pull, see quietcore.KickSync); a closed one opens with
     * the remembered passphrase where that posture is on, or shows the
     * nameless nudge.
     */
    fun ring(ctx: Context) {
        val controller = RuntimeController.get(ctx)
        if (controller.isAlive()) {
            try {
                space.quiet.quietcore.Quietcore.kickSync()
            } catch (t: Throwable) {
                Log.w("quiet-doorbell", "kick failed", t)
            }
        } else {
            controller.wakeForDoorbell()
        }
    }
}

/** Google's side of the conversation: a new token, or the ring itself. */
class FcmDoorbell : FirebaseMessagingService() {
    override fun onNewToken(token: String) {
        GoogleDoorbell.onToken(applicationContext, token)
    }

    override fun onMessageReceived(message: RemoteMessage) {
        // Whatever rode along is not read: the ring is the message.
        Doorbell.ring(applicationContext)
    }
}
