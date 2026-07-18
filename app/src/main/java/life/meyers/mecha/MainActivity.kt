package life.meyers.mecha

import android.app.NotificationChannel
import android.app.NotificationManager
import android.Manifest
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import androidx.activity.ComponentActivity
import androidx.activity.compose.BackHandler
import androidx.activity.compose.setContent
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.ChevronRight
import androidx.compose.material.icons.filled.Edit
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.Divider
import androidx.compose.material3.Icon
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString
import org.json.JSONArray
import org.json.JSONObject
import java.time.Instant
import java.util.Timer
import java.util.UUID
import java.time.temporal.ChronoUnit
import kotlin.concurrent.scheduleAtFixedRate

private const val DEFAULT_ROUTER_PORT = 8765
private const val ROUTER_RECORD_SEPARATOR = "\u001f"
private const val PREFS = "mecha"
private const val MESSAGE_NOTIFICATION_CHANNEL = "messages"
private const val NOTIFICATION_PERMISSION_REQUEST_CODE = 1
private val AGENT_ID = Regex("[A-Za-z0-9][A-Za-z0-9._-]*")
private val ROUTER_ENDPOINT = Regex("^([A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*)(?::([0-9]{1,5}))?$")
private val Ink = Color(0xFF0B0D14)
private val SurfaceDark = Color(0xFF151925)
private val SurfaceRaised = Color(0xFF202638)
private val Periwinkle = Color(0xFF9DA8FF)
private val Teal = Color(0xFF78E0CD)
private val TextPrimary = Color(0xFFF2F4FF)
private val TextMuted = Color(0xFFB5BCD0)

internal enum class Direction { INCOMING, OUTGOING }

internal data class ChatMessage(
    val id: String,
    val sender: String,
    val body: String,
    val direction: Direction,
    val createdAt: String,
)

private data class RouterConfig(
    val display: String,
    val endpoint: String,
) {
    fun persist() = "$display$ROUTER_RECORD_SEPARATOR$endpoint"
}

private data class RouterPeer(val router: String, val peer: String)

class MainActivity : ComponentActivity() {
    private val client = OkHttpClient()
    private val stateReceiver = object : android.content.BroadcastReceiver() {
        override fun onReceive(context: Context, intent: Intent) {
            val router = intent.getStringExtra(MailboxConnectionService.EXTRA_ROUTER) ?: return
            if (router == activeRouter) intent.getStringExtra(MailboxConnectionService.EXTRA_CONNECTION)?.let { connection = it }
            peers = livePeers()
            intent.getStringExtra(MailboxConnectionService.EXTRA_PEER)
                ?.let { refreshVisibleConversation(RouterPeer(router, it)) }
        }
    }

