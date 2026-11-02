package managed

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestWorkflowSettlementExactPromptWithoutEvaluator(t *testing.T) {
	for _, tc := range []struct {
		name            string
		empty           bool
		workflow, state string
		readFailure     bool
	}{
		{name: "text", workflow: "Recovery", state: "Implementation"},
		{name: "empty", empty: true, workflow: "Recovery", state: "Implementation"},
		{name: "long", workflow: strings.Repeat("w", 256), state: strings.Repeat("s", 256)},
		{name: "multibyte", workflow: strings.Repeat("界", 85), state: strings.Repeat("状", 85)},
		{name: "start-read-error", workflow: "Recovery", state: "Implementation"},
		{name: "post-claim-read-error", workflow: "Recovery", state: "Implementation", readFailure: true},
	} {
		empty := tc.empty
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "router.db")
			clock := deterministic.NewFixedClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
			db, err := store.Open(ctx, path, store.Options{Clock: clock})
			require.NoError(t, err)
			defer db.Close()
			binding := subagentTestParent(t, db, "workflow-settlement")
			wf, err := db.CreateWorkflow(ctx, store.WorkflowRequest{Name: tc.workflow})
			require.NoError(t, err)
			st, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: tc.state, WorkflowID: &wf.ID})
			require.NoError(t, err)
			require.NoError(t, db.SetManagedAgentActiveWorkflowWithVariables(ctx, binding.AgentID, &wf.ID, &st.ID, nil))
			raw, err := sql.Open("sqlite", path)
			require.NoError(t, err)
			defer raw.Close()
			_, err = raw.ExecContext(ctx, `UPDATE managed_agent_policies SET goal_enabled=1,goal_text='Complete active workflow: Recovery',desired_revision=1,applied_revision=1,projection_state='applied' WHERE agent_id=?`, binding.AgentID)
			require.NoError(t, err)
			var calls, providers atomic.Int32
			mgr, err := NewManager(ManagerOptions{Store: db, Manifest: manifestManagedTools(t), Clock: clock, Config: Config{SessionRoot: t.TempDir()}, EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { providers.Add(1); return nil }, EmbeddedGoalEvaluator: func(context.Context, store.EmbeddedGoalEvaluationKey) (string, string, error) {
				calls.Add(1)
				return store.EmbeddedGoalEvaluationDone, "ordinary evaluator", nil
			}})
			require.NoError(t, err)
			defer mgr.Close(ctx)
			if tc.name == "text" {
				for _, agent := range []*store.ManagedAgentRecord{
					{},
					{ActiveWorkflowID: "missing-workflow", ActiveWorkflowStateID: "missing-state"},
				} {
					require.Equal(t, "The workflow Workflow is currently active in state active. You must either make progress towards completing the workflow or call workflow_snooze.", mgr.workflowContinuationGuidance(ctx, agent))
				}
			}
			userBody, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "work"}}})
			require.NoError(t, err)
			user, err := db.AppendEmbeddedEntry(ctx, binding, "user", userBody)
			require.NoError(t, err)
			require.NoError(t, db.EnterEmbeddedNonMailProviderTurn(ctx, binding, user.Sequence, user.Digest))
			receipt, _, err := db.CommitEmbeddedGoalAppliedReceipt(ctx, binding, store.EmbeddedGoalAppliedReceiptRequest{TurnKey: "receipt", TurnEntrySeq: user.Sequence, TurnEntryDigest: user.Digest, GoalRevision: 1})
			require.NoError(t, err)
			content := ai.ContentList{}
			if !empty {
				content = ai.ContentList{ai.TextContent{Text: "progress"}}
			}
			body, err := json.Marshal(ai.AssistantMessage{Content: content, StopReason: ai.StopStop})
			require.NoError(t, err)
			final, err := db.AppendEmbeddedEntry(ctx, binding, "assistant", body)
			require.NoError(t, err)
			p := &embeddedPersistence{turnSeq: user.Sequence, turnDigest: user.Digest, seq: final.Sequence, digest: final.Digest}
			var diagnostics settlementLog
			previousLog := log.Writer()
			log.SetOutput(&diagnostics)
			defer log.SetOutput(previousLog)
			if tc.readFailure {
				_, err = raw.ExecContext(ctx, `CREATE TRIGGER break_agent_read AFTER INSERT ON embedded_goal_evaluations BEGIN UPDATE managed_agents SET progress_percent='broken' WHERE agent_id=NEW.agent_id; END`)
				require.NoError(t, err)
			}
			require.NoError(t, mgr.evaluateEmbeddedTurn(ctx, binding, p, &receipt, nil, nil))
			if tc.readFailure {
				// Close joins the evaluation task before examining diagnostics/counters.
				require.Eventually(t, func() bool {
					return strings.Contains(diagnostics.String(), "read managed agent after goal evaluation claim")
				}, time.Second, time.Millisecond)
				require.NoError(t, mgr.Close(ctx))
				var state string
				require.NoError(t, raw.QueryRowContext(ctx, `SELECT state FROM embedded_goal_evaluations WHERE agent_id=?`, binding.AgentID).Scan(&state))
				require.Equal(t, store.EmbeddedGoalEvaluationUnknown, state)
				require.Contains(t, diagnostics.String(), "read managed agent after goal evaluation claim")
				require.Contains(t, diagnostics.String(), binding.AgentID)
				require.Zero(t, calls.Load())
				require.Zero(t, providers.Load())
				_, err = raw.ExecContext(ctx, `UPDATE managed_agents SET progress_percent=NULL WHERE agent_id=?`, binding.AgentID)
				require.NoError(t, err)
				key := store.EmbeddedGoalEvaluationKey{UserSeq: user.Sequence, UserDigest: user.Digest, GoalRevision: 1, FinalSeq: final.Sequence, FinalDigest: final.Digest}
				_, claimed, err := db.ClaimEmbeddedGoalEvaluation(ctx, binding, key)
				require.NoError(t, err)
				require.False(t, claimed)
				return
			}
			var verdict, reason string
			require.Eventually(t, func() bool {
				return raw.QueryRowContext(ctx, `SELECT state,reason FROM embedded_goal_evaluations WHERE agent_id=?`, binding.AgentID).Scan(&verdict, &reason) == nil && verdict == store.EmbeddedGoalEvaluationNotDone
			}, time.Second, time.Millisecond)
			require.Zero(t, calls.Load())
			want := "The workflow " + tc.workflow + " is currently active in state " + tc.state + ". You must either make progress towards completing the workflow or call workflow_snooze."
			require.LessOrEqual(t, len(reason), 512)
			require.NotEmpty(t, reason)
			var reservations int
			require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*) FROM embedded_goal_continuations WHERE agent_id=? AND state='reserved'`, binding.AgentID).Scan(&reservations))
			require.Equal(t, 1, reservations)
			require.Equal(t, want, mgr.formatGoalContinuationPrompt(ctx, binding, nil))
			if tc.name == "start-read-error" {
				lane := mgr.laneFor(binding.AgentID)
				lane.serial.Lock()
				lane.serial.Unlock()
				mgr.mu.Lock()
				if mgr.embeddedOwners == nil {
					mgr.embeddedOwners = make(map[string]embeddedOwner)
				}
				mgr.embeddedOwners[binding.AgentID] = embeddedOwner{binding: binding, identity: "fixture:" + binding.AgentID}
				mgr.mu.Unlock()
				_, err = raw.ExecContext(ctx, `CREATE TRIGGER break_start_read AFTER UPDATE OF state ON embedded_goal_continuations WHEN NEW.state='started' BEGIN UPDATE managed_agents SET progress_percent='broken' WHERE agent_id=NEW.agent_id; END`)
				require.NoError(t, err)
				key := store.EmbeddedGoalEvaluationKey{UserSeq: user.Sequence, UserDigest: user.Digest, GoalRevision: 1, FinalSeq: final.Sequence, FinalDigest: final.Digest}
				// Make the reservation due without firing unrelated timers.
				// Unsnoozed reservation is already due; dispatch pacing uses a separate timer.
				mgr.startEmbeddedGoalContinuation(binding, key)
				require.Contains(t, diagnostics.String(), "read managed agent for goal continuation prompt")
				require.Zero(t, providers.Load())
				require.Zero(t, calls.Load())
				var state, disposition string
				require.NoError(t, raw.QueryRowContext(ctx, `SELECT state,disposition FROM embedded_goal_continuations WHERE agent_id=?`, binding.AgentID).Scan(&state, &disposition))
				require.Equal(t, "started", state)
				require.Equal(t, "unknown", disposition)
				_, err = raw.ExecContext(ctx, `UPDATE managed_agents SET progress_percent=NULL WHERE agent_id=?`, binding.AgentID)
				require.NoError(t, err)
				mgr.startEmbeddedGoalContinuation(binding, key)
				require.Zero(t, providers.Load(), "uncertain start must not replay after read recovery")
			}

		})
	}
}

type settlementLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *settlementLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}
func (l *settlementLog) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.buf.String() }
