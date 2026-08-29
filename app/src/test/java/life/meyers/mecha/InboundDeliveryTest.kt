package life.meyers.mecha

import java.time.Instant
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class InboundDeliveryTest {
    @Test
    fun persistsAndDeduplicatesBeforeSendingReceipts() {
        val events = mutableListOf<String>()
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
            sendInjection = { events += "injection" },
            sendAck = { events += "ack" },
        )

        assertTrue(settled)
        assertEquals(listOf("persist:sender", "injection", "ack"), events)
        assertEquals(1, stored.size)

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
        .put("attempt_number", 2)
        .put("message", JSONObject().put("id", id).put("from", "sender").put("body", "hello"))

    private fun parse(value: JSONObject) = ChatMessage(
        id = value.getString("id"),
        incarnationId = "",
        sequence = 1,
        sender = value.getString("from"),
        body = value.getString("body"),
        direction = Direction.INCOMING,
        createdAt = Instant.now().toString(),
    )
}
