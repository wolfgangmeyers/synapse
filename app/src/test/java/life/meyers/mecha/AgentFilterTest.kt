package life.meyers.mecha

import org.junit.Assert.assertEquals
import org.junit.Test

class AgentFilterTest {
    @Test
    fun visibleConnectedPeersKeepsAllPeersForBlankFilter() {
        val peers = listOf("supervisor", "android-qol-parity", "wolfgang.web")

        assertEquals(peers, visibleConnectedPeers(peers, "   "))
    }

    @Test
    fun visibleConnectedPeersMatchesRawIdsAndHumanNamesCaseInsensitively() {
        val peers = listOf("supervisor", "android-qol-parity", "wolfgang.web", "note_agent")

        assertEquals(listOf("android-qol-parity"), visibleConnectedPeers(peers, "qol"))
        assertEquals(listOf("android-qol-parity"), visibleConnectedPeers(peers, "QOL PARITY"))
        assertEquals(listOf("wolfgang.web"), visibleConnectedPeers(peers, "wolfgang web"))
        assertEquals(emptyList<String>(), visibleConnectedPeers(peers, "missing"))
    }
}
