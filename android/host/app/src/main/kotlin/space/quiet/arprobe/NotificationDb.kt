package space.quiet.arprobe

import android.content.ContentValues
import android.content.Context
import android.database.sqlite.SQLiteConstraintException
import android.database.sqlite.SQLiteDatabase
import android.database.sqlite.SQLiteOpenHelper
import java.security.SecureRandom

/**
 * AR-1b.5.3c — the presentation ledger, in SQLite because the crash windows
 * are real.
 *
 * WHY NOT A PREFERENCE FILE. The old bounded id-set in SharedPreferences
 * answered one question — "have I shown this before" — and only between clean
 * runs. The windows that actually lose a notification are inside a single
 * arrival: the row is written and the process dies before the system is told;
 * the system is told and the process dies before the row says so. Those need
 * two transactions with a state in between, and a JSON blob rewritten whole
 * has no way to express "in between".
 *
 * THE DIVISION OF RESPONSIBILITY, and it is the whole design:
 *
 * ```
 *   the core's watermark   the host has this candidate in durable storage
 *   pending / presented    the system surface has actually been told
 * ```
 *
 * So a candidate is written as `pending`, THEN acknowledged to the core, then
 * posted, then marked `presented`. Every crash lands somewhere recoverable: an
 * unacknowledged candidate comes back from the log, and an acknowledged one
 * that never reached the shade is still sitting here as `pending`, waiting for
 * recovery to post it under the same (tag, id) — which updates one entry
 * rather than making a second.
 *
 * THE OPAQUE KEY IS CREATED IN THE SAME TRANSACTION AS THE SPACE'S FIRST
 * EVENT. Otherwise a crash in between leaves a candidate with no stable tag,
 * and the recovery that posts it would have to invent one — which is how a
 * conversation ends up with two notifications that can never be told apart.
 * 128 bits, unique, retried on the collision nobody will ever see: the cost is
 * nothing and the alternative is explaining a probability to somebody whose
 * messages merged.
 *
 * SNAPSHOTS ARE CLEARED AT A TERMINAL STATE. A pending row must be renderable
 * after a crash without asking the core anything, so the label and the preview
 * live here — which means a person's words are in this database. Once the row
 * can no longer be rendered (read, dismissed, suppressed) they are wiped and
 * the row keeps only what dedup needs. The database is not in cloud backup:
 * android:allowBackup is false.
 */
