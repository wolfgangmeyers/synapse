package life.meyers.mecha

import android.app.NotificationChannel
import android.app.NotificationManager
import android.Manifest
import android.app.PendingIntent
import android.content.ClipData
import android.content.Context
import android.content.SharedPreferences
import android.content.Intent
import android.net.Uri
import android.provider.OpenableColumns
import android.content.pm.PackageManager
import android.graphics.BitmapFactory
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import androidx.activity.ComponentActivity
import androidx.activity.compose.BackHandler
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.animation.core.LinearOutSlowInEasing
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.content.ReceiveContentListener
import androidx.compose.foundation.content.TransferableContent
import androidx.compose.foundation.content.consume
import androidx.compose.foundation.content.contentReceiver
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.PaddingValues
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
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.AccessTime
import androidx.compose.material.icons.filled.ChevronRight
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material.icons.filled.Edit
import androidx.compose.material.icons.filled.Flag
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
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.graphics.vector.path
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
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
import java.io.ByteArrayOutputStream
import java.net.URL
import java.time.Instant
import java.time.ZoneId
import java.time.DateTimeException
import java.time.format.DateTimeFormatter
import java.time.format.FormatStyle
import java.util.Timer
import java.util.UUID
import java.time.temporal.ChronoUnit
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicLong
import kotlin.concurrent.scheduleAtFixedRate
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

private const val DEFAULT_ROUTER_PORT = 8765
private const val ROUTER_RECORD_SEPARATOR = "\u001f"
private const val PREFS = "mecha"
private const val MESSAGE_NOTIFICATION_CHANNEL = "messages"
private const val NOTIFICATION_PERMISSION_REQUEST_CODE = 1
// Keeps durable unsent drafts bounded per router.
private const val MAX_DRAFTS_PER_ROUTER = 50
private const val AGENT_REFRESH_INTERVAL_MS = 10_000L
private const val MODEL_CONTROL_TIMEOUT_MS = 35_000L
private const val MAX_LIVE_UPDATED_PEERS = 1_000
private val AGENT_ID = Regex("[A-Za-z0-9][A-Za-z0-9._-]*")
private val ROUTER_ENDPOINT = Regex("^([A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*)(?::([0-9]{1,5}))?$")
private val Ink = Color(0xFF0B0D14)
private val SurfaceDark = Color(0xFF151925)
private val SurfaceRaised = Color(0xFF202638)
private val Periwinkle = Color(0xFF9DA8FF)
private val Teal = Color(0xFF78E0CD)
private val TextPrimary = Color(0xFFF2F4FF)
private val TextMuted = Color(0xFFB5BCD0)
private val MessageTimestampFormatter: DateTimeFormatter = DateTimeFormatter.ofLocalizedTime(FormatStyle.SHORT).withZone(ZoneId.systemDefault())

internal enum class Direction { INCOMING, OUTGOING }

internal data class ImageReference(
    val id: String,
    val url: String,
    val mimeType: String,
    val sizeBytes: Int,
    val name: String,
)

internal data class SubagentSummary(
    val id: String,
    val description: String,
    val type: String = "",
)

internal data class GoalProjection(
    val enabled: Boolean,
    val goal: String,
)

internal data class ReminderSummary(
    val id: String,
    val target: String,
    val subject: String,
    val body: String,
    val nextDueAt: String,
    val intervalMs: Long?,
    val state: String,
)

internal data class ModelSummary(
    val provider: String,
    val id: String,
    val name: String = "",
)

internal fun deliveryGenerationMatches(
    callbackGeneration: Int,
    currentGeneration: Int,
    socketIsCurrent: Boolean,
    capturedLeaseId: String,
    currentLeaseId: String,
): Boolean = callbackGeneration == currentGeneration &&
    socketIsCurrent &&
    capturedLeaseId.isNotBlank() &&
    capturedLeaseId == currentLeaseId

internal fun shouldSendDeliveryAck(
    injectionSent: Boolean,
    callbackGeneration: Int,
    currentGeneration: Int,
    socketIsCurrent: Boolean,
    capturedLeaseId: String,
    currentLeaseId: String,
): Boolean = injectionSent && deliveryGenerationMatches(
    callbackGeneration,
    currentGeneration,
    socketIsCurrent,
    capturedLeaseId,
    currentLeaseId,
)

internal fun leaseRenewalCanSend(
    callbackGeneration: Int,
    currentGeneration: Int,
    socketIsCurrent: Boolean,
    leaseId: String,
): Boolean = callbackGeneration == currentGeneration && socketIsCurrent && leaseId.isNotBlank()

internal fun modelControlSendMatches(
    callbackGeneration: Int,
    currentGeneration: Int,
    socketIsCurrent: Boolean,
    intendedLeaseId: String,
    currentLeaseId: String,
    intendedTargetInstanceId: String,
    currentTargetInstanceId: String,
): Boolean = callbackGeneration == currentGeneration &&
    socketIsCurrent &&
    intendedLeaseId.isNotBlank() &&
    intendedLeaseId == currentLeaseId &&
    intendedTargetInstanceId.isNotBlank() &&
    intendedTargetInstanceId == currentTargetInstanceId

internal data class PeerStatus(
    val id: String,
    val activityState: String = "idle",
    val subagentCount: Int = 0,
    val subagents: List<SubagentSummary> = emptyList(),
    val instanceId: String = "",
    val goal: GoalProjection? = null,
    val currentModel: ModelSummary? = null,
    val availableModels: List<ModelSummary> = emptyList(),
    val modelCatalogTotalCount: Int = 0,
    val modelCatalogTruncated: Boolean = false,
) {
    val active get() = activityState == "active"
    val compacting get() = activityState == "compacting"
}

private data class HaloPulse(val progress: Float)

private val PaperclipIcon: ImageVector = ImageVector.Builder(
    name = "Paperclip",
    defaultWidth = 24.dp,
    defaultHeight = 24.dp,
    viewportWidth = 24f,
    viewportHeight = 24f,
).apply {
    path(fill = androidx.compose.ui.graphics.SolidColor(Color.Black)) {
        moveTo(16.5f, 6f)
        verticalLineTo(17.5f)
        curveTo(16.5f, 19.71f, 14.71f, 21.5f, 12.5f, 21.5f)
        reflectiveCurveTo(8.5f, 19.71f, 8.5f, 17.5f)
        verticalLineTo(5f)
        curveTo(8.5f, 3.62f, 9.62f, 2.5f, 11f, 2.5f)
        reflectiveCurveTo(13.5f, 3.62f, 13.5f, 5f)
        verticalLineTo(15.5f)
        curveTo(13.5f, 16.05f, 13.05f, 16.5f, 12.5f, 16.5f)
        reflectiveCurveTo(11.5f, 16.05f, 11.5f, 15.5f)
        verticalLineTo(6f)
        horizontalLineTo(10f)
        verticalLineTo(15.5f)
        curveTo(10f, 16.88f, 11.12f, 18f, 12.5f, 18f)
        reflectiveCurveTo(15f, 16.88f, 15f, 15.5f)
        verticalLineTo(5f)
        curveTo(15f, 2.79f, 13.21f, 1f, 11f, 1f)
        reflectiveCurveTo(7f, 2.79f, 7f, 5f)
        verticalLineTo(17.5f)
        curveTo(7f, 20.54f, 9.46f, 23f, 12.5f, 23f)
        reflectiveCurveTo(18f, 20.54f, 18f, 17.5f)
        verticalLineTo(6f)
        horizontalLineTo(16.5f)
        close()
    }
}.build()

internal data class ChatMessage(
    val id: String,
    val incarnationId: String,
    val sequence: Long,
    val sender: String,
    val body: String,
    val direction: Direction,
    val createdAt: String,
    val attachments: List<ImageReference> = emptyList(),
) {
    val key get() = "$id\u001f$incarnationId"
}

internal data class RouterConfig(
    val alias: String,
    val endpoint: String,
) {
    val displayName: String get() = alias.ifBlank { endpoint }
    fun persist() = "$alias$ROUTER_RECORD_SEPARATOR$endpoint"
}

internal class DraftStore(private val preferences: SharedPreferences) {
    fun get(router: String, peer: String) = preferences.getString(draftPreferenceKey(router, peer), "").orEmpty()

    fun save(router: String, peer: String, value: String) {
        val existingPeers = peers(router)
        val retainedPeers = boundedDraftPeers(existingPeers, peer)
        val editor = preferences.edit()
            .putString(draftPreferenceKey(router, peer), value)
            .putString(draftPeersKey(router), retainedPeers.joinToString(","))
        (existingPeers - retainedPeers.toSet()).forEach { editor.remove(draftPreferenceKey(router, it)) }
        editor.commit()
    }

    fun clear(router: String, peer: String) {
        val retainedPeers = peers(router).filterNot { it == peer }
        val editor = preferences.edit().remove(draftPreferenceKey(router, peer))
        if (retainedPeers.isEmpty()) editor.remove(draftPeersKey(router))
        else editor.putString(draftPeersKey(router), retainedPeers.joinToString(","))
        editor.commit()
    }

    fun clearIfUnchanged(router: String, peer: String, sent: String): Boolean {
        if (get(router, peer) != sent) return false
        clear(router, peer)
        return true
    }

    private fun peers(router: String) = preferences.getString(draftPeersKey(router), "")
        .orEmpty()
        .split(",")
        .filter(AGENT_ID::matches)
        .distinct()
}

class MainActivity : ComponentActivity() {
    private val client = OkHttpClient()
    private val historyLock = Any()
    private val connectionGenerationLock = Any()
    private lateinit var instanceId: String
    private lateinit var drafts: DraftStore
    @Volatile private var socket: WebSocket? = null
    @Volatile private var leaseTimer: Timer? = null
    @Volatile private var leaseId = ""
    private val connectionGeneration = AtomicInteger()
    private val peerRequestGeneration = AtomicLong()
    private val reminderRequestGeneration = AtomicLong()
    private val liveUpdatedPeerIds = LinkedHashSet<String>()
    private val deferredEmptyModelCatalogs = LinkedHashMap<String, JSONObject>()
    private val reconnectHandler = Handler(Looper.getMainLooper())

