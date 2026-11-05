package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedGoalContinuationDoneAndOffDoNotStart(t *testing.T) {
	for _, tc := range []struct{ name, action string }{{"done", "set"}, {"off", "off"}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			path := filepath.Join(root, "router.db")
			db, err := store.Open(ctx, path, store.Options{})
			require.NoError(t, err)
			defer db.Close()
			manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
			require.NoError(t, err)
			reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
			defer reg.Unregister()
			var requests atomic.Int32
			reg.SetResponses([]providers.FauxResponseStep{
				func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					requests.Add(1)
					return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal", map[string]any{"action": "set", "goal": "finish"}, "initial-set")}, ai.StopToolUse)
				},
				func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					requests.Add(1)
					if tc.action == "off" {
						return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal", map[string]any{"action": "off"}, "turn-off")}, ai.StopToolUse)
					}
					return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "initial final"}}, ai.StopStop)
				},
				func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					requests.Add(1)
					return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "off final"}}, ai.StopStop)
				},
			})
			var evaluations atomic.Int32
			m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest,
				Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
				CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
				EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
				CodexCredentialStore:  testfixture.CodexAdmission(t),
				EmbeddedGoalEvaluator: func(context.Context, store.EmbeddedGoalEvaluationKey) (string, string, error) {
					evaluations.Add(1)
					return store.EmbeddedGoalEvaluationDone, "finished", nil
				},
			})
			require.NoError(t, err)
			defer m.Close(ctx)
			setEmbeddedRecoveryGeneration(t, ctx, db, m)
			launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "g5-no-cont-" + tc.name, Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "set goal", RequestID: "a5555555-5555-4555-8555-555555555584"})
			require.NoError(t, err)
			wantRequests := int32(2)
			if tc.action == "off" {
				wantRequests = 3
			}
			require.Eventually(t, func() bool { return requests.Load() == wantRequests }, 3*time.Second, 10*time.Millisecond)
			if tc.name == "done" {
				require.Eventually(t, func() bool { return evaluations.Load() == 1 }, 3*time.Second, 10*time.Millisecond)
			} else {
				require.Zero(t, evaluations.Load())
			}
			time.Sleep(100 * time.Millisecond)
			require.Equal(t, wantRequests, requests.Load())
			if tc.name == "done" {
				require.Equal(t, int32(1), evaluations.Load())
			} else {
				require.Zero(t, evaluations.Load())
			}
			readOnly, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
			require.NoError(t, err)
			defer readOnly.Close()
			var count int
			require.NoError(t, readOnly.QueryRowContext(ctx, `SELECT count(*) FROM embedded_goal_continuations WHERE agent_id=?`, launched.Agent.AgentID).Scan(&count))
			require.Zero(t, count)
		})
	}
}

