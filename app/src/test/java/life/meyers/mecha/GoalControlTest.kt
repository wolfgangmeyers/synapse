package life.meyers.mecha

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * TDD contract for Task 250. The literals below are the user-facing strings in
 * origin/main web/router-ui/app.js at 999e2e0518bf9f66f56ae32287ae147b5bb17443.
 */
class GoalControlTest {
    private val defaults = GoalState(
        enabled = false,
        goal = "",
        blocking = true,
        goalLockEnabled = false,
        snoozeEnabled = true,
        snoozeDurationMs = 60_000,
        snoozeTurnLimit = null,
        snoozeBudgetCapMs = null,
        snoozeRefillYieldMs = 30_000,
        snoozeRefillElapsedMs = 60_000,
        outcomeCounts = GoalOutcomeCounts(),
    )

    private val enabledState = defaults.copy(
        enabled = true,
        goal = "Finish the Android goal editor",
        blocking = false,
        goalLockEnabled = true,
        snoozeEnabled = false,
        snoozeDurationMs = 90_000,
        snoozeTurnLimit = 20,
        snoozeBudgetCapMs = 15 * 60_000,
        snoozeRefillYieldMs = 45_000,
        snoozeRefillElapsedMs = 120_000,
        outcomeCounts = GoalOutcomeCounts(blocked = 1, completed = 2, notDone = 3, waitingForUserAuthorization = 4),
    )

    @Test
    fun fullGoalStateNormalizesEveryProtocolFieldAndCompactProjectionUsesWebDefaults() {
        assertEquals(defaults, goalStateFromCompact(null))
        assertEquals(defaults.copy(enabled = true, goal = "ship it"), goalStateFromCompact(GoalProjection(true, "ship it")))
        assertEquals(defaults, goalStateFromCompact(GoalProjection(false, "")))

        assertEquals(enabledState, normalizeGoalState(goalStateJson(enabledState)))
        val stateWithoutBlocking = goalStateJson(defaults.copy(enabled = true, goal = "blocking defaults on"))
        stateWithoutBlocking.remove("blocking")
        assertEquals(
            defaults.copy(enabled = true, goal = "blocking defaults on"),
            normalizeGoalState(stateWithoutBlocking),
        )

        val missingTurnLimit = goalStateJson(defaults).apply { remove("snooze_turn_limit") }
        val missingBudgetCap = goalStateJson(defaults).apply { remove("snooze_budget_cap_ms") }
        assertNull(normalizeGoalState(missingTurnLimit))
        assertNull(normalizeGoalState(missingBudgetCap))
        assertEquals(defaults, normalizeGoalState(goalStateJson(defaults)))

        assertNull(normalizeGoalState(goalStateJson(enabledState).put("enabled", "true")))
        assertNull(normalizeGoalState(goalStateJson(enabledState).put("goal", "")))
        assertNull(normalizeGoalState(goalStateJson(enabledState).put("goal_lock_enabled", "true")))
        assertNull(normalizeGoalState(goalStateJson(enabledState).put("snooze_duration_ms", 0)))
        assertNull(normalizeGoalState(goalStateJson(enabledState).put("snooze_duration_ms", 1.5)))
        assertNull(normalizeGoalState(goalStateJson(enabledState).put("snooze_duration_ms", 9_007_199_254_740_992L)))
        assertNull(normalizeGoalState(goalStateJson(enabledState).put("snooze_turn_limit", 21)))
        assertNull(normalizeGoalState(goalStateJson(enabledState).put("snooze_turn_limit", 1.5)))
        assertNull(normalizeGoalState(goalStateJson(enabledState).put("snooze_budget_cap_ms", 0)))
        assertNull(normalizeGoalState(goalStateJson(enabledState).put("snooze_budget_cap_ms", 9_007_199_254_740_992L)))
        assertNull(normalizeGoalState(goalStateJson(enabledState).put("snooze_refill_yield_ms", -1)))
        assertNull(normalizeGoalState(goalStateJson(enabledState).put("snooze_refill_elapsed_ms", 1.5)))
        assertNull(normalizeGoalState(goalStateJson(enabledState).put("outcome_counts", JSONObject().put("BLOCKED", -1))))
        assertNull(normalizeGoalState(goalStateJson(enabledState).put("outcome_counts", JSONObject(goalStateJson(enabledState).getJSONObject("outcome_counts").toString()).put("COMPLETED", 1_000_001))))
        assertNull(normalizeGoalState(goalStateJson(enabledState).put("outcome_counts", JSONObject(goalStateJson(enabledState).getJSONObject("outcome_counts").toString()).put("NOT_DONE", 1.5))))
        assertNull(safeGoalInteger(Double.NaN, 1, 9_007_199_254_740_991L))
        assertNull(safeGoalInteger(Double.POSITIVE_INFINITY, 1, 9_007_199_254_740_991L))
        assertNull(safeGoalInteger(1.5, 1, 9_007_199_254_740_991L))
        assertEquals(1L, safeGoalInteger(1.0, 1, 9_007_199_254_740_991L))
        listOf("BLOCKED", "COMPLETED", "NOT_DONE", "WAITING_FOR_USER_AUTHORIZATION").forEach { count ->
            val missing = goalStateJson(enabledState).getJSONObject("outcome_counts")
            missing.remove(count)
            assertNull(normalizeGoalState(goalStateJson(enabledState).put("outcome_counts", missing)))
            assertNull(normalizeGoalState(goalStateJson(enabledState).put("outcome_counts", JSONObject(missing.toString()).put(count, "invalid"))))
        }
        assertNull(normalizeGoalState(goalStateJson(enabledState).put("goal", "x".repeat(4_001))))
    }

