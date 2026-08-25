package space.quiet.arprobe

import space.quiet.asr.WhisperAsr

/**
 * SR-0 — whisper.cpp adapted to the SpeechInputEngine seam. Batch-only by
 * design: one bounded utterance after PTT release; cancel reaches the
 * native abort callback, so a cancelled inference actually stops.
 */
internal class WhisperEngine(
    private val modelPath: () -> String?,
    private val languageHint: () -> String? = { null }, // "ru"/"en"/null=auto
) : SpeechInputEngine {
    @Volatile private var asr: WhisperAsr? = null

    override fun prepare(): Boolean {
        if (asr != null) return true
        val path = modelPath() ?: return false
        return try {
            asr = WhisperAsr.open(path)
            asr != null
        } catch (_: Throwable) {
            // The native library is absent on this build: the caller falls
            // back to the fake, and the failure is a state, not a crash.
            false
        }
    }

    override fun release() {
        asr?.close()
        asr = null
    }

    override fun transcribe(
        pcm: ShortArray,
        sampleRateHz: Int,
        cancelled: () -> Boolean,
    ): SpeechInputEngine.Result {
        val a = asr ?: return SpeechInputEngine.Result.Error("model not loaded")
        if (cancelled()) return SpeechInputEngine.Result.Error("cancelled")
        // The native abort callback needs a push, not a poll from our side:
        // watch the flag on a tiny thread for the duration of the call.
        val watcher = Thread {
            while (!cancelled()) {
                try { Thread.sleep(50) } catch (_: InterruptedException) { return@Thread }
            }
            a.cancel()
        }
        watcher.isDaemon = true
        watcher.start()
        val text = try {
            a.transcribe(pcm, languageHint())
        } finally {
            watcher.interrupt()
        }
        if (text == null) {
            return if (cancelled()) SpeechInputEngine.Result.Error("cancelled")
            else SpeechInputEngine.Result.Error("inference failed")
        }
        val trimmed = text.trim()
        // Whisper marks non-speech with bracketed annotations; alone they
        // mean the model heard no words.
        val bare = trimmed.replace(Regex("\\[[^\\]]*\\]|\\([^)]*\\)"), "").trim()
        if (bare.isEmpty()) return SpeechInputEngine.Result.Empty
        return SpeechInputEngine.Result.Text(trimmed, null)
    }
}
