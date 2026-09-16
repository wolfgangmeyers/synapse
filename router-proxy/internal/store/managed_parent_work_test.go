package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/stretchr/testify/require"
)

func parentWorkAttempt(fence ManagedRuntimeFence, id string) ManagedDirectAttempt {
	return ManagedDirectAttempt{MessageID: id, MessageIncarnationID: id + "-incarnation", AttemptNumber: 1, Fence: fence, Message: domain.Message{ID: id, From: "wolfgang", To: fence.AgentID}}
}

func openParentWorkForTest(t *testing.T, durable *Store, attempt ManagedDirectAttempt) {
	t.Helper()
	require.NoError(t, durable.runImmediate(context.Background(), func() error {
		return durable.openManagedParentWorkTx(context.Background(), attempt, formatTimestamp(durable.clock.Now()))
	}))
}

func parentWorkState(t *testing.T, durable *Store, attempt ManagedDirectAttempt) string {
	t.Helper()
	var state string
	require.NoError(t, durable.db.QueryRow(`SELECT state FROM managed_parent_work WHERE message_id=? AND message_incarnation_id=? AND attempt_number=?`, attempt.MessageID, attempt.MessageIncarnationID, attempt.AttemptNumber).Scan(&state))
	return state
}

func TestManagedFinalDeliveryConvergesReplyAndSettlementInEitherOrder(t *testing.T) {
	ctx := context.Background()
	for _, first := range []string{"reply", "settled"} {
		t.Run(first, func(t *testing.T) {
			durable, fence := readyParentCompactionFixture(t)
			attempt := parentWorkAttempt(fence, "final-"+first)
			openParentWorkForTest(t, durable, attempt)
			var delivery *ManagedFinalDelivery
			if first == "reply" {
				var err error
				delivery, err = durable.QueueManagedFinalDelivery(ctx, attempt, "assistant-entry", "final")
				require.NoError(t, err)
				require.NoError(t, durable.SettleManagedParentDirectTurn(ctx, attempt))
			} else {
				require.NoError(t, durable.SettleManagedParentDirectTurn(ctx, attempt))
				var err error
				delivery, err = durable.QueueManagedFinalDelivery(ctx, attempt, "assistant-entry", "final")
				require.NoError(t, err)
			}
			require.NotNil(t, delivery)
			claimed, err := durable.ClaimManagedFinalDelivery(ctx, *delivery)
			require.NoError(t, err)
			require.NotNil(t, claimed)
			changed, err := durable.MarkManagedFinalDeliveryDelivered(ctx, *claimed)
			require.NoError(t, err)
			require.True(t, changed)
			require.Equal(t, "settled", parentWorkState(t, durable, attempt))
			agent, err := durable.ManagedAgent(ctx, fence.AgentID)
			require.NoError(t, err)
			require.Equal(t, "idle", agent.ActivityState)
		})
	}
}

func TestManagedFinalDeliveryRecoveryTerminalizesAnExhaustedInFlightAttempt(t *testing.T) {
	ctx := context.Background()
	durable, fence := readyParentCompactionFixture(t)
	attempt := parentWorkAttempt(fence, "final-recovery")
	openParentWorkForTest(t, durable, attempt)
	delivery, err := durable.QueueManagedFinalDelivery(ctx, attempt, "assistant-entry", "final")
	require.NoError(t, err)
	claimed, err := durable.ClaimManagedFinalDelivery(ctx, *delivery)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	_, err = durable.db.Exec(`UPDATE managed_parent_final_deliveries SET attempt_count=? WHERE delivery_id=?`, managedFinalDeliveryMaxAttempts, delivery.DeliveryID)
	require.NoError(t, err)

	require.NoError(t, durable.RecoverManagedFinalDeliveries(ctx, fence))
	require.Equal(t, "final_delivery_failed", parentWorkState(t, durable, attempt))
	pending, err := durable.PendingManagedFinalDeliveries(ctx, fence)
	require.NoError(t, err)
	require.Empty(t, pending)
	agent, err := durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", agent.ActivityState)
}

