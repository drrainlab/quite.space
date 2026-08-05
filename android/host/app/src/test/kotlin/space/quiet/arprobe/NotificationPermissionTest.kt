package space.quiet.arprobe

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * AR-1b.3's permission state, on the JVM.
 *
 * Two of these combinations are the reason this is a state machine and not a
 * boolean, and neither can be produced on a phone on demand: an API level
 * below 33, and notifications switched off for the app while the runtime
 * permission is still granted. A device test would quietly skip both.
 */
class NotificationPermissionTest {

    private fun state(
        sdk: Int = 35,
        granted: Boolean = false,
        on: Boolean = true,
        asked: Boolean = false,
    ) = notificationPermissionState(sdk, granted, on, asked)

    @Test
    fun belowThirteenNothingIsAskedAndNothingNeedsTo() {
        assertEquals(NotificationPermissionState.NOT_REQUIRED, state(sdk = 32, granted = false))
        assertTrue(state(sdk = 32).canPresent)
        assertFalse("there is no dialog to show", state(sdk = 32).canAsk)
    }

    @Test
    fun beforeAskingTheStateIsNotADenial() {
        val s = state(sdk = 35, granted = false, asked = false)
        assertEquals(NotificationPermissionState.NOT_REQUESTED, s)
        assertTrue(s.canAsk)
        assertFalse(s.canPresent)
    }

    @Test
    fun aRefusalIsRememberedByUsBecauseTheSystemWillNotAskAgain() {
        // On 33+ a second request after a refusal is silently ignored: no
        // dialog appears and the app concludes it was refused again. So the
        // refusal has to be OUR fact, and the recovery has to be settings.
        val s = state(sdk = 35, granted = false, asked = true)
        assertEquals(NotificationPermissionState.DENIED, s)
        assertFalse(s.canAsk)
        assertTrue(s.needsSystemSettings)
    }

    @Test
    fun grantedIsGranted() {
        val s = state(sdk = 35, granted = true, asked = true)
        assertEquals(NotificationPermissionState.GRANTED, s)
        assertTrue(s.canPresent)
        assertFalse(s.needsSystemSettings)
    }

    @Test
    fun notificationsOffForTheAppOutranksAGrantedPermission() {
        // The combination a boolean would get wrong: the permission is held,
        // and the app still cannot show anything, because the person switched
        // notifications off in system settings. Reporting GRANTED here would
        // leave them with no route back.
        val s = state(sdk = 35, granted = true, on = false, asked = true)
        assertEquals(NotificationPermissionState.DISABLED_IN_SYSTEM, s)
        assertFalse(s.canPresent)
        assertTrue(s.needsSystemSettings)
    }

    @Test
    fun notificationsOffBelowThirteenIsADecisionNotADefault() {
        val s = state(sdk = 32, granted = false, on = false)
        assertEquals(NotificationPermissionState.DISABLED_IN_SYSTEM, s)
    }

    @Test
    fun offBeforeWeEverAskedOnThirteenIsJustThePreGrantDefault() {
        // On 33+ an app that has not been granted the permission also reports
        // notifications as disabled. Calling that DISABLED_IN_SYSTEM would
        // send a first-run person to a settings page instead of showing them
        // the request.
        val s = state(sdk = 35, granted = false, on = false, asked = false)
        assertEquals(NotificationPermissionState.NOT_REQUESTED, s)
        assertTrue(s.canAsk)
    }
}
