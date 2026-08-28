package space.quiet.arprobe

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * SP-3.2 — the pump's bookkeeping, held to its two contracts: the seq moves
 * only on a node's answer, and a loss is a gap the reader must consume.
 */
class SweepSessionMachineTest {

    private fun machine(cap: Int = 8) = SweepSessionMachine("s1", capacity = cap)

    @Test
    fun seqAdvancesOnlyWhenDelivered() {
        val m = machine()
        m.onPoint(1000, 55.0, 37.0, 5)
        m.onPoint(2000, 55.1, 37.1, 5)

        val b1 = m.takeBatch()
        assertNotNull(b1)
        assertEquals(1L, b1!!.seq)
        assertEquals(2, b1.samples.size)

        // One batch out at a time — the exactly-once discipline is structural.
        assertNull(m.takeBatch())

        // A failure re-arms the SAME batch under the SAME seq: the node's
        // spool treats the retry as a no-op if the first send landed.
        m.failed()
        val retry = m.takeBatch()
        assertEquals(1L, retry!!.seq)
        assertEquals(b1.samples, retry.samples)

        m.delivered(1)
        assertEquals(1L, m.lastSeq)
        assertEquals(0, m.pending)
        assertNull(m.takeBatch())
    }

    @Test
    fun staleDeliveryChangesNothing() {
        val m = machine()
        m.onPoint(1000, 55.0, 37.0, 5)
        val b = m.takeBatch()!!
        // A stale answer (wrong seq) must not consume the queue.
        m.delivered(99)
        assertEquals(0L, m.lastSeq)
        m.delivered(b.seq)
        assertEquals(1L, m.lastSeq)
        assertEquals(0, m.pending)
    }

    @Test
    fun overflowBecomesAnUnknownGapNotASecret() {
        val m = machine(cap = 4)
        for (i in 0 until 6) {
            assertTrue(m.onPoint(1000L * (i + 1), 55.0 + i, 37.0, 5))
        }
        // Two oldest points (t=1000, t=2000) were dropped; the drop itself
        // travels as a gap with reason `unknown` — the recorder lost them,
        // and "I lost them" is not a cause (never no_fix).
        val b = m.takeBatch(limit = 16)!!
        val first = b.samples.first()
        assertTrue(first.gap)
        assertEquals("unknown", first.reason)
        assertEquals(1000L, first.unixMs)
        assertEquals(1000L, first.durationMs) // covers t=1000..2000
        assertEquals(4, b.samples.size - 1)   // the surviving points follow
        m.delivered(b.seq)
        assertEquals(0, m.pending)
        // The gap does not reappear on the next batch.
        assertNull(m.takeBatch())
    }

    @Test
    fun closedRefusesCaptureButDrainsWhatIsQueued() {
        val m = machine()
        m.onPoint(1000, 55.0, 37.0, 5)
        m.close()
        // Stop forbids capture...
        assertFalse(m.onPoint(2000, 55.1, 37.1, 5))
        assertFalse(m.onGap(2000, 500, "unknown"))
        // ...not finalization: the queued sample still drains.
        val b = m.takeBatch()
        assertNotNull(b)
        assertEquals(1, b!!.samples.size)
    }

    @Test
    fun gapsCountAsHeardAndCarryTheirSpanIntoOverflow() {
        val m = machine(cap = 2)
        m.onGap(1000, 5000, "suspended")   // covers 1000..6000
        m.onPoint(7000, 55.0, 37.0, 5)
        m.onPoint(8000, 55.1, 37.1, 5)     // evicts the gap
        val b = m.takeBatch(limit = 16)!!
        val first = b.samples.first()
        assertTrue(first.gap)
        assertEquals("unknown", first.reason)
        // The overflow gap covers the WHOLE dropped span, gap end included:
        // 1000 .. 1000+5000 = 6000.
        assertEquals(1000L, first.unixMs)
        assertEquals(5000L, first.durationMs)
    }
}
