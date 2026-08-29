package life.meyers.mecha

import java.io.File
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class AndroidTransportSecurityTest {
    @Test
    fun phoneHeaderAndOverflowMenuUseAccessibleLayoutAndColors() {
        val source = projectFile("app/src/main/java/life/meyers/mecha/MainActivity.kt").readText()

        assertTrue(source.contains("Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = \"Back\""))
        assertFalse(source.contains("Text(\"Back\")"))
        assertTrue(source.contains("Icon(Icons.Default.Edit, contentDescription = \"Use prompt template\""))
        assertTrue(source.contains("containerColor = SurfaceDark"))
        assertTrue(source.contains("MenuDefaults.itemColors(textColor = TextPrimary)"))
        assertTrue(source.contains("items(routers, key = { it.identity })"))
    }

    @Test
    fun debugNetworkConfigAllowsConfiguredCleartextButReleaseRemainsHttpsOnly() {
        val mainManifest = projectFile("app/src/main/AndroidManifest.xml").readText()
        val releaseConfig = projectFile("app/src/main/res/xml/network_security_config.xml").readText()
        val debugConfig = projectFile("app/src/debug/res/xml/network_security_config.xml").readText()

        assertTrue(mainManifest.contains("android:networkSecurityConfig=\"@xml/network_security_config\""))
        assertFalse(mainManifest.contains("android:usesCleartextTraffic=\"true\""))
        assertFalse(releaseConfig.contains("cleartextTrafficPermitted=\"true\""))
        assertTrue(releaseConfig.contains("<base-config cleartextTrafficPermitted=\"false\" />"))
        assertTrue(debugConfig.contains("<base-config cleartextTrafficPermitted=\"true\" />"))
        assertFalse(debugConfig.contains("adb reverse"))
    }

    private fun projectFile(path: String): File = sequenceOf(File(path), File("../$path"))
        .first { it.isFile }
}
