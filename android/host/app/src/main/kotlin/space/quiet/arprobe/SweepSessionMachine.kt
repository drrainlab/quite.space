package space.quiet.arprobe

/**
 * SP-3.2 — the recorder's bookkeeping, kept away from Android so every claim
 * in it can be tested as the claim it is.
 *
 * THE MACHINE OWNS THREE THINGS AND NOTHING ELSE: the queue of samples not
 * yet safely on the node's disk, the batch sequence number, and the fact
 * that capture has closed. It does not touch a sensor, a socket, or a
 * clock — the service hands facts in and carries batches out.
 *
 * WHY BATCHES HAVE A SEQUENCE NUMBER AND WHY IT NEVER MOVES ON FAILURE.
 * The node writes one host batch as one spool record and remembers the last
 * seq it applied; a retry of the same seq is a structural no-op there. So
 * the exactly-once property of the pipeline lives in two dumb rules on this
 * side: one batch in flight at a time, and the seq advances only when the
 * node has answered. A timeout is indistinguishable from "it landed and the
 * answer was lost", and with these rules that ambiguity is harmless — the
 * retry re-sends the same record and the node shrugs.
 *
 * OVERFLOW IS A GAP, NOT A SECRET. The queue is bounded because the node
 * may be unreachable for a long time on a phone that keeps hearing GPS.
 * When it fills, the oldest points are dropped — and the drop itself is
 * recorded as a gap the reader must consume, with reason `unknown`: the
 * recorder knows it lost them, and "I lost them" is not a cause. Promoting
 * it to `no_fix` would claim the sky went dark when only a buffer did.
 */
internal class SweepSessionMachine(
    val sweepId: String,
    private val capacity: Int = DEFAULT_CAPACITY,
) {

    /** One captured fact, in the shape the node's /samples body takes. */
    data class Sample(
        val gap: Boolean,
        val unixMs: Long,
        val lat: Double = 0.0,
        val lon: Double = 0.0,
        val accuracyM: Long = 0,
        val durationMs: Long = 0,
        val reason: String = "",
    )

    data class Batch(val seq: Long, val samples: List<Sample>)

    private val queue = ArrayDeque<Sample>()
    private var seq = 0L
    private var outstanding = false // a batch is out and unanswered
    private var inFlight = 0        // how many of queue's head that batch carries
    private var closed = false

    /** The dropped-region gap, held OUTSIDE the ring so it can never itself
     *  be dropped; it travels at the front of the next batch. */
    private var overflowGapStartMs = 0L
    private var overflowGapEndMs = 0L

    val lastSeq: Long get() = synchronized(this) { seq }
    val pending: Int get() = synchronized(this) { queue.size }
    val isClosed: Boolean get() = synchronized(this) { closed }

    /** A GPS fix. Refused after close: Stop forbids capture, structurally. */
    fun onPoint(unixMs: Long, lat: Double, lon: Double, accuracyM: Long): Boolean =
        offer(Sample(gap = false, unixMs = unixMs, lat = lat, lon = lon, accuracyM = accuracyM))

    /** A gap the service classified. Same closed rule as a point. */
    fun onGap(unixMs: Long, durationMs: Long, reason: String): Boolean =
        offer(Sample(gap = true, unixMs = unixMs, durationMs = durationMs, reason = reason))

    private fun offer(s: Sample): Boolean = synchronized(this) {
        if (closed) return false
        // Never evict while a batch is out — its head samples are spoken
        // for, and the seq contract says a retry re-sends the SAME record.
        // The bound is soft for exactly that window: a sample every 15 s
        // against an HTTP call of seconds overshoots by at most a few.
        while (!outstanding && queue.size >= capacity) {
            val dropped = queue.removeFirst()
            val end = if (dropped.gap) dropped.unixMs + dropped.durationMs else dropped.unixMs
            if (overflowGapStartMs == 0L) overflowGapStartMs = dropped.unixMs
            if (end > overflowGapEndMs) overflowGapEndMs = end
        }
        queue.addLast(s)
        true
    }

    /**
     * Snapshot the next batch, or null when there is nothing to send or a
     * batch is already out. The samples are NOT removed — [delivered] does
     * that, [failed] merely re-arms the same batch.
     */
    fun takeBatch(limit: Int = DEFAULT_BATCH): Batch? = synchronized(this) {
        if (outstanding) return null
        val out = ArrayList<Sample>(limit)
        if (overflowGapStartMs != 0L && overflowGapEndMs > overflowGapStartMs) {
            out.add(
                Sample(
                    gap = true, unixMs = overflowGapStartMs,
                    durationMs = overflowGapEndMs - overflowGapStartMs,
                    reason = "unknown",
                )
            )
        }
        val n = minOf(limit - out.size, queue.size)
        for (i in 0 until n) out.add(queue[i])
        if (out.isEmpty()) return null
        outstanding = true
        inFlight = n
        return Batch(seq + 1, out)
    }

    /** The node answered for [batchSeq]: the seq advances, the head leaves. */
    fun delivered(batchSeq: Long) = synchronized(this) {
        // A stale or duplicate answer changes nothing.
        if (!outstanding || batchSeq != seq + 1) return
        seq = batchSeq
        repeat(inFlight) { if (queue.isNotEmpty()) queue.removeFirst() }
        outstanding = false
        inFlight = 0
        overflowGapStartMs = 0L
        overflowGapEndMs = 0L
    }

    /** The send did not conclude; the same batch goes again under the same seq. */
    fun failed() = synchronized(this) {
        outstanding = false
        inFlight = 0
    }

    /** Stop forbids capture — from here on [onPoint]/[onGap] refuse.
     *  What is already queued may still drain: finalization is not capture. */
    fun close() = synchronized(this) { closed = true }

    companion object {
        const val DEFAULT_CAPACITY = 512
        const val DEFAULT_BATCH = 64
    }
}
