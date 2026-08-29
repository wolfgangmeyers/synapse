package life.meyers.mecha

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.Instant

class ConversationHistoryTest {
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
            createdAt = "2026-08-28T23:59:00Z",
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

    private fun message(
        id: String,
        incarnation: String = "",
        sequence: Long = 0,
        createdAt: String = "2026-08-03T00:00:00.000Z",
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
}