func TestEmbeddedGoalOffOptionalArgumentsIntegrated(t *testing.T) {
	for i, tc := range []struct {
		name string
		args map[string]any
	}{
		{
			name: "empty_goal_string",
			args: map[string]any{"action": "off", "goal": ""},
		},
		{
			name: "non_empty_goal_string",
			args: map[string]any{"action": "off", "goal": "finished all tasks"},
		},
		{
			name: "no_goal_arg",
			args: map[string]any{"action": "off"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			path := filepath.Join(root, "router.db")
			db, err := store.Open(ctx, path, store.Options{})
			require.NoError(t, err)
			defer db.Close()
			manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
			require.NoError(t, err)
			reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
			defer reg.Unregister()
			var requests atomic.Int32
			reg.SetResponses([]providers.FauxResponseStep{
				func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					requests.Add(1)
					return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal", map[string]any{"action": "set", "goal": "finish tasks"}, "initial-set")}, ai.StopToolUse)
				},
				func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					requests.Add(1)
					return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal", tc.args, "turn-off")}, ai.StopToolUse)
				},
				func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					requests.Add(1)
					return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "off final"}}, ai.StopStop)
				},
			})
			var evaluations atomic.Int32
			m, err := NewManager(ManagerOptions{
				Store:                 db,
				Manifest:              manifest,
				Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
				CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
				EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
				CodexCredentialStore:  testfixture.CodexAdmission(t),
				EmbeddedGoalEvaluator: func(context.Context, store.EmbeddedGoalEvaluationKey) (string, string, error) {
					evaluations.Add(1)
					return store.EmbeddedGoalEvaluationDone, "finished", nil
				},
			})
			require.NoError(t, err)
			defer m.Close(ctx)
			setEmbeddedRecoveryGeneration(t, ctx, db, m)
			launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{
				Name:               "g5-off-opt-" + tc.name,
				Cwd:                root,
				Runtime:            "embedded",
				Model:              &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
				InitialInstruction: "set and clear goal",
				RequestID:          fmt.Sprintf("b5555555-5555-4555-8555-5555555555%02d", i+10),
			})
			require.NoError(t, err)

			// 1. Session settles cleanly after 3 requests (set -> off -> final text)
			require.Eventually(t, func() bool { return requests.Load() == 3 }, 3*time.Second, 10*time.Millisecond)
			time.Sleep(100 * time.Millisecond)
			require.Equal(t, int32(3), requests.Load())

			// 2. Goal continuation does NOT start / evaluations are 0
			require.Zero(t, evaluations.Load())

			readOnly, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
			require.NoError(t, err)
			defer readOnly.Close()

			var continuations int
			require.NoError(t, readOnly.QueryRowContext(ctx, `SELECT count(*) FROM embedded_goal_continuations WHERE agent_id=?`, launched.Agent.AgentID).Scan(&continuations))
			require.Zero(t, continuations)

			// 3. Desired policy in store has Enabled == false and Goal == ""
			policy, err := db.ManagedGoalPolicyFor(ctx, launched.Agent.AgentID)
			require.NoError(t, err)
			require.False(t, policy.Config.Enabled)
			require.Empty(t, policy.Config.Goal)
			require.EqualValues(t, 2, policy.DesiredRevision)
			require.EqualValues(t, 2, policy.AppliedRevision)
			require.Equal(t, "applied", policy.ProjectionState)

			// 4. Receipt is recorded successfully
			var receiptCount, receiptEnabled int
			var receiptGoal string
			require.NoError(t, readOnly.QueryRowContext(ctx, `SELECT count(*), goal_enabled, goal_text FROM embedded_goal_applied_receipts WHERE agent_id=? AND goal_revision=2`, launched.Agent.AgentID).Scan(&receiptCount, &receiptEnabled, &receiptGoal))
			require.Equal(t, 1, receiptCount)
			require.Zero(t, receiptEnabled)
			require.Empty(t, receiptGoal)

			// 5. Tool call executes successfully (no error in session, tool result is produced)
			var callState, resultJSON string
			require.NoError(t, readOnly.QueryRowContext(ctx, `SELECT state, result_json FROM embedded_goal_calls WHERE call_id='turn-off'`).Scan(&callState, &resultJSON))
			require.Equal(t, "complete", callState)
			var toolResultPolicy store.ManagedGoalPolicy
			require.NoError(t, json.Unmarshal([]byte(resultJSON), &toolResultPolicy))
			require.False(t, toolResultPolicy.Config.Enabled)
			require.Empty(t, toolResultPolicy.Config.Goal)

			binding := store.EmbeddedBinding{
				AgentID:    launched.Agent.AgentID,
				SessionID:  launched.Agent.PiSessionID,
				Epoch:      launched.Agent.SessionEpoch,
				Generation: launched.Agent.RuntimeGeneration,
			}
			binding = inspectManagedOwnerBinding(t, path, binding)
			snapshot, err := db.ReopenEmbedded(ctx, binding)
			require.NoError(t, err)
			var foundTurnOffResult bool
			for _, entry := range snapshot.Entries {
				if entry.Kind == "toolResult" || entry.Kind == "tool_result" {
					msg, unmarshalErr := ai.UnmarshalMessage(entry.Body)
					require.NoError(t, unmarshalErr)
					if tr, ok := msg.(ai.ToolResultMessage); ok && tr.ToolCallID == "turn-off" {
						foundTurnOffResult = true
						require.False(t, tr.IsError)
						var resPolicy store.ManagedGoalPolicy
						require.NoError(t, json.Unmarshal([]byte(tr.Content[0].(ai.TextContent).Text), &resPolicy))
						require.False(t, resPolicy.Config.Enabled)
						require.Empty(t, resPolicy.Config.Goal)
					}
				}
			}
			require.True(t, foundTurnOffResult, "turn-off tool result must be present in session transcript")

			// 6. Session settles cleanly (idle and running, no error)
			var activity, lifecycle string
			var errCode sql.NullString
			require.NoError(t, readOnly.QueryRowContext(ctx, `SELECT activity_state, lifecycle_state, last_error_code FROM managed_agents WHERE agent_id=?`, launched.Agent.AgentID).Scan(&activity, &lifecycle, &errCode))
			require.Equal(t, "idle", activity)
			require.Equal(t, "running", lifecycle)
			require.False(t, errCode.Valid && errCode.String != "", "last_error_code was non-empty: %s", errCode.String)
		})
	}
}

