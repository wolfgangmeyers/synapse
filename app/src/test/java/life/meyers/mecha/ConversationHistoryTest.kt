package life.meyers.mecha

import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import java.util.concurrent.TimeUnit
import okhttp3.OkHttpClient
import okhttp3.Request
import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.Instant
import java.util.concurrent.CountDownLatch

class ConversationHistoryTest {
    @Test
    fun routerHistoryUrlUsesTheCanonicalBaseUrlAndEncodesBothParticipants() {
        val url = conversationHistoryUrl(
            "http://127.0.0.1:18765",
            "android-owner",
            "peer/with?reserved",
        )

        assertEquals(
            "http://127.0.0.1:18765/v1/conversations?owner=android-owner&peer=peer%2Fwith%3Freserved",
            url.toString(),
        )
    }

    @Test
    fun routerEmptyHistoryNullMessagesIsAcceptedAsAnEmptyConversation() {
        val empty = parseHistoryResponse(
            "{\"owner\":\"android-owner\",\"peer\":\"peer\",\"messages\":null,\"high_water_sequence\":0}",
            "android-owner",
            "peer",
        )

        assertNotNull(empty)
        assertEquals(0, empty?.messages?.length())
    }

    @Test
    fun malformedHistoryResponsesAreRejectedBeforeTheyCanBeApplied() {
        val valid = parseHistoryResponse("{\"owner\":\"android-owner\",\"peer\":\"peer\",\"messages\":[],\"high_water_sequence\":0}", "android-owner", "peer")
        assertNotNull(valid)
        assertEquals(0L, valid?.highWaterSequence)
        assertEquals(0, valid?.messages?.length())

        listOf(
            "{}",
            "{\"owner\":\"android-owner\",\"peer\":\"peer\",\"messages\":[],\"high_water_sequence\":\"0\"}",
            "{\"owner\":\"android-owner\",\"peer\":\"peer\",\"messages\":{},\"high_water_sequence\":0}",
            "{\"owner\":\"android-owner\",\"peer\":\"peer\",\"messages\":[null],\"high_water_sequence\":0}",
            "{\"owner\":\"android-owner\",\"peer\":\"peer\",\"messages\":[{}],\"high_water_sequence\":0}",
            "{\"owner\":\"android-owner\",\"peer\":\"peer\",\"messages\":[],\"high_water_sequence\":-1}",
            "{\"owner\":\"android-owner\",\"peer\":\"peer\",\"messages\":[],\"high_water_sequence\":1.5}",
        ).forEach { body -> assertNull(parseHistoryResponse(body)) }
    }