    @Test
    fun setAndOffFramesValidateGoalsAndSerializeEveryExistingSetting() {
        // origin/main local blank validation: "Turn on the goal and enter a description before saving."
        assertEquals("Turn on the goal and enter a description before saving.", goalControlValidationError(defaults.copy(enabled = true, goal = " \n ")))
        assertNull(goalControlValidationError(enabledState))
        assertNull(goalControlValidationError(defaults))

        val set = goalControlFrame(
            sourceAgentId = "android",
            sourceInstanceId = "android-instance",
            leaseId = "android-lease",
            targetAgentId = "worker",
            targetInstanceId = "worker-instance",
            controlId = "goal-control-1",
            state = enabledState,
        )
        assertEquals("v1", set.getString("version"))
        assertEquals("goal_control", set.getString("type"))
        assertEquals("set", set.getString("action"))
        assertEquals(enabledState.goal, set.getString("goal"))
        assertEquals("android", set.getString("agent_id"))
        assertEquals("android-instance", set.getString("instance_id"))
        assertEquals("android-lease", set.getString("lease_id"))
        assertEquals("worker", set.getString("target_agent_id"))
        assertEquals("worker-instance", set.getString("target_instance_id"))
        assertEquals("goal-control-1", set.getString("control_id"))
        assertSettings(set.getJSONObject("settings"), enabledState)

        val off = goalControlFrame(
            sourceAgentId = "android",
            sourceInstanceId = "android-instance",
            leaseId = "android-lease",
            targetAgentId = "worker",
            targetInstanceId = "worker-instance",
            controlId = "goal-control-2",
            state = defaults.copy(goal = "must be cleared"),
        )
        assertEquals("off", off.getString("action"))
        assertEquals("", off.getString("goal"))
        assertSettings(off.getJSONObject("settings"), defaults.copy(goal = "must be cleared"))
    }

