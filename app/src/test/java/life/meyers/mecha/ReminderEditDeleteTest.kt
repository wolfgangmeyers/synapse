package life.meyers.mecha

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class ReminderEditDeleteTest {
    @Test
    fun patchBodyIsExactAndNullSubjectClearsTheSubject() {
        val patch = ReminderPatchFields(
            body = "  updated body  ",
            subject = "   ",
            includeBody = true,
            includeSubject = true,
        )

        assertEquals(
            listOf(ReminderPatchEntry("body", "updated body"), ReminderPatchEntry("subject", null)),
            reminderPatchEntries(patch),
        )
    }

    @Test
    fun emptyPatchIsRejectedBeforeARequestCanBeBuilt() {
        val empty = ReminderPatchFields()
        assertEquals("Change at least one reminder field.", validateReminderPatch(empty))
        try {
            reminderPatchEntries(empty)
            throw AssertionError("Expected an empty reminder patch to be rejected")
        } catch (_: IllegalArgumentException) {
            // The validation result above carries the user-facing rejection text.
        }
    }

    @Test
    fun deleteAcceptsOnlyNoContent() {
        assertTrue(reminderCrudResponseAccepted("delete", 204))
        assertFalse(reminderCrudResponseAccepted("delete", 200))
        assertTrue(reminderCrudResponseAccepted("update", 204))
    }

    @Test
    fun notCancelableResponseProducesTheUserFacingRejectedState() {
        val state = reminderCrudErrorState(409, "reminder_not_cancelable")

        assertEquals(
            ReminderUiState(
                ReminderStatus.REJECTED,
                "Reminder is already linked to a delivery.",
                "reminder_not_cancelable",
            ),
            state,
        )
    }

    @Test
    fun staleCrudResultsAreRejectedAcrossGenerationRouterPeerAndInstance() {
        val pending = PendingReminderCrud(
            requestId = "request-1",
            operation = "update",
            peer = "peer-a",
            router = "router-a:8765",
            connectionGeneration = 7,
            reminderId = "reminder-1",
            targetInstanceId = "instance-a",
        )

        assertTrue(matchesPendingReminderCrud(pending, "router-a:8765", 7, "peer-a", "instance-a"))
        assertFalse(matchesPendingReminderCrud(pending, "router-a:8765", 6, "peer-a", "instance-a"))
        assertFalse(matchesPendingReminderCrud(pending, "router-b:8765", 7, "peer-a", "instance-a"))
        assertFalse(matchesPendingReminderCrud(pending, "router-a:8765", 7, "peer-b", "instance-a"))
        assertFalse(matchesPendingReminderCrud(pending, "router-a:8765", 7, "peer-a", "instance-b"))
        assertFalse(matchesPendingReminderCrud(pending, "router-a:8765", 7, "peer-a", null))
    }

    @Test
    fun timeoutRemovesThePendingRequestAndReportsTimeout() {
        val request = PendingReminderCrud(
            requestId = "request-1",
            operation = "delete",
            peer = "peer-a",
            router = "router-a:8765",
            connectionGeneration = 7,
            reminderId = "reminder-1",
            targetInstanceId = "instance-a",
        )
        val pending = linkedMapOf(request.requestId to request)

        val expired = pending.remove(request.requestId)

        assertEquals(request, expired)
        assertTrue(pending.isEmpty())
        assertNull(pending.remove(request.requestId))
        assertEquals(
            ReminderUiState(ReminderStatus.TIMEOUT, "The reminder deletion timed out.", "control_timeout"),
            reminderCrudTimeoutState(request.operation),
        )
    }

    @Test
    fun olderRefreshResponsesCannotOverwriteTheCurrentReminderList() {
        assertTrue(reminderListResponseIsCurrent(4, 4, 7, 7, "router-a:8765", "router-a:8765"))
        assertFalse(reminderListResponseIsCurrent(3, 4, 7, 7, "router-a:8765", "router-a:8765"))
        assertFalse(reminderListResponseIsCurrent(4, 4, 6, 7, "router-a:8765", "router-a:8765"))
        assertFalse(reminderListResponseIsCurrent(4, 4, 7, 7, "router-a:8765", "router-b:8765"))
    }
}
