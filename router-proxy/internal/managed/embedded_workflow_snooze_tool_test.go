package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedWorkflowSnoozeArguments(t *testing.T) {
	// 1. duration_minutes converts to ms
	raw, ms, err := embeddedWorkflowSnoozeArguments(map[string]any{"duration_minutes": 5}, 600000)
	require.NoError(t, err)
	require.Equal(t, int64(300000), ms)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(raw, &parsed))
	require.Equal(t, float64(5), parsed["duration_minutes"])

	// 2. duration_ms used when duration_minutes absent
	_, ms, err = embeddedWorkflowSnoozeArguments(map[string]any{"duration_ms": 120000}, 600000)
	require.NoError(t, err)
	require.Equal(t, int64(120000), ms)

	// 3. Fallback to defaultMs
	_, ms, err = embeddedWorkflowSnoozeArguments(nil, 300000)
	require.NoError(t, err)
	require.Equal(t, int64(300000), ms)

	// 4. Fallback to 10 minutes when defaultMs <= 0
	_, ms, err = embeddedWorkflowSnoozeArguments(map[string]any{}, 0)
	require.NoError(t, err)
	require.Equal(t, int64(600000), ms)

	// 5. Negative duration or 0 falls back to defaultMs
	_, ms, err = embeddedWorkflowSnoozeArguments(map[string]any{"duration_ms": -5000}, 300000)
	require.NoError(t, err)
	require.Equal(t, int64(300000), ms)

	_, ms, err = embeddedWorkflowSnoozeArguments(map[string]any{"duration_minutes": 0}, 450000)
	require.NoError(t, err)
	require.Equal(t, int64(450000), ms)

	_, ms, err = embeddedWorkflowSnoozeArguments(map[string]any{"duration_minutes": -10, "duration_ms": 0}, 350000)
	require.NoError(t, err)
	require.Equal(t, int64(350000), ms)

	// 6. Value below minimum (e.g. 500ms) clamps to 1,000ms
	_, ms, err = embeddedWorkflowSnoozeArguments(map[string]any{"duration_ms": 500}, 600000)
	require.NoError(t, err)
	require.Equal(t, int64(1000), ms)

	// 7. Value above maximum (e.g. 48 hours) clamps to 24 hours (86,400,000ms)
	const fortyEightHoursMs = int64(48 * 60 * 60 * 1000)
	_, ms, err = embeddedWorkflowSnoozeArguments(map[string]any{"duration_ms": fortyEightHoursMs}, 600000)
	require.NoError(t, err)
	require.Equal(t, store.EmbeddedGoalSnoozeMaxDurationMS, ms)
	require.Equal(t, int64(86400000), ms)

	const fortyEightHoursMinutes = 48 * 60
	_, ms, err = embeddedWorkflowSnoozeArguments(map[string]any{"duration_minutes": fortyEightHoursMinutes}, 600000)
	require.NoError(t, err)
	require.Equal(t, store.EmbeddedGoalSnoozeMaxDurationMS, ms)

	// 8. duration_minutes takes precedence over duration_ms
	_, ms, err = embeddedWorkflowSnoozeArguments(map[string]any{"duration_minutes": 10, "duration_ms": 50000}, 600000)
	require.NoError(t, err)
	require.Equal(t, int64(600000), ms)

	// 9. reason field is preserved in json arguments
	rawWithReason, ms, err := embeddedWorkflowSnoozeArguments(map[string]any{"duration_minutes": 2, "reason": "waiting for build"}, 600000)
	require.NoError(t, err)
	require.Equal(t, int64(120000), ms)
	var parsedReason map[string]any
	require.NoError(t, json.Unmarshal(rawWithReason, &parsedReason))
	require.Equal(t, "waiting for build", parsedReason["reason"])
	require.Equal(t, float64(2), parsedReason["duration_minutes"])
}