    @Test
    fun errorMappingUsesTheExactOriginMainWebMessages() {
        // origin/main web/router-ui/app.js::goalControlErrorMessage
        assertEquals("Goal controls are not available on this router.", goalControlErrorMessage("goal_control_unavailable"))
        assertEquals("This router does not support disabling goal blocking.", goalControlErrorMessage("goal_control_blocking_unavailable"))
        assertEquals("Check the goal settings and try again.", goalControlErrorMessage("invalid_settings"))
        assertEquals("The goal change was not saved.", goalControlErrorMessage("invalid_goal_settings"))
        assertEquals("Enter a goal before saving.", goalControlErrorMessage("invalid_goal"))
        assertEquals("The goal change timed out.", goalControlErrorMessage("control_timeout"))
        assertEquals("The router connection closed before the change was confirmed.", goalControlErrorMessage("router_disconnected"))
        assertEquals("The router is reconnecting.", goalControlErrorMessage("router_reconnecting"))
        assertEquals("That agent is no longer connected.", goalControlErrorMessage("target_not_connected"))
        assertEquals("That agent restarted. Close this editor and try again.", goalControlErrorMessage("target_instance_changed"))
        assertEquals("The goal change was not saved.", goalControlErrorMessage("worker_rejected_the_goal"))
        // The immediate local checks use their own source literals.
        assertEquals("The agent is not ready for goal changes.", goalControlUnavailableTargetMessage())
        assertEquals("The router is not connected.", goalControlDisconnectedMessage())
    }

    @Test
    fun sendAndRequestCorrelationRequireTheOriginalConnectionAndRequest() {
        assertFalse(goalControlCapabilityMaySend(null))
        assertFalse(goalControlCapabilityMaySend(false))
        assertTrue(goalControlCapabilityMaySend(true))
        assertEquals(GoalControlCapabilities(), goalControlCapabilitiesReset())
        assertEquals("Goal controls are not available on this router.", goalControlCapabilityError(null, null, blocking = true))
        assertEquals("Goal controls are not available on this router.", goalControlCapabilityError(false, true, blocking = true))
        assertEquals("This router does not support disabling goal blocking.", goalControlCapabilityError(true, null, blocking = false))
        assertEquals("This router does not support disabling goal blocking.", goalControlCapabilityError(true, false, blocking = false))
        assertNull(goalControlCapabilityError(true, true, blocking = false))
        assertNull(goalControlCapabilityError(true, true, blocking = true))
        assertNull(goalControlCapabilityError(true, false, blocking = true))
        assertNull(goalControlCapabilityError(true, null, blocking = true))

        assertTrue(goalControlSendMatches(7, 7, true, "lease", "lease", "worker-instance", "worker-instance"))
        assertFalse(goalControlSendMatches(7, 8, true, "lease", "lease", "worker-instance", "worker-instance"))
        assertFalse(goalControlSendMatches(7, 7, false, "lease", "lease", "worker-instance", "worker-instance"))
        assertFalse(goalControlSendMatches(7, 7, true, "", "lease", "worker-instance", "worker-instance"))
        assertFalse(goalControlSendMatches(7, 7, true, "lease", "replacement-lease", "worker-instance", "worker-instance"))
        assertFalse(goalControlSendMatches(7, 7, true, "lease", "lease", "", "worker-instance"))
        assertFalse(goalControlSendMatches(7, 7, true, "lease", "lease", "worker-instance", "replacement-instance"))

        val pending = GoalControlPending("control", "android", "worker", "worker-instance", "lease", "session", 7, "request")
        assertTrue(goalControlRequestMatchesPending(pending, "request"))
        assertFalse(goalControlRequestMatchesPending(null, "request"))
        assertFalse(goalControlRequestMatchesPending(pending, ""))
        assertFalse(goalControlRequestMatchesPending(pending, "other-request"))
    }

