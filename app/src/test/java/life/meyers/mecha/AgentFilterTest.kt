package life.meyers.mecha

import androidx.compose.ui.graphics.Color
import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.Instant

class AgentFilterTest {
    @Test
    fun staleScheduledReconnectCannotTransitionAfterNewerManualGeneration() {
        val scheduledGeneration = 7
        val currentGenerationAfterManualReconnect = scheduledGeneration + 1

        assertTrue(scheduledReconnectIsCurrent(scheduledGeneration, scheduledGeneration))
        assertFalse(scheduledReconnectIsCurrent(scheduledGeneration, currentGenerationAfterManualReconnect))
    }

    @Test
    fun deliveryGenerationMatchesCurrentSocketAndLease() {
        assertTrue(
            deliveryGenerationMatches(
                7,
                7,
                socketIsCurrent = true,
                capturedLeaseId = "lease",
                currentLeaseId = "lease",
            ),
        )
    }

    @Test
    fun deliveryGenerationMatchesRejectsStaleGenerationSocketOrLease() {
        assertFalse(
            deliveryGenerationMatches(
                6,
                7,
                socketIsCurrent = true,
                capturedLeaseId = "lease",
                currentLeaseId = "lease",
            ),
        )
        assertFalse(
            deliveryGenerationMatches(
                7,
                7,
                socketIsCurrent = false,
                capturedLeaseId = "lease",
                currentLeaseId = "lease",
            ),
        )
        assertFalse(
            deliveryGenerationMatches(
                7,
                7,
                socketIsCurrent = true,
                capturedLeaseId = "old-lease",
                currentLeaseId = "new-lease",
            ),
        )
    }

    @Test
    fun deliveryGenerationMatchesRejectsBlankLease() {
        assertFalse(
            deliveryGenerationMatches(
                7,
                7,
                socketIsCurrent = true,
                capturedLeaseId = "",
                currentLeaseId = "lease",
            ),
        )
    }

    @Test
    fun deliveryAckRequiresSuccessfulInjectionAndCurrentLeaseAtFinalCheck() {
        assertFalse(
            shouldSendDeliveryAck(
                false,
                7,
                7,
                socketIsCurrent = true,
                capturedLeaseId = "lease",
                currentLeaseId = "lease",
            ),
        )
        assertTrue(
            shouldSendDeliveryAck(
                true,
                7,
                7,
                socketIsCurrent = true,
                capturedLeaseId = "lease",
                currentLeaseId = "lease",
            ),
        )
        assertFalse(
            shouldSendDeliveryAck(
                true,
                6,
                7,
                socketIsCurrent = true,
                capturedLeaseId = "lease",
                currentLeaseId = "lease",
            ),
        )
        assertFalse(
            shouldSendDeliveryAck(
                true,
                7,
                7,
                socketIsCurrent = true,
                capturedLeaseId = "lease",
                currentLeaseId = "",
            ),
        )
        assertFalse(
            shouldSendDeliveryAck(
                true,
                7,
                7,
                socketIsCurrent = true,
                capturedLeaseId = "old-lease",
                currentLeaseId = "new-lease",
            ),
        )
    }

    @Test
    fun leaseRenewalRequiresCurrentSocketGenerationAndLease() {
        assertTrue(leaseRenewalCanSend(7, 7, socketIsCurrent = true, leaseId = "lease"))
        assertFalse(leaseRenewalCanSend(6, 7, socketIsCurrent = true, leaseId = "lease"))
        assertFalse(leaseRenewalCanSend(7, 7, socketIsCurrent = false, leaseId = "lease"))
        assertFalse(leaseRenewalCanSend(7, 7, socketIsCurrent = true, leaseId = ""))
    }

    @Test
    fun modelControlSendRequiresCurrentGenerationSocketLeaseAndTargetInstance() {
        assertTrue(modelControlSendMatches(7, 7, true, "lease", "lease", "target-1", "target-1"))
        assertFalse(modelControlSendMatches(6, 7, true, "lease", "lease", "target-1", "target-1"))
        assertFalse(modelControlSendMatches(7, 7, false, "lease", "lease", "target-1", "target-1"))
        assertFalse(modelControlSendMatches(7, 7, true, "old-lease", "new-lease", "target-1", "target-1"))
        assertFalse(modelControlSendMatches(7, 7, true, "lease", "lease", "old-target", "new-target"))
        assertFalse(modelControlSendMatches(7, 7, true, "", "lease", "target-1", "target-1"))
        assertFalse(modelControlSendMatches(7, 7, true, "lease", "lease", "", "target-1"))
    }

    @Test
    fun visibleConnectedPeersKeepsAllPeersForBlankFilter() {
        val peers = listOf("supervisor", "android-qol-parity", "wolfgang.web").map(::PeerStatus)

        assertEquals(peers, visibleConnectedPeers(peers, "   "))
    }

