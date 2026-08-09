package space.quiet.arprobe

import android.app.Application
import space.quiet.quietcore.Quietcore

/**
 * Where the runtime's owner lives.
 *
 * The Application object outlives every Activity, every WebView and every
 * configuration change, which is exactly the lifetime a node needs and exactly
 * the lifetime an Activity does not have. Nothing here starts the core — that
 * is still an explicit act — but this is the scope the owner is created at, so
 * that a rotation cannot be mistaken for a node dying.
 *
 * PROCESS-SCOPED, not eternal. Android may kill this process at any time; when
 * it does, this object and [RuntimeController] are built again from nothing
 * and reopen the SAME identity from the same data directory. "The owner
 * survives" is a claim about Activities, never about the process — AR-1e is
 * the wave that measures the difference, and it can only measure it if nothing
 * here pretends the two are one.
 */
class QuietApp : Application() {
    override fun onCreate() {
        super.onCreate()
        RuntimeController.get(this)

        // Channels are created here rather than where the first notification
        // is posted: from API 26 a notification without one is not shown at
        // all, and the first one this app ever posts may be produced while no
        // screen exists — a candidate arrives from a Go goroutine, not from a
        // tap. Creating them is idempotent, so it happens where it cannot be
        // missed. See NotificationChannels for why the ids are effectively
        // frozen the moment they first exist.
        NotificationChannels.ensure(this)

        // The USB service, handed to the core once. Registered at APPLICATION
        // scope on purpose: the core may ask for the device list from a
        // goroutine at any time, including while no Activity exists, and a
        // host tied to a screen would disappear on a rotation. The permission
        // dialog it raises belongs to the system and works from here.
        Quietcore.setUsbHost(UsbRadio.Host(this))
    }
}