    @Test
    fun goalEditorTargetAndNumericHelpersKeepOnlyValidValues() {
        assertTrue(goalEditorTargetMissing("worker", emptyList()))
        assertFalse(goalEditorTargetMissing("worker", listOf(PeerStatus("worker"))))
        assertFalse(goalEditorTargetMissing("", emptyList()))

        assertEquals(90L, goalSecondsInput("90", 60L))
        assertEquals(60L, goalSecondsInput("0", 60L))
        assertEquals(60L, goalSecondsInput("not-a-number", 60L))
        assertEquals(9_007_199_254_740L, goalSecondsInput("9007199254740", 60L))
        assertEquals(60L, goalSecondsInput("9007199254741", 60L))
        assertEquals(60L, goalSecondsInput((Long.MAX_VALUE / 1_000L).toString(), 60L))

        assertNull(goalOptionalInput("", 12L, 20L))
        assertEquals(20L, goalOptionalInput("20", 12L, 20L))
        assertEquals(12L, goalOptionalInput("0", 12L, 20L))
        assertEquals(12L, goalOptionalInput("21", 12L, 20L))
        assertEquals(12L, goalOptionalInput("not-a-number", 12L, 20L))
        assertEquals(500L, goalOptionalInput("500", null))
        assertFalse(goalStateSettingsAreValid(enabledState.copy(snoozeDurationMs = 9_007_199_254_740_992L)))
        assertFalse(goalStateSettingsAreValid(enabledState.copy(outcomeCounts = GoalOutcomeCounts(completed = 1_000_001))))
        assertEquals(
            "Outcomes: blocked 1, completed 2, not done 3, waiting 4",
            goalOutcomeCountsLabel(enabledState.outcomeCounts),
        )
    }

    @Test
    fun onlyAResultMatchingEveryGoalControlFenceCanReplaceConfirmedStateOrCloseTheEditor() {
        val pending = GoalControlPending(
            controlId = "goal-control-1",
            sourceAgentId = "android",
            targetAgentId = "worker",
            targetInstanceId = "worker-instance",
            sourceLeaseId = "android-lease",
            sourceSessionId = "android-session-1",
            connectionGeneration = 7,
        )
        val connection = GoalControlConnection(
            sourceLeaseId = "android-lease",
            sourceSessionId = "android-session-1",
            connectionGeneration = 7,
            socketIsCurrent = true,
        )
        val confirmed = defaults.copy(enabled = true, goal = "confirmed before save")
        val draft = enabledState.copy(goal = "draft should survive stale results")
        val initial = GoalControlUiState(
            confirmedState = confirmed,
            compactProjection = GoalProjection(true, confirmed.goal),
            draft = draft,
            editorPeer = "worker",
            editorInstanceId = "worker-instance",
            editorVisible = true,
            pending = pending,
            status = "Saving…",
        )
        val matching = goalControlResultJson(enabledState)
        val currentPeer = PeerStatus("worker", instanceId = "worker-instance")

        val accepted = settleGoalControlResult(initial, matching, connection, currentPeer)
        assertEquals(enabledState, accepted.confirmedState)
        assertEquals(GoalProjection(true, enabledState.goal), accepted.compactProjection)
        assertFalse(accepted.editorVisible)
        assertNull(accepted.pending)

        assertTrue(goalControlResultLeaseIsValid("worker-lease"))
        // The result lease belongs to the target. Android did not capture it before sending,
        // so a different nonblank target lease cannot be classified here.
        assertTrue(goalControlResultLeaseIsValid("replacement-worker-lease"))
        assertFalse(goalControlResultLeaseIsValid(""))
        assertFalse(goalControlResultLeaseIsValid("   "))
        assertFalse(goalControlResultLeaseIsValid(42))
        assertFalse(goalControlResultLeaseIsValid(JSONObject.NULL))

        fun assertIgnored(frame: JSONObject = matching, current: GoalControlConnection = connection, peer: PeerStatus? = currentPeer) {
            assertEquals(initial, settleGoalControlResult(initial, frame, current, peer))
        }

        // source, target agent, target instance, and control correlation from the router result.
        assertIgnored(JSONObject(matching.toString()).put("source_agent_id", "other-android"))
        assertIgnored(JSONObject(matching.toString()).put("agent_id", "other-worker"))
        assertIgnored(JSONObject(matching.toString()).put("instance_id", "replacement-instance"))
        assertIgnored(JSONObject(matching.toString()).put("control_id", "other-control"))
        assertIgnored(JSONObject(matching.toString()).put("lease_id", ""))
        assertIgnored(JSONObject(matching.toString()).put("lease_id", 42))
        // The target lease is valid but cannot be compared: the request carries Android's lease.
        val acceptedWithDifferentTargetLease = settleGoalControlResult(
            initial,
            JSONObject(matching.toString()).put("lease_id", "replacement-worker-lease"),
            connection,
            currentPeer,
        )
        assertEquals(enabledState, acceptedWithDifferentTargetLease.confirmedState)
        // Android must still own the captured active lease, session, socket, and connection generation.
        assertIgnored(current = connection.copy(sourceLeaseId = "new-lease"))
        assertIgnored(current = connection.copy(sourceSessionId = "new-session"))
        assertIgnored(current = connection.copy(socketIsCurrent = false))
        assertIgnored(current = connection.copy(connectionGeneration = 8))
        // A discovery replacement fences the old target before its stale result can mutate any editor state.
        assertIgnored(peer = PeerStatus("worker", instanceId = "replacement-instance"))
        // Closing, selection changes, timeouts, disconnects, and lease/session loss clear pending first.
        assertEquals(initial.copy(pending = null), settleGoalControlResult(initial.copy(pending = null), matching, connection, currentPeer))
    }

