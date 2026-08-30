package life.meyers.mecha

import org.junit.Assert.assertEquals
import org.json.JSONObject
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

private val MODEL_CONTROL_FRAME_FIELDS = setOf("version", "frame_id", "type", "agent_id", "instance_id", "lease_id", "target_agent_id", "target_instance_id", "control_id", "action", "model")
private val MODEL_CONTROL_MODEL_FIELDS = setOf("provider", "id")
private val GOAL_CONTROL_FRAME_FIELDS = setOf("version", "frame_id", "type", "agent_id", "instance_id", "lease_id", "target_agent_id", "target_instance_id", "control_id", "action", "goal", "settings")
private val GOAL_CONTROL_SETTINGS_FIELDS = setOf("goal_lock_enabled", "snooze_enabled", "snooze_duration_ms", "snooze_turn_limit", "snooze_budget_cap_ms", "snooze_refill_yield_ms", "snooze_refill_elapsed_ms")

class ParityControlsTest {
    @Test
    fun registeredResponseWithoutAckCapabilityUsesLegacySettlement() {
        val session = parseRegisteredSession(registeredFrame(), "request-1", "android", "instance-1")

        assertNotNull(session)
        assertNull(session?.ackConfirmation)
        assertFalse(usesCorrelatedAck(session?.ackConfirmation))
        assertTrue(registeredSessionMatchesLease(session, "lease-1"))
    }

    @Test
    fun registeredResponseWithSupportedAckCapabilityEnablesCorrelatedSettlement() {
        val session = parseRegisteredSession(
            registeredFrame().put("ack_confirmation", CORRELATED_ACK_CONFIRMATION),
            "request-1",
            "android",
            "instance-1",
        )

        assertEquals(CORRELATED_ACK_CONFIRMATION, session?.ackConfirmation)
        assertTrue(usesCorrelatedAck(session?.ackConfirmation))
        assertTrue(registeredSessionMatchesLease(session, "lease-1"))
    }

    @Test
    fun registeredResponseWithUnknownAckCapabilityRemainsConnectedWithLegacySettlement() {
        val session = parseRegisteredSession(
            registeredFrame().put("ack_confirmation", "future_v2"),
            "request-1",
            "android",
            "instance-1",
        )

        assertEquals("future_v2", session?.ackConfirmation)
        assertFalse(usesCorrelatedAck(session?.ackConfirmation))
        assertTrue(registeredSessionMatchesLease(session, "lease-1"))
        assertFalse(registeredSessionMatchesLease(session, "other-lease"))
    }

    private fun registeredFrame() = JSONObject()
        .put("version", "v1")
        .put("frame_id", "registered-1")
        .put("type", "registered")
        .put("request_id", "request-1")
        .put("agent_id", "android")
        .put("instance_id", "instance-1")
        .put("lease_id", "lease-1")
        .put("expires_at", "2026-08-29T00:00:30.000Z")
        .put("recovered", false)
        .put("agent_role", "user_interaction")

    private val draft = GoalEditorDraft(
        enabled = true,
        goal = "Ship the Android slice",
        goalLockEnabled = true,
        snoozeEnabled = true,
        snoozeDurationMs = 60_000,
        snoozeTurnLimit = null,
        snoozeBudgetCapMs = null,
        snoozeRefillYieldMs = 30_000,
        snoozeRefillElapsedMs = 60_000,
        outcomeCounts = GoalOutcomeCounts(2, 1),
    )

    @Test
    fun catalogHasDistinctLoadingEmptyAndReadyStates() {
        assertEquals(CatalogStatus.LOADING, ModelCatalog(CatalogStatus.LOADING).status)
        assertEquals(CatalogStatus.EMPTY, ModelCatalog(CatalogStatus.EMPTY).status)
        assertEquals(CatalogStatus.READY, ModelCatalog(CatalogStatus.READY, listOf(ModelSummary("openai", "gpt-5"))).status)
    }