func TestEmbeddedWorkflowSnoozeExecutionAndBypass(t *testing.T) {
	for _, args := range []map[string]any{{"duration_minutes": 2}, {}} {
		t.Run(fmt.Sprint(args), func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			path := filepath.Join(root, "router.db")
			db, err := store.Open(ctx, path, store.Options{})
			require.NoError(t, err)
			defer db.Close()

			agentID := "worker-wf-snooze-test"
			binding := subagentTestParent(t, db, agentID)
			owner := embeddedOwner{binding: binding, identity: "fixture:" + agentID}

			rawDB, err := sql.Open("sqlite", path)
			require.NoError(t, err)
			defer rawDB.Close()

			_, err = rawDB.ExecContext(ctx, `UPDATE managed_agent_policies SET goal_enabled=1, goal_text='Test goal', desired_revision=1, applied_revision=1, evaluator_state='idle', snoozed_until='' WHERE agent_id=?`, agentID)
			require.NoError(t, err)

			mgr := &Manager{store: db}

			// Create workflow and state
			wf, err := db.CreateWorkflow(ctx, store.WorkflowRequest{
				Name:             "Release Pipeline",
				Description:      "Deploy release",
				SnoozeDurationMS: 180000, // 3 minutes default
			})
			require.NoError(t, err)

			state, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{
				WorkflowID:  &wf.ID,
				Name:        "Build & Test",
				Description: "Run tests",
			})
			require.NoError(t, err)

			require.NoError(t, db.SetManagedAgentActiveWorkflowWithVariables(ctx, agentID, &wf.ID, &state.ID, nil))

			// Verify formatGoalContinuationPrompt uses workflow prompt
			prompt := mgr.formatGoalContinuationPrompt(ctx, binding, nil)
			require.Equal(t, "The workflow Release Pipeline is currently active in state Build & Test. You must either make progress towards completing the workflow, call workflow_snooze, exit the workflow with mecha_workflow_transition (action exit) when an exit is allowed, or call mecha_blocked with the reason.", prompt)

			// Test workflow_snooze tool execution
			userMsg, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "start turn"}}})
			require.NoError(t, err)
			userEntry, err := db.AppendEmbeddedEntry(ctx, binding, "user", userMsg)
			require.NoError(t, err)
			require.NoError(t, db.EnterEmbeddedNonMailProviderTurn(ctx, binding, userEntry.Sequence, userEntry.Digest))

			callID := "wf-snooze-call-1"
			intent, err := json.Marshal(map[string]any{
				"id":        callID,
				"name":      "workflow_snooze",
				"arguments": args,
			})
			require.NoError(t, err)

			gen, err := db.MintEmbeddedProcessGeneration(ctx, "wf-snooze-process")
			require.NoError(t, err)
			invocation, err := db.RecordEmbeddedGoalInvocation(ctx, binding, gen, userEntry.Sequence, userEntry.Digest, "", callID, intent)
			require.NoError(t, err)

			turnFn := func() store.EmbeddedGoalSnoozeTurn {
				return store.EmbeddedGoalSnoozeTurn{
					UserSeq:    userEntry.Sequence,
					UserDigest: userEntry.Digest,
				}
			}
			admittedFn := func(id string) *store.EmbeddedGoalInvocation {
				if id == callID {
					return invocation
				}
				return nil
			}
			var completedResult ai.ToolResultMessage
			completedFn := func(id string, inv *store.EmbeddedGoalInvocation, res ai.ToolResultMessage) {
				completedResult = res
			}

			tool := mgr.embeddedWorkflowSnoozeTool(owner, turnFn, admittedFn, completedFn)
			_, err = ai.ValidateToolArguments(ai.Tool{Name: tool.Name, Parameters: tool.Parameters}, ai.ToolCall{Name: tool.Name, Arguments: args})
			require.NoError(t, err)
			result, err := tool.Execute(ctx, callID, args, nil)
			require.NoError(t, err)
			require.NotEmpty(t, result.Content)

			var resp map[string]any
			textVal, ok := result.Content[0].(ai.TextContent)
			require.True(t, ok)
			require.NoError(t, json.Unmarshal([]byte(textVal.Text), &resp))
			require.Equal(t, true, resp["ok"])
			duration := int64(120000)
			if len(args) == 0 {
				duration = 180000
			}
			require.Equal(t, float64(duration), resp["duration_ms"])
			require.Equal(t, float64(duration/60000), resp["duration_minutes"])
			policy, err := db.ManagedGoalPolicyFor(ctx, agentID)
			require.NoError(t, err)
			require.Equal(t, policy.SnoozedUntil, resp["snoozed_until"])
			preference, err := db.CurrentEmbeddedGoalSnoozePreference(ctx, agentID)
			require.NoError(t, err)
			require.Equal(t, duration, preference.DurationMS)
			var savedResult []byte
			require.NoError(t, rawDB.QueryRowContext(ctx, `SELECT result_json FROM embedded_goal_snooze_calls WHERE agent_id=? AND call_id=?`, agentID, callID).Scan(&savedResult))
			var savedPreference store.EmbeddedGoalSnoozePreference
			require.NoError(t, json.Unmarshal(savedResult, &savedPreference))
			expected, err := savedPreference.WorkflowResultJSON()
			require.NoError(t, err)
			require.Equal(t, string(expected), textVal.Text)
			require.Equal(t, result.Content, completedResult.Content)
			require.Equal(t, "Release Pipeline", resp["workflow"])
			require.Equal(t, callID, completedResult.ToolCallID)

		})
	}
}

