package space.quiet.arprobe

import java.util.UUID
import java.util.concurrent.atomic.AtomicBoolean

/**
 * SR-0 — the push-to-talk state machine, and the owner's invariant made
 * structural: PTT IS NOT A RADIO FEATURE. This controller is a reusable
 * speech-input primitive; it does not know whether press() came from the
 * on-screen button, a headset key, or a car control — every source calls
 * the same three verbs. Radio Mode is merely the UX configuration that
 * makes it primary.
 *
 *   IDLE → press → ARMING → (threshold) → RECORDING → release → FINALIZING
 *        → transcript → SENDING → sent → IDLE
 * with ARMING→IDLE (released early), RECORDING→IDLE (cancel, buffer
 * destroyed), FINALIZING→IDLE (cancel aborts inference AND the send),
 * empty/error states surfaced to the listener, and a hard 12-second
 * ceiling that finalizes on its own (groom §12).
 *
 * NO ANDROID TYPES: capture, engine, sender, clock and executor arrive as
 * seams, so every transition is a plain JVM test — the
 * NotificationCoordinator discipline.
 */
internal class PttController(
    private val captureFactory: (onAmplitude: (Int) -> Unit, onLimit: () -> Unit) -> Capture,
    private val engine: () -> SpeechInputEngine?,
    private val send: (spaceId: String, text: String, clientRef: String) -> Boolean,
    private val listener: (Event) -> Unit,
    private val execute: (Runnable) -> Unit, // one background lane: ASR + send
    private val armingMillis: Long = 120,
    private val now: () -> Long = System::currentTimeMillis,
) {
    /** What a capture must be able to do; AudioCaptureSession is the real one. */
    interface Capture {
        fun start(): Boolean
        fun stopAndTake(): ShortArray
        fun cancel()
    }

    enum class State { IDLE, ARMING, RECORDING, FINALIZING, SENDING }

    /** Everything a surface needs to render, pushed — never polled. */
    data class Event(
        val state: State,
        val amplitude: Int = 0,
        val transcript: String? = null,
        val error: Error? = null,
        val limitReached: Boolean = false,
    )

    enum class Error { MIC_UNAVAILABLE, ENGINE_UNAVAILABLE, EMPTY, ASR_FAILED, SEND_FAILED }

    @Volatile private var state = State.IDLE
    private var capture: Capture? = null
    private var spaceId: String = ""
    private var clientRef: String = ""
    private var pressedAt = 0L
    private var generation = 0L // a cancel invalidates work already in flight
    private val cancelled = AtomicBoolean(false)

    fun state(): State = state

    /** Half-duplex hook: playback holds its queue while this is not IDLE. */
    fun busy(): Boolean = state != State.IDLE

    @Synchronized
    fun press(space: String): Boolean {
        if (state != State.IDLE) return false
        val eng = engine()
        if (eng == null || !eng.prepare()) {
            listener(Event(State.IDLE, error = Error.ENGINE_UNAVAILABLE))
            return false
        }
        val cap = captureFactory({ amp -> onAmplitude(amp) }, { onLimit() })
        if (!cap.start()) {
            listener(Event(State.IDLE, error = Error.MIC_UNAVAILABLE))
            return false
        }
        capture = cap
        spaceId = space
        // Minted at RECORDING start so a retry after a crash or timeout is
        // idempotent against handleSay's (space, client_ref) memory.
        clientRef = UUID.randomUUID().toString().replace("-", "").take(32)
        pressedAt = now()
        cancelled.set(false)
        generation++
        state = State.ARMING
        listener(Event(State.ARMING))
        return true
    }

    @Synchronized
    fun release(): Boolean {
        when (state) {
            State.ARMING -> {
                // Released before the threshold: an accidental tap, not an
                // utterance (groom §11). Nothing is transcribed or sent.
                if (now() - pressedAt < armingMillis) {
                    capture?.cancel(); capture = null
                    state = State.IDLE
                    listener(Event(State.IDLE))
                    return true
                }
                return finalizeUtterance()
            }
            State.RECORDING -> return finalizeUtterance()
            else -> return false
        }
    }

    @Synchronized
    fun cancel(): Boolean {
        when (state) {
            State.ARMING, State.RECORDING -> {
                capture?.cancel(); capture = null // the buffer dies here
                state = State.IDLE
                listener(Event(State.IDLE))
                return true
            }
            State.FINALIZING, State.SENDING -> {
                // Aborts the inference (the engine polls cancelled) and
                // orphans any in-flight work: its generation no longer
                // matches, so its result is dropped on arrival.
                cancelled.set(true)
                generation++
                state = State.IDLE
                listener(Event(State.IDLE))
                return true
            }
            else -> return false
        }
    }

    private fun onAmplitude(peak: Int) {
        synchronized(this) {
            if (state == State.ARMING && now() - pressedAt >= armingMillis) {
                state = State.RECORDING
                listener(Event(State.RECORDING))
            }
            if (state == State.RECORDING) listener(Event(State.RECORDING, amplitude = peak))
        }
    }

    private fun onLimit() {
        synchronized(this) {
            if (state == State.RECORDING || state == State.ARMING) {
                listener(Event(state, limitReached = true))
                finalizeUtterance()
            }
        }
    }

    // Callers hold the monitor.
    private fun finalizeUtterance(): Boolean {
        val cap = capture ?: return false
        capture = null
        val pcm = cap.stopAndTake()
        state = State.FINALIZING
        listener(Event(State.FINALIZING))
        val gen = generation
        val space = spaceId
        val ref = clientRef
        execute {
            val eng = engine()
            val result = if (eng == null) {
                SpeechInputEngine.Result.Error("engine gone")
            } else {
                eng.transcribe(pcm, AudioCaptureSession.SAMPLE_RATE) { cancelled.get() }
            }
            deliver(gen, space, ref, result)
        }
        return true
    }

    private fun deliver(gen: Long, space: String, ref: String, result: SpeechInputEngine.Result) {
        val text: String
        synchronized(this) {
            if (gen != generation) return // cancelled while we worked
            when (result) {
                is SpeechInputEngine.Result.Empty -> {
                    state = State.IDLE
                    listener(Event(State.IDLE, error = Error.EMPTY))
                    return
                }
                is SpeechInputEngine.Result.Error -> {
                    state = State.IDLE
                    listener(Event(State.IDLE, error = Error.ASR_FAILED))
                    return
                }
                is SpeechInputEngine.Result.Text -> {
                    text = result.text.trim()
                    if (text.isEmpty()) {
                        state = State.IDLE
                        listener(Event(State.IDLE, error = Error.EMPTY))
                        return
                    }
                    state = State.SENDING
                    // The transcript is SHOWN before/while sending (groom §13):
                    // the person sees what the system understood.
                    listener(Event(State.SENDING, transcript = text))
                }
            }
        }
        val ok = send(space, text, ref)
        synchronized(this) {
            if (gen != generation) return
            state = State.IDLE
            listener(
                if (ok) Event(State.IDLE, transcript = text)
                else Event(State.IDLE, transcript = text, error = Error.SEND_FAILED),
            )
        }
    }
}