func TestEmbeddedGoalOffOptionalArgumentsStatusIntegrated(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	var requests atomic.Int32
	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			requests.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal", map[string]any{"action": "set", "goal": "persistent active goal"}, "initial-set")}, ai.StopToolUse)
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			requests.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal", map[string]any{"action": "status", "goal": ""}, "status-check")}, ai.StopToolUse)
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			requests.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "status done, goal active"}}, ai.StopStop)
		},
	})
	var evaluations atomic.Int32
	m, err := NewManager(ManagerOptions{
		Store:                 db,
		Manifest:              manifest,
		Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		CodexCredentialStore:  testfixture.CodexAdmission(t),
		EmbeddedGoalEvaluator: func(context.Context, store.EmbeddedGoalEvaluationKey) (string, string, error) {
			evaluations.Add(1)
			return store.EmbeddedGoalEvaluationDone, "finished", nil
		},
	})
	require.NoError(t, err)
	defer m.Close(ctx)
	setEmbeddedRecoveryGeneration(t, ctx, db, m)
	launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{
		Name:               "g5-status-opt-goal",
		Cwd:                root,
		Runtime:            "embedded",
		Model:              &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		InitialInstruction: "set and check status",
		RequestID:          "b5555555-5555-4555-8555-555555555599",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool { return requests.Load() == 3 }, 3*time.Second, 10*time.Millisecond)
	// Because goal remained active and final turn finished with StopStop, evaluation runs.
	require.Eventually(t, func() bool { return evaluations.Load() == 1 }, 3*time.Second, 10*time.Millisecond)

	readOnly, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	defer readOnly.Close()

	// Verify status returns current policy and goal was active with the text
	policy, err := db.ManagedGoalPolicyFor(ctx, launched.Agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "persistent active goal", policy.Config.Goal)
	require.Equal(t, "done", policy.EvaluatorState)

	binding := store.EmbeddedBinding{
		AgentID:    launched.Agent.AgentID,
		SessionID:  launched.Agent.PiSessionID,
		Epoch:      launched.Agent.SessionEpoch,
		Generation: launched.Agent.RuntimeGeneration,
	}
	binding = inspectManagedOwnerBinding(t, path, binding)
	snapshot, err := db.ReopenEmbedded(ctx, binding)
	require.NoError(t, err)
	var foundStatusResult bool
	for _, entry := range snapshot.Entries {
		if entry.Kind == "toolResult" || entry.Kind == "tool_result" {
			msg, unmarshalErr := ai.UnmarshalMessage(entry.Body)
			require.NoError(t, unmarshalErr)
			if tr, ok := msg.(ai.ToolResultMessage); ok && tr.ToolCallID == "status-check" {
				foundStatusResult = true
				require.False(t, tr.IsError)
				var statusPolicy store.ManagedGoalPolicy
				require.NoError(t, json.Unmarshal([]byte(tr.Content[0].(ai.TextContent).Text), &statusPolicy))
				require.True(t, statusPolicy.Config.Enabled)
				require.Equal(t, "persistent active goal", statusPolicy.Config.Goal)
			}
		}
	}
	require.True(t, foundStatusResult, "status-check tool result must be present in session transcript")

	// Session settles cleanly (idle and running, no error)
	var activity, lifecycle string
	var errCode sql.NullString
	require.NoError(t, readOnly.QueryRowContext(ctx, `SELECT activity_state, lifecycle_state, last_error_code FROM managed_agents WHERE agent_id=?`, launched.Agent.AgentID).Scan(&activity, &lifecycle, &errCode))
	require.Equal(t, "idle", activity)
	require.Equal(t, "running", lifecycle)
	require.False(t, errCode.Valid && errCode.String != "", "last_error_code was non-empty: %s", errCode.String)
}

