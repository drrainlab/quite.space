package space.quiet.arprobe

import android.content.Context
import android.util.Log
import java.io.File
import java.net.HttpURLConnection
import java.net.URL
import java.security.MessageDigest

/**
 * SR-0 — the ASR model artifact: fetched ONCE, on the first enabling of
 * Radio Mode, from a pinned URL, verified against a pinned sha256 before
 * a byte of it is trusted, kept in filesDir. No speech ever leaves the
 * device — the model is a public one-time download, which is a different
 * fact, stated here so nobody conflates them.
 */
internal class ModelStore(context: Context) {
    companion object {
        // Whisper multilingual base, q5_1 — the groom's first model class.
        // The measurement gate may repin to q4 / tiny; both constants move
        // together, never one without the other.
        const val MODEL_FILE = "ggml-base-q5_1.bin"
        const val MODEL_URL =
            "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-base-q5_1.bin"
        const val MODEL_SHA256 =
            "422f1ae452ade6f30a004d7e5c6a43195e4433bc370bf23fac9cc591f01a8898"
        const val MODEL_BYTES = 59_707_625L
    }

    private val dir = File(context.filesDir, "models")

    fun path(): File = File(dir, MODEL_FILE)

    fun ready(): Boolean = path().isFile && path().length() == MODEL_BYTES

    /**
     * Download and verify; blocking, call on a worker. onProgress in
     * [0..100]. False = not fetched (offline, out of space, bad hash) —
     * the toggle stays honest and PTT reports "preparing".
     */
    fun ensure(onProgress: (Int) -> Unit = {}): Boolean {
        if (ready()) return true
        dir.mkdirs()
        val tmp = File(dir, "$MODEL_FILE.part")
        try {
            val conn = URL(MODEL_URL).openConnection() as HttpURLConnection
            conn.connectTimeout = 10_000
            conn.readTimeout = 60_000
            conn.instanceFollowRedirects = true
            if (conn.responseCode !in 200..299) return false
            val digest = MessageDigest.getInstance("SHA-256")
            conn.inputStream.use { input ->
                tmp.outputStream().use { out ->
                    val buf = ByteArray(1 shl 16)
                    var got = 0L
                    while (true) {
                        val n = input.read(buf)
                        if (n < 0) break
                        out.write(buf, 0, n)
                        digest.update(buf, 0, n)
                        got += n
                        onProgress(((got * 100) / MODEL_BYTES).toInt().coerceAtMost(99))
                    }
                }
            }
            val sum = digest.digest().joinToString("") { "%02x".format(it) }
            if (sum != MODEL_SHA256 || tmp.length() != MODEL_BYTES) {
                Log.w("quiet-asr", "model artifact rejected: hash/size mismatch")
                tmp.delete()
                return false
            }
            if (!tmp.renameTo(path())) return false
            onProgress(100)
            return true
        } catch (e: Exception) {
            Log.w("quiet-asr", "model fetch failed: ${e.javaClass.simpleName}")
            tmp.delete()
            return false
        }
    }

    fun delete() {
        path().delete()
    }
}
