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
				if text, ok := u.Content[0].(ai.TextContent); ok && (text.Text == "Continue working on the current goal." || strings.HasPrefix(text.Text, "NOT_DONE: Continue ") || strings.HasPrefix(text.Text, "NOT_DONE: ")) {
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
				if text, ok := u.Content[0].(ai.TextContent); ok && (text.Text == "Continue working on the current goal." || strings.HasPrefix(text.Text, "NOT_DONE: Continue ") || strings.HasPrefix(text.Text, "NOT_DONE: ")) {
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
					if text.Text == "Continue working on the current goal." || strings.HasPrefix(text.Text, "NOT_DONE: ") {
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

// TestEmbeddedGoalResumeAfterSubagentCompletionMixedCase tests that an embedded worker
// with a mixed-case agent ID (like "Context-visibiity") wakes up cleanly when its subagents
// complete, without racing with mailbox delivery or causing head mismatch crashes.
func TestEmbeddedGoalResumeAfterSubagentCompletionMixedCase(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var continuationTurns, mailTurns atomic.Int32
	respond := func(h ai.TranscriptContext, opts *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		if embeddedIsGoalEvaluation(h) {
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "NOT_DONE"},
			}, ai.StopStop)
		}
		for _, msg := range h.Messages {
			if u, ok := msg.(ai.UserMessage); ok && len(u.Content) == 1 {
				if text, ok := u.Content[0].(ai.TextContent); ok {
					if strings.Contains(text.Text, "Subagent Completed") || strings.Contains(text.Text, "embedded-mail") {
						mailTurns.Add(1)
						return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "mail processed"}}, ai.StopStop)
					}
					if text.Text == "Continue working on the current goal." || strings.HasPrefix(text.Text, "NOT_DONE: ") {
						continuationTurns.Add(1)
						return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "continuation work finished"}}, ai.StopStop)
					}
				}
			}
		}
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "initial turn done"}}, ai.StopStop)
	}

	steps := make([]providers.FauxResponseStep, 30)
	for i := range steps {
		steps[i] = respond
	}
	reg.SetResponses(steps)

	m := productionPolicyManager(t, db, root, reg)
	defer m.Close(ctx)

	const mixedCaseAgentID = "Context-visibiity"

	launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{
		Name:               mixedCaseAgentID,
		Cwd:                root,
		Runtime:            "embedded",
		Model:              &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		InitialInstruction: "start work",
		RequestID:          "a4444444-4444-4444-8444-444444444444",
	})
	require.NoError(t, err)
	require.Equal(t, mixedCaseAgentID, launched.Agent.AgentID)

	// Wait for agent to become idle
	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		_, hasOwner := m.embeddedOwners[mixedCaseAgentID]
		agent, err := db.ManagedAgent(ctx, mixedCaseAgentID)
		return err == nil && agent.ActivityState == "idle" && agent.LifecycleState == "running" && hasOwner
	}, 5*time.Second, 10*time.Millisecond)

	// Set goal on the agent
	res, err := m.directEmbeddedBrowserGoal(ctx, mixedCaseAgentID, "goal.control", map[string]any{
		"action": "set",
		"goal":   "Advance to the end of the workflow for embedded worker context visibility",
	}, nil)
	require.NoError(t, err)
	require.Equal(t, "ok", res.Status)

	// Wait for continuation turn from goal setting
	require.Eventually(t, func() bool {
		return continuationTurns.Load() >= 1
	}, 5*time.Second, 10*time.Millisecond)

	// Wait for agent to settle back to idle
	require.Eventually(t, func() bool {
		agent, err := db.ManagedAgent(ctx, mixedCaseAgentID)
		return err == nil && agent.ActivityState == "idle" && agent.LifecycleState == "running"
	}, 5*time.Second, 10*time.Millisecond)

	// Now simulate subagent completion by creating an active subagent task and finalizing it
	owner := m.embeddedOwners[mixedCaseAgentID]
	task := &subagentTask{
		childID: "subagent-" + mixedCaseAgentID + "-child-1",
		parent:  owner.binding,
		attempt: 1,
		status:  "running",
	}
	m.subagentsMu.Lock()
	m.subagentsActive[task.childID] = task
	m.subagentsMu.Unlock()

	// While subagent is active, kickoff continuation should NOT run
	m.kickoffIdleGoalContinuation(owner.binding)
	time.Sleep(200 * time.Millisecond)

	// Now complete the subagent: remove from active and resume
	m.subagentsMu.Lock()
	delete(m.subagentsActive, task.childID)
	m.subagentsMu.Unlock()

	// Call resumeParentGoalIfIdle
	m.resumeParentGoalIfIdle(owner.binding)

	// Parent should wake up cleanly and continue working
	require.Eventually(t, func() bool {
		return continuationTurns.Load() >= 2
	}, 5*time.Second, 10*time.Millisecond, "mixed-case agent must wake up on subagent completion without colliding")

	// Verify no head mismatch errors occurred in managed agent
	agent, err := db.ManagedAgent(ctx, mixedCaseAgentID)
	require.NoError(t, err)
	require.Empty(t, agent.LastErrorCode)
	require.Empty(t, agent.LastErrorMessage)
}

