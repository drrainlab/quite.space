package space.quiet.arprobe

import android.content.Context
import android.database.sqlite.SQLiteDatabase
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith

/**
 * AR-1b — the schema, written down, so that changing it has to be deliberate.
 *
 * THE FAILURE THIS CLOSES happened twice: a column and a table were added
 * while VERSION stayed where it was. Fresh installs got the new schema, every
 * existing install kept the old one, and every insert failed against a column
 * that was not there — correctly, silently, and invisibly. The upgrade test
 * added afterwards proves that a v1 database can become a v2 one. It cannot
 * notice a THIRD change made tomorrow without a version bump, because it only
 * knows the shapes it was told about.
 *
 * A fingerprint can. It is the whole declared shape in one string: tables,
 * columns in order, declared types, not-null, defaults, primary-key position,
 * and every index's columns and uniqueness. Any DDL edit changes it, and the
 * test fails with a diff — which is not a nuisance but the entire point:
 *
 *   IF THIS TEST FAILS, THE ANSWER IS NEVER TO PASTE IN THE NEW STRING.
 *   It is to move VERSION, write the onUpgrade step, add the case to
 *   NotificationDbUpgradeTest, AND THEN update the fingerprint.
 *
 * Auto-index NAMES are deliberately not part of it — sqlite numbers them and
 * the numbering is not a contract. What they index, and whether they are
 * unique, is.
 */
@RunWith(AndroidJUnit4::class)
class NotificationDbFingerprintTest {

    private val ctx: Context
        get() = InstrumentationRegistry.getInstrumentation().targetContext

    @Before
    fun removeAnyExisting() {
        ctx.deleteDatabase(NotificationDb.NAME)
    }

    @After
    fun cleanUp() {
        ctx.deleteDatabase(NotificationDb.NAME)
    }

    @Test
    fun theSchemaIsExactlyWhatIsDeclaredHere() {
        val db = NotificationDb(ctx)
        val actual = fingerprintOf(db.writableDatabase)
        val version = db.writableDatabase.version
        db.close()

        assertEquals(
            "user_version must be the constant the code ships",
            NotificationDb.VERSION, version,
        )
        assertEquals(
            "the schema changed. Move NotificationDb.VERSION, write the " +
                "onUpgrade step, add the case to NotificationDbUpgradeTest, " +
                "and only then update this string.",
            EXPECTED_FRESH, actual,
        )
    }

    /**
     * The stronger half of the shape test in NotificationDbUpgradeTest: not
     * only the same tables and columns, but the same declared types, defaults,
     * keys and indexes.
     *
     * A divergence here is two schemas in the wild that nobody is testing
     * separately — the upgraded one belongs to the people who have been using
     * the app longest.
     */
    @Test
    fun anUpgradedDatabaseIsExactlyWhatIsDeclaredHereToo() {
        createVersionOne().close()
        val db = NotificationDb(ctx)
        val actual = fingerprintOf(db.writableDatabase)
        db.close()

        assertEquals(
            "the migration changed shape. Both the fresh and the upgraded " +
                "schema are pinned, because both exist on real phones.",
            EXPECTED_UPGRADED, actual,
        )
    }

    /**
     * TWO SHAPES EXIST, AND THE DIFFERENCE IS DELIBERATELY NOT REMOVED.
     *
     * `ALTER TABLE ADD COLUMN` appends: on a phone that upgraded from v1,
     * `source_sequence` is the last column of notification_events and carries
     * `DEFAULT 0`; on a fresh install it is the fifth and has no default.
     * SQLite cannot reorder a column without rebuilding the table, and
     * rebuilding it would put the dedup memory — the only thing stopping a
     * person being re-told about messages they read months ago — at risk to
     * fix a difference nothing can observe.
     *
     * NOTHING CAN OBSERVE IT because every query in NotificationDb names its
     * columns; there is no `SELECT *` anywhere, so a cursor's positions belong
     * to the projection and not to the table. The default is likewise
     * invisible: every insert supplies source_sequence.
     *
     * So this test asserts what the code CAN see is identical, and the two
     * exact shapes stay pinned above. If a `SELECT *` ever appears, this
     * comment becomes wrong and the divergence becomes a bug on precisely the
     * installs that have been running longest.
     */
    @Test
    fun bothShapesAgreeOnEverythingTheCodeCanObserve() {
        val fresh = NotificationDb(ctx)
        val freshPrint = observablePartOf(fresh.writableDatabase)
        fresh.close()

        ctx.deleteDatabase(NotificationDb.NAME)
        createVersionOne().close()
        val upgraded = NotificationDb(ctx)
        val upgradedPrint = observablePartOf(upgraded.writableDatabase)
        upgraded.close()

        assertEquals(
            "the upgraded schema differs from the fresh one in something a " +
                "query can see — that is a second schema nobody is testing, " +
                "and it belongs to the people who have had the app longest",
            freshPrint, upgradedPrint,
        )
    }

    /**
     * The same shape with the two things that legitimately differ removed:
     * declaration order and defaults. Names, types, nullability, keys and
     * indexes remain — those change answers.
     */
    private fun observablePartOf(db: SQLiteDatabase): String =
        fingerprintOf(db).lines()
            .map { it.replace(Regex(" default=[^ ]+"), "") }
            .sortedBy { it.trimStart() }
            .joinToString("\n")

    /** The schema as it shipped at VERSION 1, written out rather than derived. */
    private fun createVersionOne(): SQLiteDatabase {
        val db = SQLiteDatabase.openOrCreateDatabase(
            ctx.getDatabasePath(NotificationDb.NAME), null,
        )
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
        db.execSQL("CREATE INDEX idx_events_space_state ON notification_events(space_id, state)")
        db.version = 1
        return db
    }

