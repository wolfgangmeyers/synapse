package life.meyers.mecha

import androidx.compose.ui.graphics.Color
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class ActivityAffordanceTest {
    @Test
    fun activityOrbitMapsEachStateToItsPaletteColor() {
        assertEquals(Color(0xFF78E0CD), ActivityOrbit.WORKING.color)
        assertEquals(Color(0xFF60A5FA), ActivityOrbit.SUBAGENT.color)
        assertEquals(Color(0xFFC084FC), ActivityOrbit.COMPACTING.color)

        assertEquals(ActivityOrbit.WORKING, activityOrbitFor(PeerStatus("worker", activityState = "active")))
        assertEquals(ActivityOrbit.SUBAGENT, activityOrbitFor(PeerStatus("worker", subagentCount = 2)))
        assertEquals(ActivityOrbit.COMPACTING, activityOrbitFor(PeerStatus("worker", activityState = "compacting", subagentCount = 2)))
        assertNull(activityOrbitFor(PeerStatus("worker")))
    }

    @Test
    fun workingWinsOverSubagentActivityAndCompactingWinsOverBoth() {
        assertEquals(ActivityOrbit.WORKING, activityOrbitFor(PeerStatus("worker", activityState = "active", subagentCount = 2)))
        assertEquals(ActivityOrbit.COMPACTING, activityOrbitFor(PeerStatus("worker", activityState = "compacting", subagentCount = 2)))
    }
}