    @Test
    fun historyParserRequiresRequestEchoAndTheCompleteProtocolMessageSchema() {
        val valid = validHistoryResponse()
        val parsed = parseHistoryResponse(valid.toString(), "android-owner", "peer")
        assertNotNull(parsed)
        val parsedMessage = parsed?.messages?.getJSONObject(0)
        assertEquals("message-1", parsedMessage?.getString("id"))
        assertEquals("body", parsedMessage?.getString("body"))
        assertEquals("peer", parsedMessage?.getString("from"))
        assertEquals("android-owner", parsedMessage?.getString("to"))

        val wrongOwner = JSONObject(valid.toString()).put("owner", "other-owner")
        val wrongPeer = JSONObject(valid.toString()).put("peer", "other-peer")
        val unknownRoot = JSONObject(valid.toString()).put("extra", true)
        assertNull(parseHistoryResponse(wrongOwner.toString(), "android-owner", "peer"))
        assertNull(parseHistoryResponse(wrongPeer.toString(), "android-owner", "peer"))
        assertNull(parseHistoryResponse(unknownRoot.toString(), "android-owner", "peer"))

        val withNextBefore = JSONObject(valid.toString()).put("next_before", 42L)
        assertNotNull(parseHistoryResponse(withNextBefore.toString(), "android-owner", "peer"))
        val withNullNextBefore = JSONObject(valid.toString()).put("next_before", JSONObject.NULL)
        assertNotNull(parseHistoryResponse(withNullNextBefore.toString(), "android-owner", "peer"))

        listOf("sequence", "id", "message_incarnation_id", "from", "to", "body", "created_at", "recorded_at", "attachments").forEach { key ->
            val message = valid.getJSONArray("messages").getJSONObject(0)
            message.remove(key)
            assertNull(parseHistoryResponse(valid.toString(), "android-owner", "peer"))
            message.put(key, validMessage().get(key))
        }

        val invalidValues = listOf(
            "sequence" to 0,
            "sequence" to 1.5,
            "id" to "bad id",
            "message_incarnation_id" to "not-a-v4-uuid",
            "to" to "unsafe@peer",
            "body" to 7,
            "created_at" to "2026-08-29T00:00:00Z",
            "recorded_at" to "2026-08-29T00:00:00.000+00:00",
            "attachments" to JSONObject(),
            "source" to JSONObject().put("kind", "unsupported"),
        )
        invalidValues.forEach { (key, value) ->
            val message = validMessage().put(key, value)
            val response = validHistoryResponse(message)
            assertNull("$key=$value", parseHistoryResponse(response.toString(), "android-owner", "peer"))
        }

        val tooMany = JSONArray().apply { repeat(MAX_HISTORY_MESSAGES + 1) { put(validMessage()) } }
        assertNull(parseHistoryResponse(validHistoryResponse(messages = tooMany).toString(), "android-owner", "peer"))
        assertNull(parseHistoryResponse(validHistoryResponse(validMessage().put("body", "x".repeat(65_537))).toString(), "android-owner", "peer"))
        assertNull(parseHistoryResponse(validHistoryResponse(validMessage().put("attachments", JSONArray().put(validAttachment()).put(validAttachment()).put(validAttachment()).put(validAttachment()))).toString(), "android-owner", "peer"))
        assertNull(parseHistoryResponse(validHistoryResponse(validMessage().put("body", "\uD800")).toString(), "android-owner", "peer"))
    }

    @Test
    fun boundedHistoryResponseReadingRejectsBodiesBeyondTheTransportLimit() {
        val server = MockWebServer()
        server.start()
        try {
            server.enqueue(MockResponse().setBody("12345"))
            historyHttpClient(OkHttpClient()).newCall(Request.Builder().url(server.url("/history")).build()).execute().use { response ->
                assertNull(readHistoryResponseBody(response.body, 4))
            }
        } finally {
            server.shutdown()
        }
    }

    @Test
    fun historyHttpClientDisablesRetriesRedirectsAndHasABoundedCallTimeout() {
        val client = historyHttpClient(OkHttpClient())
        assertFalse(client.followRedirects)
        assertFalse(client.followSslRedirects)
        assertFalse(client.retryOnConnectionFailure)
        assertEquals(HISTORY_REQUEST_TIMEOUT_MS, client.callTimeoutMillis.toLong())
    }

    @Test
    fun failedLivePersistenceKeepsTheAdditiveMessageForHydrationRecovery() {
        val now = Instant.parse("2026-08-29T00:00:00Z").toEpochMilli()
        val incoming = message("live", createdAt = Instant.ofEpochMilli(now).toString())
        val result = appendLiveConversationMessage(
            emptyList(),
            incoming,
            now,
            persist = { HistoryPersistenceProof(committed = false, retainedMessageKeys = setOf(incoming.key)) },
        )

        assertEquals(HistoryAppendStatus.PERSISTENCE_FAILED, result.status)
        assertTrue(result.needsHydrationRecovery)
        assertEquals(listOf("live"), result.messages.map(ChatMessage::id))
    }

    @Test
    fun duplicateConversationMessageNotifiesOnlyWhenItBecomesDurable() {
        val now = Instant.parse("2026-08-29T00:00:00Z").toEpochMilli()
        val incoming = message("duplicate", incarnation = "550e8400-e29b-41d4-a716-446655440000", sequence = 1)
        val first = appendLiveConversationMessage(
            emptyList(),
            incoming,
            now,
            persist = { HistoryPersistenceProof(committed = true, retainedMessageKeys = setOf(incoming.key)) },
        )
        val duplicate = appendLiveConversationMessage(
            first.messages,
            incoming,
            now,
            persist = { HistoryPersistenceProof(committed = true, retainedMessageKeys = setOf(incoming.key)) },
            alreadyPersisted = true,
        )
        val redeliveredBeforeCommit = appendLiveConversationMessage(
            first.messages,
            incoming,
            now,
            persist = { HistoryPersistenceProof(committed = true, retainedMessageKeys = setOf(incoming.key)) },
            alreadyPersisted = false,
        )

        assertTrue(first.shouldNotify)
        assertFalse(duplicate.shouldNotify)
        assertTrue(redeliveredBeforeCommit.shouldNotify)
        assertEquals(1, duplicate.messages.size)
    }

