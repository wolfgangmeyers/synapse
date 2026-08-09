package life.meyers.mecha

import org.junit.Assert.assertEquals
import org.junit.Test

class AgentFilterTest {
    @Test
    fun visibleConnectedPeersKeepsAllPeersForBlankFilter() {
        val peers = listOf("supervisor", "android-qol-parity", "wolfgang.web").map(::PeerStatus)

        assertEquals(peers, visibleConnectedPeers(peers, "   "))
    }

    @Test
    fun visibleConnectedPeersMatchesRawIdsAndHumanNamesCaseInsensitively() {
        val peers = listOf("supervisor", "android-qol-parity", "wolfgang.web", "note_agent").map(::PeerStatus)

        assertEquals(listOf("android-qol-parity"), visibleConnectedPeers(peers, "qol").map { it.id })
        assertEquals(listOf("android-qol-parity"), visibleConnectedPeers(peers, "QOL PARITY").map { it.id })
        assertEquals(listOf("wolfgang.web"), visibleConnectedPeers(peers, "wolfgang web").map { it.id })
        assertEquals(emptyList<String>(), visibleConnectedPeers(peers, "missing").map { it.id })
    }
}
