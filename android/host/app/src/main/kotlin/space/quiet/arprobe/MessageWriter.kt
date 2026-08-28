package space.quiet.arprobe

import android.util.Log
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL

/**
 * SR-0 — the first NATIVE writer: an ordinary Quiet text message over the
 * node's own loopback API, exactly what the page's outbox does (groom §21:
 * a transcript goes through the normal send path, never near a radio
 * adapter). Idempotent by client_ref — handleSay remembers (space, ref) —
 * so a retry after a timeout cannot double-send.
 */
internal class MessageWriter(
    private val endpoint: () -> Pair<Int, String>?, // (api port, session token)
) {
    /** Blocking; call on a background executor. */
    fun send(spaceId: String, text: String, clientRef: String): Boolean {
        val (port, token) = endpoint() ?: return false
        return try {
            val conn = URL("http://127.0.0.1:$port/api/spaces/$spaceId/messages")
                .openConnection() as HttpURLConnection
            conn.requestMethod = "POST"
            conn.connectTimeout = 4000
            conn.readTimeout = 10000
            conn.setRequestProperty("X-QP-Token", token)
            conn.setRequestProperty("Content-Type", "application/json")
            conn.doOutput = true
            val body = JSONObject().put("text", text).put("client_ref", clientRef)
            conn.outputStream.use { it.write(body.toString().toByteArray(Charsets.UTF_8)) }
            val ok = conn.responseCode in 200..299
            conn.inputStream.use { it.readBytes() }
            ok
        } catch (e: Exception) {
            Log.w("quiet-ptt", "send failed: ${e.javaClass.simpleName}")
            false
        }
    }
}