    @Test
    fun visibleConnectedPeersMatchesRawIdsAndHumanNamesCaseInsensitively() {
        val peers = listOf("supervisor", "android-qol-parity", "wolfgang.web", "note_agent").map(::PeerStatus)

        assertEquals(listOf("android-qol-parity"), visibleConnectedPeers(peers, "qol").map { it.id })
        assertEquals(listOf("android-qol-parity"), visibleConnectedPeers(peers, "QOL PARITY").map { it.id })
        assertEquals(listOf("wolfgang.web"), visibleConnectedPeers(peers, "wolfgang web").map { it.id })
        assertEquals(emptyList<String>(), visibleConnectedPeers(peers, "missing").map { it.id })
    }

    @Test
    fun retiredInstanceFenceAcceptsFreshReplacementAndRejectsDelayedOldFrame() {
        val fence = RetiredPeerInstanceTombstones(maxPeers = 4, maxInstancesPerPeer = 4)

        assertEquals(PeerInstanceDecision.REPLACEMENT, fence.observe("worker", "instance-A", "instance-B"))
        assertEquals(PeerInstanceDecision.ACCEPT, fence.observe("worker", "instance-B", "instance-B"))
        assertEquals(PeerInstanceDecision.REJECT, fence.observe("worker", "instance-B", "instance-A"))
    }

    @Test
    fun retiredInstanceFenceClearsOnPeerRemovalAndRouterReset() {
        val fence = RetiredPeerInstanceTombstones(maxPeers = 4, maxInstancesPerPeer = 4)
        fence.observe("worker", "instance-A", "instance-B")
        assertEquals(1, fence.tombstoneCount())

        fence.clearPeer("worker")
        assertEquals(0, fence.tombstoneCount())
        assertEquals(PeerInstanceDecision.REPLACEMENT, fence.observe("worker", "instance-B", "instance-A"))

        fence.clear()
        assertEquals(0, fence.tombstoneCount())
        assertEquals(0, fence.peerCount())
    }

    @Test
    fun retiredInstanceFencePrunesPerPeerTombstones() {
        val fence = RetiredPeerInstanceTombstones(maxPeers = 2, maxInstancesPerPeer = 2)
        fence.observe("worker", "A", "B")
        fence.observe("worker", "B", "C")
        fence.observe("worker", "C", "D")

        assertEquals(2, fence.tombstoneCount())
        assertEquals(1, fence.peerCount())
        assertEquals(PeerInstanceDecision.REPLACEMENT, fence.observe("worker", "D", "A"))
    }

    @Test
    fun livePeerInstanceAcceptsBlankCurrentInstance() {
        assertTrue(acceptsLivePeerInstance("", ""))
    }

    @Test
    fun livePeerInstanceAcceptsMatchingInstance() {
        assertTrue(acceptsLivePeerInstance("current-instance", "current-instance"))
    }

    @Test
    fun livePeerInstanceRejectsMissingIncomingInstance() {
        assertFalse(acceptsLivePeerInstance("current-instance", ""))
    }

    @Test
    fun livePeerInstanceRejectsMismatchedIncomingInstance() {
        assertFalse(acceptsLivePeerInstance("current-instance", "other-instance"))
    }

    @Test
    fun goalProjectionAcceptsEnabledAndExplicitOffStates() {
        assertEquals(
            GoalProjection(enabled = true, goal = "ship it"),
            normalizeGoalProjection(JSONObject().put("enabled", true).put("goal", "ship it")),
        )
        assertEquals(
            GoalProjection(enabled = false, goal = ""),
            normalizeGoalProjection(JSONObject().put("enabled", false).put("goal", "")),
        )
        assertNull(normalizeGoalProjection(JSONObject().put("enabled", true).put("goal", "")))
        assertNull(normalizeGoalProjection(JSONObject().put("enabled", false).put("goal", "stale")))
    }

    @Test
    fun peerProjectionCarriesCompactingGoalAndModelsWithoutInventingCatalogEntries() {
        val peer = peerStatusFromJson(
            "worker",
            JSONObject()
                .put("activity_state", "compacting")
                .put("goal", JSONObject().put("enabled", true).put("goal", "ship it"))
                .put("current_model", JSONObject().put("provider", "openai").put("id", "gpt"))
                .put("available_models", JSONArray().put(JSONObject().put("provider", "openai").put("id", "gpt")))
                .put("model_catalog_total_count", 2)
                .put("model_catalog_truncated", true),
        )

        assertEquals("compacting", peer.activityState)
        assertEquals(GoalProjection(true, "ship it"), peer.goal)
        assertEquals(ModelSummary("openai", "gpt"), peer.currentModel)
        assertEquals(listOf(ModelSummary("openai", "gpt")), peer.availableModels)
        assertEquals(2, peer.modelCatalogTotalCount)
        assertEquals(true, peer.modelCatalogTruncated)
    }

