package space.quiet.arprobe

/**
 * AR-1b.6a — what a conversation notification would say, decided without
 * Android in the room.
 *
 * The renderer that follows this is a handful of platform calls; everything
 * that can be WRONG is here, where a JVM test can drive it: which messages
 * are shown and in what order, who they are attributed to, what a given
 * privacy mode is allowed to reveal, and whether a shortcut may exist at all.
 *
 * MESSAGES ARE CHRONOLOGICAL AND BOUNDED. Android's MessagingStyle holds at
 * most twenty-five and shows fewer; the ledger stays authoritative and this is
 * only presentation policy. Handing the platform everything a space ever held
 * would be asking it to decide what a person sees.
 *
 * A PERSON IS AN OPAQUE LOCAL KEY, NEVER A NAME AND NEVER A DEVICE ID. Two
 * people share a name often enough; one person renames themselves; and Android
 * expects a stable, distinguishable identity across republications, so a name
 * would merge two strangers into one conversation thread the first time they
 * collided. The key is minted here the same way a notification tag is —
 * random, local, and not derived from anything on the wire — so the system's
 * conversation metadata cannot become a map of who this person talks to.
 */
object ConversationProjection {

    /**
     * How many messages reach the system object. Below Android's own limit of
     * twenty-five on purpose: a notification is a glance, and the ledger is
     * where the conversation actually lives.
     */
    const val MESSAGES_SHOWN = 12

    /** One line of a conversation, as the system will be told it. */
    data class Line(
        val personKey: String,
        val senderLabel: String,
        val text: String,
        val occurredAtUnixMs: Long,
    )

    /**
     * Everything the renderer needs and nothing it should decide.
     *
     * `conversationTitle` is null when the policy does not allow naming the
     * space — and null means the renderer must not put a title anywhere,
     * including the shortcut label, which Android 11+ prefers over the style's
     * own title.
     */
    data class Rendering(
        val policy: PresentationPolicy,
        val useConversationSurface: Boolean,
        val publishShortcut: Boolean,
        val conversationTitle: String?,
        val lines: List<Line>,
        /**
         * What to CALL each person, by their opaque key — the LAST name seen
         * for them in this aggregation, not the first.
         *
         * A conversation notification names a Person once and shows every one
         * of their messages under that name. Building it from whichever line
         * happened to come first meant a person who renamed themselves kept
         * their old name in the shade until the whole aggregation closed, even
         * though the device had already applied the rename and the member list
         * said so. Found on a phone: the title said the new name and the
         * messages underneath it said the old one.
         */
        val namesByPerson: Map<String, String>,
        /** What a generic notification says when the surface is not used. */
        val genericText: String,
    )

    /**
     * @param personKeyOf maps a sender's protocol device to the device-local
     *   opaque key. Passed in rather than looked up, so this stays pure and the
     *   storage stays in one place.
     */
    fun of(
        policy: PresentationPolicy,
        spaceLabel: String,
        items: List<NotificationCoordinator.Item>,
        personKeyOf: (device: String) -> String,
    ): Rendering {
        val shown = if (items.size <= MESSAGES_SHOWN) items
        else items.subList(items.size - MESSAGES_SHOWN, items.size)

        val lines = if (!policy.mayUseConversationSurface) emptyList() else
            shown.sortedBy { it.occurredAtUnixMs }.map {
                Line(
                    personKey = personKeyOf(it.device),
                    // A sender with no label is ordinary — a manifest may not
                    // have arrived — and an empty Person name is better than a
                    // guess with somebody's device id in it.
                    senderLabel = if (policy.mayNameSender) it.senderLabel else "",
                    text = if (policy.mayShowText) it.previewText else "",
                    occurredAtUnixMs = it.occurredAtUnixMs,
                )
            }

        val n = items.size
        // LAST WINS, per person. Lines are chronological, so the newest
        // non-empty label is the one the sender is called by now.
        val names = LinkedHashMap<String, String>()
        for (line in lines) {
            if (line.senderLabel.isNotEmpty()) names[line.personKey] = line.senderLabel
        }

        return Rendering(
            policy = policy,
            useConversationSurface = policy.mayUseConversationSurface,
            publishShortcut = policy.mayPublishShortcut,
            conversationTitle = if (policy.mayNameSpace && spaceLabel.isNotEmpty()) spaceLabel else null,
            lines = lines,
            namesByPerson = names,
            genericText = if (n == 1) "A new message" else "$n new messages",
        )
    }
}
