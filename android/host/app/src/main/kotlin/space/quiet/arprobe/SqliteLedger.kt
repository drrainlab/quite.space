package space.quiet.arprobe

import android.content.Context

/**
 * [NotificationCoordinator.Ledger] over [NotificationDb].
 *
 * A thin adapter on purpose: the decisions live in the coordinator, the
 * transactions live in the database, and this is the seam that lets each be
 * tested without the other.
 */
internal class SqliteLedger(context: Context) : NotificationCoordinator.Ledger {

    private val db = NotificationDb(context)

    override fun insertPending(c: NotificationCoordinator.Candidate) =
        db.insertPending(c)?.let {
            NotificationCoordinator.Insert(it.tag, it.spaceLabel, it.items)
        }

    override fun markPresented(eventId: String) = db.markPresented(eventId)

    override fun markSuppressed(eventId: String, reason: String) =
        db.markSuppressed(eventId, reason)

    override fun dismiss(spaceId: String) = db.dismiss(spaceId)

    override fun read(spaceId: String) = db.read(spaceId)

    override fun pendingBySpace(): Map<String, NotificationCoordinator.Recovery> =
        db.pendingBySpace().mapValues { (_, r) ->
            NotificationCoordinator.Recovery(r.tag, r.spaceLabel, r.items)
        }

    override fun activeBySpace(): Map<String, NotificationCoordinator.Recovery> =
        db.activeBySpace().mapValues { (_, r) ->
            NotificationCoordinator.Recovery(r.tag, r.spaceLabel, r.items)
        }

    override fun presentedBySpace(): Map<String, NotificationCoordinator.Recovery> =
        db.presentedBySpace().mapValues { (_, r) ->
            NotificationCoordinator.Recovery(r.tag, r.spaceLabel, r.items)
        }

    override fun personKey(device: String, label: String): String = db.personKey(device, label)

    override fun forgetSpace(spaceId: String) = db.forgetSpace(spaceId)

    override fun tagFor(spaceId: String): String? = db.tagFor(spaceId)

    override fun compact(retainFrom: Map<String, Map<String, Long>>): Int =
        db.compact(retainFrom)

    /** Empties the ledger. Tests only — see NotificationDb.wipeForTest. */
    fun wipeForTest() = db.wipeForTest()

    /** Row counts by state, for the rig's status line. */
    fun stats(): Map<String, Int> = db.stats()
}
