package life.meyers.mecha

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ReminderStateTest {
    private val now = 1_894_694_400_000L

    @Test
    fun reminderListIsBoundedAndFiltersActiveSelectedPeerRows() {
        val rows = listOf(
            row("one", "peer-a", "scheduled"),
            row("two", "peer-a", "due", enabled = false),
            row("three", "peer-a", "failed"),
            row("four", "peer-b", "scheduled"),
        )
        assertEquals(listOf("one"), visibleRemindersForPeer(rows, "peer-a").map { it.id })
        val oversized = "{\"reminders\":[${(0..1_000).joinToString(",") { rowJson("r$it") }}]}"
        assertEquals(null, parseReminderListResponse(oversized))
    }

    @Test
    fun createPayloadMatchesRouterOneShotAndRecurringContracts() {
        val oneShot = reminderCreatePayload("android", "worker", "  send report  ", "  Subject ", 5, false, "reminder-1", now)
        assertEquals("reminder-1", oneShot.id)
        assertEquals("worker", oneShot.target)
        assertEquals("send report", oneShot.body)
        assertEquals("Subject", oneShot.subject)
        assertEquals(null, oneShot.intervalMs)
        val recurring = reminderCreatePayload("android", "worker", "message", "", 5, true, "reminder-2", now)
        assertEquals(null, recurring.subject)
        assertEquals(300_000L, recurring.intervalMs)
        assertEquals("2030-01-15T08:05:00Z", recurring.nextDueAt)
    }

    @Test
    fun createValidationMatchesWebDelayAndBodyRules() {
        assertEquals("Enter reminder text.", validateReminderCreate("  ", 1))
        assertEquals("Enter a delay from 1 to 10080 minutes.", validateReminderCreate("body", 0))
        assertEquals("Enter a delay from 1 to 10080 minutes.", validateReminderCreate("body", 10_081))
        assertEquals(null, validateReminderCreate("body", 10_080))
    }

    @Test
    fun countdownUsesExactWebWordingAndBoundaries() {
        assertEquals("Due now", formatReminderCountdown("2030-01-15T08:00:00Z", now))
        assertEquals("Due in 1s", formatReminderCountdown("2030-01-15T08:00:00.999Z", now))
        assertEquals("Due in 1d 2h 3m 4s", formatReminderCountdown("2030-01-16T10:03:04Z", now))
        assertEquals("Due time unavailable", formatReminderCountdown("bad-date", now))
        assertEquals("Recurring", reminderRecurrenceLabel(1L))
        assertEquals("One-shot", reminderRecurrenceLabel(null))
    }

    @Test
    fun lockFrameUsesExactFencedFields() {
        val frame = reminderLockControlPayload("frame", "android", "source-instance", "lease", "worker", "target-instance", "control", "reminder-1", "set")
        assertEquals("v1", frame.version)
        assertEquals("reminder_lock_control", frame.type)
        assertEquals("worker", frame.targetAgentId)
        assertEquals("target-instance", frame.targetInstanceId)
        assertEquals("reminder-1", frame.reminderId)
    }

    @Test
    fun lockResultsCorrelateAndRejectStaleInstances() {
        val result = ParsedReminderLockResult("worker", "target-instance", "lease", "android", "control", "reminder-1", true, null)
        val pending = PendingReminderLock("control", "request", "worker", "target-instance", "reminder-1", "set", "http://router", 1)
        assertTrue(matchesPendingReminderLock(pending, result, "android", "lease"))
        assertFalse(matchesPendingReminderLock(pending.copy(targetInstanceId = "new-instance"), result, "android", "lease"))
        assertFalse(matchesPendingReminderLock(pending, result, "other-source", "lease"))
        val rejected = result.copy(accepted = false, errorCode = "reminder_locked")
        assertFalse(rejected.accepted)
        assertEquals("reminder_locked", rejected.errorCode)
    }

    @Test
    fun reminderStatusesDistinguishCreateLockFailureRejectionAndTimeout() {
        assertTrue(ReminderStatus.ACCEPTED != ReminderStatus.CREATED_LOCK_FAILED)
        assertTrue(ReminderStatus.CREATED_LOCK_FAILED != ReminderStatus.REJECTED)
        assertTrue(ReminderStatus.REJECTED != ReminderStatus.TIMEOUT)
        assertTrue(ReminderStatus.TIMEOUT != ReminderStatus.UNCONFIRMED)
    }

    private fun row(id: String, target: String = "peer-a", state: String = "scheduled", enabled: Boolean = true) = ReminderSummary(
        id, target, "android", null, "body", "2030-01-15T08:00:00Z", "2030-01-15T08:00:00Z", null, state, enabled,
    )

    private fun rowJson(id: String): String = "{\"id\":\"$id\",\"target\":\"peer\",\"creator\":\"android\",\"body\":\"body\",\"created_at\":\"2030-01-15T08:00:00Z\",\"next_due_at\":\"2030-01-15T08:00:00Z\",\"interval_ms\":null,\"state\":\"scheduled\"}"
}
