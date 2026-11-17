package store

import (
	"context"
	"database/sql"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestRecoverySnoozeAtomicPolicy(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[fail], func(t *testing.T) {
			s, _, b, turn, _ := snoozeFixture(t)
			ctx := context.Background()
			if fail {
				_, err := s.db.Exec(`CREATE TRIGGER recovery_policy_fail BEFORE UPDATE OF snoozed_until ON managed_agent_policies BEGIN SELECT RAISE(ABORT,'policy write failed'); END`)
				require.NoError(t, err)
			}
			p, _, err := s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "atomic", []byte(`{"duration_ms":60000}`))
			if fail {
				require.ErrorContains(t, err, "policy write failed")
				require.Zero(t, embeddedRedCount(t, s, "embedded_goal_snooze_calls"))
				require.Zero(t, embeddedRedCount(t, s, "embedded_goal_snooze_preferences"))
				return
			}
			require.NoError(t, err)
			var due sql.NullString
			require.NoError(t, s.db.QueryRow(`SELECT snoozed_until FROM managed_agent_policies WHERE agent_id=?`, b.AgentID).Scan(&due))
			require.True(t, due.Valid)
			require.Equal(t, p.DueAt, due.String)
		})
	}
}

func TestRecoverySavedDeadlineNotSettlement(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Now().UTC().Truncate(time.Millisecond))
	s.clock = clock
	p, _, err := s.SaveEmbeddedGoalSnoozePreference(ctx, b, EmbeddedGoalSnoozeTurn{UserSeq: k.UserSeq, UserDigest: k.UserDigest, GoalRevision: k.GoalRevision}, "saved", []byte(`{"duration_ms":60000}`))
	require.NoError(t, err)
	receiptForEvaluation(t, s, b, &k)
	_, _, err = s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	require.NoError(t, clock.Advance(time.Second))
	due := p.DueAt
	require.NoError(t, s.SetManagedGoalSnoozeUntil(ctx, b.AgentID, ""))
	_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "")
	require.NoError(t, err)
	c, err := s.EmbeddedGoalContinuationFor(ctx, b, k)
	require.NoError(t, err)
	require.Equal(t, due, c.DueAt.String)
}

func TestRecoveryPolicyCanonicalAndReadError(t *testing.T) {
	for _, mode := range []string{"offset", "read error", "malformed", "expired"} {
		t.Run(mode, func(t *testing.T) {
			s, b, k := evaluationFixture(t)
			defer s.Close()
			ctx := context.Background()
			due := s.clock.Now().Add(time.Minute).UTC().Truncate(time.Millisecond)
			value := due.In(time.FixedZone("offset", 3600)).Format(time.RFC3339Nano)
			if mode == "malformed" {
				value = "broken"
			}
			if mode == "expired" {
				value = formatTimestamp(s.clock.Now().Add(-time.Second))
			}
			require.NoError(t, s.SetManagedGoalSnoozeUntil(ctx, b.AgentID, value))
			receiptForEvaluation(t, s, b, &k)
			_, _, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
			require.NoError(t, err)
			if mode == "read error" {
				_, err = s.db.Exec(`ALTER TABLE managed_agent_policies RENAME COLUMN snoozed_until TO unavailable_due`)
				require.NoError(t, err)
			}
			_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "")
			if mode == "read error" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			c, err := s.EmbeddedGoalContinuationFor(ctx, b, k)
			require.NoError(t, err)
			if mode == "offset" {
				require.Equal(t, formatTimestamp(due), c.DueAt.String)
			} else {
				require.False(t, c.DueAt.Valid)
			}
		})
	}
}

