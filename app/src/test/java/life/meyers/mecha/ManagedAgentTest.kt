package life.meyers.mecha

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ManagedAgentTest {
    @Test
    fun normalizeManagedAgentNameReplacesSpacesWithHyphens() {
        assertEquals("my-new-agent", normalizeManagedAgentName("  my  new  agent  "))
        assertEquals("worker-1", normalizeManagedAgentName("worker 1"))
        assertEquals("worker-abc", normalizeManagedAgentName("worker-abc"))
    }

    @Test
    fun peerStatusFromManagedParsesCwdAndWorktree() {
        val json = JSONObject()
            .put("agent_id", "managed-worker")
            .put("cwd", "/Users/wolfgang/code/project")
            .put("worktree", true)
            .put("runtime", "embedded")
            .put("activity_state", "active")

        val peer = peerStatusFromManaged("managed-worker", json)
        assertEquals("/Users/wolfgang/code/project", peer.cwd)
        assertTrue(peer.worktree)
        assertEquals("embedded", peer.runtime)
        assertTrue(peer.isEmbedded)
    }

    @Test
    fun peerStatusFromManagedDefaultsCwdAndWorktreeWhenMissing() {
        val json = JSONObject()
            .put("agent_id", "managed-worker-2")
            .put("runtime", "embedded")

        val peer = peerStatusFromManaged("managed-worker-2", json)
        assertEquals("", peer.cwd)
        assertFalse(peer.worktree)
    }

    @Test
    fun mergeManagedPeersCopiesWorktreeAndCwdToExistingTmuxPeers() {
        val tmuxPeers = listOf(
            PeerStatus("worker-1", cwd = "", worktree = false),
        )
        val managed = listOf(
            JSONObject()
                .put("agent_id", "worker-1")
                .put("cwd", "/Users/wolfgang/code/worktree-1")
                .put("worktree", true)
                .put("runtime", "embedded"),
        )

        val merged = mergeManagedPeers(tmuxPeers, managed, localAgentId = "android")
        val peer = merged.single()
        assertEquals("/Users/wolfgang/code/worktree-1", peer.cwd)
        assertTrue(peer.worktree)
    }
}