// TestEmbeddedGoalResumeIdleWorker verifies that calling ResumeIdleEmbeddedGoals
// wakes up an idle agent that has an active goal and no active subagents.
func TestEmbeddedGoalResumeIdleWorker(t *testing.T) {
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
		if embeddedIsGoalEvaluation(h) {
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "NOT_DONE"},
			}, ai.StopStop)
		}
		for _, msg := range h.Messages {
			if u, ok := msg.(ai.UserMessage); ok && len(u.Content) == 1 {
				if text, ok := u.Content[0].(ai.TextContent); ok {
					if text.Text == "Continue working on the current goal." || strings.HasPrefix(text.Text, "NOT_DONE: ") {
						continuationTurns.Add(1)
						return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "continuation work finished"}}, ai.StopStop)
					}
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
		Name:               "idle-resume-agent",
		Cwd:                root,
		Runtime:            "embedded",
		Model:              &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		InitialInstruction: "start work",
		RequestID:          "a5555555-5555-5555-8555-555555555555",
	})
	require.NoError(t, err)

	// Wait for agent to become idle and owner published
	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		_, hasOwner := m.embeddedOwners[launched.Agent.AgentID]
		agent, err := db.ManagedAgent(ctx, launched.Agent.AgentID)
		return err == nil && agent.ActivityState == "idle" && agent.LifecycleState == "running" && hasOwner
	}, 5*time.Second, 10*time.Millisecond)

	// Set goal
	_, err = m.directEmbeddedBrowserGoal(ctx, launched.Agent.AgentID, "goal.control", map[string]any{
		"action": "set",
		"goal":   "complete idle resume test",
	}, nil)
	require.NoError(t, err)

	// Wait for first continuation turn to finish and settle back to idle
	require.Eventually(t, func() bool {
		return continuationTurns.Load() >= 1
	}, 5*time.Second, 10*time.Millisecond)

	require.Eventually(t, func() bool {
		agent, err := db.ManagedAgent(ctx, launched.Agent.AgentID)
		return err == nil && agent.ActivityState == "idle" && agent.LifecycleState == "running"
	}, 5*time.Second, 10*time.Millisecond)

	currentCount := continuationTurns.Load()

	// Call ResumeIdleEmbeddedGoals - should resume the idle worker
	require.NoError(t, m.ResumeIdleEmbeddedGoals(ctx))

	require.Eventually(t, func() bool {
		return continuationTurns.Load() > currentCount
	}, 5*time.Second, 10*time.Millisecond, "ResumeIdleEmbeddedGoals must resume an idle agent with active goal")
}

func TestIsEmbeddedGoalActive(t *testing.T) {
	now := "2026-10-13T12:00:00.000Z"
	future := "2026-10-13T13:00:00.000Z"
	past := "2026-10-13T11:00:00.000Z"

	require.False(t, isEmbeddedGoalActive(nil, now))
	require.False(t, isEmbeddedGoalActive(&store.ManagedGoalPolicy{Config: store.ManagedGoalConfig{Enabled: false}}, now))

	// Active goal: enabled, idle evaluator, no done outcome
	require.True(t, isEmbeddedGoalActive(&store.ManagedGoalPolicy{
		Config:         store.ManagedGoalConfig{Enabled: true},
		EvaluatorState: "idle",
	}, now))

	// Done goal: evaluator_state = done
	require.False(t, isEmbeddedGoalActive(&store.ManagedGoalPolicy{
		Config:         store.ManagedGoalConfig{Enabled: true},
		EvaluatorState: "done",
	}, now))

	// Completed outcome
	require.False(t, isEmbeddedGoalActive(&store.ManagedGoalPolicy{
		Config:      store.ManagedGoalConfig{Enabled: true},
		LastOutcome: store.EmbeddedGoalEvaluationCompleted,
	}, now))

	// Done outcome
	require.False(t, isEmbeddedGoalActive(&store.ManagedGoalPolicy{
		Config:      store.ManagedGoalConfig{Enabled: true},
		LastOutcome: store.EmbeddedGoalEvaluationDone,
	}, now))

	// Evaluating
	require.False(t, isEmbeddedGoalActive(&store.ManagedGoalPolicy{
		Config:         store.ManagedGoalConfig{Enabled: true},
		EvaluatorState: "evaluating",
	}, now))

	// Snoozed in future
	require.False(t, isEmbeddedGoalActive(&store.ManagedGoalPolicy{
		Config:       store.ManagedGoalConfig{Enabled: true},
		SnoozedUntil: future,
	}, now))

	// Snoozed in past (expired snooze)
	require.True(t, isEmbeddedGoalActive(&store.ManagedGoalPolicy{
		Config:       store.ManagedGoalConfig{Enabled: true},
		SnoozedUntil: past,
	}, now))
}

