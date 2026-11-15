package store

import (
	"context"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestDistinctFinalAdmissionClosesAtOriginalUserDeadline(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	receiptForEvaluation(t, s, b, &k)
	var admitted int64
	require.NoError(t, s.db.QueryRow(`SELECT retention_admitted_at_ms FROM embedded_entries WHERE agent_id=? AND seq=?`, b.AgentID, k.UserSeq).Scan(&admitted))
	s.clock = deterministic.NewFixedClock(time.UnixMilli(admitted).Add(30 * 24 * time.Hour))
	final := receiptSDKAppend(t, s, b, ai.AssistantMessage{StopReason: ai.StopStop})
	k.FinalSeq, k.FinalDigest = final.Sequence, final.Digest
	_, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.ErrorIs(t, err, ErrEmbeddedEvidenceExpired)
	require.False(t, claimed)
}

func TestFreshWorkflowFinalAfterUserDeadline(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	wf, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: "long workflow"})
	require.NoError(t, err)
	st, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "active", WorkflowID: &wf.ID})
	require.NoError(t, err)
	require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, b.AgentID, &wf.ID, &st.ID, nil))
	receiptForEvaluation(t, s, b, &k)
	old := k
	_, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, old)
	require.NoError(t, err)
	require.True(t, claimed)
	var admitted int64
	require.NoError(t, s.db.QueryRow(`SELECT retention_admitted_at_ms FROM embedded_entries WHERE agent_id=? AND seq=?`, b.AgentID, k.UserSeq).Scan(&admitted))
	s.clock = deterministic.NewFixedClock(time.UnixMilli(admitted).Add(30 * 24 * time.Hour))
	final := receiptSDKAppend(t, s, b, ai.AssistantMessage{StopReason: ai.StopStop})
	k.FinalSeq, k.FinalDigest = final.Sequence, final.Digest
	_, claimed, err = s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	require.True(t, claimed)
	_, changed, err := s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "fresh")
	require.NoError(t, err)
	require.True(t, changed)
	_, claimed, err = s.ClaimEmbeddedGoalEvaluation(ctx, b, old)
	require.Error(t, err)
	require.False(t, claimed)
}

func TestFreshWorkflowFinalAfterReceiptPruned(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	wf, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: "long workflow"})
	require.NoError(t, err)
	st, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "active", WorkflowID: &wf.ID})
	require.NoError(t, err)
	require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, b.AgentID, &wf.ID, &st.ID, nil))
	receiptForEvaluation(t, s, b, &k)
	var admitted int64
	require.NoError(t, s.db.QueryRow(`SELECT retention_admitted_at_ms FROM embedded_entries WHERE agent_id=? AND seq=?`, b.AgentID, k.UserSeq).Scan(&admitted))
	s.clock = deterministic.NewFixedClock(time.UnixMilli(admitted).Add(30 * 24 * time.Hour))
	require.NoError(t, s.runImmediate(ctx, func() error { return s.pruneEmbeddedEvidenceTx(ctx) }))
	final := receiptSDKAppend(t, s, b, ai.AssistantMessage{StopReason: ai.StopStop})
	k.FinalSeq, k.FinalDigest = final.Sequence, final.Digest
	_, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	require.True(t, claimed)
}

func TestInterruptedOlderVerdictCannotSettleAfterNewFinal(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	receiptForEvaluation(t, s, b, &k)
	_, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	require.True(t, claimed)
	receiptSDKAppend(t, s, b, ai.AssistantMessage{StopReason: ai.StopStop})
	_, changed, err := s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "stale")
	require.Error(t, err)
	require.False(t, changed)
}

