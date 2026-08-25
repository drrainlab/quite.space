package space.quiet.arprobe

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * SR-0 — every transition of the PTT machine on the JVM, with the clock,
 * the capture, the engine and the send all scripted. No Android types,
 * the NotificationCoordinator discipline.
 */
class PttControllerTest {

    private class Rig(
        var transcript: SpeechInputEngine.Result = SpeechInputEngine.Result.Text("мы дошли до лагеря", "ru"),
        var micOk: Boolean = true,
        var sendOk: Boolean = true,
    ) {
        var nowMs = 1000L
        val events = mutableListOf<PttController.Event>()
        val sent = mutableListOf<Triple<String, String, String>>()
        val queued = ArrayDeque<Runnable>()
        var captureCancelled = 0
        var pcm = ShortArray(16_000 * 3)
        var amplitude: ((Int) -> Unit)? = null
        var limit: (() -> Unit)? = null
        val engine = FakeSpeechInputEngine { _ -> transcript }

        val ptt = PttController(
            captureFactory = { onAmp, onLimit ->
                amplitude = onAmp
                limit = onLimit
                object : PttController.Capture {
                    override fun start() = micOk
                    override fun stopAndTake() = pcm
                    override fun cancel() { captureCancelled++ }
                }
            },
            engine = { engine },
            send = { s, t, r ->
                sent.add(Triple(s, t, r))
                sendOk
            },
            listener = { events.add(it) },
            execute = { queued.add(it) },
            armingMillis = 120,
            now = { nowMs },
        )

        fun drain() { while (queued.isNotEmpty()) queued.removeFirst().run() }
        fun last() = events.last()
    }

    @Test
    fun theHappyPath_pressSpeakReleaseSend() {
        val r = Rig()
        assertTrue(r.ptt.press("space1"))
        assertEquals(PttController.State.ARMING, r.ptt.state())
        r.nowMs += 200
        r.amplitude!!(500) // threshold passed inside the amplitude tick
        assertEquals(PttController.State.RECORDING, r.ptt.state())
        assertTrue(r.ptt.release())
        assertEquals(PttController.State.FINALIZING, r.ptt.state())
        r.drain()
        assertEquals(1, r.sent.size)
        assertEquals("space1", r.sent[0].first)
        assertEquals("мы дошли до лагеря", r.sent[0].second)
        assertEquals(PttController.State.IDLE, r.ptt.state())
        assertEquals("мы дошли до лагеря", r.last().transcript)
        assertTrue(r.last().error == null)
    }

    @Test
    fun anAccidentalTapSendsNothing() {
        val r = Rig()
        r.ptt.press("s")
        r.nowMs += 50 // released before the arming threshold
        assertTrue(r.ptt.release())
        r.drain()
        assertEquals(0, r.sent.size)
        assertEquals(1, r.captureCancelled)
        assertEquals(PttController.State.IDLE, r.ptt.state())
    }

    @Test
    fun cancelDuringRecordingDestroysTheBuffer() {
        val r = Rig()
        r.ptt.press("s")
        r.nowMs += 200
        r.amplitude!!(100)
        assertTrue(r.ptt.cancel())
        r.drain()
        assertEquals(0, r.sent.size)
        assertEquals(1, r.captureCancelled)
    }

    @Test
    fun cancelDuringFinalizingAbortsTheSend() {
        val r = Rig()
        r.ptt.press("s")
        r.nowMs += 200
        r.amplitude!!(100)
        r.ptt.release()
        assertTrue(r.ptt.cancel()) // while the ASR job is still queued
        r.drain()
        assertEquals("a cancelled utterance was sent", 0, r.sent.size)
        assertEquals(PttController.State.IDLE, r.ptt.state())
    }

    @Test
    fun theTwelveSecondCeilingFinalizesOnItsOwn() {
        val r = Rig()
        r.ptt.press("s")
        r.nowMs += 200
        r.amplitude!!(100)
        r.limit!!()
        assertTrue(r.events.any { it.limitReached })
        r.drain()
        assertEquals(1, r.sent.size)
    }

    @Test
    fun emptyAsrCreatesNoMessage() {
        val r = Rig(transcript = SpeechInputEngine.Result.Empty)
        r.ptt.press("s"); r.nowMs += 200; r.amplitude!!(1); r.ptt.release(); r.drain()
        assertEquals(0, r.sent.size)
        assertEquals(PttController.Error.EMPTY, r.last().error)
    }

    @Test
    fun whitespaceTranscriptIsEmpty() {
        val r = Rig(transcript = SpeechInputEngine.Result.Text("   ", null))
        r.ptt.press("s"); r.nowMs += 200; r.amplitude!!(1); r.ptt.release(); r.drain()
        assertEquals(0, r.sent.size)
        assertEquals(PttController.Error.EMPTY, r.last().error)
    }

    @Test
    fun asrFailureIsNamedNotSent() {
        val r = Rig(transcript = SpeechInputEngine.Result.Error("inference"))
        r.ptt.press("s"); r.nowMs += 200; r.amplitude!!(1); r.ptt.release(); r.drain()
        assertEquals(0, r.sent.size)
        assertEquals(PttController.Error.ASR_FAILED, r.last().error)
    }

    @Test
    fun sendFailureKeepsTheTranscriptVisible() {
        val r = Rig(sendOk = false)
        r.ptt.press("s"); r.nowMs += 200; r.amplitude!!(1); r.ptt.release(); r.drain()
        assertEquals(PttController.Error.SEND_FAILED, r.last().error)
        assertEquals("мы дошли до лагеря", r.last().transcript)
    }

    @Test
    fun aDeadMicRefusesThePress() {
        val r = Rig(micOk = false)
        assertFalse(r.ptt.press("s"))
        assertEquals(PttController.Error.MIC_UNAVAILABLE, r.last().error)
        assertEquals(PttController.State.IDLE, r.ptt.state())
    }

    @Test
    fun aSecondPressWhileBusyIsRefused() {
        val r = Rig()
        assertTrue(r.ptt.press("s"))
        assertFalse(r.ptt.press("s"))
    }

    @Test
    fun clientRefIsStablePerUtteranceAndFreshAcrossUtterances() {
        val r = Rig()
        r.ptt.press("s"); r.nowMs += 200; r.amplitude!!(1); r.ptt.release(); r.drain()
        r.ptt.press("s"); r.nowMs += 200; r.amplitude!!(1); r.ptt.release(); r.drain()
        assertEquals(2, r.sent.size)
        assertTrue(r.sent[0].third.isNotEmpty())
        assertTrue(r.sent[0].third != r.sent[1].third)
    }

    @Test
    fun busyReportsEverythingButIdle() {
        val r = Rig()
        assertFalse(r.ptt.busy())
        r.ptt.press("s")
        assertTrue(r.ptt.busy())
        r.ptt.cancel()
        assertFalse(r.ptt.busy())
    }
}
