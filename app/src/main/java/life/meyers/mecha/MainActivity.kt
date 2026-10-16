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
import androidx.lifecycle.lifecycleScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.net.HttpURLConnection
import java.net.URL
import java.net.URLEncoder
import androidx.activity.compose.BackHandler
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.gestures.detectTransformGestures
import androidx.compose.animation.core.LinearEasing
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
import androidx.compose.foundation.layout.BoxWithConstraints
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
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.ChevronRight
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material.icons.filled.Edit
import androidx.compose.material.icons.filled.ExpandLess
import androidx.compose.material.icons.filled.ExpandMore
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material.icons.filled.Terminal
import androidx.compose.material.icons.filled.Description
import androidx.compose.material.icons.filled.AccountTree
import androidx.compose.material.icons.filled.Flag
import androidx.compose.material.icons.filled.Schedule
import androidx.compose.material.icons.filled.Sync
import androidx.compose.material.icons.filled.Build
import androidx.compose.material.icons.filled.Check
import androidx.compose.material.icons.filled.Warning
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.Divider
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.Icon
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.MenuDefaults
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Surface
import androidx.compose.material3.Switch
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
import androidx.compose.ui.draw.drawWithCache
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.PathEffect
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.graphics.drawscope.DrawScope
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.graphics.vector.path
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.TextFieldValue
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.window.Dialog
import okhttp3.HttpUrl.Companion.toHttpUrl
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
import java.time.Instant
import java.time.OffsetDateTime
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.time.format.FormatStyle
import java.util.Locale
import java.util.LinkedHashMap
import java.util.Timer
import java.util.UUID
import java.util.concurrent.TimeUnit
import java.time.temporal.ChronoUnit
import kotlin.concurrent.scheduleAtFixedRate
import kotlinx.coroutines.delay
import kotlin.math.PI

private const val DEFAULT_ROUTER_PORT = 8765
private const val ROUTER_RECORD_SEPARATOR = "\u001f"
private const val PREFS = "mecha"
private const val MESSAGE_NOTIFICATION_CHANNEL = "messages"
private const val NOTIFICATION_PERMISSION_REQUEST_CODE = 1
// Keeps durable unsent drafts bounded per router.
private const val MAX_DRAFTS_PER_ROUTER = 50
internal const val MAX_DISCOVERED_PEERS = 1_000
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
private val EventTimestampFormatter: DateTimeFormatter = DateTimeFormatter.ofPattern("h:mm a", Locale.US).withZone(ZoneId.systemDefault())

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

internal data class PeerStatus(
    val id: String,
    val activityState: String = "idle",
    val subagentCount: Int = 0,
    val subagents: List<SubagentSummary> = emptyList(),
    val subagentVersion: Long? = null,
    val instanceId: String? = null,
    val modelCatalog: ModelCatalog = ModelCatalog(CatalogStatus.LOADING),
    val goalState: GoalState? = null,
    val source: String = "tmux",
    val runtime: String? = null,
) {
    val active get() = activityState == "active"
    val compacting get() = activityState == "compacting"
    val isEmbedded get() = source == "router-managed" && runtime == "embedded"
}

internal enum class ActivityOrbit(val color: Color) {
    WORKING(Teal),
    SUBAGENT(Color(0xFF60A5FA)),
    COMPACTING(Color(0xFFC084FC)),
}

internal fun activityOrbitFor(peer: PeerStatus): ActivityOrbit? = when {
    peer.compacting -> ActivityOrbit.COMPACTING
    peer.active -> ActivityOrbit.WORKING
    peer.subagentCount > 0 -> ActivityOrbit.SUBAGENT
    else -> null
}

private data class DiscoveredPeer(
    val id: String,
    val source: JSONObject,
    val instanceId: String,
    val observedAtMs: Long?,
)

private data class PendingDeliveryAcknowledgement(
    val keys: List<DeliveryLedgerKey>,
    val attemptNumber: Int,
    val connection: DeliveryConnectionIdentity,
)

private data class StoredHistoryRead(
    val messages: List<ChatMessage>,
    val migrated: Boolean,
)

internal data class ReminderSummary(
    val id: String,
    val target: String,
    val creator: String,
    val subject: String?,
    val body: String,
    val createdAt: String,
    val nextDueAt: String,
    val intervalMs: Long?,
    val state: String,
    val enabled: Boolean = true,
)

internal enum class ReminderStatus { IDLE, LOADING, SAVING, LOCK_PENDING, ACCEPTED, CREATED_LOCK_FAILED, REJECTED, TIMEOUT, UNCONFIRMED }

internal data class ReminderUiState(
    val status: ReminderStatus = ReminderStatus.IDLE,
    val message: String? = null,
    val errorCode: String? = null,
)

internal data class PendingReminderCrud(
    val requestId: String,
    val operation: String,
    val peer: String,
    val router: String,
    val connectionGeneration: Int,
    val lockAfterCreate: Boolean = false,
    val payloadId: String? = null,
    val reminderId: String? = payloadId,
    val targetInstanceId: String? = null,
)

internal data class PendingReminderLock(
    val controlId: String,
    val requestFrameId: String,
    val peer: String,
    val targetInstanceId: String,
    val reminderId: String,
    val action: String,
    val router: String,
    val connectionGeneration: Int,
    val afterCreate: Boolean = false,
)

internal data class ParsedReminderLockResult(
    val agentId: String,
    val instanceId: String,
    val leaseId: String,
    val sourceAgentId: String,
    val controlId: String,
    val reminderId: String,
    val accepted: Boolean,
    val errorCode: String?,
)

internal enum class PromptRequestStatus { IDLE, LOADING, SAVING, SUCCESS, ERROR, UNCONFIRMED }

internal data class PromptRequestState(
    val status: PromptRequestStatus = PromptRequestStatus.IDLE,
    val message: String? = null,
    val successSerial: Int = 0,
)

/** Publishes success after starting the refresh so the editor-close effect can observe it. */
internal fun completePromptTemplateSave(
    currentSerial: Int,
    operation: String,
    refresh: () -> Unit,
    publish: (PromptRequestState) -> Unit,
): Int {
    refresh()
    val message = if (operation == "delete") "Template deleted." else "Template saved."
    val success = PromptRequestState(PromptRequestStatus.SUCCESS, message, currentSerial + 1)
    publish(success)
    return success.successSerial
}

private enum class PromptImportPhase { CONFIRM, REFRESHING, RUNNING }

private data class PromptImportState(
    val templates: List<PromptTemplate>,
    val conflicts: List<PromptTemplate>,
    val phase: PromptImportPhase = PromptImportPhase.CONFIRM,
    val index: Int = 0,
    val completed: Int = 0,
    val confirmedConflictIds: Set<String> = emptySet(),
)

private data class PendingPromptRequest(
    val requestId: String,
    val operation: String,
    val templateId: String?,
    val router: String,
    val connectionGeneration: Int,
    val scopeGeneration: Int,
    val importIndex: Int? = null,
)

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

internal sealed interface ConversationTimelineEntry {
    val timestamp: String
    val key: String

    data class Chat(val message: ChatMessage) : ConversationTimelineEntry {
        override val timestamp get() = message.createdAt
        override val key get() = message.key
    }

    data class Event(val event: AgentTimelineEvent) : ConversationTimelineEntry {
        override val timestamp get() = event.timestamp
        override val key get() = event.key
    }
}

internal data class AgentTimelineEvent(
    val id: String,
    val agentId: String,
    val timestamp: String,
    val kind: String,
    val toolName: String?,
    val summary: String,
    val outcome: String?,
    val toolInput: String?,
    val output: String? = null,
) {
    val key: String get() = "event:$id"
}

internal data class ChatMessage(
    val id: String,
    val incarnationId: String,
    val sequence: Long,
    val sender: String,
    val body: String,
    val direction: Direction,
    val createdAt: String,
    val recordedAt: String? = null,
    val attachments: List<ImageReference> = emptyList(),
    val recipient: String = "",
    val deliveryStatus: OutboundDeliveryStatus = OutboundDeliveryStatus.NONE,
) {
    val key get() = "$id\u001f$incarnationId"
}

internal fun deliveryReceiptFrame(
    type: String,
    agentId: String,
    instanceId: String,
    leaseId: String,
    deliveryItems: List<ValidatedDeliveryItem>,
    correlatedAck: Boolean,
    frameId: String = UUID.randomUUID().toString(),
): JSONObject {
    val first = deliveryItems.first()
    return JSONObject()
        .put("version", "v1")
        .put("frame_id", frameId)
        .put("type", type)
        .put("agent_id", agentId)
        .put("instance_id", instanceId)
        .put("lease_id", leaseId)
        .put("message_id", first.messageId)
        .put("attempt_number", first.attemptNumber)
        .apply {
            if (correlatedAck) put("message_incarnation_id", first.messageIncarnationId)
            if (correlatedAck || deliveryItems.size > 1) {
                put("delivery_items", JSONArray(deliveryItems.map(::deliveryItemJson)))
            }
        }
}

/** Persists every original delivery item before either router receipt is sent. */
internal fun settleIncomingDelivery(
    frame: JSONObject,
    agentId: String,
    instanceId: String,
    leaseId: String,
    parseMessage: (JSONObject) -> ChatMessage?,
    persistMessage: (String, ChatMessage) -> Boolean,
    sendInjection: (JSONObject) -> Unit,
    sendAck: (JSONObject) -> Unit,
    correlatedAck: Boolean = true,
): Boolean {
    val validated = validateLiveMessageFrame(
        frame,
        LiveMessageFrameKind.DELIVERY_MESSAGE,
        requireIncarnation = correlatedAck,
    ) ?: return false
    val messages = validated.deliveryMessages.map { deliveryMessage ->
        val item = deliveryMessage.item
        val payload = deliveryMessage.payload
        if (!isInboundDeliveryForAgent(payload.getString("from"), payload.getString("to"), agentId)) return false
        val message = runCatching { parseMessage(payload) }.getOrNull()?.copy(
            id = item.messageId,
            incarnationId = item.messageIncarnationId,
        ) ?: return false
        if (message.id != item.messageId || message.sender != payload.getString("from")
            || !isInboundDeliveryForAgent(message.sender, message.recipient, agentId)
        ) return false
        message
    }
    // A batch is retryable until every original envelope has been accepted locally.
    if (messages.size != validated.deliveryItems.size) return false
    messages.forEach { message ->
        if (!persistMessage(message.sender, message)) return false
    }
    // Receipt transport is best effort after persistence. A failed send must not undo saved history.
    runCatching {
        sendInjection(deliveryReceiptFrame("injection_requested", agentId, instanceId, leaseId, validated.deliveryItems, correlatedAck))
    }
    runCatching {
        sendAck(deliveryReceiptFrame("ack", agentId, instanceId, leaseId, validated.deliveryItems, correlatedAck))
    }
    return true
}

internal fun captureDeliveryForSettlement(
    frame: JSONObject,
    connection: DeliveryConnectionIdentity,
    parseMessage: (ValidatedLiveMessage, String, String) -> ChatMessage?,
    targetInstanceId: (String) -> String?,
    correlatedAck: Boolean = true,
): CapturedDelivery? {
    val validated = validateLiveMessageFrame(
        frame,
        LiveMessageFrameKind.DELIVERY_MESSAGE,
        requireIncarnation = correlatedAck,
    ) ?: return null
    val messages = validated.deliveryMessages.map { deliveryMessage ->
        val item = deliveryMessage.item
        val payload = deliveryMessage.payload
        if (!isInboundDeliveryForAgent(
                payload.getString("from"),
                payload.getString("to"),
                connection.agentId,
            )
        ) return null
        val message = parseMessage(
            validated.copy(
                payload = payload,
                incarnationId = item.messageIncarnationId,
                attemptNumber = item.attemptNumber,
                deliveryItems = listOf(item),
                deliveryMessages = listOf(deliveryMessage),
            ),
            connection.router,
            connection.agentId,
        ) ?: return null
        if (message.id != item.messageId || message.sender != payload.getString("from")
            || !isInboundDeliveryForAgent(message.sender, message.recipient, connection.agentId)
        ) return null
        message
    }
    if (messages.size != validated.deliveryItems.size) return null
    val peers = messages.map { conversationPeerForMessage(it, connection.agentId) ?: return null }.distinct()
    return CapturedDelivery(
        connection = connection,
        peer = peers.first(),
        targetInstanceId = targetInstanceId(peers.first()),
        message = messages.first(),
        correlatedAck = correlatedAck,
        attemptNumber = validated.deliveryItems.first().attemptNumber,
        messages = messages,
        deliveryItems = validated.deliveryItems,
        messagesByPeer = messages.groupBy { conversationPeerForMessage(it, connection.agentId)!! },
        targetInstanceIdsByPeer = peers.associateWith(targetInstanceId),
    )
}

private fun deliveryItemJson(item: ValidatedDeliveryItem): JSONObject = JSONObject()
    .put("message_id", item.messageId)
    .put("message_incarnation_id", item.messageIncarnationId)
    .put("attempt_number", item.attemptNumber)
    .put("delivery_expires_at", item.deliveryExpiresAt)