    private var agentId by mutableStateOf("")
    private var activeRouter by mutableStateOf("")
    private var connection by mutableStateOf("Connecting to your agents…")
    private var peers by mutableStateOf(emptyList<RouterPeer>())
    private var selectedConversation by mutableStateOf<RouterPeer?>(null)
    private var messages by mutableStateOf(emptyList<ChatMessage>())
    private var draft by mutableStateOf("")
    private var showProfileEditor by mutableStateOf(false)
    private var showRouterManager by mutableStateOf(false)
    private var routerRevision by mutableStateOf(0)
    private var routerError by mutableStateOf("")

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val preferences = prefs()
        agentId = preferences.getString("agent_id", null)
            ?: "android-${UUID.randomUUID()}".also { preferences.edit().putString("agent_id", it).apply() }
        activeRouter = persistedActiveRouter().orEmpty()
        peers = livePeers()
        openConversation(intent)
        setContent { MechaApp() }
        val stateFilter = android.content.IntentFilter(MailboxConnectionService.ACTION_STATE_CHANGED)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            registerReceiver(stateReceiver, stateFilter, Context.RECEIVER_NOT_EXPORTED)
        } else {
            registerReceiver(stateReceiver, stateFilter)
        }
        ensureNotificationsEnabled()
        if (activeRouter.isNotBlank()) MailboxConnectionService.connect(this, activeRouter)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        openConversation(intent)
    }

    override fun onResume() {
        super.onResume()
        peers = livePeers()
        selectedConversation?.let { messages = historyFor(it) }
        if (activeRouter.isNotBlank()) MailboxConnectionService.refresh(this)
    }

    override fun onDestroy() {
        unregisterReceiver(stateReceiver)
        super.onDestroy()
    }

    private fun prefs() = getSharedPreferences(PREFS, MODE_PRIVATE)

    private fun routers(): List<RouterConfig> = prefs().getStringSet("router_endpoints", emptySet())
        .orEmpty()
        .mapNotNull { stored ->
            val parts = stored.split(ROUTER_RECORD_SEPARATOR, limit = 2)
            when {
                parts.size == 2 && normalizeRouterEndpoint(parts[1]) == parts[1] -> RouterConfig(parts[0], parts[1])
                normalizeRouterEndpoint(stored) != null -> RouterConfig(stored, normalizeRouterEndpoint(stored)!!)
                else -> null
            }
        }
        .distinctBy { it.endpoint }
        .sortedBy { it.display.lowercase() }

    private fun persistedActiveRouter(): String? {
        val routers = routers()
        return prefs().getString("active_router", null)?.takeIf { active -> routers.any { it.endpoint == active } }
            ?: routers.firstOrNull()?.endpoint
    }

    private fun routerHttp() = "http://$activeRouter"

    private fun routerWs() = "ws://$activeRouter/v1/agents"

    private fun routerHistoryKey(peer: String) = "history:$activeRouter:$peer"

    private fun knownPeersKey() = "known_peers:$activeRouter"

    private fun knownPeers(): Set<String> = prefs().getStringSet(knownPeersKey(), emptySet())?.toSet() ?: emptySet()

    private fun persistKnownPeers(value: Set<String>): Boolean = prefs().edit()
        .putStringSet(knownPeersKey(), value)
        .commit()

    private fun refreshPeers() {
        if (activeRouter.isNotBlank()) MailboxConnectionService.refresh(this)
    }

    private fun livePeers(): List<RouterPeer> = routers().flatMap { router ->
        prefs().getStringSet("known_peers:${router.endpoint}", emptySet()).orEmpty()
            .map { RouterPeer(router.endpoint, it) }
    }.sortedWith(compareBy<RouterPeer> { it.peer }.thenBy { it.router })

    private fun historyFor(conversation: RouterPeer): List<ChatMessage> =
        ConversationStore.history(this, conversation.router, conversation.peer)

    private fun refreshVisibleConversation(conversation: RouterPeer) {
        if (conversation != selectedConversation) return
        runOnUiThread { if (conversation == selectedConversation) messages = historyFor(conversation) }
    }

    private fun selectConversation(conversation: RouterPeer) {
        if (conversation.peer.isBlank()) return
        selectedConversation = conversation
        messages = historyFor(conversation)
        peers = livePeers()
    }

    private fun openConversation(intent: Intent) {
        val peer = intent.getStringExtra("peer_id")?.takeIf { it.isNotBlank() } ?: return
        val sourceRouter = intent.getStringExtra(MailboxConnectionService.EXTRA_ROUTER)
            ?.takeIf { router -> routers().any { it.endpoint == router } }
            ?: activeRouter
        if (sourceRouter.isNotBlank()) selectConversation(RouterPeer(sourceRouter, peer))
    }

    private fun returnHome() {
        selectedConversation = null
        messages = emptyList()
        draft = ""
    }

    private fun send() {
        val conversation = selectedConversation ?: return
        val recipient = conversation.peer
        val text = draft
        val selectedRouter = conversation.router
        if (text.isBlank() || selectedRouter.isBlank()) return
        val message = ChatMessage(UUID.randomUUID().toString(), agentId, text, Direction.OUTGOING, Instant.now().truncatedTo(ChronoUnit.MILLIS).toString())
        if (!ConversationStore.append(this, selectedRouter, recipient, message)) return
        messages = historyFor(conversation)
        draft = ""
        val body = JSONObject()
            .put("id", message.id)
            .put("from", agentId)
            .put("to", recipient)
            .put("body", text)
            .put("created_at", message.createdAt)
            .toString()
        Thread {
            try {
                client.newCall(
                    Request.Builder().url("http://$selectedRouter/v1/messages")
                        .post(body.toRequestBody("application/json".toMediaType()))
                        .build(),
                ).execute().use { response ->
                    runOnUiThread {
                        if (!response.isSuccessful) connection = "Message not sent (${response.code})"
                        else if (!connection.startsWith("Connected as")) connection = "Reply queued on $selectedRouter; inbound reconnecting"
                    }
                }
            } catch (_: Exception) {
                runOnUiThread { connection = "Message saved locally; router unavailable" }
            }
        }.start()
    }

    private fun saveAgentId(candidate: String) {
        val trimmed = candidate.trim()
        if (!AGENT_ID.matches(trimmed)) {
            connection = "Username must use letters, numbers, ., _, or -"
            return
        }
        if (trimmed == agentId) {
            showProfileEditor = false
            return
        }
        prefs().edit().putString("agent_id", trimmed).commit()
        agentId = trimmed
        showProfileEditor = false
        if (activeRouter.isNotBlank()) MailboxConnectionService.connect(this, activeRouter)
    }

    private fun normalizeRouterEndpoint(candidate: String): String? {
        val match = ROUTER_ENDPOINT.matchEntire(candidate.trim()) ?: return null
        val host = match.groupValues[1].lowercase()
        val port = match.groupValues[2].ifBlank { DEFAULT_ROUTER_PORT.toString() }.toIntOrNull() ?: return null
        return "$host:$port".takeIf { port in 1..65535 }
    }

    private fun saveRouter(candidate: String): Boolean {
        val entered = candidate.trim()
        val endpoint = normalizeRouterEndpoint(entered)
        if (endpoint == null) {
            routerError = "Enter a hostname with an optional port, for example donuts or donuts:8765"
            return false
        }
        val config = RouterConfig(entered, endpoint)
        val updated = (routers().filterNot { it.endpoint == endpoint } + config).map { it.persist() }.toSet()
        prefs().edit().putStringSet("router_endpoints", updated).putString("active_router", endpoint).commit()
        activeRouter = endpoint
        routerRevision++
        peers = livePeers()
        returnHome()
        routerError = ""
        showRouterManager = false
        MailboxConnectionService.connect(this, activeRouter)
        return true
    }

    private fun switchRouter(endpoint: String) {
        if (endpoint == activeRouter) {
            showRouterManager = false
            return
        }
        prefs().edit().putString("active_router", endpoint).commit()
        activeRouter = endpoint
        peers = livePeers()
        returnHome()
        showRouterManager = false
        MailboxConnectionService.connect(this, activeRouter)
    }

    private fun removeRouter(endpoint: String) {
        val remaining = routers().filterNot { it.endpoint == endpoint }
        val editor = prefs().edit()
        prefs().all.keys.filter {
            it == knownPeersKeyFor(endpoint) || it.startsWith("history:$endpoint:") || it.startsWith("conversation_id:$endpoint:")
        }.forEach(editor::remove)
        val next = if (endpoint == activeRouter) remaining.firstOrNull()?.endpoint.orEmpty() else activeRouter
        editor.putStringSet("router_endpoints", remaining.map { it.persist() }.toSet())
        if (next.isBlank()) editor.remove("active_router") else editor.putString("active_router", next)
        editor.commit()
        routerRevision++
        routerError = ""
        if (endpoint == activeRouter) {
            activeRouter = next
            peers = livePeers()
            returnHome()
            if (next.isBlank()) {
                MailboxConnectionService.stop(this)
                connection = "Add a router to connect"
                return
            }
        }
        if (activeRouter.isNotBlank()) MailboxConnectionService.connect(this, activeRouter)
    }

    private fun knownPeersKeyFor(router: String) = "known_peers:$router"

    private fun ensureNotificationsEnabled() {
        createMessageNotificationChannel()
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), NOTIFICATION_PERMISSION_REQUEST_CODE)
        }
    }

    private fun createMessageNotificationChannel() {
        val manager = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        manager.createNotificationChannel(
            NotificationChannel(MESSAGE_NOTIFICATION_CHANNEL, "Messages", NotificationManager.IMPORTANCE_DEFAULT),
        )
    }

    @Composable
    private fun MechaApp() {
        val colors = MaterialTheme.colorScheme.copy(
            background = Ink,
            surface = SurfaceDark,
            surfaceVariant = SurfaceRaised,
            primary = Periwinkle,
            secondary = Teal,
            onBackground = TextPrimary,
            onSurface = TextPrimary,
            onSurfaceVariant = TextMuted,
        )
        MaterialTheme(colorScheme = colors) {
            Surface(Modifier.fillMaxSize(), color = Ink) {
                when {
                    showRouterManager || activeRouter.isBlank() -> RouterManagement(
                        activeRouter = activeRouter,
                        routers = routerRevision.let { routers() },
                        error = routerError,
                        onAdd = ::saveRouter,
                        onSelect = ::switchRouter,
                        onRemove = ::removeRouter,
                    )
                    selectedConversation == null -> PeerHome(connection, agentId, activeRouter, routers(), peers, ::selectConversation, ::refreshPeers, { showProfileEditor = true }, { showRouterManager = true })
                    else -> {
                        val conversation = selectedConversation!!
                        BackHandler(onBack = ::returnHome)
                        Conversation(conversation, messages, draft, { draft = it }, ::send, ::returnHome)
                    }
                }
                if (showProfileEditor) UsernameDialog(agentId, ::saveAgentId, { showProfileEditor = false })
            }
        }
    }
}