    @Test
    fun peerProjectionHydratesFullGoalStateAndDerivesItsCompactProjection() {
        val peer = peerStatusFromJson(
            "worker",
            JSONObject()
                .put("instance_id", "worker-instance")
                .put(
                    "goal_state",
                    JSONObject()
                        .put("enabled", true)
                        .put("goal", "Complete the Android parity work")
                        .put("blocking", false)
                        .put("goal_lock_enabled", true)
                        .put("snooze_enabled", false)
                        .put("snooze_duration_ms", 90_000)
                        .put("snooze_turn_limit", 12)
                        .put("snooze_budget_cap_ms", 600_000)
                        .put("snooze_refill_yield_ms", 45_000)
                        .put("snooze_refill_elapsed_ms", 120_000)
                        .put(
                            "outcome_counts",
                            JSONObject()
                                .put("BLOCKED", 1)
                                .put("COMPLETED", 2)
                                .put("NOT_DONE", 3)
                                .put("WAITING_FOR_USER_AUTHORIZATION", 4),
                        ),
                ),
        )

        val fullState = GoalState(
            enabled = true,
            goal = "Complete the Android parity work",
            blocking = false,
            goalLockEnabled = true,
            snoozeEnabled = false,
            snoozeDurationMs = 90_000,
            snoozeTurnLimit = 12,
            snoozeBudgetCapMs = 600_000,
            snoozeRefillYieldMs = 45_000,
            snoozeRefillElapsedMs = 120_000,
            outcomeCounts = GoalOutcomeCounts(1, 2, 3, 4),
        )
        assertEquals(fullState, peer.goalState)
        assertEquals(GoalProjection(true, fullState.goal), peer.goal)
        assertEquals(1, peer.goalState?.outcomeCounts?.blocked)
        assertEquals(2, peer.goalState?.outcomeCounts?.completed)
        assertEquals(3, peer.goalState?.outcomeCounts?.notDone)
        assertEquals(4, peer.goalState?.outcomeCounts?.waitingForUserAuthorization)
    }

    @Test
    fun emptyModelCatalogFrameIsDeferredUntilLiveOrSnapshotCatalogIsAvailable() {
        val currentModel = ModelSummary("openai", "current-model")
        val current = PeerStatus(
            id = "worker",
            instanceId = "worker-instance",
            currentModel = currentModel,
            availableModels = listOf(currentModel),
            modelCatalogTotalCount = 1,
        )
        val emptyLiveFrame = JSONObject()
            .put("instance_id", "worker-instance")
            .put("model_summaries", JSONArray())

        assertFalse(shouldApplyModelCatalogFrame(JSONObject().put("activity_state", "active")))
        assertTrue(shouldApplyModelCatalogFrame(emptyLiveFrame))
        assertNull(
            modelCatalogProjection(
                "worker",
                current,
                emptyLiveFrame,
                pendingControlId = "control-1",
                pendingControlPeer = "worker",
                pendingControlInstanceId = "worker-instance",
            ),
        )
        val settledProjection = modelCatalogProjection("worker", current, emptyLiveFrame)
        assertEquals(emptyList<ModelSummary>(), settledProjection?.availableModels)
        assertEquals(listOf(currentModel), current.availableModels)

        val liveModel = ModelSummary("openai", "live-model")
        val liveProjection = modelCatalogProjection(
            "worker",
            current,
            JSONObject()
                .put("instance_id", "worker-instance")
                .put("model_summaries", JSONArray().put(JSONObject().put("provider", liveModel.provider).put("id", liveModel.id))),
        )
        assertEquals(listOf(liveModel), liveProjection?.availableModels)

        val snapshotModel = ModelSummary("anthropic", "snapshot-model")
        val snapshotProjection = mergeDiscoveredPeer(
            "worker",
            JSONObject()
                .put("instance_id", "worker-instance")
                .put("available_models", JSONArray().put(JSONObject().put("provider", snapshotModel.provider).put("id", snapshotModel.id))),
            settledProjection,
            hasLiveUpdate = false,
        )
        assertEquals(listOf(snapshotModel), snapshotProjection.availableModels)
    }

    @Test
    fun deferredCatalogFromAIsNotAppliedToDiscoveryFromB() {
        val currentModel = ModelSummary("openai", "current")
        val current = PeerStatus(
            "worker",
            instanceId = "instance-A",
            availableModels = listOf(currentModel),
            modelCatalogTotalCount = 1,
        )
        val deferredFromA = JSONObject()
            .put("instance_id", "instance-A")
            .put("model_summaries", JSONArray())
        val discoveryFromA = JSONObject()
            .put("instance_id", "instance-A")
            .put("model_summaries", JSONArray().put(JSONObject().put("provider", "anthropic").put("id", "from-A")))
        val discoveryFromB = JSONObject()
            .put("instance_id", "instance-B")
            .put("model_summaries", JSONArray().put(JSONObject().put("provider", "anthropic").put("id", "from-B")))

        val sameInstance = mergeDiscoveredPeer(
            "worker",
            discoveryFromA,
            current,
            hasLiveUpdate = false,
            deferredModelCatalogFrame = deferredFromA,
            pendingControlId = "control-1",
            pendingControlPeer = "worker",
            pendingControlInstanceId = "instance-A",
        )
        assertEquals(listOf(currentModel), sameInstance.availableModels)

        val differentInstance = mergeDiscoveredPeer(
            "worker",
            discoveryFromB,
            current,
            hasLiveUpdate = false,
            deferredModelCatalogFrame = deferredFromA,
            pendingControlId = "control-1",
            pendingControlPeer = "worker",
            pendingControlInstanceId = "instance-B",
        )
        assertEquals(listOf(ModelSummary("anthropic", "from-B")), differentInstance.availableModels)
        assertEquals("instance-B", differentInstance.instanceId)
    }

