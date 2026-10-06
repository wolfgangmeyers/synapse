package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

	// 1. When no entries exist, return default prompt.
	prompt := m.formatGoalContinuationPrompt(ctx, binding, nil)
	require.Equal(t, goalContinuationDefaultPrompt, prompt)

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
	for _, tc := range []struct {
		name    string
		content ai.ContentList
	}{
		{"empty-content", ai.ContentList{}},
		{"whitespace-text", ai.ContentList{ai.TextContent{Text: "   \n\t  "}}},
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
				// Turn 1 step 1: set goal
				func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					requests.Add(1)
					return providers.FauxAssistantMessage(ai.ContentList{
						providers.FauxToolCall("goal", map[string]any{"action": "set", "goal": "circuit breaker test"}, "cb-set"),
					}, ai.StopToolUse)
				},
				// Turn 1 step 2: empty or whitespace-only assistant message
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

			launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{
				Name:               "circuit-breaker-" + tc.name,
				Cwd:                root,
				Runtime:            "embedded",
				Model:              &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
				InitialInstruction: "start work",
				RequestID:          "cb111111-1111-4111-8111-111111111111",
			})
			require.NoError(t, err)

			// Wait for the turn to complete (2 requests: goal tool call + empty response)
			require.Eventually(t, func() bool {
				return requests.Load() == 2
			}, 3*time.Second, 10*time.Millisecond)

			// Verify circuit breaker tripped: evaluator was NEVER called
			time.Sleep(100 * time.Millisecond)
			require.Zero(t, evaluations.Load(), "evaluator must not be called when assistant is empty")
			require.Equal(t, int32(2), requests.Load(), "no continuation request should have been dispatched")

			// Verify agent settles cleanly to idle
			require.Eventually(t, func() bool {
				agent, err := db.ManagedAgent(ctx, launched.Agent.AgentID)
				return err == nil && agent.ActivityState == "idle"
			}, 3*time.Second, 10*time.Millisecond)

			// Verify no continuations in database
			readOnly, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
			require.NoError(t, err)
			defer readOnly.Close()
			var contCount int
			require.NoError(t, readOnly.QueryRowContext(ctx, `SELECT count(*) FROM embedded_goal_continuations WHERE agent_id=?`, launched.Agent.AgentID).Scan(&contCount))
			require.Zero(t, contCount)
		})
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
				if text, ok := u.Content[0].(ai.TextContent); ok && (text.Text == "Continue working on the current goal." || strings.HasPrefix(text.Text, "NOT_DONE: Continue ")) {
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
	require.Equal(t, 0, lane.consecutiveContinuations, "human prompt must reset consecutive continuations")
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

