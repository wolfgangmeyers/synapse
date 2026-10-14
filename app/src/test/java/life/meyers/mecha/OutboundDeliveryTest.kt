package life.meyers.mecha

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.util.concurrent.CountDownLatch

class OutboundDeliveryTest {
    private val scope = OutboundScope("router-a", 7, "peer", "instance-a")

    @Test
    fun enqueueShowsPendingMessageWithStableLocalIdentity() {
        val operation = operation("message-1", scope)
        val state = enqueueOutboundDelivery(OutboundReducerState(), operation)

        assertEquals(OutboundDeliveryStatus.PENDING, state.messagesByPeer["peer"]!!.single().deliveryStatus)
        assertEquals("message-1", state.messagesByPeer["peer"]!!.single().id)
        assertTrue(state.pending.containsKey("message-1"))
    }

    @Test
    fun acceptedHttpResultReplacesPendingWithCanonicalEcho() {
        val operation = operation("message-1", scope)
        val pending = enqueueOutboundDelivery(OutboundReducerState(), operation)
        val canonical = operation.message.copy(incarnationId = "router-incarnation", deliveryStatus = OutboundDeliveryStatus.NONE)
        val settled = reduceOutboundDelivery(
            pending,
            OutboundDeliveryEvent.Accepted("message-1", "message-1", scope, canonical),
        )

        assertEquals(OutboundDeliveryStatus.SENT, settled.messagesByPeer["peer"]!!.single().deliveryStatus)
        assertEquals("router-incarnation", settled.messagesByPeer["peer"]!!.single().incarnationId)
        assertTrue(settled.pending.isEmpty())
    }

    @Test
    fun rejectedHttpResultKeepsMessageAndMarksFailed() {
        val pending = enqueueOutboundDelivery(OutboundReducerState(), operation("message-1", scope))
        val settled = reduceOutboundDelivery(
            pending,
            OutboundDeliveryEvent.Rejected("message-1", "message-1", scope, "Message was rejected."),
        )

        assertEquals(OutboundDeliveryStatus.FAILED, settled.messagesByPeer["peer"]!!.single().deliveryStatus)
        assertEquals("Message was rejected.", settled.error)
        assertTrue(settled.pending.isEmpty())
    }

    @Test
    fun timeoutAndTransportFailureAreUncertainAndRetainTheBubble() {
        val operation = operation("message-1", scope, deadline = 100L)
        val pending = enqueueOutboundDelivery(OutboundReducerState(), operation)
        val beforeDeadline = reduceOutboundDelivery(pending, OutboundDeliveryEvent.Timeout("message-1", scope, 99L))
        val timedOut = reduceOutboundDelivery(beforeDeadline, OutboundDeliveryEvent.Timeout("message-1", scope, 100L))
        val transportFailure = reduceOutboundDelivery(
            enqueueOutboundDelivery(OutboundReducerState(), operation("message-2", scope)),
            OutboundDeliveryEvent.Uncertain("message-2", "message-2", scope, "Message delivery was not confirmed."),
        )

        assertEquals(OutboundDeliveryStatus.PENDING, beforeDeadline.messagesByPeer["peer"]!!.single().deliveryStatus)
        assertEquals(OutboundDeliveryStatus.UNCERTAIN, timedOut.messagesByPeer["peer"]!!.single().deliveryStatus)
        assertEquals(OutboundDeliveryStatus.UNCERTAIN, transportFailure.messagesByPeer["peer"]!!.single().deliveryStatus)
    }

    @Test
    fun lateHttpSuccessCannotClearDraftAfterThePendingOperationWasRejected() {
        val operation = operation("message-1", scope)
        val pending = enqueueOutboundDelivery(OutboundReducerState(), operation)
        val staleSuccess = OutboundDeliveryEvent.Accepted(
            "message-1",
            "message-1",
            scope.copy(connectionGeneration = scope.connectionGeneration + 1),
            operation.message.copy(incarnationId = "router-incarnation"),
        )

        assertFalse(acceptedOutboundEventApplied(pending, staleSuccess))
        assertEquals(pending, reduceOutboundDelivery(pending, staleSuccess))
    }

