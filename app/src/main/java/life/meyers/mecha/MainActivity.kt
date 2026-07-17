package life.meyers.mecha

import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
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

private const val ROUTER_HTTP = "http://donuts:8765"
private const val ROUTER_WS = "ws://donuts:8765/v1/agents"
private const val PREFS = "mecha"
private val AGENT_ID = Regex("[A-Za-z0-9][A-Za-z0-9._-]*")
private val Ink = Color(0xFF0B0D14)
private val SurfaceDark = Color(0xFF151925)
private val SurfaceRaised = Color(0xFF202638)
private val Periwinkle = Color(0xFF9DA8FF)
private val Teal = Color(0xFF78E0CD)
private val TextPrimary = Color(0xFFF2F4FF)
private val TextMuted = Color(0xFFB5BCD0)

private enum class Direction { INCOMING, OUTGOING }

private data class ChatMessage(
    val id: String,
    val sender: String,
    val body: String,
    val direction: Direction,
    val createdAt: String,
)

class MainActivity : ComponentActivity() {
    private val client = OkHttpClient()
    private val historyLock = Any()
    private lateinit var instanceId: String
    private var socket: WebSocket? = null
    private var leaseTimer: Timer? = null
    private var leaseId = ""
    private var connectionGeneration = 0
    private val reconnectHandler = Handler(Looper.getMainLooper())

