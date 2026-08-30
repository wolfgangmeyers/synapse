package life.meyers.mecha

import java.time.Instant
import java.util.concurrent.CountDownLatch
import java.util.concurrent.CyclicBarrier
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicReference
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class ConversationDeliveryCoordinatorTest {
    @Test
    fun capturedLeaseAndGenerationCannotBeReusedAfterConnectionChanges() {
        val lock = ConversationStateLock()
        val coordinator = ConversationDeliveryCoordinator(lock)
        val oldConnection = connection(generation = 4, leaseId = "lease-old")
        coordinator.begin(oldConnection)
        assertTrue(coordinator.register(4, "lease-old"))
        val captured = coordinator.capture(4) { identity -> delivery(identity, "old") }
        assertNotNull(captured)

        coordinator.register(4, "lease-new")
        var persistenceCalls = 0
        assertNull(coordinator.settle(captured!!, persist = {
            persistenceCalls++
            proof(it.message)
        }))
        assertEquals(0, persistenceCalls)

        coordinator.begin(connection(generation = 5, leaseId = "lease-next"))
        assertNull(coordinator.settle(captured, persist = {
            persistenceCalls++
            proof(it.message)
        }))
        assertEquals(0, persistenceCalls)
    }

    @Test
    fun finalRetentionProofControlsWhetherAReceiptCanBeProduced() {
        val lock = ConversationStateLock()
        val coordinator = ConversationDeliveryCoordinator(lock)
        coordinator.begin(connection(generation = 1, leaseId = "lease-1"))
        assertTrue(coordinator.register(1, "lease-1"))
        val clockNow = System.currentTimeMillis()
        val boundaryMessageTime = clockNow - HISTORY_RETENTION_MS
        val captured = coordinator.capture(1) { identity ->
            delivery(identity, "at-boundary", message("at-boundary", boundaryMessageTime))
        }
        var networkSends = 0

        val rejected = coordinator.settle(captured!!, persist = {
            // A later clock read would prune this boundary row. The final write proof
            // therefore contains no key, and the caller sends no receipt.
            val finalRows = pruneConversationHistory(listOf(it.message), clockNow + 1)
            DeliveryPersistenceProof(committed = true, retainedMessageKeys = finalRows.map(ChatMessage::key).toSet())
        })
        if (rejected != null) networkSends += 2

        assertNull(rejected)
        assertEquals(0, networkSends)
    }

    @Test
    fun batchSettlementRequiresEveryItemToBeRetainedBeforeReceiptsCanBeBuilt() {
        val lock = ConversationStateLock()
        val coordinator = ConversationDeliveryCoordinator(lock)
        coordinator.begin(connection(generation = 2, leaseId = "lease-2"))
        assertTrue(coordinator.register(2, "lease-2"))
        val first = message("first", System.currentTimeMillis())
        val second = message("second", System.currentTimeMillis())
        val captured = coordinator.capture(2) { identity ->
            CapturedDelivery(
                connection = identity,
                peer = "peer",
                targetInstanceId = null,
                message = first,
                attemptNumber = 1,
                messages = listOf(first, second),
            )
        }

        assertNull(coordinator.settle(captured!!, persist = {
            DeliveryPersistenceProof(committed = true, retainedMessageKeys = setOf(first.key))
        }))
    }

    @Test
    fun appendHydrationOptimisticSendAndSettlementCompeteOnTheConcreteCoordinatorSeam() {
        // MainActivity's Compose tree is not the mutation boundary. These are the four
        // callbacks that use its concrete ConversationStateLock/Coordinator seam.
        val activeGate = AtomicReference<ContentionGate?>()
        val lock = ConversationStateLock(
            onLockAttempted = { activeGate.get()?.lockAttempted() },
            onLockAcquired = { activeGate.get()?.lockAcquired() },
        )
        val coordinator = ConversationDeliveryCoordinator(lock)
        coordinator.begin(connection(generation = 9, leaseId = "lease-9"))
        assertTrue(coordinator.register(9, "lease-9"))

        val rounds = 32
        val now = System.currentTimeMillis()
        val peer = "peer"
        val scope = OutboundScope("router-a", 9, peer, null)
        var state = OutboundReducerState()
        val networkCallbacksUnderLock = AtomicBoolean(false)
        val receiptCallbacksUnderLock = AtomicBoolean(false)
        val networkCallbacks = AtomicInteger(0)
        val receiptCallbacks = AtomicInteger(0)
        val starts = Array(rounds) { CyclicBarrier(6) }
        val gates = Array(rounds) { ContentionGate(5) }
        val currentHydrationFences = Array(rounds) { round ->
            HistoryHydrationFence(
                router = "router-a",
                ownerAgentId = "android",
                ownerInstanceId = "owner-1",
                peer = peer,
                targetInstanceId = null,
                connectionGeneration = 9,
                requestSerial = round.toLong() + 1,
            )
        }
        val staleHydrationFences = currentHydrationFences.map { it.copy(requestSerial = 0) }
        val capturedSettlements = Array(rounds) { round ->
            coordinator.capture(9) { identity ->
                delivery(identity, "settle-$round", message("settle-$round", now))
            }
        }
        assertTrue(capturedSettlements.all { it != null })

        fun networkCallback() {
            networkCallbacksUnderLock.set(
                networkCallbacksUnderLock.get() || lock.isHeldByCurrentThread(),
            )
            networkCallbacks.incrementAndGet()
        }
        fun receiptCallback() {
            receiptCallbacksUnderLock.set(
                receiptCallbacksUnderLock.get() || lock.isHeldByCurrentThread(),
            )
            receiptCallbacks.incrementAndGet()
        }
        fun updateMessages(before: OutboundReducerState, incoming: ChatMessage): OutboundReducerState {
            val updated = mergeConversationMessages(before.messagesByPeer[peer].orEmpty(), listOf(incoming), now)
            return before.copy(messagesByPeer = before.messagesByPeer + (peer to updated))
        }
        fun applyHydration(
            round: Int,
            fence: HistoryHydrationFence,
            hydrated: ChatMessage,
        ) {
            networkCallback()
            lock.withLock {
                val before = state
                if (isCurrentHistoryHydration(
                        fence,
                        currentRouter = "router-a",
                        currentOwnerAgentId = "android",
                        currentOwnerInstanceId = "owner-1",
                        currentPeer = peer,
                        currentTargetInstanceId = null,
                        currentConnectionGeneration = 9,
                        currentRequestSerial = currentHydrationFences[round].requestSerial,
                    )
                ) {
                    state = updateMessages(before, hydrated)
                }
            }
        }

        val operations: List<(Int) -> Unit> = listOf(
            { round ->
                lock.withLock {
                    val before = state
                    state = updateMessages(before, message("append-$round", now))
                }
            },
            { round ->
                applyHydration(
                    round,
                    staleHydrationFences[round],
                    message("stale-hydrate-$round", now),
                )
            },
            { round ->
                applyHydration(
                    round,
                    currentHydrationFences[round],
                    message("hydrate-$round", now),
                )
            },
            { round ->
                val operation = outboundOperation("send-$round", scope, now)
                networkCallback()
                lock.withLock {
                    val before = state
                    val withHistory = before.copy(
                        messagesByPeer = before.messagesByPeer + (peer to before.messagesByPeer[peer].orEmpty()),
                    )
                    state = enqueueOutboundDelivery(withHistory, operation)
                }
            },
            { round ->
                val settled = coordinator.settle(capturedSettlements[round]!!, persist = { captured ->
                    val before = state
                    val updated = updateMessages(before, captured.message)
                    state = updated
                    DeliveryPersistenceProof(true, updated.messagesByPeer[peer].orEmpty().map(ChatMessage::key).toSet())
                })
                if (settled == null) throw AssertionError("settlement was rejected in round $round")
                receiptCallback()
            },
        )
        val executor = Executors.newFixedThreadPool(operations.size)
        try {
            val futures = operations.map { operation ->
                executor.submit {
                    repeat(rounds) { round ->
                        await(starts[round], "round $round start")
                        try {
                            operation(round)
                        } finally {
                            gates[round].completed.countDown()
                        }
                    }
                }
            }
            repeat(rounds) { round ->
                activeGate.set(gates[round])
                await(starts[round], "round $round coordinator start")
                await(gates[round].attempted, "round $round monitor attempts")
                await(gates[round].firstAcquired, "round $round first lock holder")
                assertEquals(
                    "round $round acquired more than one mutation lock before release",
                    1,
                    gates[round].acquired.get(),
                )
                if (round == 0) {
                    assertFalse(
                        "all operations acquired the mutation lock before the first holder was released",
                        gates[round].allAcquired.await(100, TimeUnit.MILLISECONDS),
                    )
                }
                gates[round].release.countDown()
                await(gates[round].completed, "round $round completion")

                val ids = state.messagesByPeer[peer].orEmpty().map(ChatMessage::id).toSet()
                assertEquals(
                    setOf("append-$round", "hydrate-$round", "send-$round", "settle-$round") +
                        (0 until round).flatMap { previous ->
                            listOf("append-$previous", "hydrate-$previous", "send-$previous", "settle-$previous")
                        }.toSet(),
                    ids,
                )
                assertTrue("stale hydration applied in round $round", "stale-hydrate-$round" !in ids)
            }
            futures.forEach { it.get(5, TimeUnit.SECONDS) }
        } finally {
            executor.shutdownNow()
        }

        assertEquals(rounds * 3, networkCallbacks.get())
        assertEquals(rounds, receiptCallbacks.get())
        assertFalse(networkCallbacksUnderLock.get())
        assertFalse(receiptCallbacksUnderLock.get())
        assertEquals(rounds * 4, state.messagesByPeer[peer].orEmpty().size)
    }

    @Test
    fun optimisticSendDoesNotReachHttpWhenItsLocalCommitFails() {
        val now = System.currentTimeMillis()
        val scope = OutboundScope("router-a", 1, "peer", null)
        val operation = outboundOperation("send", scope, now)
        var persistenceCalls = 0
        val result = persistOptimisticSend(OutboundReducerState(), operation) {
            persistenceCalls++
            false
        }
        var httpCalls = 0
        if (result.persisted) httpCalls++

        assertFalse(result.persisted)
        assertEquals(1, persistenceCalls)
        assertEquals(0, httpCalls)
        assertTrue(result.state.pending.isEmpty())
        assertTrue(result.state.messagesByPeer.isEmpty())
    }

    @Test
    fun routerSwitchSettlesAndPersistsPendingMessagesBeforeClearingOutboundScope() {
        val now = System.currentTimeMillis()
        val oldScope = OutboundScope("router-old", 4, "peer", "instance-old")
        val operation = outboundOperation("pending", oldScope, now)
        val state = enqueueOutboundDelivery(OutboundReducerState(), operation)
        val persisted = mutableListOf<Triple<String, String, List<ChatMessage>>>()
        val result = settleOutboundForRouterSwitch(state, "router-old") { router, peer, messages ->
            persisted += Triple(router, peer, messages)
            true
        }

        assertTrue(result.persisted)
        assertTrue(result.state.pending.isEmpty())
        assertTrue(result.state.messagesByPeer.isEmpty())
        assertEquals("router-old", persisted.single().first)
        assertEquals("peer", persisted.single().second)
        assertEquals(OutboundDeliveryStatus.UNCERTAIN, persisted.single().third.single().deliveryStatus)
        assertEquals("router-old", state.pending.values.single().scope.router)
    }

    @Test
    fun routerSwitchKeepsScopedStateWhenSettledHistoryCannotBePersisted() {
        val now = System.currentTimeMillis()
        val scope = OutboundScope("router-old", 4, "peer", "instance-old")
        val state = enqueueOutboundDelivery(OutboundReducerState(), outboundOperation("pending", scope, now))
        val result = settleOutboundForRouterSwitch(state, "router-old") { _, _, _ -> false }

        assertFalse(result.persisted)
        assertEquals(state, result.state)
    }

    @Test
    fun newerInstanceEventWinsWhenAStartedDiscoveryReturnsLate() {
        val fence = PeerStateEventFence()
        fence.reset(3)
        val discovery = fence.begin(3)!!
        val event = fence.begin(3)!!
        val old = PeerStatus("peer", activityState = "idle", instanceId = "old-instance")
        val source = JSONObject()
            .put("instance_id", "new-instance")
            .put("activity_state", "active")
            .put("subagent_count", 0)
        var peers = listOf(old)
        if (fence.isNewest(event)) {
            peers = peers.map { if (it.id == "peer") peerStatusFromJson("peer", source, it) else it }
        }
        if (fence.isNewest(discovery)) peers = listOf(old)

        assertEquals("new-instance", peers.single().instanceId)
        assertEquals("active", peers.single().activityState)
        assertFalse(fence.isNewest(discovery))

        fence.reset(4)
        assertFalse(fence.isNewest(event))
    }

    @Test
    fun unrelatedPeerEventDoesNotInvalidateOtherEntriesInAFullDiscoverySnapshot() {
        val fence = PeerStateEventFence()
        fence.reset(3)
        val discovery = fence.beginDiscovery(3)!!

        assertNotNull(fence.observeEvent(3, "peer-b", "instance-b", 200L))
        assertTrue(fence.isCurrentForPeer(discovery, "peer-a"))
        assertFalse(fence.isCurrentForPeer(discovery, "peer-b"))
    }

    @Test
    fun delayedOldInstanceEventCannotReplaceANewerDiscoveryInstance() {
        val fence = PeerStateEventFence()
        fence.reset(3)
        val discovery = fence.beginDiscovery(3)!!
        assertTrue(fence.acceptDiscovery(discovery, "peer", "instance-new", 200L))

        assertNull(fence.observeEvent(3, "peer", "instance-old", 100L))
        assertNotNull(fence.observeEvent(3, "peer", "instance-new", 300L))
    }

    @Test
    fun newerInstanceActivityReplacesOldProjectionInsteadOfBeingDiscarded() {
        val old = PeerStatus(
            "peer",
            activityState = "active",
            subagentCount = 1,
            subagents = listOf(SubagentSummary("old-child", "Old")),
            instanceId = "old-instance",
        )
        val updated = peerStatusFromJson(
            "peer",
            JSONObject()
                .put("instance_id", "new-instance")
                .put("activity_state", "active")
                .put("subagent_count", 1)
                .put("subagents", org.json.JSONArray().put(JSONObject().put("id", "new-child").put("description", "New"))),
            old,
        )

        assertEquals("new-instance", updated.instanceId)
        assertEquals(listOf("new-child"), updated.subagents.map(SubagentSummary::id))
        assertEquals(0, updated.modelCatalog.models.size)
        assertEquals(null, updated.goalState)
    }

    @Test
    fun peerDiscoveryRetriesHydrationWhenItResolvesAnUnknownInstance() {
        val request = HistoryHydrationFence(
            router = "router-a",
            ownerAgentId = "android",
            ownerInstanceId = "owner-1",
            peer = "peer",
            targetInstanceId = null,
            connectionGeneration = 3,
            requestSerial = 7,
        )

        assertTrue(shouldRetryHistoryAfterPeerResolution(request, "peer", "peer", null, "peer-instance-2", 3))
        assertFalse(shouldRetryHistoryAfterPeerResolution(request, "other-peer", "peer", null, "peer-instance-2", 3))
        assertFalse(shouldRetryHistoryAfterPeerResolution(request, "peer", "peer", null, "peer-instance-2", 4))
        assertFalse(shouldRetryHistoryAfterPeerResolution(request, "peer", "peer", null, null, 3))

        val retry = request.copy(targetInstanceId = "peer-instance-2", requestSerial = 8)
        assertTrue(isCurrentHistoryHydration(
            retry,
            currentRouter = "router-a",
            currentOwnerAgentId = "android",
            currentOwnerInstanceId = "owner-1",
            currentPeer = "peer",
            currentTargetInstanceId = "peer-instance-2",
            currentConnectionGeneration = 3,
            currentRequestSerial = 8,
        ))
    }

    @Test
    fun hydrationFenceRejectsNewPeerInstanceWhenRequestStartedUnknown() {
        val request = HistoryHydrationFence(
            router = "router-a",
            ownerAgentId = "android",
            ownerInstanceId = "owner-1",
            peer = "peer",
            targetInstanceId = null,
            connectionGeneration = 3,
            requestSerial = 7,
        )

        assertFalse(isCurrentHistoryHydration(
            request,
            currentRouter = "router-a",
            currentOwnerAgentId = "android",
            currentOwnerInstanceId = "owner-1",
            currentPeer = "peer",
            currentTargetInstanceId = "peer-instance-2",
            currentConnectionGeneration = 3,
            currentRequestSerial = 7,
        ))
        assertTrue(isCurrentHistoryHydration(
            request,
            currentRouter = "router-a",
            currentOwnerAgentId = "android",
            currentOwnerInstanceId = "owner-1",
            currentPeer = "peer",
            currentTargetInstanceId = null,
            currentConnectionGeneration = 3,
            currentRequestSerial = 7,
        ))
    }

    private fun connection(generation: Int, leaseId: String) = DeliveryConnectionIdentity(
        router = "router-a",
        agentId = "android",
        instanceId = "owner-1",
        generation = generation,
        leaseId = leaseId,
    )

    private fun delivery(
        connection: DeliveryConnectionIdentity,
        id: String,
        message: ChatMessage = message(id, System.currentTimeMillis()),
    ) = CapturedDelivery(
        connection = connection,
        peer = "peer",
        targetInstanceId = null,
        message = message,
        attemptNumber = 2,
    )

    private fun outboundOperation(id: String, scope: OutboundScope, now: Long) = OutboundOperation(
        localId = id,
        requestId = id,
        frameId = null,
        scope = scope,
        message = outgoing(id, now).copy(deliveryStatus = OutboundDeliveryStatus.PENDING),
        requestBody = "body-$id",
        deadlineAtMs = now + OUTBOUND_DELIVERY_TIMEOUT_MS,
    )

    private fun proof(message: ChatMessage) = DeliveryPersistenceProof(true, setOf(message.key))

    private fun message(id: String, now: Long) = ChatMessage(
        id = id,
        incarnationId = "inc-$id",
        sequence = 0,
        sender = "peer",
        body = id,
        direction = Direction.INCOMING,
        createdAt = Instant.ofEpochMilli(now).toString(),
    )

    private fun outgoing(id: String, now: Long) = message(id, now).copy(
        sender = "android",
        recipient = "peer",
        direction = Direction.OUTGOING,
    )

    private fun await(latch: CountDownLatch, label: String) {
        assertTrue("$label timed out", latch.await(5, TimeUnit.SECONDS))
    }

    private fun await(barrier: CyclicBarrier, label: String) {
        try {
            barrier.await(5, TimeUnit.SECONDS)
        } catch (error: Exception) {
            throw AssertionError("$label failed", error)
        }
    }

    private class ContentionGate(contenders: Int) {
        val attempted = CountDownLatch(contenders)
        val firstAcquired = CountDownLatch(1)
        val allAcquired = CountDownLatch(contenders)
        val completed = CountDownLatch(contenders)
        val release = CountDownLatch(1)
        val acquired = AtomicInteger()

        fun lockAttempted() {
            attempted.countDown()
        }

        fun lockAcquired() {
            acquired.incrementAndGet()
            firstAcquired.countDown()
            allAcquired.countDown()
            if (!release.await(5, TimeUnit.SECONDS)) {
                throw AssertionError("contention gate was not released")
            }
        }
    }
}
