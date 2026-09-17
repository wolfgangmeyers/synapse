package store

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func prepareManagedRootRecoveryWakeup(t *testing.T) (*Store, ManagedRuntimeFence, ManagedRuntimeFence, *ManagedRootRecoveryAction) {
	t.Helper()
	ctx := context.Background()
	durable := openTestStore(t)
	old := seedManagedRootRecoveryAgent(t, durable)
	recipe := managedRootRecoveryRecipeFixture()
	action, created, err := durable.ClaimManagedRootRecovery(ctx, old.AgentID, old, recipe.SHA256, "target-runtime")
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, durable.AdvanceManagedRootRecovery(ctx, action.ActionID, "claimed", "reaping"))
	require.NoError(t, durable.AdvanceManagedRootRecovery(ctx, action.ActionID, "reaping", "stopped"))
	require.NoError(t, durable.BeginManagedRootRecoveryTarget(ctx, action.ActionID, "target-runtime"))
	require.NoError(t, durable.InstallManagedRootRecoveryTarget(ctx, action.ActionID, ManagedProcessIdentity{PID: 72, StartMarker: "start-2", Incarnation: "inc-2"}, "target-runtime"))
	target := old
	target.ProcessPID, target.ProcessStartMarker, target.ProcessIncarnation, target.RuntimeGeneration = 72, "start-2", "inc-2", "target-runtime"
	require.NoError(t, durable.InstallManagedRootRecoveryFence(ctx, old, target))
	require.NoError(t, durable.CompleteManagedRootRecovery(ctx, old.AgentID, target))
	_, err = durable.db.Exec(`UPDATE managed_agents SET lifecycle_state='running',activity_state='idle',accepting_prompts=1,slot_held=1 WHERE agent_id=?`, old.AgentID)
	require.NoError(t, err)
	now := formatTimestamp(durable.clock.Now())
	_, err = durable.db.Exec(`INSERT INTO recipient_sources(recipient_name,source_kind,owner_identity,state,created_at,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(recipient_name) DO UPDATE SET source_kind='managed',owner_identity=excluded.owner_identity,state='active',updated_at=excluded.updated_at`, old.AgentID, "managed", old.AgentID, "active", now, now)
	require.NoError(t, err)
	return durable, old, target, action
}

func insertInterruptedWakeupChild(t *testing.T, durable *Store, fence ManagedRuntimeFence, taskID, runID string) {
	t.Helper()
	_, err := durable.db.Exec(`INSERT INTO managed_child_recoveries(agent_id,prior_pi_session_id,prior_session_epoch,prior_process_incarnation,prior_runtime_generation,parent_entry_id,task_id,run_id,source_sequence,run_json,recovery_state,recovery_cause,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?, 'interrupted_unknown','parent_process_lost',?,?)`,
		fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessIncarnation, fence.RuntimeGeneration, "entry-"+runID, taskID, runID, 3, `{"run_id":"`+runID+`","task_id":"`+taskID+`"}`, formatTimestamp(durable.clock.Now()), formatTimestamp(durable.clock.Now()))
	require.NoError(t, err)
}

func TestManagedRootRecoveryWakeupUsesExactFenceAndNormalDelivery(t *testing.T) {
	durable, old, _, action := prepareManagedRootRecoveryWakeup(t)
	ctx := context.Background()
	insertInterruptedWakeupChild(t, durable, old, "task-b", "run-b")
	insertInterruptedWakeupChild(t, durable, old, "task-a", "run-a")
	insertInterruptedWakeupChild(t, durable, ManagedRuntimeFence{AgentID: old.AgentID, PiSessionID: old.PiSessionID, SessionEpoch: old.SessionEpoch, ProcessPID: old.ProcessPID, ProcessStartMarker: old.ProcessStartMarker, ProcessIncarnation: "other-inc", RuntimeGeneration: old.RuntimeGeneration}, "task-x", "run-x")

	wakeup, err := durable.QueueManagedRootRecoveryWakeup(ctx, action.ActionID)
	require.NoError(t, err)
	require.NotNil(t, wakeup)
	require.Equal(t, "Router restarted. Interrupted child runs: run-a (task task-a), run-b (task task-b).", wakeup.Body)
	require.NotContains(t, wakeup.Body, "run-x")
	require.NotContains(t, wakeup.Body, "other-inc")
	var recoveryState string
	require.NoError(t, durable.db.QueryRow(`SELECT recovery_state FROM managed_child_recoveries WHERE run_id=?`, "run-a").Scan(&recoveryState))
	require.Equal(t, "interrupted_unknown", recoveryState)

	target, err := durable.ResolveMessageTarget(ctx, wakeup.MessageID)
	require.NoError(t, err)
	require.NotNil(t, target)
	require.Equal(t, "managed", target.SourceKind)
	require.Equal(t, old.AgentID, target.OwnerIdentity)
	var state, source, recipient string
	require.NoError(t, durable.db.QueryRow(`SELECT state,source_kind,recipient FROM messages WHERE id=?`, wakeup.MessageID).Scan(&state, &source, &recipient))
	require.Equal(t, "queued", state)
	require.Equal(t, "system", source)
	require.Equal(t, old.AgentID, recipient)
}

