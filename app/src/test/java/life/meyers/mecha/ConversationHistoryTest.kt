package life.meyers.mecha

import org.junit.Assert.assertEquals
import org.junit.Test

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

        assertEquals(listOf("retained", "live"), reconciled.map(ChatMessage::id))
    }

    @Test
    fun snapshotAndLiveCopiesOfOneIncarnationCollapse() {
        val live = message("same", "same-incarnation", 3)
        val snapshot = live.copy(body = "canonical")

        val reconciled = reconcileConversationMessages(listOf(live), listOf(snapshot), 3)

        assertEquals(1, reconciled.size)
        assertEquals("canonical", reconciled.single().body)
    }

    private fun message(id: String, incarnation: String, sequence: Long) = ChatMessage(
        id = id,
        incarnationId = incarnation,
        sequence = sequence,
        sender = "peer",
        body = id,
        direction = Direction.INCOMING,
        createdAt = "2026-08-03T00:00:00.000Z",
    )
}