    @Test
    fun matchingHistoryEchoSettlesBeforeHttpAndReorderedHttpIsIgnored() {
        val operation = operation("message-1", scope)
        val pending = enqueueOutboundDelivery(OutboundReducerState(), operation)
        val echo = operation.message.copy(incarnationId = "router-incarnation")
        val historySettled = reduceOutboundDelivery(pending, OutboundDeliveryEvent.HistoryEcho(scope, echo))
        val lateHttp = reduceOutboundDelivery(
            historySettled,
            OutboundDeliveryEvent.Accepted("message-1", "message-1", scope, echo),
        )

        assertEquals(OutboundDeliveryStatus.SENT, historySettled.messagesByPeer["peer"]!!.single().deliveryStatus)
        assertEquals(historySettled, lateHttp)
    }

    @Test
    fun unresolvedTargetAtSendMatchesResolvedHistoryEchoAndProvidesExactDraftData() {
        val sentImages = listOf(PendingImage("sent.jpg", byteArrayOf(1, 2, 3)))
        val attachments = sentImages.map { ImageReference("", "local", "image/jpeg", it.bytes.size, it.name) }
        val operation = operation("message-1", scope.copy(targetInstanceId = null)).copy(
            requestAttachments = attachments,
            sentImages = sentImages,
        )
        val pending = enqueueOutboundDelivery(OutboundReducerState(), operation)
        val echoEvent = OutboundDeliveryEvent.HistoryEcho(
            scope.copy(targetInstanceId = "instance-a"),
            operation.message.copy(incarnationId = "router-incarnation", attachments = attachments),
        )

        assertEquals("body-message-1", outboundDraftClearText(pending, echoEvent))
        assertTrue(outboundDraftClearImages(pending, echoEvent) === sentImages)

        val settled = reduceOutboundDelivery(pending, echoEvent)
        assertEquals(OutboundDeliveryStatus.SENT, settled.messagesByPeer["peer"]!!.single().deliveryStatus)
        assertTrue(settled.pending.isEmpty())
    }

    @Test
    fun nonNullTargetInstanceMismatchKeepsPendingAndDraftData() {
        val sentImages = listOf(PendingImage("sent.jpg", byteArrayOf(1, 2, 3)))
        val attachments = sentImages.map { ImageReference("", "local", "image/jpeg", it.bytes.size, it.name) }
        val operation = operation("message-1", scope).copy(
            requestAttachments = attachments,
            sentImages = sentImages,
        )
        val pending = enqueueOutboundDelivery(OutboundReducerState(), operation)
        val mismatchedEcho = OutboundDeliveryEvent.HistoryEcho(
            scope.copy(targetInstanceId = "instance-b"),
            operation.message.copy(incarnationId = "router-incarnation", attachments = attachments),
        )
        val unresolvedEcho = mismatchedEcho.copy(scope = scope.copy(targetInstanceId = null))

        listOf(mismatchedEcho, unresolvedEcho).forEach { staleEcho ->
            assertEquals(null, outboundDraftClearText(pending, staleEcho))
            assertEquals(null, outboundDraftClearImages(pending, staleEcho))
            assertEquals(pending, reduceOutboundDelivery(pending, staleEcho))
        }
    }

    @Test
    fun matchingHistoryEchoProvidesDraftTextBeforeLateHttpResult() {
        val operation = operation("message-1", scope)
        val pending = enqueueOutboundDelivery(OutboundReducerState(), operation)
        val echoEvent = OutboundDeliveryEvent.HistoryEcho(
            scope,
            operation.message.copy(incarnationId = "router-incarnation"),
        )

        assertEquals("body-message-1", outboundDraftClearText(pending, echoEvent))

        val settled = reduceOutboundDelivery(pending, echoEvent)
        val lateHttp = OutboundDeliveryEvent.Accepted(
            "message-1",
            "message-1",
            scope,
            operation.message.copy(incarnationId = "router-incarnation"),
        )
        assertEquals(null, outboundDraftClearText(settled, lateHttp))
    }

    @Test
    fun matchingHistoryEchoProvidesExactSentImagesBeforeLateHttpResult() {
        val sentImages = listOf(PendingImage("sent.jpg", byteArrayOf(1, 2, 3)))
        val attachments = sentImages.map { ImageReference("", "local", "image/jpeg", it.bytes.size, it.name) }
        val operation = operation("message-1", scope).copy(
            requestAttachments = attachments,
            sentImages = sentImages,
        )
        val pending = enqueueOutboundDelivery(OutboundReducerState(), operation)
        val echo = operation.message.copy(
            incarnationId = "router-incarnation",
            attachments = attachments,
        )
        val echoEvent = OutboundDeliveryEvent.HistoryEcho(scope, echo)

        assertTrue(outboundDraftClearImages(pending, echoEvent) === sentImages)

        val settled = reduceOutboundDelivery(pending, echoEvent)
        val lateHttp = OutboundDeliveryEvent.Accepted("message-1", "message-1", scope, echo)
        assertEquals(null, outboundDraftClearImages(settled, lateHttp))
    }