// TestEmbeddedGoalResumeIdleWorker_CompletedGoalDoesNotResumeOrSpin verifies that
// calling ResumeIdleEmbeddedGoals, MaybeResumeEmbeddedGoal, or OfferDirect on an
// idle agent whose goal is COMPLETED or DONE does not trigger continuation turns or spin.
func TestEmbeddedGoalResumeIdleWorker_CompletedGoalDoesNotResumeOrSpin(t *testing.T) {
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
		if embeddedIsGoalEvaluation(h) {
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "VERDICT: COMPLETED\nREASON: All tasks finished"},
			}, ai.StopStop)
		}
		for _, msg := range h.Messages {
			if u, ok := msg.(ai.UserMessage); ok && len(u.Content) == 1 {
				if text, ok := u.Content[0].(ai.TextContent); ok {
					if text.Text == "Continue working on the current goal." || strings.HasPrefix(text.Text, "NOT_DONE: ") {
						continuationTurns.Add(1)
						return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "unwanted continuation work"}}, ai.StopStop)
					}
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
		Name:               "completed-goal-agent",
		Cwd:                root,
		Runtime:            "embedded",
		Model:              &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		InitialInstruction: "start work",
		RequestID:          "a5555555-5555-5555-8555-555555555599",
	})
	require.NoError(t, err)

	// Wait for agent to become idle and owner published
	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		_, hasOwner := m.embeddedOwners[launched.Agent.AgentID]
		agent, err := db.ManagedAgent(ctx, launched.Agent.AgentID)
		return err == nil && agent.ActivityState == "idle" && agent.LifecycleState == "running" && hasOwner
	}, 5*time.Second, 10*time.Millisecond)

	// Set goal
	_, err = m.directEmbeddedBrowserGoal(ctx, launched.Agent.AgentID, "goal.control", map[string]any{
		"action": "set",
		"goal":   "complete idle resume test",
	}, nil)
	require.NoError(t, err)

	// Wait for goal evaluation to complete with COMPLETED
	require.Eventually(t, func() bool {
		policy, err := db.ManagedGoalPolicyFor(ctx, launched.Agent.AgentID)
		return err == nil && policy != nil && (policy.EvaluatorState == "done" || policy.LastOutcome == store.EmbeddedGoalEvaluationCompleted || policy.LastOutcome == store.EmbeddedGoalEvaluationDone)
	}, 5*time.Second, 10*time.Millisecond, "goal evaluation must complete with COMPLETED")

	// Wait for agent to be idle
	require.Eventually(t, func() bool {
		agent, err := db.ManagedAgent(ctx, launched.Agent.AgentID)
		return err == nil && agent.ActivityState == "idle" && agent.LifecycleState == "running"
	}, 5*time.Second, 10*time.Millisecond)

	settledCount := continuationTurns.Load()
	require.Positive(t, settledCount, "at least one turn ran to work on the goal before completion")

	// Simulate multiple periodic sweeps: calling ResumeIdleEmbeddedGoals must NOT resume or spin
	for i := 0; i < 5; i++ {
		require.NoError(t, m.ResumeIdleEmbeddedGoals(ctx))
	}
	// Calling MaybeResumeEmbeddedGoal directly must NOT resume
	m.MaybeResumeEmbeddedGoal(launched.Agent.AgentID)
	// Calling OfferDirect must NOT resume
	m.OfferDirect(launched.Agent.AgentID)

	// Give any async tasks a moment to settle
	time.Sleep(200 * time.Millisecond)

	require.Equal(t, settledCount, continuationTurns.Load(), "completed goal must not trigger continuation turns or spin")

	// Agent must remain idle
	agent, err := db.ManagedAgent(ctx, launched.Agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", agent.ActivityState)
	require.Equal(t, "running", agent.LifecycleState)
}