    @Test
    fun catalogBoundaryUsesValidPrecedenceAndNeverClearsOnMalformedInput() {
        val available = JSONObject().put("provider", "openai").put("id", "available")
        val summary = JSONObject().put("provider", "anthropic").put("id", "summary")
        val current = PeerStatus("worker", availableModels = listOf(ModelSummary("old", "model")))

        assertEquals(listOf(ModelSummary("openai", "available")), modelCatalogProjection("worker", current,
            JSONObject().put("available_models", JSONArray().put(available)).put("model_summaries", JSONArray().put(summary)))?.availableModels)
        assertEquals(listOf(ModelSummary("anthropic", "summary")), modelCatalogProjection("worker", current,
            JSONObject().put("available_models", JSONArray()).put("model_summaries", JSONArray().put(summary)))?.availableModels)
        assertNull(modelCatalogProjection("worker", current, JSONObject().put("available_models", "bad")))
        assertNull(modelCatalogProjection("worker", current, JSONObject().put("available_models", "bad").put("model_summaries", JSONArray())))
        assertNull(modelCatalogProjection("worker", current, JSONObject().put("available_models", JSONObject.NULL)))
        assertNull(modelCatalogProjection("worker", current, JSONObject().put("available_models", JSONObject.NULL).put("model_summaries", JSONArray())))
        assertNull(modelCatalogProjection("worker", current, JSONObject().put("available_models", JSONArray().put(JSONObject.NULL))))
        assertNull(modelCatalogProjection("worker", current, JSONObject().put("available_models", JSONArray().put(JSONObject().put("provider", "openai")))))
        assertNull(modelCatalogProjection("worker", current, JSONObject().put("model_summaries", JSONArray().put("invalid"))))
        assertNull(modelCatalogProjection("worker", current, JSONObject().put("model_summaries", "bad")))
        assertNull(modelCatalogProjection("worker", current, JSONObject().put("model_summaries", JSONObject.NULL)))
        assertEquals(listOf(ModelSummary("anthropic", "summary")), modelCatalogProjection("worker", current,
            JSONObject().put("model_summaries", JSONArray().put(summary)))?.availableModels)
        assertEquals(listOf(ModelSummary("old", "model")), peerStatusFromJson("worker", JSONObject().put("available_models", "bad"), current).availableModels)
    }

    @Test
    fun emptyCatalogClearsAndLiveStateBlocksStaleDiscovery() {
        val current = PeerStatus(
            "worker",
            instanceId = "worker-instance",
            availableModels = listOf(ModelSummary("openai", "old")),
            modelCatalogTotalCount = 1,
        )
        val emptyLiveFrame = JSONObject()
            .put("instance_id", "worker-instance")
            .put("available_models", JSONArray())
            .put("model_summaries", JSONArray())
        val cleared = peerStatusFromJson("worker", emptyLiveFrame, current)
        assertEquals(emptyList<ModelSummary>(), cleared.availableModels)

        val staleDiscovery = JSONObject()
            .put("instance_id", "worker-instance")
            .put("model_summaries", JSONArray().put(JSONObject().put("provider", "openai").put("id", "stale")))
        val protected = mergeDiscoveredPeer("worker", staleDiscovery, cleared, hasLiveUpdate = true)
        assertEquals(emptyList<ModelSummary>(), protected.availableModels)

        val restored = mergeDiscoveredPeer("worker", staleDiscovery, cleared, hasLiveUpdate = false)
        assertEquals(listOf(ModelSummary("openai", "stale")), restored.availableModels)
    }

    @Test
    fun pendingControlDefersEmptyDiscoveryCatalogUntilControlSettles() {
        val currentModel = ModelSummary("openai", "current")
        val current = PeerStatus(
            "worker",
            instanceId = "worker-instance",
            currentModel = currentModel,
            availableModels = listOf(currentModel),
            modelCatalogTotalCount = 1,
            modelCatalogTruncated = true,
        )
        val emptySnapshot = JSONObject()
            .put("instance_id", "worker-instance")
            .put("available_models", JSONArray())
            .put("model_summaries", JSONArray())

        assertTrue(modelCatalogUpdateMatchesPending("worker", emptySnapshot, "control-1", "worker", "worker-instance"))
        assertFalse(modelCatalogUpdateMatchesPending("other", emptySnapshot, "control-1", "worker", "worker-instance"))
        assertFalse(
            modelCatalogUpdateMatchesPending(
                "worker",
                JSONObject(emptySnapshot.toString()).put("instance_id", "other-instance"),
                "control-1",
                "worker",
                "worker-instance",
            ),
        )

        val deferred = mergeDiscoveredPeer(
            "worker",
            emptySnapshot,
            current,
            hasLiveUpdate = false,
            pendingControlId = "control-1",
            pendingControlPeer = "worker",
            pendingControlInstanceId = "worker-instance",
        )

        assertEquals(listOf(currentModel), deferred.availableModels)
        assertEquals(1, deferred.modelCatalogTotalCount)
        assertTrue(deferred.modelCatalogTruncated)
        val settled = modelCatalogProjection("worker", deferred, emptySnapshot)
        assertEquals(emptyList<ModelSummary>(), settled?.availableModels)
    }

