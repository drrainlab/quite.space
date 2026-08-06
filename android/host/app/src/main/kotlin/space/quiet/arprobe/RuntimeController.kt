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
     * Whether notifications were on the last time anybody said. Durable
     * because the interesting thing is the TRANSITION — see
     * setNotificationsEnabled — and a transition cannot be seen by a process
     * that has just started.
     */
    private val enabledPrefs = app.getSharedPreferences("quiet-notifications", Context.MODE_PRIVATE)

    /**
     * AR-1b: the notification plane, owned HERE for the same reason the
     * runtime is — it must outlive every Activity. A coordinator that died
     * with a screen would forget on every rotation which notifications it had
     * already shown, and show them again.
     */
    private val ledger = SqliteLedger(this.app)
    private val presenter = SystemPresenter(this.app, ledger, storedPolicy(app))

    val notifications: NotificationCoordinator = NotificationCoordinator(
        presenter,
        ledger,
        // The acknowledgement goes straight to the core: from here the log may
        // stop replaying this candidate, because the host has it in its own
        // durable storage. Called on the Go goroutine that delivered it, and
        // cheap enough to be — it is one map lookup and a counter behind the
        // binding's own lock.
        { eventId -> Quietcore.ackNotification(eventId) },
    )

    // TWO METHODS NOW, so it cannot stay a SAM lambda: arrivals, and spaces
    // that are gone. Both come off the same Go queue in order, which is what
    // stops a queued arrival from re-posting a conversation that was just
    // deleted.
    private val sink = object : NotificationSink {
        override fun onSpaceForgotten(spaceID: String) {
            // Called on a Go goroutine like the other one. Cancelling a
            // notification and closing rows is local work — no call back into
            // the core, which is what the binding's contract forbids.
            notifications.forgetSpace(spaceID)
        }

        override fun onCandidate(c: Candidate) {
        // Called on a GO goroutine. It must return promptly and must not reach
        // back into the core — the emit path runs inside the runtime's own
        // critical section, and a call back in from here would deadlock
        // against a lock this side cannot see.
        notifications.onCandidate(
            NotificationCoordinator.Candidate(
                eventId = c.eventID,
                spaceId = c.spaceID,
                device = c.device,
                schema = c.schema,
                sourceSequence = c.sourceSequence,
                occurredAtUnixMs = c.occurredAtUnixMs,
                presentationCursor = c.presentationCursor,
                authoredLocally = c.authoredLocally,
                spaceLabel = c.spaceLabel,
                senderLabel = c.senderLabel,
                previewText = c.previewText,
            )
        )
        }
    }

    init {
        // ARMED AT APPLICATION SCOPE, BEFORE ANY CORE IS OPEN — and that
        // ordering is the invariant, not an optimisation. The binding arms the
        // runtime only after node.Open has returned, so history cannot reach
        // this sink however early it is installed; installing it LATE, on the
        // other hand, would lose the first events of a session to a race with
        // whichever screen happened to come up first.
        Quietcore.armNotifications(sink)

        // WHAT THE PROCESS DIED IN THE MIDDLE OF (AR-1b.5.3c). Rows that were
        // acknowledged to the core but never confirmed as posted are posted
        // now — under the same (tag, id), so a crash that happened AFTER the
        // shade already showed one updates that entry rather than adding a
        // second. Runs before anything else can arrive, on the controller's
        // own thread because it touches SQLite.
        worker.execute {
            try {
                // FIRST: what the ledger thinks is on screen, checked against
                // what Android is holding. A force-stop or a reboot takes the
                // shade without telling anyone, and the rows left behind make
                // the summary count conversations nobody can see. Before
                // recovery, deliberately — a row posted a moment ago by
                // recoverPending may not be visible to getActiveNotifications
                // yet, and reconciling against that window would take down
                // exactly what was just restored.
                val live = try {
                    presenter.liveTags()
                } catch (t: Throwable) {
                    // Not knowing is not the same as "nothing is there":
                    // reconciling against an empty set would dismiss every
                    // live conversation because one binder call failed.
                    Log.w(TAG, "skipping reconcile: could not read the active set", t)
                    null
                }
                if (live != null) notifications.reconcileWithSystem(live)
                notifications.recoverPending()
                this@RuntimeController.compactNotifications()
            } catch (t: Throwable) {
                Log.w(TAG, "pending recovery failed", t)
            }
        }
    }

    /**
     * AR-1b.7b — what Android may know, remembered on THIS DEVICE.
     *
     * Device-local and not in the log, deliberately. The choice is about the
     * shade of one phone: a person may want previews on the handset in their
     * pocket and nothing at all on the tablet in the kitchen, and syncing it
     * would take that away in the name of consistency. It is also nobody
     * else's business, and everything in the log becomes somebody else's
     * business eventually.
     *
     * ONE WRITER. The presenter holds the live value; this is where it comes
     * from at startup and where it goes when it changes, so a restart cannot
     * quietly return to the default after somebody chose otherwise.
     */
    fun setNotificationPolicy(next: PresentationPolicy) {
        enabledPrefs.edit().putString(KEY_POLICY, next.name).commit()
        val tags = try {
            ledger.activeBySpace().mapValues { (_, r) -> r.tag }
        } catch (t: Throwable) {
            Log.w(TAG, "could not read the active set for a policy change", t)
            emptyMap()
        }
        // TIGHTENING TAKES THE SURFACES BACK IMMEDIATELY; loosening publishes
        // nothing by itself. Choosing "hidden" asks for what is already on the
        // screen to stop saying things, not merely for the next message to say
        // less.
        presenter.applyPolicy(next, tags)
    }

    fun notificationPolicy(): PresentationPolicy = presenter.policy

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
        val was = enabledPrefs.getBoolean(KEY_ENABLED, false)
        notifications.setEnabled(on)

        if (!on) {
            enabledPrefs.edit().putBoolean(KEY_ENABLED, false).commit()
            Quietcore.disarmNotifications()
            return
        }

        // TURNED BACK ON AFTER BEING OFF: everything the core held back while
        // nobody could render it becomes history, deliberately.
        //
        // Without this, granting the permission after a week would produce a
        // week of messages at once: the core does not forget unacknowledged
        // candidates — that is the whole point of the watermark — so the
        // backlog would arrive the moment the plane re-attached. Somebody who
        // switches notifications on wants the NEXT message, not every one they
        // chose not to be told about.
        //
        // The transition is what triggers it, not the state: an ordinary
        // start with the permission already granted must resume normally, or
        // every launch would eat whatever a crash left unacknowledged.
        if (!was) {
            enabledPrefs.edit().putBoolean(KEY_ENABLED, true).commit()
            worker.execute {
                try {
                    Quietcore.resetNotificationPlane()
                } catch (t: Throwable) {
                    Log.w(TAG, "reset after re-enabling failed", t)
                }
                Quietcore.armNotifications(sink)
            }
            return
        }
        Quietcore.armNotifications(sink)
    }

    /**
     * Forget the tombstones the core can no longer replay (AR-1b.5.5).
     *
     * The floor comes from the CORE, and it is the minimum across both
     * checkpoint generations — not the current watermark. A damaged checkpoint
     * falls back to the older generation and replays what the newer one had
     * confirmed; a host that trimmed to the current line would meet those
     * events again, believe them new, and resurrect notifications a person
     * dismissed a week ago.
     *
     * Runs after recovery, on the controller's own thread, never on the path
     * an arriving candidate takes.
     */
    private fun compactNotifications() {
        val root = JSONObject(Quietcore.notificationRetainFromJSON())
        val floors = HashMap<String, Map<String, Long>>()
        for (space in root.keys()) {
            val devices = root.getJSONObject(space)
            val m = HashMap<String, Long>()
            for (device in devices.keys()) m[device] = devices.getLong(device)
            floors[space] = m
        }
        val removed = notifications.compact(floors)
        if (removed > 0) Log.i(TAG, "compacted $removed unreachable notification rows")
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
            n.put("ledger", JSONObject(ledger.stats() as Map<*, *>))
            n.put("plane", Quietcore.notificationPlaneState())
            // AR-1b.8 — what the INTERFACE has told this host. Without it,
            // "the bridge is wired" is only checkable by watching a
            // notification not appear, which is the hardest kind of fact to
            // read from outside. Presence, not content: whether a space is on
            // screen, never which one.
            n.put("bridge_visible_space", notifications.hasVisibleSpace())
            n.put("bridge_reads", notifications.readCount())
            n.put("policy", presenter.policy.name.lowercase())
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
        private const val KEY_POLICY = "presentation_policy"

        /**
         * The stored choice, or the strict default. An unreadable or unknown
         * value falls back to HIDDEN rather than to the friendliest option:
         * every other setting publishes something about a conversation, and a
         * default nobody chose is how that happens by accident.
         */
        private fun storedPolicy(app: Context): PresentationPolicy {
            val raw = app.getSharedPreferences("quiet-notifications", Context.MODE_PRIVATE)
                .getString(KEY_POLICY, null) ?: return PresentationPolicy.DEFAULT
            return runCatching { PresentationPolicy.valueOf(raw) }
                .getOrDefault(PresentationPolicy.DEFAULT)
        }

        private const val TAG = "quiet-runtime"
        private const val KEY_ENABLED = "notifications_enabled"

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