    private var agentId by mutableStateOf("")
    private var activeRouter by mutableStateOf("")
    private var connection by mutableStateOf("Connecting to your agents…")
    private var peers by mutableStateOf(emptyList<PeerStatus>())
    private var reminders by mutableStateOf(emptyList<ReminderSummary>())
    private var reminderError by mutableStateOf("")
    private var agentFilter by mutableStateOf("")
    private var selectedPeer by mutableStateOf("")
    private var messages by mutableStateOf(emptyList<ChatMessage>())
    private var images by mutableStateOf(emptyList<PendingImage>())
    private var draft by mutableStateOf("")
    private var showProfileEditor by mutableStateOf(false)
    private var showRouterManager by mutableStateOf(false)
    private var routerRevision by mutableStateOf(0)
    private var routerError by mutableStateOf("")
    private var editingRouterEndpoint by mutableStateOf<String?>(null)
    private var goalDialogPeer by mutableStateOf("")
    private var reminderDialogPeer by mutableStateOf("")
    private var reminderDialogTick by mutableStateOf(0)
    private var reminderDialogTimer: Timer? = null
    private var reminderDialogSession = 0L
    private var pendingModelControlId by mutableStateOf("")
    private var pendingModelRequestId by mutableStateOf("")
    private var pendingModelPeer by mutableStateOf("")
    private var pendingModelInstanceId by mutableStateOf("")
    private var pendingModel by mutableStateOf<ModelSummary?>(null)
    private var modelControlStatus by mutableStateOf("")
    private var modelControlTimeout: Runnable? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val preferences = prefs()
        drafts = DraftStore(preferences)
        instanceId = preferences.getString("instance_id", null)
            ?: UUID.randomUUID().toString().also { preferences.edit().putString("instance_id", it).apply() }
        agentId = preferences.getString("agent_id", null)
            ?: "android-${instanceId.take(8)}".also { preferences.edit().putString("agent_id", it).apply() }
        activeRouter = persistedActiveRouter().orEmpty()
        peers = emptyList()
        intent.getStringExtra("peer_id")?.takeIf { it.isNotBlank() && activeRouter.isNotBlank() }?.let(::selectPeer)
        setContent { MechaApp() }
        ensureNotificationsEnabled()
        if (activeRouter.isNotBlank()) connect()
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        intent.getStringExtra("peer_id")?.takeIf { it.isNotBlank() && activeRouter.isNotBlank() }?.let(::selectPeer)
    }

    override fun onDestroy() {
        closeGoalDialog()
        closeReminderDialog()
        cancelConnection()
        super.onDestroy()
    }

    private fun prefs() = getSharedPreferences(PREFS, MODE_PRIVATE)

    private fun routers(): List<RouterConfig> = prefs().getStringSet("router_endpoints", emptySet())
        .orEmpty()
        .mapNotNull(::parseRouterConfig)
        .distinctBy { it.endpoint }
        .sortedBy { it.displayName.lowercase() }

    private fun persistedActiveRouter(): String? {
        val routers = routers()
        return prefs().getString("active_router", null)?.takeIf { active -> routers.any { it.endpoint == active } }
            ?: routers.firstOrNull()?.endpoint
    }

    private fun routerHttp() = "http://$activeRouter"

    private fun routerWs() = "ws://$activeRouter/v1/agents"

    private fun routerHistoryKey(peer: String) = "history:$activeRouter:$agentId:$peer"

    private fun legacyRouterHistoryKey(peer: String) = "history:$activeRouter:$peer"

    private fun draftFor(peer: String) = drafts.get(activeRouter, peer)

    private fun updateDraft(value: String) {
        val normalized = normalizeEscapedNewlines(value)
        draft = normalized
        if (selectedPeer.isNotBlank()) drafts.save(activeRouter, selectedPeer, normalized)
    }

    private fun knownPeersKey() = "known_peers:$activeRouter"

    private fun knownPeers(): Set<String> = prefs().getStringSet(knownPeersKey(), emptySet())?.toSet() ?: emptySet()

    private fun persistKnownPeers(value: Set<String>): Boolean = prefs().edit()
        .putStringSet(knownPeersKey(), value)
        .commit()

    private fun connect(expectedGeneration: Int? = null) {
        val router = activeRouter
        val owner = agentId
        val replacedSocket = synchronized(connectionGenerationLock) {
            if (expectedGeneration != null && !scheduledReconnectIsCurrent(expectedGeneration, connectionGeneration.get())) {
                return@synchronized null
            }
            if (router.isBlank()) {
                connection = "Add a router to connect"
                return@synchronized null
            }
            connection = "Connecting to your agents…"
            val generation = connectionGeneration.incrementAndGet()
            val nextSocket = client.newWebSocket(Request.Builder().url("ws://$router/v1/agents").build(), object : WebSocketListener() {
            override fun onOpen(webSocket: WebSocket, response: Response) {
                synchronized(connectionGenerationLock) {
                    if (generation != connectionGeneration.get()) return@synchronized
                    webSocket.send(
                        JSONObject()
                            .put("version", "v1")
                            .put("frame_id", UUID.randomUUID().toString())
                            .put("type", "register")
                            .put("agent_id", owner)
                            .put("instance_id", instanceId)
                            .put("agent_role", "user_interaction")
                            .put("client_type", "android")
                            .put("ack_confirmation", "correlated_v1")
                            .put("conversation_sync", "v1")
                            .toString(),
                    )
                }
            }

            override fun onMessage(webSocket: WebSocket, text: String) {
                if (generation != connectionGeneration.get()) return
                val frame = try {
                    JSONObject(text)
                } catch (_: Exception) {
                    runOnUiThread {
                        if (generation != connectionGeneration.get()) return@runOnUiThread
                        connection = "Router sent an invalid response"
                    }
                    return
                }
                when (frame.optString("type")) {
                    "registered" -> runOnUiThread {
                        if (generation != connectionGeneration.get()) return@runOnUiThread
                        val registeredLeaseId = frame.optString("lease_id")
                        if (registeredLeaseId.isBlank()) {
                            val current = synchronized(connectionGenerationLock) {
                                generation == connectionGeneration.get() && socket === webSocket
                            }
                            if (current) connection = "Router rejected this username"
                            return@runOnUiThread
                        }
                        if (!startLeaseRenewal(webSocket, generation, registeredLeaseId, router, owner)) return@runOnUiThread
                        connection = "Connected as $owner"
                        discoverPeers(generation, router, owner)
                        refreshReminders(generation, router)
                        selectedPeer.takeIf { it.isNotBlank() }?.let { hydrateHistory(it, generation) }
                    }
                    "lease_rejected" -> runOnUiThread {
                        if (generation != connectionGeneration.get()) return@runOnUiThread
                        connection = "Username is already connected elsewhere"
                    }
                    "error" -> runOnUiThread {
                        if (generation != connectionGeneration.get()) return@runOnUiThread
                        if (modelControlRequestMatchesPending(pendingModelRequestId, frame.optString("request_id"))) {
                            settleModelControl(null, frame.optString("code", "router_error"))
                        } else {
                            connection = "Router error; please try again"
                        }
                    }
                    "conversation_message" -> runOnUiThread {
                        if (generation == connectionGeneration.get()) receiveConversationMessage(frame)
                    }
                    "agent_activity" -> runOnUiThread {
                        if (generation == connectionGeneration.get()) receiveAgentActivity(frame)
                    }
                    "agent_model_catalog" -> runOnUiThread {
                        if (generation == connectionGeneration.get()) receiveAgentModelCatalog(frame)
                    }
                    "model_control_result" -> runOnUiThread {
                        if (generation == connectionGeneration.get()) receiveModelControlResult(frame)
                    }
                    "message" -> settleDelivery(webSocket, generation, frame, owner)
                }
            }

            override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
                val reconnectGeneration = invalidateFailedConnection(generation, webSocket) ?: return
                runOnUiThread {
                    if (reconnectGeneration != connectionGeneration.get()) return@runOnUiThread
                    clearModelControlStateForReconnect()
                    liveUpdatedPeerIds.clear()
                    connection = "Router connection closed; retrying…"
                    scheduleReconnect(reconnectGeneration)
                }
            }

            override fun onFailure(webSocket: WebSocket, throwable: Throwable, response: Response?) {
                val reconnectGeneration = invalidateFailedConnection(generation, webSocket) ?: return
                runOnUiThread {
                    if (reconnectGeneration != connectionGeneration.get()) return@runOnUiThread
                    clearModelControlStateForReconnect()
                    liveUpdatedPeerIds.clear()
                    connection = "Router unavailable; retrying…"
                    scheduleReconnect(reconnectGeneration)
                }
            }

            override fun onMessage(webSocket: WebSocket, bytes: ByteString) = Unit
            })
            if (generation != connectionGeneration.get()) {
                nextSocket.close(1000, "stale connection")
                null
            } else {
                socket.also { socket = nextSocket }
            }
        }
        replacedSocket?.close(1000, "replaced")
    }

    private fun invalidateFailedConnection(generation: Int, webSocket: WebSocket): Int? =
        synchronized(connectionGenerationLock) {
            if (generation != connectionGeneration.get()) return@synchronized null
            val reconnectGeneration = connectionGeneration.incrementAndGet()
            leaseTimer?.cancel()
            leaseTimer = null
            leaseId = ""
            if (socket === webSocket) socket = null
            reconnectGeneration
        }

    private fun scheduleReconnect(generation: Int) {
        reconnectHandler.postDelayed({
            connect(expectedGeneration = generation)
        }, 1_000)
    }

    private fun cancelConnection() {
        val currentSocket = synchronized(connectionGenerationLock) {
            connectionGeneration.incrementAndGet()
            leaseTimer?.cancel()
            leaseTimer = null
            leaseId = ""
            socket.also { socket = null }
        }
        currentSocket?.close(1000, "reconnecting")
        liveUpdatedPeerIds.clear()
        closeGoalDialog()
        closeReminderDialog()
        clearModelControlState()
    }

    private fun startLeaseRenewal(
        webSocket: WebSocket,
        generation: Int,
        registeredLeaseId: String,
        router: String,
        owner: String,
    ): Boolean = synchronized(connectionGenerationLock) {
        if (generation != connectionGeneration.get() || socket !== webSocket || registeredLeaseId.isBlank()) return@synchronized false
        leaseTimer?.cancel()
        leaseTimer = null
        leaseId = ""
        val timer = Timer()
        try {
            timer.scheduleAtFixedRate(20_000, 20_000) {
                synchronized(connectionGenerationLock) {
                    val currentLeaseId = leaseId
                    if (!leaseRenewalCanSend(generation, connectionGeneration.get(), socket === webSocket, currentLeaseId)) {
                        return@synchronized
                    }
                    webSocket.send(
                        JSONObject()
                            .put("version", "v1")
                            .put("frame_id", UUID.randomUUID().toString())
                            .put("type", "lease_renew")
                            .put("agent_id", owner)
                            .put("instance_id", instanceId)
                            .put("lease_id", currentLeaseId)
                            .toString(),
                    )
                }
            }
            timer.scheduleAtFixedRate(AGENT_REFRESH_INTERVAL_MS, AGENT_REFRESH_INTERVAL_MS) {
                synchronized(connectionGenerationLock) {
                    if (!leaseRenewalCanSend(generation, connectionGeneration.get(), socket === webSocket, leaseId)) {
                        return@synchronized
                    }
                }
                discoverPeers(generation, router, owner)
                refreshReminders(generation, router)
            }
        } catch (_: RuntimeException) {
            timer.cancel()
            return@synchronized false
        }
        leaseId = registeredLeaseId
        leaseTimer = timer
        true
    }

    private fun refreshPeers() {
        val generation = connectionGeneration.get()
        val router = activeRouter
        val owner = agentId
        if (leaseId.isNotBlank()) discoverPeers(generation, router, owner)
    }

    private fun discoverPeers(generation: Int, router: String, owner: String) {
        val requestGeneration = peerRequestGeneration.incrementAndGet()
        Thread {
            try {
                client.newCall(Request.Builder().url("http://$router/v1/agents").build()).execute().use { response ->
                    if (!response.isSuccessful || generation != connectionGeneration.get()) return@use
                    val agents = JSONObject(response.body?.string().orEmpty()).optJSONArray("agents") ?: return@use
                    val discovered = buildList {
                        for (index in 0 until agents.length()) {
                            val agent = agents.optJSONObject(index) ?: continue
                            val peer = agent.optString("agent_id").takeIf { it.isNotBlank() && it != owner } ?: continue
                            add(peer to agent)
                        }
                    }
                    runOnUiThread {
                        if (generation != connectionGeneration.get() || requestGeneration != peerRequestGeneration.get()) return@runOnUiThread
                        val currentPeers = peers
                        val pendingControlId = pendingModelControlId
                        val pendingControlPeer = pendingModelPeer
                        val pendingControlInstanceId = pendingModelInstanceId
                        val nextPeers = discovered.map { (peer, source) ->
                            val sourceInstanceId = (source.opt("instance_id") as? String)?.trim().orEmpty()
                            val catalogValues = modelCatalogValuesFromFrame(source)
                            val deferred = deferredEmptyModelCatalogs[peer]
                            val matchingDeferred = deferred?.takeIf {
                                val deferredInstanceId = (it.opt("instance_id") as? String)?.trim().orEmpty()
                                sourceInstanceId.isNotBlank() &&
                                    deferredInstanceId.isNotBlank() &&
                                    deferredInstanceId == sourceInstanceId &&
                                    modelCatalogUpdateMatchesPending(
                                    peer = peer,
                                    frame = it,
                                    pendingControlId = pendingControlId,
                                    pendingControlPeer = pendingControlPeer,
                                    pendingControlInstanceId = pendingControlInstanceId,
                                )
                            }
                            when {
                                catalogValues == null -> Unit
                                catalogValues.isEmpty() && matchingDeferred != null -> {
                                    deferredEmptyModelCatalogs[peer] = JSONObject(source.toString())
                                }
                                catalogValues.isEmpty() && modelCatalogUpdateMatchesPending(
                                    peer = peer,
                                    frame = source,
                                    pendingControlId = pendingControlId,
                                    pendingControlPeer = pendingControlPeer,
                                    pendingControlInstanceId = pendingControlInstanceId,
                                ) -> {
                                    deferredEmptyModelCatalogs[peer] = JSONObject(source.toString())
                                }
                                matchingDeferred == null -> deferredEmptyModelCatalogs.remove(peer)
                            }
                            mergeDiscoveredPeer(
                                peer = peer,
                                source = source,
                                deferredModelCatalogFrame = matchingDeferred,
                                current = currentPeers.firstOrNull { it.id == peer },
                                hasLiveUpdate = peer in liveUpdatedPeerIds,
                                pendingControlId = pendingControlId,
                                pendingControlPeer = pendingControlPeer,
                                pendingControlInstanceId = pendingControlInstanceId,
                            )
                        }.sortedBy { it.id }
                        peers = nextPeers
                        if (goalDialogShouldClose(goalDialogPeer, nextPeers)) closeGoalDialog()
                    }
                }
            } catch (_: Exception) {
                // The active connection remains usable when discovery is unavailable.
            }
        }.start()
    }

    private fun refreshReminders(generation: Int, router: String) {
        val requestGeneration = reminderRequestGeneration.incrementAndGet()
        Thread {
            try {
                client.newCall(Request.Builder().url("http://$router/v1/reminders").build()).execute().use { response ->
                    val responseSuccessful = response.isSuccessful
                    val body = response.body?.string().orEmpty()
                    val payload = if (responseSuccessful) {
                        runCatching { JSONObject(body).optJSONArray("reminders") }.getOrNull()
                    } else {
                        null
                    }
                    runOnUiThread {
                        if (generation != connectionGeneration.get() || requestGeneration != reminderRequestGeneration.get()) return@runOnUiThread
                        if (!responseSuccessful) {
                            reminderError = "Reminder refresh failed"
                            return@runOnUiThread
                        }
                        val next = reminderRefreshResult(
                            current = reminders,
                            responseSuccessful = responseSuccessful,
                            payload = payload,
                        )
                        val refreshError = reminderRefreshError(responseSuccessful = responseSuccessful, payload = payload)
                        reminders = next
                        reminderError = refreshError
                    }
                }
            } catch (_: Exception) {
                runOnUiThread {
                    if (generation == connectionGeneration.get() && requestGeneration == reminderRequestGeneration.get()) {
                        reminderError = "Reminder refresh failed"
                    }
                }
            }
        }.start()
    }

    private fun receiveAgentActivity(frame: JSONObject) {
        val peer = frame.optString("agent_id").takeIf { it.isNotBlank() && it != agentId } ?: return
        val current = peers.firstOrNull { it.id == peer } ?: return
        if (!acceptsLivePeerInstance(current.instanceId, frame.optString("instance_id").trim())) return
        markLiveUpdatedPeer(peer)
        val updated = peerStatusFromJson(peer, frame, current)
        peers = peers.map { if (it.id == peer) updated else it }
        if (goalDialogPeer == peer && updated.goal?.enabled != true) closeGoalDialog()
    }

    private fun receiveAgentModelCatalog(frame: JSONObject) {
        val peer = frame.optString("agent_id").takeIf { it.isNotBlank() && it != agentId } ?: return
        val current = peers.firstOrNull { it.id == peer } ?: return
        if (!acceptsLivePeerInstance(current.instanceId, frame.optString("instance_id").trim())) return
        val values = modelCatalogValuesFromFrame(frame) ?: return
        val updated = modelCatalogProjection(
            peer = peer,
            current = current,
            frame = frame,
            pendingControlId = pendingModelControlId,
            pendingControlPeer = pendingModelPeer,
            pendingControlInstanceId = pendingModelInstanceId,
        )
        if (updated == null) {
            if (values.isEmpty() && modelCatalogUpdateMatchesPending(
                    peer = peer,
                    frame = frame,
                    pendingControlId = pendingModelControlId,
                    pendingControlPeer = pendingModelPeer,
                    pendingControlInstanceId = pendingModelInstanceId,
                )
            ) {
                deferredEmptyModelCatalogs[peer] = JSONObject(frame.toString())
            }
            return
        }
        deferredEmptyModelCatalogs.remove(peer)
        markLiveUpdatedPeer(peer)
        peers = peers.map { if (it.id == peer) updated else it }
    }

    private fun applyDeferredEmptyModelCatalogs() {
        val deferred = deferredEmptyModelCatalogs.toMap()
        deferredEmptyModelCatalogs.clear()
        deferred.forEach { (peer, frame) ->
            val current = peers.firstOrNull { it.id == peer } ?: return@forEach
            if (!deferredModelCatalogFrameMatchesCurrentPeer(current, frame)) return@forEach
            val updated = modelCatalogProjection(peer, current, frame) ?: return@forEach
            markLiveUpdatedPeer(peer)
            peers = peers.map { if (it.id == peer) updated else it }
        }
    }

    private fun markLiveUpdatedPeer(peer: String) {
        if (liveUpdatedPeerIds.size >= MAX_LIVE_UPDATED_PEERS && peer !in liveUpdatedPeerIds) {
            liveUpdatedPeerIds.firstOrNull()?.let(liveUpdatedPeerIds::remove)
        }
        liveUpdatedPeerIds += peer
    }

    private fun receiveModelControlResult(frame: JSONObject) {
        val currentPendingPeer = peers.firstOrNull { it.id == pendingModelPeer }
        if (!modelControlResultTargetsCurrentPeer(listOfNotNull(currentPendingPeer), pendingModelPeer, pendingModelInstanceId)) return
        val settlement = modelControlResultSettlement(
            frame = frame,
            pendingControlId = pendingModelControlId,
            pendingPeer = pendingModelPeer,
            sourceAgentId = agentId,
            pendingInstanceId = pendingModelInstanceId,
            currentPendingPeer = currentPendingPeer,
        ) ?: return
        settleModelControl(settlement.accepted, settlement.errorCode, settlement.currentModel)
    }

    private fun selectModel(model: ModelSummary) {
        val peer = peers.firstOrNull { it.id == selectedPeer } ?: return
        val intendedLeaseId = leaseId
        val intendedTargetInstanceId = peer.instanceId
        if (intendedLeaseId.isBlank() || intendedTargetInstanceId.isBlank()) {
            modelControlStatus = modelControlStatusTitle(null, "target_not_connected")
            return
        }
        clearModelControlState()
        val controlId = "model-control-${UUID.randomUUID()}"
        val requestId = "model-control-request-${UUID.randomUUID()}"
        pendingModelControlId = controlId
        pendingModelRequestId = requestId
        pendingModelPeer = peer.id
        pendingModelInstanceId = intendedTargetInstanceId
        pendingModel = model
        val sent = synchronized(connectionGenerationLock) {
            val currentGeneration = connectionGeneration.get()
            val currentSocket = socket
            val currentLeaseId = leaseId
            val currentTargetInstanceId = peers.firstOrNull { it.id == selectedPeer }?.instanceId.orEmpty()
            if (currentSocket == null || !modelControlSendMatches(
                    callbackGeneration = currentGeneration,
                    currentGeneration = connectionGeneration.get(),
                    socketIsCurrent = currentSocket === socket,
                    intendedLeaseId = intendedLeaseId,
                    currentLeaseId = currentLeaseId,
                    intendedTargetInstanceId = intendedTargetInstanceId,
                    currentTargetInstanceId = currentTargetInstanceId,
                )
            ) {
                false
            } else {
                val frame = JSONObject()
                    .put("version", "v1")
                    .put("frame_id", requestId)
                    .put("type", "model_control")
                    .put("agent_id", agentId)
                    .put("instance_id", instanceId)
                    .put("lease_id", currentLeaseId)
                    .put("target_agent_id", peer.id)
                    .put("target_instance_id", currentTargetInstanceId)
                    .put("control_id", controlId)
                    .put("action", "set_model")
                    .put("model", JSONObject().put("provider", model.provider).put("id", model.id))
                currentSocket.send(frame.toString())
            }
        }
        if (!sent) {
            runOnUiThread { settleModelControl(null, "ui_not_connected") }
            return
        }
        modelControlTimeout = Runnable {
            if (controlId == pendingModelControlId) settleModelControl(null, "control_timeout")
        }.also { reconnectHandler.postDelayed(it, MODEL_CONTROL_TIMEOUT_MS) }
    }

    private fun settleModelControl(accepted: Boolean?, errorCode: String?, currentModel: ModelSummary? = null) {
        if (modelControlConfirmed(accepted, currentModel) && pendingModelPeer.isNotBlank()) {
            peers = peers.map { peer ->
                if (peer.id == pendingModelPeer) peer.copy(currentModel = currentModel) else peer
            }
        }
        modelControlStatus = modelControlStatusTitle(accepted, errorCode, currentModel)
        clearModelControlState(keepStatus = true)
    }

    private fun clearModelControlState(keepStatus: Boolean = false) {
        modelControlTimeout?.let(reconnectHandler::removeCallbacks)
        modelControlTimeout = null
        pendingModelControlId = ""
        pendingModelRequestId = ""
        pendingModelPeer = ""
        pendingModelInstanceId = ""
        pendingModel = null
        if (!keepStatus) modelControlStatus = ""
        applyDeferredEmptyModelCatalogs()
    }

    private fun clearModelControlStateForReconnect() {
        modelControlTimeout?.let(reconnectHandler::removeCallbacks)
        modelControlTimeout = null
        pendingModelControlId = ""
        pendingModelRequestId = ""
        pendingModelPeer = ""
        pendingModelInstanceId = ""
        pendingModel = null
        modelControlStatus = ""
        deferredEmptyModelCatalogs.clear()
    }

    private fun receiveConversationMessage(frame: JSONObject) {
        val payload = frame.optJSONObject("message") ?: return
        val message = chatMessage(payload, agentId, activeRouter) ?: return
        val peer = if (message.sender == agentId) payload.optString("to") else message.sender
        if (peer.isBlank() || !appendMessage(peer, message)) return
        if (message.direction == Direction.INCOMING) postInboundNotification(peer, message.body)
    }

    private fun settleDelivery(webSocket: WebSocket, generation: Int, frame: JSONObject, owner: String) {
        val message = frame.optJSONObject("message") ?: return
        val id = message.optString("id")
        if (id.isBlank()) return
        val delivery = JSONObject()
            .put("version", "v1")
            .put("agent_id", owner)
            .put("instance_id", instanceId)
            .put("message_id", id)
            .put("attempt_number", frame.optInt("attempt_number", 1))
        val injectionSent = synchronized(connectionGenerationLock) {
            val capturedLeaseId = leaseId
            if (!deliveryGenerationMatches(
                    callbackGeneration = generation,
                    currentGeneration = connectionGeneration.get(),
                    socketIsCurrent = socket === webSocket,
                    capturedLeaseId = capturedLeaseId,
                    currentLeaseId = leaseId,
                )
            ) return@synchronized false
            webSocket.send(
                delivery
                    .put("lease_id", capturedLeaseId)
                    .put("frame_id", UUID.randomUUID().toString())
                    .put("type", "injection_requested")
                    .toString(),
            )
        }
        if (!injectionSent) return
        val capturedLeaseId = delivery.optString("lease_id")
        synchronized(connectionGenerationLock) {
            if (!shouldSendDeliveryAck(
                    injectionSent = injectionSent,
                    callbackGeneration = generation,
                    currentGeneration = connectionGeneration.get(),
                    socketIsCurrent = socket === webSocket,
                    capturedLeaseId = capturedLeaseId,
                    currentLeaseId = leaseId,
                )
            ) return@synchronized
            webSocket.send(delivery.put("frame_id", UUID.randomUUID().toString()).put("type", "ack").toString())
        }
    }

    private fun historyFor(peer: String): List<ChatMessage> {
        if (peer.isBlank()) return emptyList()
        prefs().edit().remove(legacyRouterHistoryKey(peer)).apply()
        val raw = prefs().getString(routerHistoryKey(peer), null) ?: return emptyList()
        return try {
            val array = JSONArray(raw)
            List(array.length()) { index ->
                val value = array.getJSONObject(index)
                ChatMessage(
                    id = value.optString("id", UUID.randomUUID().toString()),
                    incarnationId = value.optString("message_incarnation_id"),
                    sequence = value.optLong("sequence"),
                    sender = value.optString("sender", peer),
                    body = value.optString("body"),
                    direction = if (value.optString("direction") == Direction.OUTGOING.name) Direction.OUTGOING else Direction.INCOMING,
                    createdAt = value.optString("created_at", Instant.EPOCH.toString()),
                    attachments = imageReferences(value.optJSONArray("attachments"), activeRouter),
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
                incarnationId = "",
                sequence = 0,
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
                .put("message_incarnation_id", message.incarnationId)
                .put("sequence", message.sequence)
                .put("sender", message.sender)
                .put("body", message.body)
                .put("direction", message.direction.name)
                .put("created_at", message.createdAt)
                .put("attachments", JSONArray(message.attachments.map(::imageReferenceJson))))
        }
        return prefs().edit().putString(routerHistoryKey(peer), array.toString()).commit()
    }

    private fun appendMessage(peer: String, message: ChatMessage): Boolean = synchronized(historyLock) {
        val updated = mergeConversationMessages(historyFor(peer), listOf(message))
        val saved = persistKnownPeers(knownPeers() + peer) && persistHistory(peer, updated)
        if (saved && selectedPeer == peer) messages = updated
        saved
    }

    private fun hydrateHistory(peer: String, generation: Int = connectionGeneration.get()) {
        if (peer.isBlank() || activeRouter.isBlank()) return
        val router = activeRouter
        val owner = agentId
        Thread {
            try {
                val url = okhttp3.HttpUrl.Builder()
                    .scheme("http")
                    .host(router.substringBefore(':'))
                    .port(router.substringAfter(':').toInt())
                    .addPathSegments("v1/conversations")
                    .addQueryParameter("owner", owner)
                    .addQueryParameter("peer", peer)
                    .build()
                client.newCall(Request.Builder().url(url).build()).execute().use { response ->
                    if (!response.isSuccessful) return@use
                    val payload = JSONObject(response.body?.string().orEmpty())
                    val snapshot = payload.optJSONArray("messages") ?: JSONArray()
                    val highWaterSequence = payload.optLong("high_water_sequence")
                    val hydrated = List(snapshot.length()) { index -> chatMessage(snapshot.getJSONObject(index), owner, router) }.filterNotNull()
                    runOnUiThread {
                        if (generation != connectionGeneration.get() || activeRouter != router || agentId != owner || selectedPeer != peer) return@runOnUiThread
                        val merged = reconcileConversationMessages(historyFor(peer), hydrated, highWaterSequence)
                        if (persistHistory(peer, merged)) messages = merged
                    }
                }
            } catch (_: Exception) {
                // A later reconnect or peer selection retries hydration without discarding live messages.
            }
        }.start()
    }

    private fun chatMessage(value: JSONObject, owner: String, router: String): ChatMessage? {
        val id = value.optString("id")
        val sender = value.optString("from")
        if (id.isBlank() || sender.isBlank()) return null
        return ChatMessage(
            id = id,
            incarnationId = value.optString("message_incarnation_id"),
            sequence = value.optLong("sequence"),
            sender = sender,
            body = value.optString("body"),
            direction = if (sender == owner) Direction.OUTGOING else Direction.INCOMING,
            createdAt = value.optString("created_at", Instant.EPOCH.toString()),
            attachments = imageReferences(value.optJSONArray("attachments"), router),
        )
    }

    private fun imageReferences(values: JSONArray?, router: String): List<ImageReference> = if (values == null) emptyList() else List(values.length()) { index ->
        val value = values.getJSONObject(index)
        val rawUrl = value.optString("url")
        ImageReference(
            id = value.optString("id"),
            url = if (rawUrl.startsWith("/")) "http://$router$rawUrl" else rawUrl,
            mimeType = value.optString("mime_type"),
            sizeBytes = value.optInt("size_bytes"),
            name = value.optString("name", "Attached image"),
        )
    }

    private fun imageReferenceJson(image: ImageReference) = JSONObject()
        .put("id", image.id)
        .put("url", image.url)
        .put("mime_type", image.mimeType)
        .put("size_bytes", image.sizeBytes)
        .put("name", image.name)

    private fun selectPeer(peer: String) {
        if (peer.isBlank()) return
        closeGoalDialog()
        closeReminderDialog()
        clearModelControlState()
        agentFilter = ""
        selectedPeer = peer
        draft = draftFor(peer)
        images = emptyList()
        messages = historyFor(peer)
        persistKnownPeers(knownPeers() + peer)
        if (leaseId.isNotBlank()) hydrateHistory(peer)
    }

    private fun returnHome() {
        closeGoalDialog()
        closeReminderDialog()
        clearModelControlState()
        selectedPeer = ""
        messages = emptyList()
        images = emptyList()
        draft = ""
    }

    private fun openGoalDialog(peer: String) {
        if (peers.firstOrNull { it.id == peer }?.goal?.enabled == true) goalDialogPeer = peer
    }

    private fun openReminderDialog(peer: String) {
        if (activeRemindersForPeer(reminders, peer).isEmpty()) return
        reminderDialogPeer = peer
        reminderDialogTick++
        val session = ++reminderDialogSession
        reminderDialogTimer?.cancel()
        val timer = Timer()
        reminderDialogTimer = timer
        timer.scheduleAtFixedRate(1_000, 1_000) {
            runOnUiThread {
                if (!reminderDialogCallbackMatchesSession(session, reminderDialogSession)) {
                    timer.cancel()
                    return@runOnUiThread
                }
                reminderDialogTick++
            }
        }
    }

    private fun closeGoalDialog() {
        goalDialogPeer = ""
    }

    private fun closeReminderDialog() {
        reminderDialogSession++
        reminderDialogTimer?.cancel()
        reminderDialogTimer = null
        reminderDialogPeer = ""
        reminderDialogTick++
    }

    private fun addImages(uris: List<Uri>) {
        val router = activeRouter
        val peer = selectedPeer
        if (uris.isEmpty() || router.isBlank() || peer.isBlank()) return
        Thread {
            try {
                val prepared = uris.take(MAX_IMAGE_ATTACHMENTS).map { uri ->
                    prepareImageAttachment(contentResolver, uri, imageDisplayName(uri))
                }
                runOnUiThread {
                    if (activeRouter != router || selectedPeer != peer) return@runOnUiThread
                    val updated = images + prepared.take(MAX_IMAGE_ATTACHMENTS - images.size)
                    if (updated.sumOf { it.bytes.size } > MAX_IMAGE_TOTAL_BYTES) {
                        connection = "Attached images are too large together"
                    } else {
                        images = updated
                    }
                }
            } catch (error: Exception) {
                runOnUiThread {
                    if (activeRouter == router && selectedPeer == peer) {
                        connection = "Image could not be attached"
                    }
                }
            }
        }.start()
    }

    private fun imageDisplayName(uri: Uri): String {
        if (uri.scheme != "content") return uri.lastPathSegment ?: "image"
        return contentResolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME), null, null, null)?.use { cursor ->
            if (cursor.moveToFirst()) cursor.getString(0) else null
        } ?: uri.lastPathSegment ?: "image"
    }

    private fun send() {
        val router = activeRouter
        val recipient = selectedPeer
        val text = normalizeEscapedNewlines(draft)
        if (text != draft) updateDraft(text)
        val sentImages = images
        if (recipient.isBlank() || (text.isBlank() && sentImages.isEmpty())) return
        val messageId = UUID.randomUUID().toString()
        val createdAt = Instant.now().truncatedTo(ChronoUnit.MILLIS).toString()
        val attachments = JSONArray(sentImages.map { image ->
            JSONObject().put("type", "image").put("data_url", image.dataUrl).put("name", image.name)
        })
        val body = JSONObject()
            .put("id", messageId)
            .put("from", agentId)
            .put("to", recipient)
            .put("body", text)
            .put("created_at", createdAt)
            .put("attachments", attachments)
            .toString()
        Thread {
            try {
                client.newCall(
                    Request.Builder().url("http://$router/v1/messages")
                        .post(body.toRequestBody("application/json".toMediaType()))
                        .build(),
                ).execute().use { response ->
                    runOnUiThread {
                        if (response.isSuccessful) {
                            drafts.clearIfUnchanged(router, recipient, text)
                            if (activeRouter == router && selectedPeer == recipient && draft == text) draft = ""
                            if (activeRouter == router && selectedPeer == recipient && images === sentImages) images = emptyList()
                            if (activeRouter == router) refreshPeers()
                        } else if (activeRouter == router) {
                            connection = "Message not sent; please try again"
                        }
                    }
                }
            } catch (_: Exception) {
                runOnUiThread {
                    if (activeRouter == router) connection = "Message not sent; router unavailable"
                }
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

    private fun saveRouter(candidate: String, alias: String): Boolean {
        val previousEndpoint = editingRouterEndpoint
        val endpoint = normalizeRouterEndpointCandidate(candidate)
        if (endpoint == null) {
            routerError = "Enter a hostname with an optional port, for example donuts or donuts:8765"
            return false
        }
        val nextActiveRouter = activeRouterAfterRouterSave(activeRouter, previousEndpoint, endpoint)
        val updated = upsertRouterConfig(routers(), candidate, alias, previousEndpoint).map { it.persist() }.toSet()
        prefs().edit().putStringSet("router_endpoints", updated).putString("active_router", nextActiveRouter).commit()
        editingRouterEndpoint = null
        routerRevision++
        routerError = ""
        showRouterManager = false
        if (nextActiveRouter != activeRouter) {
            activeRouter = nextActiveRouter
            peers = emptyList()
            reminders = emptyList()
            reminderError = ""
            returnHome()
            cancelConnection()
            connect()
        }
        return true
    }

    private fun switchRouter(endpoint: String) {
        if (endpoint == activeRouter) {
            showRouterManager = false
            return
        }
        prefs().edit().putString("active_router", endpoint).commit()
        activeRouter = endpoint
        peers = emptyList()
        reminders = emptyList()
        reminderError = ""
        returnHome()
        showRouterManager = false
        cancelConnection()
        connect()
    }

    private fun removeRouter(endpoint: String) {
        val remaining = routers().filterNot { it.endpoint == endpoint }
        val editor = prefs().edit()
        prefs().all.keys.filter {
            it == knownPeersKeyFor(endpoint) || it.startsWith("history:$endpoint:") || it.startsWith("draft:$endpoint:") || it == draftPeersKey(endpoint) || it.startsWith("conversation_id:$endpoint:")
        }.forEach(editor::remove)
        val next = if (endpoint == activeRouter) remaining.firstOrNull()?.endpoint.orEmpty() else activeRouter
        editor.putStringSet("router_endpoints", remaining.map { it.persist() }.toSet())
        if (next.isBlank()) editor.remove("active_router") else editor.putString("active_router", next)
        editor.commit()
        routerRevision++
        routerError = ""
        if (editingRouterEndpoint == endpoint) editingRouterEndpoint = null
        if (endpoint == activeRouter) {
            activeRouter = next
            peers = emptyList()
            reminders = emptyList()
            reminderError = ""
            returnHome()
            cancelConnection()
            if (next.isNotBlank()) connect() else connection = "Add a router to connect"
        }
    }

    private fun knownPeersKeyFor(router: String) = "known_peers:$router"

    private fun conversationId(peer: String): String {
        val key = "conversation_id:$activeRouter:$peer"
        return prefs().getString(key, null)
            ?: UUID.randomUUID().toString().also { prefs().edit().putString(key, it).commit() }
    }

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

    private fun postInboundNotification(from: String, body: String) {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) return
        val conversationId = conversationId(from)
        val manager = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        createMessageNotificationChannel()
        val intent = Intent(this, MainActivity::class.java)
            .setAction("mecha.conversation.$conversationId")
            .setData(android.net.Uri.parse("mecha://conversation/$conversationId"))
            .putExtra("peer_id", from)
        val pending = PendingIntent.getActivity(this, conversationId.hashCode(), intent, PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
        val notification = android.app.Notification.Builder(this, MESSAGE_NOTIFICATION_CHANNEL)
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
                when {
                    showRouterManager || activeRouter.isBlank() -> RouterManagement(
                        activeRouter = activeRouter,
                        routers = routerRevision.let { routers() },
                        error = routerError,
                        editingEndpoint = editingRouterEndpoint,
                        onAdd = ::saveRouter,
                        onSelect = ::switchRouter,
                        onRemove = ::removeRouter,
                        onEdit = { editingRouterEndpoint = it; routerError = "" },
                        onCancelEdit = { editingRouterEndpoint = null; routerError = "" },
                    )
                    selectedPeer.isBlank() -> PeerHome(
                        connection = connection,
                        routerError = routerError,
                        agentId = agentId,
                        activeRouterDisplayName = routers().firstOrNull { it.endpoint == activeRouter }?.displayName ?: activeRouter,
                        peers = peers,
                        reminders = reminders,
                        reminderError = reminderError,
                        agentFilter = agentFilter,
                        onAgentFilter = { agentFilter = it },
                        onPeer = ::selectPeer,
                        onGoal = ::openGoalDialog,
                        onReminders = ::openReminderDialog,
                        onRefresh = ::refreshPeers,
                        onEditProfile = { showProfileEditor = true },
                        onManageRouters = {
                            closeGoalDialog()
                            closeReminderDialog()
                            clearModelControlState()
                            showRouterManager = true
                        },
                    )
                    else -> {
                        BackHandler(onBack = ::returnHome)
                        Conversation(
                            peerStatus = peers.firstOrNull { it.id == selectedPeer } ?: PeerStatus(selectedPeer),
                            messages = messages,
                            pendingImages = images,
                            draft = draft,
                            onDraft = ::updateDraft,
                            onImages = ::addImages,
                            onRemoveImage = { index -> images = images.filterIndexed { current, _ -> current != index } },
                            onSend = ::send,
                            onBack = ::returnHome,
                            pendingModel = pendingModel.takeIf { pendingModelPeer == selectedPeer },
                            modelControlStatus = modelControlStatus,
                            onSelectModel = ::selectModel,
                        )
                    }
                }
                if (goalDialogPeer.isNotBlank()) {
                    val goal = peers.firstOrNull { it.id == goalDialogPeer }?.goal
                    if (goal?.enabled == true) {
                        GoalDialog(goalDialogPeer, goal.goal, ::closeGoalDialog)
                    }
                }
                if (reminderDialogPeer.isNotBlank()) {
                    ReminderDialog(
                        peer = reminderDialogPeer,
                        reminders = activeRemindersForPeer(reminders, reminderDialogPeer),
                        error = reminderError,
                        tick = reminderDialogTick,
                        onDismiss = ::closeReminderDialog,
                    )
                }
                if (showProfileEditor) UsernameDialog(agentId, ::saveAgentId, { showProfileEditor = false })
            }
        }
    }
}

