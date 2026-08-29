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
)

/** Proof returned by the same retention pass that wrote local history. */
internal data class DeliveryPersistenceProof(
    val committed: Boolean,
    val retainedMessageKeys: Set<String> = emptySet(),
)

internal data class SettledDelivery(
    val connection: DeliveryConnectionIdentity,
    val peer: String,
    val messageId: String,
    val attemptNumber: Int,
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
        if (!proof.committed || delivery.message.key !in proof.retainedMessageKeys) return@withLock null
        SettledDelivery(delivery.connection, delivery.peer, delivery.message.id, delivery.attemptNumber)
    }
}
