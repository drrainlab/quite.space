package space.quiet.arprobe

/**
 * AR-1b.2 — which schemas are worth waking a person for.
 *
 * AN ALLOWLIST, AND THE DIRECTION IS THE DECISION. A denylist means every
 * schema the protocol gains later notifies by default until somebody remembers
 * to exclude it — and the log is full of things that are correct, frequent and
 * none of a person's business: epoch rotations, delivery receipts, presence
 * updates, manifest revisions, asset carriers. A phone that announces
 * `identity.device_certified.v1` has not made a small mistake; it has told the
 * person their messenger is broken. Silence for something unknown is
 * recoverable. Noise is not.
 *
 * The list deliberately holds only what a person would recognise as somebody
 * speaking to them.
 *
 * THREE EXCLUSIONS WORTH NAMING, because each looks like an oversight:
 *
 * ```
 *   block.attached.v1     an asset CARRIER, not a feed entry — the protocol
 *                         says so at its own definition. Notifying would
 *                         announce an upload as a message, twice per picture.
 *   message.revised.v1    an edit is not news. A person who has read a message
 *   message.tombstoned.v1 does not need waking because its author fixed a typo
 *                         — or, worse, because they withdrew it.
 *   block.reaction.v1     a resonance is the quietest gesture in the product.
 *                         Announcing each one is the loudest possible reading
 *                         of it. If it belongs anywhere it is the signals
 *                         channel, aggregated — not one buzz per tap.
 * ```
 *
 * THE SIGNALS CHANNEL HAS NO SOURCE YET, and this is where that shows. AR-1b.2
 * promises two channels — `quite.messages` and the HIGH-priority
 * `quite.signals` — but the AR-1b.1 candidate carries `schema` and nothing
 * about mentions or replies. "Is this addressed to me" is the QuietRank hard
 * rule (signed @mentions, `reply_to_me`), and it lives in the core. So every
 * candidate is a MESSAGE here, honestly, and promoting one to a signal needs
 * the CORE to say so — a candidate field, not a guess on this side. Recorded
 * rather than approximated: a phone that guesses at "this one is important"
 * is worse than one that does not claim to know.
 */
internal object NotificationPolicy {

    /**
     * The product channels. Ids are frozen once created — these are the v2
     * generation, born when the channels gained the Quiet Chimes voice
     * (a channel's sound is immutable after creation, so a new voice means
     * new ids; ensure() deletes the v1 rows so nobody keeps two).
     */
    const val CHANNEL_MESSAGES = "quite.messages.v2"

    /**
     * AR-1c — the "staying connected" mode's own channel.
     *
     * SEPARATE, AND LOW. It carries one permanent card that is not news and
     * must never make a sound; putting it on the messages channel would mean
     * a person silencing a mode also silences their messages, or the reverse.
     * Low importance is also what makes it sit quietly at the bottom of the
     * shade, which is where a state belongs.
     */
    const val CHANNEL_CONNECTION = "quite.connection"
    const val CHANNEL_SIGNALS = "quite.signals.v2"

    /**
     * Personal signals — somebody CALLED this person (a signed mention; the
     * core says so, this side never guesses). Its own channel so Android
     * settings give the person separate switches for ordinary signals and
     * personal ones — the product's whole philosophy as two system rows.
     */
    const val CHANNEL_SIGNALS_PERSONAL = "quite.signals.personal.v1"

    private val NOTIFIABLE = setOf(
        "message.text.v1",
        "block.visual.v1",
        "block.video.v1",
        "block.voice.v1",
        "block.audio.v1",
        "block.file.v1",
        "block.link.v1",
    )

    fun notifiable(schema: String?): Boolean = schema != null && schema in NOTIFIABLE

    /**
     * Which channel a candidate belongs to. `personal` is the CORE's word
     * (the event's signed mentions include this person) — the field this
     * file's own comment spent a release waiting for. The plain signals
     * channel starts receiving traffic when a future candidate field
     * carries the non-personal attention verdict; its id and sound are
     * fixed NOW so that landing needs no second migration.
     */
    @Suppress("UNUSED_PARAMETER")
    fun channelFor(schema: String, personal: Boolean): String =
        if (personal) CHANNEL_SIGNALS_PERSONAL else CHANNEL_MESSAGES
}
