package managed

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// TestEmbeddedGoalAutoWakeOnSettingGoal tests that setting a goal on an idle agent
// immediately triggers a continuation turn.
func TestEmbeddedGoalAutoWakeOnSettingGoal(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var ordinaryTurns, continuationTurns atomic.Int32
	respond := func(h ai.TranscriptContext, opts *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		for _, msg := range h.Messages {
			if u, ok := msg.(ai.UserMessage); ok && len(u.Content) == 1 {
				if text, ok := u.Content[0].(ai.TextContent); ok && (text.Text == "Continue working on the current goal." || strings.HasPrefix(text.Text, "NOT_DONE: Continue ")) {
					continuationTurns.Add(1)
					return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "continuation turn finished"}}, ai.StopStop)
				}
			}
		}

		ordinaryTurns.Add(1)
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "initial turn done"}}, ai.StopStop)
	}

	steps := make([]providers.FauxResponseStep, 20)
	for i := range steps {
		steps[i] = respond
	}
	reg.SetResponses(steps)

	m := productionPolicyManager(t, db, root, reg)
	defer m.Close(ctx)

	launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{
		Name:               "auto-wake-agent",
		Cwd:                root,
		Runtime:            "embedded",
		Model:              &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		InitialInstruction: "initial prompt",
		RequestID:          "a1111111-1111-4111-8111-111111111111",
	})
	require.NoError(t, err)

	// Wait for agent to finish initial turn and become idle and ready for controls
	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		_, hasOwner := m.embeddedOwners[launched.Agent.AgentID]
		agent, err := db.ManagedAgent(ctx, launched.Agent.AgentID)
		return err == nil && agent.ActivityState == "idle" && agent.LifecycleState == "running" && ordinaryTurns.Load() >= 1 && hasOwner
	}, 5*time.Second, 10*time.Millisecond)

	require.Zero(t, continuationTurns.Load(), "no continuations before goal is set")

	// Set a goal via directEmbeddedBrowserGoal
	res, err := m.directEmbeddedBrowserGoal(ctx, launched.Agent.AgentID, "goal.control", map[string]any{
		"action": "set",
		"goal":   "implement auto wake feature",
	}, nil)
	require.NoError(t, err)
	require.Equal(t, "ok", res.Status)

	// Setting goal on idle agent should immediately trigger continuation turn!
	require.Eventually(t, func() bool {
		return continuationTurns.Load() >= 1
	}, 5*time.Second, 10*time.Millisecond, "setting a goal on an idle agent must immediately trigger a turn")
}

// TestEmbeddedGoalAutoWakeSubagentActivePreventsTurn tests that setting a goal on
// an agent that has active subagents does NOT trigger a continuation turn until
// subagents finish.
func TestEmbeddedGoalAutoWakeSubagentActivePreventsTurn(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var continuationTurns atomic.Int32
	respond := func(h ai.TranscriptContext, opts *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		for _, msg := range h.Messages {
			if u, ok := msg.(ai.UserMessage); ok && len(u.Content) == 1 {
				if text, ok := u.Content[0].(ai.TextContent); ok && (text.Text == "Continue working on the current goal." || strings.HasPrefix(text.Text, "NOT_DONE: Continue ")) {
					continuationTurns.Add(1)
					return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "continuation turn finished"}}, ai.StopStop)
				}
			}
		}
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "initial turn done"}}, ai.StopStop)
	}

	steps := make([]providers.FauxResponseStep, 20)
	for i := range steps {
		steps[i] = respond
	}
	reg.SetResponses(steps)

	m := productionPolicyManager(t, db, root, reg)
	defer m.Close(ctx)

	launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{
		Name:               "auto-wake-subagent-agent",
		Cwd:                root,
		Runtime:            "embedded",
		Model:              &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		InitialInstruction: "initial prompt",
		RequestID:          "a2222222-2222-4222-8222-222222222222",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		_, hasOwner := m.embeddedOwners[launched.Agent.AgentID]
		agent, err := db.ManagedAgent(ctx, launched.Agent.AgentID)
		return err == nil && agent.ActivityState == "idle" && agent.LifecycleState == "running" && hasOwner
	}, 5*time.Second, 10*time.Millisecond)

	binding := store.EmbeddedBinding{
		AgentID:    launched.Agent.AgentID,
		SessionID:  launched.Agent.PiSessionID,
		Epoch:      launched.Agent.SessionEpoch,
		Generation: launched.Agent.RuntimeGeneration,
	}

	// Register an active in-memory subagent
	m.subagentsMu.Lock()
	m.subagentsActive["child-subagent-1"] = &subagentTask{
		parent:  binding,
		childID: "child-subagent-1",
		status:  "running",
	}
	m.subagentsMu.Unlock()

	// Set a goal
	res, err := m.directEmbeddedBrowserGoal(ctx, launched.Agent.AgentID, "goal.control", map[string]any{
		"action": "set",
		"goal":   "goal with active subagent",
	}, nil)
	require.NoError(t, err)
	require.Equal(t, "ok", res.Status)

	// Since subagent is active, continuation should NOT run
	time.Sleep(200 * time.Millisecond)
	require.Zero(t, continuationTurns.Load(), "continuation turn must NOT run while subagent is active")

	// Finish subagent
	m.subagentsMu.Lock()
	delete(m.subagentsActive, "child-subagent-1")
	m.subagentsMu.Unlock()

	// Kick off goal continuation now that subagents are finished
	m.kickoffIdleGoalContinuation(binding)

	require.Eventually(t, func() bool {
		return continuationTurns.Load() >= 1
	}, 5*time.Second, 10*time.Millisecond)
}