func TestManagedRootRecoveryWakeupNotifiesWithoutChildrenAndIsIdempotent(t *testing.T) {
	durable, old, _, action := prepareManagedRootRecoveryWakeup(t)
	ctx := context.Background()
	first, err := durable.QueueManagedRootRecoveryWakeup(ctx, action.ActionID)
	require.NoError(t, err)
	require.NotNil(t, first)
	require.Equal(t, "Router restarted. No child runs were recorded as interrupted.", first.Body)
	second, err := durable.QueueManagedRootRecoveryWakeup(ctx, action.ActionID)
	require.NoError(t, err)
	require.Equal(t, first, second)
	var messages, markers int
	require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE recipient=? AND sender='system'`, old.AgentID).Scan(&messages))
	require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM managed_root_recovery_wakeups WHERE action_id=?`, action.ActionID).Scan(&markers))
	require.Equal(t, 1, messages)
	require.Equal(t, 1, markers)
}

func TestManagedRootRecoveryWakeupRetriesAfterReadyTransactionFailure(t *testing.T) {
	durable, old, _, _ := prepareManagedRootRecoveryWakeup(t)
	ctx := context.Background()
	_, err := durable.db.Exec(`CREATE TRIGGER reject_recovery_wakeup BEFORE INSERT ON messages WHEN NEW.sender='system' BEGIN SELECT RAISE(FAIL, 'wakeup insert failed'); END`)
	require.NoError(t, err)
	_, err = durable.QueueManagedRootRecoveryWakeupForAgent(ctx, old.AgentID)
	require.ErrorContains(t, err, "wakeup insert failed")
	var messages, markers int
	require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE sender='system'`).Scan(&messages))
	require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM managed_root_recovery_wakeups`).Scan(&markers))
	require.Zero(t, messages)
	require.Zero(t, markers)
	_, err = durable.db.Exec(`DROP TRIGGER reject_recovery_wakeup`)
	require.NoError(t, err)
	wakeup, err := durable.QueueManagedRootRecoveryWakeupForAgent(ctx, old.AgentID)
	require.NoError(t, err)
	require.NotNil(t, wakeup)
}

func TestManagedRootRecoveryWakeupAllowsLaterRecoveryNotice(t *testing.T) {
	durable, _, current, first := prepareManagedRootRecoveryWakeup(t)
	ctx := context.Background()
	firstWakeup, err := durable.QueueManagedRootRecoveryWakeup(ctx, first.ActionID)
	require.NoError(t, err)

	secondAction, created, err := durable.ClaimManagedRootRecovery(ctx, current.AgentID, current, managedRootRecoveryRecipeFixture().SHA256, "target-runtime-2")
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, durable.AdvanceManagedRootRecovery(ctx, secondAction.ActionID, "claimed", "reaping"))
	require.NoError(t, durable.AdvanceManagedRootRecovery(ctx, secondAction.ActionID, "reaping", "stopped"))
	require.NoError(t, durable.BeginManagedRootRecoveryTarget(ctx, secondAction.ActionID, "target-runtime-2"))
	require.NoError(t, durable.InstallManagedRootRecoveryTarget(ctx, secondAction.ActionID, ManagedProcessIdentity{PID: 73, StartMarker: "start-3", Incarnation: "inc-3"}, "target-runtime-2"))
	next := current
	next.ProcessPID, next.ProcessStartMarker, next.ProcessIncarnation, next.RuntimeGeneration = 73, "start-3", "inc-3", "target-runtime-2"
	require.NoError(t, durable.InstallManagedRootRecoveryFence(ctx, current, next))
	require.NoError(t, durable.CompleteManagedRootRecovery(ctx, current.AgentID, next))
	_, err = durable.db.Exec(`UPDATE managed_agents SET lifecycle_state='running',activity_state='idle',accepting_prompts=1,slot_held=1 WHERE agent_id=?`, current.AgentID)
	require.NoError(t, err)
	secondWakeup, err := durable.QueueManagedRootRecoveryWakeup(ctx, secondAction.ActionID)
	require.NoError(t, err)
	require.NotNil(t, secondWakeup)
	require.NotEqual(t, firstWakeup.MessageID, secondWakeup.MessageID)
	var count int
	require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM managed_root_recovery_wakeups`).Scan(&count))
	require.Equal(t, 2, count)
}

func TestManagedRootRecoveryWakeupBodyIsBoundedAndSanitized(t *testing.T) {
	durable, old, _, action := prepareManagedRootRecoveryWakeup(t)
	insertInterruptedWakeupChild(t, durable, old, strings.Repeat("task", 40)+"\nsecret", strings.Repeat("run", 40)+"\rsecret")
	wakeup, err := durable.QueueManagedRootRecoveryWakeup(context.Background(), action.ActionID)
	require.NoError(t, err)
	require.LessOrEqual(t, len([]byte(wakeup.Body)), managedRootRecoveryWakeupMaxBodyBytes)
	require.NotContains(t, wakeup.Body, "\n")
	require.NotContains(t, wakeup.Body, "\r")
}