func TestEmbeddedGoalContinuationDetectsUnbackedSubagentClaims(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	m, err := NewManager(ManagerOptions{
		Store:    db,
		Manifest: manifest,
		Config:   Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
	})
	require.NoError(t, err)
	defer m.Close(ctx)

	agentID := "subagent-claim-test"
	binding := store.EmbeddedBinding{
		AgentID:    agentID,
		SessionID:  "session-1",
		Epoch:      1,
		Generation: "gen-1",
	}

	// Missing agent authority must fail closed, not fabricate ordinary guidance.
	prompt := m.formatGoalContinuationPrompt(ctx, binding, nil)
	require.Empty(t, prompt)

	// Insert row in managed_agents
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,pi_session_id,session_epoch,runtime_generation,manifest_revision,manifest_hash,created_at,updated_at) VALUES(?,?,?,'openai-codex','gpt-5','running','running','idle','embedded',?,?,?,1,'hash','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		agentID, agentID, root, binding.SessionID, binding.Epoch, binding.Generation)
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	// Bind session in store
	require.NoError(t, db.BindEmbeddedSession(ctx, binding, "root"))
	token, err := db.AcquireEmbeddedOwner(ctx, binding, "owner-1", time.Minute)
	require.NoError(t, err)
	binding.OwnerToken = token

	// Append user entry
	userBody, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "Do something"}}})
	require.NoError(t, err)
	_, err = db.AppendEmbeddedEntry(ctx, binding, "user", userBody)
	require.NoError(t, err)

	// Case A: Normal assistant text message without subagent claims -> default prompt
	normalAsst, err := json.Marshal(ai.AssistantMessage{
		Content:    ai.ContentList{ai.TextContent{Text: "I edited the file."}},
		StopReason: ai.StopStop,
	})
	require.NoError(t, err)
	_, err = db.AppendEmbeddedEntry(ctx, binding, "assistant", normalAsst)
	require.NoError(t, err)

	prompt = m.formatGoalContinuationPrompt(ctx, binding, nil)
	require.Equal(t, goalContinuationDefaultPrompt, prompt)

	// Case B: Assistant claims subagent is executing, with no active subagents -> corrective notice prompt!
	claimAsst, err := json.Marshal(ai.AssistantMessage{
		Content:    ai.ContentList{ai.TextContent{Text: "Subagent `subagent-101` is executing. Resting and awaiting completion notification."}},
		StopReason: ai.StopStop,
	})
	require.NoError(t, err)
	_, err = db.AppendEmbeddedEntry(ctx, binding, "assistant", claimAsst)
	require.NoError(t, err)

	prompt = m.formatGoalContinuationPrompt(ctx, binding, nil)
	require.Equal(t, goalContinuationNoSubagentsNoticePrompt, prompt)

	// Case C: Assistant claims "awaiting completion" -> corrective notice prompt!
	awaitingAsst, err := json.Marshal(ai.AssistantMessage{
		Content:    ai.ContentList{ai.TextContent{Text: "Task started. Awaiting completion."}},
		StopReason: ai.StopStop,
	})
	require.NoError(t, err)
	_, err = db.AppendEmbeddedEntry(ctx, binding, "assistant", awaitingAsst)
	require.NoError(t, err)

	prompt = m.formatGoalContinuationPrompt(ctx, binding, nil)
	require.Equal(t, goalContinuationNoSubagentsNoticePrompt, prompt)

	// Case D: Assistant message has a tool call -> default prompt
	toolCallAsst, err := json.Marshal(ai.AssistantMessage{
		Content: ai.ContentList{
			ai.TextContent{Text: "Executing tool"},
			ai.ToolCall{ID: "call-1", Name: "bash", Arguments: map[string]any{"command": "ls"}},
		},
		StopReason: ai.StopToolUse,
	})
	require.NoError(t, err)
	_, err = db.AppendEmbeddedEntry(ctx, binding, "assistant", toolCallAsst)
	require.NoError(t, err)

	prompt = m.formatGoalContinuationPrompt(ctx, binding, nil)
	require.Equal(t, goalContinuationDefaultPrompt, prompt)
}

