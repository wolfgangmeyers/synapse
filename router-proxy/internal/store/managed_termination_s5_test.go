package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func terminationActionAtDurableWrite(t *testing.T) (*Store, context.Context, ManagedAgentRecord, ManagedLifecycleAction) {
	t.Helper()
	durable, ctx, agent, _ := restartFixture(t)
	requestID := lifecycleUUID(950)
	source := restartFenceFromAgent(&agent)
	operation := ManagedOperation{OperationID: requestID, Method: "DELETE", CanonicalPath: "/v1/managed-agents/restartable", RequestID: requestID, CanonicalBodyHash: testManifestHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	admission, err := durable.ReserveManagedLifecycle(ctx, ManagedLifecycleReservation{AgentID: agent.AgentID, Kind: "terminate", Source: "direct", Method: operation.Method, CanonicalPath: operation.CanonicalPath, RequestID: requestID, CanonicalBodyHash: operation.CanonicalBodyHash, Fence: source, Operation: operation})
	require.NoError(t, err)
	require.NoError(t, durable.AdvanceManagedTermination(ctx, requestID, "terminating", "abort", "terminating", "terminate_reap", &source))
	require.NoError(t, durable.AdvanceManagedTermination(ctx, requestID, "terminating", "terminate_reap", "cleanup", "direct_mail_retire", &source))
	require.NoError(t, durable.AdvanceManagedTermination(ctx, requestID, "cleanup", "direct_mail_retire", "cleanup", "agent_cleanup", &source))
	artifact := ManagedSessionArtifact{ArtifactID: lifecycleUUID(951), AgentID: agent.AgentID, PiSessionID: agent.PiSessionID, SessionEpoch: agent.SessionEpoch, RootRelativePath: "sessions/terminated.json", PathSHA256: testManifestHash, DiscoveryState: "not_materialized", CleanupState: "retained", DiscoveredAt: "2026-09-08T00:00:00.000Z", CleanupUpdatedAt: "2026-09-08T00:00:00.000Z"}
	require.NoError(t, durable.RecordManagedSessionArtifact(ctx, artifact))
	require.NoError(t, durable.MarkManagedSessionArtifactCleanup(ctx, artifact.ArtifactID, "retained", "pending"))
	require.NoError(t, durable.MarkManagedSessionArtifactCleanup(ctx, artifact.ArtifactID, "pending", "unlinked"))
	require.NoError(t, durable.AdvanceManagedTermination(ctx, requestID, "cleanup", "agent_cleanup", "cleanup", "durable_write", &source))
	return durable, ctx, agent, *admission.Action
}

func TestTerminateCancelsRootControlInItsAdmissionTransaction(t *testing.T) {
	durable, ctx, agent, _ := restartFixture(t)
	source := restartFenceFromAgent(&agent)
	input := lifecycleControlInput(954, agent.AgentID, source)
	_, err := durable.ReserveManagedControlRequest(ctx, input)
	require.NoError(t, err)
	terminateID := lifecycleUUID(953)
	operation := ManagedOperation{OperationID: terminateID, Method: "DELETE", CanonicalPath: "/v1/managed-agents/restartable", RequestID: terminateID, CanonicalBodyHash: testManifestHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	_, err = durable.ReserveManagedLifecycle(ctx, ManagedLifecycleReservation{AgentID: agent.AgentID, Kind: "terminate", Source: "direct", Method: operation.Method, CanonicalPath: operation.CanonicalPath, RequestID: terminateID, CanonicalBodyHash: operation.CanonicalBodyHash, Fence: source, Operation: operation})
	require.NoError(t, err)
	control, err := durable.ManagedControlRequest(ctx, input.RequestID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", control.State)
	require.Equal(t, "CANCELLED_BY_TERMINATE", control.ErrorCode)
}

func TestTerminateCancelsAnUnstartedResetBeforeExternalWork(t *testing.T) {
	durable, ctx, agent, _ := restartFixture(t)
	resetID := lifecycleUUID(955)
	reset, err := durable.ReserveManagedReset(ctx, resetReservationForTest(agent, resetID))
	require.NoError(t, err)
	source := restartFenceFromAgent(&agent)
	terminateID := lifecycleUUID(956)
	operation := ManagedOperation{OperationID: terminateID, Method: "DELETE", CanonicalPath: "/v1/managed-agents/restartable", RequestID: terminateID, CanonicalBodyHash: testManifestHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	admission, err := durable.ReserveManagedLifecycle(ctx, ManagedLifecycleReservation{AgentID: agent.AgentID, Kind: "terminate", Source: "direct", Method: operation.Method, CanonicalPath: operation.CanonicalPath, RequestID: terminateID, CanonicalBodyHash: operation.CanonicalBodyHash, Fence: source, Operation: operation})
	require.NoError(t, err)
	require.Equal(t, "terminating", admission.Action.State)
	lower, err := durable.ManagedLifecycleAction(ctx, reset.Action.ActionID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", lower.State)
	require.Equal(t, "CANCELLED_BY_HIGHER_ACTION", lower.TerminalCode)
}

func TestManagedTerminationRetryResumesItsExactCleanupStage(t *testing.T) {
	durable, ctx, agent, _ := restartFixture(t)
	source := restartFenceFromAgent(&agent)
	firstID, retryID := lifecycleUUID(960), lifecycleUUID(961)
	firstOperation := ManagedOperation{OperationID: firstID, Method: "DELETE", CanonicalPath: "/v1/managed-agents/restartable", RequestID: firstID, CanonicalBodyHash: testManifestHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	first, err := durable.ReserveManagedLifecycle(ctx, ManagedLifecycleReservation{AgentID: agent.AgentID, Kind: "terminate", Source: "direct", Method: firstOperation.Method, CanonicalPath: firstOperation.CanonicalPath, RequestID: firstID, CanonicalBodyHash: firstOperation.CanonicalBodyHash, Fence: source, Operation: firstOperation})
	require.NoError(t, err)
	require.NoError(t, durable.AdvanceManagedTermination(ctx, firstID, "terminating", "abort", "terminating", "terminate_reap", &source))
	require.NoError(t, durable.AdvanceManagedTermination(ctx, firstID, "terminating", "terminate_reap", "cleanup", "direct_mail_retire", &source))
	require.NoError(t, durable.AdvanceManagedTermination(ctx, firstID, "cleanup", "direct_mail_retire", "cleanup", "agent_cleanup", &source))
	require.NoError(t, durable.FailManagedTermination(ctx, firstID, "TERMINATION_CLEANUP_FAILED", true, &source))
	retryOperation := ManagedOperation{OperationID: retryID, Method: "DELETE", CanonicalPath: firstOperation.CanonicalPath, RequestID: retryID, CanonicalBodyHash: testManifestHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	retry, err := durable.ReserveManagedLifecycle(ctx, ManagedLifecycleReservation{AgentID: agent.AgentID, Kind: "terminate", Source: "direct", Method: retryOperation.Method, CanonicalPath: retryOperation.CanonicalPath, RequestID: retryID, CanonicalBodyHash: retryOperation.CanonicalBodyHash, Fence: source, RetryOf: first.Action.ActionID, Operation: retryOperation})
	require.NoError(t, err)
	require.Equal(t, "cleanup", retry.Action.State)
	require.Equal(t, "agent_cleanup", retry.Action.Stage)
}

func TestConfirmedTerminationImmediatelyReleasesTheNameForRelaunch(t *testing.T) {
	durable, ctx, agent, action := terminationActionAtDurableWrite(t)
	_, err := durable.RecordManagedCompleteHistory(ctx, agent.AgentID, "managed_control", "info", "Updated goal", "termination-history")
	require.NoError(t, err)
	require.NoError(t, durable.RetireManagedRecipientSourceForTermination(ctx, agent.AgentID, agent.Name))
	require.NoError(t, durable.CompleteManagedTermination(ctx, action.ActionID))
	var completeHistory int
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_complete_history WHERE agent_id=?`, agent.AgentID).Scan(&completeHistory))
	require.Zero(t, completeHistory, "a replacement agent must not inherit the prior Complete timeline")

	replacement := agent
	replacement.PiSessionID = ""
	replacement.ProcessPID = 0
	replacement.ProcessStartMarker = ""
	replacement.ProcessIncarnation = ""
	replacement.RuntimeGeneration = ""
	replacement.DesiredState = "running"
	replacement.LifecycleState = "starting"
	replacement.ActivityState = "starting"
	replacement.AcceptingPrompts = false
	replacement.CreatedAt = ""
	replacement.StartedAt = ""
	replacement.UpdatedAt = ""
	replacementOperationID := lifecycleUUID(952)
	replacementOperation := ManagedOperation{OperationID: replacementOperationID, Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: replacementOperationID, CanonicalBodyHash: testManifestHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, replacement, replacementOperation))

	source, err := durable.RecipientSource(ctx, agent.Name)
	require.NoError(t, err)
	require.Equal(t, "managed", source.SourceKind)
	require.Equal(t, agent.AgentID, source.OwnerIdentity)
	require.Equal(t, "reserved", source.State)
}

func TestCompleteManagedTerminationRollsBackEveryDurableBoundary(t *testing.T) {
	for _, stage := range []string{"before_artifact_check", "before_lifecycle_cleanup", "before_operation_cleanup", "before_agent_delete", "before_terminalize"} {
		t.Run(stage, func(t *testing.T) {
			durable, ctx, agent, action := terminationActionAtDurableWrite(t)
			durable.managedTerminationFault = func(actual string) error {
				if actual == stage {
					return errors.New("injected termination durable failure")
				}
				return nil
			}
			require.Error(t, durable.CompleteManagedTermination(ctx, action.ActionID))
			stored, err := durable.ManagedLifecycleAction(ctx, action.ActionID)
			require.NoError(t, err)
			require.Equal(t, "cleanup", stored.State)
			require.Equal(t, "durable_write", stored.Stage)
			_, err = durable.ManagedAgent(ctx, agent.AgentID)
			require.NoError(t, err, "the transaction must retain the retry tombstone and agent")
			artifact, err := durable.ManagedSessionArtifact(ctx, lifecycleUUID(951))
			require.NoError(t, err)
			require.Equal(t, "unlinked", artifact.CleanupState)

			durable.managedTerminationFault = nil
			require.NoError(t, durable.CompleteManagedTermination(ctx, action.ActionID))
			_, err = durable.ManagedAgent(ctx, agent.AgentID)
			require.ErrorIs(t, err, sql.ErrNoRows)
			stored, err = durable.ManagedLifecycleAction(ctx, action.ActionID)
			require.NoError(t, err)
			require.Equal(t, "succeeded", stored.State)
		})
	}
}
