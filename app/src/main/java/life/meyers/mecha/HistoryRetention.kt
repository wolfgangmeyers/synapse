package life.meyers.mecha

import java.time.Instant
import java.time.OffsetDateTime

internal const val MAX_HISTORY_MESSAGES = 200
internal const val HISTORY_RETENTION_MS = 30L * 24 * 60 * 60 * 1000

internal data class HistoryPersistenceProof(
    val committed: Boolean,
    val retainedMessageKeys: Set<String>,
)

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