@Composable
private fun PeerHome(
    connection: String,
    agentId: String,
    activeRouter: String,
    routers: List<RouterConfig>,
    peers: List<RouterPeer>,
    onPeer: (RouterPeer) -> Unit,
    onRefresh: () -> Unit,
    onEditProfile: () -> Unit,
    onManageRouters: () -> Unit,
) {
    Column(Modifier.fillMaxSize().padding(horizontal = 20.dp, vertical = 18.dp)) {
        TextButton(onManageRouters, Modifier.height(36.dp)) {
            Icon(Icons.AutoMirrored.Filled.ArrowBack, null)
            Spacer(Modifier.width(4.dp))
            Text("Routers")
        }
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
            Column(Modifier.weight(1f)) {
                Text("Mecha", style = MaterialTheme.typography.headlineSmall, fontWeight = FontWeight.Bold)
                Text("Your agent network", style = MaterialTheme.typography.bodyMedium, color = TextMuted)
            }
            IconButton(onRefresh, Modifier.size(48.dp)) {
                Icon(Icons.Default.Refresh, "Refresh agents", tint = Periwinkle)
            }
            IconButton(onEditProfile, Modifier.size(48.dp)) {
                Icon(Icons.Default.Edit, "Edit username", tint = Periwinkle)
            }
        }
        Card(
            Modifier.fillMaxWidth().padding(top = 18.dp),
            colors = CardDefaults.cardColors(containerColor = SurfaceRaised),
            shape = RoundedCornerShape(18.dp),
        ) {
            Row(Modifier.padding(16.dp), verticalAlignment = Alignment.CenterVertically) {
                StatusDot(connection.startsWith("Connected"))
                Spacer(Modifier.width(12.dp))
                Column(Modifier.weight(1f)) {
                    Text(connection, style = MaterialTheme.typography.titleSmall, fontWeight = FontWeight.SemiBold)
                    Text("$agentId · $activeRouter", style = MaterialTheme.typography.bodySmall, color = TextMuted)
                }
            }
        }
        Text("Agents", Modifier.padding(top = 28.dp, bottom = 10.dp), style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.Bold)
        LazyColumn(verticalArrangement = Arrangement.spacedBy(10.dp)) {
            routers.forEach { router ->
                val routerPeers = peers.filter { it.router == router.endpoint }
                item(key = "router:${router.endpoint}") {
                    Text(
                        if (router.endpoint == activeRouter) "${router.display} · Default" else router.display,
                        Modifier.padding(top = 8.dp, bottom = 2.dp),
                        style = MaterialTheme.typography.titleSmall,
                        color = if (router.endpoint == activeRouter) Teal else TextMuted,
                        fontWeight = FontWeight.SemiBold,
                    )
                }
                if (routerPeers.isEmpty()) {
                    item(key = "empty:${router.endpoint}") {
                        Text("No live agents", Modifier.padding(start = 16.dp, bottom = 8.dp), color = TextMuted)
                    }
                } else {
                    items(routerPeers, key = { "${it.router}:${it.peer}" }) { peer -> PeerRow(peer, { onPeer(peer) }) }
                }
            }
        }
    }
}

