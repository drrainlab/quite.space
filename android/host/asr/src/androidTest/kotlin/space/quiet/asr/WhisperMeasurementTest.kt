package space.quiet.asr

import android.util.Log
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import java.io.File

/**
 * SR-0A — THE MEASUREMENT GATE, on the real phone. Reads the pinned model
 * and the spoken fixtures from /data/local/tmp (pushed by the stand), and
 * reports cold load + warm inference for 3s/8s RU/EN as logcat lines the
 * harness collects. The groom's target: warm p50 < 1.0 s, p95 < 2.0 s.
 */
@RunWith(AndroidJUnit4::class)
class WhisperMeasurementTest {

    private fun readWav(f: File): ShortArray {
        val b = f.readBytes()
        // 44-byte canonical PCM WAV header; the fixtures are known-shape.
        val n = (b.size - 44) / 2
        val out = ShortArray(n)
        for (i in 0 until n) {
            val lo = b[44 + 2 * i].toInt() and 0xff
            val hi = b[45 + 2 * i].toInt()
            out[i] = ((hi shl 8) or lo).toShort()
        }
        return out
    }

    @Test
    fun measure() {
        val name = androidx.test.platform.app.InstrumentationRegistry.getArguments()
            .getString("model", "ggml-base-q5_1.bin")
        val model = File("/data/local/tmp/$name")
        assertTrue("push the model to /data/local/tmp first", model.canRead())

        val t0 = System.nanoTime()
        val asr = WhisperAsr.open(model.absolutePath)
        val coldMs = (System.nanoTime() - t0) / 1_000_000
        assertNotNull("model failed to load", asr)
        Log.i(TAG, "MEASURE cold_load_ms=$coldMs")

        val fixtures = listOf("ru_3s", "ru_8s", "en_3s", "en_8s")
        val rt = Runtime.getRuntime()
        for (name in fixtures) {
            val f = File("/data/local/tmp/qi-speech/$name.wav")
            assertTrue("missing fixture $name", f.canRead())
            val pcm = readWav(f)
            val audioMs = pcm.size * 1000L / 16_000
            // one throwaway warm-up, then three timed runs
            asr!!.transcribe(pcm, null)
            val times = LongArray(3)
            var text: String? = null
            for (i in 0 until 3) {
                val s = System.nanoTime()
                text = asr.transcribe(pcm, null)
                times[i] = (System.nanoTime() - s) / 1_000_000
            }
            times.sort()
            val usedMb = (rt.totalMemory() - rt.freeMemory()) / (1 shl 20)
            Log.i(
                TAG,
                "MEASURE fixture=$name audio_ms=$audioMs warm_ms=${times.joinToString(",")}" +
                    " median_ms=${times[1]} heap_mb=$usedMb text=${text?.trim()}",
            )
            assertTrue("no transcript for $name", !text.isNullOrBlank())
        }
        asr!!.close()
    }

    private companion object { const val TAG = "qi-measure" }
}