    /**
     * Everything sqlite will tell us about the declared shape, in a stable
     * order, as one comparable string.
     */
    private fun fingerprintOf(db: SQLiteDatabase): String {
        val out = StringBuilder()
        val tables = mutableListOf<String>()
        db.rawQuery(
            "SELECT name FROM sqlite_master WHERE type='table' AND name LIKE 'notification_%' " +
                "ORDER BY name",
            null,
        ).use { c -> while (c.moveToNext()) tables.add(c.getString(0)) }

        for (table in tables) {
            out.append("table ").append(table).append('\n')
            db.rawQuery("PRAGMA table_info($table)", null).use { c ->
                while (c.moveToNext()) {
                    val name = c.getString(1)
                    val type = c.getString(2)
                    val notNull = c.getInt(3) == 1
                    val dflt = if (c.isNull(4)) "-" else c.getString(4)
                    val pk = c.getInt(5)
                    out.append("  col ").append(name)
                        .append(' ').append(type)
                        .append(if (notNull) " NOT NULL" else " NULL")
                        .append(" default=").append(dflt)
                        .append(" pk=").append(pk)
                        .append('\n')
                }
            }
            // Indexes by what they index, not by what they are called: an
            // auto-index for a UNIQUE column is named sqlite_autoindex_<n> and
            // the number is not a contract.
            val indexes = mutableListOf<String>()
            db.rawQuery("PRAGMA index_list($table)", null).use { c ->
                val nameCol = c.getColumnIndex("name")
                val uniqueCol = c.getColumnIndex("unique")
                val originCol = c.getColumnIndex("origin")
                while (c.moveToNext()) {
                    val idx = c.getString(nameCol)
                    val unique = c.getInt(uniqueCol) == 1
                    val origin = if (originCol >= 0) c.getString(originCol) else "c"
                    val cols = mutableListOf<String>()
                    db.rawQuery("PRAGMA index_info(`$idx`)", null).use { i ->
                        while (i.moveToNext()) cols.add(i.getString(2))
                    }
                    val label = if (origin == "c") idx else "auto"
                    indexes.add(
                        "  index $label(${cols.joinToString(",")})" +
                            if (unique) " unique" else ""
                    )
                }
            }
            indexes.sorted().forEach { out.append(it).append('\n') }
        }
        return out.toString()
    }

    private companion object {
        /**
         * Regenerate ONLY after moving VERSION and writing the migration:
         * the failure message prints the actual shape.
         */
        val EXPECTED_FRESH = """
            table notification_events
              col event_id TEXT NULL default=- pk=1
              col space_id TEXT NOT NULL default=- pk=0
              col device TEXT NOT NULL default=- pk=0
              col schema TEXT NOT NULL default=- pk=0
              col source_sequence INTEGER NOT NULL default=- pk=0
              col occurred_at_unix_ms INTEGER NOT NULL default=- pk=0
              col state TEXT NOT NULL default=- pk=0
              col generation INTEGER NOT NULL default=- pk=0
              col space_label TEXT NULL default=- pk=0
              col sender_label TEXT NULL default=- pk=0
              col preview_text TEXT NULL default=- pk=0
              index auto(event_id) unique
              index idx_events_space_state(space_id,state)
            table notification_people
              col sender_device TEXT NULL default=- pk=1
              col opaque_person_key TEXT NOT NULL default=- pk=0
              col last_known_label TEXT NULL default=- pk=0
              index auto(opaque_person_key) unique
              index auto(sender_device) unique
            table notification_spaces
              col space_id TEXT NULL default=- pk=1
              col notification_key TEXT NOT NULL default=- pk=0
              col active_generation INTEGER NOT NULL default=0 pk=0
              col active_count INTEGER NOT NULL default=0 pk=0
              col latest_event_id TEXT NULL default=- pk=0
              index auto(notification_key) unique
              index auto(space_id) unique
        """.trimIndent() + "\n"

        /** The same schema reached by migration: source_sequence appended. */
        val EXPECTED_UPGRADED = """
            table notification_events
              col event_id TEXT NULL default=- pk=1
              col space_id TEXT NOT NULL default=- pk=0
              col device TEXT NOT NULL default=- pk=0
              col schema TEXT NOT NULL default=- pk=0
              col occurred_at_unix_ms INTEGER NOT NULL default=- pk=0
              col state TEXT NOT NULL default=- pk=0
              col generation INTEGER NOT NULL default=- pk=0
              col space_label TEXT NULL default=- pk=0
              col sender_label TEXT NULL default=- pk=0
              col preview_text TEXT NULL default=- pk=0
              col source_sequence INTEGER NOT NULL default=0 pk=0
              index auto(event_id) unique
              index idx_events_space_state(space_id,state)
            table notification_people
              col sender_device TEXT NULL default=- pk=1
              col opaque_person_key TEXT NOT NULL default=- pk=0
              col last_known_label TEXT NULL default=- pk=0
              index auto(opaque_person_key) unique
              index auto(sender_device) unique
            table notification_spaces
              col space_id TEXT NULL default=- pk=1
              col notification_key TEXT NOT NULL default=- pk=0
              col active_generation INTEGER NOT NULL default=0 pk=0
              col active_count INTEGER NOT NULL default=0 pk=0
              col latest_event_id TEXT NULL default=- pk=0
              index auto(notification_key) unique
              index auto(space_id) unique
        """.trimIndent() + "\n"
    }
}