@Composable
private fun PeerRow(conversation: RouterPeer, onClick: () -> Unit) {
    val peer = conversation.peer
    Card(
        modifier = Modifier.fillMaxWidth().clip(RoundedCornerShape(18.dp)).clickable(onClick = onClick),
        colors = CardDefaults.cardColors(containerColor = SurfaceDark),
        shape = RoundedCornerShape(18.dp),
    ) {
        Row(Modifier.fillMaxWidth().padding(14.dp), verticalAlignment = Alignment.CenterVertically) {
            Avatar(peer)
            Spacer(Modifier.width(14.dp))
            Column(Modifier.weight(1f)) {
                Text(humanName(peer), style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.SemiBold)
                Text("$peer · ${conversation.router}", style = MaterialTheme.typography.bodySmall, color = TextMuted, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
            Icon(Icons.Default.ChevronRight, "Open conversation", tint = TextMuted)
        }
    }
}

@Composable
private fun Conversation(
    conversation: RouterPeer,
    messages: List<ChatMessage>,
    draft: String,
    onDraft: (String) -> Unit,
    onSend: () -> Unit,
    onBack: () -> Unit,
) {
    val peer = conversation.peer
    val transcriptState = rememberLazyListState()
    LaunchedEffect(messages.lastOrNull()?.id) {
        if (messages.isNotEmpty()) transcriptState.animateScrollToItem(messages.lastIndex)
    }
    Column(Modifier.fillMaxSize()) {
        Row(
            Modifier.fillMaxWidth().background(SurfaceDark).padding(horizontal = 12.dp, vertical = 10.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            TextButton(onBack, Modifier.height(48.dp)) {
                Icon(Icons.AutoMirrored.Filled.ArrowBack, null)
                Spacer(Modifier.width(4.dp))
                Text("Back")
            }
            Avatar(peer, 34.dp)
            Spacer(Modifier.width(10.dp))
            Column {
                Text(humanName(peer), style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.Bold)
                Text("$peer · ${conversation.router}", style = MaterialTheme.typography.bodySmall, color = TextMuted)
            }
        }
        HorizontalDivider(color = SurfaceRaised)
        if (messages.isEmpty()) {
            Box(Modifier.weight(1f).fillMaxWidth(), contentAlignment = Alignment.Center) {
                Text("Start a conversation with ${humanName(peer)}", color = TextMuted)
            }
        } else {
            LazyColumn(
                state = transcriptState,
                modifier = Modifier.weight(1f).fillMaxWidth(),
                contentPadding = PaddingValues(horizontal = 16.dp, vertical = 18.dp),
                verticalArrangement = Arrangement.spacedBy(12.dp),
            ) {
                items(messages, key = { it.id }) { message -> MessageBubble(message) }
            }
        }
        HorizontalDivider(color = SurfaceRaised)
        Row(
            Modifier.fillMaxWidth().background(SurfaceDark).padding(12.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            OutlinedTextField(
                value = draft,
                onValueChange = onDraft,
                modifier = Modifier.weight(1f).height(56.dp),
                singleLine = true,
                placeholder = { Text("Message ${humanName(peer)}") },
                shape = RoundedCornerShape(16.dp),
            )
            Spacer(Modifier.width(10.dp))
            Button(
                onClick = onSend,
                modifier = Modifier.height(56.dp),
                shape = RoundedCornerShape(16.dp),
                colors = ButtonDefaults.buttonColors(containerColor = Periwinkle, contentColor = Ink),
            ) { Text("Send", fontWeight = FontWeight.Bold) }
        }
    }
}

@Composable
private fun MessageBubble(message: ChatMessage) {
    val outgoing = message.direction == Direction.OUTGOING
    Column(Modifier.fillMaxWidth(), horizontalAlignment = if (outgoing) Alignment.End else Alignment.Start) {
        Text(
            if (outgoing) "You" else humanName(message.sender),
            style = MaterialTheme.typography.labelSmall,
            color = if (outgoing) Periwinkle else Teal,
            modifier = Modifier.padding(horizontal = 4.dp, vertical = 2.dp),
        )
        Card(
            modifier = Modifier.fillMaxWidth(0.86f),
            shape = RoundedCornerShape(if (outgoing) 18.dp else 18.dp),
            colors = CardDefaults.cardColors(containerColor = if (outgoing) Periwinkle else SurfaceRaised),
            elevation = CardDefaults.cardElevation(defaultElevation = if (outgoing) 0.dp else 3.dp),
        ) {
            Column(Modifier.padding(14.dp)) {
                Text(message.body, color = if (outgoing) Ink else TextPrimary, style = MaterialTheme.typography.bodyLarge)
            }
        }
    }
}

@Composable
private fun UsernameDialog(current: String, onSave: (String) -> Unit, onDismiss: () -> Unit) {
    var value by remember(current) { mutableStateOf(current) }
    AlertDialog(
        onDismissRequest = onDismiss,
        containerColor = SurfaceDark,
        titleContentColor = TextPrimary,
        textContentColor = TextPrimary,
        title = { Text("Router username") },
        text = {
            Column {
                Text("This is your router identity. Saving reconnects Mecha as this username.", color = TextMuted)
                OutlinedTextField(value, { value = it }, Modifier.fillMaxWidth().padding(top = 14.dp), singleLine = true, label = { Text("Username") })
            }
        },
        confirmButton = { TextButton({ onSave(value) }) { Text("Save") } },
        dismissButton = { TextButton(onDismiss) { Text("Cancel") } },
    )
}

@Composable
private fun RouterManagement(
    activeRouter: String,
    routers: List<RouterConfig>,
    error: String,
    onAdd: (String) -> Boolean,
    onSelect: (String) -> Unit,
    onRemove: (String) -> Unit,
) {
    var endpoint by remember { mutableStateOf("") }
    Column(Modifier.fillMaxSize().padding(horizontal = 20.dp, vertical = 18.dp)) {
        Text("Routers", style = MaterialTheme.typography.headlineSmall, fontWeight = FontWeight.Bold)
        Text("Each router keeps its own peers and chats.", style = MaterialTheme.typography.bodyMedium, color = TextMuted)
        OutlinedTextField(
            value = endpoint,
            onValueChange = { endpoint = it },
            modifier = Modifier.fillMaxWidth().padding(top = 24.dp),
            singleLine = true,
            label = { Text("Hostname or hostname:port") },
            placeholder = { Text("router.example.com") },
            isError = error.isNotBlank(),
        )
        if (error.isNotBlank()) Text(error, Modifier.padding(top = 6.dp), style = MaterialTheme.typography.bodySmall, color = Color(0xFFFF8A80))
        Button(
            onClick = { if (onAdd(endpoint)) endpoint = "" },
            enabled = endpoint.isNotBlank(),
            modifier = Modifier.fillMaxWidth().padding(top = 10.dp).height(52.dp),
            shape = RoundedCornerShape(16.dp),
            colors = ButtonDefaults.buttonColors(containerColor = Periwinkle, contentColor = Ink),
        ) { Text("Add router", fontWeight = FontWeight.Bold) }
        Text("Your routers", Modifier.padding(top = 28.dp, bottom = 10.dp), style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.Bold)
        if (routers.isEmpty()) {
            Card(Modifier.fillMaxWidth(), colors = CardDefaults.cardColors(containerColor = SurfaceDark)) {
                Text("Add a router to get started.", Modifier.padding(20.dp), color = TextMuted)
            }
        } else {
            LazyColumn(verticalArrangement = Arrangement.spacedBy(10.dp)) {
                items(routers, key = { it.endpoint }) { router ->
                    Card(
                        Modifier.fillMaxWidth().clickable { onSelect(router.endpoint) },
                        colors = CardDefaults.cardColors(containerColor = if (router.endpoint == activeRouter) SurfaceRaised else SurfaceDark),
                        shape = RoundedCornerShape(18.dp),
                    ) {
                        Row(Modifier.fillMaxWidth().padding(14.dp), verticalAlignment = Alignment.CenterVertically) {
                            Column(Modifier.weight(1f)) {
                                Text(router.display, style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.SemiBold)
                                if (router.endpoint == activeRouter) Text("Active", style = MaterialTheme.typography.bodySmall, color = Teal)
                            }
                            TextButton({ onRemove(router.endpoint) }) { Text("Remove", color = Color(0xFFFFA4A0)) }
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun Avatar(id: String, size: androidx.compose.ui.unit.Dp = 44.dp) {
    val palette = listOf(Periwinkle, Teal, Color(0xFFF5B2D1), Color(0xFFFFC57C))
    val color = palette[(id.hashCode().and(0x7fffffff)) % palette.size]
    Box(Modifier.size(size).clip(CircleShape).background(color), contentAlignment = Alignment.Center) {
        Text(id.firstOrNull()?.uppercase() ?: "?", color = Ink, fontWeight = FontWeight.Bold)
    }
}

@Composable
private fun StatusDot(connected: Boolean) {
    Box(Modifier.size(10.dp).clip(CircleShape).background(if (connected) Teal else Color(0xFFFF8A80)))
}

internal fun humanName(id: String): String = id.split(Regex("[._-]+"))
    .filter { it.isNotBlank() }
    .joinToString(" ") { part -> part.replaceFirstChar { it.uppercase() } }
    .ifBlank { id }
