package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/stretchr/testify/require"
)

func terminateArchiveTestAction(t *testing.T, store *Store, agent ManagedAgentRecord, id string, fence ManagedRuntimeFence) ManagedLifecycleAdmission {
	t.Helper()
	op := ManagedOperation{OperationID: id, Method: "DELETE", CanonicalPath: "/v1/managed-agents/" + agent.Name, RequestID: id, CanonicalBodyHash: testManifestHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	admission, err := store.ReserveManagedLifecycle(context.Background(), ManagedLifecycleReservation{AgentID: agent.AgentID, Kind: "terminate", Source: "direct", Method: op.Method, CanonicalPath: op.CanonicalPath, RequestID: id, CanonicalBodyHash: op.CanonicalBodyHash, Fence: fence, Operation: op})
	require.NoError(t, err)
	return admission
}

func advanceArchiveActionToDurableWrite(t *testing.T, store *Store, actionID string, fence ManagedRuntimeFence) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, store.AdvanceManagedTermination(ctx, actionID, "terminating", "abort", "terminating", "terminate_reap", &fence))
	require.NoError(t, store.AdvanceManagedTermination(ctx, actionID, "terminating", "terminate_reap", "cleanup", "direct_mail_retire", &fence))
	require.NoError(t, store.AdvanceManagedTermination(ctx, actionID, "cleanup", "direct_mail_retire", "cleanup", "agent_cleanup", &fence))
	require.NoError(t, store.AdvanceManagedTermination(ctx, actionID, "cleanup", "agent_cleanup", "cleanup", "durable_write", &fence))
}

func TestManagedTerminationActionsLoadsArchiveFieldsAfterClosingActionRows(t *testing.T) {
	store, _, agent, _ := restartFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	fence := restartFenceFromAgent(&agent)
	admission := terminateArchiveTestAction(t, store, agent, lifecycleUUID(979), fence)

	actions, err := store.ManagedTerminationActions(ctx)
	require.NoError(t, err)
	require.Len(t, actions, 1)
	require.Equal(t, admission.Action.ArchiveKey, actions[0].ArchiveKey)
	require.Equal(t, admission.Action.DeliveryCutoffEnqueueSequence, actions[0].DeliveryCutoffEnqueueSequence)
}

