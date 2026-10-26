package life.meyers.mecha
import org.junit.Assert.*
import org.junit.Test
import java.io.File
class AgentAvatarTest {
    @Test fun matchesWebUtf16JsonRendezvousAndColorVectors() {
        val directory = listOf(File("../web/router-ui-react/avatars"), File("web/router-ui-react/avatars")).first { it.isDirectory }
        val keys = directory.listFiles()!!.filter { it.extension == "png" }.map { "../../avatars/${it.name}" }
        val vectors = listOf(Triple("worker", "robot062.png", "#93e25a"), Triple("hello \"there\"", "robot338.png", "#a552e0"), Triple("robot🤖", "robot063.png", "#5656e1"), Triple("line\n\t\\", "robot070.png", "#e7de79"), Triple("\ud800", "robot246.png", "#e156d6"))
        for ((name, image, color) in vectors) {
            assertEquals("../../avatars/$image", avatarAssetKey(name, keys))
            assertEquals(avatarAssetKey(name, keys), avatarAssetKey(name, keys.reversed()))
            assertEquals(color, avatarColor(name))
        }
    }
    @Test(expected = IllegalArgumentException::class) fun emptyAssetsFailClearly() { avatarAssetKey("worker", emptyList()) }
}
