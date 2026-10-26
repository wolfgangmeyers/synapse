package life.meyers.mecha

import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test

class AgentDisplayControlsTest {
    @Test fun metricsAreReadOnlyAndReflectCurrentPeerProjection() {
        val peer = peerStatusFromJson("worker", JSONObject("""{"source":"router-managed","context_usage":{"tokens":100,"context_window":1000,"percent":10.0}}"""))
        assertEquals("Context: 10.0% (100 / 1000)", peer.contextUsageLabel)
        assertNull(peerStatusFromJson("other", JSONObject()).contextUsageLabel)
    }
    @Test fun chatOnlyFiltersActivityWithoutChangingSourceAndScrollLockPausesFollow() {
        val source = listOf("message", "tool", "message")
        assertEquals(listOf("message", "message"), filterChatTimeline(source, true) { it == "message" })
        assertEquals(source, filterChatTimeline(source, false) { it == "message" })
        assertEquals(3, source.size)
        assertFalse(shouldFollowTranscript(true, 3))
        assertFalse(shouldFollowTranscript(false, 0))
        assertTrue(shouldFollowTranscript(false, 3))
    }
}