internal data class RouterConfig(
    val alias: String,
    val endpoint: String,
    val scheme: String = "http",
) {
    val displayName: String get() = alias.ifBlank { endpoint }
    val url: String get() = "${scheme}://$endpoint"
    val identity: String get() = url
    fun persist() = "$alias$ROUTER_RECORD_SEPARATOR${if (scheme == "https") "https://" else ""}$endpoint"
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
        val stored = get(router, peer)
        // Older drafts may retain escaped newlines. Compare that legacy form to the
        // already-normalized sent text without changing the meaning of a literal sent value.
        if (stored != sent && normalizeEscapedNewlines(stored) != sent) return false
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
    private val imageLoader = RemoteImageLoader(OkHttpImageFetcher(client))
    private val historyLock = ConversationStateLock()
    private val deliveryCoordinator = ConversationDeliveryCoordinator(historyLock)
    private lateinit var deliveryReplayLedger: DeliveryReplayLedger
    private val pendingDeliveryAcknowledgements = mutableMapOf<String, PendingDeliveryAcknowledgement>()
    private lateinit var instanceId: String
    private lateinit var drafts: DraftStore
    private var socket: WebSocket? = null
private var liveEventsByAgent by mutableStateOf<Map<String, List<AgentTimelineEvent>>>(emptyMap())
    private var uiLiveSocket: WebSocket? = null
    private var leaseTimer: Timer? = null
    private var leaseId = ""
    private var negotiatedAckConfirmation = ""
    private var pendingLeaseRenewalRequestId: String? = null
    private var connectionGeneration = 0
    private var lifecycleEnabled = false
    private var lifecycleVersion = 0L
    private var lifecycleRouter = ""
    private val arrivalsByAgent = LinkedHashMap<String, MessageArrival>()
    private val arrivalTimers = mutableMapOf<String, Runnable>()
    private val reconnectHandler = Handler(Looper.getMainLooper())

    private var agentId by mutableStateOf("")
    private var activeRouter by mutableStateOf("")
    private var connection by mutableStateOf("Connecting to your agents…")
    private var peers by mutableStateOf(emptyList<PeerStatus>())
    private var agentFilter by mutableStateOf("")
    private var selectedPeer by mutableStateOf("")
    private var messages by mutableStateOf(emptyList<ChatMessage>())
    private var outboundState by mutableStateOf(OutboundReducerState())
    private var images by mutableStateOf(emptyList<PendingImage>())
    private var draftValue by mutableStateOf(TextFieldValue())
    private var showProfileEditor by mutableStateOf(false)
    private var showRouterManager by mutableStateOf(false)
    private var routerRevision by mutableStateOf(0)
    private var routerError by mutableStateOf("")
    private var editingRouterEndpoint by mutableStateOf<String?>(null)
    private var arrivals by mutableStateOf<Map<String, MessageArrival>>(emptyMap())
    private var selectedArrival by mutableStateOf<MessageArrival?>(null)
    private var capabilities by mutableStateOf(ControlCapabilities())
    private val pendingModelControls = mutableMapOf<String, PendingControl>()
    private val pendingGoalControls = mutableMapOf<String, PendingControl>()
    private val controlHandler = Handler(Looper.getMainLooper())
    private var modelControlState by mutableStateOf(ControlState())
    private var goalControlState by mutableStateOf(ControlState())
    private var promptTemplates by mutableStateOf(emptyList<PromptTemplate>())
    private var promptTemplatesLoaded by mutableStateOf(false)
    private var promptRequestState by mutableStateOf(PromptRequestState())
    private var promptImportState by mutableStateOf<PromptImportState?>(null)
    private var promptScopeGeneration = 0
    private var promptRequestSerial = 0
    private val pendingPromptRequests = mutableMapOf<String, PendingPromptRequest>()
    private var reminders by mutableStateOf(emptyList<ReminderSummary>())
    private var reminderUiState by mutableStateOf(ReminderUiState())
    private var historyUiState by mutableStateOf(HistoryUiState())
    private var hydrationRequestSerial = 0L
    private var activeHydrationFence: HistoryHydrationFence? = null
    private val peerStateEventFence = PeerStateEventFence()
    private val historyRecoveryTasks = mutableMapOf<String, Runnable>()
    private val historyRecoveryAttempts = mutableMapOf<String, Int>()
    private var reminderLocks by mutableStateOf(emptyMap<String, Boolean>())
    private var reminderRefreshSerial = 0
    private val pendingReminderCrud = LinkedHashMap<String, PendingReminderCrud>()
    private val pendingReminderLocks = LinkedHashMap<String, PendingReminderLock>()

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val preferences = prefs()
        drafts = DraftStore(preferences)
        deliveryReplayLedger = DeliveryReplayLedger(preferences)
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
        cancelConnection()
        super.onDestroy()
    }

    private fun prefs() = getSharedPreferences(PREFS, MODE_PRIVATE)

    private fun routers(): List<RouterConfig> = routerConfigsFromRecords(
        prefs().getStringSet("router_endpoints", emptySet()).orEmpty(),
        allowCleartextLoopback = BuildConfig.DEBUG,
    )

    private fun persistedActiveRouter(): String? {
        val routers = routers()
        val saved = prefs().getString("active_router", null)
        val savedCandidates = savedRouterCandidates(saved, routers)
        val active = persistedRouterConfig(saved, routers, BuildConfig.DEBUG)
        if (saved != null && savedCandidates.none { routerConfigAllowed(it) }) {
            routerEndpointValidationMessage(
                savedCandidates.firstOrNull()?.url ?: saved,
                allowCleartextLoopback = BuildConfig.DEBUG,
            )?.let {
                routerError = it
                showRouterManager = true
            }
        }
        if (active == null) prefs().edit().remove("active_router").commit()
        else if (saved != active.identity) prefs().edit().putString("active_router", active.identity).commit()
        return active?.identity
    }

    private fun routerConfigAllowed(config: RouterConfig): Boolean = routerConfigAllowed(config, BuildConfig.DEBUG)

    private fun routerConfig(router: String): RouterConfig? = routers().firstOrNull { it.identity == router }
        ?: routers().firstOrNull { it.endpoint == router }

    private fun activeRouterConfig(): RouterConfig? = routerConfig(activeRouter)

    private fun activeRouterScheme(): String = activeRouterConfig()?.scheme ?: "http"

    private fun routerHttp() = activeRouterConfig()?.let { routerHttpUrl(it.endpoint, it.scheme) }
        ?: routerHttpUrl(activeRouter, activeRouterScheme())

    private fun routerSchemeFor(router: String): String = routerConfig(router)?.scheme ?: "http"

    private fun routerHttpFor(router: String): String = routerConfig(router)?.let { routerHttpUrl(it.endpoint, it.scheme) }
        ?: routerHttpUrl(router, routerSchemeFor(router))

    private fun routerWs() = activeRouterConfig()?.let { routerWebSocketUrl(it.endpoint, it.scheme) }
        ?: routerWebSocketUrl(activeRouter, activeRouterScheme())

    private fun routerHistoryKey(peer: String) = historyKey(activeRouter, agentId, instanceId, peer)

    private fun historyKey(router: String, owner: String, ownerInstance: String, peer: String) =
        historyPreferenceKey(router, owner, ownerInstance, peer)

    private fun previousRouterHistoryKey(peer: String) = previousRouterHistoryKey(activeRouter, agentId, peer)

    private fun previousRouterHistoryKey(router: String, owner: String, peer: String) = "history:$router:$owner:$peer"

    private fun legacyRouterHistoryKey(peer: String) = legacyRouterHistoryKey(activeRouter, peer)

    private fun legacyRouterHistoryKey(router: String, peer: String) = "history:$router:$peer"

    private fun draftFor(peer: String) = drafts.get(activeRouter, peer)

    private fun updateDraft(value: TextFieldValue) {
        val normalized = normalizeComposerValue(value)
        draftValue = normalized
        if (selectedPeer.isNotBlank()) drafts.save(activeRouter, selectedPeer, normalized.text)
    }

    private fun clearDraftIfUnchanged(router: String, peer: String, sentText: String) {
        drafts.clearIfUnchanged(router, peer, sentText)
        if (activeRouter == router && selectedPeer == peer && draftValue.text == sentText) {
            draftValue = TextFieldValue()
        }
    }

    private fun clearImagesIfUnchanged(router: String, peer: String, sentImages: List<PendingImage>) {
        if (activeRouter == router && selectedPeer == peer && images === sentImages) images = emptyList()
    }

    private fun knownPeersKey() = knownPeersKeyFor(activeRouter)

    private fun knownPeersKeyFor(router: String) = "known_peers:$router"

    private fun knownPeers(): Set<String> = knownPeers(activeRouter)

    private fun knownPeers(router: String): Set<String> = prefs().getStringSet(knownPeersKeyFor(router), emptySet())?.toSet() ?: emptySet()

    private fun persistKnownPeers(value: Set<String>): Boolean = persistKnownPeers(activeRouter, value)

    private fun persistKnownPeers(router: String, value: Set<String>): Boolean = prefs().edit()
        .putStringSet(knownPeersKeyFor(router), value)
        .commit()

    private fun connect() {
        settleOutboundOnConnectionChange()
        if (activeRouter.isBlank()) {
            connection = "Add a router to connect"
            return
        }
        val config = routerConfig(activeRouter)
        if (config == null || !routerConfigAllowed(config)) {
            connection = routerEndpointValidationMessage(
                config?.url ?: activeRouter,
                allowCleartextLoopback = BuildConfig.DEBUG,
            ) ?: "Choose a valid router configuration."
            showRouterManager = true
            return
        }
        val router = activeRouter
        val generation = historyLock.withLock {
            val nextGeneration = connectionGeneration + 1
            connectionGeneration = nextGeneration
            peerStateEventFence.reset(nextGeneration)
            lifecycleEnabled = false
            lifecycleRouter = router
            leaseId = ""
            negotiatedAckConfirmation = ""
            pendingLeaseRenewalRequestId = null
            pendingDeliveryAcknowledgements.clear()
            activeHydrationFence = null
            deliveryCoordinator.begin(
                DeliveryConnectionIdentity(router, agentId, instanceId, nextGeneration, ""),
            )
            nextGeneration
        }
        connection = "Connecting to your agents…"
        val registrationRequestId = UUID.randomUUID().toString()
                val uiLiveWsUrl = routerWs()?.replace(Regex("/v1/agents.*"), "/v1/ui-live")
            ?: routerWs()?.replace(Regex("/v1.*"), "/v1/ui-live")
            ?: (activeRouterConfig()?.let {
                val scheme = if (it.scheme == "https") "wss" else "ws"
                "$scheme://${it.endpoint}/v1/ui-live"
            } ?: "ws://$activeRouter/v1/ui-live")
        uiLiveSocket = client.newWebSocket(Request.Builder().url(uiLiveWsUrl).build(), object : WebSocketListener() {
            override fun onMessage(webSocket: WebSocket, text: String) {
                if (generation != connectionGeneration) return
                val frame = try { JSONObject(text) } catch (_: Exception) { return }
                if (frame.optString("type") == "managed_children_snapshot") {
                    val childrenArray = frame.optJSONArray("children")
                    if (childrenArray != null) {
                        val byAgent = mutableMapOf<String, List<SubagentSummary>>()
                        for (i in 0 until childrenArray.length()) {
                            val item = childrenArray.optJSONObject(i) ?: continue
                            val aId = item.optString("agent_id").takeIf { it.isNotBlank() } ?: continue
                            val subs = normalizedSubagentSummaries(item.optJSONArray("children"))
                            byAgent[aId] = subs
                        }
                        runOnUiThread {
                            if (generation == connectionGeneration) {
                                peers = peers.map { peer ->
                                    val subs = byAgent[peer.id]
                                    if (subs != null) peer.copy(subagents = subs, subagentCount = subs.size) else peer
                                }
                            }
                        }
                    }
                    return
                }
                if (frame.optString("type") == "history_entry") {
                    val entry = frame.optJSONObject("entry") ?: return
                    val eId = entry.optString("id").takeIf { it.isNotBlank() } ?: return
                    val targetAgent = entry.optString("agent_id").takeIf { it.isNotBlank() } ?: return
                    val at = entry.optString("at")
                    val presentation = entry.optJSONObject("presentation")
                    val kind = presentation?.optString("kind").orEmpty()
                    val toolName = presentation?.optString("tool_name")
                    val toolInputObj = entry.optJSONObject("tool_input")

                    // 1. Reconcile Messages vs Event Chips:
                    // Check if this entry is a chat message to/from Wolfgang or user:
                    val isMessage = kind == "message"
                        || toolName == "mecha_mailbox_send"
                        || (toolName == "mecha_worker" && toolInputObj?.optString("action") == "send")

                    val sender = entry.optString("sender_identity").takeIf { it.isNotBlank() }
                        ?: presentation?.optString("from").takeIf { !it.isNullOrBlank() }
                        ?: entry.optString("agent_id")
                    val recipient = toolInputObj?.optString("recipient").takeIf { !it.isNullOrBlank() }
                        ?: presentation?.optString("to")
                    val content = toolInputObj?.optString("body").takeIf { !it.isNullOrBlank() }
                        ?: entry.optString("message_content").takeIf { it.isNotBlank() }
                        ?: entry.optString("message")

                    if (isMessage) {
                        val isUser = { id: String? ->
                            if (id.isNullOrBlank()) false
                            else {
                                val norm = id.trim().lowercase()
                                norm == "wolfgang" || norm == "user" || norm == agentId.lowercase() || norm.startsWith("android-")
                            }
                        }
                        val isIncomingToWolfgang = isUser(recipient)
                        val isWolfgangPrompt = isUser(sender)

                        if (isIncomingToWolfgang) {
                            // Incoming chat message from the agent to Wolfgang!
                            val incomingMsg = ChatMessage(
                                id = eId,
                                incarnationId = "",
                                sequence = 0L,
                                sender = targetAgent,
                                body = content,
                                direction = Direction.INCOMING,
                                createdAt = at.takeIf { it.isNotBlank() } ?: Instant.now().toString(),
                                recipient = recipient.orEmpty(),
                            )
                            runOnUiThread {
                                if (generation == connectionGeneration) {
                                    val currentMsgs = outboundState.messagesByPeer[targetAgent].orEmpty()
                                    if (currentMsgs.none { it.id == eId || (it.body == content && content.isNotBlank()) }) {
                                        val updatedMsgs = currentMsgs + incomingMsg
                                        outboundState = outboundState.copy(
                                            messagesByPeer = outboundState.messagesByPeer + (targetAgent to updatedMsgs)
                                        )
                                        if (selectedPeer == targetAgent) {
                                            messages = updatedMsgs
                                        }
                                    }
                                }
                            }
                            return
                        } else if (isWolfgangPrompt) {
                            // Wolfgang's user prompt!
                            val outgoingMsg = ChatMessage(
                                id = eId,
                                incarnationId = "",
                                sequence = 0L,
                                sender = "wolfgang",
                                body = content,
                                direction = Direction.OUTGOING,
                                createdAt = at.takeIf { it.isNotBlank() } ?: Instant.now().toString(),
                                recipient = targetAgent,
                                deliveryStatus = OutboundDeliveryStatus.SENT,
                            )
                            runOnUiThread {
                                if (generation == connectionGeneration) {
                                    val currentMsgs = outboundState.messagesByPeer[targetAgent].orEmpty()
                                    if (currentMsgs.none { it.id == eId || (it.body == content && content.isNotBlank()) }) {
                                        val updatedMsgs = currentMsgs + outgoingMsg
                                        outboundState = outboundState.copy(
                                            messagesByPeer = outboundState.messagesByPeer + (targetAgent to updatedMsgs)
                                        )
                                        if (selectedPeer == targetAgent) {
                                            messages = updatedMsgs
                                        }
                                    }
                                }
                            }
                            return
                        }
                        // In general: If kind == "message", NEVER add it to liveEventsByAgent!
                        return
                    }

                    val summary = presentation?.optString("summary") ?: entry.optString("message")
                    val outcome = presentation?.optString("outcome")
                    val toolInput = toolInputObj?.toString(2)
                    val output = entry.optString("message_content").takeIf { it.isNotBlank() }
                        ?: presentation?.optJSONObject("error")?.optString("message")
                        ?: entry.optString("message").takeIf { it != summary }
                    val event = AgentTimelineEvent(
                        id = eId,
                        agentId = targetAgent,
                        timestamp = at,
                        kind = kind,
                        toolName = toolName,
                        summary = summary,
                        outcome = outcome,
                        toolInput = toolInput,
                        output = output,
                    )
                    runOnUiThread {
                        if (generation == connectionGeneration) {
                            val existing = liveEventsByAgent[targetAgent].orEmpty()
                            if (existing.none { it.id == eId }) {
                                liveEventsByAgent = liveEventsByAgent + (targetAgent to (existing + event).takeLast(50))
                            }
                        }
                    }
                }
            }
        })
        socket = client.newWebSocket(Request.Builder().url(routerWs()).build(), object : WebSocketListener() {
            override fun onOpen(webSocket: WebSocket, response: Response) {
                if (generation != connectionGeneration) return
                webSocket.send(
                    JSONObject()
                        .put("version", "v1")
                        .put("frame_id", registrationRequestId)
                        .put("type", "register")
                        .put("agent_id", agentId)
                        .put("agent_role", "user_interaction")
                        .put("instance_id", instanceId)
                        .put("client_type", "android")
                        .put("ack_confirmation", "correlated_v1")
                        .put("conversation_sync", "v1")
                        .put("delivery_receipts", "delivery_items_v1")
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
                        val currentLease = historyLock.withLock { leaseId }
                        val expectedRequestId = if (currentLease.isBlank()) registrationRequestId else pendingLeaseRenewalRequestId
                        val registered = expectedRequestId?.let {
                            parseRegisteredSession(frame, it, agentId, instanceId)
                        }
                        val accepted = registered != null && historyLock.withLock {
                            if (generation != connectionGeneration || activeRouter != router) {
                                false
                            } else if (currentLease.isBlank()) {
                                leaseId = registered.leaseId
                                negotiatedAckConfirmation = registered.ackConfirmation.orEmpty()
                                pendingLeaseRenewalRequestId = null
                                deliveryCoordinator.register(generation, registered.leaseId)
                            } else {
                                val renewalAccepted = registeredSessionMatchesLease(registered, currentLease)
                                if (renewalAccepted) {
                                    negotiatedAckConfirmation = registered.ackConfirmation.orEmpty()
                                    pendingLeaseRenewalRequestId = null
                                }
                                renewalAccepted
                            }
                        }
                        if (!accepted) {
                            historyLock.withLock {
                                if (generation == connectionGeneration && activeRouter == router) {
                                    leaseId = ""
                                    negotiatedAckConfirmation = ""
                                    pendingLeaseRenewalRequestId = null
                                    pendingDeliveryAcknowledgements.clear()
                                    deliveryCoordinator.invalidate()
                                }
                            }
                            connection = "Router rejected this connection"
                            webSocket.close(4003, "registration mismatch")
                            return
                        }
                        if (currentLease.isNotBlank()) return
                        connection = "Connected as $agentId"
                        startLeaseRenewal(webSocket, generation)
                        discoverPeers(generation)
                        fetchCapabilities(generation, router, registered!!.leaseId)
                        fetchReminders(generation)
                        runOnUiThread { if (generation == connectionGeneration) requestPromptTemplates() }
                        selectedPeer.takeIf { it.isNotBlank() }?.let { hydrateHistory(it, generation) }
                    }
                    "delivery_acknowledged" -> receiveDeliveryAcknowledged(frame, generation)
                    "lease_rejected" -> connection = "Username is already connected elsewhere"
                    "error" -> receiveRouterError(frame)
                    "conversation_message" -> runOnUiThread {
                        if (generation == connectionGeneration) receiveConversationMessage(frame)
                    }
                    "agent_activity" -> runOnUiThread {
                        if (generation == connectionGeneration) receiveAgentActivity(frame)
                    }
                    "agent_model_catalog" -> runOnUiThread {
                        if (generation == connectionGeneration) receiveModelCatalog(frame)
                    }
                    "agent_goal_state" -> runOnUiThread {
                        if (generation == connectionGeneration) receiveGoalState(frame)
                    }
                    "model_control_result" -> runOnUiThread {
                        if (generation == connectionGeneration) receiveModelControlResult(frame)
                    }
                    "goal_control_result" -> runOnUiThread {
                        if (generation == connectionGeneration) receiveGoalControlResult(frame)
                    }
                    "subagent_state_snapshot", "subagent_state_update", "subagent_state_replay" -> runOnUiThread {
                        if (generation == connectionGeneration && lifecycleEnabled && lifecycleRouter == activeRouter && leaseId.isNotBlank()) {
                            receiveLifecycleState(frame, generation, leaseId)
                        }
                    }
                    "message_arrival" -> runOnUiThread {
                        if (generation == connectionGeneration && lifecycleRouter == activeRouter && leaseId.isNotBlank()) receiveMessageArrival(frame, generation, leaseId)
                    }
                    "reminder_lock_control_result" -> runOnUiThread {
                        if (generation == connectionGeneration) receiveReminderLockResult(frame)
                    }
                    "message" -> settleDelivery(webSocket, frame, generation)
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
        settleOutboundOnConnectionChange()
        historyLock.withLock {
            connectionGeneration++
            peerStateEventFence.reset(connectionGeneration)
            activeHydrationFence = null
            cancelHistoryRecoveryUnlocked()
            deliveryCoordinator.invalidate()
            leaseId = ""
            negotiatedAckConfirmation = ""
            pendingLeaseRenewalRequestId = null
            pendingDeliveryAcknowledgements.clear()
        }
        clearReminderScope("The router connection closed before this reminder change was confirmed.")
        pendingModelControls.values.forEach { controlHandler.removeCallbacksAndMessages(it.controlId) }
        pendingGoalControls.values.forEach { controlHandler.removeCallbacksAndMessages(it.controlId) }
        pendingModelControls.clear()
        pendingGoalControls.clear()
        if (modelControlState.status == ControlStatus.PENDING) modelControlState = modelControlState.copy(status = ControlStatus.UNCONFIRMED, message = "The router connection closed before this change was confirmed.")
        if (goalControlState.status == ControlStatus.PENDING) goalControlState = goalControlState.copy(status = ControlStatus.UNCONFIRMED, message = "The router connection closed before this change was confirmed.")
        capabilities = ControlCapabilities()
        lifecycleEnabled = false
        lifecycleRouter = ""
        lifecycleVersion = 0L
        clearMessageArrivals()
        clearPromptTemplateScope()
        leaseTimer?.cancel()
        leaseTimer = null
        uiLiveSocket?.close(1000, "reconnecting")
        uiLiveSocket = null
        socket?.close(1000, "reconnecting")
        socket = null
    }

    private fun startLeaseRenewal(webSocket: WebSocket, generation: Int) {
        leaseTimer?.cancel()
        leaseTimer = Timer().apply {
            scheduleAtFixedRate(20_000, 20_000) {
                if (generation != connectionGeneration || leaseId.isBlank()) return@scheduleAtFixedRate
                val requestId = UUID.randomUUID().toString()
                pendingLeaseRenewalRequestId = requestId
                webSocket.send(
                    JSONObject()
                        .put("version", "v1")
                        .put("frame_id", requestId)
                        .put("type", "lease_renew")
                        .put("agent_id", agentId)
                        .put("instance_id", instanceId)
                        .put("lease_id", leaseId)
                        .toString(),
                )
            }
            scheduleAtFixedRate(60_000, 60_000) {
                if (generation != connectionGeneration || leaseId.isBlank()) return@scheduleAtFixedRate
                discoverPeers(generation)
            }
        }
    }

    private fun refreshPeers() {
        if (leaseId.isNotBlank()) discoverPeers(connectionGeneration)
        if (selectedPeer.isNotBlank()) {
            hydrateActivityHistory(selectedPeer)
        }
    }

    private fun fetchCapabilities(generation: Int, router: String = activeRouter, registeredLease: String = leaseId) = Thread {
        val discovered = runCatching {
            client.newBuilder()
                .callTimeout(CAPABILITY_DISCOVERY_TIMEOUT_MS, TimeUnit.MILLISECONDS)
                .build()
                .newCall(Request.Builder().url("${routerHttpFor(router)}/v1/capabilities").build())
                .execute()
                .use { response ->
                    if (!response.isSuccessful) null else parseCapabilities(JSONObject(response.body?.string().orEmpty()))
                }
        }.getOrNull()
        runOnUiThread {
            if (generation != connectionGeneration || router != activeRouter || registeredLease.isBlank() || registeredLease != leaseId) return@runOnUiThread
            capabilities = capabilityDiscoveryFallback(discovered)
            if (capabilities.subagentLifecycleV2) enableLifecycleSync(generation, router, registeredLease)
        }
    }.start()

    private fun enableLifecycleSync(generation: Int, router: String, registeredLease: String) {
        if (!capabilities.subagentLifecycleV2 || generation != connectionGeneration || router != activeRouter
            || registeredLease.isBlank() || registeredLease != leaseId || lifecycleRouter != router) return
        val webSocket = socket ?: return
        val afterVersion = lifecycleVersion.takeIf { it > 0L }
        val frame = buildSubagentSyncEnableFrame(
            frameId = "subagent-enable-${UUID.randomUUID()}",
            agentId = agentId,
            instanceId = instanceId,
            leaseId = registeredLease,
            afterVersion = afterVersion,
        )
        if (webSocket.send(frame.toString())) lifecycleEnabled = true
    }

    private fun receiveLifecycleState(frame: JSONObject, generation: Int, registeredLease: String) = historyLock.withLock {
        if (generation != connectionGeneration || lifecycleRouter != activeRouter || registeredLease != leaseId) return@withLock
        val parsed = parseLifecycleStateFrame(frame) ?: return@withLock
        when (parsed.type) {
            "subagent_state_snapshot" -> {
                val result = applyLifecycleSnapshot(peers, parsed.updates, parsed.routerVersion, lifecycleVersion, agentId)
                peers = result.peers
                lifecycleVersion = maxOf(lifecycleVersion, result.lastSnapshotVersion)
            }
            "subagent_state_update" -> {
                peers = upsertLifecycleAgent(peers, parsed.updates.single(), agentId)
                lifecycleVersion = maxOf(lifecycleVersion, parsed.routerVersion)
            }
            "subagent_state_replay" -> {
                val result = applyLifecycleReplay(peers, parsed.updates, parsed.routerVersion, lifecycleVersion, agentId)
                peers = result.first
                lifecycleVersion = result.second
            }
        }
    }

    private fun receiveMessageArrival(frame: JSONObject, generation: Int, registeredLease: String) {
        if (generation != connectionGeneration || lifecycleRouter != activeRouter || registeredLease != leaseId) return
        val arrival = parseMessageArrivalFrame(frame, agentId) ?: return
        arrivals = recordMessageArrival(arrivals, arrival)
        arrivalsByAgent.clear()
        arrivalsByAgent.putAll(arrivals)
        arrivalTimers.remove(arrival.agent)?.let(reconnectHandler::removeCallbacks)
        val timer = Runnable { dismissMessageArrival(arrival.agent) }
        arrivalTimers[arrival.agent] = timer
        reconnectHandler.postDelayed(timer, 60_000L)
    }

    private fun dismissMessageArrival(agent: String) {
        arrivalTimers.remove(agent)?.let(reconnectHandler::removeCallbacks)
        arrivalsByAgent.remove(agent)
        arrivals = arrivals - agent
    }

    private fun clearMessageArrivals() {
        arrivalTimers.values.forEach(reconnectHandler::removeCallbacks)
        arrivalTimers.clear()
        arrivalsByAgent.clear()
        arrivals = emptyMap()
        selectedArrival = null
    }

    private fun clearPromptTemplateScope() {
        promptScopeGeneration++
        pendingPromptRequests.clear()
        promptTemplates = emptyList()
        promptTemplatesLoaded = false
        promptImportState = null
        promptRequestState = PromptRequestState()
    }

    private fun promptErrorMessage(status: Int, body: String): String {
        val code = runCatching { JSONObject(body).optJSONObject("error")?.optString("code") }.getOrNull()
        return when (code) {
            "prompt_template_name_conflict" -> "A template with that name already exists."
            "prompt_template_capacity_exceeded" -> "The template limit has been reached."
            "invalid_prompt_template" -> "Enter a valid template name and body."
            "not_found" -> "That template is no longer available."
            else -> "Prompt template request failed${if (status > 0) " ($status)" else ""}."
        }
    }

    private fun parsePromptTemplateResponse(body: String): List<PromptTemplate>? = runCatching {
        val array = JSONObject(body).optJSONArray("prompt_templates") ?: return@runCatching null
        if (array.length() > MAX_PROMPT_TEMPLATES) return@runCatching null
        List(array.length()) { index ->
            val value = array.optJSONObject(index) ?: return@runCatching null
            val id = value.optString("id").takeIf { it.isNotBlank() } ?: return@runCatching null
            PromptTemplate(id = id, name = value.optString("name"), body = value.optString("body"), updatedAt = value.optString("updated_at").takeIf { it.isNotBlank() })
        }
    }.getOrNull()

    private fun parseSavedPromptTemplate(body: String): PromptTemplate? = runCatching {
        val value = JSONObject(body).optJSONObject("prompt_template") ?: return@runCatching null
        val id = value.optString("id").takeIf { it.isNotBlank() } ?: return@runCatching null
        PromptTemplate(id = id, name = value.optString("name"), body = value.optString("body"), updatedAt = value.optString("updated_at").takeIf { it.isNotBlank() })
    }.getOrNull()

    private fun requestPromptTemplates() {
        if (activeRouter.isBlank() || pendingPromptRequests.values.any { it.operation == "list" }) return
        val router = activeRouter
        val request = PendingPromptRequest("prompt-list-${UUID.randomUUID()}", "list", null, router, connectionGeneration, promptScopeGeneration)
        pendingPromptRequests[request.requestId] = request
        promptRequestState = PromptRequestState(PromptRequestStatus.LOADING)
        Thread {
            try {
                client.newBuilder().callTimeout(15, TimeUnit.SECONDS).build().newCall(Request.Builder().url("${routerHttpFor(router)}/v1/prompt-templates").get().build()).execute().use { response ->
                    val body = response.body?.string().orEmpty()
                    runOnUiThread { receivePromptTemplateResponse(request, response.code, body) }
                }
            } catch (_: Exception) {
                runOnUiThread { receivePromptTemplateFailure(request, "The router is unavailable.") }
            }
        }.start()
    }

    private fun sendPromptTemplateRequest(operation: String, templateId: String?, payload: JSONObject?, importIndex: Int? = null) {
        if (activeRouter.isBlank()) {
            promptRequestState = PromptRequestState(PromptRequestStatus.ERROR, "The router is unavailable.")
            return
        }
        val router = activeRouter
        if (pendingPromptRequests.size >= 100) {
            val oldest = pendingPromptRequests.entries.firstOrNull()?.key
            if (oldest != null) pendingPromptRequests.remove(oldest)
            promptRequestState = PromptRequestState(PromptRequestStatus.UNCONFIRMED, "An earlier template change was not confirmed.")
        }
        val request = PendingPromptRequest("prompt-$operation-${UUID.randomUUID()}", operation, templateId, router, connectionGeneration, promptScopeGeneration, importIndex)
        pendingPromptRequests[request.requestId] = request
        promptRequestState = PromptRequestState(PromptRequestStatus.SAVING)
        Thread {
            try {
                val method = when (operation) { "create" -> "POST"; "update" -> "PATCH"; else -> "DELETE" }
                val path = if (operation == "create") "/v1/prompt-templates" else "/v1/prompt-templates/${java.net.URLEncoder.encode(templateId.orEmpty(), "UTF-8").replace("+", "%20")}"
                val builder = Request.Builder().url("${routerHttpFor(router)}$path")
                if (method == "DELETE") builder.delete() else builder.method(method, (payload ?: JSONObject()).toString().toRequestBody("application/json".toMediaType()))
                client.newBuilder().callTimeout(15, TimeUnit.SECONDS).build().newCall(builder.build()).execute().use { response ->
                    val body = response.body?.string().orEmpty()
                    runOnUiThread { receivePromptTemplateResponse(request, response.code, body) }
                }
            } catch (_: Exception) {
                runOnUiThread { receivePromptTemplateFailure(request, "The router is unavailable.") }
            }
        }.start()
    }

    private fun sendPromptTemplateEditorRequest(templateId: String?, name: String, body: String) {
        val trimmedName = name.trim()
        try {
            parsePromptTemplateImport(promptTemplateExportJson(listOf(PromptTemplate(trimmedName, body))))
        } catch (error: IllegalArgumentException) {
            promptRequestState = PromptRequestState(PromptRequestStatus.ERROR, error.message ?: "Enter a valid template name and body.")
            return
        }
        sendPromptTemplateRequest(if (templateId == null) "create" else "update", templateId, JSONObject().put("name", trimmedName).put("body", body))
    }

    private fun sendPromptTemplateDeleteRequest(templateId: String) {
        sendPromptTemplateRequest("delete", templateId, null)
    }

    private fun receivePromptTemplateFailure(request: PendingPromptRequest, message: String) {
        val pending = pendingPromptRequests.remove(request.requestId)
        if (pending != request
            || request.connectionGeneration != connectionGeneration
            || request.scopeGeneration != promptScopeGeneration
            || request.router != activeRouter) return
        val importing = promptImportState
        if (importing != null) {
            promptImportState = null
            promptRequestState = PromptRequestState(PromptRequestStatus.ERROR, "Imported ${importing.completed} of ${importing.templates.size} templates before the failure.")
            promptScopeGeneration++
            requestPromptTemplates()
        } else {
            promptRequestState = PromptRequestState(PromptRequestStatus.UNCONFIRMED, message)
        }
    }

    private fun receivePromptTemplateResponse(request: PendingPromptRequest, status: Int, body: String) {
        val pending = pendingPromptRequests.remove(request.requestId)
        if (pending != request
            || request.connectionGeneration != connectionGeneration
            || request.scopeGeneration != promptScopeGeneration
            || request.router != activeRouter) return
        if (status !in 200..299) {
            promptRequestState = PromptRequestState(PromptRequestStatus.ERROR, promptErrorMessage(status, body))
            if (promptImportState != null) {
                val completed = promptImportState?.completed ?: 0
                val total = promptImportState?.templates?.size ?: 0
                promptImportState = null
                promptRequestState = PromptRequestState(PromptRequestStatus.ERROR, "Imported $completed of $total templates before the failure.")
                requestPromptTemplates()
            }
            return
        }
        if (request.operation == "list") {
            val parsed = parsePromptTemplateResponse(body)
            if (parsed == null) {
                promptRequestState = PromptRequestState(PromptRequestStatus.ERROR, "The router returned an invalid template list.")
                return
            }
            promptTemplates = parsed
            promptTemplatesLoaded = true
            val importing = promptImportState
            if (importing?.phase == PromptImportPhase.REFRESHING) {
                val conflicts = promptTemplateImportConflicts(importing.templates, parsed)
                val newlyConflicted = conflicts.any { it.id !in importing.confirmedConflictIds }
                promptImportState = if (newlyConflicted) importing.copy(phase = PromptImportPhase.CONFIRM, conflicts = conflicts, confirmedConflictIds = emptySet()) else importing.copy(phase = PromptImportPhase.RUNNING, conflicts = conflicts, index = 0, completed = 0)
                if (!newlyConflicted) sendNextPromptTemplateImport()
            }
            promptRequestState = PromptRequestState(PromptRequestStatus.IDLE)
            return
        }
        val saved = if (request.operation == "delete") null else parseSavedPromptTemplate(body)
        if (request.operation != "delete" && saved == null) {
            promptRequestState = PromptRequestState(PromptRequestStatus.ERROR, "The router returned an invalid template.")
            return
        }
        if (saved != null) promptTemplates = (promptTemplates.filterNot { it.id == saved.id } + saved)
        if (request.importIndex != null) {
            val importing = promptImportState ?: return
            if (importing.phase != PromptImportPhase.RUNNING || importing.index != request.importIndex) return
            promptImportState = importing.copy(index = importing.index + 1, completed = importing.completed + 1)
            sendNextPromptTemplateImport()
        } else {
            promptScopeGeneration++
            promptRequestSerial = completePromptTemplateSave(
                promptRequestSerial,
                request.operation,
                ::requestPromptTemplates,
            ) { promptRequestState = it }
        }
    }

    private fun beginPromptTemplateImport(value: String) {
        try {
            val imported = parsePromptTemplateImport(value)
            if (imported.isEmpty()) {
                promptRequestState = PromptRequestState(PromptRequestStatus.IDLE, "The file contains no templates.")
                return
            }
            val newCount = imported.count { incoming -> promptTemplates.none { it.id == incoming.id } }
            if (promptTemplates.size + newCount > MAX_PROMPT_TEMPLATES) throw IllegalArgumentException("Import would exceed the 100-template limit.")
            promptImportState = PromptImportState(imported, promptTemplateImportConflicts(imported, promptTemplates))
            promptRequestState = PromptRequestState()
        } catch (error: IllegalArgumentException) {
            promptImportState = null
            promptRequestState = PromptRequestState(PromptRequestStatus.ERROR, error.message ?: "Unable to read the prompt template file.")
        }
    }

    private fun confirmPromptTemplateImport() {
        val importing = promptImportState ?: return
        if (importing.phase != PromptImportPhase.CONFIRM) return
        promptImportState = importing.copy(phase = PromptImportPhase.REFRESHING, confirmedConflictIds = importing.conflicts.mapNotNull { it.id }.toSet())
        promptScopeGeneration++
        requestPromptTemplates()
    }

    private fun cancelPromptTemplateImport() {
        promptImportState = null
        promptRequestState = PromptRequestState()
    }

    private fun sendNextPromptTemplateImport() {
        val importing = promptImportState ?: return
        if (importing.phase != PromptImportPhase.RUNNING) return
        val template = importing.templates.getOrNull(importing.index) ?: run {
            promptImportState = null
            promptRequestSerial++
            promptRequestState = PromptRequestState(PromptRequestStatus.SUCCESS, "Imported ${importing.completed} template${if (importing.completed == 1) "" else "s"}.", promptRequestSerial)
            promptScopeGeneration++
            requestPromptTemplates()
            return
        }
        val existing = template.id?.let { id -> promptTemplates.firstOrNull { it.id == id } }
        if (existing != null && template.updatedAt == null) {
            promptImportState = importing.copy(index = importing.index + 1, completed = importing.completed + 1)
            sendNextPromptTemplateImport()
            return
        }
        val payload = JSONObject().put("name", template.name).put("body", template.body)
        if (existing == null && template.id != null) payload.put("id", template.id)
        if (template.updatedAt != null) payload.put("updated_at", template.updatedAt)
        sendPromptTemplateRequest(if (existing == null) "create" else "update", existing?.id, payload, importing.index)
    }

    private fun importPromptTemplateFile(uri: Uri) = Thread {
        try {
            val value = contentResolver.openInputStream(uri)?.use { input ->
                val output = ByteArrayOutputStream()
                val buffer = ByteArray(16 * 1024)
                var total = 0
                while (true) {
                    val count = input.read(buffer)
                    if (count < 0) break
                    total += count
                    if (total > 8 * 1024 * 1024) throw IllegalArgumentException("The prompt template file is too large.")
                    output.write(buffer, 0, count)
                }
                output.toByteArray().toString(Charsets.UTF_8)
            } ?: throw IllegalArgumentException("Choose a valid prompt template JSON file.")
            runOnUiThread { beginPromptTemplateImport(value) }
        } catch (error: Exception) {
            runOnUiThread { promptRequestState = PromptRequestState(PromptRequestStatus.ERROR, error.message ?: "Choose a valid prompt template JSON file.") }
        }
    }.start()

    private fun clearReminderScope(message: String? = null) {
        val hadPendingCrud = pendingReminderCrud.isNotEmpty()
        val hadPendingLocks = pendingReminderLocks.isNotEmpty()
        pendingReminderCrud.values.forEach { pending -> controlHandler.removeCallbacksAndMessages(pending.requestId) }
        pendingReminderCrud.clear()
        pendingReminderLocks.values.forEach { pending -> controlHandler.removeCallbacksAndMessages(pending.controlId) }
        pendingReminderLocks.clear()
        reminderRefreshSerial++
        reminders = emptyList()
        reminderLocks = emptyMap()
        if (message != null && (hadPendingCrud || hadPendingLocks)) {
            reminderUiState = ReminderUiState(ReminderStatus.UNCONFIRMED, message)
        } else if (message == null) {
            reminderUiState = ReminderUiState(ReminderStatus.IDLE)
        }
    }

    private fun fetchReminders(generation: Int = connectionGeneration) {
        val router = activeRouter
        if (router.isBlank()) return
        val refreshSerial = ++reminderRefreshSerial
        reminderUiState = ReminderUiState(ReminderStatus.LOADING)
        Thread {
            try {
                client.newBuilder().callTimeout(15, TimeUnit.SECONDS).build()
                    .newCall(Request.Builder().url("${routerHttpFor(router)}/v1/reminders").get().build()).execute().use { response ->
                    val body = response.body?.string().orEmpty()
                    runOnUiThread {
                        if (!reminderListResponseIsCurrent(refreshSerial, reminderRefreshSerial, generation, connectionGeneration, router, activeRouter)) return@runOnUiThread
                        if (!response.isSuccessful) {
                            reminderUiState = ReminderUiState(ReminderStatus.REJECTED, "Reminder refresh failed (${response.code}).")
                            return@runOnUiThread
                        }
                        val parsed = parseReminderListResponse(body)
                        if (parsed == null) {
                            reminderUiState = ReminderUiState(ReminderStatus.REJECTED, "The router returned an invalid reminder list.")
                            return@runOnUiThread
                        }
                        reminders = parsed
                        reminderUiState = ReminderUiState(ReminderStatus.IDLE)
                    }
                }
            } catch (_: Exception) {
                runOnUiThread {
                    if (reminderListResponseIsCurrent(refreshSerial, reminderRefreshSerial, generation, connectionGeneration, router, activeRouter)) {
                        reminderUiState = ReminderUiState(ReminderStatus.REJECTED, "Reminder refresh failed; router unavailable.")
                    }
                }
            }
        }.start()
    }

    private fun sendReminderCreate(
        peer: String,
        body: String,
        subject: String,
        delayMinutes: Long,
        recurring: Boolean,
        lockAfterCreate: Boolean,
    ) {
        val validation = validateReminderCreate(body, delayMinutes)
        if (validation != null) {
            reminderUiState = ReminderUiState(ReminderStatus.REJECTED, validation, "invalid_reminder")
            return
        }
        if (activeRouter.isBlank() || leaseId.isBlank()) {
            reminderUiState = ReminderUiState(ReminderStatus.REJECTED, "The router is not connected.", "router_disconnected")
            return
        }
        if (pendingReminderCrud.size >= 100) {
            val oldest = pendingReminderCrud.entries.firstOrNull()?.key
            if (oldest != null) pendingReminderCrud.remove(oldest)
            reminderUiState = ReminderUiState(ReminderStatus.UNCONFIRMED, "An earlier reminder change was not confirmed.")
        }
        val requestId = "reminder-crud-${UUID.randomUUID()}"
        val id = "reminder-${UUID.randomUUID()}"
        val router = activeRouter
        val targetInstanceId = peers.firstOrNull { it.id == peer }?.instanceId
        val request = PendingReminderCrud(requestId, "create", peer, router, connectionGeneration, lockAfterCreate, id, id, targetInstanceId)
        pendingReminderCrud[requestId] = request
        val payload = buildReminderCreatePayload(agentId, peer, body.trim(), subject.trim(), delayMinutes, recurring, id)
        reminderUiState = ReminderUiState(ReminderStatus.SAVING)
        executeReminderCrudRequest(request, "POST", "/v1/reminders", payload)
    }

    private fun sendReminderUpdate(peer: String, reminder: ReminderSummary, body: String, subject: String) {
        val normalizedBody = body.trim()
        val normalizedSubject = subject.trim().takeIf { it.isNotEmpty() }
        val validation = validateReminderUpdate(reminder, normalizedBody, normalizedSubject)
        if (validation != null) {
            reminderUiState = ReminderUiState(ReminderStatus.REJECTED, validation, "invalid_reminder")
            return
        }
        sendReminderCrudRequest(
            peer = peer,
            operation = "update",
            reminderId = reminder.id,
            method = "PATCH",
            path = "/v1/reminders/${encodeReminderId(reminder.id)}",
            payload = buildReminderUpdatePayload(normalizedBody, normalizedSubject),
        )
    }

    private fun sendReminderDelete(peer: String, reminder: ReminderSummary) {
        sendReminderCrudRequest(
            peer = peer,
            operation = "delete",
            reminderId = reminder.id,
            method = "DELETE",
            path = "/v1/reminders/${encodeReminderId(reminder.id)}",
        )
    }

    private fun sendReminderCrudRequest(
        peer: String,
        operation: String,
        reminderId: String,
        method: String,
        path: String,
        payload: JSONObject? = null,
    ) {
        if (activeRouter.isBlank() || leaseId.isBlank()) {
            reminderUiState = ReminderUiState(ReminderStatus.REJECTED, "The router is not connected.", "router_disconnected")
            return
        }
        if (pendingReminderCrud.size >= 100) {
            val oldest = pendingReminderCrud.entries.firstOrNull()?.key
            if (oldest != null) {
                pendingReminderCrud.remove(oldest)?.let { controlHandler.removeCallbacksAndMessages(it.requestId) }
            }
            reminderUiState = ReminderUiState(ReminderStatus.UNCONFIRMED, "An earlier reminder change was not confirmed.")
        }
        val router = activeRouter
        val targetInstanceId = peers.firstOrNull { it.id == peer }?.instanceId
        val requestId = "reminder-crud-${UUID.randomUUID()}"
        val request = PendingReminderCrud(requestId, operation, peer, router, connectionGeneration, false, reminderId, reminderId, targetInstanceId)
        pendingReminderCrud[requestId] = request
        reminderUiState = ReminderUiState(ReminderStatus.SAVING)
        executeReminderCrudRequest(request, method, path, payload)
    }

    private fun executeReminderCrudRequest(request: PendingReminderCrud, method: String, path: String, payload: JSONObject?) {
        val timeout = 15L
        Thread {
            try {
                val builder = Request.Builder().url("${routerHttpFor(request.router)}$path")
                if (method == "POST" || method == "PATCH") {
                    builder.method(method, (payload ?: JSONObject()).toString().toRequestBody("application/json".toMediaType()))
                } else {
                    builder.delete()
                }
                client.newBuilder().callTimeout(timeout, TimeUnit.SECONDS).build().newCall(builder.build()).execute().use { response ->
                    val responseBody = response.body?.string().orEmpty()
                    runOnUiThread { receiveReminderCrudResponse(request, response.code, responseBody) }
                }
            } catch (_: Exception) {
                runOnUiThread { receiveReminderCrudFailure(request) }
            }
        }.start()
        controlHandler.postDelayed({
            val expired = pendingReminderCrud.remove(request.requestId)
            if (expired != null && matchesCurrentReminderCrudScope(expired)) {
                reminderUiState = reminderCrudTimeoutState(expired.operation)
            }
        }, timeout * 1_000L)
    }

    private fun receiveReminderCrudFailure(request: PendingReminderCrud) {
        val pending = pendingReminderCrud.remove(request.requestId)
        controlHandler.removeCallbacksAndMessages(request.requestId)
        if (pending != request || !matchesCurrentReminderCrudScope(request)) return
        reminderUiState = ReminderUiState(ReminderStatus.UNCONFIRMED, "The reminder request was not confirmed.", "router_disconnected")
    }

    private fun receiveReminderCrudResponse(request: PendingReminderCrud, status: Int, body: String) {
        val pending = pendingReminderCrud.remove(request.requestId)
        controlHandler.removeCallbacksAndMessages(request.requestId)
        if (pending != request || !matchesCurrentReminderCrudScope(request)) return
        if (!reminderCrudResponseAccepted(request.operation, status)) {
            reminderUiState = reminderCrudErrorStateFromBody(status, body)
            return
        }
        if (request.operation == "delete") {
            reminderUiState = ReminderUiState(ReminderStatus.ACCEPTED, "Reminder deleted.")
            fetchReminders(request.connectionGeneration)
            return
        }
        val saved = parseSavedReminder(body)
        if (saved == null) {
            reminderUiState = ReminderUiState(ReminderStatus.REJECTED, "The router returned an invalid reminder.", "invalid_response")
            return
        }
        reminderUiState = if (request.lockAfterCreate) {
            ReminderUiState(ReminderStatus.LOCK_PENDING, "Reminder created. Setting lock…")
        } else {
            ReminderUiState(ReminderStatus.ACCEPTED, if (request.operation == "update") "Reminder updated." else "Reminder created.")
        }
        fetchReminders(request.connectionGeneration)
        if (request.lockAfterCreate) sendReminderLock(request.peer, saved.id, afterCreate = true, expectedTargetInstanceId = request.targetInstanceId)
    }

    private fun matchesCurrentReminderCrudScope(request: PendingReminderCrud): Boolean =
        matchesPendingReminderCrud(request, activeRouter, connectionGeneration, selectedPeer, peers.firstOrNull { it.id == request.peer }?.instanceId)

    private fun sendReminderLock(peer: String, reminderId: String, action: String = "set", afterCreate: Boolean = false, expectedTargetInstanceId: String? = null) {
        val targetInstance = peers.firstOrNull { it.id == peer }?.instanceId
        if (targetInstance == null || (expectedTargetInstanceId != null && targetInstance != expectedTargetInstanceId) || leaseId.isBlank()) {
            reminderUiState = if (afterCreate) {
                ReminderUiState(ReminderStatus.CREATED_LOCK_FAILED, "Reminder created, but lock setup failed: that agent is not ready.", "target_not_connected")
            } else {
                ReminderUiState(ReminderStatus.REJECTED, "That agent is not ready for lock changes.", "target_not_connected")
            }
            return
        }
        if (pendingReminderLocks.size >= 100) {
            val oldest = pendingReminderLocks.entries.firstOrNull()?.key
            if (oldest != null) {
                val evicted = pendingReminderLocks.remove(oldest)
                if (evicted != null) controlHandler.removeCallbacksAndMessages(evicted.controlId)
            }
            reminderUiState = ReminderUiState(ReminderStatus.UNCONFIRMED, "An earlier reminder lock change was not confirmed.")
        }
        val controlId = "reminder-lock-${UUID.randomUUID()}"
        val requestFrameId = "reminder-lock-control-${UUID.randomUUID()}"
        val pending = PendingReminderLock(controlId, requestFrameId, peer, targetInstance, reminderId, action, activeRouter, connectionGeneration, afterCreate)
        pendingReminderLocks[controlId] = pending
        try {
            val sent = socket?.send(buildReminderLockControlFrame(requestFrameId, agentId, instanceId, leaseId, peer, targetInstance, controlId, reminderId, action).toString()) ?: false
            if (!sent) throw IllegalStateException()
        } catch (_: Exception) {
            pendingReminderLocks.remove(controlId)
            reminderUiState = if (afterCreate) {
                ReminderUiState(ReminderStatus.CREATED_LOCK_FAILED, "Reminder created, but lock setup failed: the router is not connected.", "router_disconnected")
            } else {
                ReminderUiState(ReminderStatus.REJECTED, "The router is not connected.", "router_disconnected")
            }
            return
        }
        reminderUiState = ReminderUiState(ReminderStatus.LOCK_PENDING, if (action == "set") "Setting reminder lock…" else "Clearing reminder lock…")
        controlHandler.postDelayed({
            val expired = pendingReminderLocks.remove(controlId)
            if (expired != null) {
                reminderUiState = if (expired.afterCreate) {
                    ReminderUiState(ReminderStatus.TIMEOUT, "Reminder created, but lock setup timed out.", "control_timeout")
                } else {
                    ReminderUiState(ReminderStatus.TIMEOUT, "The reminder lock change timed out.", "control_timeout")
                }
            }
        }, 30_000L)
    }

    private fun receiveReminderLockResult(frame: JSONObject) {
        val result = parseReminderLockResult(frame) ?: return
        val pending = pendingReminderLocks[result.controlId] ?: return
        if (!matchesPendingReminderLock(pending, result, agentId, leaseId)) return
        pendingReminderLocks.remove(result.controlId)
        controlHandler.removeCallbacksAndMessages(result.controlId)
        reminderUiState = if (result.accepted) {
            reminderLocks = reminderLocks + ("${pending.peer}:${pending.reminderId}" to (pending.action == "set"))
            ReminderUiState(ReminderStatus.ACCEPTED, if (pending.afterCreate) "Reminder created and locked." else if (pending.action == "set") "Reminder lock set." else "Reminder lock cleared.")
        } else if (pending.afterCreate) {
            ReminderUiState(ReminderStatus.CREATED_LOCK_FAILED, "Reminder created, but lock setup failed.", result.errorCode)
        } else {
            ReminderUiState(ReminderStatus.REJECTED, "Reminder lock change rejected.", result.errorCode)
        }
    }

    private fun settleReminderLockError(error: ParsedRouterError) {
        val pending = pendingReminderLocks.values.firstOrNull { it.requestFrameId == error.requestId } ?: return
        pendingReminderLocks.remove(pending.controlId)
        controlHandler.removeCallbacksAndMessages(pending.controlId)
        val timeout = error.code == "reminder_lock_control_expired"
        reminderUiState = if (pending.afterCreate && timeout) {
            ReminderUiState(ReminderStatus.TIMEOUT, "Reminder created, but lock setup timed out.", error.code)
        } else if (pending.afterCreate) {
            ReminderUiState(ReminderStatus.CREATED_LOCK_FAILED, "Reminder created, but lock setup failed.", error.code)
        } else if (timeout) {
            ReminderUiState(ReminderStatus.TIMEOUT, "The reminder lock change timed out.", error.code)
        } else {
            ReminderUiState(ReminderStatus.REJECTED, error.message ?: "Reminder lock change rejected.", error.code)
        }
    }

    private fun discoverPeers(generation: Int) {
        val router = activeRouter
        val request = historyLock.withLock {
            if (generation != connectionGeneration || router != activeRouter) return@withLock null
            peerStateEventFence.beginDiscovery(generation)
        } ?: return
        Thread {
            try {
                var agentsArray: JSONArray? = null
                client.newCall(Request.Builder().url("${routerHttpFor(router)}/v1/agents").build()).execute().use { response ->
                    if (response.isSuccessful && generation == connectionGeneration) {
                        agentsArray = JSONObject(response.body?.string().orEmpty()).optJSONArray("agents")
                    }
                }
                val agents = agentsArray ?: return@Thread
                if (!discoveryResponseWithinBound(agents)) return@Thread

                val managedList = runCatching {
                    client.newCall(Request.Builder().url("${routerHttpFor(router)}/v1/managed-agents").build()).execute().use { response ->
                        if (!response.isSuccessful || generation != connectionGeneration) return@use emptyList<JSONObject>()
                        val root = JSONObject(response.body?.string().orEmpty())
                        val managedAgents = root.optJSONArray("agents") ?: return@use emptyList<JSONObject>()
                        buildList {
                            for (i in 0 until managedAgents.length()) {
                                managedAgents.optJSONObject(i)?.let { add(it) }
                            }
                        }
                    }
                }.getOrNull().orEmpty()

                val childrenByManagedId = buildMap<String, List<SubagentSummary>> {
                    for (managed in managedList) {
                        val mId = managed.optString("agent_id").takeIf { it.isNotBlank() } ?: continue
                        runCatching {
                            client.newCall(Request.Builder().url("${routerHttpFor(router)}/v1/managed-agents/$mId/children").build()).execute().use { response ->
                                if (response.isSuccessful && generation == connectionGeneration) {
                                    val root = JSONObject(response.body?.string().orEmpty())
                                    val subs = normalizedSubagentSummaries(root.optJSONArray("children"))
                                    put(mId, subs)
                                }
                            }
                        }
                    }
                }

                runOnUiThread {
                    var retryPeer: String? = null
                    historyLock.withLock {
                        if (generation != connectionGeneration || router != activeRouter) return@withLock
                        val existingById = peers.associateBy(PeerStatus::id)
                        val discovered = buildList {
                            for (index in 0 until agents.length()) {
                                val agent = agents.optJSONObject(index) ?: continue
                                val peer = agent.optString("agent_id")
                                    .takeIf { it.isNotBlank() && it != agentId && AGENT_ID.matches(it) } ?: continue
                                val instance = agent.optString("instance_id").takeIf { it.isNotBlank() }
                                val observedAt = historyObservationTimestamp(agent.optString("connected_at"))
                                if (instance != null) add(DiscoveredPeer(peer, agent, instance, observedAt))
                            }
                        }
                        val discoveredById = discovered.associateBy(DiscoveredPeer::id)
                        val nextPeers = buildList {
                            peers.forEach { current ->
                                val candidate = discoveredById[current.id]
                                if (candidate != null && peerStateEventFence.acceptDiscovery(
                                        request,
                                        candidate.id,
                                        candidate.instanceId,
                                        candidate.observedAtMs,
                                    )
                                ) {
                                    val updated = peerStatusFromJson(candidate.id, candidate.source, current)
                                    if (current.instanceId != null && current.instanceId != updated.instanceId) {
                                        settleOutboundForPeer(candidate.id, updated.instanceId)
                                    }
                                    add(updated)
                                    if (shouldRetryHistoryAfterPeerResolution(
                                            activeHydrationFence,
                                            selectedPeer,
                                            candidate.id,
                                            current.instanceId,
                                            updated.instanceId,
                                            connectionGeneration,
                                        )
                                    ) retryPeer = candidate.id
                                } else if (candidate != null || !peerStateEventFence.isCurrentForPeer(request, current.id)) {
                                    // Keep the current projection when this candidate was
                                    // rejected as stale. A newer peer event also remains
                                    // authoritative when discovery omits that peer.
                                    add(current)
                                }
                            }
                            discovered.forEach { candidate ->
                                if (candidate.id in existingById) return@forEach
                                if (peerStateEventFence.acceptDiscovery(
                                        request,
                                        candidate.id,
                                        candidate.instanceId,
                                        candidate.observedAtMs,
                                    )
                                ) add(peerStatusFromJson(candidate.id, candidate.source))
                            }
                        }

                        val mergedWithManaged = mergeManagedPeers(nextPeers, managedList, agentId, childrenByManagedId)
                        peers = mergedWithManaged.distinctBy(PeerStatus::id).sortedBy(PeerStatus::id)
                    }
                    retryPeer?.takeIf { leaseId.isNotBlank() }?.let { hydrateHistory(it, generation) }
                }
            } catch (_: Exception) {
                // The active connection remains usable when discovery is unavailable.
            }
        }.start()
    }

    private fun receiveAgentActivity(frame: JSONObject) = historyLock.withLock {
        val peer = frame.optString("agent_id").takeIf { it.isNotBlank() && it != agentId } ?: return@withLock
        val incomingInstance = frame.optString("instance_id").takeIf { it.isNotBlank() }
        val event = peerStateEventFence.observeEvent(
            connectionGeneration,
            peer,
            incomingInstance,
            historyObservationTimestamp(frame.optString("generated_at")),
        ) ?: return@withLock
        if (!peerStateEventFence.isNewest(event)) return@withLock
        val current = peers.firstOrNull { it.id == peer }
        if (current?.instanceId != null && incomingInstance != null && current.instanceId != incomingInstance) {
            settleOutboundForPeer(peer, incomingInstance)
        }
        val updated = peerStatusFromJson(peer, frame, current)
        peers = if (current == null) (peers + updated).sortedBy { it.id } else peers.map { if (it.id == peer) updated else it }
        if (shouldRetryHistoryAfterPeerResolution(
                activeHydrationFence,
                selectedPeer,
                peer,
                current?.instanceId,
                updated.instanceId,
                connectionGeneration,
            ) && leaseId.isNotBlank()
        ) {
            hydrateHistory(peer)
        }
    }

    private fun receiveModelCatalog(frame: JSONObject) = historyLock.withLock {
        val parsed = parseModelCatalogFrame(frame) ?: return@withLock
        val event = peerStateEventFence.observeEvent(
            connectionGeneration,
            parsed.agentId,
            parsed.instanceId,
            historyObservationTimestamp(frame.optString("generated_at")),
        ) ?: return@withLock
        if (!peerStateEventFence.isNewest(event)) return@withLock
        val current = peers.firstOrNull { it.id == parsed.agentId }
        val replaced = current?.instanceId != null && current.instanceId != parsed.instanceId
        if (replaced) settleOutboundForPeer(parsed.agentId, parsed.instanceId)
        val base = if (replaced) PeerStatus(parsed.agentId) else current ?: PeerStatus(parsed.agentId)
        val next = base.copy(
            instanceId = parsed.instanceId,
            modelCatalog = ModelCatalog(if (parsed.models.isEmpty()) CatalogStatus.EMPTY else CatalogStatus.READY, parsed.models, parsed.totalCount, parsed.truncated, parsed.current),
        )
        peers = if (current == null) (peers + next).sortedBy { it.id } else peers.map { if (it.id == parsed.agentId) next else it }
        if (shouldRetryHistoryAfterPeerResolution(
                activeHydrationFence,
                selectedPeer,
                parsed.agentId,
                current?.instanceId,
                next.instanceId,
                connectionGeneration,
            ) && leaseId.isNotBlank()
        ) {
            hydrateHistory(parsed.agentId)
        }
    }

    private fun receiveGoalState(frame: JSONObject) = historyLock.withLock {
        val parsed = parseGoalStateFrame(frame) ?: return@withLock
        val event = peerStateEventFence.observeEvent(
            connectionGeneration,
            parsed.agentId,
            parsed.instanceId,
            historyObservationTimestamp(frame.optString("generated_at")),
        ) ?: return@withLock
        if (!peerStateEventFence.isNewest(event)) return@withLock
        val current = peers.firstOrNull { it.id == parsed.agentId }
        val replaced = current?.instanceId != null && current.instanceId != parsed.instanceId
        if (replaced) settleOutboundForPeer(parsed.agentId, parsed.instanceId)
        val base = if (replaced) PeerStatus(parsed.agentId) else current ?: PeerStatus(parsed.agentId)
        val next = base.copy(instanceId = parsed.instanceId, goalState = parsed.state)
        peers = if (current == null) (peers + next).sortedBy { it.id } else peers.map { if (it.id == parsed.agentId) next else it }
        if (shouldRetryHistoryAfterPeerResolution(
                activeHydrationFence,
                selectedPeer,
                parsed.agentId,
                current?.instanceId,
                next.instanceId,
                connectionGeneration,
            ) && leaseId.isNotBlank()
        ) {
            hydrateHistory(parsed.agentId)
        }
    }

    private fun receiveRouterError(frame: JSONObject) {
        val error = parseRouterError(frame) ?: return
        val model = pendingModelControls.entries.firstOrNull { it.value.requestFrameId == error.requestId }?.let { pendingModelControls.remove(it.key) }
        val goal = pendingGoalControls.entries.firstOrNull { it.value.requestFrameId == error.requestId }?.let { pendingGoalControls.remove(it.key) }
        if (model != null) {
            controlHandler.removeCallbacksAndMessages(model.controlId)
            modelControlState = ControlState(controlStatusForError(error.code), model.controlId, model.targetAgentId, model.targetInstanceId, error.code, error.message)
            return
        }
        if (goal != null) {
            controlHandler.removeCallbacksAndMessages(goal.controlId)
            goalControlState = ControlState(controlStatusForError(error.code), goal.controlId, goal.targetAgentId, goal.targetInstanceId, error.code, error.message)
            return
        }
        if (pendingReminderLocks.any { it.value.requestFrameId == error.requestId }) {
            settleReminderLockError(error)
            return
        }
        connection = "Router error: ${error.code ?: "unknown"}"
    }

    private fun receiveModelControlResult(frame: JSONObject) {
        if (!capabilities.modelControlV1) return
        val result = parseModelControlResult(frame) ?: return
        val pending = pendingModelControls[result.controlId] ?: return
        if (!matchesPending(pending, result.agentId, result.instanceId, result.controlId, result.sourceAgentId)
            || result.sourceAgentId != agentId || result.leaseId != leaseId
            || pending.router != activeRouter || pending.connectionGeneration != connectionGeneration || pending.leaseId != leaseId) return
        val current = peers.firstOrNull { it.id == result.agentId } ?: return
        if (current.instanceId != result.instanceId) return
        pendingModelControls.remove(result.controlId)
        controlHandler.removeCallbacksAndMessages(result.controlId)
        if (result.accepted) {
            peers = peers.map { if (it.id == result.agentId) it.copy(modelCatalog = it.modelCatalog.copy(current = result.currentModel)) else it }
            modelControlState = ControlState(ControlStatus.ACCEPTED, result.controlId, result.agentId, result.instanceId)
        } else {
            modelControlState = ControlState(ControlStatus.REJECTED, result.controlId, result.agentId, result.instanceId, result.errorCode)
        }
    }

    private fun receiveGoalControlResult(frame: JSONObject) {
        if (!capabilities.goalControlV1) return
        val result = parseGoalControlResult(frame) ?: return
        val pending = pendingGoalControls[result.controlId] ?: return
        if (!matchesPending(pending, result.agentId, result.instanceId, result.controlId, result.sourceAgentId)
            || result.sourceAgentId != agentId || result.leaseId != leaseId
            || pending.router != activeRouter || pending.connectionGeneration != connectionGeneration || pending.leaseId != leaseId) return
        val current = peers.firstOrNull { it.id == result.agentId } ?: return
        if (current.instanceId != result.instanceId) return
        pendingGoalControls.remove(result.controlId)
        controlHandler.removeCallbacksAndMessages(result.controlId)
        if (result.accepted) {
            peers = peers.map { if (it.id == result.agentId) it.copy(goalState = result.state) else it }
            goalControlState = ControlState(ControlStatus.ACCEPTED, result.controlId, result.agentId, result.instanceId)
        } else {
            goalControlState = ControlState(ControlStatus.REJECTED, result.controlId, result.agentId, result.instanceId, result.errorCode)
        }
    }

    private fun sendModelControl(peer: String, model: ModelSummary) {
        if (!capabilities.modelControlV1) { modelControlState = ControlState(ControlStatus.REJECTED, message = "Model controls are unavailable."); return }
        val target = peers.firstOrNull { it.id == peer }
        val targetInstance = target?.instanceId
        if (target == null || targetInstance == null || leaseId.isBlank()) { modelControlState = ControlState(ControlStatus.REJECTED, message = "The agent is not ready for model changes."); return }
        val controlId = "model-control-${UUID.randomUUID()}"
        val requestId = "model-control-${UUID.randomUUID()}"
        val pending = PendingControl(controlId, requestId, peer, targetInstance, activeRouter, connectionGeneration, leaseId)
        try {
            socket?.send(buildModelControlFrame(requestId, agentId, instanceId, leaseId, peer, targetInstance, controlId, model).toString()) ?: throw IllegalStateException()
        } catch (_: Exception) { modelControlState = ControlState(ControlStatus.REJECTED, controlId, peer, targetInstance, message = "The router is not connected."); return }
        pendingModelControls[controlId] = pending
        modelControlState = ControlState(ControlStatus.PENDING, controlId, peer, targetInstance)
        controlHandler.postDelayed({ if (pendingModelControls.remove(controlId) != null) modelControlState = ControlState(ControlStatus.UNCONFIRMED, controlId, peer, targetInstance, "control_timeout", "The model change timed out.") }, capabilities.modelPendingTtlMs + 2_000L)
    }

    private fun sendGoalControl(peer: String, draft: GoalEditorDraft) {
        if (!capabilities.goalControlV1) { goalControlState = ControlState(ControlStatus.REJECTED, message = "Goal controls are unavailable."); return }
        val target = peers.firstOrNull { it.id == peer }
        val targetInstance = target?.instanceId
        if (target == null || targetInstance == null || leaseId.isBlank()) { goalControlState = ControlState(ControlStatus.REJECTED, message = "The agent is not ready for goal changes."); return }
        val validation = validateGoalDraft(draft)
        if (validation != null) { goalControlState = ControlState(ControlStatus.REJECTED, errorCode = validation, message = "Turn on the goal and enter a description before saving."); return }
        val controlId = "goal-control-${UUID.randomUUID()}"
        val requestId = "goal-control-${UUID.randomUUID()}"
        val pending = PendingControl(controlId, requestId, peer, targetInstance, activeRouter, connectionGeneration, leaseId)
        try {
            socket?.send(buildGoalControlFrame(requestId, agentId, instanceId, leaseId, peer, targetInstance, controlId, draft).toString()) ?: throw IllegalStateException()
        } catch (_: Exception) { goalControlState = ControlState(ControlStatus.REJECTED, controlId, peer, targetInstance, message = "The router is not connected."); return }
        pendingGoalControls[controlId] = pending
        goalControlState = ControlState(ControlStatus.PENDING, controlId, peer, targetInstance)
        controlHandler.postDelayed({ if (pendingGoalControls.remove(controlId) != null) goalControlState = ControlState(ControlStatus.UNCONFIRMED, controlId, peer, targetInstance, "control_timeout", "The goal change timed out.") }, 35_000L)
    }

    private fun receiveConversationMessage(frame: JSONObject) {
        val validated = validateLiveMessageFrame(frame, LiveMessageFrameKind.CONVERSATION_MESSAGE) ?: return
        val message = chatMessage(validated) ?: return
        val peer = conversationPeerForMessage(message, agentId) ?: return
        val result = appendMessage(peer, message)
        when (result.status) {
            HistoryAppendStatus.SAVED -> if (message.direction == Direction.INCOMING && result.shouldNotify) postInboundNotification(peer, message.body)
            HistoryAppendStatus.IGNORED -> Unit
            // appendMessage keeps the bounded in-memory row and schedules hydration;
            // the error banner tells the user why the recovery is in progress.
            HistoryAppendStatus.PERSISTENCE_FAILED -> Unit
        }
    }

    private fun settleDelivery(webSocket: WebSocket, frame: JSONObject, callbackGeneration: Int) {
        val correlatedAck = usesCorrelatedAck(negotiatedAckConfirmation)
        val captured = deliveryCoordinator.capture(callbackGeneration) { connection ->
            captureDeliveryForSettlement(
                frame = frame,
                connection = connection,
                parseMessage = { validated, router, owner -> chatMessage(validated, router, owner) },
                targetInstanceId = { peer -> peers.firstOrNull { it.id == peer }?.instanceId },
                correlatedAck = correlatedAck,
            )
        } ?: return
        val settled = deliveryCoordinator.settle(captured, ::appendCapturedDelivery) ?: return
        settled.notifications.forEach { notification ->
            runCatching { postInboundNotification(notification.peer, notification.message.body) }
        }

        // The coordinator has released the state lock. Use only the immutable
        // connection identity returned by it for both receipts.
        val items = settled.deliveryItems
        if (items.isEmpty() || settled.messages.size != items.size) return
        val first = items.first()
        val ledgerKeys = if (settled.correlatedAck) items.mapIndexed { index, item ->
            DeliveryLedgerKey(
                router = settled.connection.router,
                ownerAgentId = settled.connection.agentId,
                ownerInstanceId = settled.connection.instanceId,
                peer = conversationPeerForMessage(settled.messages[index], settled.connection.agentId) ?: return,
                messageId = item.messageId,
                messageIncarnationId = item.messageIncarnationId,
            )
        } else emptyList()
        val injection = deliveryReceiptFrame(
            type = "injection_requested",
            agentId = settled.connection.agentId,
            instanceId = settled.connection.instanceId,
            leaseId = settled.connection.leaseId,
            deliveryItems = items,
            correlatedAck = settled.correlatedAck,
        )
        runCatching { webSocket.send(injection.toString()) }

        val ackRequestId = UUID.randomUUID().toString()
        val ack = deliveryReceiptFrame(
            type = "ack",
            agentId = settled.connection.agentId,
            instanceId = settled.connection.instanceId,
            leaseId = settled.connection.leaseId,
            deliveryItems = items,
            correlatedAck = settled.correlatedAck,
            frameId = ackRequestId,
        )
        historyLock.withLock {
            if (settled.connection == DeliveryConnectionIdentity(
                    activeRouter,
                    agentId,
                    instanceId,
                    connectionGeneration,
                    leaseId,
                ) && settled.correlatedAck
            ) {
                pendingDeliveryAcknowledgements[ackRequestId] = PendingDeliveryAcknowledgement(
                    ledgerKeys,
                    first.attemptNumber,
                    settled.connection,
                )
            }
        }
        val ackSent = runCatching { webSocket.send(ack.toString()) }.getOrDefault(false)
        if (!ackSent) {
            if (settled.correlatedAck) historyLock.withLock { pendingDeliveryAcknowledgements.remove(ackRequestId) }
        } else if (!settled.correlatedAck) {
            historyLock.withLock {
                if (settled.connection == DeliveryConnectionIdentity(
                        activeRouter,
                        agentId,
                        instanceId,
                        connectionGeneration,
                        leaseId,
                    )
                ) {
                    ledgerKeys.forEach { deliveryReplayLedger.markAcknowledged(it) }
                }
            }
        }
    }

    private fun receiveDeliveryAcknowledged(frame: JSONObject, callbackGeneration: Int) {
        val acknowledgement = parseDeliveryAcknowledgedFrame(frame) ?: return
        historyLock.withLock {
            val pending = pendingDeliveryAcknowledgements[acknowledgement.requestId] ?: return@withLock
            if (callbackGeneration != connectionGeneration
                || pending.connection != DeliveryConnectionIdentity(activeRouter, agentId, instanceId, connectionGeneration, leaseId)
                || acknowledgement.agentId != agentId
                || acknowledgement.instanceId != instanceId
                || acknowledgement.leaseId != leaseId
                || acknowledgement.messageId != pending.keys.firstOrNull()?.messageId
                || acknowledgement.messageIncarnationId != pending.keys.firstOrNull()?.messageIncarnationId
                || acknowledgement.attemptNumber != pending.attemptNumber
            ) return@withLock
            if (pending.keys.all { key -> deliveryReplayLedger.markAcknowledged(key) }) {
                pendingDeliveryAcknowledgements.remove(acknowledgement.requestId)
            }
        }
    }

    private fun historyFor(peer: String): List<ChatMessage> = historyLock.withLock {
        historyForUnlocked(peer)
    }

    private fun historyForUnlocked(peer: String): List<ChatMessage> =
        historyForUnlocked(activeRouter, agentId, instanceId, peer)

    private fun historyForUnlocked(
        router: String,
        owner: String,
        ownerInstance: String,
        peer: String,
    ): List<ChatMessage> {
        if (peer.isBlank()) return emptyList()
        val preferences = prefs()
        val currentKey = historyKey(router, owner, ownerInstance, peer)
        val previousKey = previousRouterHistoryKey(router, owner, peer)
        val legacyKey = legacyRouterHistoryKey(router, peer)
        // Read every old scope before any key is removed. A failed write leaves the source
        // row available for the next launch and avoids silent history loss.
        val sources = listOf(
            currentKey to preferences.getString(currentKey, null),
            previousKey to preferences.getString(previousKey, null),
            legacyKey to preferences.getString(legacyKey, null),
        )
        if (sources.all { it.second == null }) return emptyList()
        val nowMs = System.currentTimeMillis()
        val parsed = sources.mapNotNull { (_, raw) ->
            raw?.let { decodeStoredHistory(it, router, owner, peer, nowMs) }
        }.fold(emptyList<ChatMessage>()) { rows, read -> mergeConversationMessages(rows, read.messages, nowMs) }
            .filter { isMessageInConversationScope(it.sender, it.recipient, owner, peer) }
        val retained = pruneConversationHistory(parsed, nowMs)
        val hasMigrationSource = sources.drop(1).any { it.second != null }
        val hasMigratedCurrent = sources.first().second != null
            && decodeStoredHistory(sources.first().second.orEmpty(), router, owner, peer, nowMs)?.migrated == true
        if (hasMigrationSource || hasMigratedCurrent || sources.first().second == null || retained != parsed) {
            val proof = persistHistoryProofUnlocked(router, owner, ownerInstance, peer, retained, nowMs)
            if (proof.committed) {
                val editor = preferences.edit()
                if (sources[1].second != null) editor.remove(previousKey)
                if (sources[2].second != null) editor.remove(legacyKey)
                editor.commit()
            }
        }
        return retained
    }

    private fun decodeStoredHistory(
        raw: String,
        router: String,
        owner: String,
        peer: String,
        migrationTimestampMs: Long,
    ): StoredHistoryRead = try {
        val array = JSONArray(raw)
        StoredHistoryRead(List(array.length()) { index ->
            val value = array.getJSONObject(index)
            val outgoing = value.optString("direction") == Direction.OUTGOING.name
            ChatMessage(
                id = value.optString("id", UUID.randomUUID().toString()),
                incarnationId = value.optString("message_incarnation_id"),
                sequence = value.optLong("sequence"),
                sender = value.optString("sender", if (outgoing) owner else peer),
                body = value.optString("body"),
                direction = if (outgoing) Direction.OUTGOING else Direction.INCOMING,
                createdAt = value.optString("created_at", Instant.ofEpochMilli(migrationTimestampMs).toString()),
                recordedAt = value.optString("recorded_at").takeIf { it.isNotBlank() },
                attachments = imageReferences(value.optJSONArray("attachments"), router),
                recipient = value.optString("recipient", if (outgoing) peer else owner),
                deliveryStatus = if (outgoing) {
                    runCatching { OutboundDeliveryStatus.valueOf(value.optString("delivery_status", OutboundDeliveryStatus.SENT.name)) }
                        .getOrDefault(OutboundDeliveryStatus.SENT)
                } else OutboundDeliveryStatus.NONE,
            )
        }, migrated = false)
    } catch (_: Exception) {
        val timestamp = Instant.ofEpochMilli(migrationTimestampMs).toString()
        val messages = if (raw.isBlank() || raw == "No messages yet") emptyList() else raw.lines()
            .filter { it.isNotBlank() }
            .map { line ->
                val outgoing = line.startsWith("You · ")
                ChatMessage(
                    id = UUID.randomUUID().toString(),
                    incarnationId = "",
                    sequence = 0,
                    sender = if (outgoing) owner else peer,
                    body = line.substringAfter(" · ", line),
                    direction = if (outgoing) Direction.OUTGOING else Direction.INCOMING,
                    createdAt = timestamp,
                    recordedAt = timestamp,
                    recipient = if (outgoing) peer else owner,
                    deliveryStatus = if (outgoing) OutboundDeliveryStatus.SENT else OutboundDeliveryStatus.NONE,
                )
            }
        StoredHistoryRead(messages, migrated = true)
    }

    private fun persistHistory(peer: String, value: List<ChatMessage>): Boolean = historyLock.withLock {
        persistHistoryUnlocked(peer, value)
    }

    private fun persistHistoryUnlocked(peer: String, value: List<ChatMessage>): Boolean =
        persistHistoryProofUnlocked(peer, value, System.currentTimeMillis()).committed

    private fun persistHistoryProofUnlocked(
        peer: String,
        value: List<ChatMessage>,
        nowMs: Long,
    ): HistoryPersistenceProof = persistHistoryProofUnlocked(
        activeRouter,
        agentId,
        instanceId,
        peer,
        value,
        nowMs,
    )

    private fun persistHistoryProofUnlocked(
        router: String,
        owner: String,
        ownerInstance: String,
        peer: String,
        value: List<ChatMessage>,
        nowMs: Long,
    ): HistoryPersistenceProof {
        if (router.isBlank() || owner.isBlank() || ownerInstance.isBlank() || peer.isBlank()
            || value.any { !isMessageInConversationScope(it.sender, it.recipient, owner, peer) }
        ) return HistoryPersistenceProof(false, emptySet())
        val retained = pruneConversationHistory(value, nowMs)
        val array = JSONArray()
        retained.forEach { message ->
            array.put(JSONObject()
                .put("id", message.id)
                .put("message_incarnation_id", message.incarnationId)
                .put("sequence", message.sequence)
                .put("sender", message.sender)
                .put("recipient", message.recipient)
                .put("body", message.body)
                .put("direction", message.direction.name)
                .put("delivery_status", message.deliveryStatus.name)
                .put("created_at", message.createdAt)
                .apply { if (message.recordedAt != null) put("recorded_at", message.recordedAt) }
                .put("attachments", JSONArray(message.attachments.map(::imageReferenceJson))))
        }
        val committed = prefs().edit()
            .putString(historyKey(router, owner, ownerInstance, peer), array.toString())
            .commit()
        return HistoryPersistenceProof(committed, retained.map(ChatMessage::key).toSet())
    }

    private fun appendCapturedDelivery(delivery: CapturedDelivery): DeliveryPersistenceProof {
        val connection = delivery.connection
        val deliveredMessages = delivery.messages
        if (deliveredMessages.any { !isInboundDeliveryForAgent(it.sender, it.recipient, connection.agentId) }
            || delivery.messagesByPeer.any { (peer, peerMessages) ->
                peerMessages.any { !isMessageInConversationScope(it.sender, it.recipient, connection.agentId, peer) }
            }
        ) return DeliveryPersistenceProof(false)
        // Peer replacement is part of the same fence as the socket identity. This
        // also rejects null-at-capture to known-instance transitions.
        if (delivery.messagesByPeer.keys.any { peer ->
                peers.firstOrNull { it.id == peer }?.instanceId != delivery.targetInstanceIdsByPeer[peer]
            }
        ) return DeliveryPersistenceProof(false)
        val nowMs = System.currentTimeMillis()
        val ledgerStatusesByKey = if (delivery.correlatedAck) deliveredMessages.associate { message ->
            message.key to deliveryReplayLedger.status(
                DeliveryLedgerKey(
                    connection.router,
                    connection.agentId,
                    connection.instanceId,
                    conversationPeerForMessage(message, connection.agentId) ?: return DeliveryPersistenceProof(false),
                    message.id,
                    message.incarnationId,
                ),
                nowMs,
            )
        } else emptyMap()
        if (deliveredMessages.any {
                !isRetainedConversationMessage(it, nowMs) && ledgerStatusesByKey[it.key] == null
            }
        ) return DeliveryPersistenceProof(false)

        var nextState = outboundState
        val retainedKeys = mutableSetOf<String>()
        val notifications = mutableListOf<DeliveredNotification>()
        delivery.messagesByPeer.forEach { (peer, peerMessages) ->
            val persistedHistory = historyForUnlocked(connection.router, connection.agentId, connection.instanceId, peer)
            val current = mergeHydrationConversationMessages(
                persisted = persistedHistory,
                inMemory = nextState.messagesByPeer[peer].orEmpty(),
                nowMs = nowMs,
            )
            val persistedKeys = persistedHistory.map(ChatMessage::key).toSet()
            val ledgerStatuses = peerMessages.associate { it.key to ledgerStatusesByKey[it.key] }
            retainedKeys += peerMessages.filter { ledgerStatuses[it.key] != null }.map(ChatMessage::key)
            val newIncomingMessages = peerMessages.filter {
                it.direction == Direction.INCOMING && it.key !in persistedKeys && ledgerStatuses[it.key] == null
            }
            val updated = peerMessages.fold(current) { rows, message ->
                mergeConversationMessages(rows, listOf(message), nowMs)
            }
            nextState = nextState.copy(messagesByPeer = nextState.messagesByPeer + (peer to updated))
            if (selectedPeer == peer) messages = updated
            if (peerMessages.all { ledgerStatuses[it.key] != null }) {
                outboundState = nextState
                return@forEach
            }
            if (!persistKnownPeers(connection.router, knownPeers(connection.router) + peer)) {
                // Keep the bounded additive state visible while the router remains the
                // source for the scheduled hydration retry. Do not acknowledge this offer.
                outboundState = nextState
                reportHistoryPersistenceFailureUnlocked(peer, connection.router, connection.generation)
                return DeliveryPersistenceProof(false)
            }
            val persisted = persistHistoryProofUnlocked(
                connection.router,
                connection.agentId,
                connection.instanceId,
                peer,
                updated,
                nowMs,
            )
            retainedKeys += persisted.retainedMessageKeys
            outboundState = nextState
            if (!persisted.committed || peerMessages.any { it.key !in persisted.retainedMessageKeys }) {
                // A failed commit or retention pass must not make any live additive
                // message disappear. Hydration will retry the complete delivery.
                reportHistoryPersistenceFailureUnlocked(peer, connection.router, connection.generation)
                return DeliveryPersistenceProof(false, retainedKeys)
            }
            peerMessages.forEach { message ->
                if (delivery.correlatedAck && ledgerStatuses[message.key] == null && !deliveryReplayLedger.markPersisted(
                        DeliveryLedgerKey(connection.router, connection.agentId, connection.instanceId, peer, message.id, message.incarnationId),
                        nowMs,
                    )
                ) {
                    reportHistoryPersistenceFailureUnlocked(peer, connection.router, connection.generation)
                    return DeliveryPersistenceProof(false, retainedKeys)
                }
                retainedKeys += message.key
            }
            if (selectedPeer == peer) historyUiState = HistoryUiState()
            notifications += newIncomingMessages.map { DeliveredNotification(peer, it) }
        }
        return DeliveryPersistenceProof(true, retainedKeys, notifications)
    }

    private fun appendMessage(peer: String, message: ChatMessage): HistoryAppendResult = historyLock.withLock {
        if (!isMessageInConversationScope(message.sender, message.recipient, agentId, peer)) {
            return@withLock HistoryAppendResult(HistoryAppendStatus.IGNORED, outboundState.messagesByPeer[peer].orEmpty())
        }
        val nowMs = System.currentTimeMillis()
        // The in-memory list can contain a row whose previous SharedPreferences commit failed.
        // Read the durable list separately so a redelivered row can notify once after recovery.
        val persisted = historyForUnlocked(activeRouter, agentId, instanceId, peer)
        val current = mergeHydrationConversationMessages(
            persisted = persisted,
            inMemory = outboundState.messagesByPeer[peer].orEmpty(),
            nowMs = nowMs,
        )
        val deliveryLedgerKey = if (message.direction == Direction.INCOMING) DeliveryLedgerKey(
            activeRouter,
            agentId,
            instanceId,
            peer,
            message.id,
            message.incarnationId,
        ) else null
        val alreadyPersisted = persisted.any { sameConversationMessageIdentity(it, message) }
            || deliveryLedgerKey?.let { deliveryReplayLedger.status(it, nowMs) != null } == true
        val pending = outboundState.pending.values.firstOrNull { it.message.id == message.id }
        val currentTarget = peers.firstOrNull { it.id == peer }?.instanceId
        if (message.direction == Direction.OUTGOING && pending != null
            && (pending.scope.router != activeRouter || pending.scope.connectionGeneration != connectionGeneration
                || pending.scope.targetInstanceId != null && pending.scope.targetInstanceId != currentTarget)) {
            return@withLock HistoryAppendResult(HistoryAppendStatus.IGNORED, current)
        }
        // Incoming history follows the same 30-day policy as settled rows. Pending
        // outgoing bubbles are the only retention exception.
        if (message.direction == Direction.INCOMING && !isRetainedConversationMessage(message, nowMs)) {
            return@withLock HistoryAppendResult(HistoryAppendStatus.IGNORED, current)
        }
        var next = outboundState.copy(messagesByPeer = outboundState.messagesByPeer + (peer to current))
        var draftClearText: String? = null
        var draftClearImages: List<PendingImage>? = null
        if (message.direction == Direction.OUTGOING) {
            val echoEvent = OutboundDeliveryEvent.HistoryEcho(
                OutboundScope(activeRouter, connectionGeneration, peer, currentTarget),
                message,
            )
            draftClearText = outboundDraftClearText(next, echoEvent)
            draftClearImages = outboundDraftClearImages(next, echoEvent)
            next = reduceOutboundDelivery(next, echoEvent)
        }
        val result = appendLiveConversationMessage(
            existing = next.messagesByPeer[peer].orEmpty(),
            message = message,
            nowMs = nowMs,
            persist = { updated ->
                if (!persistKnownPeers(knownPeers() + peer)) {
                    HistoryPersistenceProof(false, updated.map(ChatMessage::key).toSet())
                } else {
                    persistHistoryProofUnlocked(peer, updated, nowMs)
                }
            },
            alreadyPersisted = alreadyPersisted,
        )
        if (result.status == HistoryAppendStatus.IGNORED) return@withLock result
        val durableResult = if (result.status == HistoryAppendStatus.SAVED && deliveryLedgerKey != null
            && !alreadyPersisted
            && !deliveryReplayLedger.markPersisted(deliveryLedgerKey, nowMs)
        ) {
            result.copy(status = HistoryAppendStatus.PERSISTENCE_FAILED)
        } else result
        next = next.copy(messagesByPeer = next.messagesByPeer + (peer to durableResult.messages))
        if (durableResult.status == HistoryAppendStatus.SAVED) {
            outboundState = next
            if (selectedPeer == peer) {
                messages = durableResult.messages
                historyUiState = HistoryUiState()
            }
        } else {
            // Keep a retained live message in memory so a failed commit is visible.
            // The recovery hydration below is responsible for making it durable.
            outboundState = next
            if (selectedPeer == peer) messages = durableResult.messages
            reportHistoryPersistenceFailureUnlocked(peer, activeRouter, connectionGeneration)
        }
        draftClearText?.let { clearDraftIfUnchanged(activeRouter, peer, it) }
        draftClearImages?.let { clearImagesIfUnchanged(activeRouter, peer, it) }
        durableResult
    }

    private fun historyRequestIsCurrentUnlocked(request: HistoryHydrationFence): Boolean {
        val currentTargetInstance = peers.firstOrNull { it.id == request.peer }?.instanceId
        return isCurrentHistoryHydration(
            request,
            currentRouter = activeRouter,
            currentOwnerAgentId = agentId,
            currentOwnerInstanceId = instanceId,
            currentPeer = selectedPeer,
            currentTargetInstanceId = currentTargetInstance,
            currentConnectionGeneration = connectionGeneration,
            currentRequestSerial = activeHydrationFence?.requestSerial,
        )
    }

    private fun cancelHistoryRecoveryUnlocked(peer: String? = null) {
        val keys = if (peer == null) historyRecoveryTasks.keys.toList() else listOf(peer)
        keys.forEach { key -> historyRecoveryTasks.remove(key)?.let(reconnectHandler::removeCallbacks) }
        if (peer == null) historyRecoveryAttempts.clear() else historyRecoveryAttempts.remove(peer)
    }

    /** Automatic persistence recovery backs off and stops with a user-visible retry action. */
    private fun scheduleHistoryRecoveryUnlocked(peer: String, router: String, generation: Int) {
        if (peer.isBlank() || historyRecoveryTasks.containsKey(peer)) return
        val attempt = historyRecoveryAttempts[peer] ?: 0
        if (!hasHistoryRecoveryBudget(attempt)) {
            if (activeRouter == router && connectionGeneration == generation && selectedPeer == peer) {
                historyUiState = historyErrorState(HistoryFailure.PERSISTENCE_EXHAUSTED)
            }
            return
        }
        historyRecoveryAttempts[peer] = attempt + 1
        val task = Runnable {
            historyLock.withLock {
                historyRecoveryTasks.remove(peer)
                if (activeRouter == router && connectionGeneration == generation && selectedPeer == peer && leaseId.isNotBlank()) {
                    hydrateHistory(peer, generation)
                }
            }
        }
        historyRecoveryTasks[peer] = task
        reconnectHandler.postDelayed(task, historyRecoveryDelayMs(attempt))
    }

    private fun reportHistoryPersistenceFailureUnlocked(peer: String, router: String, generation: Int) {
        if (selectedPeer == peer) {
            val state = historyErrorState(HistoryFailure.PERSISTENCE)
            runOnUiThread {
                historyLock.withLock {
                    if (activeRouter == router && connectionGeneration == generation && selectedPeer == peer) {
                        historyUiState = state
                    }
                }
            }
        }
        scheduleHistoryRecoveryUnlocked(peer, router, generation)
    }

    private fun publishHistoryFailure(request: HistoryHydrationFence, failure: HistoryFailure, statusCode: Int? = null) {
        val state = historyErrorState(failure, statusCode)
        runOnUiThread {
            historyLock.withLock {
                if (historyRequestIsCurrentUnlocked(request)) historyUiState = state
            }
        }
    }

    private fun retryHistory() {
        val peer = historyLock.withLock {
            cancelHistoryRecoveryUnlocked(selectedPeer)
            selectedPeer.takeIf { it.isNotBlank() && leaseId.isNotBlank() }
        }
        peer?.let(::hydrateHistory)
    }

    private fun hydrateActivityHistory(peer: String) {
        val router = activeRouter ?: return
        if (peer.isBlank()) return
        lifecycleScope.launch(Dispatchers.IO) {
            val encodedPeer = URLEncoder.encode(peer, "UTF-8").replace("+", "%20")
            val urlString = "${routerHttpFor(router)}/v1/history?agent_id=$encodedPeer&view=complete&limit=100"
            try {
                val conn = (URL(urlString).openConnection() as HttpURLConnection).apply {
                    connectTimeout = 10_000
                    readTimeout = 10_000
                    requestMethod = "GET"
                    setRequestProperty("Accept", "application/json")
                }
                if (conn.responseCode in 200..299) {
                    val raw = conn.inputStream.bufferedReader().use { it.readText() }
                    val json = JSONObject(raw)
                    val entries = json.optJSONArray("entries") ?: JSONArray()
                    val parsed = mutableListOf<AgentTimelineEvent>()
                    for (i in 0 until entries.length()) {
                        val entry = entries.optJSONObject(i) ?: continue
                        val presentation = entry.optJSONObject("presentation")
                        val kind = presentation?.optString("kind").orEmpty().ifBlank { entry.optString("kind") }
                        if (kind == "message") continue
                        val eId = entry.optString("id").takeIf { it.isNotBlank() } ?: UUID.randomUUID().toString()
                        val at = entry.optString("at").takeIf { it.isNotBlank() } ?: entry.optString("recorded_at")
                        val toolName = presentation?.optString("tool_name").takeIf { !it.isNullOrBlank() }
                            ?: entry.optString("tool_name").takeIf { it.isNotBlank() }
                        val toolInputObj = entry.optJSONObject("tool_input")
                        val summary = presentation?.optString("summary").takeIf { !it.isNullOrBlank() }
                            ?: entry.optString("message").takeIf { it.isNotBlank() }
                            ?: toolName
                            ?: kind
                        val outcome = presentation?.optString("outcome").takeIf { !it.isNullOrBlank() }
                            ?: entry.optString("outcome").takeIf { it.isNotBlank() }
                            ?: "success"
                        val toolInput = toolInputObj?.toString(2)
                        val output = entry.optString("message_content").takeIf { it.isNotBlank() }
                            ?: presentation?.optJSONObject("error")?.optString("message")
                            ?: entry.optString("message").takeIf { it != summary }

                        parsed.add(
                            AgentTimelineEvent(
                                id = eId,
                                agentId = peer,
                                timestamp = at,
                                kind = kind,
                                toolName = toolName,
                                summary = summary,
                                outcome = outcome,
                                toolInput = toolInput,
                                output = output
                            )
                        )
                    }
                    withContext(Dispatchers.Main) {
                        val existing = liveEventsByAgent[peer].orEmpty()
                        val combined = (existing + parsed)
                            .associateBy { it.id }
                            .values
                            .sortedBy { it.timestamp }
                            .takeLast(200)
                        liveEventsByAgent = liveEventsByAgent + (peer to combined)
                    }
                }
                conn.disconnect()
            } catch (e: Exception) {
                // Ignore transient network errors during history hydration
            }
        }
    }

    private fun hydrateHistory(peer: String, generation: Int = connectionGeneration) {
        hydrateActivityHistory(peer)
        val request = historyLock.withLock {
            // The default generation is read before the lock. Recheck it, the selected
            // peer, and the owner scope after waiting so a stale caller cannot publish LOADING.
            if (!isCurrentHistoryHydrationStart(
                    requestedPeer = peer,
                    requestedGeneration = generation,
                    currentRouter = activeRouter,
                    currentOwnerAgentId = agentId,
                    currentOwnerInstanceId = instanceId,
                    currentSelectedPeer = selectedPeer,
                    currentConnectionGeneration = connectionGeneration,
                ) || leaseId.isBlank()
            ) return@withLock null
            val fence = HistoryHydrationFence(
                router = activeRouter,
                ownerAgentId = agentId,
                ownerInstanceId = instanceId,
                peer = peer,
                targetInstanceId = peers.firstOrNull { it.id == peer }?.instanceId,
                connectionGeneration = generation,
                requestSerial = ++hydrationRequestSerial,
            )
            activeHydrationFence = fence
            historyUiState = HistoryUiState(HistoryStatus.LOADING)
            fence
        } ?: return
        Thread {
            try {
                val url = conversationHistoryUrl(
                    routerHttpFor(request.router),
                    request.ownerAgentId,
                    request.peer,
                )
                historyHttpClient(client).newCall(Request.Builder().url(url).build()).execute().use { response ->
                    if (!response.isSuccessful) {
                        publishHistoryFailure(request, HistoryFailure.HTTP, response.code)
                        return@use
                    }
                    val responseBody = readHistoryResponseBody(response.body)
                    val parsed = responseBody?.let { parseHistoryResponse(it, request.ownerAgentId, request.peer) }
                    if (parsed == null) {
                        publishHistoryFailure(request, HistoryFailure.INVALID_RESPONSE)
                        return@use
                    }
                    val hydrated = mutableListOf<ChatMessage>()
                    var malformedMessage = false
                    for (index in 0 until parsed.messages.length()) {
                        val message = runCatching {
                            chatMessage(parsed.messages.getJSONObject(index), request.router, request.ownerAgentId)
                        }.getOrNull()
                        if (message == null) {
                            malformedMessage = true
                            break
                        }
                        hydrated += message
                    }
                    if (malformedMessage) {
                        publishHistoryFailure(request, HistoryFailure.INVALID_RESPONSE)
                        return@use
                    }
                    runOnUiThread {
                        historyLock.withLock {
                            if (!historyRequestIsCurrentUnlocked(request)) return@withLock
                            try {
                                val nowMs = System.currentTimeMillis()
                                val persistedHistory = historyForUnlocked(
                                    request.router,
                                    request.ownerAgentId,
                                    request.ownerInstanceId,
                                    request.peer,
                                )
                                val inMemory = outboundState.messagesByPeer[request.peer].orEmpty()
                                val recovery = (outboundState.pending.values + outboundState.settled.values)
                                    .filter { operation ->
                                        operation.scope.router == request.router
                                            && operation.scope.connectionGeneration == request.connectionGeneration
                                            && operation.scope.peer == request.peer
                                    }
                                    .map(OutboundOperation::message)
                                // Start with the in-memory copy. This preserves unsaved additive rows
                                // and uncertain sends while an older router snapshot is in flight.
                                val existing = mergeHydrationConversationMessages(
                                    persisted = persistedHistory,
                                    inMemory = inMemory,
                                    recovery = recovery,
                                    nowMs = nowMs,
                                )
                                val merged = reconcileConversationMessages(
                                    existing,
                                    hydrated,
                                    parsed.highWaterSequence,
                                    nowMs,
                                )
                                var next = outboundState.copy(messagesByPeer = outboundState.messagesByPeer + (request.peer to merged))
                                val draftClearTexts = mutableListOf<String>()
                                val draftClearImages = mutableListOf<List<PendingImage>>()
                                hydrated.filter { it.direction == Direction.OUTGOING }.forEach { echo ->
                                    val echoEvent = OutboundDeliveryEvent.HistoryEcho(
                                        OutboundScope(request.router, request.connectionGeneration, request.peer, peers.firstOrNull { it.id == request.peer }?.instanceId),
                                        echo,
                                    )
                                    outboundDraftClearText(next, echoEvent)?.let(draftClearTexts::add)
                                    outboundDraftClearImages(next, echoEvent)?.let(draftClearImages::add)
                                    next = reduceOutboundDelivery(next, echoEvent)
                                }
                                val finalMessages = next.messagesByPeer[request.peer].orEmpty()
                                val persisted = persistHistoryProofUnlocked(
                                    request.router,
                                    request.ownerAgentId,
                                    request.ownerInstanceId,
                                    request.peer,
                                    finalMessages,
                                    nowMs,
                                )
                                if (!persisted.committed) {
                                    reportHistoryPersistenceFailureUnlocked(request.peer, request.router, request.connectionGeneration)
                                    return@withLock
                                }
                                outboundState = next
                                messages = finalMessages
                                draftClearTexts.forEach { sentText ->
                                    clearDraftIfUnchanged(request.router, request.peer, sentText)
                                }
                                draftClearImages.forEach { sentImages ->
                                    clearImagesIfUnchanged(request.router, request.peer, sentImages)
                                }
                                cancelHistoryRecoveryUnlocked(request.peer)
                                historyUiState = HistoryUiState()
                            } catch (_: Exception) {
                                reportHistoryPersistenceFailureUnlocked(request.peer, request.router, request.connectionGeneration)
                            }
                        }
                    }
                }
            } catch (error: Exception) {
                publishHistoryFailure(
                    request,
                    if (error is java.io.InterruptedIOException) HistoryFailure.TIMEOUT else HistoryFailure.EXCEPTION,
                )
            }
        }.start()
    }

    private fun chatMessage(
        value: JSONObject,
        router: String = activeRouter,
        localAgentId: String = agentId,
    ): ChatMessage? {
        val validated = validateLiveMessagePayload(value, LiveMessageFrameKind.CONVERSATION_MESSAGE) ?: return null
        return chatMessage(validated, router, localAgentId)
    }

    private fun chatMessage(
        validated: ValidatedLiveMessage,
        router: String = activeRouter,
        localAgentId: String = agentId,
    ): ChatMessage {
        val value = validated.payload
        val sender = value.getString("from")
        val recipient = value.getString("to")
        val direction = when {
            sender == localAgentId || sender == "wolfgang" -> Direction.OUTGOING
            recipient == localAgentId || recipient == "wolfgang" -> Direction.INCOMING
            else -> Direction.INCOMING
        }
        return ChatMessage(
            id = value.getString("id"),
            incarnationId = validated.incarnationId,
            sequence = validated.sequence,
            sender = sender,
            body = value.getString("body"),
            direction = direction,
            createdAt = value.getString("created_at"),
            recordedAt = validated.recordedAt,
            attachments = imageReferences(value.optJSONArray("attachments"), router),
            recipient = recipient,
            deliveryStatus = if (direction == Direction.OUTGOING) OutboundDeliveryStatus.SENT else OutboundDeliveryStatus.NONE,
        )
    }

    private fun imageReferences(values: JSONArray?, router: String = activeRouter): List<ImageReference> = if (values == null) emptyList() else List(values.length()) { index ->
        val value = values.getJSONObject(index)
        val rawUrl = value.optString("url")
        ImageReference(
            id = value.optString("id"),
            url = if (rawUrl.startsWith("/")) "${routerHttpFor(router)}$rawUrl" else rawUrl,
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

    private fun selectPeer(peer: String) = historyLock.withLock {
        if (peer.isBlank()) return@withLock
        if (selectedPeer != peer) {
            clearReminderLocksForPeer(selectedPeer)
            clearReminderCrudForPeer(selectedPeer)
            activeHydrationFence = null
            cancelHistoryRecoveryUnlocked()
            historyUiState = HistoryUiState()
        }
        agentFilter = ""
        selectedPeer = peer
        draftValue = restoredComposerValue(draftFor(peer))
        images = emptyList()
        messages = historyFor(peer)
        outboundState = outboundState.copy(messagesByPeer = outboundState.messagesByPeer + (peer to messages))
        persistKnownPeers(knownPeers() + peer)
        if (leaseId.isNotBlank()) {
            hydrateHistory(peer)
            hydrateActivityHistory(peer)
        }
    }

    private fun clearReminderLocksForPeer(peer: String) {
        if (peer.isBlank()) return
        pendingReminderLocks.entries.removeIf { (_, pending) ->
            if (pending.peer == peer) {
                controlHandler.removeCallbacksAndMessages(pending.controlId)
                true
            } else false
        }
        reminderLocks = reminderLocks.filterKeys { !it.startsWith("$peer:") }
    }

    private fun clearReminderCrudForPeer(peer: String) {
        if (peer.isBlank()) return
        pendingReminderCrud.entries.removeIf { (_, pending) ->
            if (pending.peer == peer) {
                controlHandler.removeCallbacksAndMessages(pending.requestId)
                true
            } else false
        }
    }

    private fun returnHome() {
        clearReminderLocksForPeer(selectedPeer)
        clearReminderCrudForPeer(selectedPeer)
        activeHydrationFence = null
        historyLock.withLock { cancelHistoryRecoveryUnlocked() }
        historyUiState = HistoryUiState()
        selectedPeer = ""
        messages = emptyList()
        images = emptyList()
        draftValue = TextFieldValue()
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
                        connection = error.message ?: "Image could not be attached"
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
        val text = draftValue.text
        val sentImages = images
        if (recipient.isBlank() || (text.isBlank() && sentImages.isEmpty())) return
        val messageId = UUID.randomUUID().toString()
        val createdAt = Instant.now().truncatedTo(ChronoUnit.MILLIS).toString()
        val localAttachments = sentImages.map { image ->
            ImageReference(
                id = "",
                url = image.dataUrl,
                mimeType = "image/jpeg",
                sizeBytes = image.bytes.size,
                name = image.name,
            )
        }
        val message = ChatMessage(
            id = messageId,
            incarnationId = "",
            sequence = 0L,
            sender = agentId,
            recipient = recipient,
            body = pendingMessageDisplayBody(text, sentImages),
            direction = Direction.OUTGOING,
            createdAt = createdAt,
            attachments = emptyList(),
            deliveryStatus = OutboundDeliveryStatus.PENDING,
        )
        val scope = OutboundScope(
            router = router,
            connectionGeneration = connectionGeneration,
            peer = recipient,
            targetInstanceId = peers.firstOrNull { it.id == recipient }?.instanceId,
        )
        val operation = OutboundOperation(
            localId = messageId,
            requestId = messageId,
            frameId = null,
            scope = scope,
            message = message,
            requestBody = text,
            requestAttachments = localAttachments,
            sentImages = sentImages,
            deadlineAtMs = System.currentTimeMillis() + OUTBOUND_DELIVERY_TIMEOUT_MS,
        )
        var optimisticPersisted = false
        historyLock.withLock {
            val current = outboundState.messagesByPeer[recipient] ?: historyForUnlocked(recipient)
            val result = persistOptimisticSend(
                outboundState.copy(messagesByPeer = outboundState.messagesByPeer + (recipient to current)),
                operation,
            ) { next ->
                persistKnownPeers(knownPeers() + recipient)
                    && persistHistoryUnlocked(recipient, next.messagesByPeer[recipient].orEmpty())
            }
            if (!result.persisted) {
                // Keep the draft and images untouched. HTTP starts only after this branch.
                connection = "Message could not be saved locally."
                return@withLock
            }
            val next = result.state
            outboundState.pending.keys
                .filterNot { next.pending.containsKey(it) }
                .forEach { controlHandler.removeCallbacksAndMessages(it) }
            outboundState = next
            if (selectedPeer == recipient) messages = next.messagesByPeer[recipient].orEmpty()
            optimisticPersisted = true
        }
        if (!optimisticPersisted) return
        controlHandler.postDelayed({
            applyOutboundEvent(OutboundDeliveryEvent.Timeout(messageId, scope, System.currentTimeMillis()))
        }, OUTBOUND_DELIVERY_TIMEOUT_MS)

        val attachments = JSONArray(sentImages.map { image ->
            JSONObject().put("type", "image").put("data_url", image.dataUrl).put("name", image.name)
        })
        val requestBody = JSONObject()
            .put("id", messageId)
            .put("from", agentId)
            .put("to", recipient)
            .put("body", text)
            .put("created_at", createdAt)
            .put("attachments", attachments)
            .toString()
        Thread {
            try {
                client.newBuilder()
                    .retryOnConnectionFailure(false)
                    .followRedirects(false)
                    .followSslRedirects(false)
                    .callTimeout(OUTBOUND_DELIVERY_TIMEOUT_MS, TimeUnit.MILLISECONDS)
                    .build()
                    .newCall(
                        Request.Builder().url("${routerHttpFor(router)}/v1/messages")
                            .post(requestBody.toRequestBody("application/json".toMediaType()))
                            .build(),
                    ).execute().use { response ->
                        val responseBody = response.body?.string().orEmpty()
                        if (response.isSuccessful) {
                            val accepted = parseOutboundResponseMessage(responseBody, operation)
                            runOnUiThread {
                                if (accepted == null) {
                                    applyOutboundEvent(OutboundDeliveryEvent.Uncertain(
                                        messageId,
                                        operation.requestId,
                                        scope,
                                        "Message delivery was not confirmed; the router may have accepted it.",
                                    ))
                                } else {
                                    // A late success may be rejected after a reconnect, peer switch,
                                    // or history echo. Only the exact pending operation can clear its draft.
                                    val applied = applyOutboundEvent(OutboundDeliveryEvent.Accepted(messageId, operation.requestId, scope, accepted))
                                    if (!applied) return@runOnUiThread
                                    drafts.clearIfUnchanged(router, recipient, text)
                                    if (activeRouter == router && selectedPeer == recipient && draftValue.text == text) draftValue = TextFieldValue()
                                    clearImagesIfUnchanged(router, recipient, sentImages)
                                    if (activeRouter == router) refreshPeers()
                                }
                            }
                        } else {
                            runOnUiThread {
                                applyOutboundEvent(OutboundDeliveryEvent.Rejected(
                                    messageId,
                                    operation.requestId,
                                    scope,
                                    "Message not sent (${response.code}).",
                                ))
                            }
                        }
                    }
            } catch (_: Exception) {
                runOnUiThread {
                    applyOutboundEvent(OutboundDeliveryEvent.Uncertain(
                        messageId,
                        operation.requestId,
                        scope,
                        "Message delivery was not confirmed; the router may have accepted it.",
                    ))
                }
            }
        }.start()
    }

    private fun parseOutboundResponseMessage(body: String, operation: OutboundOperation): ChatMessage? = runCatching {
        val root = JSONObject(body)
        val value = root.optJSONObject("conversation_message") ?: root.optJSONObject("message") ?: return@runCatching null
        val parsed = chatMessage(value, operation.scope.router) ?: return@runCatching null
        parsed.takeIf { matchesOutboundEnvelope(operation, it) }
    }.getOrNull()

    private fun applyOutboundEvent(event: OutboundDeliveryEvent): Boolean = historyLock.withLock {
        val previous = outboundState
        val next = reduceOutboundDelivery(previous, event)
        if (next == previous) return@withLock false
        val accepted = event is OutboundDeliveryEvent.Accepted
        val callbackId = when (event) {
            is OutboundDeliveryEvent.Accepted -> event.localId
            is OutboundDeliveryEvent.Rejected -> event.localId
            is OutboundDeliveryEvent.Uncertain -> event.localId
            is OutboundDeliveryEvent.Timeout -> event.localId
            is OutboundDeliveryEvent.HistoryEcho, OutboundDeliveryEvent.ConnectionChanged -> null
        }
        callbackId?.let(controlHandler::removeCallbacksAndMessages)
        outboundState = next
        next.messagesByPeer.keys
            .filter { next.messagesByPeer[it] != previous.messagesByPeer[it] }
            .forEach { peer -> persistHistoryUnlocked(peer, next.messagesByPeer[peer].orEmpty()) }
        if (selectedPeer.isNotBlank()) messages = next.messagesByPeer[selectedPeer] ?: messages
        next.error?.let { connection = it }
        accepted
    }

    private fun settleOutboundOnConnectionChange(capturedRouter: String = activeRouter) = historyLock.withLock {
        val previous = outboundState
        val next = reduceOutboundDelivery(previous, OutboundDeliveryEvent.ConnectionChanged)
        if (next == previous) return@withLock
        previous.pending.keys.forEach { controlHandler.removeCallbacksAndMessages(it) }
        outboundState = next
        next.messagesByPeer.keys
            .filter { next.messagesByPeer[it] != previous.messagesByPeer[it] }
            .forEach { peer ->
                // The caller captures the router before any scope transition.
                persistHistoryProofUnlocked(capturedRouter, agentId, instanceId, peer, next.messagesByPeer[peer].orEmpty(), System.currentTimeMillis())
            }
        if (selectedPeer.isNotBlank()) messages = next.messagesByPeer[selectedPeer] ?: messages
        next.error?.let { connection = it }
    }

    /** Settle old pending sends and persist them before changing activeRouter. */
    private fun settleAndClearOutboundForRouterSwitch(oldRouter: String): Boolean = historyLock.withLock {
        val result = settleOutboundForRouterSwitch(outboundState, oldRouter) { router, peer, value ->
            val proof = persistHistoryProofUnlocked(router, agentId, instanceId, peer, value, System.currentTimeMillis())
            proof.committed && value.all { it.key in proof.retainedMessageKeys }
        }
        if (!result.persisted) {
            connection = "Pending messages could not be saved locally."
            return@withLock false
        }
        outboundState = result.state
        true
    }

    /** Settle and persist the old owner before changing the local identity. */
    private fun settleAndClearOutboundForIdentityChange(
        oldRouter: String,
        oldOwner: String,
        oldOwnerInstance: String,
    ): Boolean = historyLock.withLock {
        val result = settleOutboundForIdentityChange(outboundState, oldRouter, oldOwner, oldOwnerInstance) { router, owner, ownerInstance, peer, value ->
            val proof = persistHistoryProofUnlocked(router, owner, ownerInstance, peer, value, System.currentTimeMillis())
            proof.committed && value.all { it.key in proof.retainedMessageKeys }
        }
        if (!result.persisted) {
            connection = "Pending messages could not be saved locally."
            return@withLock false
        }
        outboundState = result.state
        true
    }

    private fun switchActiveRouter(nextRouter: String): Boolean {
        val oldRouter = activeRouter
        if (oldRouter == nextRouter) return true
        // Keep activeRouter unchanged until old pending state has its old scope.
        if (!settleAndClearOutboundForRouterSwitch(oldRouter)) return false
        returnHome()
        cancelConnection()
        historyLock.withLock {
            activeRouter = nextRouter
            activeHydrationFence = null
            outboundState = OutboundReducerState()
            peers = emptyList()
        }
        return true
    }

    private fun settleOutboundForPeer(peer: String, targetInstanceId: String?) = historyLock.withLock {
        val previous = outboundState
        var next = previous
        previous.pending.values.filter { it.scope.peer == peer }.forEach { pending ->
            if (pending.scope.targetInstanceId != targetInstanceId) {
                next = reduceOutboundDelivery(next, OutboundDeliveryEvent.Uncertain(
                    pending.localId,
                    pending.requestId,
                    pending.scope,
                    "Message delivery was not confirmed; the agent connection changed.",
                ))
            }
        }
        if (next == previous) return@withLock
        previous.pending.values.filter { it.scope.peer == peer }.forEach { controlHandler.removeCallbacksAndMessages(it.localId) }
        outboundState = next
        persistHistoryUnlocked(peer, next.messagesByPeer[peer].orEmpty())
        if (selectedPeer == peer) messages = next.messagesByPeer[peer].orEmpty()
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
        val oldRouter = activeRouter
        val oldOwner = agentId
        val oldOwnerInstance = instanceId
        // The old connection and history scope remain authoritative until all pending
        // rows are settled and persisted. Identity mutation happens only afterward.
        if (!settleAndClearOutboundForIdentityChange(oldRouter, oldOwner, oldOwnerInstance)) return
        cancelConnection()
        returnHome()
        if (!prefs().edit().putString("agent_id", trimmed).commit()) {
            connection = "Username could not be saved locally."
            return
        }
        historyLock.withLock {
            agentId = trimmed
            activeHydrationFence = null
            outboundState = OutboundReducerState()
            messages = emptyList()
        }
        showProfileEditor = false
        connect()
    }

    private fun saveRouter(candidate: String, alias: String): Boolean {
        val previousEndpoint = editingRouterEndpoint
        val normalizedEndpoint = normalizeRouterEndpointCandidate(candidate)
        if (normalizedEndpoint == null) {
            routerError = routerEndpointValidationMessage(candidate) ?: "Enter a valid router URL."
            return false
        }
        val currentRouters = routers()
        val updatedConfigs = upsertRouterConfig(currentRouters, candidate, alias, previousEndpoint)
        val existingScheme = currentRouters.firstOrNull { it.identity == previousEndpoint || it.endpoint == previousEndpoint }?.scheme
        val savedScheme = if (candidate.trim().contains("://")) routerSchemeForCandidate(candidate) ?: return false else existingScheme ?: "http"
        val savedConfig = updatedConfigs.firstOrNull { it.identity == "$savedScheme://$normalizedEndpoint" }
        if (savedConfig == null) {
            routerError = "Enter a valid router URL."
            return false
        }
        val endpoint = savedConfig.identity
        val nextActiveRouter = activeRouterAfterRouterSave(activeRouter, previousEndpoint, endpoint)
        val saved = prefs().edit()
            .putStringSet("router_endpoints", routerRecordsForPersistence(updatedConfigs, BuildConfig.DEBUG))
            .commit()
        if (!saved) {
            routerError = "Router settings could not be saved."
            return false
        }
        if (nextActiveRouter != activeRouter && !switchActiveRouter(nextActiveRouter)) return false
        prefs().edit().putString("active_router", nextActiveRouter).commit()
        editingRouterEndpoint = null
        routerRevision++
        routerError = ""
        showRouterManager = false
        if (nextActiveRouter != activeRouter) return true
        if (socket == null && nextActiveRouter.isNotBlank()) connect()
        return true
    }

    private fun switchRouter(router: String) {
        val config = routers().firstOrNull { it.identity == router }
        if (config == null) {
            routerError = "Choose a saved router configuration."
            return
        }
        if (!routerConfigAllowed(config)) {
            routerError = routerEndpointValidationMessage(config.url, allowCleartextLoopback = BuildConfig.DEBUG)
                ?: "Release builds require an HTTPS router URL."
            showRouterManager = true
            return
        }
        val nextActiveRouter = config.identity
        if (nextActiveRouter == activeRouter) {
            showRouterManager = false
            return
        }
        if (!switchActiveRouter(nextActiveRouter)) return
        prefs().edit().putString("active_router", nextActiveRouter).commit()
        showRouterManager = false
        connect()
    }

    private fun removeRouter(router: String) {
        val remaining = routers().filterNot { it.identity == router }
        val wasActive = router == activeRouter
        val next = if (wasActive) {
            remaining.firstOrNull { routerConfigAllowed(it) }?.identity.orEmpty()
        } else activeRouter
        if (wasActive && !switchActiveRouter(next)) return
        val editor = prefs().edit()
        prefs().all.keys.filter {
            it == knownPeersKeyFor(router) || it.startsWith("history:$router:") || it.startsWith("draft:$router:") || it == draftPeersKey(router) || it.startsWith("conversation_id:$router:")
        }.forEach(editor::remove)
        editor.putStringSet("router_endpoints", remaining.map { it.persist() }.toSet())
        if (next.isBlank()) editor.remove("active_router") else editor.putString("active_router", next)
        editor.commit()
        routerRevision++
        routerError = ""
        if (editingRouterEndpoint == router) editingRouterEndpoint = null
        if (wasActive) {
            if (next.isNotBlank()) connect() else connection = "Add a router to connect"
        }
    }

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
                        agentId = agentId,
                        activeRouterDisplayName = routers().firstOrNull { it.identity == activeRouter }?.displayName ?: activeRouter,
                        peers = peers,
                        agentFilter = agentFilter,
                        onAgentFilter = { agentFilter = it },
                        onPeer = ::selectPeer,
                        arrivals = arrivals,
                        onArrival = { selectedArrival = it },
                        onDismissArrival = ::dismissMessageArrival,
                        onRefresh = ::refreshPeers,
                        onEditProfile = { showProfileEditor = true },
                        onManageRouters = { showRouterManager = true },
                    )
                    else -> {
                        BackHandler(onBack = ::returnHome)
                        Conversation(
                            peers.firstOrNull { it.id == selectedPeer } ?: PeerStatus(selectedPeer),
                            messages,
                            liveEventsByAgent[selectedPeer].orEmpty(),
                            images,
                            draftValue,
                            ::updateDraft,
                            ::addImages,
                            { index -> images = images.filterIndexed { current, _ -> current != index } },
                            ::send,
                            ::returnHome,
                            arrivals[selectedPeer],
                            { selectedArrival = it },
                            ::dismissMessageArrival,
                            capabilities,
                            historyUiState,
                            ::retryHistory,
                            modelControlState,
                            goalControlState,
                            reminders,
                            reminderUiState,
                            pendingReminderCrud.values.mapNotNull { it.reminderId }.toSet(),
                            pendingReminderLocks.values.map { "${it.peer}:${it.reminderId}" }.toSet(),
                            reminderLocks,
                            { fetchReminders() },
                            ::sendReminderCreate,
                            ::sendReminderUpdate,
                            ::sendReminderDelete,
                            { peer, reminderId, action -> sendReminderLock(peer, reminderId, action) },
                            ::sendModelControl,
                            ::sendGoalControl,
                            promptTemplates,
                            promptTemplatesLoaded,
                            promptRequestState,
                            promptImportState,
                            ::requestPromptTemplates,
                            ::sendPromptTemplateEditorRequest,
                            ::sendPromptTemplateDeleteRequest,
                            ::beginPromptTemplateImport,
                            ::confirmPromptTemplateImport,
                            ::cancelPromptTemplateImport,
                            ::importPromptTemplateFile,
                            { promptRequestState = PromptRequestState(PromptRequestStatus.ERROR, "Unable to export prompt templates.") },
                            imageLoader,
                            routerHttp(),
                        )
                    }
                }
                if (showProfileEditor) UsernameDialog(agentId, ::saveAgentId, { showProfileEditor = false })
                selectedArrival?.let { arrival ->
                    MessageArrivalDialog(arrival, { selectedArrival = null }, { dismissMessageArrival(arrival.agent); selectedArrival = null })
                }
            }
        }
    }
}