    @Test
    fun hydrationRequestsAcceptOnlyTheCurrentGenerationAndPeerIdentity() {
        assertTrue(
            goalHydrationRequestMatches(
                requestGeneration = 2L,
                currentGeneration = 2L,
                requestRouter = "router-a",
                currentRouter = "router-a",
                requestAgent = "android",
                currentAgent = "android",
                requestPeer = "worker",
                currentPeer = "worker",
            ),
        )

        fun currentRequest(
            requestRouter: String = "router-a",
            currentRouter: String = "router-a",
            requestAgent: String = "android",
            currentAgent: String = "android",
            requestPeer: String = "worker",
            currentPeer: String = "worker",
        ) = goalHydrationRequestMatches(
            12L,
            12L,
            requestRouter,
            currentRouter,
            requestAgent,
            currentAgent,
            requestPeer,
            currentPeer,
        )
        assertFalse(
            goalHydrationRequestMatches(
                requestGeneration = 1L,
                currentGeneration = 2L,
                requestRouter = "router-a",
                currentRouter = "router-a",
                requestAgent = "android",
                currentAgent = "android",
                requestPeer = "worker",
                currentPeer = "worker",
            ),
        )
        assertFalse(currentRequest(requestRouter = "router-b"))
        assertFalse(currentRequest(requestAgent = "other-android"))
        assertFalse(currentRequest(requestPeer = "other-worker"))
        assertFalse(currentRequest(requestPeer = ""))
        assertFalse(currentRequest(currentPeer = ""))
    }

