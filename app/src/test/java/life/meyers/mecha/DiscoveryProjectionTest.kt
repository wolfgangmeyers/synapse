package life.meyers.mecha

import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.assertEquals
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

    @Test
    fun mergeManagedPeersIncludesEmbeddedWorkersAndFiltersSubagentsWithParent() {
        val tmuxPeers = listOf(
            PeerStatus("worker-1"),
            PeerStatus("subagent-child-1"),
        )
        val managedAgents = listOf(
            JSONObject()
                .put("agent_id", "embedded-worker")
                .put("runtime", "embedded")
                .put("source", "router-managed")
                .put("activity_state", "active")
                .put("model", JSONObject().put("provider", "google-vertex").put("id", "gemini-3.8-flash")),
            JSONObject()
                .put("agent_id", "subagent-child-with-parent")
                .put("parent_agent_id", "embedded-worker")
                .put("runtime", "embedded")
                .put("source", "router-managed"),
            JSONObject()
                .put("agent_id", "android") // localAgentId
                .put("runtime", "embedded")
                .put("source", "router-managed"),
        )

        val merged = mergeManagedPeers(tmuxPeers, managedAgents, localAgentId = "android")
        val ids = merged.map { it.id }

        assertEquals(listOf("embedded-worker", "subagent-child-1", "worker-1"), ids)

        val embeddedPeer = merged.first { it.id == "embedded-worker" }
        assertTrue(embeddedPeer.isEmbedded)
        assertEquals("active", embeddedPeer.activityState)
        assertEquals("router-managed", embeddedPeer.source)
        assertEquals("embedded", embeddedPeer.runtime)
        assertEquals("gemini-3.8-flash", embeddedPeer.modelCatalog.current?.id)

        val tmuxPeer = merged.first { it.id == "worker-1" }
        assertFalse(tmuxPeer.isEmbedded)
        assertEquals("tmux", tmuxPeer.source)
    }

    @Test
    fun mergeManagedPeersUpdatesExistingTmuxPeerWhenAlsoInManaged() {
        val tmuxPeers = listOf(
            PeerStatus("shared-worker", activityState = "idle", instanceId = "instance-1"),
        )
        val managedAgents = listOf(
            JSONObject()
                .put("agent_id", "shared-worker")
                .put("runtime", "embedded")
                .put("source", "router-managed")
                .put("activity_state", "active"),
        )

        val merged = mergeManagedPeers(tmuxPeers, managedAgents, localAgentId = "android")
        val peer = merged.single()
        assertEquals("shared-worker", peer.id)
        assertTrue(peer.isEmbedded)
        assertEquals("active", peer.activityState)
        assertEquals("instance-1", peer.instanceId)
    }
}