@Composable
private fun PeerHome(
    connection: String,
    agentId: String,
    activeRouterDisplayName: String,
    peers: List<PeerStatus>,
    arrivals: Map<String, MessageArrival>,
    onArrival: (MessageArrival) -> Unit,
    onDismissArrival: (String) -> Unit,
    agentFilter: String,
    onAgentFilter: (String) -> Unit,
    onPeer: (String) -> Unit,
    onRefresh: () -> Unit,
    onEditProfile: () -> Unit,
    onManageRouters: () -> Unit,
) {
    val filteredPeers = visibleConnectedPeers(peers, agentFilter)
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
                    Text(connectedStatusDetail(agentId, activeRouterDisplayName), style = MaterialTheme.typography.bodySmall, color = TextMuted)
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
                    PeerRow(peer, arrivals[peer.id], onClick = { onPeer(peer.id) }, onArrival = onArrival, onDismissArrival = onDismissArrival)
                }
            }
        }
    }
}

@Composable
private fun PeerRow(
    peer: PeerStatus,
    arrival: MessageArrival?,
    onClick: () -> Unit,
    onArrival: (MessageArrival) -> Unit,
    onDismissArrival: (String) -> Unit,
) {
    val peerId = peer.id
    val orbit = activityOrbitFor(peer)
    OrbitingBorder(
        active = orbit != null,
        orbitColor = orbit?.color ?: Teal,
        label = "peer-card-${peer.id}",
        modifier = Modifier.fillMaxWidth(),
        cornerRadius = 18.dp,
    ) {
        Card(
            modifier = Modifier.fillMaxWidth().clip(RoundedCornerShape(18.dp)).clickable(onClick = onClick),
            colors = CardDefaults.cardColors(containerColor = SurfaceDark),
            shape = RoundedCornerShape(18.dp),
        ) {
            Row(Modifier.fillMaxWidth().padding(14.dp), verticalAlignment = Alignment.CenterVertically) {
                Avatar(peerId)
                Spacer(Modifier.width(14.dp))
                Column(Modifier.weight(1f)) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Text(humanName(peerId), modifier = Modifier.weight(1f, fill = false), style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.SemiBold, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        if (peer.isEmbedded) {
                            Spacer(Modifier.width(6.dp))
                            Surface(
                                shape = RoundedCornerShape(4.dp),
                                color = Teal.copy(alpha = 0.18f),
                            ) {
                                Text(
                                    "Embedded",
                                    modifier = Modifier.padding(horizontal = 5.dp, vertical = 2.dp),
                                    style = MaterialTheme.typography.labelSmall,
                                    color = Teal,
                                    fontWeight = FontWeight.Medium,
                                )
                            }
                        }
                        if (peer.compacting) {
                            Spacer(Modifier.width(8.dp))
                            CompactingIndicator(compact = true)
                        } else if (peer.active) {
                            Spacer(Modifier.width(8.dp))
                            WorkingIndicator(compact = true)
                        }
                    }
                    PeerActivityIndicators(peer)
                    ArrivalIndicator(arrival, onArrival, onDismissArrival)
                }
                Spacer(Modifier.width(8.dp))
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
private fun ArrivalIndicator(
    arrival: MessageArrival?,
    onArrival: (MessageArrival) -> Unit,
    onDismissArrival: (String) -> Unit,
    header: Boolean = false,
) {
    if (arrival == null) return
    val label = arrival.subject?.takeIf { it.isNotBlank() } ?: arrival.preview.ifBlank { "New message" }
    Row(
        Modifier
            .fillMaxWidth(if (header) 0.92f else 1f)
            .background(Color(0xFFFFC857).copy(alpha = 0.9f), RoundedCornerShape(10.dp))
            .clickable { onArrival(arrival) }
            .padding(horizontal = 8.dp, vertical = 6.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text("✉", color = Ink, style = MaterialTheme.typography.labelSmall)
        Spacer(Modifier.width(6.dp))
        Column(Modifier.weight(1f)) {
            Text(humanName(arrival.from), color = Ink, style = MaterialTheme.typography.labelSmall, fontWeight = FontWeight.Bold, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Text(label, color = Ink, style = MaterialTheme.typography.labelSmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
        TextButton(onClick = { onDismissArrival(arrival.agent) }, contentPadding = PaddingValues(horizontal = 4.dp, vertical = 0.dp)) {
            Text("×", color = Ink)
        }
    }
}

@Composable
private fun MessageArrivalDialog(arrival: MessageArrival, onDismiss: () -> Unit, onDismissArrival: () -> Unit) {
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(humanName(arrival.from)) },
        text = {
            Column(Modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                val details = listOfNotNull(
                    arrival.subject?.takeIf { it.isNotBlank() }?.let { "Subject: $it" },
                    arrival.createdAt.takeIf { it.isNotBlank() },
                )
                if (details.isNotEmpty()) Text(details.joinToString(" · "), color = TextMuted, style = MaterialTheme.typography.bodySmall)
                Text(arrival.body, style = MaterialTheme.typography.bodyMedium)
            }
        },
        confirmButton = { TextButton(onClick = onDismiss) { Text("Close") } },
        dismissButton = { TextButton(onClick = onDismissArrival) { Text("Dismiss") } },
    )
}

@Composable
private fun CompactingIndicator(compact: Boolean = false) {
    val pulse = synchronizedHaloPulse("compacting-pulse")
    val dotSize = if (compact) 7.dp else 8.dp
    val horizontalPadding = if (compact) 6.dp else 8.dp
    val verticalPadding = if (compact) 3.dp else 4.dp
    Row(
        Modifier
            .semantics(mergeDescendants = true) { contentDescription = "Compacting" }
            .background(Color(0xFF7E22CE).copy(alpha = 0.28f), RoundedCornerShape(999.dp))
            .padding(horizontal = horizontalPadding, vertical = verticalPadding),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        PulsingDot(Periwinkle, pulse, dotSize, 10.dp)
        Spacer(Modifier.width(6.dp))
        Text("Compacting", style = MaterialTheme.typography.labelSmall, color = Color(0xFFF2E8FF), fontWeight = FontWeight.Bold)
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

/** Draws one moving segment so working state is visible without a second status pill. */
@Composable
private fun OrbitingBorder(
    active: Boolean,
    orbitColor: Color,
    label: String,
    modifier: Modifier = Modifier,
    cornerRadius: Dp = 18.dp,
    content: @Composable () -> Unit,
) {
    if (!active) {
        Box(modifier) { content() }
        return
    }
    val transition = rememberInfiniteTransition(label = "$label-orbit")
    val progress by transition.animateFloat(
        initialValue = 0f,
        targetValue = 1f,
        animationSpec = infiniteRepeatable(animation = tween(900, easing = LinearEasing)),
        label = "$label-orbit-progress",
    )
    Box(
        modifier.drawWithCache {
            val strokeWidth = 1.5.dp.toPx()
            val radius = cornerRadius.toPx()
            onDrawWithContent {
                drawContent()
                drawPartialOrbitingBorder(progress, radius, strokeWidth, orbitColor)
            }
        },
    ) { content() }
}

private fun DrawScope.drawPartialOrbitingBorder(
    progress: Float,
    cornerRadius: Float,
    strokeWidth: Float,
    color: Color,
) {
    val inset = strokeWidth / 2f
    val width = (size.width - strokeWidth).coerceAtLeast(0f)
    val height = (size.height - strokeWidth).coerceAtLeast(0f)
    val radius = cornerRadius.coerceAtMost(minOf(width, height) / 2f)
    val perimeter = (2f * (width - 2f * radius)) + (2f * (height - 2f * radius)) + (2f * PI.toFloat() * radius)
    if (perimeter <= strokeWidth * 2f) return
    val segment = (perimeter * 0.24f).coerceIn(strokeWidth, perimeter - strokeWidth)
    drawRoundRect(
        color = color.copy(alpha = 0.82f),
        topLeft = Offset(inset, inset),
        size = androidx.compose.ui.geometry.Size(width, height),
        cornerRadius = androidx.compose.ui.geometry.CornerRadius(radius, radius),
        style = Stroke(
            width = strokeWidth,
            cap = StrokeCap.Round,
            pathEffect = PathEffect.dashPathEffect(floatArrayOf(segment, perimeter - segment), -progress * perimeter),
        ),
    )
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
    agentEvents: List<AgentTimelineEvent> = emptyList(),
    pendingImages: List<PendingImage>,
    draftValue: TextFieldValue,
    onDraft: (TextFieldValue) -> Unit,
    onImages: (List<Uri>) -> Unit,
    onRemoveImage: (Int) -> Unit,
    onSend: () -> Unit,
    onBack: () -> Unit,
    arrival: MessageArrival?,
    onArrival: (MessageArrival) -> Unit,
    onDismissArrival: (String) -> Unit,
    capabilities: ControlCapabilities,
    historyState: HistoryUiState,
    onRetryHistory: () -> Unit,
    modelControlState: ControlState,
    goalControlState: ControlState,
    reminders: List<ReminderSummary>,
    reminderUiState: ReminderUiState,
    pendingReminderIds: Set<String>,
    pendingReminderLockIds: Set<String>,
    reminderLocks: Map<String, Boolean>,
    onRefreshReminders: () -> Unit,
    onCreateReminder: (String, String, String, Long, Boolean, Boolean) -> Unit,
    onUpdateReminder: (String, ReminderSummary, String, String) -> Unit,
    onDeleteReminder: (String, ReminderSummary) -> Unit,
    onReminderLock: (String, String, String) -> Unit,
    onModel: (String, ModelSummary) -> Unit,
    onGoal: (String, GoalEditorDraft) -> Unit,
    promptTemplates: List<PromptTemplate>,
    promptTemplatesLoaded: Boolean,
    promptRequestState: PromptRequestState,
    promptImportState: PromptImportState?,
    onLoadPromptTemplates: () -> Unit,
    onPromptSave: (String?, String, String) -> Unit,
    onPromptDelete: (String) -> Unit,
    onPromptImport: (String) -> Unit,
    onPromptImportConfirm: () -> Unit,
    onPromptImportCancel: () -> Unit,
    onPromptImportFile: (Uri) -> Unit,
    onPromptExportFailure: () -> Unit,
    imageLoader: RemoteImageLoader,
    routerBase: String,
) {
    val peer = peerStatus.id
    val context = LocalContext.current
    var modelPickerOpen by remember(peer) { mutableStateOf(false) }
    var goalEditorOpen by remember(peer) { mutableStateOf(false) }
    var promptPickerOpen by remember(peer) { mutableStateOf(false) }
    var promptManagerOpen by remember(peer) { mutableStateOf(false) }
    var promptEditor by remember(peer) { mutableStateOf<PromptTemplate?>(null) }
    var promptDeleteId by remember(peer) { mutableStateOf<String?>(null) }
    var promptUse by remember(peer) { mutableStateOf<PromptTemplate?>(null) }
    var promptSelection by remember(peer) { mutableStateOf<PromptTemplateSelection?>(null) }
    var reminderDialogOpen by remember(peer) { mutableStateOf(false) }
    var reminderCreateOpen by remember(peer) { mutableStateOf(false) }
    var reminderEdit by remember(peer) { mutableStateOf<ReminderSummary?>(null) }
    var reminderDeleteConfirmation by remember(peer) { mutableStateOf<ReminderSummary?>(null) }
    var modelSearch by remember(peer) { mutableStateOf("") }
    var goalDraft by remember(peerStatus.goalState) { mutableStateOf(GoalEditorDraft.from(peerStatus.goalState)) }
    LaunchedEffect(promptRequestState.successSerial) {
        if (promptRequestState.successSerial > 0) {
            promptEditor = null
            promptDeleteId = null
            promptManagerOpen = true
        }
    }
    val importLauncher = rememberLauncherForActivityResult(ActivityResultContracts.OpenDocument()) { uri -> uri?.let(onPromptImportFile) }
    val exportLauncher = rememberLauncherForActivityResult(ActivityResultContracts.CreateDocument("application/json")) { uri ->
        if (uri != null) runCatching {
            context.contentResolver.openOutputStream(uri)?.use { output ->
                output.write(promptTemplateExportDocument(promptTemplates).toByteArray(Charsets.UTF_8))
            } ?: error("Unable to export prompt templates.")
        }.onFailure { onPromptExportFailure() }
    }
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
    val timelineEntries = remember(messages, agentEvents) {
        (messages.map { ConversationTimelineEntry.Chat(it) } +
         agentEvents.map { ConversationTimelineEntry.Event(it) })
            .sortedBy { it.timestamp }
    }
    LaunchedEffect(timelineEntries.lastOrNull()?.key) {
        if (timelineEntries.isNotEmpty()) transcriptState.animateScrollToItem(timelineEntries.lastIndex)
    }
    val orbit = activityOrbitFor(peerStatus)
    Column(Modifier.fillMaxSize()) {
        OrbitingBorder(
            active = orbit != null,
            orbitColor = orbit?.color ?: Teal,
            label = "conversation-header-${peerStatus.id}",
            modifier = Modifier.fillMaxWidth(),
            cornerRadius = 0.dp,
        ) {
            BoxWithConstraints(Modifier.fillMaxWidth().background(SurfaceDark)) {
            val useOverflow = conversationHeaderUsesOverflow(maxWidth.value)
            Column(
                Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 10.dp),
            ) {
                IconButton(onClick = onBack, modifier = Modifier.size(48.dp)) {
                    Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back", tint = TextPrimary)
                }
                Row(
                    Modifier.fillMaxWidth(),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Column(Modifier.weight(1f)) {
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            Text(humanName(peer), modifier = Modifier.weight(1f, fill = false), style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.Bold, maxLines = 1, overflow = TextOverflow.Ellipsis)
                            if (peerStatus.isEmbedded) {
                                Spacer(Modifier.width(6.dp))
                                Surface(
                                    shape = RoundedCornerShape(4.dp),
                                    color = Teal.copy(alpha = 0.18f),
                                ) {
                                    Text(
                                        "Embedded",
                                        modifier = Modifier.padding(horizontal = 5.dp, vertical = 2.dp),
                                        style = MaterialTheme.typography.labelSmall,
                                        color = Teal,
                                        fontWeight = FontWeight.Medium,
                                    )
                                }
                            }
                            if (peerStatus.compacting) {
                                Spacer(Modifier.width(8.dp))
                                CompactingIndicator(compact = true)
                            } else if (peerStatus.active) {
                                Spacer(Modifier.width(8.dp))
                                WorkingIndicator(compact = true)
                            }
                        }
                        PeerActivityIndicators(peerStatus, compact = true)
                        ArrivalIndicator(arrival, onArrival, onDismissArrival, header = true)
                    }
                    if (useOverflow) {
                        // Keep prompt invocation visible on phones. The overflow menu remains available for the other controls.
                        IconButton(
                            onClick = {
                                promptSelection = PromptTemplateSelection(draftValue.selection.start, draftValue.selection.end)
                                promptPickerOpen = true
                                onLoadPromptTemplates()
                            },
                            modifier = Modifier.size(48.dp),
                        ) {
                            Icon(Icons.Default.Edit, contentDescription = "Use prompt template", tint = Periwinkle)
                        }
                        ConversationHeaderOverflowMenu(
                            capabilities = capabilities,
                            goalEnabled = peerStatus.goalState?.enabled == true,
                            currentModel = peerStatus.modelCatalog.current?.key,
                            onGoal = { goalEditorOpen = true },
                            onModel = { modelPickerOpen = true },
                            onReminders = {
                                reminderCreateOpen = visibleRemindersForPeer(reminders, peer).isEmpty()
                                reminderDialogOpen = true
                                onRefreshReminders()
                            },
                            onPrompt = {
                                promptSelection = PromptTemplateSelection(draftValue.selection.start, draftValue.selection.end)
                                promptPickerOpen = true
                                onLoadPromptTemplates()
                            },
                            onManagePrompts = { promptManagerOpen = true; onLoadPromptTemplates() },
                        )
                    } else {
                        if (capabilities.goalControlV1) {
                            TextButton(onClick = { goalEditorOpen = true }, modifier = Modifier.height(44.dp)) {
                                Text(if (peerStatus.goalState?.enabled == true) "Goal" else "Set goal", color = if (peerStatus.goalState?.enabled == true) Teal else Periwinkle)
                            }
                        }
                        if (capabilities.modelControlV1) {
                            TextButton(onClick = { modelPickerOpen = true }, modifier = Modifier.height(44.dp)) {
                                Text(peerStatus.modelCatalog.current?.key ?: "Model", color = Periwinkle, maxLines = 1, overflow = TextOverflow.Ellipsis)
                            }
                        }
                        TextButton(
                            onClick = {
                                reminderCreateOpen = visibleRemindersForPeer(reminders, peer).isEmpty()
                                reminderDialogOpen = true
                                onRefreshReminders()
                            },
                            modifier = Modifier.height(44.dp),
                        ) { Text("Reminders", color = if (visibleRemindersForPeer(reminders, peer).isEmpty()) TextMuted else Teal) }
                        TextButton(
                            onClick = {
                                promptSelection = PromptTemplateSelection(draftValue.selection.start, draftValue.selection.end)
                                promptPickerOpen = true
                                onLoadPromptTemplates()
                            },
                            modifier = Modifier.height(44.dp),
                        ) { Text("Prompt", color = Periwinkle) }
                        TextButton(onClick = { promptManagerOpen = true; onLoadPromptTemplates() }, modifier = Modifier.height(44.dp)) {
                            Text("Manage", color = TextMuted)
                        }
                    }
                }
            }
        }
        }
        HorizontalDivider(color = SurfaceRaised)
        if (historyState.status == HistoryStatus.ERROR) {
            Row(
                Modifier.fillMaxWidth().background(SurfaceRaised).padding(start = 16.dp, top = 4.dp, end = 8.dp, bottom = 4.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Text(
                    historyState.message ?: "History could not be loaded.",
                    Modifier.weight(1f),
                    color = Color(0xFFFFB4AB),
                    style = MaterialTheme.typography.bodySmall,
                )
                TextButton(onClick = onRetryHistory) { Text("Retry", color = Periwinkle) }
            }
        }

        if (timelineEntries.isEmpty()) {
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
                items(timelineEntries, key = { it.key }) { entry ->
                    when (entry) {
                        is ConversationTimelineEntry.Chat -> MessageBubble(entry.message, routerBase, imageLoader)
                        is ConversationTimelineEntry.Event -> AgentEventChip(entry.event)
                    }
                }
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
                    value = draftValue,
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
                    enabled = draftValue.text.isNotBlank() || pendingImages.isNotEmpty(),
                    modifier = Modifier.height(56.dp),
                    shape = RoundedCornerShape(16.dp),
                    colors = ButtonDefaults.buttonColors(containerColor = Periwinkle, contentColor = Ink),
                ) { Text("Send", fontWeight = FontWeight.Bold) }
            }
        }
    }
    if (reminderDialogOpen) {
        ReminderDialog(
            peer = peer,
            reminders = reminders,
            createMode = reminderCreateOpen,
            editingReminder = reminderEdit,
            deleteConfirmation = reminderDeleteConfirmation,
            pendingReminderIds = pendingReminderIds,
            pendingReminderLockIds = pendingReminderLockIds,
            reminderLocks = reminderLocks,
            state = reminderUiState,
            onCreateMode = { reminderCreateOpen = true; reminderEdit = null; reminderDeleteConfirmation = null },
            onEdit = { reminderEdit = it; reminderCreateOpen = false; reminderDeleteConfirmation = null },
            onDeleteRequest = { reminderDeleteConfirmation = it },
            onDeleteCancel = { reminderDeleteConfirmation = null },
            onDeleteConfirm = { reminder -> reminderDeleteConfirmation = null; onDeleteReminder(peer, reminder) },
            onCreate = { body, subject, delayMinutes, recurring, lockAfterCreate ->
                onCreateReminder(peer, body, subject, delayMinutes, recurring, lockAfterCreate)
            },
            onUpdate = { reminder, body, subject -> onUpdateReminder(peer, reminder, body, subject) },
            onReminderLock = { reminder, action -> onReminderLock(peer, reminder.id, action) },
            onRefresh = onRefreshReminders,
            onListMode = { reminderCreateOpen = false; reminderEdit = null; reminderDeleteConfirmation = null },
            onDismiss = { reminderDialogOpen = false; reminderCreateOpen = false; reminderEdit = null; reminderDeleteConfirmation = null },
        )
    }
    if (modelPickerOpen) {
        ModelPickerDialog(peerStatus.modelCatalog, modelSearch, modelControlState, { modelSearch = it }, { model -> modelPickerOpen = false; onModel(peer, model) }, { modelPickerOpen = false })
    }
    if (goalEditorOpen) {
        GoalEditorDialog(peer, goalDraft, goalControlState, { goalDraft = it }, { onGoal(peer, goalDraft) }, { goalDraft = GoalEditorDraft.from(peerStatus.goalState); goalEditorOpen = false })
    }
    if (promptPickerOpen) {
        PromptTemplatePickerDialog(
            templates = promptTemplates,
            loaded = promptTemplatesLoaded,
            state = promptRequestState,
            onSelect = { promptUse = it; promptPickerOpen = false },
            onManage = { promptPickerOpen = false; promptManagerOpen = true; onLoadPromptTemplates() },
            onDismiss = { promptPickerOpen = false },
        )
    }
    if (promptManagerOpen) {
        PromptTemplateManagerDialog(
            templates = promptTemplates,
            loaded = promptTemplatesLoaded,
            state = promptRequestState,
            importState = promptImportState,
            deleteId = promptDeleteId,
            onNew = { promptManagerOpen = false; promptEditor = PromptTemplate("", "") },
            onEdit = { promptManagerOpen = false; promptEditor = it },
            onDelete = { promptDeleteId = it },
            onConfirmDelete = { id -> promptDeleteId = null; onPromptDelete(id) },
            onCancelDelete = { promptDeleteId = null },
            onImport = { importLauncher.launch(arrayOf("application/json", ".json")) },
            onExport = { exportLauncher.launch("prompt-templates.json") },
            onClose = { promptManagerOpen = false },
            onConfirmImport = onPromptImportConfirm,
            onCancelImport = onPromptImportCancel,
        )
    }
    promptEditor?.let { template ->
        PromptTemplateEditorDialog(
            template = template,
            state = promptRequestState,
            onSave = { name, body -> onPromptSave(template.id, name, body) },
            onDismiss = { promptEditor = null; promptManagerOpen = true },
        )
    }
    promptUse?.let { template ->
        PromptTemplateUseDialog(
            template = template,
            draft = draftValue.text,
            selection = promptSelection,
            onPaste = { result ->
                onDraft(TextFieldValue(result.text, TextRange(result.caret)))
                promptUse = null
                promptSelection = null
            },
            onDismiss = { promptUse = null },
        )
    }
}

@Composable
private fun ConversationHeaderOverflowMenu(
    capabilities: ControlCapabilities,
    goalEnabled: Boolean,
    currentModel: String?,
    onGoal: () -> Unit,
    onModel: () -> Unit,
    onReminders: () -> Unit,
    onPrompt: () -> Unit,
    onManagePrompts: () -> Unit,
) {
    var expanded by remember { mutableStateOf(false) }
    Box {
        IconButton(onClick = { expanded = toggleConversationControlMenu(expanded) }, modifier = Modifier.size(48.dp)) {
            Icon(Icons.Default.MoreVert, contentDescription = "More conversation controls", tint = TextPrimary)
        }
        DropdownMenu(
            expanded = expanded,
            onDismissRequest = { expanded = false },
            containerColor = SurfaceDark,
        ) {
            val menuColors = MenuDefaults.itemColors(textColor = TextPrimary)
            conversationControlMenuActions(capabilities.goalControlV1, capabilities.modelControlV1).forEach { action ->
                when (action) {
                    ConversationControlAction.GOAL -> DropdownMenuItem(
                        text = { Text(if (goalEnabled) "Goal" else "Set goal") },
                        onClick = { expanded = false; onGoal() },
                        colors = menuColors,
                    )
                    ConversationControlAction.MODEL -> DropdownMenuItem(
                        text = { Text(currentModel?.let { "Model: $it" } ?: "Model") },
                        onClick = { expanded = false; onModel() },
                        colors = menuColors,
                    )
                    ConversationControlAction.REMINDERS -> DropdownMenuItem(
                        text = { Text("Reminders") },
                        onClick = { expanded = false; onReminders() },
                        colors = menuColors,
                    )
                    ConversationControlAction.PROMPT -> DropdownMenuItem(
                        text = { Text("Prompt") },
                        onClick = { expanded = false; onPrompt() },
                        colors = menuColors,
                    )
                    ConversationControlAction.MANAGE_PROMPTS -> DropdownMenuItem(
                        text = { Text("Manage prompts") },
                        onClick = { expanded = false; onManagePrompts() },
                        colors = menuColors,
                    )
                }
            }
        }
    }
}

@Composable
private fun ReminderDialog(
    peer: String,
    reminders: List<ReminderSummary>,
    createMode: Boolean,
    editingReminder: ReminderSummary?,
    deleteConfirmation: ReminderSummary?,
    pendingReminderIds: Set<String>,
    pendingReminderLockIds: Set<String>,
    reminderLocks: Map<String, Boolean>,
    state: ReminderUiState,
    onCreateMode: () -> Unit,
    onEdit: (ReminderSummary) -> Unit,
    onDeleteRequest: (ReminderSummary) -> Unit,
    onDeleteCancel: () -> Unit,
    onDeleteConfirm: (ReminderSummary) -> Unit,
    onCreate: (String, String, Long, Boolean, Boolean) -> Unit,
    onUpdate: (ReminderSummary, String, String) -> Unit,
    onReminderLock: (ReminderSummary, String) -> Unit,
    onRefresh: () -> Unit,
    onListMode: () -> Unit,
    onDismiss: () -> Unit,
) {
    val editorMode = createMode || editingReminder != null
    var now by remember(peer, editorMode, editingReminder?.id) { mutableStateOf(System.currentTimeMillis()) }
    var subject by remember(peer, editorMode, editingReminder?.id) { mutableStateOf(editingReminder?.subject.orEmpty()) }
    var body by remember(peer, editorMode, editingReminder?.id) { mutableStateOf(editingReminder?.body.orEmpty()) }
    var delayMinutes by remember(peer, editorMode, editingReminder?.id) { mutableStateOf("1") }
    var recurring by remember(peer, editorMode, editingReminder?.id) { mutableStateOf(false) }
    var lockAfterCreate by remember(peer, editorMode, editingReminder?.id) { mutableStateOf(false) }
    val active = visibleRemindersForPeer(reminders, peer)
    LaunchedEffect(peer, editorMode) {
        while (true) {
            delay(1_000)
            now = System.currentTimeMillis()
        }
    }
    LaunchedEffect(state.status, state.message) {
        if (state.status == ReminderStatus.ACCEPTED) onListMode()
    }
    AlertDialog(
        onDismissRequest = onDismiss,
        containerColor = SurfaceDark,
        titleContentColor = TextPrimary,
        textContentColor = TextPrimary,
        title = { Text(when { createMode -> "Add reminder for ${humanName(peer)}"; editingReminder != null -> "Edit reminder for ${humanName(peer)}"; else -> "Reminders for ${humanName(peer)}" }) },
        text = {
            Column(Modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(10.dp)) {
                if (!editorMode) {
                    Button(onClick = onCreateMode, colors = ButtonDefaults.buttonColors(containerColor = Periwinkle, contentColor = Ink)) { Text("Add reminder") }
                    if (state.status == ReminderStatus.LOADING) Text("Loading reminders…", color = TextMuted)
                    else if (active.isEmpty()) Text("No active reminders.", color = TextMuted)
                    active.forEach { reminder ->
                        val key = "${peer}:${reminder.id}"
                        val pending = reminder.id in pendingReminderIds
                        val lockPending = key in pendingReminderLockIds
                        Card(colors = CardDefaults.cardColors(containerColor = SurfaceRaised), shape = RoundedCornerShape(12.dp)) {
                            Column(Modifier.fillMaxWidth().padding(12.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                                Text(reminder.subject ?: "Reminder", color = TextPrimary, fontWeight = FontWeight.SemiBold)
                                Text(reminder.body, color = TextPrimary, maxLines = 4, overflow = TextOverflow.Ellipsis)
                                Text(formatReminderCountdown(reminder.nextDueAt, now), color = Teal, style = MaterialTheme.typography.bodySmall)
                                Text(reminderRecurrenceLabel(reminder.intervalMs), color = TextMuted, style = MaterialTheme.typography.bodySmall)
                                if (deleteConfirmation?.id == reminder.id) {
                                    Text("Delete this reminder?", color = Color(0xFFFF8A80), modifier = Modifier.padding(top = 4.dp))
                                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                        TextButton(onClick = onDeleteCancel, enabled = !pending) { Text("Cancel") }
                                        TextButton(onClick = { onDeleteConfirm(reminder) }, enabled = !pending) { Text("Delete", color = Color(0xFFFFA4A0)) }
                                    }
                                } else {
                                    Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                                        TextButton(onClick = { onEdit(reminder) }, enabled = !pending) { Text("Edit") }
                                        TextButton(onClick = { onDeleteRequest(reminder) }, enabled = !pending) { Text("Delete") }
                                        Spacer(Modifier.weight(1f))
                                        Text("Lock native edits", color = TextMuted, style = MaterialTheme.typography.bodySmall)
                                        Switch(
                                            checked = reminderLocks[key] == true,
                                            onCheckedChange = { checked -> onReminderLock(reminder, if (checked) "set" else "clear") },
                                            enabled = !pending && !lockPending,
                                        )
                                    }
                                }
                            }
                        }
                    }
                } else {
                    OutlinedTextField(subject, { subject = it }, Modifier.fillMaxWidth(), singleLine = true, label = { Text("Subject (optional)") })
                    OutlinedTextField(body, { body = it }, Modifier.fillMaxWidth(), minLines = 3, maxLines = 6, label = { Text("Reminder") })
                    if (editingReminder == null) {
                        OutlinedTextField(delayMinutes, { delayMinutes = it }, Modifier.fillMaxWidth(), singleLine = true, label = { Text("Send after (minutes)") })
                        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                            Text("Repeat every delay interval", Modifier.weight(1f), color = TextPrimary)
                            Switch(recurring, { recurring = it })
                        }
                        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                            Text("Lock native edits after creation", Modifier.weight(1f), color = TextPrimary)
                            Switch(lockAfterCreate, { lockAfterCreate = it })
                        }
                    }
                }
                if (state.message != null && state.status != ReminderStatus.IDLE && state.status != ReminderStatus.LOADING) {
                    Text(state.message, color = if (state.status == ReminderStatus.REJECTED || state.status == ReminderStatus.CREATED_LOCK_FAILED || state.status == ReminderStatus.TIMEOUT) Color(0xFFFF8A80) else TextMuted, style = MaterialTheme.typography.bodySmall)
                }
            }
        },
        confirmButton = {
            if (createMode) Button(
                onClick = { onCreate(body, subject, delayMinutes.toLongOrNull() ?: 0L, recurring, lockAfterCreate) },
                enabled = state.status != ReminderStatus.SAVING && state.status != ReminderStatus.LOCK_PENDING,
                colors = ButtonDefaults.buttonColors(containerColor = Periwinkle, contentColor = Ink),
            ) { Text("Create") }
            else if (editingReminder != null) Button(
                onClick = { onUpdate(editingReminder, body, subject) },
                enabled = state.status != ReminderStatus.SAVING && state.status != ReminderStatus.LOCK_PENDING,
                colors = ButtonDefaults.buttonColors(containerColor = Periwinkle, contentColor = Ink),
            ) { Text("Save changes") }
            else TextButton(onDismiss) { Text("Close") }
        },
        dismissButton = { if (editorMode) TextButton(onClick = { onListMode() }) { Text("Cancel") } },
    )
}

@Composable
private fun PromptTemplatePickerDialog(
    templates: List<PromptTemplate>,
    loaded: Boolean,
    state: PromptRequestState,
    onSelect: (PromptTemplate) -> Unit,
    onManage: () -> Unit,
    onDismiss: () -> Unit,
) {
    AlertDialog(
        onDismissRequest = onDismiss,
        containerColor = SurfaceDark,
        titleContentColor = TextPrimary,
        textContentColor = TextPrimary,
        title = { Text("Choose prompt") },
        text = {
            when {
                !loaded -> Text("Loading prompt templates…", color = TextMuted)
                templates.isEmpty() -> Text("No saved prompt templates", color = TextMuted)
                else -> LazyColumn(Modifier.heightIn(max = 360.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                    items(templates, key = { it.id ?: it.name }) { template ->
                        TextButton(onClick = { onSelect(template) }, modifier = Modifier.fillMaxWidth()) {
                            Column(Modifier.fillMaxWidth()) {
                                Text(template.name, color = TextPrimary, maxLines = 1, overflow = TextOverflow.Ellipsis)
                                Text(template.body, color = TextMuted, style = MaterialTheme.typography.bodySmall, maxLines = 2, overflow = TextOverflow.Ellipsis)
                            }
                        }
                    }
                }
            }
            if (state.status == PromptRequestStatus.ERROR) Text(state.message.orEmpty(), color = Color(0xFFFF8A80), style = MaterialTheme.typography.bodySmall)
        },
        confirmButton = {
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                TextButton(onClick = onManage) { Text("Manage prompts") }
                TextButton(onClick = onDismiss) { Text("Cancel") }
            }
        },
    )
}

@Composable
private fun PromptTemplateManagerDialog(
    templates: List<PromptTemplate>,
    loaded: Boolean,
    state: PromptRequestState,
    importState: PromptImportState?,
    deleteId: String?,
    onNew: () -> Unit,
    onEdit: (PromptTemplate) -> Unit,
    onDelete: (String) -> Unit,
    onConfirmDelete: (String) -> Unit,
    onCancelDelete: () -> Unit,
    onImport: () -> Unit,
    onExport: () -> Unit,
    onClose: () -> Unit,
    onConfirmImport: () -> Unit,
    onCancelImport: () -> Unit,
) {
    val busy = importState?.phase == PromptImportPhase.REFRESHING || importState?.phase == PromptImportPhase.RUNNING
    AlertDialog(
        onDismissRequest = { if (!busy) onClose() },
        containerColor = SurfaceDark,
        titleContentColor = TextPrimary,
        textContentColor = TextPrimary,
        title = { Text("Prompt templates") },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
                BoxWithConstraints(Modifier.fillMaxWidth()) {
                    if (promptManagerUsesStackedActions(maxWidth.value)) {
                        Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                            Button(
                                onClick = onImport,
                                enabled = loaded && !busy,
                                modifier = Modifier.fillMaxWidth(),
                                colors = ButtonDefaults.buttonColors(containerColor = Periwinkle, contentColor = Ink),
                            ) { Text("Import") }
                            TextButton(onClick = onExport, enabled = loaded && !busy, modifier = Modifier.fillMaxWidth()) { Text("Export") }
                            TextButton(onClick = onNew, enabled = !busy, modifier = Modifier.fillMaxWidth()) { Text("New") }
                        }
                    } else {
                        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                            Button(onClick = onImport, enabled = loaded && !busy, colors = ButtonDefaults.buttonColors(containerColor = Periwinkle, contentColor = Ink)) { Text("Import") }
                            TextButton(onClick = onExport, enabled = loaded && !busy) { Text("Export") }
                            TextButton(onClick = onNew, enabled = !busy) { Text("New") }
                        }
                    }
                }
                importState?.let { importing ->
                    when (importing.phase) {
                        PromptImportPhase.REFRESHING -> {
                            Text("Checking templates", color = Teal, fontWeight = FontWeight.Bold)
                            Text("Checking for changes before import…", color = TextMuted)
                        }
                        PromptImportPhase.RUNNING -> {
                            Text("Importing templates", color = Teal, fontWeight = FontWeight.Bold)
                            Text("Importing template ${minOf(importing.index + 1, importing.templates.size)} of ${importing.templates.size}…", color = TextMuted)
                        }
                        PromptImportPhase.CONFIRM -> {
                            Text("Import ${importing.templates.size} template${if (importing.templates.size == 1) "" else "s"}?", color = Teal, fontWeight = FontWeight.Bold)
                            Text(if (importing.conflicts.isEmpty()) "No existing templates will be changed." else "These existing templates will be overwritten: ${importing.conflicts.joinToString { it.name }}.", color = TextMuted)
                            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                TextButton(onClick = onCancelImport) { Text("Cancel") }
                                Button(onClick = onConfirmImport, colors = ButtonDefaults.buttonColors(containerColor = Periwinkle, contentColor = Ink)) { Text(if (importing.conflicts.isEmpty()) "Import" else "Import and overwrite") }
                            }
                        }
                    }
                }
                if (!loaded) Text("Loading prompt templates…", color = TextMuted)
                else if (templates.isEmpty()) Text("No saved prompt templates.", color = TextMuted)
                else LazyColumn(Modifier.heightIn(max = 360.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    items(templates, key = { it.id ?: it.name }) { template ->
                        Card(colors = CardDefaults.cardColors(containerColor = SurfaceRaised), shape = RoundedCornerShape(12.dp)) {
                            Column(Modifier.fillMaxWidth().padding(10.dp)) {
                                Text(template.name, color = TextPrimary, fontWeight = FontWeight.SemiBold)
                                Text(template.body, color = TextMuted, style = MaterialTheme.typography.bodySmall, maxLines = 4, overflow = TextOverflow.Ellipsis)
                                if (deleteId == template.id) {
                                    Text("Delete this template?", color = Color(0xFFFF8A80), modifier = Modifier.padding(top = 6.dp))
                                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                        TextButton(onClick = onCancelDelete) { Text("Cancel") }
                                        TextButton(onClick = { onConfirmDelete(template.id.orEmpty()) }) { Text("Delete", color = Color(0xFFFFA4A0)) }
                                    }
                                } else Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                    TextButton(onClick = { onEdit(template) }, enabled = !busy) { Text("Edit") }
                                    TextButton(onClick = { onDelete(template.id.orEmpty()) }, enabled = !busy) { Text("Delete") }
                                }
                            }
                        }
                    }
                }
                if (state.message != null && state.status != PromptRequestStatus.IDLE) {
                    Text(state.message, color = if (state.status == PromptRequestStatus.ERROR || state.status == PromptRequestStatus.UNCONFIRMED) Color(0xFFFF8A80) else TextMuted, style = MaterialTheme.typography.bodySmall)
                }
            }
        },
        confirmButton = { TextButton(onClick = onClose, enabled = !busy) { Text("Close") } },
    )
}

