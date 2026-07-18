package life.meyers.mecha

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class HistoryRetentionTest {
    @Test
    fun keepsAnIncomingMessageAtTheHistoryLimit() {
        val history = (1..MAX_CONVERSATION_MESSAGES).map { message(it) }
        val incoming = message(MAX_CONVERSATION_MESSAGES + 1, Direction.INCOMING)

        val retained = boundedHistory(history, incoming)

        assertEquals(MAX_CONVERSATION_MESSAGES, retained.size)
        assertEquals("26", retained.last().id)
        assertTrue(retained.any { it.id == "26" && it.direction == Direction.INCOMING })
        assertEquals("2", retained.first().id)
    }

    @Test
    fun openingOversizedHistoryImmediatelyKeepsOnlyTheLatestMessages() {
        val retained = retainedHistory((1..27).map { message(it) })

        assertEquals(MAX_CONVERSATION_MESSAGES, retained.size)
        assertEquals(listOf("3", "4"), retained.take(2).map { it.id })
        assertEquals("27", retained.last().id)
    }

    private fun message(index: Int, direction: Direction = Direction.OUTGOING) = ChatMessage(
        id = index.toString(), sender = "sender", body = "message $index", direction = direction, createdAt = "2026-07-18T10:00:00.000Z",
    )
}