    @Test
    fun historyRecoveryUsesExponentialDelaysAndStopsAtItsBudget() {
        assertEquals(listOf(1_000L, 2_000L, 4_000L, 8_000L, 16_000L), (0 until HISTORY_RECOVERY_MAX_ATTEMPTS).map(::historyRecoveryDelayMs))
        assertFalse(hasHistoryRecoveryBudget(HISTORY_RECOVERY_MAX_ATTEMPTS))
        assertTrue(hasHistoryRecoveryBudget(HISTORY_RECOVERY_MAX_ATTEMPTS - 1))
        assertEquals(HISTORY_RECOVERY_MAX_DELAY_MS, historyRecoveryDelayMs(10_000))
    }

    @Test
    fun terminalHistoryRecoveryStateOffersAManualRetry() {
        val state = historyErrorState(HistoryFailure.PERSISTENCE_EXHAUSTED)

        assertEquals(HistoryStatus.ERROR, state.status)
        assertTrue(state.retryable)
        assertEquals("History could not be saved after several attempts. Tap Retry to try again.", state.message)
    }

    @Test
    fun malformedHistoryResponseExposesOnlyARequestToRetry() {
        val state = historyErrorState(HistoryFailure.INVALID_RESPONSE)

        assertEquals(HistoryStatus.ERROR, state.status)
        assertTrue(state.retryable)
        assertEquals("History could not be loaded. The router returned an invalid response.", state.message)
        assertFalse(state.message.orEmpty().contains("owner"))
        assertFalse(state.message.orEmpty().contains("peer"))
    }

    @Test
    fun historyRequestsDoNotFollowCrossOriginRedirects() {
        val origin = MockWebServer()
        val destination = MockWebServer()
        origin.start()
        destination.start()
        try {
            origin.enqueue(MockResponse()
                .setResponseCode(307)
                .setHeader("Location", destination.url("/v1/conversations?owner=leaked&peer=leaked")))
            val requestUrl = conversationHistoryUrl(origin.url("/").toString().removeSuffix("/"), "owner", "peer")
            historyHttpClient(OkHttpClient()).newCall(Request.Builder().url(requestUrl).build()).execute().use { response ->
                assertEquals(307, response.code)
            }
            assertNotNull(origin.takeRequest(1, TimeUnit.SECONDS))
            assertNull(destination.takeRequest(200, TimeUnit.MILLISECONDS))
        } finally {
            origin.shutdown()
            destination.shutdown()
        }
    }

    @Test
    fun non2xxHistoryResponseExposesRetryableErrorWithoutRouterDetails() {
        val state = historyErrorState(HistoryFailure.HTTP, 503)

        assertEquals(HistoryStatus.ERROR, state.status)
        assertTrue(state.retryable)
        assertEquals("History could not be loaded (503).", state.message)
        assertFalse(state.message.orEmpty().contains("127.0.0.1"))
    }

    @Test
    fun historyExceptionExposesRetryableErrorWithoutExceptionDetails() {
        val state = historyErrorState(HistoryFailure.EXCEPTION)

        assertEquals(HistoryStatus.ERROR, state.status)
        assertTrue(state.retryable)
        assertEquals("History could not be loaded. Check the connection and try again.", state.message)
        assertFalse(state.message.orEmpty().contains("secret"))
    }

    @Test
    fun historyPersistenceFailureExposesRetryableError() {
        val state = historyErrorState(HistoryFailure.PERSISTENCE)

        assertEquals(HistoryStatus.ERROR, state.status)
        assertTrue(state.retryable)
        assertEquals("History was loaded but could not be saved on this device. Try again.", state.message)
    }

    @Test
    fun idleAndLoadingHistoryStatesDoNotOfferRetry() {
        assertFalse(HistoryUiState().retryable)
        assertFalse(HistoryUiState(HistoryStatus.LOADING).retryable)
    }