@Composable
private fun PromptTemplateEditorDialog(
    template: PromptTemplate,
    state: PromptRequestState,
    onSave: (String, String) -> Unit,
    onDismiss: () -> Unit,
) {
    var name by remember(template) { mutableStateOf(template.name) }
    var body by remember(template) { mutableStateOf(template.body) }
    AlertDialog(
        onDismissRequest = { if (state.status != PromptRequestStatus.SAVING) onDismiss() },
        containerColor = SurfaceDark,
        titleContentColor = TextPrimary,
        textContentColor = TextPrimary,
        title = { Text(if (template.id == null) "New prompt template" else "Edit prompt template") },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
                OutlinedTextField(name, { name = it }, Modifier.fillMaxWidth(), singleLine = true, label = { Text("Template name") })
                OutlinedTextField(body, { body = it }, Modifier.fillMaxWidth().heightIn(min = 180.dp), minLines = 8, maxLines = 14, label = { Text("Prompt body") })
                if (state.message != null && state.status == PromptRequestStatus.ERROR) Text(state.message, color = Color(0xFFFF8A80), style = MaterialTheme.typography.bodySmall)
            }
        },
        confirmButton = { Button(onClick = { onSave(name, body) }, enabled = state.status != PromptRequestStatus.SAVING, colors = ButtonDefaults.buttonColors(containerColor = Periwinkle, contentColor = Ink)) { Text(if (template.id == null) "Create template" else "Save changes") } },
        dismissButton = { TextButton(onClick = onDismiss, enabled = state.status != PromptRequestStatus.SAVING) { Text("Cancel") } },
    )
}