    @Test
    fun replacementAndFullStateHydrationRequireTheOpenTargetAndItsOriginalInstance() {
        val pending = GoalControlPending("control", "android", "worker", "old-instance", "lease", "session", 7)
        assertTrue(livePeerInstanceWasReplaced("old-instance", "new-instance"))
        assertFalse(livePeerInstanceWasReplaced("", "new-instance"))
        assertFalse(livePeerInstanceWasReplaced("old-instance", ""))
        assertFalse(livePeerInstanceWasReplaced("same-instance", "same-instance"))
        val replacementCleanup = goalControlReplacementCleanup(
            pending = pending,
            editorPeer = "worker",
            editorInstanceId = "old-instance",
            peer = "worker",
            previousInstanceId = "old-instance",
            incomingInstanceId = "new-instance",
        )
        assertTrue(replacementCleanup.clearPending)
        assertTrue(replacementCleanup.clearEditor)
        assertTrue(goalControlShouldClearForPeerReplacement(pending, "", "", "worker", "old-instance", "new-instance"))
        // An open editor also clears when it is the only match.
        assertTrue(goalControlShouldClearForPeerReplacement(null, "worker", "old-instance", "worker", "old-instance", "new-instance"))
        val sameInstanceCleanup = goalControlReplacementCleanup(
            pending = pending,
            editorPeer = "worker",
            editorInstanceId = "old-instance",
            peer = "worker",
            previousInstanceId = "old-instance",
            incomingInstanceId = "old-instance",
        )
        assertFalse(sameInstanceCleanup.clearPending)
        assertFalse(sameInstanceCleanup.clearEditor)
        // State belonging to another peer is preserved during this replacement.
        val unrelatedCleanup = goalControlReplacementCleanup(
            pending = pending.copy(targetAgentId = "other-worker"),
            editorPeer = "other-worker",
            editorInstanceId = "other-instance",
            peer = "worker",
            previousInstanceId = "old-instance",
            incomingInstanceId = "new-instance",
        )
        assertFalse(unrelatedCleanup.clearPending)
        assertFalse(unrelatedCleanup.clearEditor)
        // A replacement with neither a matching pending control nor editor leaves state intact.
        assertFalse(goalControlShouldClearForPeerReplacement(null, "", "", "worker", "old-instance", "new-instance"))
        assertFalse(goalControlShouldClearForPeerReplacement(null, "worker", "other-instance", "worker", "old-instance", "new-instance"))
        // Either matching fence is enough: replacement must clear a matching editor even if its pending control targets another peer.
        assertTrue(goalControlShouldClearForPeerReplacement(pending.copy(targetAgentId = "other-worker"), "worker", "old-instance", "worker", "old-instance", "new-instance"))
        assertFalse(goalControlShouldClearForPeerReplacement(pending, "worker", "old-instance", "other-worker", "old-instance", "new-instance"))
        assertFalse(goalControlShouldClearForPeerReplacement(pending, "worker", "old-instance", "worker", "", "new-instance"))
        assertFalse(goalControlShouldClearForPeerReplacement(pending, "worker", "old-instance", "", "old-instance", "new-instance"))
        assertFalse(goalControlShouldClearForPeerReplacement(pending, "worker", "old-instance", "worker", "old-instance", ""))
        assertFalse(goalControlShouldClearForPeerReplacement(pending, "worker", "old-instance", "worker", "old-instance", "old-instance"))

        assertTrue(goalDraftShouldHydrateFromFullState("worker", "new-instance", false, "worker", "new-instance"))
        assertFalse(goalDraftShouldHydrateFromFullState("worker", "new-instance", true, "worker", "new-instance"))
        assertFalse(goalDraftShouldHydrateFromFullState("", "new-instance", false, "worker", "new-instance"))
        assertFalse(goalDraftShouldHydrateFromFullState("worker", "", false, "worker", "new-instance"))
        assertFalse(goalDraftShouldHydrateFromFullState("worker", "new-instance", false, "", "new-instance"))
        assertFalse(goalDraftShouldHydrateFromFullState("worker", "new-instance", false, "worker", ""))
        assertFalse(goalDraftShouldHydrateFromFullState("worker", "new-instance", false, "other-worker", "new-instance"))
        assertFalse(goalDraftShouldHydrateFromFullState("worker", "new-instance", false, "worker", "old-instance"))
    }