    @Test
    fun matchingDeferredLiveEmptyCatalogWinsOverPendingDiscoveryUntilSettlement() {
        val currentModel = ModelSummary("openai", "current")
        val current = PeerStatus(
            "worker",
            instanceId = "worker-instance",
            availableModels = listOf(currentModel),
            modelCatalogTotalCount = 1,
        )
        val emptyLiveFrame = JSONObject()
            .put("instance_id", "worker-instance")
            .put("model_summaries", JSONArray())
        val nonemptyDiscovery = JSONObject()
            .put("instance_id", "worker-instance")
            .put("model_summaries", JSONArray().put(JSONObject().put("provider", "anthropic").put("id", "discovered")))

        val pendingProjection = mergeDiscoveredPeer(
            "worker",
            nonemptyDiscovery,
            current,
            hasLiveUpdate = false,
            deferredModelCatalogFrame = emptyLiveFrame,
            pendingControlId = "control-1",
            pendingControlPeer = "worker",
            pendingControlInstanceId = "worker-instance",
        )
        assertEquals(listOf(currentModel), pendingProjection.availableModels)

        val settledProjection = modelCatalogProjection("worker", pendingProjection, emptyLiveFrame)
        assertEquals(emptyList<ModelSummary>(), settledProjection?.availableModels)

        val subsequentProjection = mergeDiscoveredPeer(
            "worker",
            nonemptyDiscovery,
            settledProjection,
            hasLiveUpdate = false,
        )
        assertEquals(listOf(ModelSummary("anthropic", "discovered")), subsequentProjection.availableModels)
    }

    @Test
    fun discoveredPeerPreservesLiveStateButParsesInitialSnapshot() {
        val current = PeerStatus(
            id = "worker",
            instanceId = "current-instance",
            activityState = "active",
            goal = GoalProjection(true, "current goal"),
            currentModel = ModelSummary("openai", "current-model"),
        )
        val snapshot = JSONObject()
            .put("activity_state", "idle")
            .put("goal", JSONObject().put("enabled", false).put("goal", ""))
            .put("current_model", JSONObject().put("provider", "openai").put("id", "old-model"))

        assertEquals(current, mergeDiscoveredPeer("worker", snapshot, current, hasLiveUpdate = true))
        val differentInstanceSnapshot = JSONObject(snapshot.toString()).put("instance_id", "new-instance")
        assertEquals("idle", mergeDiscoveredPeer("worker", differentInstanceSnapshot, current, hasLiveUpdate = true).activityState)
        assertEquals("idle", mergeDiscoveredPeer("worker", snapshot, current, hasLiveUpdate = false).activityState)
        assertEquals("old-model", mergeDiscoveredPeer("worker", snapshot, current, hasLiveUpdate = false).currentModel?.id)
    }

    @Test
    fun goalDialogClosesWhenDiscoveryRemovesEnabledGoal() {
        assertTrue(goalDialogShouldClose("worker", listOf(PeerStatus("worker"))))
        assertFalse(goalDialogShouldClose("worker", listOf(PeerStatus("worker", goal = GoalProjection(true, "ship it")))))
        assertFalse(goalDialogShouldClose("", emptyList()))
    }

    @Test
    fun staleInstanceProjectionDropsPreviousRuntimeState() {
        val previous = peerStatusFromJson(
            "worker",
            JSONObject()
                .put("instance_id", "old-instance")
                .put("activity_state", "active")
                .put("subagent_count", 1)
                .put("subagents", JSONArray().put(JSONObject().put("id", "child").put("description", "old work")))
                .put("goal", JSONObject().put("enabled", true).put("goal", "old goal"))
                .put("current_model", JSONObject().put("provider", "openai").put("id", "old-model"))
                .put("available_models", JSONArray().put(JSONObject().put("provider", "openai").put("id", "old-model")))
                .put("model_catalog_total_count", 2)
                .put("model_catalog_truncated", true),
        )

        val refreshed = peerStatusFromJson(
            "worker",
            JSONObject().put("instance_id", "new-instance").put("activity_state", "idle"),
            previous,
        )

        assertEquals("new-instance", refreshed.instanceId)
        assertEquals("idle", refreshed.activityState)
        assertEquals(0, refreshed.subagentCount)
        assertEquals(emptyList<SubagentSummary>(), refreshed.subagents)
        assertNull(refreshed.goal)
        assertNull(refreshed.currentModel)
        assertEquals(emptyList<ModelSummary>(), refreshed.availableModels)
        assertEquals(0, refreshed.modelCatalogTotalCount)
        assertFalse(refreshed.modelCatalogTruncated)
    }