@Composable
private fun PromptTemplateUseDialog(
    template: PromptTemplate,
    draft: String,
    selection: PromptTemplateSelection?,
    onPaste: (PromptTemplateEditResult) -> Unit,
    onDismiss: () -> Unit,
) {
    val tokens = remember(template.body) { parsePromptTemplateTokens(template.body) }
    var values by remember(template) { mutableStateOf(emptyMap<String, Any?>()) }
    val selectedText = remember(draft, selection) { promptTemplateSelectedText(draft, selection) }
    val preview = renderPromptTemplate(template.body, values)
    AlertDialog(
        onDismissRequest = onDismiss,
        containerColor = SurfaceDark,
        titleContentColor = TextPrimary,
        textContentColor = TextPrimary,
        title = { Text("Use prompt template") },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
                Text(if (tokens.isEmpty()) "Paste this prompt into the composer." else "Fill in the values before pasting this prompt.", color = TextMuted)
                if (selectedText.isEmpty()) {
                    Text("Inserts at the composer cursor.", color = TextMuted, style = MaterialTheme.typography.bodySmall)
                } else {
                    Text("Replaces selected composer text:", color = Teal, style = MaterialTheme.typography.bodySmall)
                    Card(colors = CardDefaults.cardColors(containerColor = SurfaceRaised), shape = RoundedCornerShape(12.dp)) {
                        Text(selectedText, Modifier.padding(12.dp), color = TextPrimary, maxLines = 3, overflow = TextOverflow.Ellipsis)
                    }
                }
                tokens.forEach { token ->
                    OutlinedTextField(values[token]?.toString().orEmpty(), { value -> values = values + (token to value) }, Modifier.fillMaxWidth(), singleLine = true, label = { Text(token) })
                }
                Text("Preview", color = Teal, fontWeight = FontWeight.SemiBold)
                Card(colors = CardDefaults.cardColors(containerColor = SurfaceRaised), shape = RoundedCornerShape(12.dp)) {
                    Text(preview, Modifier.padding(12.dp), color = TextPrimary)
                }
            }
        },
        confirmButton = { Button(onClick = { onPaste(replacePromptTemplateSelection(draft, preview, selection)) }, colors = ButtonDefaults.buttonColors(containerColor = Periwinkle, contentColor = Ink)) { Text("Paste") } },
        dismissButton = { TextButton(onDismiss) { Text("Cancel") } },
    )
}

