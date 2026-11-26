package life.meyers.mecha

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

// Cyberpunk Dark Glassmorphic Design Palette
internal val Ink = Color(0xFF0B0D14)
internal val SurfaceDark = Color(0xFF151925)
internal val SurfaceRaised = Color(0xFF202638)
internal val SurfaceInput = Color(0xFF0F131C)
internal val Periwinkle = Color(0xFF9DA8FF)
internal val Teal = Color(0xFF78E0CD)
internal val Coral = Color(0xFFFF6B8B)
internal val Amber = Color(0xFFFFB86C)
internal val TextPrimary = Color(0xFFF2F4FF)
internal val TextMuted = Color(0xFFB5BCD0)
internal val BorderSubtle = Color(0xFF252D42)
internal val BorderGlow = Color(0xFF3D4B6E)

/**
 * Renders a compact cyberpunk status badge pill (e.g. "online", "active", "executed", "idle").
 */
@Composable
internal fun StatusBadge(
    badge: String,
    modifier: Modifier = Modifier,
) {
    val lower = badge.lowercase()
    val (badgeColor, bgColor) = when {
        lower in listOf("online", "active", "primary", "success") -> Pair(Teal, Color(0xFF122C28))
        lower in listOf("executed", "done", "complete") -> Pair(Color(0xFF8BE9FD), Color(0xFF132836))
        lower in listOf("error", "failed", "danger") -> Pair(Coral, Color(0xFF33141F))
        lower in listOf("warning", "busy", "pending") -> Pair(Amber, Color(0xFF332714))
        else -> Pair(Periwinkle, Color(0xFF1C223D))
    }

    Box(
        modifier = modifier
            .background(bgColor, RoundedCornerShape(999.dp))
            .border(1.dp, badgeColor.copy(alpha = 0.5f), RoundedCornerShape(999.dp))
            .padding(horizontal = 8.dp, vertical = 2.dp)
    ) {
        Text(
            text = badge.uppercase(),
            color = badgeColor,
            fontSize = 10.sp,
            fontWeight = FontWeight.Bold,
            fontFamily = FontFamily.Monospace,
            letterSpacing = 0.5.sp,
        )
    }
}