func TestEmbeddedStartupRecoveryResetsActiveToIdle(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	m1, err := NewManager(ManagerOptions{
		Store:    db,
		Manifest: manifest,
		Config:   Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
	})
	require.NoError(t, err)

	gen1 := setEmbeddedRecoveryGeneration(t, ctx, db, m1)
	require.Positive(t, gen1)

	// Create an embedded agent in running & active state
	binding := store.EmbeddedBinding{
		AgentID:    "agent-startup-active",
		SessionID:  "session-startup-active",
		Epoch:      1,
		Generation: "gen-startup-active",
	}
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,pi_session_id,session_epoch,runtime_generation,manifest_revision,manifest_hash,created_at,updated_at) VALUES(?,?,?,'openai-codex','gpt-5','running','running','idle','embedded',?,?,?,1,'hash','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		binding.AgentID, binding.AgentID, root, binding.SessionID, binding.Epoch, binding.Generation)
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	require.NoError(t, db.BindEmbeddedSession(ctx, binding, "root"))
	token, err := db.AcquireEmbeddedOwner(ctx, binding, "owner-active", time.Minute)
	require.NoError(t, err)
	binding.OwnerToken = token
	require.NoError(t, db.SetEmbeddedActivityState(ctx, binding, "active"))

	agent, err := db.ManagedAgent(ctx, binding.AgentID)
	require.NoError(t, err)
	require.Equal(t, "active", agent.ActivityState)
	require.Equal(t, "running", agent.LifecycleState)

	// Close m1 (simulating process kill)
	require.NoError(t, m1.Close(ctx))

	// Start m2 (simulating router boot / startup)
	m2, err := NewManager(ManagerOptions{
		Store:    db,
		Manifest: manifest,
		Config:   Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
	})
	require.NoError(t, err)
	defer m2.Close(ctx)

	// On startup recovery (SetEmbeddedStartupGeneration):
	gen2 := setEmbeddedRecoveryGeneration(t, ctx, db, m2)
	require.Positive(t, gen2)

	// Verify that the active agent was reset to idle!
	agent, err = db.ManagedAgent(ctx, binding.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", agent.ActivityState)
}

func TestEmbeddedGoalCircuitBreakerEmptyAssistantHalts(t *testing.T) {
	for _, incoming := range []string{"initial", "mail"} {
		for _, tc := range []struct {
			name    string
			content ai.ContentList
		}{
			{"empty-content", ai.ContentList{}},
			{"whitespace-text", ai.ContentList{ai.TextContent{Text: "   \n\t  "}}},
		} {
			t.Run(incoming+"/"+tc.name, func(t *testing.T) {
				ctx := context.Background()
				root := t.TempDir()
				path := filepath.Join(root, "router.db")
				db, err := store.Open(ctx, path, store.Options{})
				require.NoError(t, err)
				defer db.Close()
				manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
				require.NoError(t, err)
				reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
				defer reg.Unregister()

				var requests atomic.Int32
				reg.SetResponses([]providers.FauxResponseStep{
					// Turn 1 step 1: set goal
					func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
						requests.Add(1)
						return providers.FauxAssistantMessage(ai.ContentList{
							providers.FauxToolCall("goal", map[string]any{"action": "set", "goal": "circuit breaker test"}, "cb-set"),
						}, ai.StopToolUse)
					},
					// Turn 1 step 2: empty or whitespace-only assistant message (empty turn 1/3 -> re-nudge)
					func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
						requests.Add(1)
						return providers.FauxAssistantMessage(tc.content, ai.StopStop)
					},
					// Turn 2: empty assistant message (empty turn 2/3 -> re-nudge)
					func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
						requests.Add(1)
						return providers.FauxAssistantMessage(tc.content, ai.StopStop)
					},
					// Turn 3: empty assistant message (empty turn 3/3 -> circuit breaker halts!)
					func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
						requests.Add(1)
						return providers.FauxAssistantMessage(tc.content, ai.StopStop)
					},
					// Continuation step (should NOT be reached)
					func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
						requests.Add(1)
						return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "unreachable"}}, ai.StopStop)
					},
				})

				var evaluations atomic.Int32
				m, err := NewManager(ManagerOptions{
					Store:                 db,
					Manifest:              manifest,
					Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
					CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
					EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
					CodexCredentialStore:  testfixture.CodexAdmission(t),
					GoalContinuationDelay: 20 * time.Millisecond,
					EmbeddedGoalEvaluator: func(context.Context, store.EmbeddedGoalEvaluationKey) (string, string, error) {
						evaluations.Add(1)
						return store.EmbeddedGoalEvaluationNotDone, "still needs work", nil
					},
				})
				require.NoError(t, err)
				defer m.Close(ctx)
				setEmbeddedRecoveryGeneration(t, ctx, db, m)

				instruction := "start work"
				if incoming == "mail" {
					instruction = ""
				}
				launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{
					Name:               "circuit-breaker-" + tc.name,
					Cwd:                root,
					Runtime:            "embedded",
					Model:              &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
					InitialInstruction: instruction,
					RequestID:          "cb111111-1111-4111-8111-111111111111",
				})
				require.NoError(t, err)

				if incoming == "mail" {
					require.NoError(t, db.ReconcileManagedRecipientSources(ctx))
					// Bootstrap can be idle before ReadyEmbeddedRoot activates the recipient.
					require.Eventually(t, func() bool {
						source, err := db.RecipientSource(ctx, launched.Agent.AgentID)
						return err == nil && source != nil && source.State == "active" &&
							source.SourceKind == "managed" && source.OwnerIdentity == launched.Agent.AgentID
					}, 3*time.Second, 10*time.Millisecond)
					_, err = db.Enqueue(ctx, protocol.Envelope{
						ID:   "cb222222-2222-4222-8222-222222222222",
						From: "user", To: launched.Agent.AgentID, Body: "start work",
						CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
					}, store.EnqueueOptions{})
					require.NoError(t, err)
					target, err := db.ResolveMessageTarget(ctx, "cb222222-2222-4222-8222-222222222222")
					require.NoError(t, err)
					require.NotNil(t, target)
					next, err := db.NextEmbeddedMail(ctx, launched.Agent.AgentID)
					require.NoError(t, err)
					require.Equal(t, "cb222222-2222-4222-8222-222222222222", next)
					m.OfferDirect(launched.Agent.AgentID)
				}

				// Wait for 3 empty turns to complete (4 requests: goal tool call + 3 empty responses)
				require.Eventually(t, func() bool {
					return requests.Load() == 4
				}, 3*time.Second, 10*time.Millisecond)

				// Verify circuit breaker tripped: evaluator was NEVER called
				time.Sleep(100 * time.Millisecond)
				// Repeated idle wake offers must not bypass the settled breaker.
				for i := 0; i < 3; i++ {
					m.kickoffIdleGoalContinuation(store.EmbeddedBinding{AgentID: launched.Agent.AgentID})
				}
				require.Zero(t, evaluations.Load(), "evaluator must not be called when assistant is empty")
				require.Equal(t, int32(4), requests.Load(), "no continuation request should have been dispatched")

				// Verify agent settles cleanly to idle
				require.Eventually(t, func() bool {
					agent, err := db.ManagedAgent(ctx, launched.Agent.AgentID)
					return err == nil && agent.ActivityState == "idle"
				}, 3*time.Second, 10*time.Millisecond)

				// Verify exactly 2 continuations were dispatched for the 2 re-nudges, and none after circuit breaker tripped
				readOnly, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
				require.NoError(t, err)
				defer readOnly.Close()
				var contCount int
				require.NoError(t, readOnly.QueryRowContext(ctx, `SELECT count(*) FROM embedded_goal_continuations WHERE agent_id=?`, launched.Agent.AgentID).Scan(&contCount))
				require.Equal(t, 2, contCount)
			})
		}
	}
}

