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

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/sky-valley/pi/coding"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// TestEmbeddedProductionAutomaticCompactionSuccessfulReducingContinuedTurns verifies
// the complete positive path:
// 1. Initial turn fits context window
// 2. Subsequent prompt exceeds window - headroom, triggering automatic compaction
// 3. History summarized, candidate tokens < before tokens, context swapped
// 4. Post-compaction provider turn continues successfully
// 5. Durable checkpoint committed to Store
// 6. Cold reopen restores checkpoint with permanent replay
// 7. Continued provider turn after cold reopen completes successfully.
func TestEmbeddedProductionAutomaticCompactionSuccessfulReducingContinuedTurns(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	const window = 30000
	const maxTokens = 4096
	pinned := ai.GetModel("openai-codex", "gpt-5.6-terra")
	require.NotNil(t, pinned)

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{
		Models: []providers.FauxModelDefinition{{
			ID:            "auto-sustained-fit",
			ContextWindow: window,
			MaxTokens:     maxTokens,
			Input:         []string{"text"},
		}},
	})
	defer reg.Unregister()

	var ordinary, summaries atomic.Int32
	respond := func(h ai.TranscriptContext, opts *ai.SimpleStreamOptions, _ *providers.FauxState, model *ai.Model) *ai.AssistantMessage {
		require.NotNil(t, opts.MaxTokens)
		require.Equal(t, maxTokens, *opts.MaxTokens, "ordinary and summary dispatch must use the host cap")
		if classifyG5CompactionRequest(h, opts, "").kind == "summary" {
			summaries.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "## Goal\nContinue working on the project.\n## Key Context\nPrior work preserved."}}, ai.StopStop)
		}
		require.LessOrEqual(t, coding.EstimateContextTokens(h.Messages)+embeddedContextHeadroom, model.ContextWindow, "every ordinary/tool continuation must fit before dispatch")
		n := ordinary.Add(1)
		text := "fitting final"
		if n == 1 {
			text = strings.Repeat("H", 15000)
		}
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: text}}, ai.StopStop)
	}

	steps := make([]providers.FauxResponseStep, 10)
	for i := range steps {
		steps[i] = respond
	}
	reg.SetResponses(steps)

	m := productionPolicyManager(t, db, root, reg)
	defer m.Close(ctx)

	// Turn 1: 18000 tokens user prompt. Fits initial budget.
	prompt1 := strings.Repeat("P", 18000)
	launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{
		Name:               "sustained-compaction-root",
		Cwd:                root,
		Runtime:            "embedded",
		Model:              &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		InitialInstruction: prompt1,
		RequestID:          "b5555555-5555-4555-8555-555555555585",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, launched.Agent.AgentID)
		if e != nil {
			return false
		}
		m.mu.Lock()
		_, ready := m.embeddedOwners[a.AgentID]
		m.mu.Unlock()
		return a.LifecycleState == "running" && ready
	}, 3*time.Second, 10*time.Millisecond)

	saved, err := db.ManagedAgent(ctx, launched.Agent.AgentID)
	require.NoError(t, err)
	m.mu.Lock()
	owner := m.embeddedOwners[saved.AgentID]
	m.mu.Unlock()
	binding := owner.binding

	before, err := db.ReopenEmbedded(ctx, binding)
	require.NoError(t, err)
	require.Nil(t, before.Checkpoint, "no checkpoint before automatic compaction")

	// Calculate pre-compaction tokens before Turn 2 prompt
	codex := *ai.GetModel("openai-codex", "gpt-5.6-terra")
	codex.ContextWindow = window

	// Turn 2: 20000 tokens user prompt. Total context exceeds budget (window - headroom = 13616).
	// This triggers automatic compaction!
	prompt2 := strings.Repeat("Q", 20000)
	err = m.runEmbeddedSessionPrompt(context.WithValue(ctx, embeddedOwnerContextKey{}, owner), *saved, prompt2)
	require.NoError(t, err)

	// Assert compaction happened and reduced tokens
	require.EqualValues(t, 1, summaries.Load(), "automatic compaction summary must have executed")
	require.EqualValues(t, 2, ordinary.Load(), "ordinary provider turn must complete after compaction")

	after, err := db.ReopenEmbedded(ctx, binding)
	require.NoError(t, err)
	require.NotNil(t, after.Checkpoint, "checkpoint committed to durable store")
	for i, entry := range before.Entries {
		require.Equal(t, entry, after.Entries[i], "compaction preserves immutable entries")
	}

	status, err := db.EmbeddedOwnCompactionStatus(ctx, binding)
	require.NoError(t, err)
	require.Equal(t, "committed", status.Outcome)

	// Cold reopen verification: a fresh Store and SDK session restores the checkpoint
	cold, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer cold.Close()

	reopened, err := cold.ReopenEmbedded(ctx, binding)
	require.NoError(t, err)
	require.Equal(t, after.Checkpoint, reopened.Checkpoint)

	fresh := coding.NewSession(coding.SessionOptions{
		SessionID:          binding.SessionID,
		Model:              &codex,
		Tools:              []agent.AgentTool{},
		OrderedPersistence: &embeddedPersistence{db: cold, binding: binding},
	})
	p := &embeddedPersistence{db: cold, binding: binding}
	require.NoError(t, p.restore(ctx, fresh))
	fresh.EnableCompaction(coding.CompactionSettings{Enabled: false})
	view := fresh.Agent.TransformContext(ctx, fresh.History())
	candidateTokens := coding.EstimateContextTokens(view)
	require.LessOrEqual(t, candidateTokens+embeddedContextHeadroom, window)

	// Verify candidate tokens < before uncompacted tokens
	uncompactedTokens := 0
	for _, entry := range after.Entries {
		msg, decodeErr := ai.UnmarshalMessage(entry.Body)
		require.NoError(t, decodeErr)
		uncompactedTokens += coding.EstimateMessageTokens(msg)
	}
	require.Less(t, candidateTokens, uncompactedTokens, "candidate tokens < before tokens: compaction strictly reduced context")

	var checkpoint coding.OrderedCheckpoint
	require.NoError(t, json.Unmarshal(after.Checkpoint.Body, &checkpoint))
	require.Equal(t, after.Entries[checkpoint.ThroughSequence-1].Digest, after.Checkpoint.AncestorDigest)
	require.Contains(t, checkpoint.Summary, "## Goal")

	// Continued provider turn post-compaction: Turn 3
	require.NoError(t, m.runEmbeddedSessionPrompt(context.WithValue(ctx, embeddedOwnerContextKey{}, owner), *saved, "continue work after compaction"))
	require.EqualValues(t, 3, ordinary.Load(), "provider turn continues successfully post-compaction")
	require.EqualValues(t, 1, summaries.Load(), "no extra compaction needed for fitting prompt")
}

