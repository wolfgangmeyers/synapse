package life.meyers.mecha

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

/**
 * SlotController coordinates independent slot URLs across the application.
 */
class SlotController {
    val slotUrls = mutableStateMapOf<String, String>()

    fun setSlotUrl(slotId: String, url: String) {
        slotUrls[slotId] = url
    }

    fun getSlotUrl(slotId: String, defaultUrl: String): String {
        return slotUrls[slotId] ?: defaultUrl
    }
}

val LocalSlotController = staticCompositionLocalOf { SlotController() }

/**
 * RemoteSlotRenderer hydrates a subview from an HTTP endpoint and listens for update_slot mutations.
 */
@Composable
fun RemoteSlotRenderer(
    slotId: String,
    viewUrl: String,
    modifier: Modifier = Modifier,
) {
    val slotController = LocalSlotController.current
    val actionDispatcher = LocalActionDispatcher.current
    val currentUrl = slotController.slotUrls[slotId] ?: viewUrl

    var surface by remember(currentUrl) { mutableStateOf<Surface?>(null) }
    var isLoading by remember(currentUrl) { mutableStateOf(true) }
    var errorMessage by remember(currentUrl) { mutableStateOf<String?>(null) }
    var reloadCount by remember { mutableIntStateOf(0) }

    LaunchedEffect(currentUrl, reloadCount) {
        if (currentUrl.isBlank()) {
            isLoading = false
            return@LaunchedEffect
        }
        isLoading = true
        errorMessage = null
        try {
            val fetched = actionDispatcher.fetchSurface(currentUrl)
            surface = fetched
            isLoading = false
        } catch (e: Exception) {
            errorMessage = e.message ?: "Failed to load remote slot"
            isLoading = false
        }
    }

    Box(modifier = modifier) {
        when {
            isLoading -> {
                Box(
                    modifier = Modifier
                        .fillMaxWidth()
                        .padding(24.dp),
                    contentAlignment = Alignment.Center
                ) {
                    CircularProgressIndicator(
                        color = Teal,
                        strokeWidth = 2.dp,
                        modifier = Modifier.size(28.dp)
                    )
                }
            }
            errorMessage != null -> {
                Card(
                    modifier = Modifier
                        .fillMaxWidth()
                        .padding(8.dp),
                    shape = RoundedCornerShape(10.dp),
                    colors = CardDefaults.cardColors(containerColor = SurfaceDark),
                    border = BorderStroke(1.dp, Coral)
                ) {
                    Column(
                        modifier = Modifier.padding(16.dp),
                        verticalArrangement = Arrangement.spacedBy(8.dp),
                        horizontalAlignment = Alignment.CenterHorizontally
                    ) {
                        Text(
                            text = errorMessage ?: "Failed to load subview",
                            color = Coral,
                            fontSize = 13.sp
                        )
                        OutlinedButton(
                            onClick = { reloadCount++ },
                            border = BorderStroke(1.dp, Teal),
                            colors = ButtonDefaults.outlinedButtonColors(contentColor = Teal)
                        ) {
                            Text("Retry")
                        }
                    }
                }
            }
            surface != null -> {
                Column(
                    modifier = Modifier.fillMaxWidth(),
                    verticalArrangement = Arrangement.spacedBy(8.dp)
                ) {
                    surface?.blocks?.forEach { block ->
                        BlockRenderer(block = block)
                    }
                }
            }
        }
    }
}