func TestRecoveryLifecycleRetiresEvidence(t *testing.T) {
	for _, mode := range []string{"transition", "exit", "deactivate", "rollback"} {
		t.Run(mode, func(t *testing.T) {
			s, b, k := evaluationFixture(t)
			defer s.Close()
			ctx := context.Background()
			st, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "next"})
			require.NoError(t, err)
			p, _, err := s.SaveEmbeddedGoalSnoozePreference(ctx, b, EmbeddedGoalSnoozeTurn{UserSeq: k.UserSeq, UserDigest: k.UserDigest, GoalRevision: k.GoalRevision}, "lifecycle", []byte(`{"duration_ms":60000}`))
			require.NoError(t, err)
			receiptForEvaluation(t, s, b, &k)
			_, _, err = s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
			require.NoError(t, err)
			_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "")
			require.NoError(t, err)
			if mode == "rollback" {
				_, err = s.db.Exec(`CREATE TRIGGER recovery_cleanup_fail BEFORE UPDATE OF snoozed_until ON managed_agent_policies BEGIN SELECT RAISE(ABORT,'cleanup failed'); END`)
				require.NoError(t, err)
			}
			switch mode {
			case "exit":
				err = s.SetManagedAgentActiveWorkflowState(ctx, b.AgentID, nil)
			case "deactivate":
				err = s.SetManagedAgentActiveWorkflowWithVariables(ctx, b.AgentID, nil, nil, nil)
			default:
				err = s.SetManagedAgentActiveWorkflowState(ctx, b.AgentID, &st.ID)
			}
			if mode == "rollback" {
				require.ErrorContains(t, err, "cleanup failed")
				a, e := s.ManagedAgent(ctx, b.AgentID)
				require.NoError(t, e)
				require.Empty(t, a.ActiveWorkflowStateID)
				c, e := s.EmbeddedGoalContinuationFor(ctx, b, k)
				require.NoError(t, e)
				require.Equal(t, "reserved", c.State)
				var due string
				require.NoError(t, s.db.QueryRow(`SELECT snoozed_until FROM managed_agent_policies WHERE agent_id=?`, b.AgentID).Scan(&due))
				require.Equal(t, p.DueAt, due)
				return
			}
			require.NoError(t, err)
			var active int
			require.NoError(t, s.db.QueryRow(`SELECT active FROM embedded_goal_snooze_preferences WHERE call_id=?`, p.CallID).Scan(&active))
			require.Zero(t, active)
			c, err := s.EmbeddedGoalContinuationFor(ctx, b, k)
			require.NoError(t, err)
			require.Equal(t, "invalidated", c.State)
			policy, err := s.ManagedGoalPolicyFor(ctx, b.AgentID)
			require.NoError(t, err)
			require.Empty(t, policy.SnoozedUntil)
			require.Equal(t, k.GoalRevision, policy.DesiredRevision)
			if mode != "transition" {
				require.False(t, policy.Config.Enabled)
				require.Empty(t, policy.Config.Goal)
			} else {
				require.True(t, policy.Config.Enabled)
			}
		})
	}
}

func TestRecoveryWorkflowCatalog(t *testing.T) {
	require.True(t, ValidWorkflowTools["workflow_snooze"])
}

func TestRecoveryWorkflowIntentAndResultFences(t *testing.T) {
	for _, mode := range []string{"success", "custom default", "changed args", "partial result", "stale process", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			s, _, b, turn, _ := snoozeFixture(t)
			ctx := context.Background()
			wf, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: "release", SnoozeDurationMS: 120000})
			require.NoError(t, err)
			st, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "build", WorkflowID: &wf.ID})
			require.NoError(t, err)
			require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, b.AgentID, &wf.ID, &st.ID, nil))
			require.NoError(t, s.EnterEmbeddedNonMailProviderTurn(ctx, b, turn.UserSeq, turn.UserDigest))
			gen, err := s.MintEmbeddedProcessGeneration(ctx, "process")
			require.NoError(t, err)
			intent := []byte(`{"id":"workflow-call","name":"workflow_snooze","arguments":{"justification":"Waiting for CI build pipeline","duration_minutes":1}}`)
			if mode == "custom default" {
				intent = []byte(`{"id":"workflow-call","name":"workflow_snooze","arguments":{"justification":"Waiting for CI build pipeline"}}`)
			}
			w, err := s.RecordEmbeddedGoalInvocation(ctx, b, gen, turn.UserSeq, turn.UserDigest, "", "workflow-call", intent)
			require.NoError(t, err)
			require.NoError(t, s.CheckEmbeddedGoalInvocation(ctx, w))
			raw := []byte(`{"justification":"Waiting for CI build pipeline","duration_minutes":1}`)
			if mode == "custom default" {
				raw = []byte(`{"justification":"Waiting for CI build pipeline"}`)
			}
			if mode == "changed args" {
				raw = []byte(`{"justification":"Waiting for CI build pipeline","duration_ms":60000}`)
			}
			if mode == "stale process" {
				_, err = s.MintEmbeddedProcessGeneration(ctx, "replacement")
				require.NoError(t, err)
			}
			p, replay, err := s.SaveCurrentEmbeddedWorkflowSnoozePreference(ctx, b, turn, "workflow-call", raw, w)
			if mode == "changed args" || mode == "stale process" {
				require.Error(t, err)
				require.Zero(t, embeddedRedCount(t, s, "embedded_goal_snooze_calls"))
				return
			}
			require.NoError(t, err)
			require.False(t, replay)
			require.Equal(t, "release", p.Workflow)
			if mode == "custom default" {
				require.EqualValues(t, 120000, p.DurationMS)
			} else {
				require.EqualValues(t, 60000, p.DurationMS)
			}
			result, err := p.WorkflowResultJSON()
			require.NoError(t, err)
			if mode == "partial result" {
				result = []byte(`{"ok":true}`)
			}
			err = s.CompleteEmbeddedGoalInvocation(ctx, w, ai.ToolResultMessage{ToolCallID: "workflow-call", ToolName: "workflow_snooze", Content: ai.ContentList{ai.TextContent{Text: string(result)}}})
			if mode == "partial result" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if mode == "duplicate" {
				_, _, err = s.SaveCurrentEmbeddedWorkflowSnoozePreference(ctx, b, turn, "workflow-call", raw, w)
				require.Error(t, err)
				require.Equal(t, 1, embeddedRedCount(t, s, "embedded_goal_snooze_calls"))
			}
		})
	}
}

