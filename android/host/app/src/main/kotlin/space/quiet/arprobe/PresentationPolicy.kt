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
 *   SENDER    who is writing and where, ALWAYS; the words only while the
 *             phone is unlocked. This file once said a fourth mode "can be
 *             added when somebody asks". Somebody did (AN-3): it is what a
 *             person means by "like every other messenger" — a locked phone
 *             on a table says Mike wrote, not what Mike wrote. It uses the
 *             conversation surface exactly as PREVIEW does, so it publishes
 *             the same identity into system metadata; what it withholds is
 *             the text, and only from a locked screen.
 *
 *             THE LOCK IS ENFORCED HERE, NOT LEFT TO ANDROID. The platform's
 *             own redaction (VISIBILITY_PRIVATE) is a system preference that
 *             ships switched OFF on most phones, so relying on it would make
 *             this mode a promise kept only where somebody had already
 *             changed a setting they have never heard of. The presenter asks
 *             whether the device is locked and renders accordingly, and
 *             re-renders — silently — when that changes.
 */
enum class PresentationPolicy {
    HIDDEN,
    SPACE,
    SENDER,
    PREVIEW;

    /** Whether the conversation APIs may be used at all. */
    val mayUseConversationSurface: Boolean
        get() = this == SENDER || this == PREVIEW

    /** Whether a long-lived shortcut may be published. */
    val mayPublishShortcut: Boolean
        get() = this == SENDER || this == PREVIEW

    /** Whether the space's name may leave this process. */
    val mayNameSpace: Boolean
        get() = this != HIDDEN

    /** Whether a sender may be named, in the notification or in metadata. */
    val mayNameSender: Boolean
        get() = this == SENDER || this == PREVIEW

    /**
     * Whether the message itself may be shown, given whether the phone is
     * locked RIGHT NOW. Locked is the default argument on purpose: a caller
     * that forgot to ask gets the strict answer.
     */
    fun mayShowText(deviceLocked: Boolean = true): Boolean = when (this) {
        PREVIEW -> true
        SENDER -> !deviceLocked
        else -> false
    }

    /** Whether what is shown depends on the lock, so a change must re-render. */
    val followsTheLock: Boolean
        get() = this == SENDER

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
         * The default until a person chooses otherwise.
         *
         * IT WAS HIDDEN, and the reasoning was sound: every other choice
         * publishes something about a conversation to software outside this
         * app. It is SENDER now by the owner's decision (AN-3), made against
         * a measured cost of the strict default: a notification that names
         * nobody is one people learn to ignore, and in the situations this
         * product is also built for — somebody waiting on a word that
         * matters — an ignored notification is the app failing. The words
         * stay off a locked screen; HIDDEN is one tap away and says so.
         */
        val DEFAULT = SENDER

        /**
         * A word that is not a mode — or no word at all — reads as the
         * STRICTEST one, not as the default: the default is a decision about
         * people who have not chosen, and a value nobody can interpret is not
         * that. Being wrong towards silence is recoverable.
         */
        fun parse(name: String?): PresentationPolicy =
            entries.firstOrNull { it.name.equals(name, ignoreCase = true) } ?: HIDDEN
    }
}
