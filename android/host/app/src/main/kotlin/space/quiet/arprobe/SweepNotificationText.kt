package space.quiet.arprobe

/**
 * SP-3.2 — what the sweep's persistent notification says, kept away from
 * Android so it can be tested as the sentence it is (the AvailabilityText
 * discipline).
 *
 * THE VISIBILITY IS THE LAW, NOT A COURTESY: background location exists only
 * inside an explicitly started, VISIBLY ACTIVE, bounded session (ADR-034).
 * This sentence is the visible half of that promise, so it must always be
 * true — it names the operation, how long it has run, how far it has gone,
 * and whether the sky is currently answering. Nothing else: it sits on a
 * lock screen for the length of a search operation, so it carries the
 * session's own facts and no one's messages.
 */
internal object SweepNotificationText {

    const val TITLE = "Quiet · Sweep in progress"

    fun summary(label: String, elapsedMs: Long, distanceM: Long, gpsOk: Boolean): String {
        val parts = mutableListOf<String>()
        if (label.isNotBlank()) parts.add(label)
        parts.add(minutes(elapsedMs))
        parts.add(distance(distanceM))
        // ● is a claim about NOW — the last fix is fresh. ○ says the
        // recorder is currently not hearing, which is a state a person
        // standing under trees deserves to see rather than infer.
        parts.add(if (gpsOk) "GPS ●" else "no fix ○")
        return parts.joinToString(" · ")
    }

    private fun minutes(elapsedMs: Long): String {
        val m = elapsedMs / 60_000
        return if (m >= 60) "${m / 60} h ${m % 60} min" else "$m min"
    }

    private fun distance(m: Long): String =
        if (m >= 1000) {
            val whole = m / 1000
            val tenth = (m % 1000) / 100
            "$whole.$tenth km"
        } else {
            "$m m"
        }
}