    @Test
    fun replacementInstanceDoesNotInheritFullGoalStateModelOrSubagents() {
        val previous = peerStatusFromJson(
            "worker",
            JSONObject()
                .put("instance_id", "old-instance")
                .put("subagent_count", 1)
                .put("subagents", JSONArray().put(JSONObject().put("id", "old-child").put("description", "old work")))
                .put("current_model", JSONObject().put("provider", "openai").put("id", "old-model"))
                .put(
                    "goal_state",
                    JSONObject()
                        .put("enabled", true)
                        .put("goal", "old goal")
                        .put("blocking", false)
                        .put("goal_lock_enabled", true)
                        .put("snooze_enabled", false)
                        .put("snooze_duration_ms", 90_000)
                        .put("snooze_turn_limit", 12)
                        .put("snooze_budget_cap_ms", 600_000)
                        .put("snooze_refill_yield_ms", 45_000)
                        .put("snooze_refill_elapsed_ms", 120_000)
                        .put("outcome_counts", JSONObject().put("BLOCKED", 1).put("COMPLETED", 2).put("NOT_DONE", 3).put("WAITING_FOR_USER_AUTHORIZATION", 4)),
                ),
        )

        val replacement = peerStatusFromJson(
            "worker",
            JSONObject().put("instance_id", "new-instance"),
            previous,
        )

        assertEquals("new-instance", replacement.instanceId)
        assertNull(replacement.goalState)
        assertNull(replacement.goal)
        assertNull(replacement.currentModel)
        assertEquals(0, replacement.subagentCount)
        assertEquals(emptyList<SubagentSummary>(), replacement.subagents)
    }

    @Test
    fun staleModelControlResultIsRejectedAfterDiscoveryReplacement() {
        val oldPeer = PeerStatus("worker", instanceId = "old-instance")
        val replacement = mergeDiscoveredPeer(
            "worker",
            JSONObject().put("instance_id", "new-instance"),
            oldPeer,
            hasLiveUpdate = true,
        )
        val staleResult = JSONObject()
            .put("control_id", "control-1")
            .put("agent_id", "worker")
            .put("source_agent_id", "android")
            .put("instance_id", "old-instance")
            .put("accepted", true)
            .put("current_model", JSONObject().put("provider", "openai").put("id", "gpt"))
        val currentResult = JSONObject(staleResult.toString()).put("instance_id", "new-instance")

        assertEquals("new-instance", replacement.instanceId)
        assertTrue(modelControlResultMatchesPending(staleResult, "control-1", "worker", "android", "old-instance"))
        assertFalse(modelControlResultTargetsCurrentPeer(listOf(replacement), "worker", "old-instance"))
        assertTrue(modelControlResultTargetsCurrentPeer(listOf(replacement), "worker", "new-instance"))
        assertNull(
            modelControlResultSettlement(
                staleResult,
                "control-1",
                "worker",
                "android",
                "old-instance",
                replacement,
            ),
        )
        assertEquals(
            true,
            modelControlResultSettlement(
                currentResult,
                "control-1",
                "worker",
                "android",
                "new-instance",
                replacement,
            )?.accepted,
        )
        assertTrue(modelControlResultConfirmed(currentResult, "control-1", "worker", "android", "new-instance"))
    }

    @Test
    fun compactingProjectionPreservesSameInstanceStateAndSetsCompactingFlag() {
        val previous = peerStatusFromJson(
            "worker",
            JSONObject()
                .put("instance_id", "same-instance")
                .put("activity_state", "active")
                .put("subagent_count", 1)
                .put("subagents", JSONArray().put(JSONObject().put("id", "child").put("description", "current work")))
                .put("goal", JSONObject().put("enabled", true).put("goal", "current goal"))
                .put("current_model", JSONObject().put("provider", "openai").put("id", "current-model"))
                .put("available_models", JSONArray().put(JSONObject().put("provider", "openai").put("id", "current-model")))
                .put("model_catalog_total_count", 1)
                .put("model_catalog_truncated", false),
        )

        val compacting = peerStatusFromJson(
            "worker",
            JSONObject().put("instance_id", "same-instance").put("activity_state", "compacting"),
            previous,
        )

        assertTrue(compacting.compacting)
        assertFalse(compacting.active)
        assertEquals(previous.subagents, compacting.subagents)
        assertEquals(previous.goal, compacting.goal)
        assertEquals(previous.currentModel, compacting.currentModel)
        assertEquals(previous.availableModels, compacting.availableModels)
        assertEquals(previous.modelCatalogTotalCount, compacting.modelCatalogTotalCount)
    }

    @Test
    fun modelControlRequestMatchesPendingRequiresNonblankMatchingIds() {
        assertFalse(modelControlRequestMatchesPending("", ""))
        assertFalse(modelControlRequestMatchesPending("pending-1", ""))
        assertFalse(modelControlRequestMatchesPending("", "request-1"))
        assertTrue(modelControlRequestMatchesPending("request-1", "request-1"))
        assertFalse(modelControlRequestMatchesPending("pending-1", "request-1"))
    }

