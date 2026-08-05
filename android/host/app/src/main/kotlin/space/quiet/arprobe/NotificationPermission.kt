package space.quiet.arprobe

/**
 * AR-1b.3 — what this app is allowed to show, as five states rather than a
 * boolean.
 *
 * A boolean would collapse four different situations that need four different
 * answers from the product, and three of them are not failures:
 *
 * ```
 *   NOT_REQUIRED         below API 33 — nothing was ever asked, and nothing
 *                        needs to be. Showing an "enable notifications" action
 *                        here would offer a person a switch that does nothing.
 *   NOT_REQUESTED        we have not asked yet. The right moment is a product
 *                        moment, not onCreate.
 *   GRANTED              allowed.
 *   DENIED               they said no, ONCE. The app keeps working, we stop
 *                        asking, and the offer moves into settings where they
 *                        can take it up when they want it.
 *   DISABLED_IN_SYSTEM   notifications are off for the app as a whole — which
 *                        a person can do without ever seeing our dialog, and
 *                        which no in-app request can undo. The only honest
 *                        action is to open the system settings page.
 * ```
 *
 * DENIED AND DISABLED_IN_SYSTEM ARE DIFFERENT, and telling them apart is the
 * whole reason this is not a boolean: on Android 13+ a second request after a
 * refusal is silently ignored, so an app that keeps calling the system dialog
 * shows the person nothing and concludes they refused again. The refusal has
 * to be remembered by US, and the recovery has to be a route into settings.
 *
 * A REFUSAL IS AN ORDINARY STATE. Quiet works exactly as before without
 * notifications — the node still syncs, the log is still the truth, and the
 * only thing missing is the tap on the shoulder. The core is told to stop
 * producing candidates nobody can render (see `Runtime.ArmNotifications(nil)`),
 * which is cheaper than producing them and dropping them silently.
 */
enum class NotificationPermissionState {
    NOT_REQUIRED,
    NOT_REQUESTED,
    GRANTED,
    DENIED,
    DISABLED_IN_SYSTEM;

    /** Whether the coordinator should present anything at all. */
    val canPresent: Boolean
        get() = this == NOT_REQUIRED || this == GRANTED

    /** Whether asking the SYSTEM dialog can still produce a dialog. */
    val canAsk: Boolean
        get() = this == NOT_REQUESTED

    /** Whether the only remaining route is the system settings page. */
    val needsSystemSettings: Boolean
        get() = this == DENIED || this == DISABLED_IN_SYSTEM
}

/**
 * The state machine, as a pure function of four facts, so it can be tested
 * against every combination without a device — including the ones a phone in
 * hand will not reproduce on demand (below API 33; notifications switched off
 * for the app while the permission itself is granted).
 *
 * @param sdkInt              the device's API level
 * @param permissionGranted   POST_NOTIFICATIONS is held (meaningless below 33)
 * @param notificationsOn     the app's notifications are enabled as a whole
 * @param alreadyAsked        we have shown the system dialog at least once
 */
fun notificationPermissionState(
    sdkInt: Int,
    permissionGranted: Boolean,
    notificationsOn: Boolean,
    alreadyAsked: Boolean,
): NotificationPermissionState {
    // Checked FIRST, above the API level: a person can switch an app's
    // notifications off in system settings on any Android, and on 33+ they can
    // do it while the runtime permission stays granted. Reading the permission
    // alone would report GRANTED for an app that cannot show anything, and the
    // product would offer no way out.
    if (!notificationsOn) {
        return if (sdkInt < 33 || permissionGranted || alreadyAsked) {
            NotificationPermissionState.DISABLED_IN_SYSTEM
        } else {
            // Below the dialog, "off" on 33+ before we ever asked is simply
            // the pre-grant default, not a decision the person made.
            NotificationPermissionState.NOT_REQUESTED
        }
    }
    if (sdkInt < 33) return NotificationPermissionState.NOT_REQUIRED
    if (permissionGranted) return NotificationPermissionState.GRANTED
    return if (alreadyAsked) {
        NotificationPermissionState.DENIED
    } else {
        NotificationPermissionState.NOT_REQUESTED
    }
}