@Composable
private fun ModelPickerDialog(
    catalog: ModelCatalog,
    query: String,
    controlState: ControlState,
    onQuery: (String) -> Unit,
    onSelect: (ModelSummary) -> Unit,
    onDismiss: () -> Unit,
) {
    val visible = catalog.models.filter { model -> "${model.key} ${model.name.orEmpty()}".contains(query.trim(), ignoreCase = true) }
    AlertDialog(
        onDismissRequest = onDismiss,
        containerColor = SurfaceDark,
        titleContentColor = TextPrimary,
        textContentColor = TextPrimary,
        title = { Text("Choose model") },
        text = {
            Column {
                OutlinedTextField(query, onQuery, Modifier.fillMaxWidth(), singleLine = true, label = { Text("Search models") })
                Spacer(Modifier.height(8.dp))
                when {
                    catalog.status == CatalogStatus.LOADING -> Text("Loading models…", color = TextMuted, modifier = Modifier.padding(12.dp))
                    catalog.status == CatalogStatus.EMPTY -> Text("No models available", color = TextMuted, modifier = Modifier.padding(12.dp))
                    visible.isEmpty() -> Text("No matching models", color = TextMuted, modifier = Modifier.padding(12.dp))
                    else -> LazyColumn(Modifier.heightIn(max = 300.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                        items(visible, key = { it.key }) { model ->
                            TextButton(onClick = { onSelect(model) }, modifier = Modifier.fillMaxWidth()) {
                                Column(Modifier.fillMaxWidth()) {
                                    Text(model.name ?: model.key, color = TextPrimary, maxLines = 1, overflow = TextOverflow.Ellipsis)
                                    Text(model.key, color = TextMuted, style = MaterialTheme.typography.bodySmall)
                                }
                            }
                        }
                    }
                }
                ControlStatusText(controlState)
            }
        },
        confirmButton = { TextButton(onDismiss) { Text("Cancel") } },
    )
}

@Composable
private fun GoalEditorDialog(
    peer: String,
    initial: GoalEditorDraft,
    controlState: ControlState,
    onDraft: (GoalEditorDraft) -> Unit,
    onSave: () -> Unit,
    onDismiss: () -> Unit,
) {
    var draft by remember(initial) { mutableStateOf(initial) }
    fun update(next: GoalEditorDraft) { draft = next; onDraft(next) }
    AlertDialog(
        onDismissRequest = onDismiss,
        containerColor = SurfaceDark,
        titleContentColor = TextPrimary,
        textContentColor = TextPrimary,
        title = { Text("Goal for ${humanName(peer)}") },
        text = {
            val layout = goalEditorLayoutState()
            Column(
                modifier = Modifier
                    .fillMaxWidth()
                    .heightIn(max = layout.maxHeightDp.dp)
                    .verticalScroll(rememberScrollState())
                    .imePadding(),
                verticalArrangement = Arrangement.spacedBy(8.dp),
            ) {
                Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                    Text("Goal", Modifier.weight(1f), color = TextPrimary)
                    Switch(draft.enabled, { update(draft.copy(enabled = it)) })
                }
                OutlinedTextField(draft.goal, { update(draft.copy(goal = it)) }, Modifier.fillMaxWidth(), enabled = draft.enabled, minLines = 3, maxLines = 6, label = { Text("What should this agent work toward?") })
                Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                    Text("Goal lock", Modifier.weight(1f), color = TextPrimary)
                    Switch(draft.goalLockEnabled, { update(draft.copy(goalLockEnabled = it)) })
                }
                Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                    Text("Allow snoozing", Modifier.weight(1f), color = TextPrimary)
                    Switch(draft.snoozeEnabled, { update(draft.copy(snoozeEnabled = it)) })
                }
                Text("Snooze settings", style = MaterialTheme.typography.titleSmall, color = Teal)
                GoalNumberField("Duration (seconds)", draft.snoozeDurationMs / 1000, { update(draft.copy(snoozeDurationMs = (it ?: 0L) * 1000)) })
                GoalNumberField("Turn limit (optional)", draft.snoozeTurnLimit?.toLong(), { update(draft.copy(snoozeTurnLimit = it?.toInt())) })
                GoalNumberField("Budget (minutes, optional)", draft.snoozeBudgetCapMs?.div(60_000), { update(draft.copy(snoozeBudgetCapMs = it?.times(60_000))) })
                GoalNumberField("Refill yield (seconds)", draft.snoozeRefillYieldMs / 1000, { update(draft.copy(snoozeRefillYieldMs = (it ?: 0L) * 1000)) })
                GoalNumberField("Refill time (seconds)", draft.snoozeRefillElapsedMs / 1000, { update(draft.copy(snoozeRefillElapsedMs = (it ?: 0L) * 1000)) })
                Text("Outcomes: completed ${draft.outcomeCounts.completed}, not done ${draft.outcomeCounts.notDone}", color = TextMuted, style = MaterialTheme.typography.bodySmall)
                ControlStatusText(controlState)
            }
        },
        confirmButton = { TextButton(onSave, enabled = controlState.status != ControlStatus.PENDING) { Text("Save") } },
        dismissButton = { TextButton(onDismiss) { Text("Cancel") } },
    )
}

