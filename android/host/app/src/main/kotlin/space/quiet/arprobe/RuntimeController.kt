package space.quiet.arprobe

import android.content.Context
import android.util.Log
import org.json.JSONObject
import space.quiet.quietcore.Candidate
import space.quiet.quietcore.NotificationSink
import space.quiet.quietcore.Quietcore
import java.io.File
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean

/**
 * AR-1a: the one owner of the runtime, and it is NOT an Activity.
 *
 * WHY THIS EXISTS. An Activity is destroyed and recreated for reasons that
 * have nothing to do with a node: a rotation, a theme change, a locale change,
 * the system reclaiming a WebView, "Don't keep activities". If the Activity
 * owns the core, every one of those reads as the node dying — and the honest
 * consequences follow: a second `Open` against a directory the first one still
 * holds, a fresh `runtime_epoch` for a runtime that never stopped, and a UI
 * that offers to start something already running.
 *
 * So ownership sits at Application scope, above everything that gets torn
 * down, and an Activity becomes a CLIENT: it asks for a snapshot and listens.
 *
 * PROCESS-SCOPED, NOT ETERNAL. Application scope outlives every Activity and
 * nothing more. When Android kills the process this object is gone, and the
 * next launch builds a new one that opens the same identity from the same
 * directory — a new `runtime_epoch` for the same person. Both facts are in
 * the status precisely so a restart is REPORTED as a restart rather than
 * passing as "the process was kept".
 *
 * THREE STATES, NOT TWO. `node.Open` is seconds on a phone — AR-0 measured
 * 2.6 s for a 16 000-event log — so "not alive" is two different situations:
 *
 * ```
 *   unavailable   nothing is open, and nothing is trying
 *   opening       an open is in flight; the node is working, not dead
 *   alive         open and serving
 * ```
 *
 * The Go side publishes "opening" before it begins the slow work and answers
 * status through a separate lock, so this class can read the difference rather
 * than infer it from a timeout.
 */
class RuntimeController private constructor(appContext: Context) {

    /** What a listener is told. Kept deliberately small: state, not payload. */
    fun interface Listener {
        fun onRuntimeState(state: String)
    }

    /**
     * Application context only — never an Activity. Holding an Activity here
     * would defeat the entire purpose and leak it besides.
     */
    private val app: Context = appContext.applicationContext

    private val worker = Executors.newSingleThreadExecutor { r ->
        Thread(r, "quiet-runtime").apply { isDaemon = false }
    }

    private val listeners = CopyOnWriteArrayList<Listener>()

    /**
     * Guards against two overlapping starts from this process. The data-dir
     * lock is the real defence and the Go binding refuses a second Start on
     * its own — but the lock exists to stop two PROCESSES, and provoking it
     * from inside one would be this class failing at its only job.
     */
    private val starting = AtomicBoolean(false)

    /**
     * AR-1b: the notification plane, owned HERE for the same reason the
     * runtime is — it must outlive every Activity. A coordinator that died
     * with a screen would forget on every rotation which notifications it had
     * already shown, and show them again.
     */
    private val presenter = LogPresenter()

    val notifications: NotificationCoordinator =
        NotificationCoordinator(presenter, PrefsNotificationStore(app))

    private val sink = NotificationSink { c: Candidate ->
        // Called on a GO goroutine. It must return promptly and must not reach
        // back into the core — the emit path runs inside the runtime's own
        // critical section, and a call back in from here would deadlock
        // against a lock this side cannot see.
        notifications.onCandidate(
            c.eventID, c.spaceID, c.device, c.schema,
            c.createdAt, c.authoredLocally,
        )
    }

    init {
        // ARMED AT APPLICATION SCOPE, BEFORE ANY CORE IS OPEN — and that
        // ordering is the invariant, not an optimisation. The binding arms the
        // runtime only after node.Open has returned, so history cannot reach
        // this sink however early it is installed; installing it LATE, on the
        // other hand, would lose the first events of a session to a race with
        // whichever screen happened to come up first.
        Quietcore.armNotifications(sink)
    }

    /**
     * What the person's answer to the permission means, all the way down.
     *
     * A refusal does not merely stop the host from posting: it DISARMS the
     * core, so no candidate is produced at all. Producing them and dropping
     * them on this side would burn the bounded queue, count drops nobody can
     * act on, and make the status lie about why nothing appeared.
     *
     * Re-arming reaches the live runtime, so a person who turns notifications
     * back on in settings does not have to restart anything.
     */
    fun setNotificationsEnabled(on: Boolean) {
        notifications.setEnabled(on)
        if (on) Quietcore.armNotifications(sink) else Quietcore.disarmNotifications()
    }

