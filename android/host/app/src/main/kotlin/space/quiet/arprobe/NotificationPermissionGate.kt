package space.quiet.arprobe

import android.Manifest
import android.app.NotificationManager
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.provider.Settings

/**
 * The Android-facing half of [notificationPermissionState]: it reads the three
 * facts the system knows and remembers the one only we can know.
 *
 * "We have asked at least once" has NO system API — deliberately, from
 * Android's side — and it is exactly the fact that separates "they have not
 * been asked" from "they said no". Without remembering it ourselves, an app on
 * 33+ re-requests after a refusal, the platform silently ignores the call, no
 * dialog appears, and the app concludes it was refused again. That loop is
 * invisible to the person and would make our own state machine lie.
 */
internal class NotificationPermissionGate(context: Context) {

    private val app = context.applicationContext
    private val prefs = app.getSharedPreferences(FILE, Context.MODE_PRIVATE)

    fun state(): NotificationPermissionState = notificationPermissionState(
        sdkInt = Build.VERSION.SDK_INT,
        permissionGranted = permissionGranted(),
        notificationsOn = notificationsOn(),
        alreadyAsked = prefs.getBoolean(KEY_ASKED, false),
    )

    /** Recorded BEFORE the system dialog returns: a person may dismiss it. */
    fun markAsked() {
        prefs.edit().putBoolean(KEY_ASKED, true).commit()
    }

    private fun permissionGranted(): Boolean =
        Build.VERSION.SDK_INT < 33 ||
            app.checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) ==
            PackageManager.PERMISSION_GRANTED

    private fun notificationsOn(): Boolean =
        app.getSystemService(NotificationManager::class.java)
            ?.areNotificationsEnabled() ?: true

    /**
     * The only honest recovery from DENIED or DISABLED_IN_SYSTEM: our dialog
     * cannot come back, so the person is taken to the page that can.
     */
    fun systemSettingsIntent(): Intent =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS)
                .putExtra(Settings.EXTRA_APP_PACKAGE, app.packageName)
        } else {
            Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS)
                .setData(Uri.fromParts("package", app.packageName, null))
        }

    private companion object {
        const val FILE = "quiet-notifications"      // shared with the id store
        const val KEY_ASKED = "permission_asked"
    }
}