func TestRecoveryMaxCanonicalDeadline(t *testing.T) {
	for _, laterPolicy := range []bool{false, true} {
		t.Run(map[bool]string{false: "preference later", true: "policy later"}[laterPolicy], func(t *testing.T) {
			s, b, k := evaluationFixture(t)
			defer s.Close()
			ctx := context.Background()
			p, _, err := s.SaveEmbeddedGoalSnoozePreference(ctx, b, EmbeddedGoalSnoozeTurn{UserSeq: k.UserSeq, UserDigest: k.UserDigest, GoalRevision: k.GoalRevision}, "maximum", []byte(`{"duration_ms":60000}`))
			require.NoError(t, err)
			saved, err := time.Parse(time.RFC3339Nano, p.DueAt)
			require.NoError(t, err)
			policy := saved.Add(-time.Second)
			expected := saved
			if laterPolicy {
				policy = saved.Add(time.Second)
				expected = policy
			}
			require.NoError(t, s.SetManagedGoalSnoozeUntil(ctx, b.AgentID, policy.In(time.FixedZone("offset", -3600)).Format(time.RFC3339Nano)))
			receiptForEvaluation(t, s, b, &k)
			_, _, err = s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
			require.NoError(t, err)
			_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "")
			require.NoError(t, err)
			c, err := s.EmbeddedGoalContinuationFor(ctx, b, k)
			require.NoError(t, err)
			require.Equal(t, formatTimestamp(expected), c.DueAt.String)
		})
	}
}

func TestRecoveryStartedFenceSurvivesRetirement(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	receiptForEvaluation(t, s, b, &k)
	_, _, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "")
	require.NoError(t, err)
	_, started, err := s.StartEmbeddedGoalContinuation(ctx, b, k)
	require.NoError(t, err)
	require.True(t, started)
	require.NoError(t, s.SetManagedAgentActiveWorkflowState(ctx, b.AgentID, nil))
	c, err := s.EmbeddedGoalContinuationFor(ctx, b, k)
	require.NoError(t, err)
	require.Equal(t, "started", c.State)
	require.Equal(t, "unknown", c.Disposition)
	_, started, err = s.StartEmbeddedGoalContinuation(ctx, b, k)
	require.NoError(t, err)
	require.False(t, started)
}