@Composable
private fun PeerHome(
    connection: String,
    routerError: String,
    agentId: String,
    activeRouterDisplayName: String,
    peers: List<PeerStatus>,
    reminders: List<ReminderSummary>,
    reminderError: String,
    agentFilter: String,
    onAgentFilter: (String) -> Unit,
    onPeer: (String) -> Unit,
    onGoal: (String) -> Unit,
    onReminders: (String) -> Unit,
    onRefresh: () -> Unit,
    onEditProfile: () -> Unit,
    onManageRouters: () -> Unit,
) {
    val filteredPeers = visibleConnectedPeers(peers, agentFilter)
    val displayState = connectionDisplayState(connection, routerError, peers.isNotEmpty())
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
                StatusDot(displayState == ConnectionDisplayState.CONNECTED)
                Spacer(Modifier.width(12.dp))
                Column(Modifier.weight(1f)) {
                    Text(connectionDisplayTitle(displayState, connection, agentId), style = MaterialTheme.typography.titleSmall, fontWeight = FontWeight.SemiBold)
                    Text(
                        connectionDisplayDetail(displayState, connection, routerError, agentId, activeRouterDisplayName),
                        style = MaterialTheme.typography.bodySmall,
                        color = TextMuted,
                    )
                    if (reminderError.isNotBlank()) {
                        Text(reminderError, style = MaterialTheme.typography.bodySmall, color = Color(0xFFFFB4AB))
                    }
                }
            }
        }
        Row(Modifier.fillMaxWidth().padding(top = 28.dp, bottom = 10.dp), verticalAlignment = Alignment.CenterVertically) {
            Text("Connected agents", Modifier.weight(1f), style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.Bold)
            Text(filteredPeers.size.toString(), style = MaterialTheme.typography.bodySmall, color = TextMuted)
        }
        OutlinedTextField(
            value = agentFilter,
            onValueChange = onAgentFilter,
            modifier = Modifier.fillMaxWidth().padding(bottom = 12.dp),
            singleLine = true,
            label = { Text("Filter connected agents") },
        )
        if (peers.isEmpty()) {
            Card(Modifier.fillMaxWidth(), colors = CardDefaults.cardColors(containerColor = SurfaceDark)) {
                Text("No connected agents yet", Modifier.padding(20.dp), color = TextMuted)
            }
        } else if (filteredPeers.isEmpty()) {
            Card(Modifier.fillMaxWidth(), colors = CardDefaults.cardColors(containerColor = SurfaceDark)) {
                Text("No connected agents match", Modifier.padding(20.dp), color = TextMuted)
            }
        } else {
            LazyColumn(verticalArrangement = Arrangement.spacedBy(10.dp)) {
                items(filteredPeers, key = { it.id }) { peer ->
                    PeerRow(
                        peer = peer,
                        reminders = activeRemindersForPeer(reminders, peer.id),
                        onClick = { onPeer(peer.id) },
                        onGoal = { onGoal(peer.id) },
                        onReminders = { onReminders(peer.id) },
                    )
                }
            }
        }
    }
}