    @Test
    fun staleHydrationCallerCannotPublishLoadingAfterGenerationChangesWhileWaitingForTheLock() {
        val lock = ConversationStateLock()
        val callerStarted = CountDownLatch(1)
        val callerDone = CountDownLatch(1)
        var generation = 1
        var loadingPublished = false
        lock.withLock {
            val staleCaller = Thread {
                callerStarted.countDown()
                lock.withLock {
                    if (isCurrentHistoryHydrationStart(
                            requestedPeer = "peer",
                            requestedGeneration = 1,
                            currentRouter = "router-a",
                            currentOwnerAgentId = "android",
                            currentOwnerInstanceId = "instance-1",
                            currentSelectedPeer = "peer",
                            currentConnectionGeneration = generation,
                        )
                    ) loadingPublished = true
                }
                callerDone.countDown()
            }
            staleCaller.start()
            assertTrue(callerStarted.await(1, TimeUnit.SECONDS))
            generation = 2
            staleCaller.join(1_000)
            assertTrue(staleCaller.isAlive)
        }

        // The caller acquires the serialized lock only after the generation changed.
        assertTrue(callerDone.await(1, TimeUnit.SECONDS))
        assertFalse(loadingPublished)
    }

    @Test
    fun snapshotRemovesPrunedRowsAndKeepsLiveMessagesAfterItsHighWaterMark() {
        val stale = message("stale", "stale-incarnation", 1)
        val retained = message("retained", "retained-incarnation", 2)
        val live = message("live", "live-incarnation", 5)
        val legacyUnsequenced = message("legacy", "", 0)

        val reconciled = reconcileConversationMessages(
            existing = listOf(stale, live, legacyUnsequenced),
            snapshot = listOf(retained),
            highWaterSequence = 4,
        )

        assertEquals(listOf("retained", "live", "legacy"), reconciled.map(ChatMessage::id))
    }

    @Test
    fun hydrationMergesCurrentMemoryAndRecoveryStateBeforeApplyingAnOlderSnapshot() {
        val persisted = message("persisted", sequence = 1)
        val unsaved = message("unsaved", sequence = 0)
        val uncertain = message(
            "uncertain",
            sequence = 0,
            direction = Direction.OUTGOING,
            deliveryStatus = OutboundDeliveryStatus.UNCERTAIN,
        ).copy(sender = "android", recipient = "peer")

        val merged = mergeHydrationConversationMessages(
            persisted = listOf(persisted),
            inMemory = listOf(unsaved),
            recovery = listOf(uncertain),
        )
        val reconciled = reconcileConversationMessages(
            existing = merged,
            snapshot = listOf(message("newer", sequence = 2)),
            highWaterSequence = 2,
        )

        assertEquals(setOf("unsaved", "uncertain", "newer"), reconciled.map(ChatMessage::id).toSet())
        assertEquals(OutboundDeliveryStatus.UNCERTAIN, reconciled.single { it.id == "uncertain" }.deliveryStatus)
    }

    @Test
    fun historyRetainsExactlyTheTwoHundredNewestMessages() {
        val now = Instant.parse("2026-08-29T00:00:00Z").toEpochMilli()
        val retained = pruneConversationHistory(
            (1..MAX_HISTORY_MESSAGES).map { number -> message("message-$number", createdAt = Instant.ofEpochMilli(now - number).toString()) },
            now,
        )

        assertEquals(MAX_HISTORY_MESSAGES, retained.size)
        assertEquals("message-200", retained.first().id)
        assertEquals("message-1", retained.last().id)
    }

    @Test
    fun historyEvictsTheOldestMessageWhenTheCapIsExceeded() {
        val now = Instant.parse("2026-08-29T00:00:00Z").toEpochMilli()
        val retained = pruneConversationHistory(
            (0..MAX_HISTORY_MESSAGES).map { number -> message("message-$number", createdAt = Instant.ofEpochMilli(now - number).toString()) },
            now,
        )

        assertEquals(MAX_HISTORY_MESSAGES, retained.size)
        assertTrue(retained.none { it.id == "message-200" })
        assertEquals("message-0", retained.last().id)
    }

