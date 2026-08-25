package space.quiet.arprobe

/**
 * SR-0 — the seam every ASR backend sits behind. The UI, the PTT state
 * machine and the send path never know which engine ran: whisper.cpp
 * today, sherpa-onnx tomorrow, a fake in tests and in builds where the
 * native module is absent. Inference is BATCH: one bounded utterance in,
 * one transcript out — never streaming (groom §60: mic on only while
 * the button is held).
 */
internal interface SpeechInputEngine {
    /** Load the model; idempotent; false = not available on this build/device. */
    fun prepare(): Boolean

    fun release()

    /**
     * Transcribe one utterance. Called on a background executor; checks
     * [cancelled] between decode steps so a cancel actually stops work.
     */
    fun transcribe(pcm: ShortArray, sampleRateHz: Int, cancelled: () -> Boolean): Result

    sealed interface Result {
        data class Text(val text: String, val language: String?) : Result
        /** Silence, whitespace, or the model heard no speech. Not an error. */
        object Empty : Result
        data class Error(val why: String) : Result
    }
}

/**
 * The stand-in engine: JVM tests script it, and a debug build without the
 * native ASR module uses it so the whole PTT path stays walkable. It
 * NEVER pretends to recognize — the produced text says what it is.
 */
internal class FakeSpeechInputEngine(
    @Volatile var script: ((ShortArray) -> SpeechInputEngine.Result)? = null,
) : SpeechInputEngine {
    @Volatile var prepared = false
        private set

    override fun prepare(): Boolean {
        prepared = true
        return true
    }

    override fun release() {
        prepared = false
    }

    override fun transcribe(
        pcm: ShortArray,
        sampleRateHz: Int,
        cancelled: () -> Boolean,
    ): SpeechInputEngine.Result {
        val s = script
        if (s != null) return s(pcm)
        if (pcm.isEmpty()) return SpeechInputEngine.Result.Empty
        val secs = pcm.size / sampleRateHz.coerceAtLeast(1)
        return SpeechInputEngine.Result.Text("[voice ${secs}s — offline ASR not installed]", null)
    }
}
