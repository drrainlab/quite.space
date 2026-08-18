package space.quiet.arprobe

import android.os.Build
import android.view.HapticFeedbackConstants
import android.view.View

/**
 * The keypad's sense of touch.
 *
 * WHY performHapticFeedback AND NOT Vibrator. The Vibrator API needs the
 * VIBRATE permission and, worse, ignores the person: it buzzes a phone whose
 * owner turned touch feedback off in Settings, and it buzzes at a strength
 * somebody else chose. performHapticFeedback asks the system for a NAMED
 * feeling and lets the system decide what that feels like on this handset —
 * so the phone that has been asked to stay still stays still, a manifest
 * permission is not needed for a nicety, and the taps match every other
 * keyboard the person already uses.
 *
 * FLAG_IGNORE_VIEW_SETTING IS DELIBERATELY NOT PASSED, for the same reason.
 *
 * The constants below are the ones Android added in API 30 for exactly this
 * shape of interaction; on 24-29 the older KEYBOARD_TAP stands in, which is
 * the honest fallback rather than a silent one. Nothing here is load-bearing:
 * a device with no vibrator returns false and the code path is unchanged.
 */
internal object Haptics {

    /** A digit landed. The lightest thing the system has. */
    fun tap(v: View) {
        v.performHapticFeedback(HapticFeedbackConstants.KEYBOARD_TAP)
    }

    /** A digit taken back, or the field cleared. */
    fun erase(v: View) {
        v.performHapticFeedback(
            if (Build.VERSION.SDK_INT >= 30) HapticFeedbackConstants.CLOCK_TICK
            else HapticFeedbackConstants.KEYBOARD_TAP
        )
    }

    /** It opened. */
    fun accept(v: View) {
        v.performHapticFeedback(
            if (Build.VERSION.SDK_INT >= 30) HapticFeedbackConstants.CONFIRM
            else HapticFeedbackConstants.KEYBOARD_TAP
        )
    }

    /** It did not open, or the two codes were not the same. */
    fun refuse(v: View) {
        v.performHapticFeedback(
            if (Build.VERSION.SDK_INT >= 30) HapticFeedbackConstants.REJECT
            else HapticFeedbackConstants.LONG_PRESS
        )
    }
}
