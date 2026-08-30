package life.meyers.mecha

import java.time.Instant
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class InboundDeliveryTest {
    @Test
    fun persistsAndDeduplicatesBeforeSendingReceipts() {
        val events = mutableListOf<String>()
        val receipts = mutableListOf<JSONObject>()
        var stored = emptyList<ChatMessage>()
        val frame = deliveryFrame("message-1")
        val settled = settleIncomingDelivery(
            frame = frame,
            agentId = "android",
            instanceId = "instance-1",
            leaseId = "lease-1",
            parseMessage = ::parse,
            persistMessage = { peer, message ->
                events += "persist:$peer"
                stored = mergeConversationMessages(stored, listOf(message))
                true
            },
            sendInjection = { events += "injection"; receipts += it },
            sendAck = { events += "ack"; receipts += it },
        )

        assertTrue(settled)
        assertEquals(listOf("persist:sender", "injection", "ack"), events)
        assertEquals(1, stored.size)
        assertEquals(2, receipts.size)
        receipts.forEach { receipt ->
            assertEquals("550e8400-e29b-41d4-a716-446655440000", receipt.getString("message_incarnation_id"))
            assertEquals(1, receipt.getJSONArray("delivery_items").length())
        }

        settleIncomingDelivery(
            frame = frame,
            agentId = "android",
            instanceId = "instance-1",
            leaseId = "lease-1",
            parseMessage = ::parse,
            persistMessage = { peer, message ->
                events += "persist:$peer"
                stored = mergeConversationMessages(stored, listOf(message))
                true
            },
            sendInjection = { events += "injection" },
            sendAck = { events += "ack" },
        )
        assertEquals(1, stored.size)
    }

    @Test
    fun legacyDeliveryWithoutAckCapabilityPersistsAndUsesLegacyReceipts() {
        val registration = parseRegisteredSession(
            JSONObject()
                .put("version", "v1")
                .put("frame_id", "registered-1")
                .put("type", "registered")
                .put("request_id", "request-1")
                .put("agent_id", "android")
                .put("instance_id", "instance-1")
                .put("lease_id", "lease-1")
                .put("expires_at", "2026-08-29T00:00:30.000Z")
                .put("recovered", false)
                .put("agent_role", "user_interaction"),
            "request-1",
            "android",
            "instance-1",
        )
        assertNotNull(registration)
        val correlatedAck = usesCorrelatedAck(registration?.ackConfirmation)
        assertFalse(correlatedAck)

        val events = mutableListOf<String>()
        var stored: ChatMessage? = null
        val receipts = mutableListOf<JSONObject>()
        val frame = deliveryFrame("legacy-message").apply { remove("message_incarnation_id") }
        assertFalse(validateLiveMessageFrame(frame, LiveMessageFrameKind.DELIVERY_MESSAGE) != null)

        val coordinator = ConversationDeliveryCoordinator(ConversationStateLock())
        coordinator.begin(DeliveryConnectionIdentity("router", "android", "instance-1", 1, ""))
        assertNull(coordinator.capture(1) { connection ->
            captureDeliveryForSettlement(
                frame = frame,
                connection = connection,
                parseMessage = { validated, _, _ -> parse(validated.payload) },
                targetInstanceId = { null },
                correlatedAck = correlatedAck,
            )
        })
        assertTrue(coordinator.register(1, "lease-1"))
        assertNotNull(coordinator.capture(1) { connection ->
            captureDeliveryForSettlement(
                frame = frame,
                connection = connection,
                parseMessage = { validated, _, _ -> parse(validated.payload) },
                targetInstanceId = { null },
                correlatedAck = correlatedAck,
            )
        })

        assertTrue(settleIncomingDelivery(
            frame = frame,
            agentId = "android",
            instanceId = "instance-1",
            leaseId = "lease-1",
            parseMessage = ::parse,
            persistMessage = { peer, message ->
                events += "persist:$peer"
                stored = message
                true
            },
            sendInjection = { events += "injection"; receipts += it },
            sendAck = { events += "ack"; receipts += it },
            correlatedAck = correlatedAck,
        ))

        assertEquals(listOf("persist:sender", "injection", "ack"), events)
        assertEquals("legacy-message", stored?.id)
        assertEquals("", stored?.incarnationId)
        receipts.forEach { receipt ->
            assertFalse(receipt.has("message_incarnation_id"))
            assertFalse(receipt.has("delivery_items"))
        }
    }

    @Test
    fun malformedLegacyDeliveryRemainsRejected() {
        val events = mutableListOf<String>()
        val malformed = deliveryFrame("legacy-malformed").apply {
            remove("message_incarnation_id")
            getJSONObject("message").put("body", 7)
        }

        assertFalse(settleIncomingDelivery(
            frame = malformed,
            agentId = "android",
            instanceId = "instance-1",
            leaseId = "lease-1",
            parseMessage = { events += "parse"; parse(it) },
            persistMessage = { _, _ -> events += "persist"; true },
            sendInjection = { events += "injection" },
            sendAck = { events += "ack" },
            correlatedAck = false,
        ))
        assertTrue(events.isEmpty())
    }

    @Test
    fun delimiterTextInOneBodyIsPersistedAsOneBodyAndNeverSplit() {
        var stored: ChatMessage? = null
        val frame = deliveryFrame("message-delimiter").apply {
            getJSONObject("message").put("body", "Mailbox message batch\\n\\nfirst\\n\\n---\\n\\nsecond")
        }

        assertTrue(settleIncomingDelivery(
            frame,
            "android",
            "instance-1",
            "lease-1",
            ::parse,
            { _, message -> stored = message; true },
            {},
            {},
        ))
        assertEquals("Mailbox message batch\\n\\nfirst\\n\\n---\\n\\nsecond", stored?.body)
    }

    @Test
    fun legacyMultiDeliveryWithoutPerItemEnvelopesRemainsUnacked() {
        val events = mutableListOf<String>()
        val first = deliveryItem("message-1", "550e8400-e29b-41d4-a716-446655440000", 2)
        val second = deliveryItem("message-2", "550e8400-e29b-41d4-a716-446655440001", 3)
        val frame = deliveryFrame("message-1").put("delivery_items", org.json.JSONArray(listOf(first, second)))

        val settled = settleIncomingDelivery(
            frame = frame,
            agentId = "android",
            instanceId = "instance-1",
            leaseId = "lease-1",
            parseMessage = { events += "parse"; parse(it) },
            persistMessage = { _, _ -> events += "persist"; true },
            sendInjection = { events += "injection" },
            sendAck = { events += "ack" },
        )

        assertFalse(settled)
        assertTrue(events.isEmpty())
    }

    @Test
    fun legacyRegistrationWithExplicitTwoItemBatchEchoesExactItemsWithoutIncarnation() {
        val registration = parseRegisteredSession(
            JSONObject()
                .put("version", "v1")
                .put("frame_id", "registered-legacy")
                .put("type", "registered")
                .put("request_id", "request-legacy")
                .put("agent_id", "android")
                .put("instance_id", "instance-1")
                .put("lease_id", "lease-1")
                .put("expires_at", "2026-08-29T00:00:30.000Z")
                .put("recovered", false)
                .put("agent_role", "user_interaction"),
            "request-legacy",
            "android",
            "instance-1",
        )
        assertNotNull(registration)
        val correlatedAck = usesCorrelatedAck(registration?.ackConfirmation)
        assertFalse(correlatedAck)

        val first = deliveryItem("message-1", "550e8400-e29b-41d4-a716-446655440000", 2)
        val second = deliveryItem("message-2", "550e8400-e29b-41d4-a716-446655440001", 2)
        val frame = deliveryFrame("message-1").apply {
            remove("message_incarnation_id")
            getJSONObject("message").put("body", "Mailbox message batch\\n\\none\\n\\n---\\n\\ntwo")
            put("delivery_items", org.json.JSONArray().put(first).put(second))
            put("delivery_messages", org.json.JSONArray().put(
                JSONObject()
                    .put("id", "message-1")
                    .put("from", "sender")
                    .put("to", "android")
                    .put("body", "one")
                    .put("created_at", "2026-08-29T00:00:00.000Z"),
            ).put(
                JSONObject()
                    .put("id", "message-2")
                    .put("from", "sender")
                    .put("to", "android")
                    .put("body", "two")
                    .put("created_at", "2026-08-29T00:00:01.000Z"),
            ))
        }
        val receipts = mutableListOf<Pair<String, JSONObject>>()
        var stored = emptyList<ChatMessage>()

        assertTrue(settleIncomingDelivery(
            frame = frame,
            agentId = "android",
            instanceId = "instance-1",
            leaseId = "lease-1",
            parseMessage = ::parse,
            persistMessage = { peer, message ->
                stored = mergeConversationMessages(stored, listOf(message))
                peer == "sender"
            },
            sendInjection = { receipts += "injection_requested" to it },
            sendAck = { receipts += "ack" to it },
            correlatedAck = correlatedAck,
        ))

        assertEquals(listOf("message-1", "message-2"), stored.map(ChatMessage::id))
        assertEquals(listOf("injection_requested", "ack"), receipts.map { it.first })
        receipts.forEach { (_, receipt) ->
            assertEquals(org.json.JSONArray().put(first).put(second).toString(), receipt.getJSONArray("delivery_items").toString())
            assertFalse(receipt.has("message_incarnation_id"))
        }
    }

    @Test
    fun settleDeliveryPersistsEveryExplicitTwoItemEnvelopeBeforeAcknowledging() {
        val events = mutableListOf<String>()
        var stored = emptyList<ChatMessage>()
        val first = deliveryItem("message-1", "550e8400-e29b-41d4-a716-446655440000", 2)
        val second = deliveryItem("message-2", "550e8400-e29b-41d4-a716-446655440001", 3)
        val frame = deliveryFrame("message-1").apply {
            getJSONObject("message").put("body", "Mailbox message batch\\n\\none\\n\\n---\\n\\ntwo")
            put("delivery_items", org.json.JSONArray().put(first).put(second))
            put("delivery_messages", org.json.JSONArray().put(
                JSONObject()
                    .put("id", "message-1")
                    .put("from", "sender")
                    .put("to", "android")
                    .put("body", "one")
                    .put("created_at", "2026-08-29T00:00:00.000Z"),
            ).put(
                JSONObject()
                    .put("id", "message-2")
                    .put("from", "sender")
                    .put("to", "android")
                    .put("body", "two")
                    .put("created_at", "2026-08-29T00:00:01.000Z"),
            ))
        }
        val receipts = mutableListOf<JSONObject>()

        assertTrue(settleIncomingDelivery(
            frame = frame,
            agentId = "android",
            instanceId = "instance-1",
            leaseId = "lease-1",
            parseMessage = ::parse,
            persistMessage = { peer, message ->
                events += "persist:$peer:${message.id}"
                stored = mergeConversationMessages(stored, listOf(message))
                true
            },
            sendInjection = { events += "injection"; receipts += it },
            sendAck = { events += "ack"; receipts += it },
        ))

        assertEquals(listOf("message-1", "message-2"), stored.map(ChatMessage::id))
        assertEquals(listOf("one", "two"), stored.map(ChatMessage::body))
        assertEquals(listOf("persist:sender:message-1", "persist:sender:message-2", "injection", "ack"), events)
        assertEquals(2, receipts.first().getJSONArray("delivery_items").length())
        assertEquals(2, receipts.last().getJSONArray("delivery_items").length())
    }

    @Test
    fun wrongDeliveryRecipientIsRejectedBeforePersistenceOrReceipt() {
        val events = mutableListOf<String>()
        val frame = deliveryFrame("message-wrong-recipient").apply {
            getJSONObject("message").put("to", "other-agent")
        }

        assertFalse(settleIncomingDelivery(
            frame,
            "android",
            "instance-1",
            "lease-1",
            { events += "parse"; parse(it) },
            { _, _ -> events += "persist"; true },
            { events += "injection" },
            { events += "ack" },
        ))
        assertTrue(events.isEmpty())
    }

    @Test
    fun mismatchedOrDuplicateDeliveryItemsAreRejectedBeforePersistence() {
        var persistenceCalls = 0
        val mismatched = deliveryFrame("message-1").put("delivery_items", org.json.JSONArray(listOf(
            deliveryItem("message-other", "550e8400-e29b-41d4-a716-446655440000", 2),
        )))
        val duplicate = deliveryFrame("message-1").put("delivery_items", org.json.JSONArray(listOf(
            deliveryItem("message-1", "550e8400-e29b-41d4-a716-446655440000", 2),
            deliveryItem("message-1", "550e8400-e29b-41d4-a716-446655440000", 2),
        )))
        val messageMismatch = deliveryFrame("message-1").apply {
            put("delivery_items", org.json.JSONArray(listOf(deliveryItem("message-1", "550e8400-e29b-41d4-a716-446655440000", 2))))
            put("delivery_messages", org.json.JSONArray().put(
                JSONObject()
                    .put("id", "message-other")
                    .put("from", "sender")
                    .put("to", "android")
                    .put("body", "wrong")
                    .put("created_at", "2026-08-29T00:00:00.000Z"),
            ))
        }

        listOf(mismatched, duplicate, messageMismatch).forEach { frame ->
            assertFalse(settleIncomingDelivery(
                frame = frame,
                agentId = "android",
                instanceId = "instance-1",
                leaseId = "lease-1",
                parseMessage = ::parse,
                persistMessage = { _, _ -> persistenceCalls++; true },
                sendInjection = { error("receipt sent") },
                sendAck = { error("receipt sent") },
            ))
        }
        assertEquals(0, persistenceCalls)
    }

    @Test
    fun acknowledgementFailureLeavesPersistedMessageUntouched() {
        var stored = emptyList<ChatMessage>()
        val settled = settleIncomingDelivery(
            frame = deliveryFrame("message-2"),
            agentId = "android",
            instanceId = "instance-1",
            leaseId = "lease-1",
            parseMessage = ::parse,
            persistMessage = { _, message ->
                stored = mergeConversationMessages(stored, listOf(message))
                true
            },
            sendInjection = { },
            sendAck = { error("socket closed") },
        )

        assertTrue(settled)
        assertEquals(listOf("message-2"), stored.map(ChatMessage::id))
    }

    @Test
    fun staleIncomingMessageRejectedByRetentionSendsNoReceipt() {
        val now = Instant.parse("2026-08-29T00:00:00Z").toEpochMilli()
        val events = mutableListOf<String>()
        val frame = deliveryFrame("stale-message").put(
            "message",
            JSONObject()
                .put("id", "stale-message")
                .put("from", "sender")
                .put("to", "android")
                .put("body", "old")
                .put("created_at", Instant.ofEpochMilli(now - HISTORY_RETENTION_MS - 1).toString()),
        )

        val settled = settleIncomingDelivery(
            frame = frame,
            agentId = "android",
            instanceId = "instance-1",
            leaseId = "lease-1",
            parseMessage = { value ->
                ChatMessage(
                    id = value.getString("id"),
                    incarnationId = "",
                    sequence = 1,
                    sender = value.getString("from"),
                    body = value.getString("body"),
                    direction = Direction.INCOMING,
                    createdAt = value.getString("created_at"),
                )
            },
            // This is the append/persistence contract: retention rejection is not durable.
            persistMessage = { _, message -> isRetainedConversationMessage(message, now) },
            sendInjection = { events += "injection" },
            sendAck = { events += "ack" },
        )

        assertFalse(settled)
        assertTrue(events.isEmpty())
    }

    @Test
    fun malformedDeliveryFrameSendsNoReceiptOrPersistenceAttempt() {
        val events = mutableListOf<String>()
        val malformed = deliveryFrame("message-malformed").apply {
            getJSONObject("message").put("body", 7)
        }

        assertFalse(settleIncomingDelivery(
            frame = malformed,
            agentId = "android",
            instanceId = "instance-1",
            leaseId = "lease-1",
            parseMessage = { events += "parse"; parse(it) },
            persistMessage = { _, _ -> events += "persist"; true },
            sendInjection = { events += "injection" },
            sendAck = { events += "ack" },
        ))
        assertTrue(events.isEmpty())
    }

    @Test
    fun deliveryValidationRequiresTheSameStrictMessageFieldsAsConversationValidation() {
        val frame = deliveryFrame("message-valid")
        val conversation = JSONObject()
            .put("version", "v1")
            .put("frame_id", "conversation-valid")
            .put("type", "conversation_message")
            .put("message", JSONObject()
                .put("sequence", 1)
                .put("id", "message-valid")
                .put("message_incarnation_id", "550e8400-e29b-41d4-a716-446655440000")
                .put("from", "sender")
                .put("to", "android")
                .put("body", "hello")
                .put("created_at", "2026-08-29T00:00:00.000Z")
                .put("recorded_at", "2026-08-29T00:00:00.000Z")
                .put("attachments", org.json.JSONArray()))

        assertTrue(validateLiveMessageFrame(frame, LiveMessageFrameKind.DELIVERY_MESSAGE) != null)
        assertTrue(validateLiveMessageFrame(conversation, LiveMessageFrameKind.CONVERSATION_MESSAGE) != null)
        val originalDelivery = JSONObject(frame.getJSONObject("message").toString())
        val originalConversation = JSONObject(conversation.getJSONObject("message").toString())
        listOf("id", "from", "to", "body", "created_at").forEach { key ->
            frame.getJSONObject("message").put(key, if (key == "body") 7 else "")
            conversation.getJSONObject("message").put(key, if (key == "body") 7 else "")
            assertFalse(validateLiveMessageFrame(frame, LiveMessageFrameKind.DELIVERY_MESSAGE) != null)
            assertFalse(validateLiveMessageFrame(conversation, LiveMessageFrameKind.CONVERSATION_MESSAGE) != null)
            frame.getJSONObject("message").put(key, originalDelivery.get(key))
            conversation.getJSONObject("message").put(key, originalConversation.get(key))
        }
    }

    @Test
    fun invalidOrUnpersistableDeliverySendsNoReceipt() {
        var sends = 0
        assertFalse(settleIncomingDelivery(
            frame = deliveryFrame("message-3"),
            agentId = "android",
            instanceId = "instance-1",
            leaseId = "lease-1",
            parseMessage = { null },
            persistMessage = { _, _ -> true },
            sendInjection = { sends++ },
            sendAck = { sends++ },
        ))
        assertFalse(settleIncomingDelivery(
            frame = deliveryFrame("message-4"),
            agentId = "android",
            instanceId = "instance-1",
            leaseId = "lease-1",
            parseMessage = ::parse,
            persistMessage = { _, _ -> false },
            sendInjection = { sends++ },
            sendAck = { sends++ },
        ))
        assertEquals(0, sends)
    }

    private fun deliveryFrame(id: String) = JSONObject()
        .put("version", "v1")
        .put("frame_id", "frame-$id")
        .put("type", "message")
        .put("message", JSONObject()
            .put("id", id)
            .put("from", "sender")
            .put("to", "android")
            .put("body", "hello")
            .put("created_at", "2026-08-29T00:00:00.000Z"))
        .put("message_incarnation_id", "550e8400-e29b-41d4-a716-446655440000")
        .put("attempt_number", 2)
        .put("delivery_expires_at", "2026-08-29T00:00:30.000Z")

    private fun deliveryItem(id: String, incarnation: String, attempt: Int) = JSONObject()
        .put("message_id", id)
        .put("message_incarnation_id", incarnation)
        .put("attempt_number", attempt)
        .put("delivery_expires_at", "2026-08-29T00:00:30.000Z")

    private fun parse(value: JSONObject) = ChatMessage(
        id = value.getString("id"),
        incarnationId = "",
        sequence = 1,
        sender = value.getString("from"),
        body = value.getString("body"),
        direction = Direction.INCOMING,
        createdAt = value.getString("created_at"),
        recipient = value.optString("to", "android"),
    )
}