@Composable
private fun GoalNumberField(label: String, value: Long?, onValue: (Long?) -> Unit) {
    var text by remember(value) { mutableStateOf(value?.toString().orEmpty()) }
    OutlinedTextField(text, { next -> text = next; onValue(next.toLongOrNull()) }, Modifier.fillMaxWidth(), singleLine = true, label = { Text(label) })
}

@Composable
private fun ControlStatusText(state: ControlState) {
    val text = when (state.status) {
        ControlStatus.IDLE -> ""
        ControlStatus.PENDING -> "Saving…"
        ControlStatus.ACCEPTED -> "Saved."
        ControlStatus.REJECTED -> state.message ?: "Change rejected."
        ControlStatus.UNCONFIRMED -> state.message ?: "The router may have accepted this change."
    }
    if (text.isNotEmpty()) Text(text, color = if (state.status == ControlStatus.REJECTED) Color(0xFFFF8A80) else TextMuted, style = MaterialTheme.typography.bodySmall)
}

@Composable
private fun AgentEventChip(event: AgentTimelineEvent) {
    var showDialog by remember { mutableStateOf(false) }
    val formattedTime = remember(event.timestamp) { formatEventTimestamp(event.timestamp) }

    val toolIcon = when (event.toolName?.lowercase() ?: event.kind.lowercase()) {
        "bash" -> Icons.Default.Terminal
        "read", "write", "edit" -> Icons.Default.Description
        "subagent", "mecha_worker" -> Icons.Default.AccountTree
        "goal" -> Icons.Default.Flag
        "reminder", "mecha_reminder" -> Icons.Default.Schedule
        "context_reset", "reset" -> Icons.Default.Sync
        else -> Icons.Default.Build
    }

    val outcomeNormalized = event.outcome?.lowercase().orEmpty()
    val borderStroke = when {
        outcomeNormalized == "error" || outcomeNormalized == "failure" -> BorderStroke(1.5.dp, Color(0xFFEF4444))
        outcomeNormalized in setOf("running", "active", "in_progress") -> BorderStroke(1.dp, Color(0xFFF59E0B))
        else -> BorderStroke(1.dp, Color(0xFF10B981).copy(alpha = 0.5f))
    }

    Surface(
        shape = RoundedCornerShape(8.dp),
        color = SurfaceDark,
        border = borderStroke,
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 4.dp, vertical = 2.dp)
            .clickable { showDialog = true }
    ) {
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .padding(horizontal = 10.dp, vertical = 8.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(8.dp)
        ) {
            Icon(
                imageVector = toolIcon,
                contentDescription = null,
                tint = Periwinkle,
                modifier = Modifier.size(16.dp)
            )

            val toolLabel = event.toolName?.takeIf { it.isNotBlank() } ?: event.kind.takeIf { it.isNotBlank() } ?: "event"
            Surface(
                shape = RoundedCornerShape(4.dp),
                color = Periwinkle.copy(alpha = 0.2f)
            ) {
                Text(
                    text = toolLabel,
                    color = Periwinkle,
                    fontSize = 11.sp,
                    fontWeight = FontWeight.Bold,
                    modifier = Modifier.padding(horizontal = 6.dp, vertical = 2.dp)
                )
            }

            Text(
                text = event.summary,
                color = TextMuted,
                fontSize = 12.sp,
                maxLines = 2,
                overflow = TextOverflow.Ellipsis,
                modifier = Modifier.weight(1f)
            )

            if (formattedTime.isNotBlank()) {
                Text(
                    text = formattedTime,
                    color = TextMuted.copy(alpha = 0.7f),
                    fontSize = 10.sp
                )
            }

            Icon(
                imageVector = Icons.Default.ChevronRight,
                contentDescription = "Details",
                tint = TextMuted.copy(alpha = 0.5f),
                modifier = Modifier.size(16.dp)
            )
        }
    }

    if (showDialog) {
        AgentEventDetailDialog(event = event, onDismiss = { showDialog = false })
    }
}

@Composable
private fun AgentEventDetailDialog(event: AgentTimelineEvent, onDismiss: () -> Unit) {
    val formattedTime = remember(event.timestamp) { formatEventTimestamp(event.timestamp) }
    val toolIcon = when (event.toolName?.lowercase() ?: event.kind.lowercase()) {
        "bash" -> Icons.Default.Terminal
        "read", "write", "edit" -> Icons.Default.Description
        "subagent", "mecha_worker" -> Icons.Default.AccountTree
        "goal" -> Icons.Default.Flag
        "reminder", "mecha_reminder" -> Icons.Default.Schedule
        "context_reset", "reset" -> Icons.Default.Sync
        else -> Icons.Default.Build
    }
    val outcomeNormalized = event.outcome?.lowercase().orEmpty()
    val isSuccess = outcomeNormalized in setOf("success", "completed", "done")
    val isError = outcomeNormalized in setOf("error", "failed", "interrupted")
    val outcomeBadgeColor = when {
        isSuccess -> Color(0xFF4ADE80)
        isError -> Color(0xFFFF8A80)
        else -> Color(0xFFFFC857)
    }
    val outcomeBadgeText = when {
        isSuccess -> "success"
        isError -> "error"
        outcomeNormalized.isNotBlank() -> outcomeNormalized
        else -> "running"
    }

    AlertDialog(
        onDismissRequest = onDismiss,
        containerColor = SurfaceRaised,
        title = {
            Row(
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(8.dp),
                modifier = Modifier.fillMaxWidth()
            ) {
                Icon(toolIcon, contentDescription = null, tint = Periwinkle, modifier = Modifier.size(20.dp))
                val toolLabel = event.toolName?.takeIf { it.isNotBlank() } ?: event.kind.takeIf { it.isNotBlank() } ?: "event"
                Surface(
                    shape = RoundedCornerShape(4.dp),
                    color = Periwinkle.copy(alpha = 0.2f)
                ) {
                    Text(
                        text = toolLabel,
                        color = Periwinkle,
                        fontSize = 12.sp,
                        fontWeight = FontWeight.Bold,
                        modifier = Modifier.padding(horizontal = 6.dp, vertical = 2.dp)
                    )
                }
                Surface(
                    shape = RoundedCornerShape(4.dp),
                    color = outcomeBadgeColor.copy(alpha = 0.15f)
                ) {
                    Text(
                        text = outcomeBadgeText,
                        color = outcomeBadgeColor,
                        fontSize = 11.sp,
                        fontWeight = FontWeight.Medium,
                        modifier = Modifier.padding(horizontal = 6.dp, vertical = 2.dp)
                    )
                }
                Spacer(Modifier.weight(1f))
                if (formattedTime.isNotBlank()) {
                    Text(formattedTime, color = TextMuted, fontSize = 11.sp)
                }
            }
        },
        text = {
            val scrollState = rememberScrollState()
            Column(
                modifier = Modifier
                    .fillMaxWidth()
                    .verticalScroll(scrollState),
                verticalArrangement = Arrangement.spacedBy(10.dp)
            ) {
                if (event.summary.isNotBlank()) {
                    Text(
                        text = event.summary,
                        color = TextPrimary,
                        fontSize = 13.sp
                    )
                }
                val hasToolInput = !event.toolInput.isNullOrBlank()
                val hasOutput = !event.output.isNullOrBlank()

                if (hasToolInput) {
                    Column(
                        modifier = Modifier
                            .fillMaxWidth()
                            .clip(RoundedCornerShape(6.dp))
                            .background(Ink)
                            .padding(8.dp),
                        verticalArrangement = Arrangement.spacedBy(4.dp)
                    ) {
                        Text("PARAMETERS", color = TextMuted, fontSize = 10.sp, fontWeight = FontWeight.Bold)
                        Text(
                            text = event.toolInput.orEmpty(),
                            color = TextPrimary,
                            fontFamily = FontFamily.Monospace,
                            fontSize = 11.sp
                        )
                    }
                }
                if (hasOutput) {
                    Column(
                        modifier = Modifier
                            .fillMaxWidth()
                            .clip(RoundedCornerShape(6.dp))
                            .background(Ink)
                            .padding(8.dp),
                        verticalArrangement = Arrangement.spacedBy(4.dp)
                    ) {
                        Text("RESULT", color = TextMuted, fontSize = 10.sp, fontWeight = FontWeight.Bold)
                        Text(
                            text = event.output.orEmpty(),
                            color = if (isError) Color(0xFFFF8A80) else Teal,
                            fontFamily = FontFamily.Monospace,
                            fontSize = 11.sp
                        )
                    }
                }
            }
        },
        confirmButton = {
            TextButton(onClick = onDismiss) {
                Text("Close", color = Periwinkle)
            }
        }
    )
}

