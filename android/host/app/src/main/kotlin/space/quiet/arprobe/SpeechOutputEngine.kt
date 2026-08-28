package space.quiet.arprobe

/**
 * SR-0 — the seam every TTS backend sits behind. Android's system
 * TextToSpeech today; a packaged local engine (Piper / sherpa-onnx)
 * later, with no caller the wiser.
 *
 * The one law every backend must keep: OFFLINE OR SILENT. A voice that
 * needs the network is refused rather than silently used — speech that
 * routes a private message through somebody's cloud is not a fallback,
 * it is a leak (groom §29).
 */
internal interface SpeechOutputEngine {
    /** onReady(false) = no offline voice on this device: nothing will speak. */
    fun prepare(onReady: (offlineVoiceAvailable: Boolean) -> Unit)

    /** Speak one utterance; exactly one onDone however it ends. */
    fun speak(utteranceId: String, text: String, language: String?, onDone: (ok: Boolean) -> Unit)

    /** Stop the current utterance, if any (half-duplex, shutdown). */
    fun stop()

    fun release()
}

/** The JVM stand-in: scripts completions, records what would have sounded. */
internal class FakeSpeechOutputEngine(
    private val offlineVoice: Boolean = true,
) : SpeechOutputEngine {
    val spoken = mutableListOf<Pair<String, String>>() // (utteranceId, text)
    var stops = 0
        private set
    private var pending: ((Boolean) -> Unit)? = null

    override fun prepare(onReady: (Boolean) -> Unit) = onReady(offlineVoice)

    override fun speak(utteranceId: String, text: String, language: String?, onDone: (ok: Boolean) -> Unit) {
        spoken.add(utteranceId to text)
        pending = onDone
    }

    /** The test's hand on the clock: the current utterance finishes. */
    fun finishCurrent(ok: Boolean = true) {
        val d = pending ?: return
        pending = null
        d(ok)
    }

    override fun stop() {
        stops++
        val d = pending
        pending = null
        d?.invoke(false)
    }

    override fun release() {}
}