func TestEmbeddedGoalConsecutiveContinuationCapHalts(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var requests, evaluations, continuations atomic.Int32
	respond := func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		requests.Add(1)
		for _, msg := range h.Messages {
			if u, ok := msg.(ai.UserMessage); ok && len(u.Content) == 1 {
				if text, ok := u.Content[0].(ai.TextContent); ok && (text.Text == "Continue working on the current goal." || strings.HasPrefix(text.Text, "NOT_DONE: Continue ") || strings.HasPrefix(text.Text, "NOT_DONE: ")) {
					continuations.Add(1)
					return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "autonomous progress text only"}}, ai.StopStop)
				}
				if text, ok := u.Content[0].(ai.TextContent); ok && text.Text == "human prompt to unpause" {
					return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "human prompt acknowledged"}}, ai.StopStop)
				}
			}
		}
		n := requests.Load()
		if n == 1 {
			return providers.FauxAssistantMessage(ai.ContentList{
				providers.FauxToolCall("goal", map[string]any{"action": "set", "goal": "cap test goal"}, "cap-set"),
			}, ai.StopToolUse)
		}
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "initial work final"}}, ai.StopStop)
	}

	steps := make([]providers.FauxResponseStep, 30)
	for i := range steps {
		steps[i] = respond
	}
	reg.SetResponses(steps)

	// Set cap to 3 consecutive continuations with fast 10ms pacing
	m, err := NewManager(ManagerOptions{
		Store:                           db,
		Manifest:                        manifest,
		Config:                          Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:                   CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver:           func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		CodexCredentialStore:            testfixture.CodexAdmission(t),
		GoalContinuationDelay:           10 * time.Millisecond,
		MaxConsecutiveGoalContinuations: 3,
		EmbeddedGoalEvaluator: func(context.Context, store.EmbeddedGoalEvaluationKey) (string, string, error) {
			evaluations.Add(1)
			return store.EmbeddedGoalEvaluationNotDone, "needs more work", nil
		},
	})
	require.NoError(t, err)
	defer m.Close(ctx)
	setEmbeddedRecoveryGeneration(t, ctx, db, m)

	launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{
		Name:               "cap-test-agent",
		Cwd:                root,
		Runtime:            "embedded",
		Model:              &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		InitialInstruction: "start autonomous work",
		RequestID:          "cap-req-1111-1111-1111-111111111111",
	})
	require.NoError(t, err)

	// Wait until 3 continuations run
	require.Eventually(t, func() bool {
		return continuations.Load() >= 3
	}, 4*time.Second, 10*time.Millisecond)

	// After reaching the cap of 3, continuations must halt
	time.Sleep(150 * time.Millisecond)
	contSnap := continuations.Load()
	require.Equal(t, int32(3), contSnap, "continuations should halt at cap of 3")

	// Verify agent settles to idle
	require.Eventually(t, func() bool {
		agent, err := db.ManagedAgent(ctx, launched.Agent.AgentID)
		return err == nil && agent.ActivityState == "idle"
	}, 3*time.Second, 10*time.Millisecond)

	// User sends a human prompt: resets the cap!
	require.NoError(t, m.AdmitPromptWithRequestID(ctx, launched.Agent.AgentID, "human prompt to unpause", "req-human-unpause"))

	// Verify lane counter reset to 0
	lane := m.laneFor(launched.Agent.AgentID)
	require.Zero(t, lane.consecutiveContinuations.Load(), "human prompt must reset consecutive continuations")
}