    private var agentId by mutableStateOf("")
    private var connection by mutableStateOf("Connecting to your agents…")
    private var peers by mutableStateOf(emptyList<String>())
    private var selectedPeer by mutableStateOf("")
    private var messages by mutableStateOf(emptyList<ChatMessage>())
    private var draft by mutableStateOf("")
    private var showProfileEditor by mutableStateOf(false)

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val preferences = prefs()
        instanceId = preferences.getString("instance_id", null)
            ?: UUID.randomUUID().toString().also { preferences.edit().putString("instance_id", it).apply() }
        agentId = preferences.getString("agent_id", null)
            ?: "android-${instanceId.take(8)}".also { preferences.edit().putString("agent_id", it).apply() }
        peers = knownPeers().sorted()
        intent.getStringExtra("peer_id")?.takeIf { it.isNotBlank() }?.let(::selectPeer)
        setContent { MechaApp() }
        connect()
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        intent.getStringExtra("peer_id")?.takeIf { it.isNotBlank() }?.let(::selectPeer)
    }

    override fun onDestroy() {
        cancelConnection()
        super.onDestroy()
    }

    private fun prefs() = getSharedPreferences(PREFS, MODE_PRIVATE)

    private fun knownPeers(): Set<String> = prefs().getStringSet("known_peers", emptySet()) ?: emptySet()

    private fun connect() {
        val generation = ++connectionGeneration
        connection = "Connecting to your agents…"
        socket = client.newWebSocket(Request.Builder().url(ROUTER_WS).build(), object : WebSocketListener() {
            override fun onOpen(webSocket: WebSocket, response: Response) {
                if (generation != connectionGeneration) return
                webSocket.send(
                    JSONObject()
                        .put("version", "v1")
                        .put("frame_id", UUID.randomUUID().toString())
                        .put("type", "register")
                        .put("agent_id", agentId)
                        .put("instance_id", instanceId)
                        .toString(),
                )
            }

            override fun onMessage(webSocket: WebSocket, text: String) {
                if (generation != connectionGeneration) return
                val frame = try {
                    JSONObject(text)
                } catch (_: Exception) {
                    connection = "Router sent an invalid response"
                    return
                }
                when (frame.optString("type")) {
                    "registered" -> {
                        leaseId = frame.optString("lease_id")
                        if (leaseId.isBlank()) {
                            connection = "Router rejected this username"
                            return
                        }
                        connection = "Connected as $agentId"
                        startLeaseRenewal(webSocket, generation)
                        discoverPeers(generation)
                    }
                    "lease_rejected" -> connection = "Username is already connected elsewhere"
                    "error" -> connection = "Router error: ${frame.optString("code", "unknown")}"
                    "message" -> receiveMessage(webSocket, frame)
                }
            }

            override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
                if (generation != connectionGeneration) return
                connection = "Router connection closed; retrying…"
                scheduleReconnect(generation)
            }

            override fun onFailure(webSocket: WebSocket, throwable: Throwable, response: Response?) {
                if (generation != connectionGeneration) return
                connection = "Router unavailable; retrying…"
                scheduleReconnect(generation)
            }

            override fun onMessage(webSocket: WebSocket, bytes: ByteString) = Unit
        })
    }

    private fun scheduleReconnect(generation: Int) {
        reconnectHandler.postDelayed({
            if (generation == connectionGeneration) connect()
        }, 1_000)
    }

    private fun cancelConnection() {
        connectionGeneration++
        leaseTimer?.cancel()
        leaseTimer = null
        socket?.close(1000, "reconnecting")
        socket = null
        leaseId = ""
    }

    private fun startLeaseRenewal(webSocket: WebSocket, generation: Int) {
        leaseTimer?.cancel()
        leaseTimer = Timer().apply {
            scheduleAtFixedRate(20_000, 20_000) {
                if (generation != connectionGeneration || leaseId.isBlank()) return@scheduleAtFixedRate
                webSocket.send(
                    JSONObject()
                        .put("version", "v1")
                        .put("frame_id", UUID.randomUUID().toString())
                        .put("type", "lease_renew")
                        .put("agent_id", agentId)
                        .put("instance_id", instanceId)
                        .put("lease_id", leaseId)
                        .toString(),
                )
            }
        }
    }

    private fun discoverPeers(generation: Int) = Thread {
        try {
            client.newCall(Request.Builder().url("$ROUTER_HTTP/v1/agents").build()).execute().use { response ->
                if (!response.isSuccessful || generation != connectionGeneration) return@use
                val agents = JSONObject(response.body?.string().orEmpty()).optJSONArray("agents") ?: return@use
                val discovered = buildSet {
                    for (index in 0 until agents.length()) {
                        agents.optJSONObject(index)?.optString("agent_id")?.takeIf { it.isNotBlank() && it != agentId }?.let(::add)
                    }
                    addAll(knownPeers().filter { it != agentId })
                }
                peers = discovered.sorted()
            }
        } catch (_: Exception) {
            // The active connection remains usable when discovery is unavailable.
        }
    }.start()

    private fun receiveMessage(webSocket: WebSocket, frame: JSONObject) {
        val message = frame.optJSONObject("message") ?: return
        val sender = message.optString("from")
        val body = message.optString("body")
        val id = message.optString("id", UUID.randomUUID().toString())
        if (sender.isBlank() || !appendMessage(sender, ChatMessage(id, sender, body, Direction.INCOMING, Instant.now().toString()))) return
        postInboundNotification(sender, body)
        val delivery = JSONObject()
            .put("version", "v1")
            .put("agent_id", agentId)
            .put("instance_id", instanceId)
            .put("lease_id", leaseId)
            .put("message_id", id)
            .put("attempt_number", frame.optInt("attempt_number", 1))
        webSocket.send(delivery.put("frame_id", UUID.randomUUID().toString()).put("type", "injection_requested").toString())
        webSocket.send(delivery.put("frame_id", UUID.randomUUID().toString()).put("type", "ack").toString())
    }

    private fun historyFor(peer: String): List<ChatMessage> {
        if (peer.isBlank()) return emptyList()
        val raw = prefs().getString("history:$peer", null) ?: return emptyList()
        return try {
            val array = JSONArray(raw)
            List(array.length()) { index ->
                val value = array.getJSONObject(index)
                ChatMessage(
                    id = value.optString("id", UUID.randomUUID().toString()),
                    sender = value.optString("sender", peer),
                    body = value.optString("body"),
                    direction = if (value.optString("direction") == Direction.OUTGOING.name) Direction.OUTGOING else Direction.INCOMING,
                    createdAt = value.optString("created_at", Instant.EPOCH.toString()),
                )
            }
        } catch (_: Exception) {
            migrateLegacyHistory(peer, raw)
        }
    }

    private fun migrateLegacyHistory(peer: String, raw: String): List<ChatMessage> {
        if (raw.isBlank() || raw == "No messages yet") return emptyList()
        return raw.lines().filter { it.isNotBlank() }.map { line ->
            val outgoing = line.startsWith("You · ")
            ChatMessage(
                id = UUID.randomUUID().toString(),
                sender = if (outgoing) agentId else peer,
                body = line.substringAfter(" · ", line),
                direction = if (outgoing) Direction.OUTGOING else Direction.INCOMING,
                createdAt = Instant.EPOCH.toString(),
            )
        }.also { persistHistory(peer, it) }
    }

    private fun persistHistory(peer: String, value: List<ChatMessage>): Boolean {
        val array = JSONArray()
        value.takeLast(200).forEach { message ->
            array.put(JSONObject()
                .put("id", message.id)
                .put("sender", message.sender)
                .put("body", message.body)
                .put("direction", message.direction.name)
                .put("created_at", message.createdAt))
        }
        return prefs().edit().putString("history:$peer", array.toString()).commit()
    }

    private fun appendMessage(peer: String, message: ChatMessage): Boolean = synchronized(historyLock) {
        val existing = historyFor(peer)
        val updated = (existing + message).takeLast(200)
        val saved = prefs().edit()
            .putStringSet("known_peers", knownPeers() + peer)
            .commit()
        if (saved && persistHistory(peer, updated) && selectedPeer == peer) messages = updated
        saved
    }

    private fun selectPeer(peer: String) {
        if (peer.isBlank()) return
        selectedPeer = peer
        messages = historyFor(peer)
        peers = (peers + peer).distinct().sorted()
        prefs().edit().putStringSet("known_peers", knownPeers() + peer).apply()
    }

    private fun returnHome() {
        selectedPeer = ""
        messages = emptyList()
        draft = ""
    }

    private fun send() {
        val recipient = selectedPeer
        val text = draft
        if (recipient.isBlank() || text.isBlank()) return
        val message = ChatMessage(UUID.randomUUID().toString(), agentId, text, Direction.OUTGOING, Instant.now().truncatedTo(ChronoUnit.MILLIS).toString())
        if (!appendMessage(recipient, message)) return
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
                    Request.Builder().url("$ROUTER_HTTP/v1/messages")
                        .post(body.toRequestBody("application/json".toMediaType()))
                        .build(),
                ).execute().use { response ->
                    if (!response.isSuccessful) runOnUiThread { connection = "Message not sent (${response.code})" }
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
        cancelConnection()
        connect()
    }

    private fun conversationId(peer: String): String {
        val key = "conversation_id:$peer"
        return prefs().getString(key, null)
            ?: UUID.randomUUID().toString().also { prefs().edit().putString(key, it).commit() }
    }

    private fun postInboundNotification(from: String, body: String) {
        val conversationId = conversationId(from)
        val manager = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        manager.createNotificationChannel(NotificationChannel("messages", "Messages", NotificationManager.IMPORTANCE_DEFAULT))
        val intent = Intent(this, MainActivity::class.java)
            .setAction("mecha.conversation.$conversationId")
            .setData(android.net.Uri.parse("mecha://conversation/$conversationId"))
            .putExtra("peer_id", from)
        val pending = PendingIntent.getActivity(this, conversationId.hashCode(), intent, PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
        val notification = android.app.Notification.Builder(this, "messages")
            .setSmallIcon(android.R.drawable.stat_notify_chat)
            .setContentTitle(humanName(from))
            .setContentText(body)
            .setContentIntent(pending)
            .setAutoCancel(true)
            .build()
        manager.notify(conversationId, 1, notification)
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
                if (selectedPeer.isBlank()) {
                    PeerHome(connection, agentId, peers, ::selectPeer, { showProfileEditor = true })
                } else {
                    BackHandler(onBack = ::returnHome)
                    Conversation(selectedPeer, messages, draft, { draft = it }, ::send, ::returnHome)
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
    peers: List<String>,
    onPeer: (String) -> Unit,
    onEditProfile: () -> Unit,
) {
    Column(Modifier.fillMaxSize().padding(horizontal = 20.dp, vertical = 18.dp)) {
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
            Column(Modifier.weight(1f)) {
                Text("Mecha", style = MaterialTheme.typography.headlineSmall, fontWeight = FontWeight.Bold)
                Text("Your agent network", style = MaterialTheme.typography.bodyMedium, color = TextMuted)
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
                    Text("Signed in as $agentId", style = MaterialTheme.typography.bodySmall, color = TextMuted)
                }
            }
        }
        Text("Agents", Modifier.padding(top = 28.dp, bottom = 10.dp), style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.Bold)
        if (peers.isEmpty()) {
            Card(Modifier.fillMaxWidth(), colors = CardDefaults.cardColors(containerColor = SurfaceDark)) {
                Text("No connected agents yet", Modifier.padding(20.dp), color = TextMuted)
            }
        } else {
            LazyColumn(verticalArrangement = Arrangement.spacedBy(10.dp)) {
                items(peers, key = { it }) { peer -> PeerRow(peer, { onPeer(peer) }) }
            }
        }
    }
}

@Composable
private fun PeerRow(peer: String, onClick: () -> Unit) {
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
                Text("$peer · Available", style = MaterialTheme.typography.bodySmall, color = TextMuted, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
            Icon(Icons.Default.ChevronRight, "Open conversation", tint = TextMuted)
        }
    }
}

@Composable
private fun Conversation(
    peer: String,
    messages: List<ChatMessage>,
    draft: String,
    onDraft: (String) -> Unit,
    onSend: () -> Unit,
    onBack: () -> Unit,
) {
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
                Text(peer, style = MaterialTheme.typography.bodySmall, color = TextMuted)
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

private fun humanName(id: String): String = id.split(Regex("[._-]+"))
    .filter { it.isNotBlank() }
    .joinToString(" ") { part -> part.replaceFirstChar { it.uppercase() } }
    .ifBlank { id }
