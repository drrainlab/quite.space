package space.quiet.arprobe

import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * SP-3.2 — the visible half of the first law, tested as the sentence it is.
 */
class SweepNotificationTextTest {

    @Test
    fun theSessionCardNamesTheOperationAndItsFacts() {
        assertEquals(
            "Sector B3 · 24 min · 1.2 km · GPS ●",
            SweepNotificationText.summary("Sector B3", 24 * 60_000L, 1234, gpsOk = true),
        )
    }

    @Test
    fun aRecorderNotHearingSaysSo() {
        assertEquals(
            "Sector B3 · 3 min · 40 m · no fix ○",
            SweepNotificationText.summary("Sector B3", 3 * 60_000L, 40, gpsOk = false),
        )
    }

    @Test
    fun hoursAndAMissingLabelStillReadAsASentence() {
        assertEquals(
            "1 h 12 min · 5.0 km · GPS ●",
            SweepNotificationText.summary("", 72 * 60_000L, 5049, gpsOk = true),
        )
    }
}