func TestRecoveryMalformedContinuationDenied(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	receiptForEvaluation(t, s, b, &k)
	_, _, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "")
	require.NoError(t, err)
	// Supporting corruption fixture: bypass only the immutable reservation trigger.
	rows, err := s.db.Query(`SELECT name FROM sqlite_master WHERE type='trigger' AND tbl_name='embedded_goal_continuations'`)
	require.NoError(t, err)
	var names []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		names = append(names, name)
	}
	require.NoError(t, rows.Close())
	for _, name := range names {
		_, err = s.db.Exec(`DROP TRIGGER "` + name + `"`)
		require.NoError(t, err)
	}
	_, err = s.db.Exec(`UPDATE embedded_goal_continuations SET due_at='broken' WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	_, started, err := s.StartEmbeddedGoalContinuation(ctx, b, k)
	require.ErrorContains(t, err, "invalid continuation due")
	require.False(t, started)
}

func TestRecoveryActiveWorkflowCannotComplete(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	wf, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: "active"})
	require.NoError(t, err)
	st, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "build", WorkflowID: &wf.ID})
	require.NoError(t, err)
	require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, b.AgentID, &wf.ID, &st.ID, nil))
	receiptForEvaluation(t, s, b, &k)
	_, _, err = s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	_, changed, err := s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationDone, "")
	require.Error(t, err)
	require.False(t, changed)
	policy, err := s.ManagedGoalPolicyFor(ctx, b.AgentID)
	require.NoError(t, err)
	require.True(t, policy.Config.Enabled)
}

func TestRecoveryCompletedGoalRetirementFailureRollsBack(t *testing.T) {
	for _, tc := range []struct {
		name, trigger string
	}{
		{"preference retirement", `BEFORE UPDATE OF active ON embedded_goal_snooze_preferences WHEN OLD.active=1 AND NEW.active=0`},
		{"reserved continuation invalidation", `BEFORE UPDATE OF state ON embedded_goal_continuations WHEN OLD.state='reserved' AND NEW.state='invalidated'`},
		{"final policy update", `BEFORE UPDATE OF goal_enabled ON managed_agent_policies WHEN NEW.evaluator_state='done'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, b, priorKey := evaluationFixture(t)
			defer s.Close()
			ctx := context.Background()
			p, replay, err := s.SaveEmbeddedGoalSnoozePreference(ctx, b, EmbeddedGoalSnoozeTurn{UserSeq: priorKey.UserSeq, UserDigest: priorKey.UserDigest, GoalRevision: priorKey.GoalRevision}, "completion-rollback", []byte(`{"duration_ms":60000}`))
			require.NoError(t, err)
			require.False(t, replay)
			receiptForEvaluation(t, s, b, &priorKey)
			_, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, priorKey)
			require.NoError(t, err)
			require.True(t, claimed)
			_, changed, err := s.CompleteEmbeddedGoalEvaluation(ctx, b, priorKey, EmbeddedGoalEvaluationNotDone, "more work")
			require.NoError(t, err)
			require.True(t, changed)
			reservation, err := s.EmbeddedGoalContinuationFor(ctx, b, priorKey)
			require.NoError(t, err)
			require.Equal(t, "reserved", reservation.State)
			require.Equal(t, "pending", reservation.Disposition)

			// A later ordinary turn completes the same goal while the earlier
			// reservation and call-time preference still require retirement.
			user := receiptProviderUser(t, s, b, "finished")
			k := EmbeddedGoalEvaluationKey{UserSeq: user.Sequence, UserDigest: user.Digest, GoalRevision: priorKey.GoalRevision}
			_, replay, err = s.CommitEmbeddedGoalAppliedReceipt(ctx, b, EmbeddedGoalAppliedReceiptRequest{TurnKey: "completion-turn", TurnEntrySeq: k.UserSeq, TurnEntryDigest: k.UserDigest, GoalRevision: k.GoalRevision})
			require.NoError(t, err)
			require.False(t, replay)
			final := receiptSDKAppend(t, s, b, ai.AssistantMessage{StopReason: ai.StopStop})
			k.FinalSeq, k.FinalDigest = final.Sequence, final.Digest
			initial, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
			require.NoError(t, err)
			require.True(t, claimed)
			require.Equal(t, EmbeddedGoalEvaluationUnknown, initial.State)
			policy, err := s.ManagedGoalPolicyFor(ctx, b.AgentID)
			require.NoError(t, err)
			require.True(t, policy.Config.Enabled)
			require.Equal(t, p.DueAt, policy.SnoozedUntil)
			var active int
			require.NoError(t, s.db.QueryRow(`SELECT active FROM embedded_goal_snooze_preferences WHERE call_id=?`, p.CallID).Scan(&active))
			require.Equal(t, 1, active)

			// Compare every stored column, including NULLs, timestamps and
			// immutable identities, rather than only the visible state flags.
			snapshot := func() [][][]any {
				var result [][][]any
				for _, table := range []string{"embedded_goal_evaluations", "embedded_goal_snooze_calls", "embedded_goal_snooze_preferences", "embedded_goal_continuations", "managed_agent_policies"} {
					rows, err := s.db.Query(`SELECT * FROM `+table+` WHERE agent_id=? ORDER BY rowid`, b.AgentID)
					require.NoError(t, err)
					columns, err := rows.Columns()
					require.NoError(t, err)
					var records [][]any
					for rows.Next() {
						values := make([]any, len(columns))
						pointers := make([]any, len(columns))
						for i := range values {
							pointers[i] = &values[i]
						}
						require.NoError(t, rows.Scan(pointers...))
						records = append(records, values)
					}
					require.NoError(t, rows.Err())
					require.NoError(t, rows.Close())
					result = append(result, records)
				}
				return result
			}
			before := snapshot()
			_, err = s.db.Exec(`CREATE TRIGGER completion_retirement_fail ` + tc.trigger + ` BEGIN SELECT RAISE(ABORT,'completion retirement failed'); END`)
			require.NoError(t, err)
			_, changed, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationDone, "all finished")
			require.ErrorContains(t, err, "completion retirement failed")
			require.False(t, changed)
			require.Equal(t, before, snapshot(), "all writes preceding the injected failure must roll back")
			after, err := s.EmbeddedEvaluationRow(ctx, b, k)
			require.NoError(t, err)
			require.Equal(t, initial, after)
			require.Equal(t, EmbeddedGoalEvaluationUnknown, after.State)
			require.Empty(t, after.CompletedAt)
			counts, _, err := s.EmbeddedGoalDiagnostics(ctx, b.AgentID)
			require.NoError(t, err)
			require.Zero(t, counts["COMPLETED"], "no partial DONE may survive")
		})
	}
}
