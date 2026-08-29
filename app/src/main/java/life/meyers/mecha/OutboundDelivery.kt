package life.meyers.mecha

/** The only outbound states exposed by the composer. */
internal enum class OutboundDeliveryStatus { NONE, PENDING, SENT, FAILED, UNCERTAIN }

internal const val MAX_PENDING_OUTBOUND_OPERATIONS = 100
internal const val OUTBOUND_DELIVERY_TIMEOUT_MS = 30_000L

internal data class OutboundScope(
    val router: String,
    val connectionGeneration: Int,
    val peer: String,
    val targetInstanceId: String?,
)

internal data class OutboundOperation(
    val localId: String,
    val requestId: String,
    val frameId: String?,
    val scope: OutboundScope,
    val message: ChatMessage,
    /** The body sent to the router. The message body may include local attachment labels. */
    val requestBody: String,
    /** Attachment metadata for correlation; local data URLs stay out of persisted history. */
    val requestAttachments: List<ImageReference> = message.attachments,
    val deadlineAtMs: Long,
)

internal data class OutboundReducerState(
    val messagesByPeer: Map<String, List<ChatMessage>> = emptyMap(),
    val pending: Map<String, OutboundOperation> = emptyMap(),
    val settled: Map<String, OutboundOperation> = emptyMap(),
    val error: String? = null,
)

internal sealed interface OutboundDeliveryEvent {
    data class Accepted(
        val localId: String,
        val requestId: String,
        val scope: OutboundScope,
        val message: ChatMessage,
        val frameId: String? = null,
    ) : OutboundDeliveryEvent

    data class Rejected(
        val localId: String,
        val requestId: String,
        val scope: OutboundScope,
        val message: String,
        val frameId: String? = null,
    ) : OutboundDeliveryEvent

    data class Uncertain(
        val localId: String,
        val requestId: String,
        val scope: OutboundScope,
        val message: String,
        val frameId: String? = null,
    ) : OutboundDeliveryEvent

    /** A history or websocket echo has no HTTP request id, so its envelope id is the correlation key. */
    data class HistoryEcho(
        val scope: OutboundScope,
        val message: ChatMessage,
    ) : OutboundDeliveryEvent

    data class Timeout(
        val localId: String,
        val scope: OutboundScope,
        val nowMs: Long,
    ) : OutboundDeliveryEvent

    data object ConnectionChanged : OutboundDeliveryEvent
}

internal fun reduceOutboundDelivery(
    state: OutboundReducerState,
    event: OutboundDeliveryEvent,
): OutboundReducerState = when (event) {
    is OutboundDeliveryEvent.Accepted -> settleOutbound(
        state,
        event.localId,
        event.requestId,
        event.frameId,
        event.scope,
        event.message,
        OutboundDeliveryStatus.SENT,
        null,
    )
    is OutboundDeliveryEvent.Rejected -> settleOutbound(
        state,
        event.localId,
        event.requestId,
        event.frameId,
        event.scope,
        null,
        OutboundDeliveryStatus.FAILED,
        event.message,
    )
    is OutboundDeliveryEvent.Uncertain -> settleOutbound(
        state,
        event.localId,
        event.requestId,
        event.frameId,
        event.scope,
        null,
        OutboundDeliveryStatus.UNCERTAIN,
        event.message,
    )
    is OutboundDeliveryEvent.HistoryEcho -> acceptHistoryEcho(state, event.scope, event.message)
    is OutboundDeliveryEvent.Timeout -> {
        val pending = state.pending[event.localId]
        if (pending == null || !sameOutboundScope(pending.scope, event.scope) || event.nowMs < pending.deadlineAtMs) {
            state
        } else {
            settleOutbound(
                state,
                pending.localId,
                pending.requestId,
                pending.frameId,
                pending.scope,
                null,
                OutboundDeliveryStatus.UNCERTAIN,
                "Message delivery was not confirmed; the router may have accepted it.",
            )
        }
    }
    OutboundDeliveryEvent.ConnectionChanged -> {
        var next = state
        state.pending.values.toList().forEach { pending ->
            next = settleOutbound(
                next,
                pending.localId,
                pending.requestId,
                pending.frameId,
                pending.scope,
                null,
                OutboundDeliveryStatus.UNCERTAIN,
                "Message delivery was not confirmed; the router may have accepted it.",
            )
        }
        next
    }
}

internal fun enqueueOutboundDelivery(
    state: OutboundReducerState,
    operation: OutboundOperation,
): OutboundReducerState {
    if (state.pending.containsKey(operation.localId) || state.settled.containsKey(operation.localId)) return state
    var next = state.copy(error = null)
    if (next.pending.size >= MAX_PENDING_OUTBOUND_OPERATIONS) {
        val oldest = next.pending.values.firstOrNull()
        if (oldest != null) {
            next = settleOutbound(
                next,
                oldest.localId,
                oldest.requestId,
                oldest.frameId,
                oldest.scope,
                null,
                OutboundDeliveryStatus.UNCERTAIN,
                "An earlier message was not confirmed.",
            )
        }
    }
    val message = operation.message.copy(deliveryStatus = OutboundDeliveryStatus.PENDING)
    val peerMessages = next.messagesByPeer[operation.scope.peer].orEmpty()
    val updatedMessages = replaceLocalMessage(peerMessages, message)
    return next.copy(
        messagesByPeer = next.messagesByPeer + (operation.scope.peer to updatedMessages),
        pending = next.pending + (operation.localId to operation.copy(message = message)),
        error = null,
    )
}

