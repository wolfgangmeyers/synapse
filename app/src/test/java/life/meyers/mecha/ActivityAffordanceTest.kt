package life.meyers.mecha

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class ActivityAffordanceTest {
    @Test
    fun activityOrbitMapsOnlyWorkingAgents() {
        assertEquals(ActivityOrbit.WORKING, activityOrbitFor(PeerStatus("worker", activityState = "active")))
        assertNull(activityOrbitFor(PeerStatus("worker", activityState = "idle", subagentCount = 2)))
        assertNull(activityOrbitFor(PeerStatus("worker", activityState = "compacting", subagentCount = 2)))
    }
}
