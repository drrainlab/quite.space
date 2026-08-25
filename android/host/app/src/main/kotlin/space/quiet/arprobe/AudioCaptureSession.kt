package space.quiet.arprobe

import android.annotation.SuppressLint
import android.media.AudioFormat
import android.media.AudioRecord
import android.media.MediaRecorder
import android.media.audiofx.NoiseSuppressor
import java.util.concurrent.atomic.AtomicBoolean

/**
 * SR-0 — one PTT utterance: AudioRecord, 16 kHz mono PCM16, into a
 * PREALLOCATED in-memory buffer bounded by the maximum utterance. Nothing
 * touches storage (groom §38: memory-only, destroyed on cancel/error);
 * the mic is on exactly while the button is held (§60).
 */
internal class AudioCaptureSession(
    private val maxMillis: Int,
    private val onAmplitude: (Int) -> Unit, // 0..32767, ~15 Hz
    private val onLimit: () -> Unit,        // the 12-second ceiling arrived
) {
    companion object {
        const val SAMPLE_RATE = 16_000
    }

    private val buf = ShortArray(SAMPLE_RATE * maxMillis / 1000)
    @Volatile private var filled = 0
    private val running = AtomicBoolean(false)
    private var record: AudioRecord? = null
    private var suppressor: NoiseSuppressor? = null
    private var reader: Thread? = null

    /** False when the mic could not be opened (permission, hardware). */
    @SuppressLint("MissingPermission") // the caller gates on RECORD_AUDIO
    fun start(): Boolean {
        val min = AudioRecord.getMinBufferSize(
            SAMPLE_RATE, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT,
        )
        if (min <= 0) return false
        val r = try {
            AudioRecord(
                MediaRecorder.AudioSource.VOICE_RECOGNITION,
                SAMPLE_RATE, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT,
                maxOf(min * 2, SAMPLE_RATE / 2),
            )
        } catch (_: Exception) {
            return false
        }
        if (r.state != AudioRecord.STATE_INITIALIZED) {
            r.release()
            return false
        }
        if (NoiseSuppressor.isAvailable()) {
            suppressor = try { NoiseSuppressor.create(r.audioSessionId) } catch (_: Exception) { null }
        }
        record = r
        running.set(true)
        r.startRecording()
        reader = Thread({
            val chunk = ShortArray(SAMPLE_RATE / 15) // ~66 ms → the amplitude cadence
            while (running.get() && filled < buf.size) {
                val want = minOf(chunk.size, buf.size - filled)
                val n = r.read(chunk, 0, want)
                if (n <= 0) break
                System.arraycopy(chunk, 0, buf, filled, n)
                filled += n
                var peak = 0
                for (i in 0 until n) {
                    val a = if (chunk[i] >= 0) chunk[i].toInt() else -chunk[i].toInt()
                    if (a > peak) peak = a
                }
                onAmplitude(peak)
            }
            if (running.get() && filled >= buf.size) onLimit()
        }, "qp-ptt-capture")
        reader!!.start()
        return true
    }

    /** Stop and hand over the utterance. Empty when nothing was captured. */
    fun stopAndTake(): ShortArray {
        shutdown()
        return buf.copyOf(filled)
    }

    /** The buffer dies here — a cancelled utterance leaves no bytes behind. */
    fun cancel() {
        shutdown()
        buf.fill(0)
        filled = 0
    }

    private fun shutdown() {
        running.set(false)
        reader?.join(500)
        reader = null
        try { record?.stop() } catch (_: Exception) {}
        suppressor?.release(); suppressor = null
        record?.release(); record = null
    }
}
