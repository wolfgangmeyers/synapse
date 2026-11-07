package managed

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestPiRootPromptPreservesBaseline(t *testing.T) {
	p := resolveEmbeddedWorkerSystemPrompt("root", "root-id", "/project")
	require.Contains(t, p, "embedded worker running")
	require.NotContains(t, p, "embedded root running")
}

func TestPiForegroundEvaluatorNotCanceledByGoalChange(t *testing.T) {
	f := newFailureFixture(t)
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(release) })
	f.m.embeddedGoalEvaluator = func(ctx context.Context, _ store.EmbeddedGoalEvaluationKey) (string, string, error) {
		close(entered)
		select {
		case <-ctx.Done():
			close(canceled)
			return "", "", ctx.Err()
		case <-release:
			return store.EmbeddedGoalEvaluationDone, "done", nil
		}
	}
	owner := f.launch(t, "")
	f.m.startWG.Wait()
	ctx := context.Background()
	_, err := f.db.UpdateManagedGoalConfig(ctx, owner.binding.AgentID, store.ManagedGoalConfig{Enabled: true, Goal: "original", Settings: store.DefaultManagedGoalSettings()})
	require.NoError(t, err)
	_, err = f.raw.Exec(`UPDATE managed_agent_policies SET goal_enabled=1,goal_text='original',desired_revision=1,applied_revision=1,projection_state='applied' WHERE agent_id=?`, owner.binding.AgentID)
	require.NoError(t, err)
	body, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "work"}}})
	require.NoError(t, err)
	user, err := f.db.AppendEmbeddedEntry(ctx, owner.binding, "user", body)
	require.NoError(t, err)
	require.NoError(t, f.db.EnterEmbeddedNonMailProviderTurn(ctx, owner.binding, user.Sequence, user.Digest))
	receipt, _, err := f.db.CommitEmbeddedGoalAppliedReceipt(ctx, owner.binding, store.EmbeddedGoalAppliedReceiptRequest{TurnKey: "pi-fixture", TurnEntrySeq: user.Sequence, TurnEntryDigest: user.Digest, GoalRevision: 1})
	require.NoError(t, err)
	body, err = json.Marshal(ai.AssistantMessage{Content: ai.ContentList{ai.TextContent{Text: "progress"}}, StopReason: ai.StopStop})
	require.NoError(t, err)
	final, err := f.db.AppendEmbeddedEntry(ctx, owner.binding, "assistant", body)
	require.NoError(t, err)
	p := &embeddedPersistence{turnSeq: user.Sequence, turnDigest: user.Digest, seq: final.Sequence, digest: final.Digest}
	require.NoError(t, f.m.evaluateEmbeddedTurn(ctx, owner.binding, p, &receipt, &ai.Model{Provider: "openai-codex", ID: "test"}, nil))
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("evaluator did not start")
	}
	_, err = f.m.directEmbeddedBrowserGoal(ctx, owner.binding.AgentID, "goal.control", map[string]any{"action": "set", "goal": "revised"}, nil)
	require.NoError(t, err)
	select {
	case <-canceled:
		t.Fatal("Pi evaluator canceled by goal change")
	case <-time.After(200 * time.Millisecond):
	}
}