func TestRecoverInterruptedWorkflowCheckFences(t *testing.T) {
	for _, mode := range []string{"recover", "starting", "evaluator-entered", "same-owner", "stale-final", "workflow-exited", "started-effect", "expired", "rollback", "queued-child", "running-child"} {
		t.Run(mode, func(t *testing.T) {
			s, b, k := evaluationFixture(t)
			defer s.Close()
			ctx := context.Background()
			wf, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: "recovery"})
			require.NoError(t, err)
			st, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "work", WorkflowID: &wf.ID})
			require.NoError(t, err)
			require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, b.AgentID, &wf.ID, &st.ID, nil))
			receiptForEvaluation(t, s, b, &k)
			_, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
			require.NoError(t, err)
			require.True(t, claimed)
			if mode == "queued-child" || mode == "running-child" {
				child := ManagedSubagentRecord{AgentID: "child", ParentAgentID: b.AgentID, Description: "work", SubagentType: "general-purpose", Status: "queued"}
				require.NoError(t, s.CreateManagedSubagent(ctx, b, child))
				if mode == "running-child" {
					require.NoError(t, s.UpdateManagedSubagent(ctx, b, child.AgentID, "queued", ManagedSubagentResult{Status: "running"}))
				}
			}
			if mode == "evaluator-entered" {
				_, err = s.EnterEmbeddedGoalEvaluationExecution(ctx, b, k)
				require.NoError(t, err)
			}
			if mode == "expired" {
				var admitted int64
				require.NoError(t, s.db.QueryRow(`SELECT retention_basis_ms FROM embedded_goal_evaluations WHERE agent_id=? AND final_seq=?`, b.AgentID, k.FinalSeq).Scan(&admitted))
				s.clock = deterministic.NewFixedClock(time.UnixMilli(admitted).Add(30 * 24 * time.Hour))
			}
			if mode == "rollback" {
				_, err = s.db.Exec(`CREATE TEMP TRIGGER fail_recovery_reserve BEFORE INSERT ON main.embedded_goal_continuations BEGIN SELECT RAISE(ABORT,'injected reservation failure'); END`)
				require.NoError(t, err)
			}
			if mode == "started-effect" {
				_, err = s.EnterEmbeddedSDKExecution(ctx, b, nil)
				require.NoError(t, err)
			}
			if mode == "stale-final" {
				redAppend(t, s, b, "user", "new work")
			}
			if mode == "workflow-exited" {
				require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, b.AgentID, nil, nil, nil))
			}
			if mode == "starting" {
				_, err = s.db.Exec(`INSERT INTO managed_http_operations(operation_id,session_identity_hash,method,canonical_path,request_id,canonical_body_hash,state,response_status,response_headers_json,response_body,created_at) VALUES('preclaim-launch','preclaim','POST','/launch','preclaim','hash','pending',202,'{}','{}','2025-01-01')`)
				require.NoError(t, err)
				_, err = s.db.Exec(`UPDATE managed_agents SET lifecycle_state='starting',launch_operation_id='preclaim-launch' WHERE agent_id=?`, b.AgentID)
				require.NoError(t, err)
			}
			next := b
			if mode != "same-owner" {
				next.OwnerToken, err = s.AcquireEmbeddedOwner(ctx, b, "successor", 0)
				require.NoError(t, err)
				require.Greater(t, next.OwnerToken, b.OwnerToken)
			}
			changed, err := s.RecoverInterruptedWorkflowCheck(ctx, next, k)
			if mode == "queued-child" || mode == "running-child" {
				require.NoError(t, err)
				require.False(t, changed)
				keys, err := s.InterruptedWorkflowChecks(ctx, next)
				require.NoError(t, err)
				require.Empty(t, keys)
				var state string
				require.NoError(t, s.db.QueryRow(`SELECT state FROM embedded_goal_evaluations WHERE agent_id=?`, b.AgentID).Scan(&state))
				require.Equal(t, EmbeddedGoalEvaluationUnknown, state)
				return
			}
			if mode != "recover" && mode != "starting" && mode != "evaluator-entered" {
				require.Error(t, err)
				require.False(t, changed)
				if mode == "rollback" {
					var state string
					require.NoError(t, s.db.QueryRow(`SELECT state FROM embedded_goal_evaluations WHERE agent_id=?`, b.AgentID).Scan(&state))
					require.Equal(t, EmbeddedGoalEvaluationUnknown, state)
				}
				return
			}
			require.NoError(t, err)
			require.True(t, changed)
			_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "late")
			require.Error(t, err)
			c, err := s.EmbeddedGoalContinuationFor(ctx, next, k)
			require.NoError(t, err)
			require.Equal(t, "reserved", c.State)
			if mode == "starting" {
				_, err = s.db.Exec(`UPDATE managed_agents SET lifecycle_state='running' WHERE agent_id=?`, b.AgentID)
				require.NoError(t, err)
			}
			_, started, err := s.StartEmbeddedGoalContinuation(ctx, next, k)
			require.NoError(t, err)
			require.True(t, started)
			_, started, err = s.StartEmbeddedGoalContinuation(ctx, next, k)
			require.NoError(t, err)
			require.False(t, started, "ambiguous started delivery is never replayed")
			changed, err = s.RecoverInterruptedWorkflowCheck(ctx, next, k)
			require.NoError(t, err)
			require.False(t, changed)
		})
	}
}

