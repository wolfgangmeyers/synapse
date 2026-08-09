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
    fun connectedStatusDetailUsesAgentDisplayNameAndRouterDisplayName() {
        assertEquals("Android Qol Parity · Home lab", connectedStatusDetail("android-qol-parity", "Home lab"))
    }

    @Test
    fun savingInactiveRouterKeepsCurrentActiveRouter() {
        assertEquals("active:8765", activeRouterAfterRouterSave("active:8765", "inactive:8765", "renamed:8765"))
        assertEquals("renamed:8765", activeRouterAfterRouterSave("active:8765", "active:8765", "renamed:8765"))
        assertEquals("new:8765", activeRouterAfterRouterSave("active:8765", null, "new:8765"))
    }
}