    @Test
    fun historyKeepsTheAgeBoundaryAndDropsOneMillisecondBeforeIt() {
        val now = Instant.parse("2026-08-29T00:00:00Z").toEpochMilli()
        val cutoff = now - HISTORY_RETENTION_MS
        val retained = pruneConversationHistory(
            listOf(
                message("boundary", createdAt = Instant.ofEpochMilli(cutoff).toString()),
                message("expired", createdAt = Instant.ofEpochMilli(cutoff - 1).toString()),
            ),
            now,
        )

        assertEquals(listOf("boundary"), retained.map(ChatMessage::id))
    }

    @Test
    fun invalidAndMissingTimestampsUseNowLikeTheWebUi() {
        val now = Instant.parse("2026-08-29T00:00:00Z").toEpochMilli()
        val retained = pruneConversationHistory(
            listOf(
                message("invalid", createdAt = "not-a-timestamp"),
                message("missing", createdAt = ""),
            ),
            now,
        )

        assertEquals(listOf("invalid", "missing"), retained.map(ChatMessage::id))
    }

    @Test
    fun reconnectHydrationKeepsUnsequencedOptimisticMessage() {
        val pending = message(
            "pending",
            sequence = 0,
            createdAt = testTimestamp(60_000),
            direction = Direction.OUTGOING,
            deliveryStatus = OutboundDeliveryStatus.PENDING,
        )
        val hydrated = reconcileConversationMessages(
            existing = listOf(pending, message("old", sequence = 1)),
            snapshot = listOf(message("canonical", sequence = 2)),
            highWaterSequence = 2,
        )

        assertEquals(listOf("canonical", "pending"), hydrated.map(ChatMessage::id))
    }

    @Test
    fun pendingOptimisticMessageIsNotEvictedByAgeOrTheCap() {
        val now = Instant.parse("2026-08-29T00:00:00Z").toEpochMilli()
        val pending = message(
            "pending",
            createdAt = Instant.ofEpochMilli(now - HISTORY_RETENTION_MS - 1).toString(),
            direction = Direction.OUTGOING,
            deliveryStatus = OutboundDeliveryStatus.PENDING,
        )
        val retained = pruneConversationHistory(
            listOf(pending) + (1..MAX_HISTORY_MESSAGES).map { number ->
                message("settled-$number", createdAt = Instant.ofEpochMilli(now - number).toString())
            },
            now,
        )

        assertEquals(MAX_HISTORY_MESSAGES, retained.size)
        assertTrue(retained.any { it.id == "pending" })
        assertTrue(retained.none { it.id == "settled-200" })
    }

    @Test
    fun historyScopeSeparatesRouterPeerAndInstallInstance() {
        val base = historyPreferenceKey("router-a:8765", "agent", "instance-a", "peer")

        assertEquals(base, historyPreferenceKey("router-a:8765", "agent", "instance-a", "peer"))
        assertTrue(base != historyPreferenceKey("router-b:8765", "agent", "instance-a", "peer"))
        assertTrue(base != historyPreferenceKey("router-a:8765", "agent", "instance-b", "peer"))
        assertTrue(base != historyPreferenceKey("router-a:8765", "agent", "instance-a", "other-peer"))
    }

    @Test
    fun snapshotAndLiveCopiesOfOneIncarnationCollapse() {
        val live = message("same", "same-incarnation", 3)
        val snapshot = live.copy(body = "canonical")

        val reconciled = reconcileConversationMessages(listOf(live), listOf(snapshot), 3)

        assertEquals(1, reconciled.size)
        assertEquals("canonical", reconciled.single().body)
    }

    private fun validHistoryResponse(
        message: JSONObject = validMessage(),
        messages: JSONArray = JSONArray().put(message),
    ): JSONObject = JSONObject()
        .put("owner", "android-owner")
        .put("peer", "peer")
        .put("high_water_sequence", 1)
        .put("messages", messages)

    private fun validMessage(): JSONObject = JSONObject()
        .put("sequence", 1)
        .put("id", "message-1")
        .put("message_incarnation_id", "550e8400-e29b-41d4-a716-446655440000")
        .put("from", "peer")
        .put("to", "android-owner")
        .put("body", "body")
        .put("created_at", "2026-08-29T00:00:00.000Z")
        .put("recorded_at", "2026-08-29T00:00:00.000Z")
        .put("attachments", JSONArray())
        .put("source", JSONObject().put("kind", "agent"))

