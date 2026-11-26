package life.meyers.mecha

import android.content.Context
import android.content.SharedPreferences
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import coil.compose.AsyncImage
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import okhttp3.OkHttpClient
import okhttp3.Request
import java.util.concurrent.TimeUnit

data class RouterEndpoint(
    val identity: String,
    val url: String,
)

class MainActivity : ComponentActivity() {
    private val prefs: SharedPreferences by lazy {
        getSharedPreferences("mecha_prefs", Context.MODE_PRIVATE)
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        val intentRouterUrl = intent?.getStringExtra("router_url")
        if (!intentRouterUrl.isNullOrBlank()) {
            prefs.edit().putString("router_url", intentRouterUrl).apply()
        }

        setContent {
            MechaApp(prefs = prefs)
        }
    }

    override fun onNewIntent(intent: android.content.Intent) {
        super.onNewIntent(intent)
        val intentRouterUrl = intent.getStringExtra("router_url")
        if (!intentRouterUrl.isNullOrBlank()) {
            prefs.edit().putString("router_url", intentRouterUrl).apply()
            recreate()
        }
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun MechaApp(prefs: SharedPreferences) {
    val defaultRouterUrl = "http://10.0.2.2:8765"
    var activeRouterUrl by remember {
        mutableStateOf(prefs.getString("router_url", defaultRouterUrl) ?: defaultRouterUrl)
    }

    var routers by remember {
        mutableStateOf(
            listOf(
                RouterEndpoint(identity = "Default Emulator (10.0.2.2)", url = "http://10.0.2.2:8765"),
                RouterEndpoint(identity = "Localhost (127.0.0.1)", url = "http://127.0.0.1:8765"),
            )
        )
    }

    val sduiState = remember { SduiState() }
    val slotController = remember { SlotController() }
    val lightboxController = remember { LightboxController() }
    val modalStack = remember { mutableStateListOf<Surface>() }
    var showRouterSettings by remember { mutableStateOf(false) }
    var reloadBootstrapKey by remember { mutableIntStateOf(0) }

    val actionDispatcher = remember(activeRouterUrl) {
        ActionDispatcher(
            baseUrl = activeRouterUrl,
            state = sduiState,
            slotController = slotController,
            onUpdateSurface = { updated ->
                // If a modal is open, update top of stack, otherwise update slot
                if (modalStack.isNotEmpty()) {
                    modalStack[modalStack.lastIndex] = updated
                }
            },
            onPushSurface = { pushed ->
                modalStack.add(pushed)
            },
        )
    }

    val darkColors = darkColorScheme(
        primary = Teal,
        onPrimary = Ink,
        surface = SurfaceDark,
        onSurface = TextPrimary,
        background = Ink,
        onBackground = TextPrimary,
        outline = BorderSubtle,
    )

    MaterialTheme(colorScheme = darkColors) {
        CompositionLocalProvider(
            LocalSduiState provides sduiState,
            LocalSlotController provides slotController,
            LocalActionDispatcher provides actionDispatcher,
            LocalLightboxController provides lightboxController,
        ) {
            Surface(
                modifier = Modifier.fillMaxSize(),
                color = Ink,
            ) {
                Scaffold(
                    containerColor = Ink,
                    topBar = {
                        TopAppBar(
                            title = {
                                Row(
                                    verticalAlignment = Alignment.CenterVertically,
                                    horizontalArrangement = Arrangement.spacedBy(8.dp)
                                ) {
                                    Text(
                                        text = "MECHA",
                                        fontWeight = FontWeight.Black,
                                        fontFamily = FontFamily.Monospace,
                                        letterSpacing = 2.sp,
                                        fontSize = 18.sp,
                                        color = TextPrimary,
                                    )
                                    StatusBadge(badge = "sdui")
                                }
                            },
                            actions = {
                                IconButton(onClick = { showRouterSettings = true }) {
                                    Icon(
                                        imageVector = Icons.Default.Settings,
                                        contentDescription = "Router Settings",
                                        tint = TextMuted,
                                    )
                                }
                            },
                            colors = TopAppBarDefaults.topAppBarColors(
                                containerColor = SurfaceDark,
                                titleContentColor = TextPrimary,
                            ),
                        )
                    }
                ) { innerPadding ->
                    Box(
                        modifier = Modifier
                            .fillMaxSize()
                            .padding(innerPadding)
                    ) {
                        // Root bootstrap slot
                        val bootstrapUrl = actionDispatcher.resolveUrl("/v1/ui/bootstrap")
                        Column(
                            modifier = Modifier
                                .fillMaxSize()
                                .verticalScroll(rememberScrollState())
                                .padding(12.dp),
                            verticalArrangement = Arrangement.spacedBy(10.dp)
                        ) {
                            RemoteSlotRenderer(
                                slotId = "root_bootstrap",
                                viewUrl = bootstrapUrl,
                                modifier = Modifier.fillMaxWidth(),
                            )
                        }

                        // Modal stack dialog overlay
                        if (modalStack.isNotEmpty()) {
                            val topModal = modalStack.last()
                            ModalSurfaceDialog(
                                surface = topModal,
                                onDismiss = { modalStack.removeAt(modalStack.lastIndex) }
                            )
                        }

                        // Lightbox full-screen viewer
                        lightboxController.activeImageUrl?.let { imageUrl ->
                            LightboxDialog(
                                imageUrl = imageUrl,
                                onDismiss = { lightboxController.dismiss() }
                            )
                        }

                        // Router settings dialog
                        if (showRouterSettings) {
                            RouterSettingsDialog(
                                currentUrl = activeRouterUrl,
                                routers = routers,
                                onSelectRouter = { endpoint ->
                                    activeRouterUrl = endpoint.url
                                    prefs.edit().putString("router_url", endpoint.url).apply()
                                    slotController.slotUrls.clear()
                                    reloadBootstrapKey++
                                    showRouterSettings = false
                                },
                                onSaveCustomUrl = { customUrl ->
                                    activeRouterUrl = customUrl
                                    prefs.edit().putString("router_url", customUrl).apply()
                                    slotController.slotUrls.clear()
                                    reloadBootstrapKey++
                                    showRouterSettings = false
                                },
                                onDismiss = { showRouterSettings = false },
                            )
                        }
                    }
                }
            }
        }
    }
}

/**
 * Fullscreen / modal surface overlay with back navigation.
 */
@Composable
fun ModalSurfaceDialog(
    surface: Surface,
    onDismiss: () -> Unit,
) {
    Dialog(
        onDismissRequest = onDismiss,
        properties = DialogProperties(usePlatformDefaultWidth = false),
    ) {
        Surface(
            modifier = Modifier.fillMaxSize(),
            color = Ink,
        ) {
            Column(
                modifier = Modifier
                    .fillMaxSize()
                    .padding(16.dp),
            ) {
                // Modal header with accessible back navigation
                Row(
                    modifier = Modifier
                        .fillMaxWidth()
                        .padding(bottom = 12.dp),
                    horizontalArrangement = Arrangement.SpaceBetween,
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Row(
                        verticalAlignment = Alignment.CenterVertically,
                        horizontalArrangement = Arrangement.spacedBy(8.dp)
                    ) {
                        IconButton(onClick = onDismiss) {
                            Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back", tint = TextPrimary)
                        }
                        Text(
                            text = surface.title ?: "Modal",
                            color = TextPrimary,
                            fontSize = 18.sp,
                            fontWeight = FontWeight.Bold,
                        )
                    }
                }

                HorizontalDivider(color = BorderSubtle, thickness = 1.dp)

                Column(
                    modifier = Modifier
                        .fillMaxSize()
                        .verticalScroll(rememberScrollState())
                        .padding(vertical = 12.dp),
                    verticalArrangement = Arrangement.spacedBy(10.dp),
                ) {
                    surface.blocks.forEach { block ->
                        BlockRenderer(block = block)
                    }
                }
            }
        }
    }
}

/**
 * Full-screen zoomable lightbox for images.
 */
@Composable
fun LightboxDialog(
    imageUrl: String,
    onDismiss: () -> Unit,
) {
    Dialog(
        onDismissRequest = onDismiss,
        properties = DialogProperties(usePlatformDefaultWidth = false),
    ) {
        Box(
            modifier = Modifier
                .fillMaxSize()
                .background(Color.Black.copy(alpha = 0.95f))
        ) {
            IconButton(
                onClick = onDismiss,
                modifier = Modifier
                    .align(Alignment.TopEnd)
                    .padding(16.dp)
                    .background(SurfaceDark.copy(alpha = 0.8f), CircleShape)
            ) {
                Icon(
                    imageVector = Icons.Default.Close,
                    contentDescription = "Close",
                    tint = TextPrimary,
                )
            }

            AsyncImage(
                model = imageUrl,
                contentDescription = "Lightbox Image",
                contentScale = ContentScale.Fit,
                modifier = Modifier
                    .fillMaxSize()
                    .padding(24.dp),
            )
        }
    }
}

/**
 * Router settings and switcher dialog.
 */
@Composable
fun RouterSettingsDialog(
    currentUrl: String,
    routers: List<RouterEndpoint>,
    onSelectRouter: (RouterEndpoint) -> Unit,
    onSaveCustomUrl: (String) -> Unit,
    onDismiss: () -> Unit,
) {
    var customUrlText by remember { mutableStateOf(currentUrl) }

    Dialog(onDismissRequest = onDismiss) {
        Card(
            modifier = Modifier.fillMaxWidth(),
            shape = RoundedCornerShape(14.dp),
            colors = CardDefaults.cardColors(containerColor = SurfaceDark),
            border = BorderStroke(1.dp, BorderGlow),
        ) {
            Column(
                modifier = Modifier.padding(16.dp),
                verticalArrangement = Arrangement.spacedBy(12.dp),
            ) {
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.SpaceBetween,
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Text(
                        text = "Router Configuration",
                        color = TextPrimary,
                        fontSize = 16.sp,
                        fontWeight = FontWeight.Bold,
                    )
                    IconButton(onClick = onDismiss) {
                        Icon(
                            imageVector = Icons.Default.Close,
                            contentDescription = "Close",
                            tint = TextMuted,
                        )
                    }
                }

                HorizontalDivider(color = BorderSubtle, thickness = 1.dp)

                Text(
                    text = "Configured Endpoints",
                    color = TextMuted,
                    fontSize = 12.sp,
                )

                // List of router endpoints with accessible items key
                LazyColumn(
                    modifier = Modifier.heightIn(max = 140.dp),
                    verticalArrangement = Arrangement.spacedBy(6.dp),
                ) {
                    items(routers, key = { it.identity }) { router ->
                        val isSelected = router.url == currentUrl
                        Row(
                            modifier = Modifier
                                .fillMaxWidth()
                                .clip(RoundedCornerShape(8.dp))
                                .background(if (isSelected) Color(0xFF1B2E33) else SurfaceRaised)
                                .border(1.dp, if (isSelected) Teal else BorderSubtle, RoundedCornerShape(8.dp))
                                .clickable { onSelectRouter(router) }
                                .padding(10.dp),
                            horizontalArrangement = Arrangement.SpaceBetween,
                            verticalAlignment = Alignment.CenterVertically,
                        ) {
                            Column {
                                Text(router.identity, color = TextPrimary, fontSize = 13.sp, fontWeight = FontWeight.Medium)
                                Text(router.url, color = TextMuted, fontSize = 11.sp, fontFamily = FontFamily.Monospace)
                            }
                            if (isSelected) {
                                StatusBadge(badge = "active")
                            }
                        }
                    }
                }

                OutlinedTextField(
                    value = customUrlText,
                    onValueChange = { customUrlText = it },
                    label = { Text("Custom Router URL", color = TextMuted) },
                    singleLine = true,
                    colors = OutlinedTextFieldDefaults.colors(
                        focusedTextColor = TextPrimary,
                        unfocusedTextColor = TextPrimary,
                        focusedBorderColor = Teal,
                        unfocusedBorderColor = BorderSubtle,
                    ),
                    modifier = Modifier.fillMaxWidth(),
                )

                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.End,
                ) {
                    OutlinedButton(
                        onClick = onDismiss,
                        border = BorderStroke(1.dp, BorderSubtle),
                        colors = ButtonDefaults.outlinedButtonColors(contentColor = TextMuted),
                        modifier = Modifier.padding(end = 8.dp),
                    ) {
                        Text("Cancel")
                    }
                    Button(
                        onClick = { onSaveCustomUrl(customUrlText) },
                        colors = ButtonDefaults.buttonColors(containerColor = Teal, contentColor = Ink),
                    ) {
                        Text("Save & Connect")
                    }
                }
            }
        }
    }
}
