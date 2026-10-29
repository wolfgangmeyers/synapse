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
	_, err = durable.db.ExecContext(ctx, `INSERT INTO managed_stock_pi_session_reservations (
		reservation_id,agent_id,session_epoch,launch_operation_id,root_relative_path,path_sha256,pi_session_id,state,session_artifact_id,created_at,updated_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, "termination-reservation", agent.AgentID, agent.SessionEpoch, agent.LaunchOperationID, artifact.RootRelativePath, artifact.PathSHA256, agent.PiSessionID, "materialized", artifact.ArtifactID, artifact.DiscoveredAt, artifact.CleanupUpdatedAt)
	require.NoError(t, err)
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

func TestLatestRetryableManagedTerminationExcludesSupersededSuccessorPredecessors(t *testing.T) {
	durable, ctx, agent, _ := restartFixture(t)
	source := restartFenceFromAgent(&agent)
	firstID, retryID := lifecycleUUID(962), lifecycleUUID(963)
	firstOperation := ManagedOperation{OperationID: firstID, Method: "DELETE", CanonicalPath: "/v1/managed-agents/restartable", RequestID: firstID, CanonicalBodyHash: testManifestHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	first, err := durable.ReserveManagedLifecycle(ctx, ManagedLifecycleReservation{AgentID: agent.AgentID, Kind: "terminate", Source: "direct", Method: firstOperation.Method, CanonicalPath: firstOperation.CanonicalPath, RequestID: firstID, CanonicalBodyHash: firstOperation.CanonicalBodyHash, Fence: source, Operation: firstOperation})
	require.NoError(t, err)
	require.NoError(t, durable.AdvanceManagedTermination(ctx, firstID, "terminating", "abort", "terminating", "terminate_reap", &source))
	require.NoError(t, durable.FailManagedTermination(ctx, firstID, "TERMINATION_CLEANUP_FAILED", true, &source))
	retryOperation := ManagedOperation{OperationID: retryID, Method: "DELETE", CanonicalPath: firstOperation.CanonicalPath, RequestID: retryID, CanonicalBodyHash: testManifestHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	retry, err := durable.ReserveManagedLifecycle(ctx, ManagedLifecycleReservation{AgentID: agent.AgentID, Kind: "terminate", Source: "direct", Method: retryOperation.Method, CanonicalPath: retryOperation.CanonicalPath, RequestID: retryID, CanonicalBodyHash: retryOperation.CanonicalBodyHash, Fence: source, RetryOf: first.Action.ActionID, Operation: retryOperation})
	require.NoError(t, err)
	require.NoError(t, durable.CompleteManagedLifecycleAction(ctx, ManagedLifecycleTerminalization{ActionID: retry.Action.ActionID, ExpectedState: retry.Action.State, ExpectedStage: retry.Action.Stage, TerminalState: "superseded", Code: "SUPERSEDED_BY_HIGHER_ACTION", Message: "lifecycle action superseded by a higher priority action", Fence: &source}))

	_, err = durable.LatestRetryableManagedTermination(ctx, agent.AgentID)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestConfirmedTerminationImmediatelyReleasesTheNameForRelaunch(t *testing.T) {
	durable, ctx, agent, action := terminationActionAtDurableWrite(t)
	_, err := durable.RecordManagedCompleteHistory(ctx, agent.AgentID, "managed_control", "info", "Updated goal", "termination-history")
	require.NoError(t, err)
	_, err = durable.RecordManagedCompleteHistory(ctx, "unrelated-agent", "managed_control", "info", "Unrelated history", "unrelated-history")
	require.NoError(t, err)

	// Populate conversation_timeline for terminated agent and an unrelated agent
	content1 := "timeline message for terminated agent"
	roleUser := "user"
	_, err = durable.RecordConversationTimelineTx(ctx, "timeline-term-1", agent.AgentID, "message", &roleUser, &content1, nil, 1000)
	require.NoError(t, err)

	// Populate scratchpad_entries and agent_progress_history
	_, err = durable.db.ExecContext(ctx, `INSERT INTO scratchpad_entries (id, agent_id, content, pinned, created_at, updated_at) VALUES ('sp-1', ?, 'note', 0, '2030-01-01T00:00:00Z', '2030-01-01T00:00:00Z')`, agent.AgentID)
	require.NoError(t, err)
	_, err = durable.db.ExecContext(ctx, `INSERT INTO agent_progress_history (agent_id, percent, note, created_at) VALUES (?, 50, 'halfway', '2030-01-01T00:00:00Z')`, agent.AgentID)
	require.NoError(t, err)

	_, err = durable.db.ExecContext(ctx, `INSERT INTO conversation_messages (message_id,message_incarnation_id,delivery_message_id,sender,recipient,participant_a,participant_b,body,created_at,recorded_at)
		VALUES ('terminated-conversation','terminated-incarnation','terminated-delivery',?,?,?,?,?,?,?),
		       ('unrelated-conversation','unrelated-incarnation','unrelated-delivery','unrelated-a','unrelated-b','unrelated-a','unrelated-b','unrelated',?,?)`,
		agent.AgentID, "peer", agent.AgentID, "peer", "terminated", "2030-01-01T00:00:00.000Z", "2030-01-01T00:00:00.000Z", "2030-01-01T00:00:00.000Z", "2030-01-01T00:00:00.000Z")
	require.NoError(t, err)
	require.NoError(t, durable.RetireManagedRecipientSourceForTermination(ctx, agent.AgentID, agent.Name))
	require.NoError(t, durable.CompleteManagedTermination(ctx, action.ActionID))
	var completeHistory, conversationHistory, historyBoundaries, historyIncarnations int
	var timelineHistory, scratchpadEntries, progressHistory int
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_complete_history WHERE agent_id=?`, agent.AgentID).Scan(&completeHistory))
	require.Equal(t, 0, completeHistory, "termination purges complete history owned by the terminated agent")
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversation_messages WHERE sender=? OR recipient=?`, agent.AgentID, agent.AgentID).Scan(&conversationHistory))
	require.Equal(t, 0, conversationHistory, "termination purges conversation history owned by the terminated agent")
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversation_timeline WHERE agent_id=?`, agent.AgentID).Scan(&timelineHistory))
	require.Equal(t, 0, timelineHistory, "termination purges conversation timeline entries owned by the terminated agent")
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM scratchpad_entries WHERE agent_id=?`, agent.AgentID).Scan(&scratchpadEntries))
	require.Equal(t, 0, scratchpadEntries, "termination purges scratchpad entries owned by the terminated agent")
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_progress_history WHERE agent_id=?`, agent.AgentID).Scan(&progressHistory))
	require.Equal(t, 0, progressHistory, "termination purges progress history owned by the terminated agent")
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_history_boundaries WHERE agent_id=?`, agent.AgentID).Scan(&historyBoundaries))
	require.Equal(t, 0, historyBoundaries, "termination removes the prior history boundary")
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_history_incarnations WHERE agent_id=?`, agent.AgentID).Scan(&historyIncarnations))
	require.Equal(t, 0, historyIncarnations, "termination removes prior history incarnations")
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_complete_history WHERE agent_id='unrelated-agent'`).Scan(&completeHistory))
	require.Equal(t, 1, completeHistory, "termination must not purge another agent's activity history")
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversation_timeline WHERE agent_id='unrelated-agent'`).Scan(&timelineHistory))
	require.Equal(t, 1, timelineHistory, "termination must not purge another agent's conversation timeline")
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversation_messages WHERE sender='unrelated-a' OR recipient='unrelated-a'`).Scan(&conversationHistory))
	require.Equal(t, 1, conversationHistory, "termination must not purge another agent's conversation history")

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
	page, err := durable.ManagedHistoryPage(ctx, replacement.AgentID, "complete", 0, 10)
	require.NoError(t, err)
	require.Empty(t, page.Entries, "a replacement agent must not inherit the prior Complete timeline")
	conversation, err := durable.ConversationHistory(ctx, replacement.AgentID, "peer")
	require.NoError(t, err)
	require.Empty(t, conversation, "a replacement agent must not inherit the prior conversation")
	timelineEvents, _, err := durable.GetConversationTimeline(ctx, replacement.AgentID, 50, nil)
	require.NoError(t, err)
	require.Empty(t, timelineEvents, "a replacement agent must start with completely empty timeline history")

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
			retained, err := durable.ManagedAgent(ctx, agent.AgentID)
			require.NoError(t, err, "the transaction must retain the retry tombstone and agent")
			require.True(t, retained.SlotHeld)
			artifact, err := durable.ManagedSessionArtifact(ctx, lifecycleUUID(951))
			require.NoError(t, err)
			require.Equal(t, "unlinked", artifact.CleanupState)
			var reservations int
			require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_stock_pi_session_reservations WHERE agent_id=?`, agent.AgentID).Scan(&reservations))
			require.Equal(t, 1, reservations)

			durable.managedTerminationFault = nil
			require.NoError(t, durable.CompleteManagedTermination(ctx, action.ActionID))
			_, err = durable.ManagedAgent(ctx, agent.AgentID)
			require.ErrorIs(t, err, sql.ErrNoRows)
			require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_stock_pi_session_reservations WHERE agent_id=?`, agent.AgentID).Scan(&reservations))
			require.Zero(t, reservations)
			stored, err = durable.ManagedLifecycleAction(ctx, action.ActionID)
			require.NoError(t, err)
			require.Equal(t, "succeeded", stored.State)
		})
	}
}
