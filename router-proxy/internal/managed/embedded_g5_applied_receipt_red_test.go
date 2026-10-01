package managed

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// G5/T6: desired goal state is not evidence that this SDK turn used the goal.
// The durable receipt must bind the saved user turn (sequence AND digest),
// session epoch/generation and exact revision before the next provider request.
// This names a deliberately explicit receipt contract, rather than accepting
// managed_agent_policies.desired_revision as an applied receipt. The evaluator
// and NOT_DONE continuation are downstream of this first gate.
func TestEmbeddedG5AppliedGoalReceiptBeforeNextProviderRequestRED(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	const goal = "finish the saved root goal"
	const callID = "g5-t6-goal-set"
	nextRequest := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseProvider := func() { releaseOnce.Do(func() { close(release) }) }
	var ordinary, evaluations atomic.Int32
	reg.SetResponses([]providers.FauxResponseStep{
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			require.False(t, embeddedIsGoalEvaluation(h))
			ordinary.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal", map[string]any{"action": "set", "goal": goal}, callID)}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			require.False(t, embeddedIsGoalEvaluation(h))
			ordinary.Add(1)
			// The request has begun. Hold the provider here so the test reads the
			// durable state at the required pre-provider-response boundary.
			nextRequest <- struct{}{}
			<-release
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "root turn finished"}}, ai.StopStop)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			require.True(t, embeddedIsGoalEvaluation(h))
			evaluations.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "DONE"}}, ai.StopStop)
		},
	})
	m := productionPolicyManager(t, db, root, reg)
	defer func() { releaseProvider(); _ = m.Close(ctx) }()
	readOnly, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	defer readOnly.Close()
	launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "g5-t6-root", Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "set your own goal", RequestID: "a5555555-5555-4555-8555-555555555581"})
	require.NoError(t, err)
	binding := store.EmbeddedBinding{AgentID: launched.Agent.AgentID, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration}
	select {
	case <-nextRequest:
	case <-time.After(4 * time.Second):
		t.Fatal("next SDK provider request after goal set never arrived")
	}

	// First post-request assertion is the durable applied receipt. A policy
	// row with desired_revision=1 is NOT this receipt. Sequence 2 is the
	// checked initial user entry (after the system declaration).
	var turnDigest, goalText string
	var turnSequence, revision int64
	var enabled int
	err = readOnly.QueryRowContext(ctx, `SELECT turn_entry_seq,turn_entry_digest,goal_revision,goal_enabled,goal_text
		FROM embedded_goal_applied_receipts
		WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND turn_entry_seq=?`,
		binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation, 2,
	).Scan(&turnSequence, &turnDigest, &revision, &enabled, &goalText)
	require.NoError(t, err, "G5/T6 RED: missing durable per-SDK-turn applied-goal receipt before next provider request; policy desired state is not applied state; all evaluator/NOT_DONE/reopen assertions unexecuted")
	binding = inspectManagedOwnerBinding(t, path, binding)
	snapshot, err := db.ReopenEmbedded(ctx, binding)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(snapshot.Entries), 4)
	require.Equal(t, "user", snapshot.Entries[1].Kind)
	require.Equal(t, snapshot.Entries[1].Sequence, turnSequence)
	require.Equal(t, snapshot.Entries[1].Digest, turnDigest)
	require.EqualValues(t, 1, revision)
	require.Equal(t, 1, enabled)
	require.Equal(t, goal, goalText)
	policy, err := db.ManagedGoalPolicyFor(ctx, binding.AgentID)
	require.NoError(t, err)
	require.EqualValues(t, 1, policy.DesiredRevision)
	require.EqualValues(t, 1, policy.AppliedRevision)
	require.Equal(t, "applied", policy.ProjectionState)

	// Downstream: final assistant must be checked into this same SDK turn.
	// Never release the provider before the receipt/projection assertions.
	releaseProvider()
	require.Eventually(t, func() bool {
		saved, reopenErr := db.ReopenEmbedded(ctx, binding)
		if reopenErr != nil || len(saved.Entries) < 5 {
			return false
		}
		last := saved.Entries[len(saved.Entries)-1]
		return last.Kind == "assistant" && strings.Contains(string(last.Body), "root turn finished")
	}, 3*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		var verdict string
		return readOnly.QueryRow(`SELECT state FROM embedded_goal_evaluations WHERE agent_id=?`, binding.AgentID).Scan(&verdict) == nil && verdict == "DONE"
	}, 3*time.Second, 10*time.Millisecond)
	require.NoError(t, m.Close(ctx))
	require.EqualValues(t, 2, ordinary.Load())
	require.EqualValues(t, 1, evaluations.Load())
	settled, err := db.ReopenEmbedded(ctx, binding)
	require.NoError(t, err)
	require.Len(t, settled.Entries, 5, "evaluation must not append another ordinary assistant or result")
}