func TestWorkflowSnoozeNativeOptionalSchema(t *testing.T) {
	tool := ai.Tool{Name: "workflow_snooze", Parameters: embeddedWorkflowSnoozeSchema()}
	for _, args := range []map[string]any{{}, {"duration_minutes": float64(2)}, {"duration_ms": float64(2000)}, {"reason": "waiting"}} {
		_, err := ai.ValidateToolArguments(tool, ai.ToolCall{Name: tool.Name, Arguments: args})
		require.NoError(t, err)
	}
}

func TestWorkflowSnoozeStrictNumbers(t *testing.T) {
	for _, args := range []map[string]any{
		{"extra": true}, {"duration_ms": "2000"}, {"duration_ms": nil}, {"reason": 7},
		{"duration_minutes": 1.5}, {"duration_ms": math.NaN()}, {"duration_ms": math.Inf(1)},
		{"duration_minutes": float64(9223372036854775808.0)}, {"duration_ms": json.Number("9223372036854775808")},
		{"duration_minutes": 1, "duration_ms": false},
	} {
		_, _, err := embeddedWorkflowSnoozeArguments(args, 600000)
		require.Error(t, err, "args=%v", args)
	}
	for _, value := range []any{int64(math.MaxInt64), json.Number("9223372036854775807"), float64(1e15)} {
		_, ms, err := embeddedWorkflowSnoozeArguments(map[string]any{"duration_minutes": value}, 600000)
		require.NoError(t, err)
		require.Equal(t, int64(86400000), ms)
	}
}

func TestGoalSnoozeNativeDefaultAndConfiguredCap(t *testing.T) {
	_, err := ai.ValidateToolArguments(ai.Tool{Name: "goal_snooze", Parameters: embeddedGoalSnoozeSchema()}, ai.ToolCall{Name: "goal_snooze", Arguments: map[string]any{}})
	require.NoError(t, err)
	for _, v := range []any{1.5, float64(9223372036854775808.0), math.NaN(), math.Inf(1)} {
		_, err := embeddedGoalSnoozeArguments(map[string]any{"duration_ms": v}, 180000)
		require.Error(t, err)
	}
	for _, args := range []map[string]any{{}, {"duration_ms": int64(math.MaxInt64)}} {
		raw, err := embeddedGoalSnoozeArguments(args, 180000)
		require.NoError(t, err)
		require.JSONEq(t, `{"duration_ms":180000}`, string(raw))
	}
}