    @Test
    fun modelControlCorrelationRequiresTheExpectedControlSourceTargetAndInstance() {
        val expected = JSONObject()
            .put("control_id", "control-1")
            .put("agent_id", "worker")
            .put("source_agent_id", "android")
            .put("instance_id", "worker-instance")

        assertTrue(modelControlResultMatchesPending(expected, "control-1", "worker", "android", "worker-instance"))
        assertFalse(
            modelControlResultMatchesPending(
                JSONObject()
                    .put("control_id", "")
                    .put("agent_id", "")
                    .put("source_agent_id", "")
                    .put("instance_id", ""),
                "",
                "",
                "",
                "",
            ),
        )
        listOf("control_id", "agent_id", "source_agent_id", "instance_id").forEach { field ->
            assertFalse(
                "wrong $field must be ignored",
                modelControlResultMatchesPending(expected.put(field, "wrong"), "control-1", "worker", "android", "worker-instance"),
            )
            expected.put(field, when (field) {
                "control_id" -> "control-1"
                "agent_id" -> "worker"
                "source_agent_id" -> "android"
                else -> "worker-instance"
            })
        }
    }

    @Test
    fun modelControlSuccessRequiresAcceptedResultAndValidCurrentModel() {
        val validModel = normalizeModelSummary(JSONObject().put("provider", "openai").put("id", "gpt"))
        val invalidModel = normalizeModelSummary(JSONObject().put("provider", "openai"))
        val correlated = JSONObject()
            .put("control_id", "control-1")
            .put("agent_id", "worker")
            .put("source_agent_id", "android")
            .put("instance_id", "worker-instance")
            .put("accepted", true)
            .put("current_model", JSONObject().put("provider", "openai").put("id", "gpt"))

        assertTrue(modelControlConfirmed(true, validModel))
        assertTrue(modelControlResultConfirmed(correlated, "control-1", "worker", "android", "worker-instance"))
        assertFalse(modelControlConfirmed(true, invalidModel))
        assertFalse(modelControlConfirmed(true, null))
        assertFalse(modelControlConfirmed(false, validModel))
        assertFalse(modelControlResultConfirmed(JSONObject(correlated.toString()).put("accepted", false), "control-1", "worker", "android", "worker-instance"))
        assertFalse(modelControlResultConfirmed(JSONObject(correlated.toString()).put("current_model", JSONObject().put("provider", "openai")), "control-1", "worker", "android", "worker-instance"))
    }

    @Test
    fun reminderDialogTimerTokenRejectsOldPeerSessionFromTickingOrCancellingNewDialog() {
        val oldPeerSession = 11L
        val newerPeerSession = 12L

        assertTrue(reminderDialogCallbackMatchesSession(newerPeerSession, newerPeerSession))
        assertFalse(reminderDialogCallbackMatchesSession(oldPeerSession, newerPeerSession))
        assertFalse(reminderDialogCallbackMatchesSession(oldPeerSession, newerPeerSession + 1))
    }

    @Test
    fun activeReminderProjectionFiltersByPeerAndState() {
        val reminders = listOf(
            ReminderSummary("active", "worker", "Active", "body", "2030-01-01T00:00:00.000Z", null, "scheduled"),
            ReminderSummary("queued", "worker", "Queued", "body", "2030-01-01T00:00:00.000Z", 60_000, "message_queued"),
            ReminderSummary("failed", "worker", "Failed", "body", "2030-01-01T00:00:00.000Z", null, "failed"),
            ReminderSummary("other", "other", "Other", "body", "2030-01-01T00:00:00.000Z", null, "scheduled"),
        )

        assertEquals(listOf("active", "queued"), activeRemindersForPeer(reminders, "worker").map { it.id })
    }

    @Test
    fun reminderRefreshPreservesFailedResultAndReplacesSuccessfulPayload() {
        val existing = listOf(ReminderSummary("existing", "worker", "Existing", "", "", null, "scheduled"))
        val replacement = ReminderSummary("replacement", "worker", "Replacement", "", "", null, "due")
        val payload = JSONArray().put(
            JSONObject()
                .put("id", replacement.id)
                .put("target", replacement.target)
                .put("subject", replacement.subject)
                .put("state", replacement.state),
        )

        assertEquals(existing, reminderRefreshResult(existing, responseSuccessful = false, payload = null))
        assertEquals(existing, reminderRefreshResult(existing, responseSuccessful = true, payload = null))
        assertEquals("Reminder refresh failed", reminderRefreshError(responseSuccessful = true, payload = null))
        assertEquals("", reminderRefreshError(responseSuccessful = true, payload = JSONArray()))
        assertEquals(emptyList<ReminderSummary>(), reminderRefreshResult(existing, responseSuccessful = true, payload = JSONArray()))
        assertEquals(listOf(replacement), reminderRefreshResult(existing, responseSuccessful = true, payload = payload))
    }

    @Test
    fun reminderCountdownAndModelControlStatusUseUserFacingStates() {
        val dueAt = Instant.parse("2030-01-01T00:02:05Z")
        val now = Instant.parse("2030-01-01T00:00:00Z")
        assertEquals("Due in 2m 5s", formatReminderCountdown(dueAt.toString(), now))
        assertEquals("Due now", formatReminderCountdown(now.toString(), now))
        assertEquals("Due time unavailable", formatReminderCountdown("invalid", now))
        assertEquals("Recurring", reminderRecurrenceLabel(60_000))
        assertEquals("One-shot", reminderRecurrenceLabel(null))
        assertEquals("Model changed", modelControlStatusTitle(true, null, ModelSummary("openai", "gpt")))
        assertEquals("Model change not confirmed", modelControlStatusTitle(true, null))
        assertEquals("Model change rejected", modelControlStatusTitle(false, "model_unavailable"))
        assertEquals("Model change not confirmed", modelControlStatusTitle(false, "control_timeout"))
    }