    @Test
    fun staleRejectedAndUncertainResultsDoNotProvideDraftTextOrImagesToClear() {
        val sentImages = listOf(PendingImage("sent.jpg", byteArrayOf(1, 2, 3)))
        val operation = operation("message-1", scope).copy(sentImages = sentImages)
        val pending = enqueueOutboundDelivery(OutboundReducerState(), operation)
        val stale = OutboundDeliveryEvent.Accepted(
            "message-1",
            "message-1",
            scope.copy(connectionGeneration = scope.connectionGeneration + 1),
            operation.message.copy(incarnationId = "router-incarnation"),
        )
        val rejected = OutboundDeliveryEvent.Rejected("message-1", "message-1", scope, "rejected")
        val uncertain = OutboundDeliveryEvent.Uncertain("message-1", "message-1", scope, "uncertain")

        assertEquals(null, outboundDraftClearText(pending, stale))
        assertEquals(null, outboundDraftClearText(pending, rejected))
        assertEquals(null, outboundDraftClearText(pending, uncertain))
        assertEquals(null, outboundDraftClearImages(pending, stale))
        assertEquals(null, outboundDraftClearImages(pending, rejected))
        assertEquals(null, outboundDraftClearImages(pending, uncertain))
    }

    @Test
    fun duplicateEchoDoesNotDuplicateOrchangeSentMessage() {
        val operation = operation("message-1", scope)
        val echo = operation.message.copy(incarnationId = "router-incarnation")
        val once = reduceOutboundDelivery(
            enqueueOutboundDelivery(OutboundReducerState(), operation),
            OutboundDeliveryEvent.HistoryEcho(scope, echo),
        )
        val twice = reduceOutboundDelivery(once, OutboundDeliveryEvent.HistoryEcho(scope, echo))

        assertEquals(1, twice.messagesByPeer["peer"]!!.size)
        assertEquals(once, twice)
    }

    @Test
    fun staleRouterGenerationOrTargetInstanceCannotSettleOperation() {
        val operation = operation("message-1", scope)
        val pending = enqueueOutboundDelivery(OutboundReducerState(), operation)
        val staleRouter = scope.copy(router = "router-b")
        val staleGeneration = scope.copy(connectionGeneration = 8)
        val staleInstance = scope.copy(targetInstanceId = "instance-b")

        listOf(staleRouter, staleGeneration, staleInstance).forEach { staleScope ->
            val result = reduceOutboundDelivery(
                pending,
                OutboundDeliveryEvent.Accepted("message-1", "message-1", staleScope, operation.message),
            )
            assertEquals(pending, result)
        }
        assertEquals(OutboundDeliveryStatus.PENDING, pending.messagesByPeer["peer"]!!.single().deliveryStatus)
    }

    @Test
    fun identityChangePersistsSettledRowsUnderTheOldOwnerScopeBeforeClearing() {
        val pending = enqueueOutboundDelivery(OutboundReducerState(), operation("message-1", scope))
        val persisted = mutableListOf<List<String>>()
        val result = settleOutboundForIdentityChange(
            pending,
            oldRouter = "router-old",
            oldOwner = "old-owner",
            oldOwnerInstance = "old-instance",
        ) { router, owner, instance, peer, messages ->
            persisted += listOf(router, owner, instance, peer, messages.single().id, messages.single().deliveryStatus.name)
            true
        }

        assertTrue(result.persisted)
        assertTrue(result.state.messagesByPeer.isEmpty())
        assertTrue(result.state.pending.isEmpty())
        assertEquals(listOf("router-old", "old-owner", "old-instance", "peer", "message-1", "UNCERTAIN"), persisted.single())
    }

    @Test
    fun identityChangeKeepsOldScopedStateWhenPersistenceFails() {
        val pending = enqueueOutboundDelivery(OutboundReducerState(), operation("message-1", scope))

        val result = settleOutboundForIdentityChange(
            pending,
            oldRouter = "router-old",
            oldOwner = "old-owner",
            oldOwnerInstance = "old-instance",
        ) { _, _, _, _, _ -> false }

        assertFalse(result.persisted)
        assertEquals(pending, result.state)
    }