    @Test
    fun modelAndGoalWireFieldsMatchCurrentProtocol() {
        assertEquals(setOf("version", "frame_id", "type", "agent_id", "instance_id", "lease_id", "target_agent_id", "target_instance_id", "control_id", "action", "model"), MODEL_CONTROL_FRAME_FIELDS)
        assertEquals(setOf("provider", "id"), MODEL_CONTROL_MODEL_FIELDS)
        assertEquals(setOf("version", "frame_id", "type", "agent_id", "instance_id", "lease_id", "target_agent_id", "target_instance_id", "control_id", "action", "goal", "settings"), GOAL_CONTROL_FRAME_FIELDS)
        assertEquals(setOf("goal_lock_enabled", "snooze_enabled", "snooze_duration_ms", "snooze_turn_limit", "snooze_budget_cap_ms", "snooze_refill_yield_ms", "snooze_refill_elapsed_ms"), GOAL_CONTROL_SETTINGS_FIELDS)
    }

    @Test
    fun goalEditorValidationCoversSetAndAllSettings() {
        assertEquals(null, validateGoalDraft(draft))
        assertEquals("invalid_goal", validateGoalDraft(draft.copy(goal = "   ")))
        assertEquals("invalid_goal", validateGoalDraft(draft.copy(goal = "x".repeat(4_001))))
        assertEquals("invalid_settings", validateGoalDraft(draft.copy(snoozeDurationMs = 0)))
        assertEquals("invalid_settings", validateGoalDraft(draft.copy(snoozeTurnLimit = 21)))
        assertEquals("invalid_settings", validateGoalDraft(draft.copy(snoozeBudgetCapMs = 0)))
        assertEquals(null, validateGoalDraft(draft.copy(enabled = false, goal = "")))
    }

    @Test
    fun acceptedAndRejectedStatesAreDistinct() {
        val accepted = ControlState(ControlStatus.ACCEPTED, "control-1", "worker", "worker-1")
        val rejected = ControlState(ControlStatus.REJECTED, "control-2", "worker", "worker-1", "invalid_model")
        assertEquals(ControlStatus.ACCEPTED, accepted.status)
        assertEquals(ControlStatus.REJECTED, rejected.status)
        assertTrue(accepted.status != rejected.status)
    }

    @Test
    fun timeoutAndDisconnectedChangesRemainUnconfirmed() {
        assertEquals(ControlStatus.UNCONFIRMED, controlStatusForError("model_control_expired"))
        assertEquals(ControlStatus.UNCONFIRMED, controlStatusForError("goal_control_expired"))
        assertEquals(ControlStatus.UNCONFIRMED, controlStatusForError("router_disconnected"))
        assertEquals(ControlStatus.REJECTED, controlStatusForError("target_not_connected"))
        assertEquals(ControlStatus.REJECTED, controlStatusForError("invalid_model"))
    }

    @Test
    fun staleTargetAndResultCannotSettlePendingControl() {
        val pending = PendingControl("control-1", "request-1", "worker", "worker-1")
        assertFalse(matchesPending(pending, "worker", "worker-2", "control-1", "android"))
        assertFalse(matchesPending(pending, "other", "worker-1", "control-1", "android"))
        assertFalse(matchesPending(pending, "worker", "worker-1", "control-1", "worker"))
        assertTrue(matchesPending(pending, "worker", "worker-1", "control-1", "android"))
    }

    @Test
    fun capabilitiesFailClosedWhenControlLimitIsMissing() {
        val capabilities = ControlCapabilities(modelCatalogV1 = true, modelControlV1 = false, goalControlV1 = false)
        assertTrue(capabilities.modelCatalogV1)
        assertFalse(capabilities.modelControlV1)
        assertFalse(capabilities.goalControlV1)
    }

    @Test
    fun editorDraftRetainsCurrentSettingsAndOutcomes() {
        val restored = GoalEditorDraft.from(GoalState(true, "goal", true, false, 10, 3, 120, 20, 30, GoalOutcomeCounts(4, 5)))
        assertEquals(3, restored.snoozeTurnLimit)
        assertEquals(120L, restored.snoozeBudgetCapMs)
        assertEquals(4, restored.outcomeCounts.completed)
        assertEquals(5, restored.outcomeCounts.notDone)
    }
}