    @Test
    fun modelSearchMatchesProviderIdAndDisplayLabel() {
        val model = ModelSummary("openai", "gpt-5", "Fast GPT")

        assertEquals(true, modelMatchesSearch(model, "openai/gpt-5"))
        assertEquals(true, modelMatchesSearch(model, "fast gpt"))
        assertEquals(false, modelMatchesSearch(model, "missing"))
    }

    @Test
    fun remindersProjectionSkipsInvalidRecordsAndPreservesValidFields() {
        val values = JSONArray()
            .put("not an object")
            .put(JSONObject().put("id", "missing-target"))
            .put(JSONObject().put("id", "").put("target", "worker"))
            .put(JSONObject().put("id", 42).put("target", "worker"))
            .put(JSONObject().put("id", "valid").put("target", "worker").put("interval_ms", "60000").put("state", "scheduled"))

        val reminders = remindersFromJson(values)

        assertEquals(1, reminders.size)
        assertEquals(ReminderSummary("valid", "worker", "", "", "", null, "scheduled"), reminders.single())
    }

    @Test
    fun remindersProjectionIsBounded() {
        val values = JSONArray()
        repeat(1_001) { index ->
            values.put(JSONObject().put("id", "reminder-$index").put("target", "worker"))
        }

        assertEquals(1_000, remindersFromJson(values).size)

    }

    @Test
    fun activityOrbitCoversWorkingSubagentAndCompactingStatesWithWebUiColors() {
        assertEquals(ActivityOrbit.WORKING, activityOrbitFor(PeerStatus("worker", activityState = "active")))
        assertEquals(
            ActivityOrbit.SUBAGENT,
            activityOrbitFor(PeerStatus("worker", subagentCount = 1)),
        )
        assertEquals(
            ActivityOrbit.COMPACTING,
            activityOrbitFor(PeerStatus("worker", activityState = "compacting", subagentCount = 1)),
        )
        assertEquals(null, activityOrbitFor(PeerStatus("worker")))
        assertEquals(Color(0xFF78E0CD), ActivityOrbit.WORKING.color)
        assertEquals(Color(0xFF60A5FA), ActivityOrbit.SUBAGENT.color)
        assertEquals(Color(0xFFC084FC), ActivityOrbit.COMPACTING.color)
    }

    @Test
    fun connectionPresentationMatchesWebUiStates() {
        val connecting = connectionDisplayState("Connecting to your agents…")
        assertEquals(ConnectionDisplayState.CONNECTING, connecting)
        assertEquals("Connecting", connectionDisplayTitle(connecting, "Connecting to your agents…", "android"))
        assertEquals("Connecting to your agents… · Home", connectionDisplayDetail(connecting, "Connecting to your agents…", "", "android", "Home"))

        val connected = connectionDisplayState("Connected as android")
        assertEquals(ConnectionDisplayState.CONNECTED, connected)
        assertEquals("Connected as android", connectionDisplayTitle(connected, "Connected as android", "android"))
        assertEquals("", connectionDisplayDetail(connected, "Connected as android", "", "android", "Home"))

        assertEquals(ConnectionDisplayState.RECONNECTING, connectionDisplayState("Connection stale", hasPeers = true))
        assertEquals(ConnectionDisplayState.ERROR, connectionDisplayState("Username must contain only letters", hasPeers = true))
        assertEquals(ConnectionDisplayState.ERROR, connectionDisplayState("Message not sent: router rejected it", hasPeers = true))
        assertEquals(ConnectionDisplayState.ERROR, connectionDisplayState("Attached images are too large together", hasPeers = true))

        val reconnecting = connectionDisplayState("Router unavailable; retrying…")
        assertEquals(ConnectionDisplayState.RECONNECTING, reconnecting)
        assertEquals("Reconnecting", connectionDisplayTitle(reconnecting, "Router unavailable; retrying…", "android"))
        assertEquals("Router unavailable; retrying… · Home", connectionDisplayDetail(reconnecting, "Router unavailable; retrying…", "", "android", "Home"))

        val error = connectionDisplayState("Router error: bad_frame")
        assertEquals(ConnectionDisplayState.ERROR, error)
        assertEquals("Error", connectionDisplayTitle(error, "Router error: bad_frame", "android"))
        assertEquals("Router error: bad_frame · Home", connectionDisplayDetail(error, "Router error: bad_frame", "", "android", "Home"))

        val disconnected = connectionDisplayState("Add a router to connect")
        assertEquals(ConnectionDisplayState.DISCONNECTED, disconnected)
        assertEquals("Disconnected", connectionDisplayTitle(disconnected, "Add a router to connect", "android"))
        assertEquals("Open routers to connect.", connectionDisplayDetail(disconnected, "Add a router to connect", "", "android", "Home"))
    }
}