func TestRecoverPreclaimWorkflowFinalFences(t *testing.T) {
	for _, mode := range []string{"recover", "starting", "stale-owner", "new-head", "wrong-execution", "no-execution", "workflow-exited", "goal-disabled", "expired", "rollback"} {
		t.Run(mode, func(t *testing.T) {
			s, b, k := evaluationFixture(t)
			defer s.Close()
			ctx := context.Background()
			wf, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: "preclaim"})
			require.NoError(t, err)
			st, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "work", WorkflowID: &wf.ID})
			require.NoError(t, err)
			require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, b.AgentID, &wf.ID, &st.ID, nil))
			receiptForEvaluation(t, s, b, &k)
			token, err := s.EnterEmbeddedSDKExecution(ctx, b, nil)
			require.NoError(t, err)
			require.NoError(t, s.SetEmbeddedSDKExecutionUser(ctx, b, token, k.UserSeq, k.UserDigest))
			if mode == "no-execution" {
				require.NoError(t, s.ClearEmbeddedExecution(ctx, b, token))
			}
			if mode == "wrong-execution" {
				require.NoError(t, s.ClearEmbeddedExecution(ctx, b, token))
				_, err = s.EnterEmbeddedSDKExecution(ctx, b, nil)
				require.NoError(t, err)
			}
			if mode == "new-head" {
				redAppend(t, s, b, "user", "new work")
			}
			if mode == "workflow-exited" {
				require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, b.AgentID, nil, nil, nil))
			}
			if mode == "goal-disabled" {
				_, err = s.UpdateManagedGoalConfig(ctx, b.AgentID, ManagedGoalConfig{Enabled: false, Settings: DefaultManagedGoalSettings()})
				require.NoError(t, err)
			}
			if mode == "expired" {
				var admitted int64
				require.NoError(t, s.db.QueryRow(`SELECT retention_admitted_at_ms FROM embedded_entries WHERE agent_id=? AND seq=?`, b.AgentID, k.FinalSeq).Scan(&admitted))
				s.clock = deterministic.NewFixedClock(time.UnixMilli(admitted).Add(30 * 24 * time.Hour))
			}
			if mode == "rollback" {
				_, err = s.db.Exec(`CREATE TEMP TRIGGER fail_preclaim_reserve BEFORE INSERT ON main.embedded_goal_continuations BEGIN SELECT RAISE(ABORT,'injected'); END`)
				require.NoError(t, err)
			}
			if mode == "starting" {
				_, err = s.db.Exec(`INSERT INTO managed_http_operations(operation_id,session_identity_hash,method,canonical_path,request_id,canonical_body_hash,state,response_status,response_headers_json,response_body,created_at) VALUES('preclaim-launch','preclaim','POST','/launch','preclaim','hash','pending',202,'{}','{}','2025-01-01')`)
				require.NoError(t, err)
				_, err = s.db.Exec(`UPDATE managed_agents SET lifecycle_state='starting',launch_operation_id='preclaim-launch' WHERE agent_id=?`, b.AgentID)
				require.NoError(t, err)
			}
			next := b
			next.OwnerToken, err = s.AcquireEmbeddedOwner(ctx, b, "successor", 0)
			require.NoError(t, err)
			if mode == "stale-owner" {
				next = b
			}
			got, found, err := s.RecoverPreclaimWorkflowCheck(ctx, next)
			if mode == "recover" || mode == "starting" {
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, k, got)
				c, err := s.EmbeddedGoalContinuationFor(ctx, next, k)
				require.NoError(t, err)
				require.Equal(t, "reserved", c.State)
				_, again, err := s.RecoverPreclaimWorkflowCheck(ctx, next)
				require.NoError(t, err)
				require.False(t, again)
				return
			}
			require.False(t, found)
			if mode == "new-head" || mode == "workflow-exited" || mode == "no-execution" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			var count int
			require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_goal_evaluations WHERE agent_id=?`, b.AgentID).Scan(&count))
			require.Zero(t, count)
		})
	}
}

func TestPreclaimRecoveryAdditionalFences(t *testing.T) {
	for _, mode := range []string{"missing-receipt", "conflicting-receipt", "queued-child", "running-child", "changed-goal", "existing-claim", "existing-effect"} {
		t.Run(mode, func(t *testing.T) {
			s, b, k := evaluationFixture(t)
			defer s.Close()
			ctx := context.Background()
			wf, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: "preclaim fences"})
			require.NoError(t, err)
			st, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "work", WorkflowID: &wf.ID})
			require.NoError(t, err)
			require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, b.AgentID, &wf.ID, &st.ID, nil))
			if mode == "missing-receipt" {
				final := receiptSDKAppend(t, s, b, ai.AssistantMessage{StopReason: ai.StopStop})
				k.FinalSeq, k.FinalDigest = final.Sequence, final.Digest
			} else {
				receiptForEvaluation(t, s, b, &k)
			}
			token, err := s.EnterEmbeddedSDKExecution(ctx, b, nil)
			require.NoError(t, err)
			// A missing receipt cannot be installed as the execution's current user via
			// the public API, so model the durable crash evidence directly in that case.
			if mode == "missing-receipt" {
				e, _, err := s.embeddedExecutionRowTx(ctx, b)
				require.NoError(t, err)
				e.Current = &embeddedExecutionClosure{EmbeddedGoalEvaluationKey: EmbeddedGoalEvaluationKey{UserSeq: k.UserSeq, UserDigest: k.UserDigest, GoalRevision: k.GoalRevision}}
				require.NoError(t, s.saveEmbeddedExecutionTx(ctx, b, e))
			} else {
				require.NoError(t, s.SetEmbeddedSDKExecutionUser(ctx, b, token, k.UserSeq, k.UserDigest))
			}
			switch mode {
			case "conflicting-receipt":
				_, err = s.db.Exec(`DROP TRIGGER embedded_goal_receipt_immutable_update`)
				require.NoError(t, err)
				_, err = s.db.Exec(`UPDATE embedded_goal_applied_receipts SET turn_entry_digest='conflicting' WHERE agent_id=?`, b.AgentID)
				require.NoError(t, err)
			case "queued-child", "running-child":
				child := ManagedSubagentRecord{AgentID: "child", ParentAgentID: b.AgentID, Description: "work", SubagentType: "general-purpose", Status: "queued"}
				require.NoError(t, s.CreateManagedSubagent(ctx, b, child))
				if mode == "running-child" {
					require.NoError(t, s.UpdateManagedSubagent(ctx, b, child.AgentID, "queued", ManagedSubagentResult{Status: "running"}))
				}
			case "changed-goal":
				goalAPIPolicy(t, s, b, true, "changed")
			case "existing-claim", "existing-effect":
				_, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
				require.NoError(t, err)
				require.True(t, claimed)
				if mode == "existing-effect" {
					_, changed, err := s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "existing effect")
					require.NoError(t, err)
					require.True(t, changed)
				}
			}
			next := b
			next.OwnerToken, err = s.AcquireEmbeddedOwner(ctx, b, "successor", 0)
			require.NoError(t, err)
			_, found, err := s.RecoverPreclaimWorkflowCheck(ctx, next)
			require.False(t, found)
			if mode == "existing-claim" || mode == "existing-effect" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			var evaluations, continuations int
			require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_goal_evaluations WHERE agent_id=?`, b.AgentID).Scan(&evaluations))
			require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_goal_continuations WHERE agent_id=?`, b.AgentID).Scan(&continuations))
			if mode == "existing-claim" || mode == "existing-effect" {
				require.Equal(t, 1, evaluations)
			} else {
				require.Zero(t, evaluations)
			}
			if mode == "existing-effect" {
				require.Equal(t, 1, continuations)
			} else {
				require.Zero(t, continuations)
			}
		})
	}
}

func TestInterruptedWorkflowCandidateDiscovery(t *testing.T) {
	for _, mode := range []string{"current", "superseded", "standalone"} {
		t.Run(mode, func(t *testing.T) {
			s, b, k := evaluationFixture(t)
			defer s.Close()
			ctx := context.Background()
			if mode != "standalone" {
				wf, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: "recovery"})
				require.NoError(t, err)
				st, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "work", WorkflowID: &wf.ID})
				require.NoError(t, err)
				require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, b.AgentID, &wf.ID, &st.ID, nil))
			}
			receiptForEvaluation(t, s, b, &k)
			_, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
			require.NoError(t, err)
			require.True(t, claimed)
			if mode == "superseded" {
				redAppend(t, s, b, "user", "new work")
			}
			next := b
			next.OwnerToken, err = s.AcquireEmbeddedOwner(ctx, b, "successor", 0)
			require.NoError(t, err)
			keys, err := s.InterruptedWorkflowChecks(ctx, next)
			require.NoError(t, err)
			if mode == "current" {
				require.Equal(t, []EmbeddedGoalEvaluationKey{k}, keys)
			} else {
				require.Empty(t, keys)
			}
			var state string
			require.NoError(t, s.db.QueryRow(`SELECT state FROM embedded_goal_evaluations WHERE agent_id=?`, b.AgentID).Scan(&state))
			require.Equal(t, EmbeddedGoalEvaluationUnknown, state)
		})
	}
}