// TestEmbeddedGoalAutoWakeOnStartupRecovery tests that an idle agent with an active goal
// is automatically kicked off on router startup / agent recovery.
func TestEmbeddedGoalAutoWakeOnStartupRecovery(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var continuations atomic.Int32
	respond := func(h ai.TranscriptContext, opts *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		if embeddedIsGoalEvaluation(h) {
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "NOT_DONE"},
			}, ai.StopStop)
		}
		for _, msg := range h.Messages {
			if u, ok := msg.(ai.UserMessage); ok && len(u.Content) == 1 {
				if text, ok := u.Content[0].(ai.TextContent); ok {
					if text.Text == "Continue working on the current goal." || strings.HasPrefix(text.Text, "NOT_DONE: Continue ") {
						continuations.Add(1)
						return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "recovered continuation finished"}}, ai.StopStop)
					}
				}
			}
		}
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "initial work done"}}, ai.StopStop)
	}

	steps := make([]providers.FauxResponseStep, 20)
	for i := range steps {
		steps[i] = respond
	}
	reg.SetResponses(steps)

	m := productionPolicyManager(t, db, root, reg)

	launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{
		Name:               "recovery-goal-agent",
		Cwd:                root,
		Runtime:            "embedded",
		Model:              &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		InitialInstruction: "initial work",
		RequestID:          "a3333333-3333-4333-8333-333333333333",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		_, hasOwner := m.embeddedOwners[launched.Agent.AgentID]
		agent, err := db.ManagedAgent(ctx, launched.Agent.AgentID)
		return err == nil && agent.ActivityState == "idle" && agent.LifecycleState == "running" && hasOwner
	}, 5*time.Second, 10*time.Millisecond)

	// Set goal and wait for it to be recorded
	_, err = m.directEmbeddedBrowserGoal(ctx, launched.Agent.AgentID, "goal.control", map[string]any{
		"action": "set",
		"goal":   "standing recovery goal",
	}, nil)
	require.NoError(t, err)

	// Wait for the continuation from setting goal to complete
	require.Eventually(t, func() bool {
		return continuations.Load() >= 1
	}, 5*time.Second, 10*time.Millisecond)

	// Wait for agent to settle back to idle
	require.Eventually(t, func() bool {
		agent, err := db.ManagedAgent(ctx, launched.Agent.AgentID)
		return err == nil && agent.ActivityState == "idle" && agent.LifecycleState == "running"
	}, 5*time.Second, 10*time.Millisecond)

	// Now close Manager m to simulate shutdown
	require.NoError(t, m.Close(ctx))

	rawDB, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer rawDB.Close()
	// Clear the lease so the recovered manager can acquire owner immediately without waiting 30s TTL
	_, err = rawDB.ExecContext(ctx, `UPDATE embedded_sessions SET owner_id=NULL, owner_expires_at=NULL WHERE agent_id=?`, launched.Agent.AgentID)
	require.NoError(t, err)

	// Reopen second Manager m2 to simulate restart
	continuations.Store(0)
	m2 := productionPolicyManager(t, db, root, reg)
	defer m2.Close(ctx)

	savedAgent, err := db.ManagedAgent(ctx, launched.Agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", savedAgent.ActivityState)
	require.Equal(t, "running", savedAgent.LifecycleState)

	// Boot/recover the agent on startup
	m2.startEmbeddedRoot(*savedAgent, savedAgent.LaunchOperationID, true)

	// The agent should be automatically kicked off on startup/recovery!
	require.Eventually(t, func() bool {
		return continuations.Load() >= 1
	}, 5*time.Second, 10*time.Millisecond, "idle agent with active goal must be kicked off on startup/recovery")
}