    @Test
    fun rejectedAndTimedOutControlsKeepTheDraftWhileReplacementAndStaleResultsChangeNothing() {
        val pending = GoalControlPending("goal-control-1", "android", "worker", "worker-instance", "android-lease", "session-1", 7)
        val connection = GoalControlConnection("android-lease", "session-1", 7, socketIsCurrent = true)
        val initial = GoalControlUiState(
            confirmedState = defaults,
            compactProjection = GoalProjection(false, ""),
            draft = enabledState,
            editorPeer = "worker",
            editorInstanceId = "worker-instance",
            editorVisible = true,
            pending = pending,
            status = "Saving…",
        )
        val peer = PeerStatus("worker", instanceId = "worker-instance")
        val rejected = goalControlResultJson(defaults, accepted = false, errorCode = "invalid_settings")

        assertEquals(
            initial.copy(pending = null, status = "Check the goal settings and try again."),
            settleGoalControlResult(initial, rejected, connection, peer),
        )
        assertEquals(
            initial.copy(pending = null, status = "The goal change timed out."),
            timeoutGoalControl(initial),
        )
        assertTrue(timeoutGoalControl(initial).editorVisible)
        assertEquals(enabledState, timeoutGoalControl(initial).draft)
        val noPending = initial.copy(pending = null, status = "Already settled")
        assertEquals(noPending, timeoutGoalControl(noPending))
    }

    private fun assertSettings(settings: JSONObject, state: GoalState) {
        assertEquals(state.blocking, settings.getBoolean("blocking"))
        assertEquals(state.goalLockEnabled, settings.getBoolean("goal_lock_enabled"))
        assertEquals(state.snoozeEnabled, settings.getBoolean("snooze_enabled"))
        assertEquals(state.snoozeDurationMs, settings.getLong("snooze_duration_ms"))
        assertEquals(state.snoozeTurnLimit, settings.nullableInt("snooze_turn_limit"))
        assertEquals(state.snoozeBudgetCapMs, settings.nullableLong("snooze_budget_cap_ms"))
        assertEquals(state.snoozeRefillYieldMs, settings.getLong("snooze_refill_yield_ms"))
        assertEquals(state.snoozeRefillElapsedMs, settings.getLong("snooze_refill_elapsed_ms"))
    }

    private fun goalStateJson(state: GoalState) = JSONObject()
        .put("enabled", state.enabled)
        .put("goal", state.goal)
        .put("blocking", state.blocking)
        .put("goal_lock_enabled", state.goalLockEnabled)
        .put("snooze_enabled", state.snoozeEnabled)
        .put("snooze_duration_ms", state.snoozeDurationMs)
        .put("snooze_turn_limit", state.snoozeTurnLimit ?: JSONObject.NULL)
        .put("snooze_budget_cap_ms", state.snoozeBudgetCapMs ?: JSONObject.NULL)
        .put("snooze_refill_yield_ms", state.snoozeRefillYieldMs)
        .put("snooze_refill_elapsed_ms", state.snoozeRefillElapsedMs)
        .put(
            "outcome_counts",
            JSONObject()
                .put("BLOCKED", state.outcomeCounts.blocked)
                .put("COMPLETED", state.outcomeCounts.completed)
                .put("NOT_DONE", state.outcomeCounts.notDone)
                .put("WAITING_FOR_USER_AUTHORIZATION", state.outcomeCounts.waitingForUserAuthorization),
        )

    private fun goalControlResultJson(
        state: GoalState,
        accepted: Boolean = true,
        errorCode: String? = null,
        leaseId: String = "worker-lease",
    ) = JSONObject()
        .put("type", "goal_control_result")
        .put("source_agent_id", "android")
        .put("agent_id", "worker")
        .put("instance_id", "worker-instance")
        .put("lease_id", leaseId)
        .put("control_id", "goal-control-1")
        .put("revision", 1)
        .put("accepted", accepted)
        .apply { if (errorCode != null) put("error_code", errorCode) }
        .put("goal_state", goalStateJson(state))

    private fun JSONObject.nullableInt(key: String): Int? = if (isNull(key)) null else getInt(key)
    private fun JSONObject.nullableLong(key: String): Long? = if (isNull(key)) null else getLong(key)
}