    private fun validAttachment(): JSONObject = JSONObject()
        .put("id", "attachment-1")
        .put("url", "/v1/attachments/attachment-1")
        .put("mime_type", "image/jpeg")
        .put("size_bytes", 10)

    private fun message(
        id: String,
        incarnation: String = "",
        sequence: Long = 0,
        createdAt: String = testTimestamp(),
        direction: Direction = Direction.INCOMING,
        deliveryStatus: OutboundDeliveryStatus = OutboundDeliveryStatus.NONE,
    ) = ChatMessage(
        id = id,
        incarnationId = incarnation,
        sequence = sequence,
        sender = "peer",
        body = id,
        direction = direction,
        createdAt = createdAt,
        recipient = if (direction == Direction.OUTGOING) "peer" else "",
        deliveryStatus = deliveryStatus,
    )

    private fun testTimestamp(offsetMs: Long = 0): String =
        java.time.format.DateTimeFormatter.ofPattern("uuuu-MM-dd'T'HH:mm:ss.SSS'Z'")
            .withZone(java.time.ZoneOffset.UTC)
            .format(java.time.Instant.ofEpochMilli(System.currentTimeMillis() + offsetMs))

    @Test
    fun conversationScopeAllowsOperatorFallbackMessages() {
        // Postel's law: router falls back to wolfgang <-> peer conversation when new android client has no history
        assertTrue(isMessageInConversationScope("wolfgang", "supervisor", "android-7a28f24c", "supervisor"))
        assertTrue(isMessageInConversationScope("supervisor", "wolfgang", "android-7a28f24c", "supervisor"))
        assertTrue(isMessageInConversationScope("android-7a28f24c", "supervisor", "android-7a28f24c", "supervisor"))
        assertTrue(isMessageInConversationScope("supervisor", "android-7a28f24c", "android-7a28f24c", "supervisor"))

        // Rejects messages not involving the peer or self-messages
        assertFalse(isMessageInConversationScope("wolfgang", "other-agent", "android-7a28f24c", "supervisor"))
        assertFalse(isMessageInConversationScope("supervisor", "supervisor", "android-7a28f24c", "supervisor"))
    }

    @Test
    fun conversationPeerForMessageHandlesOperatorMessages() {
        val incomingFromPeerToWolfgang = message("1").copy(sender = "supervisor", recipient = "wolfgang")
        val outgoingFromWolfgangToPeer = message("2").copy(sender = "wolfgang", recipient = "supervisor")
        val incomingFromPeerToAndroid = message("3").copy(sender = "supervisor", recipient = "android-7a28f24c")

        assertEquals("supervisor", conversationPeerForMessage(incomingFromPeerToWolfgang, "android-7a28f24c"))
        assertEquals("supervisor", conversationPeerForMessage(outgoingFromWolfgangToPeer, "android-7a28f24c"))
        assertEquals("supervisor", conversationPeerForMessage(incomingFromPeerToAndroid, "android-7a28f24c"))
    }
    @Test
    fun validateLiveMessageFrameAcceptsRouterBroadcastConversationMessage() {
        val baseMessage = JSONObject()
            .put("id", "11111111-2222-3333-4444-555555555555")
            .put("from", "agent-one")
            .put("to", "wolfgang")
            .put("body", "Hello world from agent")
            .put("created_at", "2026-04-13T12:00:00.000Z")
            .put("sequence", 42)

        val frame = JSONObject()
            .put("type", "conversation_message")
            .put("message", baseMessage)

        val parsed = validateLiveMessageFrame(frame, LiveMessageFrameKind.CONVERSATION_MESSAGE)
        assertNotNull(parsed)
        assertEquals(42L, parsed?.sequence)
        assertEquals("Hello world from agent", parsed?.payload?.optString("body"))

        val withOptionalFields = JSONObject(frame.toString())
            .put("version", "v1")
            .put("frame_id", "frame-1234")

        val parsedWithOptions = validateLiveMessageFrame(withOptionalFields, LiveMessageFrameKind.CONVERSATION_MESSAGE)
        assertNotNull(parsedWithOptions)
        assertEquals(42L, parsedWithOptions?.sequence)
        assertEquals("Hello world from agent", parsedWithOptions?.payload?.optString("body"))
    }

}
