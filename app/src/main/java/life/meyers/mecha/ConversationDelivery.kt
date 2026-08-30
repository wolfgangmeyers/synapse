package life.meyers.mecha

/**
 * The connection identity observed by one WebSocket callback. Every value is copied
 * before a delivery is persisted, so a reconnect cannot change the receipt scope.
 */
internal data class DeliveryConnectionIdentity(
    val router: String,
    val agentId: String,
    val instanceId: String,
    val generation: Int,
    val leaseId: String,
)

internal data class CapturedDelivery(
    val connection: DeliveryConnectionIdentity,
    val peer: String,
    val targetInstanceId: String?,
    val message: ChatMessage,
    val attemptNumber: Int,
    /** Correlated sessions require incarnation identity and use the replay ledger. */
    val correlatedAck: Boolean = true,
    /** Every original message represented by the delivery frame, including the first one. */
    val messages: List<ChatMessage> = listOf(message),
    val deliveryItems: List<ValidatedDeliveryItem> = emptyList(),
    val messagesByPeer: Map<String, List<ChatMessage>> = mapOf(peer to messages),
    val targetInstanceIdsByPeer: Map<String, String?> = mapOf(peer to targetInstanceId),
)

internal data class DeliveredNotification(
    val peer: String,
    val message: ChatMessage,
)

/** Proof returned by the same retention pass that wrote local history. */
internal data class DeliveryPersistenceProof(
    val committed: Boolean,
    val retainedMessageKeys: Set<String> = emptySet(),
    val notifications: List<DeliveredNotification> = emptyList(),
)

internal data class SettledDelivery(
    val connection: DeliveryConnectionIdentity,
    val peer: String,
    val messageId: String,
    val attemptNumber: Int,
    val correlatedAck: Boolean = true,
    val messages: List<ChatMessage> = emptyList(),
    val deliveryItems: List<ValidatedDeliveryItem> = emptyList(),
    val notifications: List<DeliveredNotification> = emptyList(),
)

/**
 * Serializes connection fencing, delivery capture, and the persistence callback.
 * Receipt construction and socket writes happen after [settle] releases the lock.
 */
internal class ConversationDeliveryCoordinator(
    private val stateLock: ConversationStateLock,
) {
    private var currentConnection: DeliveryConnectionIdentity? = null

    fun begin(connection: DeliveryConnectionIdentity) = stateLock.withLock {
        currentConnection = connection.copy(leaseId = "")
    }

    fun register(generation: Int, leaseId: String): Boolean = stateLock.withLock {
        val current = currentConnection
        if (current == null || current.generation != generation || leaseId.isBlank()) return@withLock false
        currentConnection = current.copy(leaseId = leaseId)
        true
    }

    fun invalidate() = stateLock.withLock {
        currentConnection = null
    }

    /** Capture the immutable connection identity at the callback boundary. */
    fun capture(
        callbackGeneration: Int,
        build: (DeliveryConnectionIdentity) -> CapturedDelivery?,
    ): CapturedDelivery? = stateLock.withLock {
        val connection = currentConnection ?: return@withLock null
        if (connection.generation != callbackGeneration || connection.leaseId.isBlank()) return@withLock null
        build(connection)?.copy(connection = connection)
    }

    /**
     * Revalidate the captured identity and persist while holding the state lock.
     * The returned proof is consumed before any receipt or socket write occurs.
     */
    fun settle(
        delivery: CapturedDelivery,
        persist: (CapturedDelivery) -> DeliveryPersistenceProof,
    ): SettledDelivery? = stateLock.withLock {
        if (currentConnection != delivery.connection) return@withLock null
        val proof = persist(delivery)
        if (!proof.committed || delivery.messages.any { it.key !in proof.retainedMessageKeys }) return@withLock null
        SettledDelivery(
            connection = delivery.connection,
            peer = delivery.peer,
            messageId = delivery.message.id,
            attemptNumber = delivery.attemptNumber,
            correlatedAck = delivery.correlatedAck,
            messages = delivery.messages,
            deliveryItems = delivery.deliveryItems,
            notifications = proof.notifications,
        )
    }
}