@Composable
private fun PeerRow(
    peer: PeerStatus,
    reminders: List<ReminderSummary>,
    onClick: () -> Unit,
    onGoal: () -> Unit,
    onReminders: () -> Unit,
) {
    val peerId = peer.id
    Card(
        modifier = Modifier.fillMaxWidth().clip(RoundedCornerShape(18.dp)),
        colors = CardDefaults.cardColors(containerColor = SurfaceDark),
        shape = RoundedCornerShape(18.dp),
    ) {
        Row(Modifier.fillMaxWidth().padding(14.dp), verticalAlignment = Alignment.CenterVertically) {
            Row(
                Modifier.weight(1f).clickable(onClick = onClick),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Avatar(peerId)
                Spacer(Modifier.width(14.dp))
                Column(Modifier.weight(1f)) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Text(humanName(peerId), modifier = Modifier.weight(1f, fill = false), style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.SemiBold, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        if (peer.compacting) {
                            Spacer(Modifier.width(8.dp))
                            CompactingIndicator(compact = true)
                        } else if (peer.active) {
                            Spacer(Modifier.width(8.dp))
                            WorkingIndicator(compact = true)
                        }
                    }
                    PeerActivityIndicators(peer)
                }
            }
            if (peer.goal?.enabled == true) {
                IconButton(onClick = onGoal, modifier = Modifier.size(38.dp)) {
                    Icon(Icons.Default.Flag, "Show goal", tint = Color(0xFFF5B2D1))
                }
            }
            if (reminders.isNotEmpty()) {
                IconButton(onClick = onReminders, modifier = Modifier.size(38.dp)) {
                    Icon(Icons.Default.AccessTime, "Show reminders", tint = Teal)
                }
            }
            Spacer(Modifier.width(4.dp))
            IconButton(onClick = onClick, modifier = Modifier.size(38.dp)) {
                Icon(Icons.Default.ChevronRight, "Open conversation", tint = TextMuted)
            }
        }
    }
}