func TestManagedFinalDeliveryFailureIsBoundedAndInterruptClosesExactFence(t *testing.T) {
	ctx := context.Background()
	durable, fence := readyParentCompactionFixture(t)
	attempt := parentWorkAttempt(fence, "final-failure")
	openParentWorkForTest(t, durable, attempt)
	delivery, err := durable.QueueManagedFinalDelivery(ctx, attempt, "assistant-entry", "final")
	require.NoError(t, err)
	for want := 1; want <= managedFinalDeliveryMaxAttempts; want++ {
		claimed, err := durable.ClaimManagedFinalDelivery(ctx, *delivery)
		require.NoError(t, err)
		require.NotNil(t, claimed)
		terminal, err := durable.FailManagedFinalDelivery(ctx, *claimed, "transport_failed")
		require.NoError(t, err)
		require.Equal(t, want == managedFinalDeliveryMaxAttempts, terminal)
		if !terminal {
			_, err = durable.db.Exec(`UPDATE managed_parent_final_deliveries SET next_attempt_at='1970-01-01T00:00:00.000Z' WHERE delivery_id=?`, delivery.DeliveryID)
			require.NoError(t, err)
		}
	}
	require.Equal(t, "final_delivery_failed", parentWorkState(t, durable, attempt))
	agent, err := durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", agent.ActivityState)

	other := parentWorkAttempt(fence, "final-interrupt")
	openParentWorkForTest(t, durable, other)
	_, err = durable.QueueManagedFinalDelivery(ctx, other, "assistant-entry-two", "final")
	require.NoError(t, err)
	require.NoError(t, durable.InterruptManagedParentWork(ctx, fence))
	require.Equal(t, "interrupted", parentWorkState(t, durable, other))

	fresh := parentWorkAttempt(fence, "final-stale-fence")
	openParentWorkForTest(t, durable, fresh)
	_, err = durable.QueueManagedFinalDelivery(ctx, fresh, "assistant-entry-three", "final")
	require.NoError(t, err)
	stale := fence
	stale.ProcessIncarnation = "replaced-incarnation"
	require.NoError(t, durable.InterruptManagedParentWork(ctx, stale))
	require.Equal(t, "final_delivery_pending", parentWorkState(t, durable, fresh))
}

func TestReconcileManagedParentActivityRepairsHistoricalStaleRowsIdempotently(t *testing.T) {
	ctx := context.Background()
	durable, fence := readyParentCompactionFixture(t)
	_, err := durable.db.Exec(`UPDATE managed_agents SET activity_state='active' WHERE agent_id=?`, fence.AgentID)
	require.NoError(t, err)
	beforeVersion, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)

	repaired, err := durable.ReconcileManagedParentActivity(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, repaired)
	agent, err := durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", agent.ActivityState)
	afterVersion, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, beforeVersion+1, afterVersion)

	repaired, err = durable.ReconcileManagedParentActivity(ctx)
	require.NoError(t, err)
	require.Zero(t, repaired)
	repeatVersion, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, afterVersion, repeatVersion)
}

func insertManagedReconciliationAgent(t *testing.T, durable *Store, agentID, activity string, pid int, incarnation string) ManagedRuntimeFence {
	t.Helper()
	fence := ManagedRuntimeFence{AgentID: agentID, PiSessionID: "session-" + agentID, SessionEpoch: 1, ProcessPID: pid, ProcessStartMarker: "marker-" + agentID, ProcessIncarnation: incarnation, RuntimeGeneration: "generation-" + agentID, ManifestRevision: 1, ManifestHash: testManifestHash}
	_, err := durable.db.Exec(`INSERT INTO managed_agents (
		agent_id,name,slot_held,cwd,selected_model_provider,selected_model_id,
		desired_state,lifecycle_state,activity_state,accepting_prompts,pi_session_id,
		session_epoch,process_pid,process_start_marker,process_incarnation,runtime_generation,
		manifest_revision,manifest_hash,created_at,updated_at)
		VALUES (?,?,0,'/tmp','openai-codex','gpt-5.6-terra','running','running',?,0,?,?,?,?,?,?,1,?,'2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')`,
		agentID, agentID, activity, fence.PiSessionID, fence.SessionEpoch, fence.ProcessPID, fence.ProcessStartMarker, fence.ProcessIncarnation, fence.RuntimeGeneration, fence.ManifestHash)
	require.NoError(t, err)
	return fence
}

func managedReconciliationCursor(t *testing.T, durable *Store) string {
	t.Helper()
	durable.managedParentActivityReconcileMu.Lock()
	defer durable.managedParentActivityReconcileMu.Unlock()
	return durable.managedParentActivityReconcileCursor
}

