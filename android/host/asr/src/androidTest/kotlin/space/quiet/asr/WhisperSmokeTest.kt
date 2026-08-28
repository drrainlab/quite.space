package space.quiet.asr

import android.util.Log
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import java.io.File

/** SR-0A step 0: where exactly does it stop — library, file, init? */
@RunWith(AndroidJUnit4::class)
class WhisperSmokeTest {
    @Test
    fun stepByStep() {
        Log.i("qi-measure", "STEP loadLibrary begin")
        System.loadLibrary("quietasr")
        Log.i("qi-measure", "STEP loadLibrary ok")
        val model = File("/data/local/tmp/ggml-base-q5_1.bin")
        Log.i("qi-measure", "STEP canRead=${model.canRead()} len=${model.length()}")
        assertTrue(model.canRead())
        val bytes = model.inputStream().use { it.read(ByteArray(16)) }
        Log.i("qi-measure", "STEP read16=$bytes")
        val t0 = System.nanoTime()
        Log.i("qi-measure", "STEP open begin")
        val asr = WhisperAsr.open(model.absolutePath)
        Log.i("qi-measure", "STEP open done ms=${(System.nanoTime() - t0) / 1_000_000} ok=${asr != null}")
        asr?.close()
    }
}

@RunWith(AndroidJUnit4::class)
class WhisperSingleThreadTest {
    @Test
    fun oneThreadTranscribes() {
        val model = File("/data/local/tmp/ggml-base-q5_1.bin")
        val asr = WhisperAsr.open(model.absolutePath)!!
        val f = File("/data/local/tmp/qi-speech/ru_3s.wav")
        val b = f.readBytes()
        val pcm = ShortArray((b.size - 44) / 2)
        for (i in pcm.indices) pcm[i] = (((b[45 + 2 * i].toInt()) shl 8) or (b[44 + 2 * i].toInt() and 0xff)).toShort()
        val t0 = System.nanoTime()
        val text = asr.transcribe(pcm, "ru", threads = 1)
        Log.i("qi-measure", "STEP t1 ms=${(System.nanoTime() - t0) / 1_000_000} text=$text")
        asr.close()
        assertTrue(!text.isNullOrBlank())
    }
}