@Composable
private fun PeerActivityIndicators(peer: PeerStatus, compact: Boolean = false) {
    if (peer.subagentCount <= 0) return
    Column(Modifier.padding(top = 5.dp), verticalArrangement = Arrangement.spacedBy(5.dp)) {
        SubagentIndicator(peer.subagentCount, peer.subagents, compact)
    }
}

@Composable
private fun SubagentIndicator(count: Int, subagents: List<SubagentSummary>, compact: Boolean = false) {
    var expanded by remember(count, subagents) { mutableStateOf(false) }
    val canExpand = subagents.isNotEmpty()
    val pulse = synchronizedHaloPulse("subagent-pulse")
    val label = "$count subagent${if (count == 1) "" else "s"}"
    val badgeShape = RoundedCornerShape(if (expanded) 12.dp else 999.dp)
    Column(
        Modifier
            .background(Color(0xFF266AFF).copy(alpha = 0.26f), badgeShape)
            .border(1.dp, Color(0xFF60A5FA).copy(alpha = 0.34f), badgeShape)
            .then(if (canExpand) Modifier.clickable { expanded = !expanded } else Modifier)
            .padding(horizontal = if (expanded) 10.dp else 7.dp, vertical = if (expanded) 8.dp else 4.dp),
        verticalArrangement = Arrangement.spacedBy(6.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            PulsingDot(Color(0xFF60A5FA), pulse, if (compact) 6.dp else 7.dp, 8.dp)
            Spacer(Modifier.width(6.dp))
            Text(label, style = MaterialTheme.typography.labelSmall, color = Color(0xFFDBE8FF), fontWeight = FontWeight.Bold, maxLines = 1, overflow = TextOverflow.Ellipsis)
            if (canExpand) {
                Spacer(Modifier.width(5.dp))
                Text(if (expanded) "▴" else "▾", style = MaterialTheme.typography.labelSmall, color = Color(0xFFDBE8FF))
            }
        }
        if (expanded) {
            subagents.forEach { subagent ->
                Row(Modifier.padding(start = 13.dp), verticalAlignment = Alignment.CenterVertically) {
                    PulsingDot(Color(0xFF60A5FA), pulse, 7.dp, 8.dp)
                    Spacer(Modifier.width(7.dp))
                    Text(subagent.description, style = MaterialTheme.typography.labelSmall, color = Color(0xFFDBE8FF), maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
            }
        }
    }
}

@Composable
private fun CompactingIndicator(compact: Boolean = false) {
    if (compact) {
        Box(Modifier.size(26.dp), contentAlignment = Alignment.Center) {
            PulsingDot(Periwinkle, synchronizedHaloPulse("compacting-pulse"), 7.dp, 10.dp)
        }
        return
    }
    Row(
        Modifier.background(Periwinkle.copy(alpha = 0.18f), RoundedCornerShape(999.dp)).padding(horizontal = 9.dp, vertical = 5.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        PulsingDot(Periwinkle, synchronizedHaloPulse("compacting-pulse"), 8.dp, 10.dp)
        Spacer(Modifier.width(6.dp))
        Text("Mailbox is compacting; delivery is queued.", style = MaterialTheme.typography.labelSmall, color = TextPrimary, fontWeight = FontWeight.Bold)
    }
}

@Composable
private fun WorkingIndicator(compact: Boolean = false) {
    val pulse = synchronizedHaloPulse("working-pulse")
    if (compact) {
        Box(Modifier.size(26.dp), contentAlignment = Alignment.Center) {
            PulsingDot(Teal, pulse, 7.dp, 10.dp)
        }
        return
    }
    Row(
        Modifier.background(Teal.copy(alpha = 0.18f), RoundedCornerShape(999.dp)).padding(horizontal = 9.dp, vertical = 5.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        PulsingDot(Teal, pulse, 8.dp, 10.dp)
        Spacer(Modifier.width(6.dp))
        Text("Working", style = MaterialTheme.typography.labelSmall, color = TextPrimary, fontWeight = FontWeight.Bold)
    }
}

@Composable
private fun synchronizedHaloPulse(label: String): HaloPulse {
    val transition = rememberInfiniteTransition(label = label)
    val progress by transition.animateFloat(
        initialValue = 0f,
        targetValue = 1f,
        animationSpec = infiniteRepeatable(animation = tween(1_200, easing = LinearOutSlowInEasing)),
        label = "$label-progress",
    )
    return HaloPulse(progress)
}

@Composable
private fun PulsingDot(color: Color, pulse: HaloPulse, size: Dp, haloSpread: Dp) {
    val haloAlpha = (1f - pulse.progress) * 0.58f
    val haloScale = 1f + pulse.progress * ((haloSpread.value * 2f) / size.value)
    Box(Modifier.size(size + haloSpread * 2), contentAlignment = Alignment.Center) {
        Box(
            Modifier
                .size(size)
                .graphicsLayer {
                    scaleX = haloScale
                    scaleY = haloScale
                    alpha = haloAlpha
                }
                .clip(CircleShape)
                .background(color),
        )
        Box(Modifier.size(size).clip(CircleShape).background(color))
    }
}

@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun Conversation(
    peerStatus: PeerStatus,
    messages: List<ChatMessage>,
    pendingImages: List<PendingImage>,
    draft: String,
    onDraft: (String) -> Unit,
    onImages: (List<Uri>) -> Unit,
    onRemoveImage: (Int) -> Unit,
    onSend: () -> Unit,
    onBack: () -> Unit,
    pendingModel: ModelSummary?,
    modelControlStatus: String,
    onSelectModel: (ModelSummary) -> Unit,
) {
    val peer = peerStatus.id
    val transcriptState = rememberLazyListState()
    val imagePicker = rememberLauncherForActivityResult(ActivityResultContracts.GetMultipleContents()) { uris ->
        if (uris.isNotEmpty()) onImages(uris)
    }
    val receiveImages = remember(onImages) {
        object : ReceiveContentListener {
            override fun onReceive(transferableContent: TransferableContent): TransferableContent? {
                val uris = imageUris(transferableContent.clipEntry.clipData)
                if (uris.isNotEmpty()) onImages(uris)
                return transferableContent.consume { item ->
                    acceptedImageUri(item.uri?.toString(), item.intent?.data?.toString(), item.text?.toString()) != null
                }
            }
        }
    }
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
            Column(Modifier.weight(1f)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(humanName(peer), modifier = Modifier.weight(1f, fill = false), style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.Bold, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    if (peerStatus.compacting) {
                        Spacer(Modifier.width(8.dp))
                        CompactingIndicator(compact = false)
                    } else if (peerStatus.active) {
                        Spacer(Modifier.width(8.dp))
                        WorkingIndicator(compact = true)
                    }
                }
                PeerActivityIndicators(peerStatus, compact = true)
            }
        }
        HorizontalDivider(color = SurfaceRaised)
        if (peerStatus.availableModels.isNotEmpty()) {
            ModelPicker(
                models = peerStatus.availableModels,
                currentModel = peerStatus.currentModel,
                pendingModel = pendingModel,
                catalogTotalCount = peerStatus.modelCatalogTotalCount,
                catalogTruncated = peerStatus.modelCatalogTruncated,
                status = modelControlStatus,
                onSelect = onSelectModel,
            )
            HorizontalDivider(color = SurfaceRaised)
        }
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
                items(messages, key = { it.key }) { message -> MessageBubble(message) }
            }
        }
        HorizontalDivider(color = SurfaceRaised)
        Column(Modifier.fillMaxWidth().background(SurfaceDark).padding(12.dp)) {
            if (pendingImages.isNotEmpty()) {
                Row(Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    pendingImages.forEachIndexed { index, image -> PendingImagePreview(image) { onRemoveImage(index) } }
                }
                Spacer(Modifier.height(8.dp))
            }
            Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                IconButton(
                    onClick = { imagePicker.launch("image/*") },
                    enabled = pendingImages.size < MAX_IMAGE_ATTACHMENTS,
                    modifier = Modifier.size(56.dp),
                ) { Icon(PaperclipIcon, "Attach images", tint = Periwinkle) }
                OutlinedTextField(
                    value = draft,
                    onValueChange = onDraft,
                    modifier = Modifier.weight(1f).heightIn(min = 56.dp).contentReceiver(receiveImages),
                    singleLine = false,
                    maxLines = 5,
                    placeholder = { Text("Message ${humanName(peer)}") },
                    shape = RoundedCornerShape(16.dp),
                )
                Spacer(Modifier.width(10.dp))
                Button(
                    onClick = onSend,
                    enabled = draft.isNotBlank() || pendingImages.isNotEmpty(),
                    modifier = Modifier.height(56.dp),
                    shape = RoundedCornerShape(16.dp),
                    colors = ButtonDefaults.buttonColors(containerColor = Periwinkle, contentColor = Ink),
                ) { Text("Send", fontWeight = FontWeight.Bold) }
            }
        }
    }
}