internal class NotificationDb(context: Context) :
    SQLiteOpenHelper(context.applicationContext, NAME, null, VERSION) {

    override fun onCreate(db: SQLiteDatabase) {
        db.execSQL(
            """
            CREATE TABLE notification_spaces (
              space_id          TEXT PRIMARY KEY,
              notification_key  TEXT NOT NULL UNIQUE,
              active_generation INTEGER NOT NULL DEFAULT 0,
              active_count      INTEGER NOT NULL DEFAULT 0,
              latest_event_id   TEXT
            )
            """.trimIndent()
        )
        db.execSQL(
            """
            CREATE TABLE notification_events (
              event_id            TEXT PRIMARY KEY,
              space_id            TEXT NOT NULL,
              device              TEXT NOT NULL,
              schema              TEXT NOT NULL,
              source_sequence     INTEGER NOT NULL,
              occurred_at_unix_ms INTEGER NOT NULL,
              state               TEXT NOT NULL,
              generation          INTEGER NOT NULL,
              space_label         TEXT,
              sender_label        TEXT,
              preview_text        TEXT,
              personal            INTEGER NOT NULL DEFAULT 0
            )
            """.trimIndent()
        )
        db.execSQL("CREATE INDEX idx_events_space_state ON notification_events(space_id, state)")
        db.execSQL(
            """
            CREATE TABLE notification_people (
              sender_device     TEXT PRIMARY KEY,
              opaque_person_key TEXT NOT NULL UNIQUE,
              last_known_label  TEXT
            )
            """.trimIndent()
        )
    }

    /**
     * MIGRATIONS ADD; THEY NEVER DROP. Dropping a table here would drop the
     * dedup memory, and an upgrade would re-announce everything still live —
     * a person reinstalling would be told about every message they had
     * already read.
     *
     * THIS EXISTED BECAUSE THE VERSION DID NOT MOVE. Two schema changes —
     * `source_sequence` on events, and the whole `notification_people` table —
     * were added while VERSION stayed at 1, so an install created before them
     * kept the old schema and every insert failed against a column that was
     * not there. The failure was correct at every layer and invisible from
     * outside: the insert threw, the candidate was NOT acknowledged (which is
     * exactly right — see DEFERRED_STORAGE_UNAVAILABLE), the core kept
     * replaying it, and nothing was ever shown. It took an instrumented test
     * to say the word "no such table" out loud.
     */
    override fun onUpgrade(db: SQLiteDatabase, old: Int, new: Int) {
        if (old < 3) {
            // Quiet Chimes: the core now says "somebody called you"; the
            // row carries it so a re-render keeps routing to the personal
            // channel after a restart. DEFAULT 0 — history stays ordinary.
            runCatching {
                db.execSQL(
                    "ALTER TABLE notification_events ADD COLUMN personal INTEGER NOT NULL DEFAULT 0"
                )
            }
        }
        if (old < 2) {
            runCatching {
                db.execSQL(
                    "ALTER TABLE notification_events ADD COLUMN source_sequence INTEGER NOT NULL DEFAULT 0"
                )
            }
            db.execSQL(
                """
                CREATE TABLE IF NOT EXISTS notification_people (
                  sender_device     TEXT PRIMARY KEY,
                  opaque_person_key TEXT NOT NULL UNIQUE,
                  last_known_label  TEXT
                )
                """.trimIndent()
            )
        }
    }

    // ------------------------------------------------------------ the writes

    /**
     * The durable half of an arrival, in ONE transaction: the space row and
     * its opaque key, the event as `pending`, and the aggregation.
     *
     * Returns null when the event was already known — the crash-recovery case
     * where the host stored it and died before the core heard the
     * acknowledgement. The count must not move for a redelivery, and the
     * caller must acknowledge it anyway.
     */
    fun insertPending(c: NotificationCoordinator.Candidate): Insert? {
        val db = writableDatabase
        db.beginTransaction()
        try {
            val key = ensureSpaceKey(db, c.spaceId)
            val generation = querySpaceLong(db, c.spaceId, "active_generation")

            val values = ContentValues().apply {
                put("event_id", c.eventId)
                put("space_id", c.spaceId)
                put("device", c.device)
                put("schema", c.schema)
                put("source_sequence", c.sourceSequence)
                put("occurred_at_unix_ms", c.occurredAtUnixMs)
                put("state", STATE_PENDING)
                put("generation", generation)
                put("space_label", c.spaceLabel)
                put("sender_label", c.senderLabel)
                put("preview_text", c.previewText)
                put("personal", if (c.personal) 1 else 0)
            }
            // CONFLICT_IGNORE, not REPLACE: a redelivery must not resurrect a
            // row that has already been dismissed or read back into pending.
            val rowId = db.insertWithOnConflict(
                "notification_events", null, values, SQLiteDatabase.CONFLICT_IGNORE
            )
            if (rowId == -1L) {
                db.setTransactionSuccessful()
                return null
            }

            db.execSQL(
                "UPDATE notification_spaces SET active_count = active_count + 1, " +
                    "latest_event_id = ? WHERE space_id = ?",
                arrayOf(c.eventId, c.spaceId),
            )
            val items = aggregationLocked(db, c.spaceId, generation)
            db.setTransactionSuccessful()
            return Insert(tag = "space:$key", spaceLabel = c.spaceLabel, items = items)
        } finally {
            db.endTransaction()
        }
    }

    fun markPresented(eventId: String) = setState(eventId, STATE_PRESENTED, wipeSnapshot = false)

    /**
     * A terminal state for something that will never be shown: the space was
     * on screen, or the permission was refused. The snapshot goes with it —
     * the row exists from here on only so the same event is never announced
     * twice.
     */
    fun markSuppressed(eventId: String, reason: String) =
        setState(eventId, reason, wipeSnapshot = true)

    /**
     * The person swiped the shade clear. The live aggregation closes — a new
     * generation starts, so the next message is one message rather than a
     * repeat of what they just dismissed — and the dedup memory stays, or the
     * next sync would bring the same events straight back.
     */
    fun dismiss(spaceId: String) = closeGeneration(spaceId, STATE_DISMISSED)

    /** The interface actually showed the conversation. */
    fun read(spaceId: String) = closeGeneration(spaceId, STATE_READ)

    /**
     * SD-0 — the space was deleted from this device, so its rows go with it.
     *
     * EVERYTHING, INCLUDING THE OPAQUE KEY. Every other terminal state keeps
     * a tombstone, because the events still exist in a log that will sync
     * again and the dedup memory is what stops them being announced twice.
     * Here the log itself is gone. What would be left is a table saying how
     * many messages arrived in a space somebody asked to be rid of, kept
     * against a replay that cannot happen.
     *
     * SAFE BECAUSE OF THE ORDER ON THE OTHER SIDE. The core tells the host
     * about a deletion through the SAME queue as candidates, so anything
     * already in flight for this space has been handled before this runs and
     * is removed by it. Nothing can arrive afterwards: the space does not
     * exist. And if the person joins again tomorrow, the core takes a fresh
     * baseline at attach, so the imported history is history and announces
     * nothing.
     */
    fun forgetSpace(spaceId: String) {
        val db = writableDatabase
        db.beginTransaction()
        try {
            db.execSQL("DELETE FROM notification_events WHERE space_id = ?", arrayOf(spaceId))
            db.execSQL("DELETE FROM notification_spaces WHERE space_id = ?", arrayOf(spaceId))
            db.setTransactionSuccessful()
        } finally {
            db.endTransaction()
        }
    }

    private fun closeGeneration(spaceId: String, terminal: String) {
        val db = writableDatabase
        db.beginTransaction()
        try {
            db.execSQL(
                "UPDATE notification_events SET state = ?, space_label = NULL, " +
                    "sender_label = NULL, preview_text = NULL " +
                    "WHERE space_id = ? AND state IN (?, ?)",
                arrayOf(terminal, spaceId, STATE_PENDING, STATE_PRESENTED),
            )
            db.execSQL(
                "UPDATE notification_spaces SET active_generation = active_generation + 1, " +
                    "active_count = 0 WHERE space_id = ?",
                arrayOf(spaceId),
            )
            db.setTransactionSuccessful()
        } finally {
            db.endTransaction()
        }
    }

    private fun setState(eventId: String, state: String, wipeSnapshot: Boolean) {
        val sql = if (wipeSnapshot) {
            "UPDATE notification_events SET state = ?, space_label = NULL, " +
                "sender_label = NULL, preview_text = NULL WHERE event_id = ?"
        } else {
            "UPDATE notification_events SET state = ? WHERE event_id = ?"
        }
        writableDatabase.execSQL(sql, arrayOf(state, eventId))
    }

    // ------------------------------------------------------------- the reads

    /**
     * What recovery needs: everything acknowledged to the core but never
     * confirmed as posted, grouped by space, oldest first. This is the answer
     * to "the process died between the transaction and the system call".
     */
    fun pendingBySpace(): Map<String, Recovery> = bySpace(arrayOf(STATE_PENDING))

    /**
     * Everything still live — pending OR presented. Recovery must post only
     * what was never posted; taking notifications down has to reach what is
     * already on screen, and asking one question for both left a refused
     * permission with every existing card still showing.
     */
    fun activeBySpace(): Map<String, Recovery> = bySpace(arrayOf(STATE_PENDING, STATE_PRESENTED))

    /**
     * Only what we believe Android is HOLDING right now. Separate from
     * [activeBySpace] because reconciling against the system must not touch a
     * pending row: pending means not posted yet, so its absence from the shade
     * is correct rather than evidence that anything was cleared.
     */
    fun presentedBySpace(): Map<String, Recovery> = bySpace(arrayOf(STATE_PRESENTED))

    private fun bySpace(states: Array<String>): Map<String, Recovery> {
        val out = LinkedHashMap<String, Recovery>()
        val marks = states.joinToString(",") { "?" }
        readableDatabase.rawQuery(
            "SELECT DISTINCT space_id FROM notification_events WHERE state IN ($marks)",
            states,
        ).use { c ->
            while (c.moveToNext()) {
                val spaceId = c.getString(0)
                val key = existingSpaceKey(spaceId) ?: continue
                val gen = querySpaceLong(readableDatabase, spaceId, "active_generation")
                out[spaceId] = Recovery(
                    tag = "space:$key",
                    spaceLabel = spaceLabelOf(spaceId),
                    items = aggregationLocked(readableDatabase, spaceId, gen),
                )
            }
        }
        return out
    }

    /**
     * AR-1b.5.5 — drop what can never be replayed, and nothing else.
     *
     * Only TERMINAL rows, and only those strictly behind the floor the core
     * gave. `pending` and `presented` are still part of a live aggregation and
     * are never touched; a row at or after the floor is still reachable by a
     * checkpoint rollback, and deleting it is how a dismissed notification
     * comes back as news.
     *
     * The opaque key is NOT dropped with the rows: it is the space's stable
     * identity in the shade, and re-minting it would split one conversation's
     * notifications in two. It goes only when the space itself does.
     */
    fun compact(retainFrom: Map<String, Map<String, Long>>): Int {
        val terminal = arrayOf(
            STATE_DISMISSED, STATE_READ,
            STATE_SUPPRESSED_ON_SCREEN, STATE_SUPPRESSED_PERMISSION,
        )
        var removed = 0
        val db = writableDatabase
        db.beginTransaction()
        try {
            for ((spaceId, devices) in retainFrom) {
                for ((device, floor) in devices) {
                    if (floor <= 0) continue
                    val marks = terminal.joinToString(",") { "?" }
                    val args = mutableListOf(spaceId, device, floor.toString())
                    args.addAll(terminal)
                    removed += db.delete(
                        "notification_events",
                        "space_id = ? AND device = ? AND source_sequence < ? " +
                            "AND state IN ($marks)",
                        args.toTypedArray(),
                    )
                }
            }
            db.setTransactionSuccessful()
        } finally {
            db.endTransaction()
        }
        return removed
    }

    /**
     * Empties the ledger. FOR TESTS ONLY, and named so — the product has no
     * business forgetting what it has shown, and a compaction that could be
     * mistaken for this one would be a way to resurrect every notification a
     * person has already dealt with.
     */
    fun wipeForTest() {
        val db = writableDatabase
        db.beginTransaction()
        try {
            db.execSQL("DELETE FROM notification_events")
            db.execSQL("DELETE FROM notification_spaces")
            db.execSQL("DELETE FROM notification_people")
            db.setTransactionSuccessful()
        } finally {
            db.endTransaction()
        }
    }

    fun stats(): Map<String, Int> {
        val out = LinkedHashMap<String, Int>()
        readableDatabase.rawQuery(
            "SELECT state, COUNT(*) FROM notification_events GROUP BY state", null,
        ).use { c ->
            while (c.moveToNext()) out[c.getString(0)] = c.getInt(1)
        }
        readableDatabase.rawQuery("SELECT COUNT(*) FROM notification_spaces", null).use { c ->
            if (c.moveToNext()) out["spaces"] = c.getInt(0)
        }
        return out
    }

    // ---------------------------------------------------------------- private

    private fun aggregationLocked(
        db: SQLiteDatabase,
        spaceId: String,
        generation: Long,
    ): List<NotificationCoordinator.Item> {
        val items = mutableListOf<NotificationCoordinator.Item>()
        db.rawQuery(
            "SELECT event_id, device, schema, occurred_at_unix_ms, sender_label, preview_text, personal " +
                "FROM notification_events WHERE space_id = ? AND generation = ? " +
                "AND state IN (?, ?) ORDER BY occurred_at_unix_ms, event_id",
            arrayOf(spaceId, generation.toString(), STATE_PENDING, STATE_PRESENTED),
        ).use { c ->
            while (c.moveToNext()) {
                items.add(
                    NotificationCoordinator.Item(
                        eventId = c.getString(0),
                        device = c.getString(1),
                        schema = c.getString(2),
                        occurredAtUnixMs = c.getLong(3),
                        senderLabel = c.getString(4) ?: "",
                        previewText = c.getString(5) ?: "",
                        personal = c.getInt(6) != 0,
                    )
                )
            }
        }
        // Oldest first, bounded the way a conversation notification is.
        return if (items.size <= NotificationCoordinator.MESSAGES_PER_SPACE) items
        else items.subList(items.size - NotificationCoordinator.MESSAGES_PER_SPACE, items.size)
    }

    /**
     * The space's row and its opaque key, created if absent. 128 random bits;
     * the retry exists because UNIQUE is what makes the collision impossible
     * rather than merely unlikely, and a loop is cheaper than a paragraph
     * explaining a probability.
     */
    private fun ensureSpaceKey(db: SQLiteDatabase, spaceId: String): String {
        db.rawQuery(
            "SELECT notification_key FROM notification_spaces WHERE space_id = ?",
            arrayOf(spaceId),
        ).use { c -> if (c.moveToNext()) return c.getString(0) }

        repeat(8) {
            val key = randomKey()
            try {
                val v = ContentValues().apply {
                    put("space_id", spaceId)
                    put("notification_key", key)
                }
                db.insertOrThrow("notification_spaces", null, v)
                return key
            } catch (e: SQLiteConstraintException) {
                // Either the key collided (128 bits — it did not) or another
                // transaction created the row first. Both are answered by
                // reading it back.
                db.rawQuery(
                    "SELECT notification_key FROM notification_spaces WHERE space_id = ?",
                    arrayOf(spaceId),
                ).use { c -> if (c.moveToNext()) return c.getString(0) }
            }
        }
        throw IllegalStateException("could not allocate a notification key for a space")
    }

    /**
     * The device-local opaque key for a sender, minted once and remembered.
     *
     * 128 bits with UNIQUE and a retry, like the space's tag and for the same
     * reason: what leaves this process must be stable enough for Android to
     * thread a conversation with, and meaningless to anything that reads it.
     *
     * The label is kept beside it — not as identity, only so a renamed person
     * shows their new name under the SAME key rather than arriving as somebody
     * new.
     */
    fun personKey(device: String, label: String): String {
        val db = writableDatabase
        db.rawQuery(
            "SELECT opaque_person_key FROM notification_people WHERE sender_device = ?",
            arrayOf(device),
        ).use { c ->
            if (c.moveToNext()) {
                val key = c.getString(0)
                if (label.isNotEmpty()) {
                    db.execSQL(
                        "UPDATE notification_people SET last_known_label = ? WHERE sender_device = ?",
                        arrayOf(label, device),
                    )
                }
                return key
            }
        }
        repeat(8) {
            try {
                val v = ContentValues().apply {
                    put("sender_device", device)
                    put("opaque_person_key", randomKey())
                    put("last_known_label", label)
                }
                db.insertOrThrow("notification_people", null, v)
            } catch (e: SQLiteConstraintException) {
                // A key collision at 128 bits, or another thread got there
                // first. Both are answered by reading it back.
            }
            db.rawQuery(
                "SELECT opaque_person_key FROM notification_people WHERE sender_device = ?",
                arrayOf(device),
            ).use { c -> if (c.moveToNext()) return c.getString(0) }
        }
        throw IllegalStateException("could not allocate a person key")
    }

    /** The opaque tag for a space, or null when it has never had one. */
    fun tagFor(spaceId: String): String? = existingSpaceKey(spaceId)?.let { "space:$it" }

    private fun existingSpaceKey(spaceId: String): String? {
        readableDatabase.rawQuery(
            "SELECT notification_key FROM notification_spaces WHERE space_id = ?",
            arrayOf(spaceId),
        ).use { c -> return if (c.moveToNext()) c.getString(0) else null }
    }

    private fun querySpaceLong(db: SQLiteDatabase, spaceId: String, column: String): Long {
        db.rawQuery(
            "SELECT $column FROM notification_spaces WHERE space_id = ?", arrayOf(spaceId),
        ).use { c -> return if (c.moveToNext()) c.getLong(0) else 0L }
    }

    private fun randomKey(): String {
        val b = ByteArray(16)
        SecureRandom().nextBytes(b)
        return b.joinToString("") { "%02x".format(it) }
    }

    class Insert(
        val tag: String,
        val spaceLabel: String,
        val items: List<NotificationCoordinator.Item>,
    )

    class Recovery(
        val tag: String,
        val spaceLabel: String,
        val items: List<NotificationCoordinator.Item>,
    )

    /**
     * The space's last known label, from whichever live row still carries one.
     * Empty is ordinary: a terminal row has had its snapshot wiped, and a
     * space whose manifest never arrived never had one.
     */
    private fun spaceLabelOf(spaceId: String): String {
        readableDatabase.rawQuery(
            "SELECT space_label FROM notification_events WHERE space_id = ? " +
                "AND space_label IS NOT NULL AND space_label != '' " +
                "ORDER BY occurred_at_unix_ms DESC LIMIT 1",
            arrayOf(spaceId),
        ).use { c -> return if (c.moveToNext()) c.getString(0) ?: "" else "" }
    }

    companion object {
        const val NAME = "quiet-notifications.db"
        // 2 — source_sequence and notification_people. Every schema change
        // moves this, or an existing install quietly stops storing anything.
        const val VERSION = 3

        const val STATE_PENDING = "pending"
        const val STATE_PRESENTED = "presented"
        const val STATE_DISMISSED = "dismissed"
        const val STATE_READ = "read"
        const val STATE_SUPPRESSED_ON_SCREEN = "suppressed_on_screen"
        const val STATE_SUPPRESSED_PERMISSION = "suppressed_permission"
    }
}
