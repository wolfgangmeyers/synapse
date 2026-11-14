package managed

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestRestartFencesPendingNonClaudeGoalEvaluation(t *testing.T) {
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	f.m.startWG.Wait()
	ctx := context.Background()
	_, err := f.db.UpdateManagedGoalConfig(ctx, owner.binding.AgentID, store.ManagedGoalConfig{Enabled: true, Goal: "Finish the task", Settings: store.DefaultManagedGoalSettings()})
	require.NoError(t, err)
	policy, err := f.db.ManagedGoalPolicyFor(ctx, owner.binding.AgentID)
	require.NoError(t, err)
	body, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "work"}}})
	require.NoError(t, err)
	user, err := f.db.AppendEmbeddedEntry(ctx, owner.binding, "user", body)
	require.NoError(t, err)
	require.NoError(t, f.db.EnterEmbeddedNonMailProviderTurn(ctx, owner.binding, user.Sequence, user.Digest))
	receipt, _, err := f.db.CommitEmbeddedGoalAppliedReceipt(ctx, owner.binding, store.EmbeddedGoalAppliedReceiptRequest{TurnKey: "restart-fence", TurnEntrySeq: user.Sequence, TurnEntryDigest: user.Digest, GoalRevision: policy.DesiredRevision})
	require.NoError(t, err)
	body, err = json.Marshal(ai.AssistantMessage{Content: ai.ContentList{ai.TextContent{Text: "Work in progress"}}, StopReason: ai.StopStop})
	require.NoError(t, err)
	final, err := f.db.AppendEmbeddedEntry(ctx, owner.binding, "assistant", body)
	require.NoError(t, err)
	lane := f.m.laneFor(owner.binding.AgentID)
	p := &embeddedPersistence{turnSeq: user.Sequence, turnDigest: user.Digest, seq: final.Sequence, digest: final.Digest, claudeRestartGeneration: lane.claudeRestartGeneration.Load()}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	f.m.embeddedBeforeGoalCommit = func() { close(entered); <-release }
	require.NoError(t, f.m.evaluateEmbeddedTurn(ctx, owner.binding, p, &receipt, &ai.Model{Provider: "test-provider", ID: "test-model"}, nil))
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("goal evaluation did not reach commit boundary")
	}
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM embedded_goal_evaluations WHERE agent_id=? AND state='UNKNOWN'`, owner.binding.AgentID))
	_, err = f.m.ReserveRestart(ctx, RestartRequest{AgentID: owner.binding.AgentID, Method: "POST", CanonicalPath: "/restart", RequestID: "restart-goal-fence", CanonicalBodyHash: "hash"})
	require.NoError(t, err)
	unblock()
	f.m.startWG.Wait()
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM embedded_goal_evaluations WHERE agent_id=? AND state='UNKNOWN'`, owner.binding.AgentID))
	require.Equal(t, 0, f.count(t, `SELECT count(*) FROM embedded_goal_continuations WHERE agent_id=?`, owner.binding.AgentID))
}
