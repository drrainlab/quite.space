package space.quiet.asr

/**
 * SR-0 — the thin Kotlin face of the whisper.cpp JNI. A plain class on
 * purpose: this module knows nothing of Quiet, engines or UI — the app
 * adapts it behind its own SpeechInputEngine seam, which is what lets a
 * different backend slot in at the measurement gate.
 */
class WhisperAsr private constructor(private var handle: Long) {

    /** null = the model would not load. */
    companion object {
        init {
            System.loadLibrary("quietasr")
        }

        fun open(modelPath: String): WhisperAsr? {
            val h = nativeInit(modelPath)
            return if (h == 0L) null else WhisperAsr(h)
        }

        @JvmStatic private external fun nativeInit(modelPath: String): Long
        @JvmStatic private external fun nativeTranscribe(
            handle: Long, pcm: ShortArray, language: String?, threads: Int,
        ): String?
        @JvmStatic private external fun nativeCancel(handle: Long)
        @JvmStatic private external fun nativeFree(handle: Long)
    }

    /** Batch transcription; null = failed or cancelled. Blocking. */
    fun transcribe(pcm: ShortArray, language: String?, threads: Int = 4): String? {
        val h = handle
        if (h == 0L) return null
        return nativeTranscribe(h, pcm, language, threads)
    }

    /** Aborts a transcription in flight (the abort callback polls this). */
    fun cancel() {
        val h = handle
        if (h != 0L) nativeCancel(h)
    }

    fun close() {
        val h = handle
        handle = 0
        if (h != 0L) nativeFree(h)
    }
}