func managedReconciliationHighWater(t *testing.T, durable *Store) string {
	t.Helper()
	durable.managedParentActivityReconcileMu.Lock()
	defer durable.managedParentActivityReconcileMu.Unlock()
	return durable.managedParentActivityReconcileHighWater
}

func TestReconcileManagedParentActivityMakesBoundedKeysetProgress(t *testing.T) {
	ctx := context.Background()
	durable, _ := readyParentCompactionFixture(t)
	var nativeFence ManagedRuntimeFence
	for index := 0; index < 257; index++ {
		activity := "idle"
		if index == 0 {
			activity = "active"
		}
		fence := insertManagedReconciliationAgent(t, durable, fmt.Sprintf("agent-%03d", index), activity, 1000+index, fmt.Sprintf("incarnation-%03d", index))
		if index == 0 {
			nativeFence = fence
			_, err := durable.db.Exec(`INSERT INTO managed_parent_compaction_state(agent_id,pi_session_id,session_epoch,process_incarnation,runtime_generation,source_sequence,compacting,active,updated_at) VALUES(?,?,?,?,?,1,0,1,'2026-01-01T00:00:00.000Z')`, fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessIncarnation, fence.RuntimeGeneration)
			require.NoError(t, err)
		}
	}
	staleFence := insertManagedReconciliationAgent(t, durable, "zz-stale", "active", 2000, "stale-incarnation")

	repaired, err := durable.ReconcileManagedParentActivity(ctx)
	require.NoError(t, err)
	require.Zero(t, repaired)
	require.Equal(t, "agent-255", managedReconciliationCursor(t, durable))
	require.Equal(t, "zz-stale", managedReconciliationHighWater(t, durable))
	native, err := durable.ManagedAgent(ctx, nativeFence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "active", native.ActivityState)
	stale, err := durable.ManagedAgent(ctx, staleFence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "active", stale.ActivityState)

	_, err = durable.db.Exec(`DELETE FROM managed_agents WHERE agent_id=?`, "agent-255")
	require.NoError(t, err)
	insertManagedReconciliationAgent(t, durable, "agent-257-inserted", "idle", 2257, "inserted-incarnation")
	_, err = durable.db.Exec(`UPDATE managed_agents SET process_pid=?, process_start_marker=?, process_incarnation=?, runtime_generation=? WHERE agent_id=?`, 3000, "replacement-marker", "replacement-incarnation", "replacement-generation", staleFence.AgentID)
	require.NoError(t, err)
	staleFence.ProcessPID = 3000
	staleFence.ProcessStartMarker = "replacement-marker"
	staleFence.ProcessIncarnation = "replacement-incarnation"
	staleFence.RuntimeGeneration = "replacement-generation"

	repaired, err = durable.ReconcileManagedParentActivity(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, repaired)
	require.Equal(t, "zz-stale", managedReconciliationCursor(t, durable))
	stale, err = durable.ManagedAgent(ctx, staleFence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", stale.ActivityState)

	repaired, err = durable.ReconcileManagedParentActivity(ctx)
	require.NoError(t, err)
	require.Zero(t, repaired)
	require.Empty(t, managedReconciliationCursor(t, durable), "an exhausted keyset wraps on the next bounded sweep")
	require.Empty(t, managedReconciliationHighWater(t, durable))
}

func TestReconcileManagedParentActivityWrapsFiniteCycleUnderTailChurn(t *testing.T) {
	ctx := context.Background()
	durable, _ := readyParentCompactionFixture(t)
	for index := 0; index < 257; index++ {
		insertManagedReconciliationAgent(t, durable, fmt.Sprintf("agent-%03d", index), "idle", 4000+index, fmt.Sprintf("incarnation-%03d", index))
	}
	first, err := durable.ReconcileManagedParentActivity(ctx)
	require.NoError(t, err)
	require.Zero(t, first)
	require.Equal(t, "agent-255", managedReconciliationCursor(t, durable))
	require.Equal(t, "source-agent", managedReconciliationHighWater(t, durable))

	staleFence := insertManagedReconciliationAgent(t, durable, "agent-100-new", "active", 5000, "stale-incarnation")
	insertManagedReconciliationAgent(t, durable, "zz-tail-000", "idle", 6000, "tail-incarnation-000")
	repaired, err := durable.ReconcileManagedParentActivity(ctx)
	require.NoError(t, err)
	require.Zero(t, repaired)
	agent, err := durable.ManagedAgent(ctx, staleFence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "active", agent.ActivityState)

	insertManagedReconciliationAgent(t, durable, "zz-tail-001", "idle", 6001, "tail-incarnation-001")
	repaired, err = durable.ReconcileManagedParentActivity(ctx)
	require.NoError(t, err)
	require.Zero(t, repaired)
	require.Empty(t, managedReconciliationCursor(t, durable))
	require.Empty(t, managedReconciliationHighWater(t, durable))
	agent, err = durable.ManagedAgent(ctx, staleFence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "active", agent.ActivityState)

	insertManagedReconciliationAgent(t, durable, "zz-tail-002", "idle", 6002, "tail-incarnation-002")
	repaired, err = durable.ReconcileManagedParentActivity(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, repaired, "the below-cursor stale row is visited in the next finite cycle")
	agent, err = durable.ManagedAgent(ctx, staleFence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", agent.ActivityState)
}

func TestReconcileManagedParentActivityRejectsStaleFenceAfterReplacement(t *testing.T) {
	ctx := context.Background()
	durable, fence := readyParentCompactionFixture(t)
	_, err := durable.db.Exec(`UPDATE managed_agents SET activity_state='active', process_pid=?, process_start_marker=?, process_incarnation=?, runtime_generation=? WHERE agent_id=?`, 3000, "replacement-marker", "replacement-incarnation", "replacement-generation", fence.AgentID)
	require.NoError(t, err)
	stale := fence
	updated, err := durable.RecomputeManagedParentActivity(ctx, stale)
	require.NoError(t, err)
	require.False(t, updated)
	agent, err := durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "active", agent.ActivityState)

	repaired, err := durable.ReconcileManagedParentActivity(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, repaired)
	agent, err = durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", agent.ActivityState)
}

func TestReconcileManagedParentActivityPreservesNativeActiveUntilNewerIdle(t *testing.T) {
	ctx := context.Background()
	durable, fence := readyParentCompactionFixture(t)
	updated, err := durable.ApplyManagedParentActivity(ctx, fence, ManagedParentActivityEvent{Sequence: 1, State: "active"})
	require.NoError(t, err)
	require.True(t, updated)

	repaired, err := durable.ReconcileManagedParentActivity(ctx)
	require.NoError(t, err)
	require.Zero(t, repaired)
	agent, err := durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "active", agent.ActivityState)

	updated, err = durable.ApplyManagedParentActivity(ctx, fence, ManagedParentActivityEvent{Sequence: 2, State: "idle"})
	require.NoError(t, err)
	require.True(t, updated)
	repaired, err = durable.ReconcileManagedParentActivity(ctx)
	require.NoError(t, err)
	require.Zero(t, repaired)
	agent, err = durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", agent.ActivityState)
}

