package life.meyers.mecha

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Test

class RouterAliasTest {
    @Test
    fun routerConfigUsesAliasWhenPresentAndEndpointOtherwise() {
        assertEquals("Home lab", RouterConfig("Home lab", "127.0.0.1:18765").displayName)
        assertEquals("127.0.0.1:18765", RouterConfig("", "127.0.0.1:18765").displayName)
    }

    @Test
    fun parseRouterConfigSupportsNewLegacyAndRawRecords() {
        assertEquals("Home lab\u001f127.0.0.1:18765", RouterConfig("Home lab", "127.0.0.1:18765").persist())
        assertEquals("\u001f127.0.0.1:18765", RouterConfig("", "127.0.0.1:18765").persist())
        assertEquals(RouterConfig("Home lab", "127.0.0.1:18765"), parseRouterConfig("  Home lab  \u001f127.0.0.1:18765", allowCleartextLoopback = true))
        assertEquals(RouterConfig("", "127.0.0.1:18765"), parseRouterConfig("\u001f127.0.0.1:18765", allowCleartextLoopback = true))
        assertEquals(RouterConfig("", "127.0.0.1:18765"), parseRouterConfig("127.0.0.1:18765", allowCleartextLoopback = true))
        assertNull(parseRouterConfig("not a router://bad"))
    }

    @Test
    fun routerEndpointNormalizationRejectsInvalidPortsAndDefaultsMissingPorts() {
        assertEquals("127.0.0.1:18765", normalizeRouterEndpointCandidate("127.0.0.1:18765", allowCleartextLoopback = true))
        assertNull(normalizeRouterEndpointCandidate("router.example:0"))
        assertNull(normalizeRouterEndpointCandidate("router.example:70000"))
    }

    @Test
    fun routerConfigPreservesHttpsForHttpAndWebSocketTransport() {
        assertEquals("https", routerSchemeForCandidate("https://Secure-Router"))
        assertEquals(RouterConfig("Secure", "secure-router:8765", "https"), parseRouterConfig("Secure\u001fhttps://secure-router"))
        assertEquals("Secure\u001fhttps://secure-router:8765", RouterConfig("Secure", "secure-router:8765", "https").persist())
    }

    @Test
    fun routerUpsertTrimsAliasesAndReplacesEditedEndpoint() {
        val routers = listOf(RouterConfig("Old", "127.0.0.1:18765"), RouterConfig("Other", "localhost:8765", "https"))

        val updated = upsertRouterConfig(
            routers = routers,
            candidate = "https://new-router.example",
            alias = "  Home lab  ",
            previousEndpoint = "127.0.0.1:18765",
            allowCleartextLoopback = true,
        )

        assertEquals(listOf(RouterConfig("Other", "localhost:8765", "https"), RouterConfig("Home lab", "new-router.example:8765", "https")), updated.sortedBy { it.endpoint })
    }

    @Test
    fun debugCleartextRejectsLegacy8765AndAllowsOnlyTheExactAdbReverse18765Endpoint() {
        assertEquals("127.0.0.1:18765", normalizeRouterEndpointCandidate("http://127.0.0.1:18765", allowCleartextLoopback = true))
        assertEquals("127.0.0.1:18765", normalizeRouterEndpointCandidate("127.0.0.1:18765", allowCleartextLoopback = true))
        assertNull(normalizeRouterEndpointCandidate("http://127.0.0.1:8765", allowCleartextLoopback = true))
        assertNull(parseRouterConfig("\u001f127.0.0.1:8765", allowCleartextLoopback = true))
        assertNull(normalizeRouterEndpointCandidate("http://localhost:18765", allowCleartextLoopback = true))
        assertNull(normalizeRouterEndpointCandidate("http://[::1]:18765", allowCleartextLoopback = true))
        assertEquals(
            "Debug cleartext is limited to http://127.0.0.1:18765. Run adb reverse tcp:18765 tcp:18765.",
            routerEndpointValidationMessage("http://127.0.0.1:8765", allowCleartextLoopback = true),
        )
        assertEquals("127.0.0.1:8765", normalizeRouterEndpointCandidate("https://127.0.0.1:8765", allowCleartextLoopback = false))
        assertNull(normalizeRouterEndpointCandidate("http://router.example"))
        assertNull(normalizeRouterEndpointCandidate("router.example", allowCleartextLoopback = false))
        assertEquals("secure.example:443", normalizeRouterEndpointCandidate("https://secure.example:443", allowCleartextLoopback = false))
        assertNull(normalizeRouterEndpointCandidate("https://user:pass@secure.example"))
        assertNull(normalizeRouterEndpointCandidate("https://secure.example/path"))
        assertNull(normalizeRouterEndpointCandidate("https://secure.example?query=1"))
        if (BuildConfig.DEBUG) {
            assertEquals("http://127.0.0.1:18765", routerHttpUrl("127.0.0.1:18765", "http"))
            assertEquals("ws://127.0.0.1:18765/v1/agents", routerWebSocketUrl("127.0.0.1:18765", "http"))
        } else {
            assertThrows(IllegalArgumentException::class.java) { routerHttpUrl("127.0.0.1:18765", "http") }
            assertThrows(IllegalArgumentException::class.java) { routerWebSocketUrl("127.0.0.1:18765", "http") }
        }
        assertThrows(IllegalArgumentException::class.java) { routerHttpUrl("127.0.0.1:8765", "http") }
        assertThrows(IllegalArgumentException::class.java) { routerWebSocketUrl("127.0.0.1:8765", "http") }
        assertEquals("https://secure.example:443", routerHttpUrl("secure.example:443", "https"))
        assertEquals("wss://secure.example:443/v1/agents", routerWebSocketUrl("secure.example:443", "https"))
    }

    @Test
    fun aliasEditsPreserveTheExistingRouterScheme() {
        val routers = listOf(RouterConfig("Secure", "127.0.0.1:8765", "https"))
        assertEquals(
            listOf(RouterConfig("Renamed", "127.0.0.1:8765", "https")),
            upsertRouterConfig(routers, "127.0.0.1", "Renamed", "127.0.0.1:8765", allowCleartextLoopback = true),
        )
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