    /**
     * The data directory, resolved from the APPLICATION context.
     *
     * `node.DefaultDataDir()` reads $HOME, which an Android app process does
     * not have — it would yield the relative path "quiet-data" under a CWD of
     * "/" and fail at MkdirAll. The real directory is app-private internal
     * storage, and it is resolved in exactly one place so no caller can invent
     * a second one.
     */
    fun dataDir(): File = File(app.filesDir, "node").apply { mkdirs() }

    fun addListener(l: Listener) {
        listeners.add(l)
        l.onRuntimeState(state())   // a new client is told where things stand
    }

    fun removeListener(l: Listener) {
        listeners.remove(l)
    }

    /**
     * What the runtime is doing. Never blocks.
     *
     * The controller's OWN in-flight flag is consulted first, and that is not
     * belt-and-braces — it closes a race that made the rig lie. [ensureStarted]
     * returns as soon as the work is queued, so for the moments between
     * queuing and the worker thread reaching the Go side, the core still
     * reports "unavailable". A caller waiting for the state to leave "opening"
     * therefore saw "unavailable", concluded there was nothing to wait for,
     * and answered before the open had begun.
     *
     * `starting` is set synchronously by the caller's own thread, so it is
     * true the instant ensureStarted returns. The controller is authoritative
     * about its own operations; the core is authoritative about everything
     * else.
     */
    fun state(): String {
        val core = try {
            JSONObject(Quietcore.status()).optString("state", STATE_UNAVAILABLE)
        } catch (e: Exception) {
            STATE_UNAVAILABLE
        }
        if (core == STATE_ALIVE) return core
        return if (starting.get()) STATE_OPENING else core
    }

    fun isAlive(): Boolean = state() == STATE_ALIVE

    /**
     * Opens the core if it is not already open. Returns immediately; the work
     * happens on the controller's own thread, because it is seconds long and
     * the caller is usually the main thread.
     *
     * Idempotent in the direction that matters: calling it twice does not
     * produce two runtimes. An Activity recreated three times in a second
     * calls this three times, and that must be uneventful.
     */
    fun ensureStarted(passphrase: String?, name: String?, withLAN: Boolean) {
        if (isAlive() || !starting.compareAndSet(false, true)) return
        worker.execute {
            try {
                publish()                                    // "opening" is now visible
                Quietcore.start(
                    dataDir().absolutePath,
                    passphrase ?: "",
                    name ?: "me",
                    withLAN,
                )
            } catch (t: Throwable) {
                // A failure is a RESULT, not something to swallow: "the core
                // would not open" is a state a host must be able to show.
                Log.w(TAG, "start failed", t)
            } finally {
                starting.set(false)
                publish()
            }
        }
    }

    /** Closes the core. Safe when nothing is open. */
    fun stop() {
        worker.execute {
            try {
                Quietcore.stop()
            } catch (t: Throwable) {
                Log.w(TAG, "stop failed", t)
            } finally {
                publish()
            }
        }
    }

    /**
     * The full status: the core's own answer plus the facts only the framework
     * can supply. Callers get one object rather than assembling their own, so
     * two callers cannot disagree about what the runtime is.
     */
    fun snapshot(): JSONObject {
        val out = JSONObject()
        try {
            out.put("core", JSONObject(Quietcore.status()))
            out.put("owner", "RuntimeController")            // never an Activity
            out.put("data_dir", dataDir().absolutePath)

            // The notification half, as DECISIONS rather than a rendering.
            // This is what makes "opening a journal showed nothing, and the
            // next event showed exactly one" a fact a harness can read without
            // a screen, a permission dialog or a person watching a shade.
            val n = JSONObject(notifications.stats() as Map<*, *>)
            n.put("presenter", presenter.snapshot())
            out.put("notifications", n)
        } catch (e: Exception) {
            Log.w(TAG, "snapshot failed", e)
        }
        return out
    }

    private fun publish() {
        val s = state()
        for (l in listeners) {
            try {
                l.onRuntimeState(s)
            } catch (t: Throwable) {
                Log.w(TAG, "listener threw", t)
            }
        }
    }

    companion object {
        private const val TAG = "quiet-runtime"

        const val STATE_UNAVAILABLE = "unavailable"
        const val STATE_OPENING = "opening"
        const val STATE_ALIVE = "alive"

        @Volatile
        private var instance: RuntimeController? = null

        @JvmStatic
        fun get(anyContext: Context): RuntimeController =
            instance ?: synchronized(this) {
                instance ?: RuntimeController(anyContext.applicationContext)
                    .also { instance = it }
            }
    }
}