    @Test
    fun connectionChangeSettlesWithoutSendingAgain() {
        val pending = enqueueOutboundDelivery(OutboundReducerState(), operation("message-1", scope))
        val uncertain = reduceOutboundDelivery(pending, OutboundDeliveryEvent.ConnectionChanged)
        val duplicateEnqueue = enqueueOutboundDelivery(uncertain, operation("message-1", scope.copy(connectionGeneration = 8)))

        assertEquals(OutboundDeliveryStatus.UNCERTAIN, uncertain.messagesByPeer["peer"]!!.single().deliveryStatus)
        assertTrue(uncertain.pending.isEmpty())
        assertEquals(uncertain, duplicateEnqueue)
    }

    @Test
    fun matchingHistoryEchoCanConfirmUncertainButWrongEchoCannot() {
        val operation = operation("message-1", scope)
        val uncertain = reduceOutboundDelivery(
            enqueueOutboundDelivery(OutboundReducerState(), operation),
            OutboundDeliveryEvent.Uncertain("message-1", "message-1", scope, "Delivery unknown."),
        )
        val wrong = operation.message.copy(body = "different", incarnationId = "new")
        val unchanged = reduceOutboundDelivery(uncertain, OutboundDeliveryEvent.HistoryEcho(scope, wrong))
        val confirmed = reduceOutboundDelivery(
            unchanged,
            OutboundDeliveryEvent.HistoryEcho(scope, operation.message.copy(incarnationId = "new")),
        )

        assertEquals(uncertain, unchanged)
        assertEquals(OutboundDeliveryStatus.SENT, confirmed.messagesByPeer["peer"]!!.single().deliveryStatus)
    }

    @Test
    fun serializedConcurrentReducerUpdatesKeepBothMessages() {
        val lock = ConversationStateLock()
        var state = OutboundReducerState()
        val ready = CountDownLatch(2)
        val start = CountDownLatch(1)
        val workers = listOf("message-a", "message-b").map { id ->
            Thread {
                ready.countDown()
                start.await()
                lock.withLock {
                    val before = state
                    Thread.sleep(10)
                    state = enqueueOutboundDelivery(before, operation(id, scope))
                }
            }.also(Thread::start)
        }
        assertTrue(ready.await(1, java.util.concurrent.TimeUnit.SECONDS))
        start.countDown()
        workers.forEach { it.join() }

        val messages = state.messagesByPeer["peer"].orEmpty()
        assertEquals(setOf("message-a", "message-b"), messages.map(ChatMessage::id).toSet())
        assertEquals(setOf("message-a", "message-b"), state.pending.keys)
    }

    @Test
    fun pendingBoundSettlesOldestDeterministically() {
        var state = OutboundReducerState()
        (1..MAX_PENDING_OUTBOUND_OPERATIONS).forEach { number ->
            state = enqueueOutboundDelivery(state, operation("message-$number", scope))
        }
        state = enqueueOutboundDelivery(state, operation("message-new", scope))

        assertEquals(MAX_PENDING_OUTBOUND_OPERATIONS, state.pending.size)
        assertFalse(state.pending.containsKey("message-1"))
        assertEquals(OutboundDeliveryStatus.UNCERTAIN, state.messagesByPeer["peer"]!!.first { it.id == "message-1" }.deliveryStatus)
        assertTrue(state.pending.containsKey("message-new"))
    }

    private fun operation(id: String, operationScope: OutboundScope, deadline: Long = 30_000L) = OutboundOperation(
        localId = id,
        requestId = id,
        frameId = null,
        scope = operationScope,
        message = ChatMessage(
            id = id,
            incarnationId = "",
            sequence = 0,
            sender = "android",
            recipient = operationScope.peer,
            body = "body-$id",
            direction = Direction.OUTGOING,
            createdAt = testTimestamp(),
            deliveryStatus = OutboundDeliveryStatus.PENDING,
        ),
        requestBody = "body-$id",
        deadlineAtMs = deadline,
    )

    private fun testTimestamp(offsetMs: Long = 0): String =
        java.time.format.DateTimeFormatter.ofPattern("uuuu-MM-dd'T'HH:mm:ss.SSS'Z'")
            .withZone(java.time.ZoneOffset.UTC)
            .format(java.time.Instant.ofEpochMilli(System.currentTimeMillis() + offsetMs))

}
