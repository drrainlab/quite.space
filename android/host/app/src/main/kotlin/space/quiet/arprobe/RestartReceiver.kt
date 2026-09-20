package space.quiet.arprobe

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.util.Log

/**
 * AFTER A REBOOT AND AFTER AN UPDATE — the two moments a sticky service does
 * not survive. Measured on a handset: installing an update killed the process
 * and nothing came back; the phone heard nothing until somebody opened the
 * app, which after a night's auto-update is a morning of missed messages.
 *
 * It starts the availability service only when the person has "stay
 * connected" on. The service then does what it does after any restart by the
 * system: opens the node if it may, and otherwise WATCHES without a key
 * (AN-2). Nothing here touches a passphrase.
 *
 * BOOT_COMPLETED reaches an app that is not direct-boot aware only after the
 * first unlock, which is also when its private storage — where the watch
 * plan lives — becomes readable. remoteMessaging is a foreground-service type
 * Android still allows from these two broadcasts; if a platform refuses
 * anyway, the service records the refusal and the switch says who put it Off.
 */
class RestartReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent?) {
        when (intent?.action) {
            Intent.ACTION_BOOT_COMPLETED, Intent.ACTION_MY_PACKAGE_REPLACED -> Unit
            else -> return
        }
        try {
            val app = context.applicationContext
            if (!RuntimeController.get(app).availabilityRequested()) return
            AvailabilityService.startAfterRestart(app)
        } catch (t: Throwable) {
            Log.w("quiet-availability", "restart after ${intent.action}", t)
        }
    }
}
