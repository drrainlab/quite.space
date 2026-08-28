package space.quiet.arprobe

/**
 * SR-0 — automatic speech of incoming messages: the presentation effect
 * AFTER trusted projection, and nothing else (groom D4). It is fed the
 * same candidates the notification plane gets — which is the whole
 * security argument: a candidate exists only for an event that passed
 * verify, identity, policy and apply, and history cannot produce one by
 * construction.
 *
 * A SIBLING of NotificationCoordinator, never a step inside it: the
 * notification pipeline owns the ack, and playback must not be able to
 * disturb it. This coordinator NEVER acks.
 *
 * The gates, in order (each a named decision, JVM-tested):
 *   text schema only → not the person's own words (any device) →
 *   live cursor (redeliveries carry 0) → space enabled && autoplay →
 *   not spoken before (bounded LRU) → hold while PTT is busy
 *   (HALF-DUPLEX: the phone must not talk into its own microphone) →
 *   FIFO queue of 8, overflow drops the OLDEST — radio semantics,
 *   recency wins; the messages themselves lose nothing.
 *
 * NO ANDROID TYPES. Called on the Go pump goroutine: gates and queue
 * only, then a drain posted to the playback executor.
 */
internal class RadioPlaybackCoordinator(
    private val enabled: (spaceId: String) -> Boolean,
    private val autoplay: (spaceId: String) -> Boolean,
    private val engine: SpeechOutputEngine,
    private val execute: (Runnable) -> Unit,
    private val pttBusy: () -> Boolean = { false },
    private val speakingChanged: (spaceId: String, on: Boolean) -> Unit = { _, _ -> },
    private val maxQueue: Int = 8,
    private val recentLimit: Int = 256,
) {
    enum class Decision {
        SPOKEN_ENQUEUED,
        SUPPRESSED_SCHEMA,
        SUPPRESSED_OWN,
        SUPPRESSED_REPLAY,
        SUPPRESSED_DISABLED,
        SUPPRESSED_AUTOPLAY_OFF,
        SUPPRESSED_ALREADY_SPOKEN,
        SUPPRESSED_EMPTY,
        DROPPED_OLDEST_ON_OVERFLOW,
    }

    data class Item(val eventId: String, val spaceId: String, val sender: String, val text: String)

    private val queue = ArrayDeque<Item>()
    private val recent = LinkedHashSet<String>()
    @Volatile private var offlineVoice = false
    @Volatile private var prepared = false
    private var speaking: Item? = null
    var overflowDropped = 0
        private set

    fun prepare() {
        if (prepared) return
        prepared = true
        engine.prepare { ok -> offlineVoice = ok }
    }

    /** True when nothing will ever sound: the honest settings warning. */
    fun offlineVoiceMissing(): Boolean = prepared && !offlineVoice

    @Synchronized
    fun onCandidate(
        eventId: String,
        spaceId: String,
        schema: String,
        authoredByPrincipal: Boolean,
        presentationCursor: Long,
        senderLabel: String,
        spokenText: String,
    ): Decision {
        if (schema != "message.text.v1") return Decision.SUPPRESSED_SCHEMA
        if (authoredByPrincipal) return Decision.SUPPRESSED_OWN
        if (presentationCursor <= 0) return Decision.SUPPRESSED_REPLAY
        if (!enabled(spaceId)) return Decision.SUPPRESSED_DISABLED
        if (!autoplay(spaceId)) return Decision.SUPPRESSED_AUTOPLAY_OFF
        if (spokenText.isBlank()) return Decision.SUPPRESSED_EMPTY
        if (!recent.add(eventId)) return Decision.SUPPRESSED_ALREADY_SPOKEN
        while (recent.size > recentLimit) recent.remove(recent.first())
        var overflowed = false
        if (queue.size >= maxQueue) {
            queue.removeFirst()
            overflowDropped++
            overflowed = true
        }
        queue.addLast(Item(eventId, spaceId, senderLabel, spokenText))
        execute { drain() }
        return if (overflowed) Decision.DROPPED_OLDEST_ON_OVERFLOW else Decision.SPOKEN_ENQUEUED
    }

    /**
     * HALF-DUPLEX, the other direction: the PTT went down — silence the
     * live utterance and hold the queue until [resume].
     */
    @Synchronized
    fun holdForPtt() {
        if (speaking != null) {
            engine.stop() // its onDone(false) advances the queue, which then holds
        }
    }

    fun resume() {
        execute { drain() }
    }

    @Synchronized
    fun cancelSpace(spaceId: String) {
        queue.removeAll { it.spaceId == spaceId }
        if (speaking?.spaceId == spaceId) engine.stop()
    }

    private fun drain() {
        val item: Item
        synchronized(this) {
            if (speaking != null || !offlineVoice) return
            if (pttBusy()) return // resumed by PttController reaching IDLE
            item = queue.removeFirstOrNull() ?: return
            speaking = item
        }
        speakingChanged(item.spaceId, true)
        // "Anna. Мы дошли до лагеря." — the sender prefix is what makes
        // screen-off usable (groom §31); no timestamps, no route.
        val body = if (item.sender.isNotBlank()) "${item.sender}. ${item.text}" else item.text
        engine.speak(item.eventId, body, detectLanguage(item.text)) { _ ->
            synchronized(this) { speaking = null }
            speakingChanged(item.spaceId, false)
            execute { drain() }
        }
    }

    companion object {
        /**
         * Deterministic RU/EN pick (groom §30): predominantly Cyrillic →
         * ru, predominantly Latin → en, mixed/uncertain → null (device
         * default voice). Never a cloud call.
         */
        fun detectLanguage(text: String): String? {
            var cyr = 0
            var lat = 0
            for (ch in text) {
                when {
                    ch in 'Ѐ'..'ӿ' -> cyr++
                    ch in 'a'..'z' || ch in 'A'..'Z' -> lat++
                }
            }
            val letters = cyr + lat
            if (letters == 0) return null
            return when {
                cyr * 10 >= letters * 7 -> "ru"
                lat * 10 >= letters * 7 -> "en"
                else -> null
            }
        }
    }
}
