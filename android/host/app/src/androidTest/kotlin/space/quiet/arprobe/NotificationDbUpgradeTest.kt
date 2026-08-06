package space.quiet.arprobe

import android.content.Context
import android.database.sqlite.SQLiteDatabase
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith

/**
 * AR-1b.6b.5 — upgrading over a database somebody already has.
 *
 * THE INVARIANT THIS EXISTS TO ENFORCE, written as a rule because it was
 * broken twice in three commits:
 *
 *   any change to the DDL moves the schema version AND arrives with a test
 *   that upgrades from the previous stored database.
 *
 * Without it, fresh-install tests stay green while the people who actually
 * have the app get nothing. That is precisely what happened: `source_sequence`
 * and the whole `notification_people` table were added while VERSION stayed at
 * 1, so an existing install kept the old schema and every insert failed
 * against a column that was not there. The failure was correct at every layer
 * — the insert threw, the candidate was not acknowledged, the core kept
 * replaying it — and completely invisible from outside.
 *
 * MIGRATIONS ADD; THEY NEVER DROP. The tempting recovery is to recreate the
 * tables, and it would drop the dedup memory: an upgrade would then re-announce
 * every message a person had already read and dismissed. So the tests below
 * assert survival, not merely that the app did not crash.
 */
@RunWith(AndroidJUnit4::class)
class NotificationDbUpgradeTest {

    private val ctx: Context
        get() = InstrumentationRegistry.getInstrumentation().targetContext

    private val dbName = NotificationDb.NAME

    @Before
    fun removeAnyExisting() {
        ctx.deleteDatabase(dbName)
    }

    @After
    fun cleanUp() {
        ctx.deleteDatabase(dbName)
    }

    /**
     * The schema as it shipped in AR-1b.5.3c: no source_sequence, no people
     * table. Written out in full rather than derived, because a migration test
     * that builds the old schema from the new code is testing nothing.
     */
    private fun createVersionOne(): SQLiteDatabase {
        val db = SQLiteDatabase.openOrCreateDatabase(ctx.getDatabasePath(dbName), null)
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
              occurred_at_unix_ms INTEGER NOT NULL,
              state               TEXT NOT NULL,
              generation          INTEGER NOT NULL,
              space_label         TEXT,
              sender_label        TEXT,
              preview_text        TEXT
            )
            """.trimIndent()
        )
        db.version = 1
        return db
    }

    @Test
    fun anUpgradeKeepsWhatThePersonHasAlreadyDealtWith() {
        val old = createVersionOne()
        old.execSQL(
            "INSERT INTO notification_spaces (space_id, notification_key, active_generation, " +
                "active_count) VALUES ('space-a', 'oldkey0123456789', 3, 0)"
        )
        // A message read a week ago: its tombstone is the only thing standing
        // between the person and being told about it again.
        old.execSQL(
            "INSERT INTO notification_events (event_id, space_id, device, schema, " +
                "occurred_at_unix_ms, state, generation) VALUES " +
                "('old-event', 'space-a', 'device-b', 'message.text.v1', 1000, 'read', 2)"
        )
        old.close()

        val db = NotificationDb(ctx)
        // Touching it runs the migration.
        val states = db.stats()

        assertEquals("the tombstone must survive the upgrade", 1, states["read"])

        // The dedup memory still answers: re-delivering the same event after
        // an upgrade must not produce a notification.
        val again = db.insertPending(
            NotificationCoordinator.Candidate(
                eventId = "old-event",
                spaceId = "space-a",
                device = "device-b",
                schema = "message.text.v1",
                sourceSequence = 1,
                occurredAtUnixMs = 1000,
                presentationCursor = 1,
                authoredLocally = false,
            )
        )
        assertEquals(
            "an upgrade that forgot would re-announce every message already read",
            null, again,
        )

        // The opaque key is identity, and identity may not change under a
        // person: a new one would split the conversation's notifications.
        assertEquals("space:oldkey0123456789", db.tagFor("space-a"))
        db.close()
    }

    @Test
    fun theNewColumnsWorkAfterAnUpgradeRatherThanOnlyOnAFreshInstall() {
        createVersionOne().close()

        val db = NotificationDb(ctx)
        val inserted = db.insertPending(
            NotificationCoordinator.Candidate(
                eventId = "new-event",
                spaceId = "space-b",
                device = "device-c",
                schema = "message.text.v1",
                sourceSequence = 42,          // the column added in AR-1b.5.5
                occurredAtUnixMs = 2000,
                presentationCursor = 1,
                authoredLocally = false,
                spaceLabel = "Room",
                senderLabel = "bob",
                previewText = "hello",
            )
        )
        assertNotNull("an upgraded database must accept a candidate", inserted)

        // The table added in AR-1b.6a — the one whose absence failed every
        // instrumented test on the first run.
        val key = db.personKey("device-c", "bob")
        assertTrue("a person key must be mintable after an upgrade", key.length >= 32)
        assertEquals("and stable", key, db.personKey("device-c", "bob"))
        db.close()
    }

    @Test
    fun aFreshInstallAndAnUpgradedOneEndUpWithTheSameShape() {
        // Fresh.
        ctx.deleteDatabase(dbName)
        val fresh = NotificationDb(ctx)
        fresh.personKey("device-x", "x")
        val freshShape = shapeOf(fresh.writableDatabase)
        fresh.close()

        // Upgraded.
        ctx.deleteDatabase(dbName)
        createVersionOne().close()
        val upgraded = NotificationDb(ctx)
        upgraded.personKey("device-x", "x")
        val upgradedShape = shapeOf(upgraded.writableDatabase)
        upgraded.close()

        assertEquals(
            "an upgraded database that differs from a fresh one is a second " +
                "schema nobody is testing",
            freshShape, upgradedShape,
        )
    }

    /** Table and column names, so two databases can be compared by shape. */
    private fun shapeOf(db: SQLiteDatabase): Map<String, List<String>> {
        val out = sortedMapOf<String, List<String>>()
        db.rawQuery(
            "SELECT name FROM sqlite_master WHERE type='table' AND name LIKE 'notification_%'",
            null,
        ).use { c ->
            while (c.moveToNext()) {
                val table = c.getString(0)
                val cols = mutableListOf<String>()
                db.rawQuery("PRAGMA table_info($table)", null).use { t ->
                    while (t.moveToNext()) cols.add(t.getString(1))
                }
                out[table] = cols.sorted()
            }
        }
        return out
    }
}
