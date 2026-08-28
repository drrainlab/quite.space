package space.quiet.arprobe

import android.annotation.SuppressLint
import android.app.Notification
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.location.Location
import android.location.LocationListener
import android.location.LocationManager
import android.os.Build
import android.os.Handler
import android.os.HandlerThread
import android.os.IBinder
import android.os.SystemClock
import android.util.Log
import org.json.JSONObject
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors

/**
 * SP-3.2 — the Field Session: the ONE place background location exists.
 *
 * THE FIRST LAW (ADR-034): background location exists only inside an
 * explicitly started, visibly active, bounded Field Session. Every clause is
 * load-bearing here. EXPLICITLY STARTED: this service starts from a visible
 * Activity after a person pressed a button that says what it does — Android
 * 12+ forbids anything else, and the restriction is the right shape.
 * VISIBLY ACTIVE: a persistent notification for the whole session, with the
 * operation's name and a Stop button that works from the lock screen.
 * BOUNDED: the session ends — by the person's Stop, by the node finalizing
 * it, or by the node declaring it interrupted when nobody claims it.
 *
 * IT IS NOT THE ENGINE. The recording engine — spool, saga, sealing, the
 * completion event — lives in the Go node. This service is a SENSOR PUMP:
 * it hears GPS, classifies its own silences, and carries batches over the
 * loopback API. Kill it and the recording survives on the node's disk; the
 * sticky restart claims the session back and the seam shows as one honest
 * gap(suspended), never as a line drawn through a forest.
 *
 * STOP FORBIDS CAPTURE, NOT FINALIZATION. The moment a person stops the
 * sweep, the machine closes and the location listener dies — no further
 * position is captured or emitted. What may continue is finishing the
 * record they already asked for: draining queued samples and posting the
 * stop. A promise that forbade THAT would strand the sweep unfinished.
 *
 * NO WAKE LOCK, DELIBERATELY (v1). Doze will slow this pump on some
 * devices, and the design's answer is honesty rather than force: the gap
 * classifier proves sleep with the same clock pair the core uses and
 * records it as data. Whether a wake lock is WORTH its battery is a
 * measurement on real hardware (the ADR-034 gate), not an assumption.
 *
 * LIKE AvailabilityService IN SHAPE: a lease on the runtime, never a second
 * owner of it; a refusal recorded rather than only logged; START_STICKY so
 * memory pressure does not end a session a person started — while a
 * force-stop does, and must, because "stop" from a person means stop.
 */
class SweepService : Service() {

    private var leased = false
    private var machine: SweepSessionMachine? = null
    private var api: SweepApi? = null
    private var io: ExecutorService? = null
    private var pump: HandlerThread? = null
    private var handler: Handler? = null

    // The session's face, updated from the node's own answers.
    @Volatile private var label = ""
    @Volatile private var startedAtSec = 0L
    @Volatile private var distanceM = 0L
    @Volatile private var sampleCount = 0L
    @Volatile private var gpsOk = false
    @Volatile private var capturing = false

    // The silence bookkeeping GapClassifier feeds on: when the recorder
    // last heard ANYTHING, and the clock pair at that moment.
    private var lastEventUnixMs = 0L
    private var lastEventElapsed = 0L
    private var lastEventUptime = 0L
    private var providerOffSeen = false

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val controller = RuntimeController.get(this)
        api = api ?: SweepApi { controller.apiEndpoint() }
        io = io ?: Executors.newSingleThreadExecutor()

        when (intent?.action) {
            ACTION_STOP -> {
                // From the notification (no judgement: the node records
                // `undeclared`) or from the bridge's dialog (a real slug).
                stopCapture(
                    intent.getStringExtra(EXTRA_RESULT) ?: "",
                    intent.getStringExtra(EXTRA_NOTE) ?: "",
                )
                return START_NOT_STICKY
            }
        }

        NotificationChannels.ensure(this)
        try {
            startForeground(NOTIFICATION_ID, buildNotification("starting…"), foregroundType())
        } catch (t: Throwable) {
            // The platform refused the start — RECORDED, not only logged
            // (the AvailabilityService lesson): without this the button
            // just does nothing and the person blames themselves.
            Log.w(TAG, "the sweep session could not start", t)
            controller.noteSweepRefused()
            stopSelf()
            return START_NOT_STICKY
        }
        controller.clearSweepRefused()