@Composable
private fun MessageBubble(message: ChatMessage, routerBase: String, imageLoader: RemoteImageLoader) {
    val outgoing = message.direction == Direction.OUTGOING
    var viewerImage by remember(message.key) { mutableStateOf<Pair<ImageReference, ByteArray>?>(null) }
    Column(Modifier.fillMaxWidth(), horizontalAlignment = if (outgoing) Alignment.End else Alignment.Start) {
        Row(
            modifier = Modifier.padding(horizontal = 4.dp, vertical = 2.dp),
            horizontalArrangement = Arrangement.spacedBy(8.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text(
                if (!outgoing) humanName(message.sender) else when (message.deliveryStatus) {
                    OutboundDeliveryStatus.PENDING -> "You · sending"
                    OutboundDeliveryStatus.FAILED -> "You · not sent"
                    OutboundDeliveryStatus.UNCERTAIN -> "You · delivery not confirmed"
                    else -> "You"
                },
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
                    MarkdownBody(message.body, outgoing)
                }
                message.attachments.forEach { attachment ->
                    RemoteImage(attachment, routerBase, imageLoader) { bytes -> viewerImage = attachment to bytes }
                }
            }
        }
    }
    viewerImage?.let { (attachment, bytes) ->
        ImageViewer(attachment, bytes) { viewerImage = null }
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
private fun RemoteImage(
    attachment: ImageReference,
    routerBase: String,
    imageLoader: RemoteImageLoader,
    onOpen: (ByteArray) -> Unit,
) {
    var result by remember(routerBase, attachment.url) { mutableStateOf<ImageFetchResult?>(null) }
    LaunchedEffect(routerBase, attachment.url, attachment.mimeType) {
        result = imageLoader.load(
            routerIdentity = routerBase,
            routerBase = routerBase,
            url = attachment.url,
            expectedMimeType = attachment.mimeType,
        )
    }
    when (val loaded = result) {
        null -> Text("Loading image…", color = TextMuted, style = MaterialTheme.typography.bodySmall)
        is ImageFetchResult.Failure -> Text("Image unavailable.", color = TextMuted, style = MaterialTheme.typography.bodySmall)
        is ImageFetchResult.Success -> {
            val bitmap = remember(loaded.bytes) { decodeBoundedBitmap(loaded.bytes)?.asImageBitmap() }
            if (bitmap == null) {
                Text("Image unavailable.", color = TextMuted, style = MaterialTheme.typography.bodySmall)
            } else {
                Image(
                    bitmap = bitmap,
                    contentDescription = attachment.name,
                    modifier = Modifier
                        .fillMaxWidth()
                        .heightIn(max = 320.dp)
                        .clip(RoundedCornerShape(12.dp))
                        .clickable { onOpen(loaded.bytes) },
                    contentScale = ContentScale.Fit,
                )
            }
        }
    }
}

@Composable
private fun ImageViewer(attachment: ImageReference, bytes: ByteArray, onDismiss: () -> Unit) {
    var viewerState by remember(attachment.url) { mutableStateOf(ImageViewerState().opened()) }
    val bitmap = remember(bytes) { decodeBoundedBitmap(bytes)?.asImageBitmap() }
    BackHandler(onBack = onDismiss)
    Dialog(onDismissRequest = onDismiss) {
        Surface(modifier = Modifier.fillMaxSize(), color = Color.Black) {
            Box(
                modifier = Modifier
                    .fillMaxSize()
                    .pointerInput(attachment.url) {
                        detectTransformGestures { _, pan, zoom, _ ->
                            viewerState = viewerState.zoomBy(zoom).panBy(pan.x, pan.y)
                        }
                    },
            ) {
                if (bitmap != null) {
                    Image(
                        bitmap = bitmap,
                        contentDescription = attachment.name,
                        modifier = Modifier
                            .fillMaxSize()
                            .graphicsLayer {
                                scaleX = viewerState.scale
                                scaleY = viewerState.scale
                                translationX = viewerState.offsetX
                                translationY = viewerState.offsetY
                            },
                        contentScale = ContentScale.Fit,
                    )
                }
                IconButton(onClick = onDismiss, modifier = Modifier.align(Alignment.TopEnd).padding(8.dp)) {
                    Icon(Icons.Filled.Close, contentDescription = "Close image")
                }
            }
        }
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
    val editingRouter = routers.firstOrNull { it.identity == editingEndpoint }
    var endpoint by remember(editingEndpoint) { mutableStateOf(editingRouter?.url.orEmpty()) }
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
                items(routers, key = { it.identity }) { router ->
                    Card(
                        Modifier.fillMaxWidth().clickable { onSelect(router.identity) },
                        colors = CardDefaults.cardColors(containerColor = if (router.identity == activeRouter) SurfaceRaised else SurfaceDark),
                        shape = RoundedCornerShape(18.dp),
                    ) {
                        Row(Modifier.fillMaxWidth().padding(14.dp), verticalAlignment = Alignment.CenterVertically) {
                            Column(Modifier.weight(1f)) {
                                Text(router.displayName, style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.SemiBold)
                                Text(router.url, style = MaterialTheme.typography.bodySmall, color = TextMuted)
                                if (router.identity == activeRouter) Text("Active", style = MaterialTheme.typography.bodySmall, color = Teal)
                            }
                            IconButton({ onEdit(router.identity) }) {
                                Icon(Icons.Default.Edit, "Edit router", tint = Periwinkle)
                            }
                            IconButton({ onRemove(router.identity) }) {
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

private val REMINDER_ID = Regex("^[A-Za-z0-9][A-Za-z0-9._-]{0,98}$")
private val REMINDER_FRAME_ID = Regex("^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
private val ACTIVE_REMINDER_STATES = setOf("scheduled", "due", "message_queued")
private const val MAX_REMINDER_ROWS = 1_000

private fun encodeReminderId(id: String): String = java.net.URLEncoder.encode(id, "UTF-8").replace("+", "%20")

internal fun visibleRemindersForPeer(reminders: List<ReminderSummary>, peer: String): List<ReminderSummary> =
    reminders.filter { it.target == peer && it.enabled && it.state in ACTIVE_REMINDER_STATES }

internal fun validateReminderCreate(body: String, delayMinutes: Long): String? = when {
    body.trim().isEmpty() -> "Enter reminder text."
    delayMinutes !in 1..10_080 -> "Enter a delay from 1 to 10080 minutes."
    else -> null
}

internal data class ReminderPatchFields(
    val intervalMs: Long? = null,
    val nextDueAt: String? = null,
    val body: String? = null,
    val subject: String? = null,
    val target: String? = null,
    val includeIntervalMs: Boolean = false,
    val includeNextDueAt: Boolean = false,
    val includeBody: Boolean = false,
    val includeSubject: Boolean = false,
    val includeTarget: Boolean = false,
)

internal fun validateReminderPatch(patch: ReminderPatchFields): String? {
    if (!patch.includeIntervalMs && !patch.includeNextDueAt && !patch.includeBody && !patch.includeSubject && !patch.includeTarget) {
        return "Change at least one reminder field."
    }
    if (patch.includeIntervalMs && patch.intervalMs != null && patch.intervalMs <= 0) return "Enter a positive reminder interval."
    if (patch.includeNextDueAt && patch.nextDueAt.isNullOrBlank()) return "Enter a reminder due time."
    if (patch.includeBody && patch.body?.trim().isNullOrEmpty()) return "Enter reminder text."
    if (patch.includeTarget && patch.target?.let(AGENT_ID::matches) != true) return "Enter a valid reminder target."
    return null
}

internal data class ReminderPatchEntry(val key: String, val value: Any?)

internal fun reminderPatchEntries(patch: ReminderPatchFields): List<ReminderPatchEntry> {
    require(validateReminderPatch(patch) == null)
    return buildList {
        if (patch.includeIntervalMs) add(ReminderPatchEntry("interval_ms", patch.intervalMs))
        if (patch.includeNextDueAt) add(ReminderPatchEntry("next_due_at", patch.nextDueAt))
        if (patch.includeBody) add(ReminderPatchEntry("body", patch.body?.trim()))
        if (patch.includeSubject) add(ReminderPatchEntry("subject", patch.subject?.trim()?.takeIf { it.isNotEmpty() }))
        if (patch.includeTarget) add(ReminderPatchEntry("target", patch.target))
    }
}

internal fun buildReminderUpdatePayload(patch: ReminderPatchFields): JSONObject = JSONObject().apply {
    reminderPatchEntries(patch).forEach { entry -> put(entry.key, entry.value ?: JSONObject.NULL) }
}

internal fun buildReminderUpdatePayload(body: String, subject: String?): JSONObject =
    buildReminderUpdatePayload(ReminderPatchFields(body = body, subject = subject, includeBody = true, includeSubject = true))

internal fun validateReminderUpdate(reminder: ReminderSummary, body: String, subject: String?): String? {
    val normalizedBody = body.trim()
    val normalizedSubject = subject?.trim()?.takeIf { it.isNotEmpty() }
    return validateReminderPatch(ReminderPatchFields(body = normalizedBody, subject = normalizedSubject, includeBody = true, includeSubject = true))
        ?: if (reminder.body == normalizedBody && reminder.subject == normalizedSubject) "Change at least one reminder field." else null
}

internal fun reminderCrudResponseAccepted(operation: String, status: Int): Boolean =
    if (operation == "delete") status == 204 else status in 200..299

internal fun reminderErrorCode(body: String): String? = runCatching {
    JSONObject(body).optJSONObject("error")?.optString("code")?.takeIf { it.isNotBlank() }
}.getOrNull()

internal fun reminderErrorMessage(code: String?, status: Int): String = when (code) {
    "reminder_not_cancelable" -> "Reminder is already linked to a delivery."
    "not_found" -> "That reminder no longer exists."
    "invalid_reminder" -> "Enter valid reminder details."
    else -> "Reminder request failed ($status)."
}

internal fun reminderCrudErrorState(status: Int, code: String?): ReminderUiState =
    ReminderUiState(ReminderStatus.REJECTED, reminderErrorMessage(code, status), code)

internal fun reminderCrudErrorStateFromBody(status: Int, body: String): ReminderUiState =
    reminderCrudErrorState(status, reminderErrorCode(body))

internal fun reminderCrudTimeoutState(operation: String): ReminderUiState =
    ReminderUiState(ReminderStatus.TIMEOUT, "The reminder ${if (operation == "delete") "deletion" else "change"} timed out.", "control_timeout")

internal fun matchesPendingReminderCrud(
    pending: PendingReminderCrud,
    router: String,
    generation: Int,
    selectedPeer: String,
    targetInstanceId: String?,
): Boolean = pending.router == router
    && pending.connectionGeneration == generation
    && pending.peer == selectedPeer
    && (pending.targetInstanceId == null || pending.targetInstanceId == targetInstanceId)

internal fun reminderListResponseIsCurrent(
    responseSerial: Int,
    currentSerial: Int,
    responseGeneration: Int,
    currentGeneration: Int,
    responseRouter: String,
    currentRouter: String,
): Boolean = responseSerial == currentSerial
    && responseGeneration == currentGeneration
    && responseRouter == currentRouter

internal data class ReminderCreatePayload(
    val id: String,
    val target: String,
    val creator: String,
    val subject: String?,
    val body: String,
    val createdAt: String,
    val nextDueAt: String,
    val intervalMs: Long?,
)

internal fun reminderCreatePayload(
    creator: String,
    target: String,
    body: String,
    subject: String,
    delayMinutes: Long,
    recurring: Boolean,
    id: String,
    nowMs: Long = System.currentTimeMillis(),
): ReminderCreatePayload {
    require(REMINDER_ID.matches(id))
    require(AGENT_ID.matches(creator) && AGENT_ID.matches(target))
    require(validateReminderCreate(body, delayMinutes) == null)
    val createdAt = Instant.ofEpochMilli(nowMs).truncatedTo(ChronoUnit.MILLIS)
    return ReminderCreatePayload(
        id = id,
        target = target,
        creator = creator,
        subject = subject.trim().takeIf { it.isNotEmpty() },
        body = body.trim(),
        createdAt = createdAt.toString(),
        nextDueAt = createdAt.plus(delayMinutes, ChronoUnit.MINUTES).toString(),
        intervalMs = if (recurring) delayMinutes * 60_000L else null,
    )
}

internal fun buildReminderCreatePayload(
    creator: String,
    target: String,
    body: String,
    subject: String,
    delayMinutes: Long,
    recurring: Boolean,
    id: String,
    nowMs: Long = System.currentTimeMillis(),
): JSONObject = reminderCreatePayload(creator, target, body, subject, delayMinutes, recurring, id, nowMs).let { payload ->
    JSONObject()
        .put("id", payload.id)
        .put("target", payload.target)
        .put("creator", payload.creator)
        .put("body", payload.body)
        .put("created_at", payload.createdAt)
        .put("next_due_at", payload.nextDueAt)
        .put("interval_ms", payload.intervalMs ?: JSONObject.NULL)
        .also { if (payload.subject != null) it.put("subject", payload.subject) }
}

internal fun parseReminderListResponse(body: String): List<ReminderSummary>? = runCatching {
    val values = JSONObject(body).optJSONArray("reminders") ?: return@runCatching null
    if (values.length() > MAX_REMINDER_ROWS) return@runCatching null
    List(values.length()) { index -> parseReminderSummary(values.optJSONObject(index)) ?: return@runCatching null }
}.getOrNull()

internal fun parseSavedReminder(body: String): ReminderSummary? = runCatching {
    parseReminderSummary(JSONObject(body).optJSONObject("reminder"))
}.getOrNull()

private fun parseReminderSummary(value: JSONObject?): ReminderSummary? {
    val allowed = setOf("id", "target", "creator", "subject", "body", "created_at", "next_due_at", "interval_ms", "sentinel", "state", "linked_message_id", "linked_message_state", "occurrence_number", "attempt_count", "last_attempt_at", "enabled")
    if (value == null || value.keys().asSequence().any { it !in allowed }) return null
    val id = value.optString("id")
    val target = value.optString("target")
    val creator = value.optString("creator")
    val body = value.optString("body")
    val createdAt = value.optString("created_at")
    val nextDueAt = value.optString("next_due_at")
    val state = value.optString("state")
    if (!REMINDER_ID.matches(id) || !AGENT_ID.matches(target) || !AGENT_ID.matches(creator)
        || body.isEmpty() || createdAt.isEmpty() || nextDueAt.isEmpty() || state.isEmpty()) return null
    val subject = when {
        !value.has("subject") || value.isNull("subject") -> null
        value.get("subject") is String -> value.optString("subject")
        else -> return null
    }
    val interval = when {
        !value.has("interval_ms") || value.isNull("interval_ms") -> null
        else -> {
            val raw = value.get("interval_ms") as? Number ?: return null
            val number = raw.toDouble()
            raw.toLong().takeIf { number.isFinite() && number == it.toDouble() && it > 0 }
        }
    }
    if (value.has("interval_ms") && !value.isNull("interval_ms") && interval == null) return null
    val enabled = if (value.has("enabled")) {
        if (value.get("enabled") !is Boolean) return null else value.getBoolean("enabled")
    } else true
    return ReminderSummary(id, target, creator, subject, body, createdAt, nextDueAt, interval, state, enabled)
}

internal fun formatReminderCountdown(nextDueAt: String?, nowMs: Long = System.currentTimeMillis()): String {
    val dueMs = runCatching { Instant.parse(nextDueAt.orEmpty()).toEpochMilli() }.getOrNull() ?: return "Due time unavailable"
    var remaining = maxOf(0L, kotlin.math.ceil((dueMs - nowMs) / 1_000.0).toLong())
    if (remaining == 0L) return "Due now"
    val days = remaining / 86_400
    remaining %= 86_400
    val hours = remaining / 3_600
    remaining %= 3_600
    val minutes = remaining / 60
    remaining %= 60
    return buildList {
        if (days > 0) add("${days}d")
        if (hours > 0) add("${hours}h")
        if (minutes > 0) add("${minutes}m")
        if (remaining > 0) add("${remaining}s")
    }.joinToString(" ").let { "Due in $it" }
}

internal fun reminderRecurrenceLabel(intervalMs: Long?): String = if (intervalMs != null && intervalMs > 0) "Recurring" else "One-shot"

internal data class ReminderLockControlPayload(
    val version: String,
    val frameId: String,
    val type: String,
    val agentId: String,
    val instanceId: String,
    val leaseId: String,
    val targetAgentId: String,
    val targetInstanceId: String,
    val controlId: String,
    val reminderId: String,
    val action: String,
)

internal fun reminderLockControlPayload(
    frameId: String,
    agentId: String,
    instanceId: String,
    leaseId: String,
    targetAgentId: String,
    targetInstanceId: String,
    controlId: String,
    reminderId: String,
    action: String,
): ReminderLockControlPayload {
    require(REMINDER_FRAME_ID.matches(frameId) && AGENT_ID.matches(agentId) && REMINDER_FRAME_ID.matches(instanceId) && REMINDER_FRAME_ID.matches(leaseId))
    require(AGENT_ID.matches(targetAgentId) && REMINDER_FRAME_ID.matches(targetInstanceId) && REMINDER_FRAME_ID.matches(controlId) && REMINDER_ID.matches(reminderId))
    require(action == "set" || action == "clear")
    return ReminderLockControlPayload("v1", frameId, "reminder_lock_control", agentId, instanceId, leaseId, targetAgentId, targetInstanceId, controlId, reminderId, action)
}

internal fun buildReminderLockControlFrame(
    frameId: String,
    agentId: String,
    instanceId: String,
    leaseId: String,
    targetAgentId: String,
    targetInstanceId: String,
    controlId: String,
    reminderId: String,
    action: String,
): JSONObject = reminderLockControlPayload(frameId, agentId, instanceId, leaseId, targetAgentId, targetInstanceId, controlId, reminderId, action).let { frame ->
    JSONObject()
        .put("version", frame.version).put("frame_id", frame.frameId).put("type", frame.type)
        .put("agent_id", frame.agentId).put("instance_id", frame.instanceId).put("lease_id", frame.leaseId)
        .put("target_agent_id", frame.targetAgentId).put("target_instance_id", frame.targetInstanceId)
        .put("control_id", frame.controlId).put("reminder_id", frame.reminderId).put("action", frame.action)
}

internal fun parseReminderLockResult(value: JSONObject): ParsedReminderLockResult? {
    val allowed = setOf("version", "frame_id", "type", "agent_id", "instance_id", "lease_id", "source_agent_id", "control_id", "reminder_id", "accepted", "error_code")
    val required = allowed - "error_code"
    if (!value.keys().asSequence().all { it in allowed } || !required.all(value::has) || value.optString("version") != "v1" || value.optString("type") != "reminder_lock_control_result") return null
    val accepted = if (value.get("accepted") !is Boolean) return null else value.getBoolean("accepted")
    val errorCode = if (value.has("error_code")) value.optString("error_code").takeIf { it.isNotBlank() } ?: return null else null
    if (accepted && errorCode != null || !accepted && errorCode == null) return null
    val agent = value.optString("agent_id")
    val instance = value.optString("instance_id")
    val lease = value.optString("lease_id")
    val source = value.optString("source_agent_id")
    val control = value.optString("control_id")
    val reminder = value.optString("reminder_id")
    if (!AGENT_ID.matches(agent) || !REMINDER_FRAME_ID.matches(instance) || !REMINDER_FRAME_ID.matches(lease)
        || !AGENT_ID.matches(source) || !REMINDER_FRAME_ID.matches(control) || !REMINDER_ID.matches(reminder)
        || !REMINDER_FRAME_ID.matches(value.optString("frame_id"))) return null
    return ParsedReminderLockResult(agent, instance, lease, source, control, reminder, accepted, errorCode)
}

internal fun matchesPendingReminderLock(
    pending: PendingReminderLock,
    result: ParsedReminderLockResult,
    sourceAgentId: String,
    currentLeaseId: String,
): Boolean = pending.peer == result.agentId
    && pending.targetInstanceId == result.instanceId
    && pending.reminderId == result.reminderId
    && pending.controlId == result.controlId
    && pending.router.isNotBlank()
    && result.sourceAgentId == sourceAgentId
    && result.leaseId == currentLeaseId

internal fun mergeHydrationConversationMessages(
    persisted: List<ChatMessage>,
    inMemory: List<ChatMessage>,
    recovery: List<ChatMessage> = emptyList(),
    nowMs: Long = System.currentTimeMillis(),
): List<ChatMessage> = mergeConversationMessages(
    mergeConversationMessages(recovery, inMemory, nowMs),
    persisted,
    nowMs,
)

internal fun reconcileConversationMessages(
    existing: List<ChatMessage>,
    snapshot: List<ChatMessage>,
    highWaterSequence: Long,
    nowMs: Long = System.currentTimeMillis(),
): List<ChatMessage> {
    val snapshotKeys = snapshot.map(ChatMessage::key).toSet()
    val retainedLive = existing.filter { message ->
        message.sequence == 0L || message.sequence > highWaterSequence || message.key in snapshotKeys
    }
    return mergeConversationMessages(retainedLive, snapshot, nowMs)
}

internal fun sameConversationMessageIdentity(left: ChatMessage, right: ChatMessage): Boolean =
    left.id == right.id && (left.incarnationId == right.incarnationId || left.incarnationId.isBlank() || right.incarnationId.isBlank())

internal fun mergeConversationMessages(
    existing: List<ChatMessage>,
    incoming: List<ChatMessage>,
    nowMs: Long = System.currentTimeMillis(),
): List<ChatMessage> {
    val merged = existing.toMutableList()
    incoming.forEach { message ->
        val index = merged.indexOfFirst { current -> sameConversationMessageIdentity(current, message) }
        if (index < 0) {
            merged += message
            return@forEach
        }
        val current = merged[index]
        if (current.direction == Direction.OUTGOING && current.deliveryStatus != OutboundDeliveryStatus.NONE) {
            if (current.deliveryStatus == OutboundDeliveryStatus.PENDING && sameOutgoingEnvelope(current, message)) {
                merged[index] = message.copy(deliveryStatus = OutboundDeliveryStatus.SENT)
            } else if (current.deliveryStatus != OutboundDeliveryStatus.SENT) {
                // A failed or uncertain local send is changed only by its correlated reducer event.
                return@forEach
            } else if (!sameOutgoingEnvelope(current, message)) {
                // The router message id is an idempotency key; a different envelope is not its echo.
                return@forEach
            } else {
                merged[index] = message.copy(deliveryStatus = OutboundDeliveryStatus.SENT)
            }
        } else if (isCanonicalConversationMessage(current) && !isCanonicalConversationMessage(message)) {
            // Delivery envelopes are identity proofs, not an authority to replace a richer history row.
            return@forEach
        } else if (isCanonicalConversationMessage(current)
            && isCanonicalConversationMessage(message)
            && current.incarnationId.isNotBlank()
            && current.incarnationId == message.incarnationId
            && current != message
            && message.sequence <= current.sequence
        ) {
            // Same-incarnation conflicts have no ordering evidence. Keep the existing canonical row.
            return@forEach
        } else {
            merged[index] = message
        }
    }
    return pruneConversationHistory(
        merged.sortedWith(compareBy<ChatMessage> { if (it.sequence == 0L) Long.MAX_VALUE else it.sequence }.thenBy { it.createdAt }),
        nowMs,
    )
}

private val BRACKETED_IPV6_ENDPOINT = Regex("^\\[([^]]+)](?::([0-9]{1,5}))?$")

private fun normalizedRouterAuthority(authority: String): Pair<String, String>? {
    val ipv6 = BRACKETED_IPV6_ENDPOINT.matchEntire(authority)
    if (ipv6 != null) {
        val host = ipv6.groupValues[1].lowercase()
        val port = ipv6.groupValues[2].ifBlank { DEFAULT_ROUTER_PORT.toString() }.toIntOrNull() ?: return null
        return if (port in 1..65535) host to "[$host]:$port" else null
    }
    if (authority == "::1") return "::1" to "[::1]:$DEFAULT_ROUTER_PORT"
    val match = ROUTER_ENDPOINT.matchEntire(authority) ?: return null
    val host = match.groupValues[1].lowercase()
    val port = match.groupValues[2].ifBlank { DEFAULT_ROUTER_PORT.toString() }.toIntOrNull() ?: return null
    return if (port in 1..65535) host to "$host:$port" else null
}

private fun normalizedRouterEndpoint(
    candidate: String,
    allowCleartextLoopback: Boolean = BuildConfig.DEBUG,
): Pair<String, String>? {
    val trimmed = candidate.trim()
    val hasScheme = trimmed.contains("://")
    val scheme = when {
        trimmed.startsWith("https://", ignoreCase = true) -> "https"
        trimmed.startsWith("http://", ignoreCase = true) -> "http"
        hasScheme -> return null
        else -> "http"
    }
    val authority = if (hasScheme) trimmed.substringAfter("://") else trimmed
    val (_, endpoint) = normalizedRouterAuthority(authority) ?: return null
    if (scheme == "http" && !allowCleartextLoopback) return null
    return scheme to endpoint
}

internal fun routerEndpointValidationMessage(
    candidate: String,
    allowCleartextLoopback: Boolean = BuildConfig.DEBUG,
): String? {
    if (normalizedRouterEndpoint(candidate, allowCleartextLoopback) != null) return null
    val trimmed = candidate.trim()
    val scheme = when {
        trimmed.startsWith("https://", ignoreCase = true) -> "https"
        trimmed.startsWith("http://", ignoreCase = true) -> "http"
        trimmed.contains("://") -> "other"
        else -> "implicit-http"
    }
    return when {
        scheme == "https" -> "Enter a valid HTTPS router URL without credentials, a path, or a query."
        (scheme == "http" || scheme == "implicit-http") && !allowCleartextLoopback -> "Release builds require an HTTPS router URL."
        else -> "Enter a valid HTTP or HTTPS router URL without credentials, a path, or a query."
    }
}

internal fun normalizeRouterEndpointCandidate(
    candidate: String,
    allowCleartextLoopback: Boolean = BuildConfig.DEBUG,
): String? = normalizedRouterEndpoint(candidate, allowCleartextLoopback)?.second

internal fun routerSchemeForCandidate(
    candidate: String,
    allowCleartextLoopback: Boolean = BuildConfig.DEBUG,
): String? = normalizedRouterEndpoint(candidate, allowCleartextLoopback)?.first

internal fun routerHttpUrl(endpoint: String, scheme: String): String = when (scheme) {
    "http" -> {
        require(BuildConfig.DEBUG) { "Release builds require an HTTPS router URL." }
        "http://$endpoint"
    }
    "https" -> "https://$endpoint"
    else -> error("Unsupported router scheme")
}

internal fun conversationHistoryUrl(routerBaseUrl: String, owner: String, peer: String): okhttp3.HttpUrl {
    val base = runCatching { routerBaseUrl.toHttpUrl() }
        .getOrElse { throw IllegalArgumentException("Router URL is invalid", it) }
    return base.newBuilder()
        .addPathSegments("v1/conversations")
        .addQueryParameter("owner", owner)
        .addQueryParameter("peer", peer)
        .build()
}

internal fun historyHttpClient(base: OkHttpClient): OkHttpClient = base.newBuilder()
    .retryOnConnectionFailure(false)
    .callTimeout(HISTORY_REQUEST_TIMEOUT_MS, TimeUnit.MILLISECONDS)
    .followRedirects(false)
    .followSslRedirects(false)
    .build()

internal fun routerWebSocketUrl(endpoint: String, scheme: String): String = when (scheme) {
    "http" -> {
        require(BuildConfig.DEBUG) { "Release builds require a WSS router connection." }
        "ws://$endpoint/v1/agents"
    }
    "https" -> "wss://$endpoint/v1/agents"
    else -> error("Unsupported router scheme")
}

private fun parseRouterEndpointScheme(
    stored: String,
    allowCleartextLoopback: Boolean = BuildConfig.DEBUG,
): Pair<String, String>? = normalizedRouterEndpoint(stored, allowCleartextLoopback)

internal fun parseRouterConfig(stored: String): RouterConfig? = parseRouterConfig(stored, BuildConfig.DEBUG)

internal fun parseRouterConfig(
    stored: String,
    allowCleartextLoopback: Boolean,
): RouterConfig? {
    val parts = stored.split(ROUTER_RECORD_SEPARATOR, limit = 2)
    val alias = if (parts.size == 2) parts[0].trim() else ""
    val rawEndpoint = if (parts.size == 2) parts[1] else parts[0]
    val (scheme, endpoint) = parseRouterEndpointScheme(rawEndpoint, allowCleartextLoopback) ?: return null
    return RouterConfig(alias, endpoint, scheme)
}

internal fun routerConfigAllowed(config: RouterConfig, allowCleartextLoopback: Boolean): Boolean =
    config.scheme != "http" || allowCleartextLoopback

internal fun routerConfigsFromRecords(
    records: Iterable<String>,
    allowCleartextLoopback: Boolean,
): List<RouterConfig> = records
    .mapNotNull { parseRouterConfig(it, allowCleartextLoopback) }
    .distinctBy { it.identity }
    .sortedBy { it.displayName.lowercase() }

internal fun routerRecordsForPersistence(
    routers: Iterable<RouterConfig>,
    allowCleartextLoopback: Boolean,
): Set<String> = routers
    .filter { routerConfigAllowed(it, allowCleartextLoopback) }
    .map { it.persist() }
    .toSet()

internal fun savedRouterCandidates(saved: String?, routers: List<RouterConfig>): List<RouterConfig> {
    if (saved == null) return emptyList()
    routers.firstOrNull { it.identity == saved }?.let { return listOf(it) }
    return routers.filter { it.endpoint == saved }
}

internal fun persistedRouterConfig(
    saved: String?,
    routers: List<RouterConfig>,
    allowCleartextLoopback: Boolean,
): RouterConfig? {
    val requested = savedRouterCandidates(saved, routers).firstOrNull { routerConfigAllowed(it, allowCleartextLoopback) }
    return requested ?: routers.firstOrNull { routerConfigAllowed(it, allowCleartextLoopback) }
}

internal fun upsertRouterConfig(
    routers: List<RouterConfig>,
    candidate: String,
    alias: String,
    previousEndpoint: String? = null,
    scheme: String? = null,
    allowCleartextLoopback: Boolean = BuildConfig.DEBUG,
): List<RouterConfig> {
    val existingScheme = routers.firstOrNull { it.identity == previousEndpoint || it.endpoint == previousEndpoint }?.scheme
    val candidateHasScheme = candidate.trim().contains("://")
    val candidateForValidation = if (!candidateHasScheme && existingScheme == "https") "https://${candidate.trim()}" else candidate
    val endpoint = normalizeRouterEndpointCandidate(candidateForValidation, allowCleartextLoopback) ?: return routers
    val savedScheme = scheme ?: if (candidateHasScheme) {
        routerSchemeForCandidate(candidateForValidation, allowCleartextLoopback) ?: return routers
    } else {
        existingScheme ?: "http"
    }
    val trimmedAlias = alias.trim()
    val savedConfig = RouterConfig(trimmedAlias, endpoint, savedScheme)
    return (routers.filterNot { it.identity == savedConfig.identity || it.identity == previousEndpoint || it.endpoint == previousEndpoint } + savedConfig)
        .distinctBy { it.identity }
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

internal fun discoveryResponseWithinBound(agents: JSONArray): Boolean = agents.length() <= MAX_DISCOVERED_PEERS

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

internal fun peerStatusFromJson(peer: String, source: JSONObject, current: PeerStatus? = null): PeerStatus {
    val instanceId = source.optString("instance_id").takeIf { it.isNotBlank() }
    val sameInstance = instanceId == null || current?.instanceId == null || instanceId == current.instanceId
    val incomingVersion = if (source.has("subagent_version")) source.optLong("subagent_version", -1L).takeIf { it >= 0 } else null
    val incomingSummaries = normalizedSubagentSummaries(source.optJSONArray("subagents"))
    val incomingCount = source.optInt("subagent_count", 0).coerceIn(0, 999)
    val incomingStale = sameInstance && current?.subagentVersion != null && incomingVersion != null
        && (incomingVersion < current.subagentVersion || (incomingVersion == current.subagentVersion
            && (incomingCount != current.subagentCount || incomingSummaries != current.subagents)))
    val durableVersion = if (incomingStale) current?.subagentVersion else incomingVersion ?: current?.subagentVersion?.takeIf { sameInstance }
    val preservedSummaries = if (!sameInstance) {
        incomingSummaries
    } else if (incomingStale) {
        current?.subagents.orEmpty()
    } else if (incomingVersion != null) {
        if (incomingCount == 0) emptyList() else incomingSummaries
    } else if (current?.subagentVersion != null) {
        current.subagents
    } else if (incomingSummaries.isNotEmpty()) {
        incomingSummaries
    } else if (incomingCount > 0 && current?.subagents?.size == incomingCount) {
        current.subagents
    } else {
        emptyList()
    }
    val normalizedSubagentCount = if (incomingStale || durableVersion != null && incomingVersion == null) current?.subagentCount ?: incomingCount else maxOf(incomingCount, preservedSummaries.size)
    val parsedModels = if (source.has("available_models")) parseModelSummaries(source.optJSONArray("available_models")) else null
    val currentModel = if (source.has("current_model")) parseModelSummary(source.optJSONObject("current_model"))
    else current?.modelCatalog?.current?.takeIf { sameInstance }
    val catalog = if (parsedModels != null) {
        ModelCatalog(
            if (parsedModels.isEmpty()) CatalogStatus.EMPTY else CatalogStatus.READY,
            parsedModels,
            source.optInt("model_catalog_total_count", parsedModels.size).coerceAtLeast(parsedModels.size),
            source.optBoolean("model_catalog_truncated", false),
            currentModel,
        )
    } else if (sameInstance) {
        current?.modelCatalog ?: ModelCatalog(CatalogStatus.LOADING, current = currentModel)
    } else {
        ModelCatalog(CatalogStatus.LOADING, current = currentModel)
    }
    val goalState = if (source.has("goal_state")) parseGoalStateValue(source.optJSONObject("goal_state"))
    else current?.goalState?.takeIf { sameInstance }
    val peerSource = source.optString("source").takeIf { it.isNotBlank() } ?: current?.source ?: "tmux"
    val runtime = source.optString("runtime").takeIf { it.isNotBlank() } ?: current?.runtime
    return PeerStatus(
        id = peer,
        activityState = source.optString("activity_state", "idle"),
        subagentVersion = durableVersion,
        subagentCount = normalizedSubagentCount,
        subagents = preservedSummaries,
        instanceId = instanceId ?: current?.instanceId,
        modelCatalog = catalog,
        goalState = goalState,
        source = peerSource,
        runtime = runtime,
    )
}

internal fun peerStatusFromManaged(
    agentId: String,
    managedAgent: JSONObject,
    children: List<SubagentSummary> = emptyList(),
    current: PeerStatus? = null,
): PeerStatus {
    val activityState = when (managedAgent.optString("activity_state", "idle")) {
        "active", "idle", "compacting", "starting", "stopped", "failed" -> managedAgent.optString("activity_state")
        else -> "idle"
    }
    val goalState = if (managedAgent.has("goal_state")) parseGoalStateValue(managedAgent.optJSONObject("goal_state"))
    else current?.goalState
    val modelObj = managedAgent.optJSONObject("model")
    val currentModel = modelObj?.let {
        ModelSummary(it.optString("provider"), it.optString("id"), it.optString("name").ifBlank { it.optString("id") })
    } ?: current?.modelCatalog?.current
    val catalog = ModelCatalog(
        status = if (currentModel != null) CatalogStatus.READY else CatalogStatus.EMPTY,
        models = currentModel?.let(::listOf) ?: emptyList(),
        totalCount = if (currentModel != null) 1 else 0,
        truncated = false,
        current = currentModel,
    )
    val runtime = managedAgent.optString("runtime", "embedded").ifBlank { "embedded" }
    return PeerStatus(
        id = agentId,
        activityState = activityState,
        subagentCount = children.size,
        subagents = children,
        instanceId = current?.instanceId,
        modelCatalog = catalog,
        goalState = goalState,
        source = "router-managed",
        runtime = runtime,
    )
}

internal fun mergeManagedPeers(
    tmuxPeers: List<PeerStatus>,
    managedAgents: List<JSONObject>,
    localAgentId: String,
    childrenByAgent: Map<String, List<SubagentSummary>> = emptyMap(),
): List<PeerStatus> {
    val existingById = tmuxPeers.associateBy(PeerStatus::id)
    val managedRows = buildList {
        for (managed in managedAgents) {
            val agentId = managed.optString("agent_id").takeIf { it.isNotBlank() } ?: continue
            if (agentId == localAgentId || !AGENT_ID.matches(agentId)) continue
            val parentAgentId = managed.optString("parent_agent_id").takeIf { it.isNotBlank() }
            if (parentAgentId != null) continue
            val current = existingById[agentId]
            val children = childrenByAgent[agentId] ?: current?.subagents ?: emptyList()
            add(peerStatusFromManaged(agentId, managed, children = children, current = current))
        }
    }
    val managedById = managedRows.associateBy(PeerStatus::id)
    val result = mutableListOf<PeerStatus>()
    for (tmux in tmuxPeers) {
        val managed = managedById[tmux.id]
        if (managed != null) {
            result.add(
                tmux.copy(
                    source = "router-managed",
                    runtime = managed.runtime,
                    activityState = if (managed.activityState != "idle") managed.activityState else tmux.activityState,
                    goalState = managed.goalState ?: tmux.goalState,
                    subagentCount = if (managed.subagentCount > 0) managed.subagentCount else tmux.subagentCount,
                    subagents = if (managed.subagents.isNotEmpty()) managed.subagents else tmux.subagents,
                )
            )
        } else {
            result.add(tmux)
        }
    }
    for (managed in managedRows) {
        if (managed.id !in existingById) {
            result.add(managed)
        }
    }
    return result.sortedBy(PeerStatus::id)
}

internal fun formatMessageTimestamp(createdAt: String): String = runCatching {
    MessageTimestampFormatter.format(Instant.parse(createdAt))
}.getOrElse { "" }

internal fun formatEventTimestamp(timestamp: String): String {
    if (timestamp.isBlank()) return ""
    return runCatching {
        val instant = runCatching { Instant.parse(timestamp) }
            .recoverCatching { OffsetDateTime.parse(timestamp).toInstant() }
            .getOrNull() ?: return@runCatching ""
        EventTimestampFormatter.format(instant)
    }.getOrElse { "" }
}

private fun humanName(id: String): String = id.split(Regex("[._-]+"))
    .filter { it.isNotBlank() }
    .joinToString(" ") { part -> part.replaceFirstChar { it.uppercase() } }
    .ifBlank { id }

internal fun connectedStatusDetail(agentId: String, routerDisplayName: String): String =
    "${humanName(agentId)} · $routerDisplayName"