// TestEmbeddedGoalSnoozeFixedClockDueCompletionDurability verifies:
// 1. Standing goal set and snoozed in turn 1
// 2. Evaluation runs after turn 1 and commits NOT_DONE
// 3. Goal snooze sets due_at and continuation is reserved
// 4. Advancing deterministic fixed clock past due fires the due wake continuation
// 5. Continuation provider turn executes ("Continue working on the current goal.")
// 6. Follow-up prompt completes the work
// 7. Second evaluation completes with status "completed" (DONE)
// 8. Applied receipts and evaluations are committed durably.
func TestEmbeddedGoalSnoozeFixedClockDueCompletionDurability(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	clock := deterministic.NewFixedClock(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC))
	db, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer db.Close()

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var ordinary, evaluations, wakes atomic.Int32
	respond := func(h ai.TranscriptContext, opts *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		for _, msg := range h.Messages {
			if u, ok := msg.(ai.UserMessage); ok && len(u.Content) == 1 {
				if text, ok := u.Content[0].(ai.TextContent); ok && (text.Text == "Continue working on the current goal." || strings.HasPrefix(text.Text, "NOT_DONE: Continue ") || strings.HasPrefix(text.Text, "NOT_DONE: ")) {
					wakes.Add(1)
					return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "due wake continuation final"}}, ai.StopStop)
				}
			}
		}
		n := ordinary.Add(1)
		switch n {
		case 1:
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal", map[string]any{"action": "set", "goal": "complete durability goal"}, "durability-set")}, ai.StopToolUse)
		case 2:
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal_snooze", map[string]any{"duration_ms": 1000}, "durability-snooze")}, ai.StopToolUse)
		case 3:
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "snoozed turn final"}}, ai.StopStop)
		case 4:
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "completed all work final"}}, ai.StopStop)
		default:
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "unexpected turn final"}}, ai.StopStop)
		}
	}

	steps := make([]providers.FauxResponseStep, 10)
	for i := range steps {
		steps[i] = respond
	}
	reg.SetResponses(steps)

	m, err := NewManager(ManagerOptions{
		Store:                db,
		Manifest:             manifestManagedTools(t),
		Clock:                clock,
		Config:               Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:        CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		CodexCredentialStore: testfixture.CodexAdmission(t),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model {
			return reg.GetModel()
		},
		EmbeddedGoalEvaluator: func(_ context.Context, _ store.EmbeddedGoalEvaluationKey) (string, string, error) {
			n := evaluations.Add(1)
			if n == 1 {
				return store.EmbeddedGoalEvaluationNotDone, "still working on goal", nil
			}
			return store.EmbeddedGoalEvaluationDone, "goal successfully finished", nil
		},
	})
	require.NoError(t, err)
	defer m.Close(ctx)
	setEmbeddedRecoveryGeneration(t, ctx, db, m)

	launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{
		Name:               "goal-snooze-durability",
		Cwd:                root,
		Runtime:            "embedded",
		Model:              &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		InitialInstruction: "set and snooze goal",
		RequestID:          "a5555555-5555-4555-8555-555555555586",
	})
	require.NoError(t, err)

	ro, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	defer ro.Close()

	// Wait for turn 1 to complete and first evaluation to commit NOT_DONE
	require.Eventually(t, func() bool {
		return ordinary.Load() == 3 && evaluations.Load() == 1
	}, 3*time.Second, 10*time.Millisecond)

	m.mu.Lock()
	owner := m.embeddedOwners[launched.Agent.AgentID]
	m.mu.Unlock()
	binding := owner.binding

	// Check durable evaluation state is NOT_DONE
	var eval1State string
	require.NoError(t, ro.QueryRowContext(ctx, `SELECT state FROM embedded_goal_evaluations WHERE agent_id=? ORDER BY completed_at ASC LIMIT 1`, binding.AgentID).Scan(&eval1State))
	require.Equal(t, store.EmbeddedGoalEvaluationNotDone, eval1State)

	// Check continuation is reserved with due_at
	var contState, contDue string
	require.NoError(t, ro.QueryRowContext(ctx, `SELECT state, due_at FROM embedded_goal_continuations WHERE agent_id=?`, binding.AgentID).Scan(&contState, &contDue))
	require.Equal(t, "reserved", contState)
	require.NotEmpty(t, contDue)
	require.Equal(t, clock.Now().Add(time.Second).Format("2006-01-02T15:04:05.000Z"), contDue)

	// Check applied receipt was committed for turn 1
	var receiptsCount int
	require.NoError(t, ro.QueryRowContext(ctx, `SELECT count(*) FROM embedded_goal_applied_receipts WHERE agent_id=? AND goal_enabled=1`, binding.AgentID).Scan(&receiptsCount))
	require.Equal(t, 1, receiptsCount)

	// Before clock advances, due wake has NOT fired
	require.Zero(t, wakes.Load())

	// Advance clock past due: 1000ms
	require.NoError(t, clock.Advance(time.Second))

	// Due wake fires!
	require.Eventually(t, func() bool {
		return wakes.Load() == 1
	}, 3*time.Second, 10*time.Millisecond)

	// Check continuation state updated to started
	require.Eventually(t, func() bool {
		var s string
		return ro.QueryRowContext(ctx, `SELECT state FROM embedded_goal_continuations WHERE agent_id=?`, binding.AgentID).Scan(&s) == nil && s == "started"
	}, 3*time.Second, 10*time.Millisecond)

	saved, err := db.ManagedAgent(ctx, binding.AgentID)
	require.NoError(t, err)

	// Follow-up turn completes the work and triggers second evaluation
	err = m.runEmbeddedSessionPrompt(context.WithValue(ctx, embeddedOwnerContextKey{}, owner), *saved, "all goal requirements satisfied")
	require.NoError(t, err)

	// Second evaluation completes with DONE (completed)
	require.Eventually(t, func() bool {
		return evaluations.Load() >= 2
	}, 3*time.Second, 10*time.Millisecond)

	var eval2State string
	require.NoError(t, ro.QueryRowContext(ctx, `SELECT state FROM embedded_goal_evaluations WHERE agent_id=? ORDER BY completed_at DESC LIMIT 1`, binding.AgentID).Scan(&eval2State))
	require.Equal(t, store.EmbeddedGoalEvaluationDone, eval2State)

	// Applied receipt committed for the completed turn
	require.NoError(t, ro.QueryRowContext(ctx, `SELECT count(*) FROM embedded_goal_applied_receipts WHERE agent_id=? AND goal_enabled=1`, binding.AgentID).Scan(&receiptsCount))
	require.GreaterOrEqual(t, receiptsCount, 2)

	// Reopen transcript verifies full durability
	snapshot, err := db.ReopenEmbedded(ctx, binding)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(snapshot.Entries), 6)
}

func manifestManagedTools(t *testing.T) Manifest {
	t.Helper()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	return manifest
}
