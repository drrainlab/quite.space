package space.quiet.arprobe

import android.content.Context
import android.media.AudioAttributes
import android.media.AudioFocusRequest
import android.media.AudioManager
import android.os.Build
import android.speech.tts.TextToSpeech
import android.speech.tts.UtteranceProgressListener
import android.util.Log
import java.util.Locale

/**
 * SR-0 — the system TextToSpeech behind the SpeechOutputEngine seam.
 *
 * OFFLINE OR SILENT: after init the engine looks for a voice that does
 * not require the network for the utterance's language (or the device
 * locale); if none exists it reports unavailable and nothing will ever
 * speak — a message routed through a cloud voice is a leak, not a
 * fallback (groom §29).
 *
 * Audio focus: TRANSIENT_MAY_DUCK per utterance — the podcast dips, the
 * call is never broken, DND is never bypassed. If focus is refused the
 * utterance is skipped (the message is on screen regardless). The OS
 * keeps routing: speaker, headphones, car — never forced (groom §34-35).
 */
internal class AndroidTtsEngine(context: Context) : SpeechOutputEngine {
    private val app = context.applicationContext
    private val audio = app.getSystemService(Context.AUDIO_SERVICE) as AudioManager
    private var tts: TextToSpeech? = null
    @Volatile private var ready = false
    private var focus: AudioFocusRequest? = null
    private val attrs = AudioAttributes.Builder()
        .setUsage(AudioAttributes.USAGE_ASSISTANCE_NAVIGATION_GUIDANCE)
        .setContentType(AudioAttributes.CONTENT_TYPE_SPEECH)
        .build()
    private val done = java.util.concurrent.ConcurrentHashMap<String, (Boolean) -> Unit>()

    override fun prepare(onReady: (Boolean) -> Unit) {
        if (tts != null) {
            onReady(ready)
            return
        }
        tts = TextToSpeech(app) { status ->
            if (status != TextToSpeech.SUCCESS) {
                ready = false
                onReady(false)
                return@TextToSpeech
            }
            val t = tts!!
            t.setAudioAttributes(attrs)
            ready = hasOfflineVoice(t)
            t.setOnUtteranceProgressListener(object : UtteranceProgressListener() {
                override fun onStart(id: String?) {}
                override fun onDone(id: String?) = finish(id, true)
                @Deprecated("platform")
                override fun onError(id: String?) = finish(id, false)
                override fun onError(id: String?, code: Int) = finish(id, false)
            })
            onReady(ready)
        }
    }

    private fun hasOfflineVoice(t: TextToSpeech): Boolean = try {
        t.voices?.any { !it.isNetworkConnectionRequired } == true
    } catch (_: Exception) {
        false
    }

    private fun finish(id: String?, ok: Boolean) {
        abandonFocus()
        val cb = id?.let { done.remove(it) } ?: return
        cb(ok)
    }

    override fun speak(utteranceId: String, text: String, language: String?, onDone: (ok: Boolean) -> Unit) {
        val t = tts
        if (t == null || !ready) {
            onDone(false)
            return
        }
        // The user's explicit language override rides in as "ru"/"en";
        // an offline voice for it is required, or we fall to the default.
        if (language != null) {
            val loc = Locale(language)
            val v = t.voices?.firstOrNull {
                it.locale.language == loc.language && !it.isNetworkConnectionRequired
            }
            if (v != null) t.voice = v
        }
        if (!requestFocus()) {
            onDone(false) // the message is still on screen; speech is polite
            return
        }
        done[utteranceId] = onDone
        val r = t.speak(text, TextToSpeech.QUEUE_ADD, null, utteranceId)
        if (r != TextToSpeech.SUCCESS) finish(utteranceId, false)
    }

    private fun requestFocus(): Boolean {
        return if (Build.VERSION.SDK_INT >= 26) {
            val req = AudioFocusRequest.Builder(AudioManager.AUDIOFOCUS_GAIN_TRANSIENT_MAY_DUCK)
                .setAudioAttributes(attrs)
                .build()
            focus = req
            audio.requestAudioFocus(req) == AudioManager.AUDIOFOCUS_REQUEST_GRANTED
        } else {
            @Suppress("DEPRECATION")
            audio.requestAudioFocus(null, AudioManager.STREAM_MUSIC,
                AudioManager.AUDIOFOCUS_GAIN_TRANSIENT_MAY_DUCK) ==
                AudioManager.AUDIOFOCUS_REQUEST_GRANTED
        }
    }

    private fun abandonFocus() {
        if (Build.VERSION.SDK_INT >= 26) {
            focus?.let { audio.abandonAudioFocusRequest(it) }
            focus = null
        } else {
            @Suppress("DEPRECATION")
            audio.abandonAudioFocus(null)
        }
    }

    override fun stop() {
        try { tts?.stop() } catch (e: Exception) { Log.w("quiet-tts", "stop", e) }
        // The platform does not deliver onDone for stopped utterances
        // reliably; every pending callback is settled as not-ok.
        val ids = done.keys.toList()
        for (id in ids) finish(id, false)
    }

    override fun release() {
        stop()
        tts?.shutdown()
        tts = null
        ready = false
    }
}