@Composable
private fun ModelPicker(
    models: List<ModelSummary>,
    currentModel: ModelSummary?,
    pendingModel: ModelSummary?,
    catalogTotalCount: Int,
    catalogTruncated: Boolean,
    status: String,
    onSelect: (ModelSummary) -> Unit,
) {
    var expanded by remember { mutableStateOf(false) }
    var search by remember { mutableStateOf("") }
    val selected = pendingModel ?: currentModel
    val visibleModels = models.filter { model -> modelMatchesSearch(model, search) }
    Column(
        Modifier.fillMaxWidth().background(SurfaceDark).padding(horizontal = 16.dp, vertical = 10.dp),
        verticalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text("Model", style = MaterialTheme.typography.labelMedium, color = TextMuted)
            Spacer(Modifier.width(10.dp))
            TextButton(onClick = { expanded = !expanded }) {
                Text(selected?.let(::modelKey) ?: "Select model", maxLines = 1, overflow = TextOverflow.Ellipsis)
                Spacer(Modifier.width(4.dp))
                Text(if (expanded) "▴" else "▾", color = TextMuted)
            }
        }
        if (pendingModel != null) {
            Text("Pending: ${modelKey(pendingModel)}", style = MaterialTheme.typography.bodySmall, color = Periwinkle)
        }
        if (status.isNotBlank()) {
            Text(status, style = MaterialTheme.typography.bodySmall, color = if (status == "Model changed") Teal else Color(0xFFFFB4AB))
        }
        if (catalogTruncated) {
            Text("Showing ${models.size} of ${maxOf(models.size, catalogTotalCount)} models.", style = MaterialTheme.typography.bodySmall, color = TextMuted)
        }
        if (expanded) {
            OutlinedTextField(
                value = search,
                onValueChange = { search = it },
                modifier = Modifier.fillMaxWidth(),
                singleLine = true,
                label = { Text("Search models") },
            )
            if (visibleModels.isEmpty()) {
                Text("No matching models", color = TextMuted, modifier = Modifier.padding(vertical = 8.dp))
            } else {
                LazyColumn(
                    modifier = Modifier.fillMaxWidth().heightIn(max = 260.dp),
                    verticalArrangement = Arrangement.spacedBy(4.dp),
                ) {
                    items(visibleModels, key = { modelKey(it) }) { model ->
                        TextButton(
                            onClick = {
                                onSelect(model)
                                expanded = false
                                search = ""
                            },
                            modifier = Modifier.fillMaxWidth(),
                        ) {
                            Column(Modifier.fillMaxWidth()) {
                                Text(modelDisplayLabel(model), maxLines = 1, overflow = TextOverflow.Ellipsis)
                                Text(modelKey(model), style = MaterialTheme.typography.bodySmall, color = TextMuted)
                            }
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun GoalDialog(peer: String, goal: String, onDismiss: () -> Unit) {
    AlertDialog(
        onDismissRequest = onDismiss,
        containerColor = SurfaceDark,
        titleContentColor = TextPrimary,
        textContentColor = TextPrimary,
        title = { Text("Goal for ${humanName(peer)}") },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Text("Current goal", color = TextMuted)
                Text(goal)
            }
        },
        confirmButton = { TextButton(onDismiss) { Text("Close") } },
    )
}

@Composable
private fun ReminderDialog(
    peer: String,
    reminders: List<ReminderSummary>,
    error: String,
    tick: Int,
    onDismiss: () -> Unit,
) {
    // Reading tick keeps countdown text recomposed once per second while this dialog is open.
    tick
    AlertDialog(
        onDismissRequest = onDismiss,
        containerColor = SurfaceDark,
        titleContentColor = TextPrimary,
        textContentColor = TextPrimary,
        title = { Text("Reminders for ${humanName(peer)}") },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
                if (error.isNotBlank()) Text(error, color = Color(0xFFFFB4AB))
                if (reminders.isEmpty()) {
                    Text("No active reminders", color = TextMuted)
                } else {
                    LazyColumn(
                        modifier = Modifier.fillMaxWidth().heightIn(max = 360.dp),
                        verticalArrangement = Arrangement.spacedBy(12.dp),
                    ) {
                        items(reminders, key = { it.id }) { reminder ->
                            Column(verticalArrangement = Arrangement.spacedBy(3.dp)) {
                                Text(reminder.subject.ifBlank { "Reminder" }, fontWeight = FontWeight.SemiBold)
                                if (reminder.body.isNotBlank()) Text(reminder.body)
                                Text(formatReminderCountdown(reminder.nextDueAt), style = MaterialTheme.typography.bodySmall, color = Teal)
                                Text(reminderRecurrenceLabel(reminder.intervalMs), style = MaterialTheme.typography.bodySmall, color = TextMuted)
                            }
                        }
                    }
                }
            }
        },
        confirmButton = { TextButton(onDismiss) { Text("Close") } },
    )
}

@Composable
private fun MessageBubble(message: ChatMessage) {
    val outgoing = message.direction == Direction.OUTGOING
    Column(Modifier.fillMaxWidth(), horizontalAlignment = if (outgoing) Alignment.End else Alignment.Start) {
        Row(
            modifier = Modifier.padding(horizontal = 4.dp, vertical = 2.dp),
            horizontalArrangement = Arrangement.spacedBy(8.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text(
                if (outgoing) "You" else humanName(message.sender),
                style = MaterialTheme.typography.labelSmall,
                color = if (outgoing) Periwinkle else Teal,
            )
            Text(
                formatMessageTimestamp(message.createdAt),
                style = MaterialTheme.typography.labelSmall,
                color = TextMuted,
            )
        }
        Card(
            modifier = Modifier.fillMaxWidth(0.86f),
            shape = RoundedCornerShape(if (outgoing) 18.dp else 18.dp),
            colors = CardDefaults.cardColors(containerColor = if (outgoing) Periwinkle else SurfaceRaised),
            elevation = CardDefaults.cardElevation(defaultElevation = if (outgoing) 0.dp else 3.dp),
        ) {
            Column(Modifier.padding(14.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                if (message.body.isNotBlank()) {
                    Text(message.body, color = if (outgoing) Ink else TextPrimary, style = MaterialTheme.typography.bodyLarge)
                }
                message.attachments.forEach { attachment -> RemoteImage(attachment) }
            }
        }
    }
}

@Composable
private fun PendingImagePreview(image: PendingImage, onRemove: () -> Unit) {
    val bitmap = remember(image) { BitmapFactory.decodeByteArray(image.bytes, 0, image.bytes.size)?.asImageBitmap() }
    Column(horizontalAlignment = Alignment.CenterHorizontally) {
        if (bitmap != null) {
            Image(
                bitmap = bitmap,
                contentDescription = image.name,
                modifier = Modifier.size(88.dp).clip(RoundedCornerShape(12.dp)),
                contentScale = ContentScale.Crop,
            )
        }
        TextButton(onRemove) { Text("Remove") }
    }
}

@Composable
private fun RemoteImage(attachment: ImageReference) {
    var bytes by remember(attachment.url) { mutableStateOf<ByteArray?>(null) }
    LaunchedEffect(attachment.url) {
        bytes = withContext(Dispatchers.IO) { loadBoundedImage(attachment.url) }
    }
    val bitmap = remember(bytes) { bytes?.let(::decodeBoundedBitmap)?.asImageBitmap() }
    if (bitmap != null) {
        Image(
            bitmap = bitmap!!,
            contentDescription = attachment.name,
            modifier = Modifier.fillMaxWidth().heightIn(max = 320.dp).clip(RoundedCornerShape(12.dp)),
            contentScale = ContentScale.Fit,
        )
    }
}

private fun decodeBoundedBitmap(bytes: ByteArray): android.graphics.Bitmap? {
    val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
    BitmapFactory.decodeByteArray(bytes, 0, bytes.size, bounds)
    if (bounds.outWidth < 1 || bounds.outHeight < 1 || bounds.outWidth.toLong() * bounds.outHeight > 40_000_000L) return null
    var sample = 1
    while (maxOf(bounds.outWidth, bounds.outHeight) / sample > MAX_IMAGE_EDGE) sample *= 2
    return BitmapFactory.decodeByteArray(bytes, 0, bytes.size, BitmapFactory.Options().apply { inSampleSize = sample })
}

private fun imageUris(clip: ClipData): List<Uri> = (0 until clip.itemCount).mapNotNull { index ->
    val item = clip.getItemAt(index)
    acceptedImageUri(item.uri?.toString(), item.intent?.data?.toString(), item.text?.toString())?.let(Uri::parse)
}

private fun loadBoundedImage(url: String): ByteArray? = try {
    val connection = URL(url).openConnection().apply {
        connectTimeout = 5_000
        readTimeout = 5_000
    }
    connection.getInputStream().use { input ->
        val output = ByteArrayOutputStream()
        val buffer = ByteArray(16 * 1024)
        var total = 0
        while (true) {
            val count = input.read(buffer)
            if (count < 0) break
            total += count
            if (total > MAX_IMAGE_BYTES) return null
            output.write(buffer, 0, count)
        }
        output.toByteArray()
    }
} catch (_: Exception) {
    null
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
    editingEndpoint: String?,
    onAdd: (String, String) -> Boolean,
    onSelect: (String) -> Unit,
    onRemove: (String) -> Unit,
    onEdit: (String) -> Unit,
    onCancelEdit: () -> Unit,
) {
    val editingRouter = routers.firstOrNull { it.endpoint == editingEndpoint }
    var endpoint by remember(editingEndpoint) { mutableStateOf(editingRouter?.endpoint.orEmpty()) }
    var alias by remember(editingEndpoint) { mutableStateOf(editingRouter?.alias.orEmpty()) }
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
        OutlinedTextField(
            value = alias,
            onValueChange = { alias = it },
            modifier = Modifier.fillMaxWidth().padding(top = 10.dp),
            singleLine = true,
            label = { Text("Alias") },
            placeholder = { Text("Home lab") },
        )
        if (error.isNotBlank()) Text(error, Modifier.padding(top = 6.dp), style = MaterialTheme.typography.bodySmall, color = Color(0xFFFF8A80))
        Button(
            onClick = { if (onAdd(endpoint, alias)) { endpoint = ""; alias = "" } },
            enabled = endpoint.isNotBlank(),
            modifier = Modifier.fillMaxWidth().padding(top = 10.dp).height(52.dp),
            shape = RoundedCornerShape(16.dp),
            colors = ButtonDefaults.buttonColors(containerColor = Periwinkle, contentColor = Ink),
        ) { Text(if (editingEndpoint == null) "Add router" else "Save router", fontWeight = FontWeight.Bold) }
        if (editingEndpoint != null) TextButton(onCancelEdit, Modifier.fillMaxWidth()) { Text("Cancel edit") }
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
                                Text(router.displayName, style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.SemiBold)
                                if (router.alias.isNotBlank()) Text(router.endpoint, style = MaterialTheme.typography.bodySmall, color = TextMuted)
                                if (router.endpoint == activeRouter) Text("Active", style = MaterialTheme.typography.bodySmall, color = Teal)
                            }
                            IconButton({ onEdit(router.endpoint) }) {
                                Icon(Icons.Default.Edit, "Edit router", tint = Periwinkle)
                            }
                            IconButton({ onRemove(router.endpoint) }) {
                                Icon(Icons.Default.Delete, "Remove router", tint = Color(0xFFFFA4A0))
                            }
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

internal enum class ConnectionDisplayState { DISCONNECTED, CONNECTING, CONNECTED, RECONNECTING, ERROR }

internal fun connectionDisplayState(connection: String, routerError: String = "", hasPeers: Boolean = false): ConnectionDisplayState {
    if (connection == "Add a router to connect" || connection.isBlank()) return ConnectionDisplayState.DISCONNECTED
    if (connection.startsWith("Connected as")) return ConnectionDisplayState.CONNECTED
    if (connection.startsWith("Connecting to")) return ConnectionDisplayState.CONNECTING
    if (connection.startsWith("Router connection closed") || connection.startsWith("Router unavailable") || connection.startsWith("Connection stale")) return ConnectionDisplayState.RECONNECTING
    if (routerError.isNotBlank() || connection.isNotBlank()) return ConnectionDisplayState.ERROR
    return ConnectionDisplayState.ERROR
}

internal fun connectionDisplayTitle(state: ConnectionDisplayState, connection: String, agentId: String): String = when (state) {
    ConnectionDisplayState.DISCONNECTED -> "Disconnected"
    ConnectionDisplayState.CONNECTING -> "Connecting"
    ConnectionDisplayState.CONNECTED -> "Connected as $agentId"
    ConnectionDisplayState.RECONNECTING -> "Reconnecting"
    ConnectionDisplayState.ERROR -> "Error"
}

internal fun connectionDisplayDetail(
    state: ConnectionDisplayState,
    connection: String,
    routerError: String,
    agentId: String,
    routerDisplayName: String,
): String = when (state) {
    ConnectionDisplayState.DISCONNECTED -> "Open routers to connect."
    ConnectionDisplayState.CONNECTING -> "Connecting to your agents… · $routerDisplayName"
    ConnectionDisplayState.CONNECTED -> ""
    ConnectionDisplayState.RECONNECTING,
    ConnectionDisplayState.ERROR -> "${routerError.ifBlank { connection }} · $routerDisplayName"
}

internal fun modelKey(model: ModelSummary): String = "${model.provider}/${model.id}"

internal fun modelDisplayLabel(model: ModelSummary): String =
    model.name.ifBlank { modelKey(model) }

internal fun modelMatchesSearch(model: ModelSummary, search: String): Boolean {
    val normalizedSearch = search.trim().lowercase()
    return normalizedSearch.isBlank() || "${modelKey(model)} ${modelDisplayLabel(model)}".lowercase().contains(normalizedSearch)
}

internal fun reconcileConversationMessages(
    existing: List<ChatMessage>,
    snapshot: List<ChatMessage>,
    highWaterSequence: Long,
): List<ChatMessage> {
    val snapshotKeys = snapshot.map(ChatMessage::key).toSet()
    val retainedLive = existing.filter { message ->
        message.sequence > highWaterSequence || message.key in snapshotKeys
    }
    return mergeConversationMessages(retainedLive, snapshot)
}

internal fun mergeConversationMessages(existing: List<ChatMessage>, incoming: List<ChatMessage>): List<ChatMessage> {
    val merged = existing.toMutableList()
    incoming.forEach { message ->
        val index = merged.indexOfFirst { current ->
            current.id == message.id && (current.incarnationId == message.incarnationId || current.incarnationId.isBlank() || message.incarnationId.isBlank())
        }
        if (index < 0) merged += message else merged[index] = message
    }
    return merged.sortedWith(compareBy<ChatMessage> { if (it.sequence == 0L) Long.MAX_VALUE else it.sequence }.thenBy { it.createdAt }).takeLast(200)
}

internal fun normalizeRouterEndpointCandidate(candidate: String): String? {
    val match = ROUTER_ENDPOINT.matchEntire(candidate.trim()) ?: return null
    val host = match.groupValues[1].lowercase()
    val port = match.groupValues[2].ifBlank { DEFAULT_ROUTER_PORT.toString() }.toIntOrNull() ?: return null
    return "$host:$port".takeIf { port in 1..65535 }
}

internal fun parseRouterConfig(stored: String): RouterConfig? {
    val parts = stored.split(ROUTER_RECORD_SEPARATOR, limit = 2)
    if (parts.size == 2) {
        val endpoint = normalizeRouterEndpointCandidate(parts[1]) ?: return null
        return RouterConfig(parts[0].trim(), endpoint)
    }
    val endpoint = normalizeRouterEndpointCandidate(stored) ?: return null
    return RouterConfig("", endpoint)
}

internal fun upsertRouterConfig(
    routers: List<RouterConfig>,
    candidate: String,
    alias: String,
    previousEndpoint: String? = null,
): List<RouterConfig> {
    val endpoint = normalizeRouterEndpointCandidate(candidate) ?: return routers
    val trimmedAlias = alias.trim()
    return (routers.filterNot { it.endpoint == endpoint || it.endpoint == previousEndpoint } + RouterConfig(trimmedAlias, endpoint))
        .distinctBy { it.endpoint }
}

internal fun activeRouterAfterRouterSave(activeRouter: String, previousEndpoint: String?, savedEndpoint: String): String =
    if (previousEndpoint == null || previousEndpoint == activeRouter) savedEndpoint else activeRouter

private fun draftPeersKey(router: String) = "draft_peers:$router"

internal fun draftPreferenceKey(router: String, peer: String) = "draft:$router:$peer"

internal fun normalizeEscapedNewlines(value: String): String = value
    .replace("\\r\\n", "\n")
    .replace("\\n", "\n")
    .replace("\\r", "\n")

internal fun boundedDraftPeers(existing: List<String>, peer: String): List<String> =
    (existing.filterNot { it == peer } + peer).takeLast(MAX_DRAFTS_PER_ROUTER)

internal fun visibleConnectedPeers(peers: List<PeerStatus>, filter: String): List<PeerStatus> {
    val normalizedFilter = filter.trim().lowercase()
    return peers.filter { peer ->
        val descriptions = peer.subagents.joinToString(" ") { it.description }
        normalizedFilter.isBlank() || "${peer.id} ${humanName(peer.id)} $descriptions".lowercase().contains(normalizedFilter)
    }
}

internal fun normalizedSubagentSummaries(subagents: JSONArray?): List<SubagentSummary> {
    if (subagents == null) return emptyList()
    return buildList {
        for (index in 0 until subagents.length()) {
            val subagent = subagents.optJSONObject(index) ?: continue
            val id = subagent.optString("id").trim().take(128)
            val description = subagent.optString("description").trim().take(512)
            if (id.isBlank() || description.isBlank()) continue
            val type = subagent.optString("type").trim().take(64)
            add(SubagentSummary(id, description, type))
        }
    }.take(999)
}

internal fun normalizeGoalProjection(value: JSONObject?): GoalProjection? {
    if (value == null) return null
    val enabled = value.opt("enabled") as? Boolean ?: return null
    val goal = value.opt("goal") as? String ?: return null
    if (goal.length > 4_000) return null
    if (enabled && goal.isEmpty()) return null
    if (!enabled && goal.isNotEmpty()) return null
    return GoalProjection(enabled, if (enabled) goal else "")
}

internal fun normalizeModelSummary(value: JSONObject?): ModelSummary? {
    if (value == null) return null
    val provider = (value.opt("provider") as? String)?.trim().orEmpty()
    val id = (value.opt("id") as? String)?.trim().orEmpty()
    val name = (value.opt("name") as? String)?.trim().orEmpty()
    if (provider.isEmpty() || provider.codePointCount(0, provider.length) > 128) return null
    if (id.isEmpty() || id.codePointCount(0, id.length) > 256) return null
    if (name.codePointCount(0, name.length) > 256) return null
    return ModelSummary(provider, id, name)
}

internal fun normalizedModelSummaries(values: JSONArray?): List<ModelSummary> {
    if (values == null) return emptyList()
    val seen = mutableSetOf<String>()
    return buildList {
        for (index in 0 until values.length()) {
            val model = normalizeModelSummary(values.optJSONObject(index)) ?: continue
            if (seen.add("${model.provider}\u0000${model.id}")) add(model)
        }
    }.take(1_000)
}

private fun normalizedModelSummariesOrNull(values: JSONArray): List<ModelSummary>? {
    val summaries = normalizedModelSummaries(values)
    return if (values.length() > 0 && summaries.isEmpty()) null else summaries
}

private val modelCatalogFrameFields = listOf(
    "available_models",
    "model_summaries",
    "model_catalog_total_count",
    "total_count",
    "model_catalog_truncated",
    "truncated",
)

internal fun mergeDeferredModelCatalogFrame(source: JSONObject, deferred: JSONObject): JSONObject {
    val merged = JSONObject(source.toString())
    modelCatalogFrameFields.forEach { field ->
        if (deferred.has(field)) merged.put(field, deferred.opt(field)) else merged.remove(field)
    }
    return merged
}

private fun modelCatalogValuesFromFrame(frame: JSONObject): List<ModelSummary>? {
    val available = if (frame.has("available_models")) frame.optJSONArray("available_models") else null
    val summaries = if (frame.has("model_summaries")) frame.optJSONArray("model_summaries") else null
    if (frame.has("available_models") && available == null) return null
    if (frame.has("model_summaries") && summaries == null) return null
    val availableValues = available?.let(::normalizedModelSummariesOrNull)
    val summaryValues = summaries?.let(::normalizedModelSummariesOrNull)
    if ((available != null && availableValues == null) || (summaries != null && summaryValues == null)) return null
    return when {
        !availableValues.isNullOrEmpty() -> availableValues
        !summaryValues.isNullOrEmpty() -> summaryValues
        available != null || summaries != null -> emptyList()
        else -> null
    }
}

internal fun shouldApplyModelCatalogFrame(frame: JSONObject): Boolean =
    modelCatalogValuesFromFrame(frame) != null

internal fun modelCatalogUpdateMatchesPending(
    peer: String,
    frame: JSONObject,
    pendingControlId: String,
    pendingControlPeer: String,
    pendingControlInstanceId: String,
): Boolean {
    val frameInstanceId = frame.optString("instance_id").trim()
    return pendingControlId.isNotBlank() &&
        peer.isNotBlank() &&
        pendingControlPeer.isNotBlank() &&
        pendingControlInstanceId.isNotBlank() &&
        frameInstanceId.isNotBlank() &&
        peer == pendingControlPeer &&
        frameInstanceId == pendingControlInstanceId
}

internal fun modelCatalogProjection(
    peer: String,
    current: PeerStatus,
    frame: JSONObject,
    pendingControlId: String = "",
    pendingControlPeer: String = "",
    pendingControlInstanceId: String = "",
): PeerStatus? {
    val values = modelCatalogValuesFromFrame(frame) ?: return null
    if (values.isEmpty() && modelCatalogUpdateMatchesPending(
            peer,
            frame,
            pendingControlId,
            pendingControlPeer,
            pendingControlInstanceId,
        )
    ) return null
    return peerStatusFromJson(peer, frame, current)
}

internal fun remindersFromJson(values: JSONArray?): List<ReminderSummary> {
    if (values == null) return emptyList()
    return buildList {
        for (index in 0 until values.length()) {
            val value = values.optJSONObject(index) ?: continue
            val id = (value.opt("id") as? String).orEmpty()
            val target = (value.opt("target") as? String).orEmpty()
            if (id.isBlank() || target.isBlank()) continue
            val intervalMs = when (val interval = value.opt("interval_ms")) {
                is Number -> interval.toLong()
                else -> null
            }
            add(
                ReminderSummary(
                    id = id,
                    target = target,
                    subject = (value.opt("subject") as? String).orEmpty(),
                    body = (value.opt("body") as? String).orEmpty(),
                    nextDueAt = (value.opt("next_due_at") as? String).orEmpty(),
                    intervalMs = intervalMs,
                    state = (value.opt("state") as? String).orEmpty(),
                ),
            )
        }
    }.take(1_000)
}

internal fun reminderDialogCallbackMatchesSession(callbackSession: Long, currentSession: Long): Boolean =
    callbackSession == currentSession

internal fun activeRemindersForPeer(reminders: List<ReminderSummary>, peer: String): List<ReminderSummary> =
    reminders.filter { it.target == peer && it.state in setOf("scheduled", "due", "message_queued") }

internal fun reminderRefreshResult(
    current: List<ReminderSummary>,
    responseSuccessful: Boolean,
    payload: JSONArray?,
): List<ReminderSummary> = if (responseSuccessful && payload != null) remindersFromJson(payload) else current

internal fun reminderRefreshError(responseSuccessful: Boolean, payload: JSONArray?): String =
    if (responseSuccessful && payload == null) "Reminder refresh failed" else ""

internal fun formatReminderCountdown(nextDueAt: String, now: Instant = Instant.now()): String {
    val dueAt = runCatching { Instant.parse(nextDueAt) }.getOrNull() ?: return "Due time unavailable"
    val remainingMillis = try {
        java.time.Duration.between(now, dueAt).toMillis()
    } catch (_: DateTimeException) {
        return "Due time unavailable"
    } catch (_: ArithmeticException) {
        return "Due time unavailable"
    }
    val remainingSeconds = if (remainingMillis <= 0) 0 else (remainingMillis + 999) / 1_000
    if (remainingSeconds == 0L) return "Due now"

    var seconds = remainingSeconds
    val days = seconds / (24 * 60 * 60)
    seconds %= 24 * 60 * 60
    val hours = seconds / (60 * 60)
    seconds %= 60 * 60
    val minutes = seconds / 60
    seconds %= 60
    return buildList {
        if (days > 0) add("${days}d")
        if (hours > 0) add("${hours}h")
        if (minutes > 0) add("${minutes}m")
        if (seconds > 0) add("${seconds}s")
    }.joinToString(" ").let { "Due in $it" }
}

internal fun reminderRecurrenceLabel(intervalMs: Long?): String =
    if (intervalMs != null && intervalMs > 0) "Recurring" else "One-shot"

internal fun modelControlRequestMatchesPending(pendingRequestId: String, requestId: String): Boolean =
    pendingRequestId.isNotBlank() && requestId.isNotBlank() && pendingRequestId == requestId

internal fun modelControlConfirmed(accepted: Boolean?, currentModel: ModelSummary?): Boolean =
    accepted == true && currentModel != null

internal fun modelControlResultTargetsCurrentPeer(
    peers: List<PeerStatus>,
    pendingPeer: String,
    pendingInstanceId: String,
): Boolean = peers.firstOrNull { it.id == pendingPeer }?.instanceId == pendingInstanceId

internal data class ModelControlSettlement(
    val accepted: Boolean,
    val errorCode: String?,
    val currentModel: ModelSummary?,
)

internal fun modelControlResultSettlement(
    frame: JSONObject,
    pendingControlId: String,
    pendingPeer: String,
    sourceAgentId: String,
    pendingInstanceId: String,
    currentPendingPeer: PeerStatus?,
): ModelControlSettlement? {
    if (!modelControlResultMatchesPending(frame, pendingControlId, pendingPeer, sourceAgentId, pendingInstanceId)) return null
    if (!modelControlResultTargetsCurrentPeer(listOfNotNull(currentPendingPeer), pendingPeer, pendingInstanceId)) return null
    val accepted = frame.opt("accepted") as? Boolean ?: return null
    return ModelControlSettlement(
        accepted = accepted,
        errorCode = frame.optString("error_code").takeIf { it.isNotBlank() },
        currentModel = normalizeModelSummary(frame.optJSONObject("current_model")),
    )
}

internal fun modelControlResultMatchesPending(
    frame: JSONObject,
    pendingControlId: String,
    pendingPeer: String,
    sourceAgentId: String,
    pendingInstanceId: String,
): Boolean {
    val controlId = frame.optString("control_id")
    val peer = frame.optString("agent_id")
    val source = frame.optString("source_agent_id")
    val instanceId = frame.optString("instance_id")
    return pendingControlId.isNotBlank() &&
        pendingPeer.isNotBlank() &&
        sourceAgentId.isNotBlank() &&
        pendingInstanceId.isNotBlank() &&
        controlId.isNotBlank() &&
        peer.isNotBlank() &&
        source.isNotBlank() &&
        instanceId.isNotBlank() &&
        controlId == pendingControlId &&
        peer == pendingPeer &&
        source == sourceAgentId &&
        instanceId == pendingInstanceId
}

internal fun modelControlResultConfirmed(
    frame: JSONObject,
    pendingControlId: String,
    pendingPeer: String,
    sourceAgentId: String,
    pendingInstanceId: String,
): Boolean {
    if (!modelControlResultMatchesPending(frame, pendingControlId, pendingPeer, sourceAgentId, pendingInstanceId)) return false
    val accepted = frame.opt("accepted") as? Boolean ?: return false
    return modelControlConfirmed(accepted, normalizeModelSummary(frame.optJSONObject("current_model")))
}

internal fun modelControlStatusTitle(
    accepted: Boolean?,
    errorCode: String?,
    currentModel: ModelSummary? = null,
): String {
    if (modelControlConfirmed(accepted, currentModel)) return "Model changed"
    val confirmedRejection = setOf(
        "invalid_model",
        "model_registry_failed",
        "model_registry_unavailable",
        "model_switch_failed",
        "model_switch_rejected",
        "model_switch_unavailable",
        "model_unavailable",
        "model_control_not_allowed",
        "unsupported_model_control",
    )
    return if (errorCode != null && errorCode in confirmedRejection) "Model change rejected" else "Model change not confirmed"
}

internal fun acceptsLivePeerInstance(currentInstanceId: String, incomingInstanceId: String): Boolean =
    currentInstanceId.isBlank() || (incomingInstanceId.isNotBlank() && incomingInstanceId == currentInstanceId)

internal fun deferredModelCatalogFrameMatchesCurrentPeer(current: PeerStatus, frame: JSONObject): Boolean {
    val currentInstanceId = current.instanceId.trim()
    val frameInstanceId = frame.optString("instance_id").trim()
    return currentInstanceId.isNotBlank() && frameInstanceId.isNotBlank() && currentInstanceId == frameInstanceId
}

internal fun mergeDiscoveredPeer(
    peer: String,
    source: JSONObject,
    current: PeerStatus?,
    hasLiveUpdate: Boolean,
    deferredModelCatalogFrame: JSONObject? = null,
    pendingControlId: String = "",
    pendingControlPeer: String = "",
    pendingControlInstanceId: String = "",
): PeerStatus {
    val sourceInstanceId = (source.opt("instance_id") as? String)?.trim().orEmpty()
    val effectiveSource = deferredModelCatalogFrame
        ?.takeIf {
            val deferredInstanceId = (it.opt("instance_id") as? String)?.trim().orEmpty()
            sourceInstanceId.isNotBlank() &&
                deferredInstanceId.isNotBlank() &&
                deferredInstanceId == sourceInstanceId &&
                modelCatalogUpdateMatchesPending(
                peer = peer,
                frame = it,
                pendingControlId = pendingControlId,
                pendingControlPeer = pendingControlPeer,
                pendingControlInstanceId = pendingControlInstanceId,
            )
        }
        ?.let { mergeDeferredModelCatalogFrame(source, it) }
        ?: source
    val incomingInstanceId = (effectiveSource.opt("instance_id") as? String)?.trim().orEmpty()
    val currentInstanceId = current?.instanceId.orEmpty()
    val sameInstance = incomingInstanceId.isBlank() || currentInstanceId.isBlank() || incomingInstanceId == currentInstanceId
    return if (hasLiveUpdate && current != null && sameInstance) {
        current
    } else {
        peerStatusFromJson(
            peer,
            effectiveSource,
            current,
            pendingControlId = pendingControlId,
            pendingControlPeer = pendingControlPeer,
            pendingControlInstanceId = pendingControlInstanceId,
        )
    }
}

internal fun goalDialogShouldClose(openPeer: String, peers: List<PeerStatus>): Boolean =
    openPeer.isNotBlank() && peers.none { it.id == openPeer && it.goal?.enabled == true }

internal fun peerStatusFromJson(
    peer: String,
    source: JSONObject,
    current: PeerStatus? = null,
    pendingControlId: String = "",
    pendingControlPeer: String = "",
    pendingControlInstanceId: String = "",
): PeerStatus {
    val incomingInstanceId = (source.opt("instance_id") as? String)?.trim().orEmpty()
    val sameInstance = current == null || incomingInstanceId.isEmpty() || current.instanceId.isEmpty() || current.instanceId == incomingInstanceId
    val base = if (sameInstance) current else null

    val subagentsProvided = source.has("subagents")
    val countProvided = source.has("subagent_count")
    val summaries = normalizedSubagentSummaries(source.optJSONArray("subagents"))
    val incomingCount = source.optInt("subagent_count", 0).coerceIn(0, 999)
    val projectedSubagents = when {
        subagentsProvided -> summaries
        countProvided && incomingCount > 0 && base?.subagents?.size == incomingCount -> base.subagents
        sameInstance && !countProvided -> base?.subagents.orEmpty()
        else -> emptyList()
    }
    val projectedCount = when {
        countProvided -> maxOf(incomingCount, projectedSubagents.size)
        subagentsProvided -> projectedSubagents.size
        sameInstance -> base?.subagentCount ?: projectedSubagents.size
        else -> projectedSubagents.size
    }

    val goal = if (source.has("goal")) normalizeGoalProjection(source.optJSONObject("goal")) ?: base?.goal else base?.goal
    val currentModel = if (source.has("current_model")) normalizeModelSummary(source.optJSONObject("current_model")) ?: base?.currentModel else base?.currentModel
    val modelValues = modelCatalogValuesFromFrame(source)
    val deferEmptyModelCatalog = modelValues?.isEmpty() == true && modelCatalogUpdateMatchesPending(
        peer = peer,
        frame = source,
        pendingControlId = pendingControlId,
        pendingControlPeer = pendingControlPeer,
        pendingControlInstanceId = pendingControlInstanceId,
    )
    val modelsProvided = modelValues != null && !deferEmptyModelCatalog
    val availableModels = if (deferEmptyModelCatalog) base?.availableModels.orEmpty() else modelValues ?: base?.availableModels.orEmpty()
    val totalCountProvided = source.has("model_catalog_total_count") || source.has("total_count")
    val incomingTotalCount = when {
        source.has("model_catalog_total_count") -> source.optLong("model_catalog_total_count", -1L)
        source.has("total_count") -> source.optLong("total_count", -1L)
        else -> -1L
    }
    val modelCatalogTotalCount = when {
        deferEmptyModelCatalog -> base?.modelCatalogTotalCount ?: availableModels.size
        totalCountProvided && incomingTotalCount >= 0 -> maxOf(availableModels.size.toLong(), incomingTotalCount).coerceAtMost(Int.MAX_VALUE.toLong()).toInt()
        modelsProvided -> availableModels.size
        sameInstance -> maxOf(availableModels.size, base?.modelCatalogTotalCount ?: 0)
        else -> availableModels.size
    }
    val modelCatalogTruncated = when {
        deferEmptyModelCatalog -> base?.modelCatalogTruncated ?: false
        source.has("model_catalog_truncated") -> source.opt("model_catalog_truncated") as? Boolean ?: base?.modelCatalogTruncated ?: false
        source.has("truncated") -> source.opt("truncated") as? Boolean ?: false
        modelsProvided -> false
        sameInstance -> base?.modelCatalogTruncated ?: false
        else -> false
    }
    return PeerStatus(
        id = peer,
        activityState = (source.opt("activity_state") as? String)?.takeIf { it.isNotBlank() } ?: base?.activityState ?: "idle",
        subagentCount = projectedCount,
        subagents = projectedSubagents,
        instanceId = incomingInstanceId.ifEmpty { base?.instanceId.orEmpty() },
        goal = goal,
        currentModel = currentModel,
        availableModels = availableModels,
        modelCatalogTotalCount = modelCatalogTotalCount,
        modelCatalogTruncated = modelCatalogTruncated,
    )
}

internal fun scheduledReconnectIsCurrent(expectedGeneration: Int, currentGeneration: Int): Boolean =
    expectedGeneration == currentGeneration

internal fun formatMessageTimestamp(createdAt: String): String = runCatching {
    MessageTimestampFormatter.format(Instant.parse(createdAt))
}.getOrElse { "" }

private fun humanName(id: String): String = id.split(Regex("[._-]+"))
    .filter { it.isNotBlank() }
    .joinToString(" ") { part -> part.replaceFirstChar { it.uppercase() } }
    .ifBlank { id }
