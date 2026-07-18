package life.meyers.mecha

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class HistoryRetentionTest {
    @Test
    fun keepsAnIncomingMessageAtTheHistoryLimit() {
        val history = (1..200).map { message(it) }
        val incoming = message(201, Direction.INCOMING)

        val retained = boundedHistory(history, incoming)

        assertEquals(200, retained.size)
        assertEquals("201", retained.last().id)
        assertTrue(retained.any { it.id == "201" && it.direction == Direction.INCOMING })
        assertEquals("2", retained.first().id)
    }

    @Test
    fun preservesTheNewestMessagesBeyondTheHistoryLimit() {
        var history = (1..200).map { message(it) }
        history = boundedHistory(history, message(201, Direction.INCOMING))
        history = boundedHistory(history, message(202, Direction.INCOMING))

        assertEquals(200, history.size)
        assertEquals(listOf("3", "4"), history.take(2).map { it.id })
        assertEquals(listOf("201", "202"), history.takeLast(2).map { it.id })
    }

    private fun message(index: Int, direction: Direction = Direction.OUTGOING) = ChatMessage(
        id = index.toString(), sender = "sender", body = "message $index", direction = direction, createdAt = "2026-07-18T10:00:00.000Z",
    )
}
