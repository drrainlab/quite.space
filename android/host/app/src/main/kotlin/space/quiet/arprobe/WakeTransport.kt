package space.quiet.arprobe

import android.content.Context
import android.util.Log
import space.quiet.quietcore.Quietcore
import space.quiet.quietcore.WatchSink
import java.io.File

/**
 * HOW A CLOSED NODE LEARNS THAT SOMETHING IS WAITING (AN-2).
 *
 * The bell and the wire that carries it are two things. What rings is always
 * the same: one line in the shade that names nothing. What carries the ring
 * is a transport, and there will be more than one — this interface is the
 * seam, so the second (an optional push lane with a contentless signal, for
 * phones that would rather spend the platform's one shared connection than
 * their own) can arrive without touching the people who call it.
 */
internal interface WakeTransport {
    /** False when it cannot run at all right now (nothing to watch with). */
    fun start(onMail: () -> Unit, onExpired: () -> Unit): Boolean
    fun stop()
    val running: Boolean
}

/**
 * The direct one: our own connection to our own relays, parked on the
 * addresses of this device's mailboxes, holding no key.
 *
 * The plan lives in noBackupFilesDir ON PURPOSE. It is not harmless: it is a
 * bounded right to WATCH this device's mailboxes for as long as it is valid
 * — never to read or to empty them — and a copy of it in a cloud backup is a
 * copy of that right.
 */
internal class DirectWatch(private val app: Context) : WakeTransport {

    override val running: Boolean
        get() = try { Quietcore.watchRunning() } catch (t: Throwable) { false }

    override fun start(onMail: () -> Unit, onExpired: () -> Unit): Boolean = try {
        Quietcore.startWatch(planFile(app).absolutePath, object : WatchSink {
            override fun onMail() = onMail()
            override fun onExpired() = onExpired()
            override fun onState(addr: String?, state: String?) {
                Log.i(TAG, "watch $state @ $addr")
            }
        })
    } catch (t: Throwable) {
        Log.w(TAG, "the watch could not start", t)
        false
    }

    override fun stop() {
        try { Quietcore.stopWatch() } catch (t: Throwable) { Log.w(TAG, "stopWatch", t) }
    }

    companion object {
        private const val TAG = "quiet-watch"
        fun planFile(app: Context) = File(app.noBackupFilesDir, "watch-plan.json")
    }
}
