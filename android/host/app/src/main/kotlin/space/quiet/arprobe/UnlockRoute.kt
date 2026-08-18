package space.quiet.arprobe

/**
 * WHICH WAY IN A LAUNCH TAKES.
 *
 * There are now three things that can open this app without a typed
 * passphrase — a face, four digits, and a remembered passphrase — and they
 * all want to own the launch. Left inline in the Activity that became an if
 * whose one hard-won lesson was a paragraph of comment; a third arrival
 * makes it a decision worth naming, and naming it here means it can be
 * tested on the JVM, without a handset, exactly like NotificationPolicy.
 *
 * NO ANDROID TYPE MAY APPEAR IN THIS FILE. That is what keeps it testable,
 * and it is the whole reason the class exists rather than a fourth branch
 * in onCreate.
 *
 * THE ORDER, AND WHY:
 *
 *   BIOMETRIC first, when one is enrolled. It is the most recent deliberate
 *   act somebody can take about this device, it is what every other app on
 *   the phone does, and it costs nothing when refused — [refused] falls
 *   straight through to whatever else exists.
 *
 *   CODE next. Measured on a real handset: with an auto-open in front of it
 *   the node was already alive before the keypad could be drawn, so the code
 *   was never asked and was pure decoration. A code exists in order to ask a
 *   question; a remembered passphrase exists in order not to.
 *
 *   REMEMBERED PASSPHRASE last of the three, for that same reason.
 *
 *   ASK when nothing is set up, which is also the honest destination for a
 *   biometric that was refused on a device with nothing else bound.
 */
internal enum class UnlockRoute {
    /** Raise the system's biometric prompt. */
    BIOMETRIC,

    /** Draw the keypad. */
    CODE,

    /** Open with the stored passphrase, asking nothing. */
    REMEMBERED,

    /** Show the passphrase field. */
    ASK,
    ;

    companion object {
        /**
         * @param biometric a passphrase is sealed behind a face or fingerprint
         * @param code      four digits are bound
         * @param remembered a passphrase is stored with no question in front of it
         */
        fun of(biometric: Boolean, code: Boolean, remembered: Boolean): UnlockRoute = when {
            biometric -> BIOMETRIC
            code -> CODE
            remembered -> REMEMBERED
            else -> ASK
        }

        /**
         * The way in after a biometric was refused, dismissed, or could not
         * run at all.
         *
         * A REFUSAL IS NOT A LOCKOUT. Somebody who cancels the face prompt is
         * not somebody who failed to prove anything — they are somebody who
         * wants the other door, and every other door on this device is still
         * theirs. The one thing this must never do is return BIOMETRIC and
         * loop the prompt forever.
         */
        fun refused(code: Boolean, remembered: Boolean): UnlockRoute =
            of(biometric = false, code = code, remembered = remembered)
    }
}
