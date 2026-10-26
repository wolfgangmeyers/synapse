package life.meyers.mecha

import android.graphics.BitmapFactory
import androidx.compose.foundation.Image
import androidx.compose.foundation.layout.size
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.BlendMode
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.ColorFilter
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp

internal fun hashAgentName(name: String): Long {
    var hash = 0x811c9dc5L
    // Kotlin Char and JavaScript charCodeAt both iterate UTF-16 code units.
    for (char in name) hash = ((hash xor char.code.toLong()) * 0x01000193L) and 0xffffffffL
    return hash
}

private fun avatarJsonString(value: String): String = buildString {
    append('"')
    value.forEachIndexed { index, c ->
        when (c) {
            '"' -> append("\\\"")
            '\\' -> append("\\\\")
            '\b' -> append("\\b")
            '\u000c' -> append("\\f")
            '\n' -> append("\\n")
            '\r' -> append("\\r")
            '\t' -> append("\\t")
            else -> {
                val loneSurrogate = (c.isHighSurrogate() && (index + 1 == value.length || !value[index + 1].isLowSurrogate())) ||
                    (c.isLowSurrogate() && (index == 0 || !value[index - 1].isHighSurrogate()))
                if (c.code < 32 || loneSurrogate) append("\\u" + c.code.toString(16).padStart(4, '0')) else append(c)
            }
        }
    }
    append('"')
}

internal fun avatarAssetKey(name: String, keys: List<String>): String {
    require(keys.isNotEmpty()) { "No bundled agent avatars were found" }
    return keys.sorted().maxBy { hashAgentName("agent-avatar/image/v1\u0000[${avatarJsonString(name)},${avatarJsonString(it)}]") }
}

private val avatarColors = listOf(
    "#e56991", "#e18156", "#e2c35c", "#92cc5d", "#47c2ac", "#53bae1", "#8699ea", "#ae85ea", "#db7ae7",
    "#e05252", "#e78b79", "#e6ab70", "#dfad49", "#e7de79", "#c7e04d", "#c0e774", "#93e25a", "#91e774",
    "#56e156", "#67e487", "#52e099", "#70e6be", "#56e1d6", "#67e4e4", "#52c9e0", "#74b7e7", "#5a93e2",
    "#6c8ae5", "#5656e1", "#8470e6", "#a552e0", "#c76ce5", "#e156d6", "#e774ca", "#e25aa9", "#e87d8f",
)
internal fun avatarColor(name: String): String = avatarColors[(hashAgentName("$name\u0000color") % avatarColors.size).toInt()]

@Composable
internal fun AgentAvatar(id: String, size: Dp = 44.dp) {
    val assets = LocalContext.current.assets
    // ponytail: share the web assets and retain only each visible image, without a global bitmap cache.
    val bitmap = remember(id, assets) {
        val keys = assets.list("").orEmpty().filter { it.endsWith(".png") }.map { "../../avatars/$it" }
        val key = avatarAssetKey(id, keys)
        assets.open(key.substringAfterLast('/')).use { BitmapFactory.decodeStream(it).asImageBitmap() }
    }
    Image(bitmap, contentDescription = null, modifier = Modifier.size(size),
        colorFilter = ColorFilter.tint(Color(android.graphics.Color.parseColor(avatarColor(id))), BlendMode.Color))
}
