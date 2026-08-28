package space.quiet.arprobe

import android.util.Log
import org.json.JSONArray
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL

/**
 * SP-3.2 — the sweep pump's side of the node's loopback API.
 *
 * THE MESSAGEWRITER DISCIPLINE, EXTENDED: the recording ENGINE lives in the
 * Go node — spool, saga, sealing, the completion event are all its. The
 * host is a sensor with a battery, and this class is the whole of how it
 * speaks: blocking HTTP on a background executor, the endpoint re-read on
 * EVERY call because the core clears its token when it stops and mints a
 * new one when it opens — a cached pair would talk to a node that no
 * longer exists.
 *
 * ANSWERS ARE THREE-VALUED because two different failures demand opposite
 * reactions: a transport failure means "keep the samples, try again" and a
 * 409 means "the session is over, stop pumping" — collapsing them into one
 * boolean is how a service ends up retrying into a closed door forever.
 */
internal class SweepApi(
    private val endpoint: () -> Pair<Int, String>?,
) {

    enum class Verdict { OK, CLOSED, FAILED }

    data class Info(
        val sweepId: String,
        val space: String,
        val label: String,
        val startedAt: Long,
        val samples: Long,
        val distanceM: Long,
    )

    /** Start a sweep on the node; null when it refused or was unreachable. */
    fun start(spaceId: String, parentObjectId: String, taskId: String, name: String): Info? {
        val body = JSONObject()
            .put("parent_object_id", parentObjectId)
            .put("task_id", taskId)
            .put("name", name)
        val (code, resp) = call("POST", "/api/spaces/$spaceId/sweeps", body) ?: return null
        if (code !in 200..299 || resp == null) return null
        return info(resp)
    }

    /** One batch under one seq — the node's spool makes the retry a no-op. */
    fun samples(sweepId: String, batch: SweepSessionMachine.Batch): Pair<Verdict, Info?> {
        val arr = JSONArray()
        for (s in batch.samples) {
            val o = JSONObject().put("unix_ms", s.unixMs)
            if (s.gap) {
                o.put("kind", "gap").put("duration_ms", s.durationMs).put("reason", s.reason)
            } else {
                o.put("kind", "point").put("lat", s.lat).put("lon", s.lon)
                    .put("accuracy_m", s.accuracyM)
            }
            arr.put(o)
        }
        val body = JSONObject().put("seq", batch.seq).put("samples", arr)
        val (code, resp) = call("POST", "/api/sweeps/$sweepId/samples", body)
            ?: return Verdict.FAILED to null
        return when {
            code in 200..299 -> Verdict.OK to resp?.let { info(it) }
            code == 409 -> Verdict.CLOSED to null
            else -> Verdict.FAILED to null
        }
    }

    /** The sticky-restart claim: suspended → recording, one honest gap. */
    fun resume(sweepId: String): Verdict {
        val (code, _) = call("POST", "/api/sweeps/$sweepId/resume", JSONObject())
            ?: return Verdict.FAILED
        return when {
            code in 200..299 -> Verdict.OK
            code == 409 -> Verdict.CLOSED
            else -> Verdict.FAILED
        }
    }

    /**
     * Stop. An empty result is DELIBERATE vocabulary — the node records
     * `undeclared`: the person pressed Stop without giving judgement, and
     * the completion fact travels now rather than waiting for prose.
     * A 409 is success in different clothes: someone else already closed it.
     */
    fun stop(sweepId: String, result: String, note: String): Verdict {
        val body = JSONObject().put("result", result).put("note", note)
        val (code, _) = call("POST", "/api/sweeps/$sweepId/stop", body)
            ?: return Verdict.FAILED
        return when {
            code in 200..299 || code == 409 -> Verdict.OK
            else -> Verdict.FAILED
        }
    }

    /** The node's truth about live sessions — the discovery the sticky
     *  restart trusts INSTEAD of its own remembered intent. */
    fun actives(): List<Info>? {
        val (code, resp) = call("GET", "/api/sweeps", null) ?: return null
        if (code !in 200..299 || resp == null) return null
        val arr = resp.optJSONArray("sweeps") ?: return emptyList()
        val out = ArrayList<Info>(arr.length())
        for (i in 0 until arr.length()) out.add(info(arr.getJSONObject(i)))
        return out
    }

    private fun info(o: JSONObject) = Info(
        sweepId = o.optString("sweep_id"),
        space = o.optString("space"),
        label = o.optString("label"),
        startedAt = o.optLong("started_at"),
        samples = o.optLong("samples"),
        distanceM = o.optLong("distance_m"),
    )

    /** null = transport never concluded; otherwise (status, parsed body?). */
    private fun call(method: String, path: String, body: JSONObject?): Pair<Int, JSONObject?>? {
        val (port, token) = endpoint() ?: return null
        return try {
            val conn = URL("http://127.0.0.1:$port$path").openConnection() as HttpURLConnection
            conn.requestMethod = method
            conn.connectTimeout = 4000
            conn.readTimeout = 10000
            conn.setRequestProperty("X-QP-Token", token)
            if (body != null) {
                conn.setRequestProperty("Content-Type", "application/json")
                conn.doOutput = true
                conn.outputStream.use { it.write(body.toString().toByteArray(Charsets.UTF_8)) }
            }
            val code = conn.responseCode
            val stream = if (code in 200..299) conn.inputStream else conn.errorStream
            val text = stream?.use { it.readBytes().toString(Charsets.UTF_8) } ?: ""
            val parsed = if (text.startsWith("{")) runCatching { JSONObject(text) }.getOrNull() else null
            code to parsed
        } catch (e: Exception) {
            Log.w(TAG, "sweep api $path: ${e.javaClass.simpleName}")
            null
        }
    }

    private companion object {
        const val TAG = "quiet-sweep"
    }
}
