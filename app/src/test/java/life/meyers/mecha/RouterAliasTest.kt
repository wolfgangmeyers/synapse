package life.meyers.mecha

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Test

class RouterAliasTest {
    @Test
    fun routerConfigUsesAliasWhenPresentAndEndpointOtherwise() {
        assertEquals("Home lab", RouterConfig("Home lab", "router.example:8765").displayName)
        assertEquals("router.example:8765", RouterConfig("", "router.example:8765").displayName)
    }

    @Test
    fun parseRouterConfigSupportsNewLegacyAndRawRecords() {
        assertEquals("Home lab\u001frouter.example:8765", RouterConfig("Home lab", "router.example:8765").persist())
        assertEquals("\u001frouter.example:8765", RouterConfig("", "router.example:8765").persist())
        assertEquals(RouterConfig("Home lab", "router.example:8765"), parseRouterConfig("  Home lab  \u001frouter.example:8765", allowCleartextLoopback = true))
        assertEquals(RouterConfig("", "router.example:8765"), parseRouterConfig("\u001frouter.example:8765", allowCleartextLoopback = true))
        assertEquals(RouterConfig("", "router.example:8765"), parseRouterConfig("router.example:8765", allowCleartextLoopback = true))
        assertNull(parseRouterConfig("not a router://bad"))
    }

    @Test
    fun routerEndpointNormalizationRejectsInvalidPortsAndDefaultsMissingPorts() {
        assertEquals("router.example:8765", normalizeRouterEndpointCandidate("router.example", allowCleartextLoopback = true))
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
        val routers = listOf(RouterConfig("Old", "127.0.0.1:8765"), RouterConfig("Other", "localhost:8765", "https"))

        val updated = upsertRouterConfig(
            routers = routers,
            candidate = "https://new-router.example",
            alias = "  Home lab  ",
            previousEndpoint = "127.0.0.1:8765",
            allowCleartextLoopback = true,
        )

        assertEquals(listOf(RouterConfig("Other", "localhost:8765", "https"), RouterConfig("Home lab", "new-router.example:8765", "https")), updated.sortedBy { it.endpoint })
    }

    @Test
    fun httpAndHttpsProfilesWithTheSameEndpointRemainDistinct() {
        val http = RouterConfig("Local", "router.example:8765", "http")
        val https = RouterConfig("Secure", "router.example:8765", "https")
        val updated = upsertRouterConfig(listOf(http), "https://router.example:8765", "Secure", allowCleartextLoopback = false)

        assertEquals(listOf(http, https), updated.sortedBy { it.scheme })
        assertEquals(https.identity, persistedRouterConfig(https.identity, updated, allowCleartextLoopback = false)?.identity)
    }

    @Test
    fun releaseEnumerationDropsLegacyCleartextAndSavingPurgesIt() {
        val legacy = RouterConfig("Legacy", "router.example:8765").persist()
        val secure = RouterConfig("Secure", "secure.example:443", "https")
        val enumerated = routerConfigsFromRecords(listOf(legacy, secure.persist()), allowCleartextLoopback = false)

        assertEquals(listOf(secure), enumerated)

        val updated = upsertRouterConfig(enumerated, "https://new.example", "New", allowCleartextLoopback = false)
        assertEquals(
            setOf(secure.persist(), RouterConfig("New", "new.example:8765", "https").persist()),
            routerRecordsForPersistence(updated, allowCleartextLoopback = false),
        )
    }

    @Test
    fun debugEnumerationPreservesConfiguredHttpProfiles() {
        val http = RouterConfig("Local", "router.example:8765").persist()
        val https = RouterConfig("Secure", "router.example:8765", "https").persist()

        assertEquals(
            listOf(
                RouterConfig("Local", "router.example:8765"),
                RouterConfig("Secure", "router.example:8765", "https"),
            ),
            routerConfigsFromRecords(listOf(http, https), allowCleartextLoopback = true),
        )
        assertEquals(
            setOf(http, https),
            routerRecordsForPersistence(
                listOf(RouterConfig("Local", "router.example:8765"), RouterConfig("Secure", "router.example:8765", "https")),
                allowCleartextLoopback = true,
            ),
        )
    }

    @Test
    fun releaseDoesNotRestoreLegacyCleartextAsActive() {
        val http = RouterConfig("Local", "router.example:8765", "http")
        val https = RouterConfig("Secure", "router.example:8765", "https")

        assertEquals(https.identity, persistedRouterConfig("router.example:8765", listOf(http, https), allowCleartextLoopback = false)?.identity)
        assertNull(persistedRouterConfig("router.example:8765", listOf(http), allowCleartextLoopback = false))
        assertEquals(http.identity, persistedRouterConfig("router.example:8765", listOf(http), allowCleartextLoopback = true)?.identity)
    }

    @Test
    fun debugAcceptsUserConfiguredHttpAndWebSocketEndpointsButReleaseRejectsCleartext() {
        assertEquals("127.0.0.1:8765", normalizeRouterEndpointCandidate("http://127.0.0.1:8765", allowCleartextLoopback = true))
        assertEquals("192.168.1.20:8765", normalizeRouterEndpointCandidate("http://192.168.1.20:8765", allowCleartextLoopback = true))
        assertEquals("router.tailnet.example:8765", normalizeRouterEndpointCandidate("http://router.tailnet.example", allowCleartextLoopback = true))
        assertEquals("[fd7a:115c:a1e0::1]:8765", normalizeRouterEndpointCandidate("http://[fd7a:115c:a1e0::1]:8765", allowCleartextLoopback = true))
        assertEquals("http", routerSchemeForCandidate("http://router.example", allowCleartextLoopback = true))
        assertEquals("Release builds require an HTTPS router URL.", routerEndpointValidationMessage("http://router.example", allowCleartextLoopback = false))
        assertNull(normalizeRouterEndpointCandidate("http://router.example", allowCleartextLoopback = false))
        assertEquals("127.0.0.1:8765", normalizeRouterEndpointCandidate("https://127.0.0.1:8765", allowCleartextLoopback = false))
        assertEquals("secure.example:443", normalizeRouterEndpointCandidate("https://secure.example:443", allowCleartextLoopback = false))
        assertNull(normalizeRouterEndpointCandidate("https://user:pass@secure.example"))
        assertNull(normalizeRouterEndpointCandidate("https://secure.example/path"))
        assertNull(normalizeRouterEndpointCandidate("https://secure.example?query=1"))
        if (BuildConfig.DEBUG) {
            assertEquals("http://router.example:8765", routerHttpUrl("router.example:8765", "http"))
            assertEquals("ws://router.example:8765/v1/agents", routerWebSocketUrl("router.example:8765", "http"))
        } else {
            assertThrows(IllegalArgumentException::class.java) { routerHttpUrl("router.example:8765", "http") }
            assertThrows(IllegalArgumentException::class.java) { routerWebSocketUrl("router.example:8765", "http") }
        }
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
