package space.quiet.arprobe

import android.app.Notification
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import android.util.Log

/**
 * AR-1c.1 — "Stay connected": ON BY DEFAULT, AND NEVER INVISIBLE.
 *
 * This began as a mode a person had to turn on, because keeping a process
 * alive spends somebody's battery on a decision they did not make. The
 * default was reversed once it was clear what the other side of it costs:
 * media bytes never travel to the relay, so a phone with this off takes every
 * photo it has ever sent offline with it, and the person waiting sees a
 * picture that never arrives and no reason why. A default that quietly breaks
 * other people's screens is not the cautious one.
 *
 * WHAT SURVIVES THE REVERSAL IS THE VISIBILITY, and it is the part that made
 * the original argument right. The mode says so in a permanent notification
 * for as long as it runs, can be turned off from that notification without
 * opening the app, and sits first in its settings tab. On by default is not
 * the same as on by stealth.
 *
 * IT IS NOT A SECOND OWNER OF THE CORE. [RuntimeController] owns the runtime
 * at Application scope and always has; this service holds a LEASE on it. The
 * distinction is not bookkeeping: a service that opened its own node would be
 * a second writer to one data directory — the failure AR-0 spent a wave
 * proving is fatal — and a service that could close the core would take the
 * interface's node away while somebody was reading it.
 *
 * remoteMessaging, DELIBERATELY, NOT dataSync. Android 14 requires a
 * foreground service type; 15 caps background `dataSync` at six hours per
 * day, which for a mode whose entire purpose is "stay reachable" would mean
 * going quiet in the evening with nothing to show for it. remoteMessaging
 * describes carrying messages between devices, which is what this does, and
 * carries no such cap.
 *
 * WHAT IT DOES NOT CLAIM. A foreground service raises the process's standing;
 * it is not a promise that the CPU never sleeps and it is not a wake lock.
 * Whether messages actually arrive on a dark phone is measured on one, in the
 * AR-1c gate, rather than assumed from the fact that a service exists.
 */
class AvailabilityService : Service() {

    private var leased = false

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_STOP -> {
                // The person pressed the notification's own button. Turning it
                // off is the whole point of a mode being visible.
                stopMode()
                return START_NOT_STICKY
            }
        }

        val controller = RuntimeController.get(this)
        NotificationChannels.ensure(this)
        try {
            startForeground(
                NOTIFICATION_ID,
                buildNotification(controller),
                foregroundType(),
            )
        } catch (t: Throwable) {
            // A start refused by the platform — a background start, a missing
            // type, a permission — must not take the app down with it. The
            // mode does not come on, and the refusal is RECORDED rather than
            // only logged: the switch goes to Off either way, and without this
            // there is nothing to tell a person that the system did that and
            // not them.
            Log.w(TAG, "the availability mode could not start", t)
            controller.noteAvailabilityRefused()
            stopSelf()
            return START_NOT_STICKY
        }
        // It is running, so any earlier refusal is history.
        controller.clearAvailabilityRefused()

        if (!leased) {
            leased = true
            controller.acquireAvailabilityLease()
        }

        // START_STICKY, NOT REDELIVER: if Android kills the process under
        // memory pressure the mode is still what the person asked for, so it
        // comes back — but a FORCE-STOP does not, and must not. Android does
        // not restart a force-stopped app, which is the behaviour we want:
        // "stop" from a person means stop.
        return START_STICKY
    }

    override fun onDestroy() {
        if (leased) {
            leased = false
            RuntimeController.get(this).releaseAvailabilityLease()
        }
        super.onDestroy()
    }

    private fun stopMode() {
        RuntimeController.get(this).setAvailabilityRequested(false)
        if (leased) {
            leased = false
            RuntimeController.get(this).releaseAvailabilityLease()
        }
        stopForeground(STOP_FOREGROUND_REMOVE)
        stopSelf()
    }

    private fun foregroundType(): Int =
        if (Build.VERSION.SDK_INT >= 34) {
            ServiceInfo.FOREGROUND_SERVICE_TYPE_REMOTE_MESSAGING
        } else {
            0
        }

    /**
     * The permanent notification, and what it is allowed to say.
     *
     * NOTHING ABOUT ANY CONVERSATION. This one is visible for hours, on a
     * lock screen, to every notification listener — so it carries a state,
     * not a life: that the mode is on, and how to turn it off. Space names,
     * senders and counts of who wrote belong to the message notifications,
     * where a privacy policy already governs them.
     */
    private fun buildNotification(controller: RuntimeController): Notification {
        val stop = PendingIntent.getService(
            this, 0,
            Intent(this, AvailabilityService::class.java).setAction(ACTION_STOP),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        val open = PendingIntent.getActivity(
            this, 0,
            Intent(this, QuietActivity::class.java),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        return Notification.Builder(this, NotificationPolicy.CHANNEL_CONNECTION)
            .setSmallIcon(R.drawable.ic_stat_quiet)
            .setContentTitle(TITLE)
            .setContentText(AvailabilityText.summary(controller.availabilitySnapshot()))
            .setContentIntent(open)
            .setOngoing(true)
            .setShowWhen(false)
            // The one place a person can end it without hunting for the app.
            .addAction(Notification.Action.Builder(null, STOP_LABEL, stop).build())
            .build()
    }

    companion object {
        private const val TAG = "quiet-availability"

        /** Its own id: this notification is a mode, never a conversation. */
        const val NOTIFICATION_ID = 2

        const val ACTION_STOP = "space.quiet.arprobe.STOP_AVAILABILITY"

        const val TITLE = "Quiet is staying connected"
        const val STOP_LABEL = "Turn off"

        /**
         * Started from a VISIBLE Activity, after a person asked for it.
         * Android 12+ forbids starting a foreground service from the
         * background in the general case, and the restriction is the right
         * shape: a mode nobody switched on has no business existing.
         */
        @JvmStatic
        fun start(context: Context) {
            val i = Intent(context, AvailabilityService::class.java)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                context.startForegroundService(i)
            } else {
                context.startService(i)
            }
        }

        @JvmStatic
        fun stop(context: Context) {
            context.startService(
                Intent(context, AvailabilityService::class.java).setAction(ACTION_STOP)
            )
        }
    }
}

/**
 * What the permanent notification says, kept away from Android so it can be
 * tested as the sentence it is.
 *
 * TWO FACTS AND NO NAMES: how the device is reachable, and when it last heard
 * anything. "2 relays · just now" is a state; "3 messages from Anna" would be
 * a conversation, on a card that sits in the shade for hours.
 */
internal object AvailabilityText {

    data class State(
        val relays: Int,
        val secondsSinceEvent: Long,
        /** False while the core is closed — the mode is on, nothing is open. */
        val runtimeAlive: Boolean,
    )

    fun summary(s: State): String {
        if (!s.runtimeAlive) return "Waiting to be unlocked"
        val where = when (s.relays) {
            0 -> "No relay"
            1 -> "1 relay"
            else -> "${s.relays} relays"
        }
        return "$where · ${ago(s.secondsSinceEvent)}"
    }

    private fun ago(seconds: Long): String = when {
        seconds < 0 -> "nothing yet"
        seconds < 45 -> "just now"
        seconds < 3600 -> "${seconds / 60} min ago"
        seconds < 86400 -> "${seconds / 3600} h ago"
        else -> "${seconds / 86400} d ago"
    }
}
