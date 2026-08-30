package life.meyers.mecha

import org.json.JSONArray
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class DiscoveryProjectionTest {
    @Test
    fun discoveryAcceptsTheDeclaredPeerBound() {
        val agents = JSONArray().apply {
            repeat(MAX_DISCOVERED_PEERS) { put(mapOf("agent_id" to "peer-$it")) }
        }

        assertTrue(discoveryResponseWithinBound(agents))
    }

    @Test
    fun oversizedDiscoveryIsRejectedBeforeProjectionMutation() {
        val agents = JSONArray().apply {
            repeat(MAX_DISCOVERED_PEERS + 1) { put(mapOf("agent_id" to "peer-$it")) }
        }

        assertFalse(discoveryResponseWithinBound(agents))
    }
}