func TestTerminationAdmissionCapturesArchiveKeyCutoffRetiresSourceAndFreezesSettlement(t *testing.T) {
	store, fence := readyManagedDirectFixture(t)
	ctx := context.Background()
	agent, err := store.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	before, err := store.Enqueue(ctx, protocol.Envelope{ID: "archive-before", From: "sender", To: agent.Name, Body: "before", CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	require.NoError(t, err)
	_, err = store.ResolveMessageTarget(ctx, before.Message.ID)
	require.NoError(t, err)
	beforeAttempt, err := store.AllocateManagedDirectAttempt(ctx, fence)
	require.NoError(t, err)
	require.NotNil(t, beforeAttempt)
	admission := terminateArchiveTestAction(t, store, *agent, lifecycleUUID(980), fence)
	require.Regexp(t, managedCanonicalUUIDRE, admission.Action.ArchiveKey)
	require.Equal(t, before.Message.EnqueueSequence, admission.Action.DeliveryCutoffEnqueueSequence)
	require.NotEmpty(t, admission.Action.DeliveryFrozenAt)
	source, err := store.RecipientSource(ctx, agent.Name)
	require.NoError(t, err)
	require.Equal(t, "retired", source.State)
	require.Equal(t, retiredManagedRecipientOwner(agent.AgentID), source.OwnerIdentity)

	attempt, err := store.AllocateManagedDirectAttempt(ctx, fence)
	var notReady *ManagedDirectNotReadyError
	require.ErrorAs(t, err, &notReady)
	require.Nil(t, attempt, "retirement must prevent new settlement work")
	var state string
	require.NoError(t, store.db.QueryRow(`SELECT state FROM managed_direct_delivery_attempts WHERE message_id=?`, before.Message.ID).Scan(&state))
	require.Equal(t, "direct_pending", state, "an existing attempt must remain for archive aggregation")
	storedBeforeAttempt, err := store.ManagedDirectAttempt(ctx, before.Message.ID, 1)
	require.NoError(t, err)
	require.NoError(t, store.AcknowledgeManagedDirect(ctx, *storedBeforeAttempt, "late-receipt", true))
	afterAttempt, err := store.ManagedDirectAttempt(ctx, before.Message.ID, 1)
	require.NoError(t, err)
	require.Equal(t, "direct_pending", afterAttempt.State)
}

func TestCompleteManagedTerminationDeletesStaleDirectAttemptsAndPreservesArchive(t *testing.T) {
	store, fence := readyManagedDirectFixture(t)
	ctx := context.Background()
	agent, err := store.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	before, err := store.Enqueue(ctx, protocol.Envelope{ID: "archive-stale-before", From: "sender", To: agent.Name, Body: "before", CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	require.NoError(t, err)
	_, err = store.ResolveMessageTarget(ctx, before.Message.ID)
	require.NoError(t, err)
	beforeAttempt, err := store.AllocateManagedDirectAttempt(ctx, fence)
	require.NoError(t, err)
	require.NotNil(t, beforeAttempt)
	admission := terminateArchiveTestAction(t, store, *agent, lifecycleUUID(985), fence)
	stale, err := store.Enqueue(ctx, protocol.Envelope{ID: "archive-stale-after", From: "sender", To: agent.Name, Body: "stale", CreatedAt: time.Now().UTC().Format("2006-01-01T00:00:00.000Z")})
	require.NoError(t, err)
	require.Greater(t, stale.Message.EnqueueSequence, admission.Action.DeliveryCutoffEnqueueSequence)
	require.NoError(t, func() error {
		_, err := store.db.ExecContext(ctx, `INSERT INTO message_target_snapshots (snapshot_id,message_id,message_incarnation_id,recipient_name,source_kind,owner_identity,created_at) VALUES (?,?,?,?,?,?,?)`,
			"stale-target", stale.Message.ID, stale.Message.MessageIncarnationID, agent.Name, "managed", agent.AgentID, "2026-09-08T00:00:00.000Z")
		return err
	}())
	require.NoError(t, func() error {
		_, err := store.db.ExecContext(ctx, `INSERT INTO managed_direct_delivery_attempts (message_id,message_incarnation_id,attempt_number,snapshot_id,agent_id,pi_session_id,session_epoch,process_pid,process_start_marker,process_incarnation,runtime_generation,manifest_revision,manifest_hash,state,created_at,expires_at)
			SELECT ?,message_incarnation_id,1,'stale-target',agent_id,pi_session_id,session_epoch,process_pid,process_start_marker,process_incarnation,runtime_generation,manifest_revision,manifest_hash,'direct_pending',created_at,expires_at
			FROM managed_direct_delivery_attempts WHERE message_id=? AND attempt_number=1`, stale.Message.ID, before.Message.ID)
		return err
	}())
	var staleAttempts int
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_direct_delivery_attempts WHERE agent_id=?`, agent.AgentID).Scan(&staleAttempts))
	require.Equal(t, 2, staleAttempts)

	advanceArchiveActionToDurableWrite(t, store, admission.Action.ActionID, fence)
	require.NoError(t, store.CompleteManagedTermination(ctx, admission.Action.ActionID))

	archive, err := store.ManagedAgentArchive(ctx, admission.Action.ArchiveKey)
	require.NoError(t, err)
	require.Equal(t, 1, archive.Delivery.Total)
	require.Equal(t, 1, archive.Delivery.InFlightOrUnknown)
	_, err = store.ManagedAgent(ctx, agent.AgentID)
	require.ErrorIs(t, err, sql.ErrNoRows)
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_direct_delivery_attempts WHERE agent_id=?`, agent.AgentID).Scan(&staleAttempts))
	require.Equal(t, 0, staleAttempts)
	message, err := store.MessageFor(ctx, before.Message.ID)
	require.NoError(t, err)
	require.Nil(t, message)
	message, err = store.MessageFor(ctx, stale.Message.ID)
	require.NoError(t, err)
	require.NotNil(t, message, "post-cutoff stale message is retained outside archive cleanup")
}

func TestCompleteManagedTerminationPublishesArchiveAndDeletesOnlyCutoffMessages(t *testing.T) {
	store, ctx, agent, _ := restartFixture(t)
	fence := restartFenceFromAgent(&agent)
	before, err := store.Enqueue(ctx, protocol.Envelope{ID: "archive-cutoff-before", From: "sender", To: agent.Name, Body: "before", CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	require.NoError(t, err)
	_, err = store.ResolveMessageTarget(ctx, before.Message.ID)
	require.NoError(t, err)
	admission := terminateArchiveTestAction(t, store, agent, lifecycleUUID(981), fence)
	after, err := store.Enqueue(ctx, protocol.Envelope{ID: "archive-cutoff-after", From: "sender", To: agent.Name, Body: "after", CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	require.NoError(t, err)
	advanceArchiveActionToDurableWrite(t, store, admission.Action.ActionID, fence)
	require.NoError(t, store.CompleteManagedTermination(ctx, admission.Action.ActionID))

	archive, err := store.ManagedAgentArchive(ctx, admission.Action.ArchiveKey)
	require.NoError(t, err)
	require.Equal(t, "terminated", archive.TerminalOutcome)
	require.Equal(t, 1, archive.Delivery.Total)
	require.Equal(t, 1, archive.Delivery.QueuedNeverAttempted)
	require.Equal(t, "queued", archive.Delivery.Disposition)
	_, err = store.ManagedAgent(ctx, agent.AgentID)
	require.ErrorIs(t, err, sql.ErrNoRows)
	message, err := store.MessageFor(ctx, before.Message.ID)
	require.NoError(t, err)
	require.Nil(t, message)
	message, err = store.MessageFor(ctx, after.Message.ID)
	require.NoError(t, err)
	require.NotNil(t, message, "post-cutoff messages are not part of termination cleanup")
}

func TestManagedTerminationRetryPreservesArchiveKeyAndLaunchBarrier(t *testing.T) {
	store, ctx, agent, _ := restartFixture(t)
	fence := restartFenceFromAgent(&agent)
	first := terminateArchiveTestAction(t, store, agent, lifecycleUUID(982), fence)
	firstKey := first.Action.ArchiveKey
	require.NoError(t, store.AdvanceManagedTermination(ctx, first.Action.ActionID, "terminating", "abort", "terminating", "terminate_reap", &fence))
	require.NoError(t, store.AdvanceManagedTermination(ctx, first.Action.ActionID, "terminating", "terminate_reap", "cleanup", "direct_mail_retire", &fence))
	require.NoError(t, store.FailManagedTermination(ctx, first.Action.ActionID, "TERMINATION_CLEANUP_FAILED", true, &fence))
	replacement := agent
	replacement.PiSessionID = ""
	replacement.ProcessPID = 0
	replacement.ProcessStartMarker = ""
	replacement.ProcessIncarnation = ""
	replacement.RuntimeGeneration = ""
	replacement.LifecycleState = "starting"
	replacement.ActivityState = "starting"
	replacement.DesiredState = "running"
	op := ManagedOperation{OperationID: lifecycleUUID(983), Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: lifecycleUUID(983), CanonicalBodyHash: testManifestHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	var barrier *ManagedLifecycleConflictError
	require.ErrorAs(t, store.ReserveManagedLaunch(ctx, replacement, op), &barrier)
	retryOp := ManagedOperation{OperationID: lifecycleUUID(984), Method: "DELETE", CanonicalPath: "/v1/managed-agents/" + agent.Name, RequestID: lifecycleUUID(984), CanonicalBodyHash: testManifestHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	retry, err := store.ReserveManagedLifecycle(ctx, ManagedLifecycleReservation{AgentID: agent.AgentID, Kind: "terminate", Source: "direct", Method: retryOp.Method, CanonicalPath: retryOp.CanonicalPath, RequestID: retryOp.RequestID, CanonicalBodyHash: retryOp.CanonicalBodyHash, Fence: fence, RetryOf: first.Action.ActionID, Operation: retryOp})
	require.NoError(t, err)
	require.Equal(t, firstKey, retry.Action.ArchiveKey)
	require.Equal(t, first.Action.DeliveryCutoffEnqueueSequence, retry.Action.DeliveryCutoffEnqueueSequence)
}

func TestManagedArchivePrunesAtOpenAndSweep(t *testing.T) {
	start := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	store := openTestStoreWithOptions(t, Options{Clock: clock})
	key := testUUID("0090")
	insertManagedArchiveForReadTest(t, store, key, "old-archive", formatTimestamp(start.Add(-managedArchiveRetention)))
	require.NoError(t, store.Close())
	reopened, err := Open(context.Background(), store.path, Options{Clock: clock})
	require.NoError(t, err)
	_, err = reopened.ManagedAgentArchive(context.Background(), key)
	require.ErrorIs(t, err, sql.ErrNoRows)
	require.NoError(t, reopened.Close())
}