// Post-implementation regression backfill: admission alone does not authorize
// snoozing an ordinary goal or retain authority after workflow unbinding.
func TestWorkflowSnoozeWithoutActiveWorkflowPreservesPolicy(t *testing.T) {
	for _, removeBinding := range []bool{false, true} {
		t.Run(fmt.Sprintf("remove-binding-after-check=%t", removeBinding), func(t *testing.T) {
			ctx := context.Background()
			db, binding, path := completionFixture(t)
			raw, err := sql.Open("sqlite", path)
			require.NoError(t, err)
			defer raw.Close()
			_, err = raw.ExecContext(ctx, `UPDATE managed_agent_policies SET goal_enabled=1,goal_text='ordinary goal',desired_revision=1,applied_revision=1,evaluator_state='idle',snoozed_until='2030-01-02T04:04:05.000Z' WHERE agent_id=?`, binding.AgentID)
			require.NoError(t, err)
			if removeBinding {
				wf, err := db.CreateWorkflow(ctx, store.WorkflowRequest{Name: "Admission only"})
				require.NoError(t, err)
				state, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "Work", WorkflowID: &wf.ID})
				require.NoError(t, err)
				require.NoError(t, db.SetManagedAgentActiveWorkflowWithVariables(ctx, binding.AgentID, &wf.ID, &state.ID, nil))
			}
			before, err := db.ManagedGoalPolicyFor(ctx, binding.AgentID)
			require.NoError(t, err)
			body, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "current user"}}})
			require.NoError(t, err)
			user, err := db.AppendEmbeddedEntry(ctx, binding, "user", body)
			require.NoError(t, err)
			require.NoError(t, db.EnterEmbeddedNonMailProviderTurn(ctx, binding, user.Sequence, user.Digest))
			generation, err := db.MintEmbeddedProcessGeneration(ctx, "workflow-denial")
			require.NoError(t, err)
			intent := []byte(`{"id":"current-call","name":"workflow_snooze","arguments":{"duration_ms":1000}}`)
			invocation, err := db.RecordEmbeddedGoalInvocation(ctx, binding, generation, user.Sequence, user.Digest, "", "current-call", intent)
			require.NoError(t, err)
			mgr := &Manager{store: db}
			committed, completed, reachedDomain := false, false, false
			mgr.embeddedAfterGoalCommit = func(context.Context) { committed = true }
			tool := mgr.embeddedWorkflowSnoozeTool(embeddedOwner{binding: binding, identity: "wake-fixture"}, func() store.EmbeddedGoalSnoozeTurn {
				// Execute has checked admission and original intent before asking
				// for the turn; the workflow lookup is still inside the DOMAIN tx.
				reachedDomain = true
				if removeBinding {
					require.NoError(t, db.SetManagedAgentActiveWorkflowWithVariables(ctx, binding.AgentID, nil, nil, nil))
					// Supported unbinding clears goal/snooze policy itself; only
					// subsequent denial must preserve this post-cleanup baseline.
					before, err = db.ManagedGoalPolicyFor(ctx, binding.AgentID)
					require.NoError(t, err)
				}
				return store.EmbeddedGoalSnoozeTurn{UserSeq: user.Sequence, UserDigest: user.Digest}
			}, func(string) *store.EmbeddedGoalInvocation { return invocation }, func(string, *store.EmbeddedGoalInvocation, ai.ToolResultMessage) { completed = true })
			result, err := tool.Execute(ctx, "current-call", map[string]any{"duration_ms": 1000}, nil)
			require.ErrorIs(t, err, sql.ErrNoRows, "active workflow lookup, not an earlier admission fence, must deny")
			require.Empty(t, result.Content)
			require.True(t, reachedDomain)
			require.False(t, committed)
			require.False(t, completed)
			after, err := db.ManagedGoalPolicyFor(ctx, binding.AgentID)
			require.NoError(t, err)
			require.Equal(t, before, after, "denial must preserve the full policy including its existing deadline")
			for _, table := range []string{"embedded_goal_snooze_calls", "embedded_goal_snooze_preferences"} {
				var count int
				require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE agent_id=?`, binding.AgentID).Scan(&count))
				require.Zero(t, count, table)
			}
			completionAssertUnknown(t, db, binding, path, user.Sequence)
			fabricated := ai.ToolResultMessage{ToolCallID: "current-call", ToolName: "workflow_snooze", Content: ai.ContentList{ai.TextContent{Text: `{"ok":true}`}}}
			require.Error(t, db.CompleteEmbeddedGoalInvocation(ctx, invocation, fabricated))
			_, err = db.RecordEmbeddedGoalInvocation(ctx, binding, generation, user.Sequence, user.Digest, "", "current-call", intent)
			require.Error(t, err, "denied UNKNOWN intent cannot be freshly admitted again")
			completionAssertUnknown(t, db, binding, path, user.Sequence)
		})
	}
}
