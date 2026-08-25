package space.quiet.arprobe

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * SR-0 — the T1–T8 functional matrix of the groom, on the JVM: every gate
 * of automatic speech, with the engine scripted and time in hand.
 */
class RadioPlaybackCoordinatorTest {

    private class Rig(
        var radioOn: Boolean = true,
        var auto: Boolean = true,
        offlineVoice: Boolean = true,
    ) {
        val engine = FakeSpeechOutputEngine(offlineVoice)
        val queued = ArrayDeque<Runnable>()
        var busy = false
        val speakingEvents = mutableListOf<Pair<String, Boolean>>()
        val c = RadioPlaybackCoordinator(
            enabled = { radioOn },
            autoplay = { auto },
            engine = engine,
            execute = { queued.add(it) },
            pttBusy = { busy },
            speakingChanged = { s, on -> speakingEvents.add(s to on) },
        ).also { it.prepare() }

        fun candidate(
            id: String,
            own: Boolean = false,
            cursor: Long = 5,
            schema: String = "message.text.v1",
            text: String = "мы дошли до лагеря",
            sender: String = "Anna",
        ) = c.onCandidate(id, "space1", schema, own, cursor, sender, text)

        fun drain() { while (queued.isNotEmpty()) queued.removeFirst().run() }
    }

    @Test
    fun t1_radioOffMeansSilence() {
        val r = Rig(radioOn = false)
        assertEquals(RadioPlaybackCoordinator.Decision.SUPPRESSED_DISABLED, r.candidate("e1"))
        r.drain()
        assertEquals(0, r.engine.spoken.size)
    }

    @Test
    fun t2_radioOnSpeaksExactlyOnceWithTheSenderPrefix() {
        val r = Rig()
        assertEquals(RadioPlaybackCoordinator.Decision.SPOKEN_ENQUEUED, r.candidate("e1"))
        r.drain()
        assertEquals(1, r.engine.spoken.size)
        assertEquals("Anna. мы дошли до лагеря", r.engine.spoken[0].second)
    }

    @Test
    fun t3_ownWordsAreNeverAutoSpoken() {
        val r = Rig()
        assertEquals(RadioPlaybackCoordinator.Decision.SUPPRESSED_OWN, r.candidate("e1", own = true))
        r.drain()
        assertEquals(0, r.engine.spoken.size)
    }

    @Test
    fun t4_aDuplicateEventIsSpokenOnce() {
        val r = Rig()
        r.candidate("e1")
        assertEquals(RadioPlaybackCoordinator.Decision.SUPPRESSED_ALREADY_SPOKEN, r.candidate("e1"))
        r.drain()
        r.engine.finishCurrent()
        r.drain()
        assertEquals(1, r.engine.spoken.size)
    }

    @Test
    fun t7_replayAndRedeliveryCarryCursorZeroAndStaySilent() {
        val r = Rig()
        assertEquals(RadioPlaybackCoordinator.Decision.SUPPRESSED_REPLAY, r.candidate("e1", cursor = 0))
        r.drain()
        assertEquals(0, r.engine.spoken.size)
    }

    @Test
    fun nonTextSchemasNeverSpeak() {
        val r = Rig()
        for (schema in listOf(
            "message.revised.v1", "message.tombstoned.v1", "presence.update.v1",
            "block.voice.v1", "membership.epoch.v1",
        )) {
            assertEquals(RadioPlaybackCoordinator.Decision.SUPPRESSED_SCHEMA,
                r.candidate("e-$schema", schema = schema))
        }
        r.drain()
        assertEquals(0, r.engine.spoken.size)
    }

    @Test
    fun autoplayOffRendersButDoesNotSpeak() {
        val r = Rig(auto = false)
        assertEquals(RadioPlaybackCoordinator.Decision.SUPPRESSED_AUTOPLAY_OFF, r.candidate("e1"))
    }

    @Test
    fun fifoOrderIsKept() {
        val r = Rig()
        r.candidate("a", text = "первое")
        r.candidate("b", text = "второе")
        r.candidate("c", text = "третье")
        r.drain(); r.engine.finishCurrent(); r.drain(); r.engine.finishCurrent(); r.drain(); r.engine.finishCurrent(); r.drain()
        assertEquals(listOf("a", "b", "c"), r.engine.spoken.map { it.first })
    }

    @Test
    fun overflowDropsTheOldestSpeechNeverTheMessages() {
        val r = Rig()
        for (i in 0 until 10) r.candidate("e$i", text = "msg $i")
        // queue cap 8: e0, e1 dropped from SPEECH only
        var finished = 0
        r.drain()
        while (r.engine.spoken.size > finished) {
            finished = r.engine.spoken.size
            r.engine.finishCurrent()
            r.drain()
        }
        assertEquals(8, r.engine.spoken.size)
        assertEquals("e2", r.engine.spoken.first().first)
        assertEquals(2, r.c.overflowDropped)
    }

    @Test
    fun halfDuplex_pttSilencesAndIdleResumes() {
        val r = Rig()
        r.candidate("e1")
        r.drain()
        assertEquals(1, r.engine.spoken.size)
        r.busy = true
        r.c.holdForPtt() // stops the live utterance
        assertEquals(1, r.engine.stops)
        r.candidate("e2")
        r.drain() // pttBusy → held
        assertEquals(1, r.engine.spoken.size)
        r.busy = false
        r.c.resume()
        r.drain()
        assertEquals(2, r.engine.spoken.size)
    }

    @Test
    fun noOfflineVoiceMeansHonestSilence() {
        val r = Rig(offlineVoice = false)
        assertTrue(r.c.offlineVoiceMissing())
        r.candidate("e1")
        r.drain()
        assertEquals(0, r.engine.spoken.size)
    }

    @Test
    fun speakingIndicatorFollowsTheUtterance() {
        val r = Rig()
        r.candidate("e1")
        r.drain()
        assertEquals(listOf("space1" to true), r.speakingEvents)
        r.engine.finishCurrent()
        assertEquals(listOf("space1" to true, "space1" to false), r.speakingEvents)
    }

    @Test
    fun languageDetectorIsDeterministic() {
        assertEquals("ru", RadioPlaybackCoordinator.detectLanguage("мы дошли до лагеря"))
        assertEquals("en", RadioPlaybackCoordinator.detectLanguage("meet me at the river"))
        assertEquals(null, RadioPlaybackCoordinator.detectLanguage("ok да ok да"))
        assertEquals(null, RadioPlaybackCoordinator.detectLanguage("1234 :: 5678"))
    }

    @Test
    fun emptySpokenTextIsSuppressed() {
        val r = Rig()
        assertEquals(RadioPlaybackCoordinator.Decision.SUPPRESSED_EMPTY, r.candidate("e1", text = "  "))
    }
}
