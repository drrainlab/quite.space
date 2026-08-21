package space.quiet.arprobe

import android.content.Context
import android.net.wifi.WifiManager
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

    // SCHEDULED, not merely single-threaded: AR-1c.6's storage retry needs a
    // delay, and giving it its own thread would mean two threads touching
    // SQLite — the one thing this executor exists to prevent.
    private val worker = Executors.newSingleThreadScheduledExecutor { r ->
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
        // AR-1c.6 — a retry after storage refused, on the controller's own
        // worker. The same thread SQLite is touched from everywhere else, so
        // a retry cannot race the arrival it is retrying.
        { delayMs, run ->
            worker.schedule({ run() }, delayMs, java.util.concurrent.TimeUnit.MILLISECONDS)
            Unit
        },
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
                personal = c.personal,
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
     * The same two facts the bridge carries, as a door the DEBUG RIG can
     * knock on — NotificationCoordinator is internal, so a Java harness cannot
     * reach it directly.
     *
     * Public here rather than the coordinator made visible: this is the
     * narrowest thing that works, and it keeps the plane's own type out of a
     * surface that exists for a measurement harness.
     */
    fun reportVisibleSpace(spaceId: String?) = notifications.onVisibleSpace(spaceId)

    fun reportRead(spaceId: String) = notifications.onRead(spaceId)

    // ------------------------------------------------- AR-1c availability

    /**
     * How many holders are asking this process to stay reachable.
     *
     * A LEASE, NOT A SECOND OWNER. The runtime belongs here and always has;
     * a holder — today only the foreground service — says "keep it open while
     * I exist" and hands that back when it stops. Counted rather than boolean
     * because there will be more than one holder (a wake worker in AR-1d), and
     * a second holder arriving must not be able to end the first one's claim.
     */
    private val availabilityLeases = java.util.concurrent.atomic.AtomicInteger(0)

    /**
     * Whether the PERSON has the mode switched on. Separate from the leases:
     * a lease is a live holder, this is an intention, and it is what survives
     * the process so the switch does not lie after a restart.
     */
    /**
     * Whether this device should stay reachable when nobody is looking at it.
     *
     * THE DEFAULT IS ON, and it is a product decision rather than a
     * convenience. Media is not carried by the relay — only the message that
     * mentions it is — so a photo's bytes live on the phone that sent them
     * until somebody opens the card. With this off, a phone that locks takes
     * every picture it ever sent with it, and the person on the other side
     * watches a placeholder that never resolves. That was reported as a bug
     * twice before anybody worked out it was a setting, and it will be
     * reported as a bug by everybody who never finds the switch.
     *
     * What it costs is stated where it is spent: a permanent notification
     * Android requires us to show, and battery. Both are visible, and the
     * switch that turns it off is the first thing in its settings tab.
     */
    fun availabilityRequested(): Boolean =
        enabledPrefs.getBoolean(KEY_AVAILABILITY, true)

    fun setAvailabilityRequested(on: Boolean) {
        // Asking for it again clears any refusal: the person is entitled to a
        // fresh answer, and a stale complaint beside a switch they just turned
        // on would be describing the last attempt, not this one.
        enabledPrefs.edit()
            .putBoolean(KEY_AVAILABILITY, on)
            .apply { if (on) putBoolean(KEY_AVAILABILITY_REFUSED, false) }
            .commit()
        publish()
    }

    /**
     * The platform would not let the mode start.
     *
     * WHY THIS HAS TO BE RECORDED RATHER THAN ONLY LOGGED. A refused start
     * turns the intention off so nothing loops — correct, and invisible: the
     * person then finds the switch sitting at Off and reads it as their own
     * choice, or as the setting not working. That was survivable while the
     * mode was off by default and is not now, because a device that cannot
     * hold it is a device whose media nobody else can open, and the only
     * clue would be a line in logcat.
     */
    fun noteAvailabilityRefused() {
        enabledPrefs.edit()
            .putBoolean(KEY_AVAILABILITY, false)
            .putBoolean(KEY_AVAILABILITY_REFUSED, true)
            .commit()
        publish()
    }

    /** Cleared the moment the mode actually runs — see [noteAvailabilityRefused]. */
    fun clearAvailabilityRefused() {
        if (!enabledPrefs.getBoolean(KEY_AVAILABILITY_REFUSED, false)) return
        enabledPrefs.edit().putBoolean(KEY_AVAILABILITY_REFUSED, false).commit()
        publish()
    }

    fun availabilityRefused(): Boolean =
        enabledPrefs.getBoolean(KEY_AVAILABILITY_REFUSED, false)

    fun acquireAvailabilityLease(): Int {
        val n = availabilityLeases.incrementAndGet()
        publish()
        return n
    }

    fun releaseAvailabilityLease(): Int {
        // Floored at zero: a double release is a bug in a holder, and it must
        // not drive the count negative and then swallow a real holder's claim
        // on the way back up.
        val n = availabilityLeases.updateAndGet { if (it > 0) it - 1 else 0 }
        publish()
        return n
    }

    fun availabilityLeaseCount(): Int = availabilityLeases.get()

    /** What the permanent notification says, gathered in one place. */
    internal fun availabilitySnapshot(): AvailabilityText.State {
        val alive = isAlive()
        var relays = 0
        var since = -1L
        if (alive) {
            try {
                val core = snapshot().optJSONObject("core")
                val d = core?.optJSONObject("relay")
                if (d != null) {
                    if (d.optString("primary").isNotEmpty()) relays++
                    if (d.optString("backup").isNotEmpty()) relays++
                    val ago = d.optInt("seconds_since_pull", -1)
                    since = if (ago >= 0) ago.toLong() else -1L
                }
            } catch (t: Throwable) {
                // A snapshot that cannot be read is not a reason to fail the
                // mode; the card says what it knows.
                Log.w(TAG, "could not read the relay state for the card", t)
            }
        }
        return AvailabilityText.State(relays, since, alive)
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

    // ---- pairing, the child half (MD-1): strictly BEFORE the first start,
    // against an empty data dir. The Activity drives the screens; this owns
    // the directory, like everywhere else.

    fun hasIdentity(): Boolean = Quietcore.hasIdentity(dataDir().absolutePath)

    /** Starts the ceremony; returns null on success or the refusal's text.
     *  The multicast lock is held for the ceremony's life: the BEACON is the
     *  fallback when the offer's address guess is stale, and without the
     *  lock Android's Wi-Fi stack drops exactly those packets. */
    fun pairStart(passphrase: String, offer: String): String? = try {
        acquireMulticast()
        Quietcore.pairStart(dataDir().absolutePath, passphrase, offer,
            android.os.Build.MODEL ?: "Android"); null
    } catch (t: Throwable) {
        releaseMulticast()
        t.message ?: "could not start pairing"
    }

    fun pairState(): JSONObject = JSONObject(Quietcore.pairState()).also {
        // The ceremony ended, however it ended: the lock has done its job.
        val st = it.optString("stage")
        if (st == "done" || st == "failed") releaseMulticast()
    }

    fun pairApprove() { Quietcore.pairApprove() }

    /** Erases the data dir for a fresh pairing; null on success. Guarded in
     *  the core: refused under a running node or a ceremony in flight. */
    fun startOver(): String? = try {
        Quietcore.startOver(dataDir().absolutePath); null
    } catch (t: Throwable) { t.message ?: "could not erase" }

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
     * Why the last attempt to open failed, as the core put it, or null.
     *
     * The core has always recorded this and no host has ever read it, so the
     * unlock screen showed the same fixed sentence whether the passphrase was
     * eight characters short or the data directory was held by another
     * process. A reason nobody surfaces is a reason nobody has.
     */
    fun lastError(): String? = try {
        JSONObject(Quietcore.status()).optString("last_error", "").ifEmpty { null }
    } catch (e: Exception) {
        null
    }

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
                // Multicast is dropped by the Wi-Fi stack unless a lock is
                // held, and LAN discovery IS multicast — without this the
                // phone announces into the void and hears nobody (T6-LAN).
                // Held for the node's lifetime, released in stop(); the
                // battery cost is the price of "stay connected", which is
                // already this app's default posture.
                if (withLAN) acquireMulticast()
                Quietcore.startAs(
                    dataDir().absolutePath,
                    passphrase ?: "",
                    name ?: "me",
                    android.os.Build.MODEL ?: "Android",
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

    private var multicast: WifiManager.MulticastLock? = null

    private fun acquireMulticast() {
        try {
            if (multicast?.isHeld == true) return
            val wifi = app.getSystemService(Context.WIFI_SERVICE) as WifiManager
            multicast = wifi.createMulticastLock("quiet-lan").apply {
                setReferenceCounted(false)
                acquire()
            }
        } catch (t: Throwable) {
            // A phone that cannot multicast is an ordinary phone: the node
            // still opens, LAN discovery just stays deaf, and the core's own
            // status carries the LAN error if StartLAN itself failed.
            Log.w(TAG, "multicast lock unavailable", t)
        }
    }

    private fun releaseMulticast() {
        try {
            multicast?.takeIf { it.isHeld }?.release()
        } catch (t: Throwable) {
            Log.w(TAG, "multicast release failed", t)
        }
        multicast = null
    }

    /** Closes the core. Safe when nothing is open. */
    fun stop() {
        worker.execute {
            try {
                Quietcore.stop()
            } catch (t: Throwable) {
                Log.w(TAG, "stop failed", t)
            } finally {
                releaseMulticast()
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
            // AR-1c. The MODE and its LEASES are separate facts: one is what
            // the person asked for, the other is who is currently holding the
            // process to it. A gate that could only see the first would call
            // a service that failed to start "on".
            n.put("availability_requested", availabilityRequested())
            n.put("availability_leases", availabilityLeaseCount())
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
        private const val KEY_AVAILABILITY = "availability_mode"
        private const val KEY_AVAILABILITY_REFUSED = "availability_refused"

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