func TestUnwrapEmbeddedMailText(t *testing.T) {
	raw := `{"Version":"embedded-mail/v1","AttemptID":"123","MessageID":"456","IncarnationID":"789","Sender":"wolfgang","Recipient":"workflows","Body":"WHY DO YOU KEEP STOPPING?"}`
	unwrapped := unwrapEmbeddedMailText(raw)
	require.Contains(t, unwrapped, "[Direct message from wolfgang]: WHY DO YOU KEEP STOPPING?")
	require.Contains(t, unwrapped, "You MUST address and answer this message directly")

	// Plain text should be left unchanged
	plain := "Just a normal prompt"
	require.Equal(t, plain, unwrapEmbeddedMailText(plain))
}

func TestEmbeddedGoalCircuitBreakerTurnErrorsHalt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var requests atomic.Int32
	respond := func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		n := requests.Add(1)
		if n == 1 {
			// Turn 1 step 1: set goal ("error circuit breaker test")
			return providers.FauxAssistantMessage(ai.ContentList{
				providers.FauxToolCall("goal", map[string]any{"action": "set", "goal": "error test"}, "goal_call_1"),
			}, ai.StopToolUse)
		}
		// Subsequent turns: return assistant message with StopReason: ai.StopError
		return &ai.AssistantMessage{
			StopReason:   ai.StopError,
			ErrorMessage: "Antigravity API error 400: function response turn comes immediately after a function call turn",
		}
	}

	steps := make([]providers.FauxResponseStep, 50)
	for i := range steps {
		steps[i] = respond
	}
	reg.SetResponses(steps)

	m, err := NewManager(ManagerOptions{
		Store:                 db,
		Manifest:              manifest,
		Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		CodexCredentialStore:  testfixture.CodexAdmission(t),
		GoalContinuationDelay: 10 * time.Millisecond,
		EmbeddedGoalEvaluator: func(context.Context, store.EmbeddedGoalEvaluationKey) (string, string, error) {
			return store.EmbeddedGoalEvaluationNotDone, "still needs work", nil
		},
	})
	require.NoError(t, err)
	defer m.Close(ctx)
	setEmbeddedRecoveryGeneration(t, ctx, db, m)

	launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{
		Name:               "error-cb-agent",
		Cwd:                root,
		Runtime:            "embedded",
		Model:              &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		InitialInstruction: "start error circuit breaker test",
		RequestID:          "err-cb-1111-1111-1111-111111111111",
	})
	require.NoError(t, err)

	// Verify that the system trips a circuit breaker and does NOT enter a runaway loop
	// (it halts after bounded attempts, e.g. 3 consecutive errors, and activity state settles to idle).
	// If there's a runaway loop, requests will keep climbing well beyond bounded attempts.
	time.Sleep(500 * time.Millisecond)

	// Verify requests are bounded (e.g. at most 1 initial + 3 consecutive errors + 1 margin = <= 5)
	reqCount := requests.Load()
	require.LessOrEqual(t, reqCount, int32(5), "requests should be bounded by circuit breaker, got runaway loop with %d requests", reqCount)

	// Verify activity state settles to idle
	require.Eventually(t, func() bool {
		agent, err := db.ManagedAgent(ctx, launched.Agent.AgentID)
		return err == nil && agent.ActivityState == "idle"
	}, 3*time.Second, 10*time.Millisecond)
}
