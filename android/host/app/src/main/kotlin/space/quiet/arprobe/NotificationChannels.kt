package space.quiet.arprobe

import android.app.NotificationChannel
import android.app.NotificationManager
import android.content.Context
import android.media.AudioAttributes
import android.net.Uri
import android.os.Build

/**
 * AR-1b.3 — the two channels, created once at Application scope.
 *
 * WHY AT APPLICATION SCOPE AND NOT WHERE THE FIRST NOTIFICATION IS POSTED.
 * From API 26 a notification without a channel is not shown at all, and the
 * first notification this app ever posts may well be produced while no screen
 * exists — a candidate arrives from a Go goroutine, not from a tap. Creating
 * channels is idempotent and cheap, so it happens where it cannot be missed.
 *
 * IDS AND INITIAL SEMANTICS ARE EFFECTIVELY FROZEN. After creation the app
 * cannot raise or lower a channel's importance: that control belongs to the
 * person, permanently, and an app that wants a different one has to create a
 * NEW channel — which arrives in their settings as a second row they never
 * asked for. So the ids are chosen once and the initial importance is a
 * proposal, not a setting we can revise later.
 *
 * CHANNELS, NOT ONE PER SPACE. A channel per conversation would hand a person
 * an ever-growing settings list they did not build and cannot prune, and it
 * would leak the names of their spaces into system UI. Conversations are
 * expressed as Android CONVERSATIONS instead — a shortcut plus a tag — which
 * is AR-1b.3's `MessagingStyle` work and needs no channel of its own.
 *
 * `quite.signals` exists with nothing routed to it yet, and that is
 * deliberate rather than an oversight: the candidate carries no way to tell a
 * mention or a reply from an ordinary message — that is QuietRank's hard rule,
 * and it lives in the core. Creating the channel now means the person's
 * choices about it survive the release that starts using it; creating it later
 * would silently reset nothing, but would also mean the first signal arrives
 * on a channel they have never seen. See NotificationPolicy.
 */
internal object NotificationChannels {

    /**
     * The Quiet Chimes voice, rendered by scripts/sound/render-chimes.cjs
     * from the SAME chime-dsp.js the web app plays — one model, two mouths.
     *
     * IMPORTANCE IS PLATFORM DELIVERY SEMANTICS (heads-up behaviour, the
     * person's own vibration settings) — it is NOT part of the sonic
     * hierarchy. The voice's anti-escalation law lives inside the WAVs:
     * every tier shares one peak budget, and a personal signal is richer
     * and longer than an ordinary one, never louder. After creation the
     * person owns each channel's actual volume and vibration in system
     * settings, which is exactly where that power belongs.
     */
    private fun sound(context: Context, res: Int): Pair<Uri, AudioAttributes> =
        Uri.parse("android.resource://${context.packageName}/$res") to
            AudioAttributes.Builder()
                .setUsage(AudioAttributes.USAGE_NOTIFICATION)
                .setContentType(AudioAttributes.CONTENT_TYPE_SONIFICATION)
                .build()

    fun ensure(context: Context) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return
        val nm = context.getSystemService(NotificationManager::class.java) ?: return

        // The v1 rows carried the system default sound; their ids are frozen,
        // so the voice arrives as a new generation and the old rows go —
        // deleting is the sanctioned migration, and it keeps the person's
        // settings list at exactly one row per meaning.
        nm.deleteNotificationChannel("quite.messages")
        nm.deleteNotificationChannel("quite.signals")

        nm.createNotificationChannel(
            NotificationChannel(
                NotificationPolicy.CHANNEL_MESSAGES,
                "Messages",
                NotificationManager.IMPORTANCE_DEFAULT,
            ).apply {
                description = "Someone wrote in a space you are in."
                setShowBadge(true)
                val (uri, attrs) = sound(context, R.raw.chime_message)
                setSound(uri, attrs)
            }
        )

        nm.createNotificationChannel(
            NotificationChannel(
                NotificationPolicy.CHANNEL_CONNECTION,
                "Staying connected",
                NotificationManager.IMPORTANCE_LOW,
            ).apply {
                description = "Shown while Quiet is deliberately holding a connection."
                setShowBadge(false)
                enableVibration(false)
                setSound(null, null)
            }
        )

        nm.createNotificationChannel(
            NotificationChannel(
                NotificationPolicy.CHANNEL_SIGNALS,
                "Signals",
                NotificationManager.IMPORTANCE_HIGH,
            ).apply {
                description = "Something the space itself flagged for you."
                setShowBadge(true)
                val (uri, attrs) = sound(context, R.raw.chime_signal)
                setSound(uri, attrs)
            }
        )

        nm.createNotificationChannel(
            NotificationChannel(
                NotificationPolicy.CHANNEL_SIGNALS_PERSONAL,
                "Personal signals",
                NotificationManager.IMPORTANCE_HIGH,
            ).apply {
                description = "Somebody called you by name."
                setShowBadge(true)
                val (uri, attrs) = sound(context, R.raw.chime_signal_personal)
                setSound(uri, attrs)
            }
        )
    }
}
