package life.meyers.mecha

import java.io.ByteArrayOutputStream
import java.io.IOException
import java.nio.charset.CodingErrorAction
import java.time.Instant
import java.time.OffsetDateTime
import okhttp3.ResponseBody
import org.json.JSONArray
import org.json.JSONObject

internal const val MAX_HISTORY_MESSAGES = 200
internal const val MAX_HISTORY_RESPONSE_BYTES = 16 * 1024 * 1024
internal const val HISTORY_REQUEST_TIMEOUT_MS = 5_000L
internal const val HISTORY_RETENTION_MS = 30L * 24 * 60 * 60 * 1000
internal const val HISTORY_RECOVERY_DELAY_MS = 1_000L
internal const val HISTORY_RECOVERY_MAX_ATTEMPTS = 5
internal const val HISTORY_RECOVERY_MAX_DELAY_MS = 30_000L

private val HISTORY_MESSAGE_ID = Regex("^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
private val HISTORY_AGENT_ID = Regex("^(?!.*@)[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
private val HISTORY_FRAME_ID = Regex("^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
private val HISTORY_INCARNATION_ID = Regex("^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$")
private val HISTORY_ATTACHMENT_ID = Regex("^[A-Za-z0-9][A-Za-z0-9._-]{0,191}$")
private val HISTORY_ATTACHMENT_URL = Regex("^/v1/attachments/[A-Za-z0-9._~%-]{1,511}$")
private val HISTORY_UTC_MILLIS = Regex("^\\d{4}-\\d{2}-\\d{2}T\\d{2}:\\d{2}:\\d{2}\\.\\d{3}Z$")
private val HISTORY_ATTACHMENT_MIME_TYPES = setOf("image/png", "image/jpeg", "image/webp", "image/gif")
private val HISTORY_SOURCE_KINDS = setOf("agent", "web_ui", "android", "discord", "reminder", "system", "unknown")
private val HISTORY_SENTINEL_MODEL = Regex("^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$")

internal enum class HistoryStatus { IDLE, LOADING, ERROR }

internal enum class HistoryFailure { HTTP, INVALID_RESPONSE, EXCEPTION, TIMEOUT, PERSISTENCE, PERSISTENCE_EXHAUSTED }

internal enum class LiveMessageFrameKind { CONVERSATION_MESSAGE, DELIVERY_MESSAGE }

internal data class ValidatedDeliveryItem(
    val messageId: String,
    val messageIncarnationId: String,
    val attemptNumber: Int,
    val deliveryExpiresAt: String,
)

internal data class ValidatedDeliveryMessage(
    val payload: JSONObject,
    val item: ValidatedDeliveryItem,
)

internal data class ValidatedDeliveryAcknowledgement(
    val requestId: String,
    val agentId: String,
    val instanceId: String,
    val leaseId: String,
    val messageId: String,
    val messageIncarnationId: String,
    val attemptNumber: Int,
)

internal data class ValidatedLiveMessage(
    val payload: JSONObject,
    val incarnationId: String,
    val sequence: Long,
    val recordedAt: String?,
    val attemptNumber: Int? = null,
    val deliveryItems: List<ValidatedDeliveryItem> = emptyList(),
    val deliveryMessages: List<ValidatedDeliveryMessage> = emptyList(),
)

internal data class HistoryResponse(
    val owner: String,
    val peer: String,
    val messages: JSONArray,
    val highWaterSequence: Long,
)

/**
 * Parses the exact `/v1/conversations` response shape before any state is changed.
 * The optional expected values exist for fixture tests; the network call always supplies both.
 */
internal fun parseHistoryResponse(
    body: String,
    expectedOwner: String? = null,
    expectedPeer: String? = null,
): HistoryResponse? = runCatching {
    val payload = JSONObject(body)
    if (!hasExactHistoryKeys(payload, setOf("owner", "peer", "high_water_sequence", "messages"))) return@runCatching null
    val owner = strictHistoryString(payload, "owner", 1, 128) ?: return@runCatching null
    val peer = strictHistoryString(payload, "peer", 1, 128) ?: return@runCatching null
    if (expectedOwner != null && owner != expectedOwner) return@runCatching null
    if (expectedPeer != null && peer != expectedPeer) return@runCatching null
    if (!HISTORY_AGENT_ID.matches(owner)) return@runCatching null
    // Go's JSON encoder emits a nil slice as null for an empty conversation.
    // Treat that wire representation as the router's empty history, while still
    // rejecting every other non-array value.
    val messages = if (payload.isNull("messages")) JSONArray() else {
        payload.get("messages") as? JSONArray ?: return@runCatching null
    }
    if (messages.length() > MAX_HISTORY_MESSAGES) return@runCatching null
    val highWaterSequence = strictHistoryLong(payload, "high_water_sequence", 0L) ?: return@runCatching null
    for (index in 0 until messages.length()) {
        val message = validateLiveMessagePayload(messages.opt(index), LiveMessageFrameKind.CONVERSATION_MESSAGE)
            ?: return@runCatching null
        if (!isMessageInConversationScope(
                message.payload.getString("from"),
                message.payload.getString("to"),
                owner,
                peer,
            )
        ) return@runCatching null
    }
    HistoryResponse(owner, peer, messages, highWaterSequence)
}.getOrNull()

/** Read at most the protocol response bound and reject malformed UTF-8. */
internal fun readHistoryResponseBody(body: ResponseBody?, maxBytes: Int = MAX_HISTORY_RESPONSE_BYTES): String? {
    if (body == null || maxBytes <= 0 || body.contentLength() > maxBytes) return null
    return runCatching {
        val bytes = ByteArrayOutputStream(minOf(maxBytes, 64 * 1024))
        body.byteStream().use { input ->
            val buffer = ByteArray(16 * 1024)
            var total = 0
            while (true) {
                val count = input.read(buffer)
                if (count < 0) break
                if (count == 0) continue
                total += count
                if (total > maxBytes) return@runCatching null
                bytes.write(buffer, 0, count)
            }
        }
        Charsets.UTF_8.newDecoder()
            .onMalformedInput(CodingErrorAction.REPORT)
            .onUnmappableCharacter(CodingErrorAction.REPORT)
            .decode(java.nio.ByteBuffer.wrap(bytes.toByteArray()))
            .toString()
    }.getOrNull()
}

private fun hasOnlyHistoryKeys(value: JSONObject, keys: Set<String>): Boolean =
    value.keys().asSequence().all { it in keys }

private fun hasExactHistoryKeys(value: JSONObject, keys: Set<String>): Boolean =
    hasOnlyHistoryKeys(value, keys) && keys.all(value::has)

private fun strictHistoryString(value: JSONObject, key: String, minLength: Int, maxLength: Int): String? {
    val text = value.opt(key) as? String ?: return null
    if (text.length !in minLength..maxLength || !hasPairedSurrogates(text)) return null
    return text
}

private fun strictHistoryLong(value: JSONObject, key: String, minimum: Long): Long? {
    val number = when (val raw = value.opt(key)) {
        is Int -> raw.toLong()
        is Long -> raw
        is Short -> raw.toLong()
        is Byte -> raw.toLong()
        else -> return null
    }
    return number.takeIf { it >= minimum }
}

private fun strictHistoryOptionalString(value: JSONObject, key: String, maxLength: Int): String? {
    if (!value.has(key) || value.isNull(key)) return null
    return strictHistoryString(value, key, 0, maxLength)
}

private fun hasPairedSurrogates(value: String): Boolean {
    var index = 0
    while (index < value.length) {
        val character = value[index]
        if (character.isHighSurrogate()) {
            if (index + 1 >= value.length || !value[index + 1].isLowSurrogate()) return false
            index += 2
        } else if (character.isLowSurrogate()) {
            return false
        } else {
            index++
        }
    }
    return true
}

/**
 * Validates both live server message frames and their message payloads. The protocol has
 * different outer fields for conversation broadcasts and mailbox delivery, but their shared
 * message fields are checked in this one function before either path mutates local state.
 */
internal fun validateLiveMessageFrame(
    frame: JSONObject,
    kind: LiveMessageFrameKind,
    requireIncarnation: Boolean = true,
): ValidatedLiveMessage? = runCatching {
    val baseRequired = setOf("version", "frame_id", "type", "message")
    val allowed = when (kind) {
        LiveMessageFrameKind.CONVERSATION_MESSAGE -> baseRequired
        LiveMessageFrameKind.DELIVERY_MESSAGE -> baseRequired + setOf(
            "message_incarnation_id", "attempt_number", "delivery_expires_at", "delivery_items", "delivery_messages",
        )
    }
    if (!hasOnlyHistoryKeys(frame, allowed) || !baseRequired.all(frame::has) || frame.optString("version") != "v1") return@runCatching null
    val expectedType = when (kind) {
        LiveMessageFrameKind.CONVERSATION_MESSAGE -> "conversation_message"
        LiveMessageFrameKind.DELIVERY_MESSAGE -> "message"
    }
    if (frame.optString("type") != expectedType) return@runCatching null
    val frameId = strictHistoryString(frame, "frame_id", 1, 128) ?: return@runCatching null
    if (!HISTORY_FRAME_ID.matches(frameId)) return@runCatching null
    val payload = frame.get("message") as? JSONObject ?: return@runCatching null
    val parsed = validateLiveMessagePayload(payload, kind) ?: return@runCatching null
    if (kind == LiveMessageFrameKind.CONVERSATION_MESSAGE) return@runCatching parsed

    val hasIncarnation = frame.has("message_incarnation_id")
    if (hasIncarnation && frame.isNull("message_incarnation_id")) return@runCatching null
    val incarnation = if (hasIncarnation) {
        strictHistoryString(frame, "message_incarnation_id", 36, 36) ?: return@runCatching null
    } else ""
    if (requireIncarnation && incarnation.isBlank()) return@runCatching null
    if (incarnation.isNotBlank() && !HISTORY_INCARNATION_ID.matches(incarnation)) return@runCatching null
    val attempt = strictHistoryInt(frame, "attempt_number", 1) ?: return@runCatching null
    val expiresAt = strictHistoryString(frame, "delivery_expires_at", 24, 24) ?: return@runCatching null
    if (!HISTORY_UTC_MILLIS.matches(expiresAt) || parseHistoryTimestamp(expiresAt) == null) return@runCatching null
    val hasDeliveryItems = frame.has("delivery_items")
    val deliveryItems = if (hasDeliveryItems) {
        val items = frame.get("delivery_items") as? JSONArray ?: return@runCatching null
        if (items.length() == 0) return@runCatching null
        List(items.length()) { index ->
            parseDeliveryItem(items.opt(index)) ?: return@runCatching null
        }.also { parsedItems ->
            val first = parsedItems.first()
            if (first.messageId != parsed.payload.getString("id")
                || incarnation.isNotBlank() && first.messageIncarnationId != incarnation
                || first.attemptNumber != attempt
                || first.deliveryExpiresAt != expiresAt
                || parsedItems.map(ValidatedDeliveryItem::messageId).toSet().size != parsedItems.size
            ) return@runCatching null
        }
    } else {
        listOf(ValidatedDeliveryItem(parsed.payload.getString("id"), incarnation, attempt, expiresAt))
    }
    if (frame.has("delivery_messages") && !hasDeliveryItems) return@runCatching null
    val deliveryMessages = if (frame.has("delivery_messages")) {
        val messages = frame.get("delivery_messages") as? JSONArray ?: return@runCatching null
        if (messages.length() != deliveryItems.size) return@runCatching null
        List(messages.length()) { index ->
            val item = deliveryItems[index]
            val message = validateLiveMessagePayload(messages.opt(index), LiveMessageFrameKind.DELIVERY_MESSAGE)
                ?: return@runCatching null
            if (message.payload.getString("id") != item.messageId) return@runCatching null
            ValidatedDeliveryMessage(message.payload, item)
        }.also { parsedMessages ->
            val first = parsedMessages.first()
            if (first.payload.getString("id") != parsed.payload.getString("id")
                || first.payload.getString("from") != parsed.payload.getString("from")
                || first.payload.getString("to") != parsed.payload.getString("to")
            ) return@runCatching null
        }
    } else {
        if (deliveryItems.size > 1) return@runCatching null
        listOf(ValidatedDeliveryMessage(parsed.payload, deliveryItems.first()))
    }
    parsed.copy(
        incarnationId = incarnation,
        attemptNumber = attempt,
        deliveryItems = deliveryItems,
        deliveryMessages = deliveryMessages,
    )
}.getOrNull()

/** Validates the common message envelope plus the fields specific to each protocol payload. */
internal fun validateLiveMessagePayload(
    raw: Any?,
    kind: LiveMessageFrameKind,
): ValidatedLiveMessage? = runCatching {
    val message = raw as? JSONObject ?: return@runCatching null
    val common = setOf("id", "from", "to", "body", "created_at")
    val allowed = common + setOf("subject", "source") + when (kind) {
        LiveMessageFrameKind.CONVERSATION_MESSAGE -> setOf("sequence", "message_incarnation_id", "recorded_at", "attachments")
        LiveMessageFrameKind.DELIVERY_MESSAGE -> setOf("sentinel")
    }
    val required = common + when (kind) {
        LiveMessageFrameKind.CONVERSATION_MESSAGE -> setOf("sequence", "message_incarnation_id", "recorded_at", "attachments")
        LiveMessageFrameKind.DELIVERY_MESSAGE -> emptySet()
    }
    if (!hasOnlyHistoryKeys(message, allowed) || !required.all(message::has)) return@runCatching null
    val id = strictHistoryString(message, "id", 1, 128) ?: return@runCatching null
    if (!HISTORY_MESSAGE_ID.matches(id)) return@runCatching null
    val sender = strictHistoryString(message, "from", 1, 128) ?: return@runCatching null
    val recipient = strictHistoryString(message, "to", 1, 128) ?: return@runCatching null
    if (!HISTORY_AGENT_ID.matches(sender) || !HISTORY_AGENT_ID.matches(recipient)) return@runCatching null
    val subject = strictHistoryOptionalString(message, "subject", 256)
    if (message.has("subject") && subject == null) return@runCatching null
    val body = strictHistoryString(message, "body", 0, 65_536) ?: return@runCatching null
    val createdAt = strictHistoryString(message, "created_at", 24, 24) ?: return@runCatching null
    if (!HISTORY_UTC_MILLIS.matches(createdAt) || parseHistoryTimestamp(createdAt) == null) return@runCatching null
    if (message.has("source") && !validateMessageSource(message.get("source"))) return@runCatching null
    if (message.has("sentinel") && !validateSentinel(message.get("sentinel"))) return@runCatching null

    val sequence = if (kind == LiveMessageFrameKind.CONVERSATION_MESSAGE) {
        strictHistoryLong(message, "sequence", 1L) ?: return@runCatching null
    } else 0L
    val incarnation = if (kind == LiveMessageFrameKind.CONVERSATION_MESSAGE) {
        strictHistoryString(message, "message_incarnation_id", 36, 36) ?: return@runCatching null
    } else ""
    if (kind == LiveMessageFrameKind.CONVERSATION_MESSAGE && !HISTORY_INCARNATION_ID.matches(incarnation)) return@runCatching null
    val recordedAt = if (kind == LiveMessageFrameKind.CONVERSATION_MESSAGE) {
        strictHistoryString(message, "recorded_at", 24, 24) ?: return@runCatching null
    } else null
    if (recordedAt != null && (!HISTORY_UTC_MILLIS.matches(recordedAt) || parseHistoryTimestamp(recordedAt) == null)) return@runCatching null
    if (kind == LiveMessageFrameKind.CONVERSATION_MESSAGE) {
        val attachments = message.get("attachments") as? JSONArray ?: return@runCatching null
        if (attachments.length() > MAX_IMAGE_ATTACHMENTS) return@runCatching null
        for (index in 0 until attachments.length()) {
            if (!validateHistoryAttachment(attachments.opt(index))) return@runCatching null
        }
    }
    // Keep all fields consumed above in the validator so malformed optional values cannot be
    // silently converted into empty UI values by the renderer.
    sender.length
    subject?.length
    ValidatedLiveMessage(message, incarnation, sequence, recordedAt)
}.getOrNull()

private fun strictHistoryInt(value: JSONObject, key: String, minimum: Int): Int? {
    val number = strictHistoryLong(value, key, minimum.toLong()) ?: return null
    return number.takeIf { it <= Int.MAX_VALUE }?.toInt()
}

private fun validateMessageSource(raw: Any?): Boolean {
    val source = raw as? JSONObject ?: return false
    if (!hasExactHistoryKeys(source, setOf("kind"))) return false
    val kind = strictHistoryString(source, "kind", 1, 32) ?: return false
    return kind in HISTORY_SOURCE_KINDS
}

private fun validateSentinel(raw: Any?): Boolean {
    val sentinel = raw as? JSONObject ?: return false
    if (!hasExactHistoryKeys(sentinel, setOf("model"))) return false
    val model = strictHistoryString(sentinel, "model", 1, 128) ?: return false
    return HISTORY_SENTINEL_MODEL.matches(model)
}

private fun parseDeliveryItem(raw: Any?): ValidatedDeliveryItem? {
    val item = raw as? JSONObject ?: return null
    val required = setOf("message_id", "message_incarnation_id", "attempt_number", "delivery_expires_at")
    if (!hasExactHistoryKeys(item, required)) return null
    val id = strictHistoryString(item, "message_id", 1, 128) ?: return null
    val incarnation = strictHistoryString(item, "message_incarnation_id", 36, 36) ?: return null
    val attempt = strictHistoryInt(item, "attempt_number", 1) ?: return null
    val expiresAt = strictHistoryString(item, "delivery_expires_at", 24, 24) ?: return null
    if (!HISTORY_MESSAGE_ID.matches(id)
        || !HISTORY_INCARNATION_ID.matches(incarnation)
        || !HISTORY_UTC_MILLIS.matches(expiresAt)
        || parseHistoryTimestamp(expiresAt) == null
    ) return null
    return ValidatedDeliveryItem(id, incarnation, attempt, expiresAt)
}

private fun validateDeliveryItem(raw: Any?): Boolean = parseDeliveryItem(raw) != null

private fun validateHistoryAttachment(raw: Any?): Boolean {
    val attachment = raw as? JSONObject ?: return false
    val required = setOf("id", "url", "mime_type", "size_bytes")
    val allowed = required + "name"
    if (!attachment.keys().asSequence().all { it in allowed } || !required.all(attachment::has)) return false
    val id = strictHistoryString(attachment, "id", 1, 192) ?: return false
    val url = strictHistoryString(attachment, "url", 1, 512) ?: return false
    val mimeType = strictHistoryString(attachment, "mime_type", 1, 32) ?: return false
    val sizeBytes = strictHistoryLong(attachment, "size_bytes", 1L) ?: return false
    if (!HISTORY_ATTACHMENT_ID.matches(id) || !HISTORY_ATTACHMENT_URL.matches(url)
        || mimeType !in HISTORY_ATTACHMENT_MIME_TYPES || sizeBytes > MAX_IMAGE_BYTES
    ) return false
    val name = strictHistoryOptionalString(attachment, "name", 120)
    return (!attachment.has("name") || name != null) && (name == null || name.isNotEmpty())
}

internal fun isMessageInConversationScope(sender: String, recipient: String, owner: String, peer: String): Boolean =
    (sender == owner && recipient == peer) || (sender == peer && recipient == owner)

internal fun isInboundDeliveryForAgent(sender: String, recipient: String, agentId: String): Boolean =
    recipient == agentId && sender != agentId

internal fun conversationPeerForMessage(message: ChatMessage, owner: String): String? = when {
    message.sender == owner && message.recipient != owner -> message.recipient
    message.recipient == owner && message.sender != owner -> message.sender
    else -> null
}

internal fun isCanonicalConversationMessage(message: ChatMessage): Boolean =
    message.sequence > 0L && !message.recordedAt.isNullOrBlank()

internal fun parseDeliveryAcknowledgedFrame(value: JSONObject): ValidatedDeliveryAcknowledgement? = runCatching {
    val required = setOf(
        "version", "frame_id", "type", "request_id", "agent_id", "instance_id", "lease_id",
        "message_id", "message_incarnation_id", "attempt_number",
    )
    if (!hasExactHistoryKeys(value, required) || value.optString("version") != "v1"
        || value.optString("type") != "delivery_acknowledged"
    ) return@runCatching null
    val requestId = strictHistoryString(value, "request_id", 1, 128) ?: return@runCatching null
    val frameId = strictHistoryString(value, "frame_id", 1, 128) ?: return@runCatching null
    if (!HISTORY_FRAME_ID.matches(requestId) || !HISTORY_FRAME_ID.matches(frameId)) return@runCatching null
    val agentId = strictHistoryString(value, "agent_id", 1, 128) ?: return@runCatching null
    val instanceId = strictHistoryString(value, "instance_id", 1, 128) ?: return@runCatching null
    val leaseId = strictHistoryString(value, "lease_id", 1, 128) ?: return@runCatching null
    val messageId = strictHistoryString(value, "message_id", 1, 128) ?: return@runCatching null
    val incarnation = strictHistoryString(value, "message_incarnation_id", 36, 36) ?: return@runCatching null
    val attempt = strictHistoryInt(value, "attempt_number", 1) ?: return@runCatching null
    if (!HISTORY_AGENT_ID.matches(agentId) || !HISTORY_FRAME_ID.matches(instanceId)
        || !HISTORY_FRAME_ID.matches(leaseId) || !HISTORY_MESSAGE_ID.matches(messageId)
        || !HISTORY_INCARNATION_ID.matches(incarnation)
    ) return@runCatching null
    ValidatedDeliveryAcknowledgement(requestId, agentId, instanceId, leaseId, messageId, incarnation, attempt)
}.getOrNull()

internal enum class HistoryAppendStatus { SAVED, IGNORED, PERSISTENCE_FAILED }

internal data class HistoryAppendResult(
    val status: HistoryAppendStatus,
    val messages: List<ChatMessage>,
    val alreadyPersisted: Boolean = false,
) {
    val needsHydrationRecovery: Boolean get() = status == HistoryAppendStatus.PERSISTENCE_FAILED
    val shouldNotify: Boolean get() = status == HistoryAppendStatus.SAVED && !alreadyPersisted
}

internal data class HistoryUiState(
    val status: HistoryStatus = HistoryStatus.IDLE,
    val message: String? = null,
) {
    val retryable: Boolean get() = status == HistoryStatus.ERROR
}

internal fun historyErrorState(failure: HistoryFailure, statusCode: Int? = null): HistoryUiState = HistoryUiState(
    status = HistoryStatus.ERROR,
    message = when (failure) {
        HistoryFailure.HTTP -> "History could not be loaded${statusCode?.let { " ($it)" }.orEmpty()}."
        HistoryFailure.INVALID_RESPONSE -> "History could not be loaded. The router returned an invalid response."
        HistoryFailure.EXCEPTION -> "History could not be loaded. Check the connection and try again."
        HistoryFailure.TIMEOUT -> "History took too long to load. Try again."
        HistoryFailure.PERSISTENCE -> "History was loaded but could not be saved on this device. Try again."
        HistoryFailure.PERSISTENCE_EXHAUSTED -> "History could not be saved after several attempts. Tap Retry to try again."
    },
)

/** Automatic recovery is finite; a manual Retry starts a fresh budget. */
internal fun historyRecoveryDelayMs(attempt: Int): Long {
    require(attempt >= 0)
    val multiplier = 1L shl attempt.coerceAtMost(30)
    return (HISTORY_RECOVERY_DELAY_MS * multiplier).coerceAtMost(HISTORY_RECOVERY_MAX_DELAY_MS)
}

internal fun hasHistoryRecoveryBudget(attempt: Int): Boolean = attempt in 0 until HISTORY_RECOVERY_MAX_ATTEMPTS

internal data class HistoryPersistenceProof(
    val committed: Boolean,
    val retainedMessageKeys: Set<String>,
)

/** Applies a live additive message and reports a failed durable write explicitly. */
internal fun appendLiveConversationMessage(
    existing: List<ChatMessage>,
    message: ChatMessage,
    nowMs: Long,
    persist: (List<ChatMessage>) -> HistoryPersistenceProof,
    alreadyPersisted: Boolean = false,
): HistoryAppendResult {
    if (message.direction == Direction.INCOMING && !isRetainedConversationMessage(message, nowMs)) {
        return HistoryAppendResult(HistoryAppendStatus.IGNORED, existing, alreadyPersisted)
    }
    val updated = mergeConversationMessages(existing, listOf(message), nowMs)
    if (message.direction == Direction.INCOMING && updated.none { it.key == message.key }) {
        return HistoryAppendResult(HistoryAppendStatus.IGNORED, existing, alreadyPersisted)
    }
    val proof = persist(updated)
    val committed = proof.committed && (message.direction != Direction.INCOMING || message.key in proof.retainedMessageKeys)
    return HistoryAppendResult(
        if (committed) HistoryAppendStatus.SAVED else HistoryAppendStatus.PERSISTENCE_FAILED,
        updated,
        alreadyPersisted,
    )
}

internal data class HistoryHydrationFence(
    val router: String,
    val ownerAgentId: String,
    val ownerInstanceId: String,
    val peer: String,
    val targetInstanceId: String?,
    val connectionGeneration: Int,
    val requestSerial: Long,
)

internal fun isCurrentHistoryHydration(
    request: HistoryHydrationFence,
    currentRouter: String,
    currentOwnerAgentId: String,
    currentOwnerInstanceId: String,
    currentPeer: String,
    currentTargetInstanceId: String?,
    currentConnectionGeneration: Int,
    currentRequestSerial: Long?,
): Boolean = request.router == currentRouter
    && request.ownerAgentId == currentOwnerAgentId
    && request.ownerInstanceId == currentOwnerInstanceId
    && request.peer == currentPeer
    // Null means that the peer was unknown when the request started. A newly
    // observed instance is still a different identity and invalidates the snapshot.
    && request.targetInstanceId == currentTargetInstanceId
    && request.connectionGeneration == currentConnectionGeneration
    && request.requestSerial == currentRequestSerial

/** Checks a caller before it creates a loading state or publishes a hydration fence. */
internal fun isCurrentHistoryHydrationStart(
    requestedPeer: String,
    requestedGeneration: Int,
    currentRouter: String,
    currentOwnerAgentId: String,
    currentOwnerInstanceId: String,
    currentSelectedPeer: String,
    currentConnectionGeneration: Int,
): Boolean = requestedPeer.isNotBlank()
    && currentRouter.isNotBlank()
    && currentOwnerAgentId.isNotBlank()
    && currentOwnerInstanceId.isNotBlank()
    && requestedPeer == currentSelectedPeer
    && requestedGeneration == currentConnectionGeneration

internal data class PeerStateEventToken(
    val generation: Int,
    val version: Long,
    val peer: String? = null,
    val discoveryVersion: Long = 0L,
    val peerVersions: Map<String, Long> = emptyMap(),
)

private data class PeerObservation(
    val instanceId: String,
    val observedAtMs: Long?,
)

/**
 * Fences each peer independently. A discovery response can update peers untouched by a
 * newer event, while an event from an older instance is rejected by its observation time.
 */
internal class PeerStateEventFence {
    private var generation: Int? = null
    private var nextVersion = 0L
    private var latestDiscoveryVersion = 0L
    private val peerVersions = mutableMapOf<String, Long>()
    private val observations = mutableMapOf<String, PeerObservation>()

    fun reset(nextGeneration: Int) {
        generation = nextGeneration
        nextVersion = 0L
        latestDiscoveryVersion = 0L
        peerVersions.clear()
        observations.clear()
    }

    fun beginDiscovery(generation: Int): PeerStateEventToken? {
        if (this.generation != generation) return null
        val version = ++nextVersion
        latestDiscoveryVersion = version
        return PeerStateEventToken(generation, version, discoveryVersion = version, peerVersions = peerVersions.toMap())
    }

    /** Compatibility overload for tests and callers that do not have peer metadata. */
    fun begin(generation: Int): PeerStateEventToken? {
        if (this.generation != generation) return null
        val version = ++nextVersion
        return PeerStateEventToken(generation, version)
    }

    fun begin(generation: Int, peer: String): PeerStateEventToken? {
        if (this.generation != generation || peer.isBlank()) return null
        val version = (peerVersions[peer] ?: 0L) + 1L
        peerVersions[peer] = version
        return PeerStateEventToken(generation, version, peer = peer, discoveryVersion = latestDiscoveryVersion)
    }

    /** Accept an event only when it is current for this peer and instance. */
    fun observeEvent(
        generation: Int,
        peer: String,
        instanceId: String?,
        observedAtMs: Long?,
    ): PeerStateEventToken? {
        if (instanceId.isNullOrBlank()) return begin(generation, peer)
        val current = observations[peer]
        if (current != null && current.instanceId != instanceId) {
            // A frame without a trustworthy timestamp cannot replace a known instance.
            if (observedAtMs == null || current.observedAtMs != null && observedAtMs < current.observedAtMs) return null
        } else if (current?.observedAtMs != null && observedAtMs != null && observedAtMs < current.observedAtMs) {
            return null
        }
        val token = begin(generation, peer) ?: return null
        observations[peer] = PeerObservation(
            instanceId,
            if (current?.instanceId == instanceId) maxObservedAt(current.observedAtMs, observedAtMs) else observedAtMs,
        )
        return token
    }

    /** Apply one discovery entry only when no newer event or discovery superseded it. */
    fun acceptDiscovery(token: PeerStateEventToken, peer: String, instanceId: String?, observedAtMs: Long?): Boolean {
        if (!isCurrentForPeer(token, peer) || instanceId.isNullOrBlank()) return false
        val current = observations[peer]
        if (current != null && current.instanceId != instanceId
            && (observedAtMs == null || current.observedAtMs != null && observedAtMs < current.observedAtMs)
        ) return false
        peerVersions[peer] = (peerVersions[peer] ?: 0L) + 1L
        observations[peer] = PeerObservation(
            instanceId,
            if (current?.instanceId == instanceId) maxObservedAt(current.observedAtMs, observedAtMs) else observedAtMs,
        )
        return true
    }

    fun isCurrentForPeer(token: PeerStateEventToken, peer: String): Boolean =
        token.generation == generation
            && token.discoveryVersion == latestDiscoveryVersion
            && (token.peerVersions[peer] ?: 0L) == (peerVersions[peer] ?: 0L)

    fun isNewest(token: PeerStateEventToken): Boolean = when {
        token.generation != generation -> false
        token.peer != null -> token.version == peerVersions[token.peer]
        else -> token.version == nextVersion
    }

    private fun maxObservedAt(left: Long?, right: Long?): Long? = when {
        left == null -> right
        right == null -> left
        else -> maxOf(left, right)
    }
}

internal fun historyObservationTimestamp(value: String?): Long? = value
    ?.takeIf { it.isNotBlank() }
    ?.takeIf(HISTORY_UTC_MILLIS::matches)
    ?.let(::parseHistoryTimestamp)

/** A request started before discovery must be replaced once its peer identity is known. */
internal fun shouldRetryHistoryAfterPeerResolution(
    request: HistoryHydrationFence?,
    selectedPeer: String,
    resolvedPeer: String,
    previousInstanceId: String?,
    resolvedInstanceId: String?,
    currentConnectionGeneration: Int,
): Boolean = request != null
    && selectedPeer == resolvedPeer
    && resolvedInstanceId != null
    && previousInstanceId != resolvedInstanceId
    && request.peer == resolvedPeer
    && request.connectionGeneration == currentConnectionGeneration
    && request.targetInstanceId != resolvedInstanceId

/** Serializes the short local history/state transaction; network fetches run outside it. */
internal class ConversationStateLock(
    // The default callbacks keep production behavior unchanged. Tests use them only to
    // observe the monitor boundary and hold the first owner while contenders arrive.
    private val onLockAttempted: () -> Unit = {},
    private val onLockAcquired: () -> Unit = {},
) {
    private val monitor = Any()

    fun <T> withLock(block: () -> T): T {
        onLockAttempted()
        return synchronized(monitor) {
            onLockAcquired()
            block()
        }
    }

    internal fun isHeldByCurrentThread(): Boolean = Thread.holdsLock(monitor)
}

/** Incoming delivery may be acknowledged only when this predicate is true. */
internal fun isRetainedConversationMessage(message: ChatMessage, nowMs: Long): Boolean {
    val cutoff = nowMs - HISTORY_RETENTION_MS
    return historyMessageTimeMs(message, nowMs) >= cutoff
        || message.deliveryStatus == OutboundDeliveryStatus.PENDING
}

/**
 * Mirrors the Web UI's message timestamp fallback: an absent, malformed, or
 * epoch timestamp is treated as now, so locally-created messages remain visible.
 */
internal fun historyMessageTimeMs(message: ChatMessage, nowMs: Long): Long {
    val candidate = message.recordedAt?.takeIf { it.isNotBlank() } ?: message.createdAt
    val parsed = parseHistoryTimestamp(candidate)
    return if (parsed == null || parsed == 0L) nowMs else parsed
}

internal fun pruneConversationHistory(
    messages: List<ChatMessage>,
    nowMs: Long,
): List<ChatMessage> {
    val cutoff = nowMs - HISTORY_RETENTION_MS
    val ordered = messages.withIndex()
        .map { indexed -> indexed to historyMessageTimeMs(indexed.value, nowMs) }
        .filter { (indexed, timestamp) ->
            timestamp >= cutoff || indexed.value.deliveryStatus == OutboundDeliveryStatus.PENDING
        }
        .sortedWith(compareBy<Pair<IndexedValue<ChatMessage>, Long>> { it.second }.thenBy { it.first.index })

    if (ordered.size <= MAX_HISTORY_MESSAGES) return ordered.map { it.first.value }

    // A pending optimistic bubble is active UI state. Keep it while evicting
    // the oldest settled rows, then apply the same chronological ordering.
    val pending = ordered.filter { it.first.value.deliveryStatus == OutboundDeliveryStatus.PENDING }
    val settled = ordered.filter { it.first.value.deliveryStatus != OutboundDeliveryStatus.PENDING }
    val retained = if (pending.size >= MAX_HISTORY_MESSAGES) {
        pending.takeLast(MAX_HISTORY_MESSAGES)
    } else {
        settled.takeLast(MAX_HISTORY_MESSAGES - pending.size) + pending
    }
    return retained.sortedWith(compareBy<Pair<IndexedValue<ChatMessage>, Long>> { it.second }.thenBy { it.first.index })
        .map { it.first.value }
}

internal fun historyPreferenceKey(router: String, agentId: String, instanceId: String, peer: String): String =
    "history:$router:$agentId:$instanceId:$peer"

private fun parseHistoryTimestamp(value: String): Long? = runCatching {
    Instant.parse(value).toEpochMilli()
}.recoverCatching {
    OffsetDateTime.parse(value).toInstant().toEpochMilli()
}.getOrNull()