        if (!leased) {
            leased = true
            controller.acquireAvailabilityLease()
        }

        val space = intent?.getStringExtra(EXTRA_SPACE)
        if (space != null) {
            begin(
                space,
                intent.getStringExtra(EXTRA_PARENT) ?: "",
                intent.getStringExtra(EXTRA_TASK) ?: "",
                intent.getStringExtra(EXTRA_NAME) ?: "",
            )
        } else if (machine == null) {
            // A null intent: Android restarted a sticky service whose
            // process died mid-session. The remembered intent names the
            // sweep; the NODE is the truth about whether it still runs.
            stickyRecover()
        }
        return START_STICKY
    }

    // ------------------------------------------------------------ lifecycle

    /** The start saga's host half: the NODE mints the session (its own
     *  crash-safe saga), and only its answer authorizes capture. */
    private fun begin(space: String, parent: String, task: String, name: String) {
        io?.execute {
            val info = api?.start(space, parent, task, name)
            if (info == null || info.sweepId.isEmpty()) {
                Log.w(TAG, "the node refused the sweep")
                finishWith("The sweep could not start")
                return@execute
            }
            prefs().edit()
                .putString(KEY_SWEEP, info.sweepId)
                .putString(KEY_LABEL, info.label)
                .commit()
            adopt(info)
        }
    }

    /**
     * The HYBRID orphan policy's live half (ADR-034): a sticky restart asks
     * the node — not its own memory — whether the session still runs, and
     * claims it back with a resume whose seam is one honest gap(suspended).
     * The dead half belongs to the node: a sweep nobody claims within the
     * grace is finalized as `interrupted`, track preserved.
     */
    private fun stickyRecover() {
        val sweepId = prefs().getString(KEY_SWEEP, "") ?: ""
        if (sweepId.isEmpty()) {
            stopSelf()
            return
        }
        io?.execute {
            val controller = RuntimeController.get(this)
            // The core may be cold — the process died with the session. A
            // remembered passphrase may open it (the doorbell discipline);
            // a node locked at rest stays locked, and the sweep then ends
            // as the orphan it honestly is.
            if (!controller.isAlive()) {
                val stored = try {
                    PassphraseVault(this).load()
                } catch (t: Throwable) {
                    null
                }
                if (stored != null) controller.ensureStarted(stored, null, true)
            }
            var waited = 0L
            while (controller.apiEndpoint() == null && waited < RECOVER_WAIT_MS) {
                Thread.sleep(2000)
                waited += 2000
            }
            val actives = api?.actives()
            val mine = actives?.firstOrNull { it.sweepId == sweepId }
            if (mine == null) {
                // The node either finalized it (grace expired, or a Stop
                // landed from elsewhere) or never heard of it. Either way
                // the session is over and the person can read the result
                // in the app; a locked node lands here too, and its grace
                // does the honest thing.
                prefs().edit().remove(KEY_SWEEP).remove(KEY_LABEL).commit()
                finishWith(if (actives == null) "Sweep paused — open Quiet" else "Sweep ended")
                return@execute
            }
            when (api?.resume(sweepId)) {
                SweepApi.Verdict.OK -> adopt(mine)
                else -> {
                    prefs().edit().remove(KEY_SWEEP).remove(KEY_LABEL).commit()
                    finishWith("Sweep ended")
                }
            }
        }
    }

    /** From here the session is live on both sides: pump and notify. */
    private fun adopt(info: SweepApi.Info) {
        machine = SweepSessionMachine(info.sweepId)
        label = info.label
        startedAtSec = info.startedAt
        distanceM = info.distanceM
        sampleCount = info.samples
        capturing = true
        markHeard(System.currentTimeMillis())
        providerOffSeen = false
        val t = HandlerThread("quiet-sweep").also { it.start() }
        pump = t
        handler = Handler(t.looper)
        handler?.post { startListening() }
        handler?.postDelayed(::tick, INTERVAL_MS)
        updateNotification()
        pushState("recording")
    }

    @SuppressLint("MissingPermission")
    private fun startListening() {
        try {
            val lm = getSystemService(LOCATION_SERVICE) as LocationManager
            lm.requestLocationUpdates(
                LocationManager.GPS_PROVIDER, INTERVAL_MS, 0f, listener,
                pump?.looper ?: return,
            )
        } catch (t: Throwable) {
            // Permission revoked mid-session, or no GPS at all. Capture is
            // impossible, so the session ends the way a Stop without
            // judgement does — the node keeps whatever already landed.
            Log.w(TAG, "location updates refused", t)
            stopCapture("", "")
        }
    }

    private val listener = object : LocationListener {
        override fun onLocationChanged(location: Location) {
            val m = machine ?: return
            val now = System.currentTimeMillis()
            recordSilenceBefore(now)
            m.onPoint(now, location.latitude, location.longitude,
                location.accuracy.toLong().coerceAtLeast(1))
            markHeard(now)
            gpsOk = true
        }

        // The PLATFORM's own claim — the one thing that may earn `no_fix`.
        override fun onProviderDisabled(provider: String) {
            providerOffSeen = true
            gpsOk = false
        }

        override fun onProviderEnabled(provider: String) {}
    }

    /** The recorder's heartbeat: notice silences, drain batches, keep the
     *  visible half of the session's promise up to date. */
    private fun tick() {
        if (!capturing) return
        recordSilenceBefore(System.currentTimeMillis())
        flush()
        updateNotification()
        pushState("recording")
        handler?.postDelayed(::tick, INTERVAL_MS)
    }

    /**
     * A silent stretch over the trigger becomes a gap SAMPLE — and the
     * trigger is never the cause. The classifier gets the platform's own
     * provider claim and a measured sleep: elapsedRealtime advances through
     * suspend, uptimeMillis does not, so their divergence over the span IS
     * the time the phone spent asleep, the same clock pair the core trusts.
     */
    private fun recordSilenceBefore(nowMs: Long) {
        val m = machine ?: return
        val span = nowMs - lastEventUnixMs
        if (lastEventUnixMs == 0L || span <= 3 * INTERVAL_MS) return
        val sleepMs = (SystemClock.elapsedRealtime() - lastEventElapsed) -
            (SystemClock.uptimeMillis() - lastEventUptime)
        val reason = GapClassifier.classify(providerOffSeen, span, sleepMs, INTERVAL_MS)
        m.onGap(lastEventUnixMs, span, reason)
        markHeard(nowMs)
        providerOffSeen = false
        gpsOk = false
    }

    private fun markHeard(nowMs: Long) {
        lastEventUnixMs = nowMs
        lastEventElapsed = SystemClock.elapsedRealtime()
        lastEventUptime = SystemClock.uptimeMillis()
    }

    /** One batch out, on the IO thread; the machine's seq discipline makes
     *  a retry after any ambiguous outcome exactly-once on the node. */
    private fun flush() {
        val m = machine ?: return
        val batch = m.takeBatch() ?: return
        io?.execute {
            val (verdict, info) = api?.samples(m.sweepId, batch) ?: return@execute
            when (verdict) {
                SweepApi.Verdict.OK -> {
                    m.delivered(batch.seq)
                    if (info != null) {
                        distanceM = info.distanceM
                        sampleCount = info.samples
                    }
                }
                SweepApi.Verdict.CLOSED -> {
                    // The session ended on the node's side — a Stop from
                    // the page, or a finalization. The pump's cue to leave.
                    endCapture()
                    prefs().edit().remove(KEY_SWEEP).remove(KEY_LABEL).commit()
                    finishWith("Sweep ended")
                }
                SweepApi.Verdict.FAILED -> m.failed()
            }
        }
    }

    // ----------------------------------------------------------------- stop

    /**
     * Capture closes NOW — machine refuses, listener dies, ticker stops.
     * Then, and only then, the finishing: drain what is queued, post the
     * stop. If the node cannot be reached the stop becomes PENDING — the
     * person's judgement is not droppable — and lands when Quiet next opens.
     */
    private fun stopCapture(result: String, note: String) {
        val m = machine
        val sweepId = m?.sweepId ?: prefs().getString(KEY_SWEEP, "") ?: ""
        endCapture()
        if (sweepId.isEmpty()) {
            finishWith(null)
            return
        }
        io?.execute {
            if (m != null) {
                var tries = 0
                while (tries < DRAIN_TRIES) {
                    val batch = m.takeBatch() ?: break
                    val (verdict, _) = api?.samples(sweepId, batch) ?: (SweepApi.Verdict.FAILED to null)
                    when (verdict) {
                        SweepApi.Verdict.OK -> m.delivered(batch.seq)
                        SweepApi.Verdict.CLOSED -> break
                        SweepApi.Verdict.FAILED -> {
                            m.failed(); tries++
                        }
                    }
                }
            }
            when (api?.stop(sweepId, result, note)) {
                SweepApi.Verdict.OK -> {
                    prefs().edit()
                        .remove(KEY_SWEEP).remove(KEY_LABEL)
                        .remove(KEY_PENDING_SWEEP).remove(KEY_PENDING_RESULT).remove(KEY_PENDING_NOTE)
                        .commit()
                    pushState("stopped")
                    finishWith(null)
                }
                else -> {
                    prefs().edit()
                        .remove(KEY_SWEEP).remove(KEY_LABEL)
                        .putString(KEY_PENDING_SWEEP, sweepId)
                        .putString(KEY_PENDING_RESULT, result)
                        .putString(KEY_PENDING_NOTE, note)
                        .commit()
                    finishWith("Sweep will finish when Quiet opens")
                }
            }
        }
    }

    /** The capture machinery, off. Idempotent; finalization not included. */
    private fun endCapture() {
        capturing = false
        machine?.close()
        try {
            (getSystemService(LOCATION_SERVICE) as LocationManager).removeUpdates(listener)
        } catch (_: Throwable) {}
        handler?.removeCallbacksAndMessages(null)
    }

    /** Leave the foreground; [message] becomes a plain, dismissable note.
     *  The ongoing notification goes FIRST — while the service is foreground
     *  its notification cannot be dismissed, so the order matters. */
    private fun finishWith(message: String?) {
        stopForeground(STOP_FOREGROUND_REMOVE)
        if (message != null) {
            try {
                val nm = getSystemService(NOTIFICATION_SERVICE) as NotificationManager
                nm.notify(
                    NOTIFICATION_ID,
                    Notification.Builder(this, NotificationPolicy.CHANNEL_SWEEP)
                        .setSmallIcon(R.drawable.ic_stat_quiet)
                        .setContentTitle("Quiet")
                        .setContentText(message)
                        .setContentIntent(openIntent())
                        .setAutoCancel(true)
                        .build(),
                )
            } catch (_: Throwable) {}
        }
        stopSelf()
    }

    override fun onDestroy() {
        endCapture()
        pump?.quitSafely()
        io?.shutdown()
        if (leased) {
            leased = false
            RuntimeController.get(this).releaseAvailabilityLease()
        }
        super.onDestroy()
    }

    // -------------------------------------------------------------- surface

    private fun foregroundType(): Int =
        if (Build.VERSION.SDK_INT >= 29) ServiceInfo.FOREGROUND_SERVICE_TYPE_LOCATION else 0

    private fun openIntent(): PendingIntent = PendingIntent.getActivity(
        this, 0,
        Intent(this, QuietActivity::class.java),
        PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
    )

    private fun buildNotification(text: String): Notification {
        val stop = PendingIntent.getService(
            this, 1,
            Intent(this, SweepService::class.java).setAction(ACTION_STOP),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        return Notification.Builder(this, NotificationPolicy.CHANNEL_SWEEP)
            .setSmallIcon(R.drawable.ic_stat_quiet)
            .setContentTitle(SweepNotificationText.TITLE)
            .setContentText(text)
            .setContentIntent(openIntent())
            .setOngoing(true)
            .setShowWhen(false)
            .addAction(Notification.Action.Builder(null, STOP_LABEL, stop).build())
            .build()
    }

    private fun updateNotification() {
        val elapsed = if (startedAtSec > 0) System.currentTimeMillis() - startedAtSec * 1000 else 0
        val text = SweepNotificationText.summary(label, elapsed, distanceM, gpsOk)
        try {
            val nm = getSystemService(NOTIFICATION_SERVICE) as NotificationManager
            nm.notify(NOTIFICATION_ID, buildNotification(text))
        } catch (_: Throwable) {}
    }

    /** The page's mirror of the session — the quietRadio/quietPtt pattern:
     *  pushed by the host, visible surface only, never a getter. */
    private fun pushState(state: String) {
        val m = machine ?: return
        val elapsed = if (startedAtSec > 0) System.currentTimeMillis() - startedAtSec * 1000 else 0
        val json = JSONObject()
            .put("sweep_id", m.sweepId)
            .put("state", state)
            .put("label", label)
            .put("elapsed_ms", elapsed)
            .put("distance_m", distanceM)
            .put("samples", sampleCount)
            .put("gps", gpsOk)
        RuntimeController.get(this).pushSweep(json.toString())
    }

    private fun prefs() = getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    companion object {
        private const val TAG = "quiet-sweep"

        /** Its own id — a sweep is a session, never a conversation. */
        const val NOTIFICATION_ID = 3

        const val ACTION_STOP = "space.quiet.arprobe.STOP_SWEEP"
        const val EXTRA_SPACE = "space"
        const val EXTRA_PARENT = "parent"
        const val EXTRA_TASK = "task"
        const val EXTRA_NAME = "name"
        const val EXTRA_RESULT = "result"
        const val EXTRA_NOTE = "note"

        const val STOP_LABEL = "Stop"

        const val INTERVAL_MS = 15_000L
        private const val DRAIN_TRIES = 3
        private const val RECOVER_WAIT_MS = 60_000L

        private const val PREFS = "sweep"
        private const val KEY_SWEEP = "sweep_id"
        private const val KEY_LABEL = "label"
        private const val KEY_PENDING_SWEEP = "pending_sweep"
        private const val KEY_PENDING_RESULT = "pending_result"
        private const val KEY_PENDING_NOTE = "pending_note"

        /**
         * Started from a VISIBLE Activity after a person pressed the button
         * that says what it does — the first law's "explicitly started",
         * which is also exactly what Android 12+ requires of a location
         * foreground service.
         */
        @JvmStatic
        fun start(context: Context, space: String, parent: String, task: String, name: String) {
            val i = Intent(context, SweepService::class.java)
                .putExtra(EXTRA_SPACE, space)
                .putExtra(EXTRA_PARENT, parent)
                .putExtra(EXTRA_TASK, task)
                .putExtra(EXTRA_NAME, name)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                context.startForegroundService(i)
            } else {
                context.startService(i)
            }
        }

        /** Stop with the person's judgement (or without: "" → undeclared). */
        @JvmStatic
        fun requestStop(context: Context, result: String, note: String) {
            context.startService(
                Intent(context, SweepService::class.java)
                    .setAction(ACTION_STOP)
                    .putExtra(EXTRA_RESULT, result)
                    .putExtra(EXTRA_NOTE, note)
            )
        }

        /**
         * A Stop whose POST never landed is a person's judgement on hold —
         * delivered here, from the Activity, the next time the core is
         * alive. Blocking; call off the main thread.
         */
        @JvmStatic
        fun deliverPendingStop(context: Context) {
            val prefs = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
            val sweepId = prefs.getString(KEY_PENDING_SWEEP, "") ?: ""
            if (sweepId.isEmpty()) return
            val api = SweepApi { RuntimeController.get(context).apiEndpoint() }
            val verdict = api.stop(
                sweepId,
                prefs.getString(KEY_PENDING_RESULT, "") ?: "",
                prefs.getString(KEY_PENDING_NOTE, "") ?: "",
            )
            if (verdict == SweepApi.Verdict.OK) {
                prefs.edit()
                    .remove(KEY_PENDING_SWEEP).remove(KEY_PENDING_RESULT).remove(KEY_PENDING_NOTE)
                    .commit()
            }
        }
    }
}
