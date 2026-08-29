package life.meyers.mecha

import java.io.File
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class AndroidTransportSecurityTest {
    @Test
    fun debugNetworkConfigAllowsLoopbackHostButAppValidationRestrictsCleartextToExactAdbReversePort() {
        val mainManifest = projectFile("app/src/main/AndroidManifest.xml").readText()
        val releaseConfig = projectFile("app/src/main/res/xml/network_security_config.xml").readText()
        val debugConfig = projectFile("app/src/debug/res/xml/network_security_config.xml").readText()

        assertTrue(mainManifest.contains("android:networkSecurityConfig=\"@xml/network_security_config\""))
        assertFalse(mainManifest.contains("android:usesCleartextTraffic=\"true\""))
        assertFalse(releaseConfig.contains("cleartextTrafficPermitted=\"true\""))
        assertTrue(releaseConfig.contains("<base-config cleartextTrafficPermitted=\"false\" />"))
        assertTrue(debugConfig.contains("<base-config cleartextTrafficPermitted=\"false\" />"))
        assertTrue(debugConfig.contains("<domain includeSubdomains=\"false\">127.0.0.1</domain>"))
        assertFalse(debugConfig.contains("<domain includeSubdomains=\"false\">localhost</domain>"))
        assertFalse(debugConfig.contains("<domain includeSubdomains=\"false\">::1</domain>"))
        assertFalse(debugConfig.contains("10.0.2.2"))
    }

    private fun projectFile(path: String): File = sequenceOf(File(path), File("../$path"))
        .first { it.isFile }
}
