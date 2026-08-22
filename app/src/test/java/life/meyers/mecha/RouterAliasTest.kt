package life.meyers.mecha

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class RouterAliasTest {
    @Test
    fun routerConfigUsesAliasWhenPresentAndEndpointOtherwise() {
        assertEquals("Home lab", RouterConfig("Home lab", "donuts:8765").displayName)
        assertEquals("donuts:8765", RouterConfig("", "donuts:8765").displayName)
    }

    @Test
    fun parseRouterConfigSupportsNewLegacyAndRawRecords() {
        assertEquals("Home lab\u001fdonuts:8765", RouterConfig("Home lab", "donuts:8765").persist())
        assertEquals("\u001fdonuts:8765", RouterConfig("", "donuts:8765").persist())
        assertEquals(RouterConfig("Home lab", "donuts:8765"), parseRouterConfig("  Home lab  \u001fdonuts:8765"))
        assertEquals(RouterConfig("", "donuts:8765"), parseRouterConfig("\u001fdonuts:8765"))
        assertEquals(RouterConfig("", "donuts:8765"), parseRouterConfig("donuts:8765"))
        assertNull(parseRouterConfig("not a router://bad"))
    }

    @Test
    fun routerEndpointNormalizationRejectsInvalidPortsAndDefaultsMissingPorts() {
        assertEquals("donuts:8765", normalizeRouterEndpointCandidate("Donuts"))
        assertNull(normalizeRouterEndpointCandidate("donuts:0"))
        assertNull(normalizeRouterEndpointCandidate("donuts:70000"))
    }

    @Test
    fun routerUpsertTrimsAliasesAndReplacesEditedEndpoint() {
        val routers = listOf(RouterConfig("Old", "old-router:8765"), RouterConfig("Other", "other:8765"))

        val updated = upsertRouterConfig(
            routers = routers,
            candidate = "New-Router",
            alias = "  Home lab  ",
            previousEndpoint = "old-router:8765",
        )

        assertEquals(listOf(RouterConfig("Home lab", "new-router:8765"), RouterConfig("Other", "other:8765")), updated.sortedBy { it.endpoint })
    }

    @Test
    fun connectionPresentationMatchesWebUiStates() {
        assertEquals(ConnectionDisplayState.DISCONNECTED, connectionDisplayState("Add a router to connect", ""))
        assertEquals("Disconnected", connectionDisplayTitle(ConnectionDisplayState.DISCONNECTED, "", "android"))
        assertEquals("Open routers to connect.", connectionDisplayDetail(ConnectionDisplayState.DISCONNECTED, "", "", "android", "Home lab"))

        assertEquals(ConnectionDisplayState.CONNECTING, connectionDisplayState("Connecting to your agents…", ""))
        assertEquals("Connecting", connectionDisplayTitle(ConnectionDisplayState.CONNECTING, "", "android"))
        assertEquals("Connecting to your agents… · Home lab", connectionDisplayDetail(ConnectionDisplayState.CONNECTING, "", "", "android", "Home lab"))

        assertEquals(ConnectionDisplayState.CONNECTED, connectionDisplayState("Connected as android", ""))
        assertEquals("Connected as android", connectionDisplayTitle(ConnectionDisplayState.CONNECTED, "", "android"))
        assertEquals("", connectionDisplayDetail(ConnectionDisplayState.CONNECTED, "", "", "android", "Home lab"))

        assertEquals(ConnectionDisplayState.RECONNECTING, connectionDisplayState("Router connection closed; retrying…", "", hasPeers = true))
        assertEquals("Reconnecting", connectionDisplayTitle(ConnectionDisplayState.RECONNECTING, "", "android"))
        assertEquals("Router connection closed; retrying… · Home lab", connectionDisplayDetail(ConnectionDisplayState.RECONNECTING, "Router connection closed; retrying…", "", "android", "Home lab"))
        assertEquals(ConnectionDisplayState.RECONNECTING, connectionDisplayState("Router unavailable; retrying…", ""))

        assertEquals(ConnectionDisplayState.ERROR, connectionDisplayState("Router rejected this username", ""))
        assertEquals("Error", connectionDisplayTitle(ConnectionDisplayState.ERROR, "", "android"))
        assertEquals("Router rejected this username · Home lab", connectionDisplayDetail(ConnectionDisplayState.ERROR, "Router rejected this username", "", "android", "Home lab"))
        assertEquals(ConnectionDisplayState.ERROR, connectionDisplayState("anything", "Invalid router"))
        assertEquals("Invalid router · Home lab", connectionDisplayDetail(ConnectionDisplayState.ERROR, "anything", "Invalid router", "android", "Home lab"))
    }

    @Test
    fun savingInactiveRouterKeepsCurrentActiveRouter() {
        assertEquals("active:8765", activeRouterAfterRouterSave("active:8765", "inactive:8765", "renamed:8765"))
        assertEquals("renamed:8765", activeRouterAfterRouterSave("active:8765", "active:8765", "renamed:8765"))
        assertEquals("new:8765", activeRouterAfterRouterSave("active:8765", null, "new:8765"))
    }
}