func TestManagedParentActivityKeepsOtherActiveWorkVisibleUntilAllSourcesSettle(t *testing.T) {
	ctx := context.Background()
	durable, fence := readyParentCompactionFixture(t)
	first := parentWorkAttempt(fence, "parent-source-one")
	second := parentWorkAttempt(fence, "parent-source-two")
	openParentWorkForTest(t, durable, first)
	openParentWorkForTest(t, durable, second)

	require.NoError(t, durable.SettleManagedParentDirectTurn(ctx, first))
	agent, err := durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "active", agent.ActivityState)
	require.NoError(t, durable.SettleManagedParentDirectTurn(ctx, second))
	agent, err = durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", agent.ActivityState)
}

func TestManagedParentWorkRetentionKeepsABoundedTerminalArchivePerAgent(t *testing.T) {
	ctx := context.Background()
	durable, fence := readyParentCompactionFixture(t)
	for index := 0; index <= managedParentWorkTerminalPerAgent; index++ {
		attempt := parentWorkAttempt(fence, fmt.Sprintf("retention-%03d", index))
		openParentWorkForTest(t, durable, attempt)
		require.NoError(t, durable.SettleManagedParentDirectTurn(ctx, attempt))
	}
	require.NoError(t, durable.runImmediate(ctx, func() error {
		return durable.pruneManagedParentWorkTx(ctx, formatTimestamp(durable.clock.Now()))
	}))
	var count int
	require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM managed_parent_work WHERE agent_id=? AND state='settled'`, fence.AgentID).Scan(&count))
	require.Equal(t, managedParentWorkTerminalPerAgent, count)
}