internal fun sameOutboundScope(left: OutboundScope, right: OutboundScope): Boolean =
    left.router == right.router
        && left.connectionGeneration == right.connectionGeneration
        && left.peer == right.peer
        && (right.targetInstanceId == null || left.targetInstanceId == right.targetInstanceId)

internal fun sameOutgoingEnvelope(expected: ChatMessage, actual: ChatMessage): Boolean =
    expected.id == actual.id
        && expected.sender == actual.sender
        && (expected.recipient.isBlank() || expected.recipient == actual.recipient)
        && expected.body == actual.body
        && expected.createdAt == actual.createdAt
        && sameAttachmentMetadata(expected.attachments, actual.attachments)

internal fun matchesOutboundEnvelope(operation: OutboundOperation, actual: ChatMessage): Boolean =
    operation.message.id == actual.id
        && operation.message.sender == actual.sender
        && operation.scope.peer == actual.recipient
        && operation.requestBody == actual.body
        && operation.message.createdAt == actual.createdAt
        && sameAttachmentMetadata(operation.requestAttachments, actual.attachments)

internal fun pendingMessageDisplayBody(body: String, images: List<PendingImage>): String {
    if (images.isEmpty()) return body
    val text = body.trim()
    return buildString {
        append(text)
        if (text.isNotEmpty()) append("\n\n")
        append("Attached image")
        if (images.size != 1) append('s')
        append(":\n")
        images.forEachIndexed { index, image ->
            if (index > 0) append('\n')
            append(index + 1).append(". ").append(image.name.ifBlank { "image.jpg" })
        }
    }
}

private fun sameAttachmentMetadata(expected: List<ImageReference>, actual: List<ImageReference>): Boolean {
    if (expected.size != actual.size) return false
    return expected.zip(actual).all { (left, right) ->
        left.mimeType == right.mimeType
            && left.sizeBytes == right.sizeBytes
            && (right.name.isBlank() || left.name == right.name)
    }
}

private fun settleOutbound(
    state: OutboundReducerState,
    localId: String,
    requestId: String,
    frameId: String?,
    scope: OutboundScope,
    acceptedMessage: ChatMessage?,
    status: OutboundDeliveryStatus,
    error: String?,
): OutboundReducerState {
    val pending = state.pending[localId] ?: return state
    if (pending.requestId != requestId || pending.frameId != frameId || !sameOutboundScope(pending.scope, scope)) return state
    if (acceptedMessage != null && !matchesOutboundEnvelope(pending, acceptedMessage)) return state
    val canonical = (acceptedMessage ?: pending.message).copy(deliveryStatus = status)
    val updatedMessages = replaceLocalMessage(state.messagesByPeer[scope.peer].orEmpty(), canonical)
    val pendingNext = state.pending - localId
    val settledNext = boundedSettled(state.settled + (localId to pending.copy(message = canonical)))
    return state.copy(
        messagesByPeer = state.messagesByPeer + (scope.peer to updatedMessages),
        pending = pendingNext,
        settled = settledNext,
        error = error,
    )
}

private fun acceptHistoryEcho(
    state: OutboundReducerState,
    scope: OutboundScope,
    echo: ChatMessage,
): OutboundReducerState {
    val pending = state.pending.values.firstOrNull { operation ->
        operation.message.id == echo.id
            && sameOutboundScope(operation.scope, scope)
            && matchesOutboundEnvelope(operation, echo)
    }
    if (pending != null) {
        return settleOutbound(
            state,
            pending.localId,
            pending.requestId,
            pending.frameId,
            scope,
            echo,
            OutboundDeliveryStatus.SENT,
            null,
        )
    }
    val settled = state.settled.values.firstOrNull { operation ->
        operation.message.id == echo.id
            && operation.message.deliveryStatus == OutboundDeliveryStatus.UNCERTAIN
            && sameOutboundScope(operation.scope, scope)
            && matchesOutboundEnvelope(operation, echo)
    } ?: return state
    val canonical = echo.copy(deliveryStatus = OutboundDeliveryStatus.SENT)
    return state.copy(
        messagesByPeer = state.messagesByPeer + (scope.peer to replaceLocalMessage(state.messagesByPeer[scope.peer].orEmpty(), canonical)),
        settled = state.settled - settled.localId,
        error = null,
    )
}

private fun boundedSettled(values: Map<String, OutboundOperation>): Map<String, OutboundOperation> =
    values.entries.toList().takeLast(MAX_PENDING_OUTBOUND_OPERATIONS).associate { it.key to it.value }

private fun replaceLocalMessage(messages: List<ChatMessage>, replacement: ChatMessage): List<ChatMessage> {
    val withoutSameId = messages.filterNot { it.id == replacement.id }
    return pruneConversationHistory(
        (withoutSameId + replacement).sortedWith(
            compareBy<ChatMessage> { if (it.sequence == 0L) Long.MAX_VALUE else it.sequence }.thenBy { it.createdAt },
        ),
        System.currentTimeMillis(),
    )
}
