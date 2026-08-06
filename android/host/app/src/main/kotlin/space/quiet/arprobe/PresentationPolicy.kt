package space.quiet.arprobe

/**
 * AR-1b.6a/7a — what Android is allowed to be told.
 *
 * THE REASON THIS EXISTS BEFORE THE RENDERER DOES. A conversation
 * notification is not only what a person reads on the lock screen: it is a
 * long-lived shortcut in the launcher and the sharesheet, a `Person` in the
 * system's conversation metadata, and a `locusId` the platform keeps. So an
 * app can hide every word of a message and still have handed over the name of
 * the space and the people in it — the strict mode broken in advance, by the
 * machinery meant to serve the friendly one.
 *
 * Hence the order: the policy first, the renderer second, and the system
 * publication only through the policy. Until AR-1b.7 gives a person a way to
 * choose, the default is the strictest mode, and the friendly renderer exists
 * without being reachable.
 *
 * ```
 *   HIDDEN    a generic notification. No MessagingStyle, no shortcut, no
 *             Person, no space name, no sender, no text. The ONLY genuinely
 *             strict mode — anything using the conversation APIs puts
 *             identity into system metadata whatever the visible text says.
 *
 *   SPACE     the space's name may be shown; the sender and the message may
 *             not. Still a generic notification: the conversation APIs are
 *             built around PEOPLE and require a Person, and inventing an
 *             anonymous one to get into the conversation section would be
 *             working around Android's model rather than using it.
 *
 *   PREVIEW   the friendly one, and the only place conversation integration
 *             is honest: MessagingStyle, a long-lived shortcut, a Person per
 *             sender, the space's name, and a line of the message.
 * ```
 *
 * A fourth mode — the sender's name without the text — is a reasonable thing
 * to want and is deliberately not invented here. Three modes that each mean
 * something are better than four where one is a compromise nobody asked for;
 * it can be added when somebody does.
 */
enum class PresentationPolicy {
    HIDDEN,
    SPACE,
    PREVIEW;

    /** Whether the conversation APIs may be used at all. */
    val mayUseConversationSurface: Boolean
        get() = this == PREVIEW

    /** Whether a long-lived shortcut may be published. */
    val mayPublishShortcut: Boolean
        get() = this == PREVIEW

    /** Whether the space's name may leave this process. */
    val mayNameSpace: Boolean
        get() = this == SPACE || this == PREVIEW

    /** Whether a sender may be named, in the notification or in metadata. */
    val mayNameSender: Boolean
        get() = this == PREVIEW

    /** Whether any of the message itself may be shown. */
    val mayShowText: Boolean
        get() = this == PREVIEW

    /**
     * Whether moving from [previous] to this mode must take system surfaces
     * back.
     *
     * TIGHTENING IS THE ONLY DIRECTION THAT NEEDS WORK, and it needs it
     * urgently: a shortcut published under PREVIEW carries the space's name
     * and a Person into the launcher, and those do not expire on their own.
     * Loosening publishes nothing by itself — the next notification does that,
     * through the policy, the way everything else here does.
     */
    fun tightensFrom(previous: PresentationPolicy): Boolean = ordinal < previous.ordinal

    companion object {
        /**
         * The default until a person has been asked (AR-1b.7b).
         *
         * Strictest, deliberately: every other choice publishes something
         * about a conversation to software outside this app, and defaults
         * chosen for us are how that happens without anybody deciding.
         */
        val DEFAULT = HIDDEN

        fun parse(name: String?): PresentationPolicy =
            entries.firstOrNull { it.name.equals(name, ignoreCase = true) } ?: DEFAULT
    }
}
