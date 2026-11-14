package managed

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestRestartRejectsStaleForegroundEmptyCallback(t *testing.T) {
	for _, provider := range []string{claudeCodeProvider, "test-provider"} {
		t.Run(provider, func(t *testing.T) {
			f := newFailureFixture(t)
			owner := f.launch(t, "")
			f.m.startWG.Wait()
			ctx := context.Background()
			_, err := f.raw.Exec(`UPDATE managed_agent_policies SET goal_enabled=1,goal_text='original',desired_revision=1,applied_revision=1,projection_state='applied' WHERE agent_id=?`, owner.binding.AgentID)
			require.NoError(t, err)
			body, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "work"}}})
			require.NoError(t, err)
			user, err := f.db.AppendEmbeddedEntry(ctx, owner.binding, "user", body)
			require.NoError(t, err)
			require.NoError(t, f.db.EnterEmbeddedNonMailProviderTurn(ctx, owner.binding, user.Sequence, user.Digest))
			receipt, _, err := f.db.CommitEmbeddedGoalAppliedReceipt(ctx, owner.binding, store.EmbeddedGoalAppliedReceiptRequest{TurnKey: "stale-empty", TurnEntrySeq: user.Sequence, TurnEntryDigest: user.Digest, GoalRevision: 1})
			require.NoError(t, err)
			body, err = json.Marshal(ai.AssistantMessage{StopReason: ai.StopStop})
			require.NoError(t, err)
			final, err := f.db.AppendEmbeddedEntry(ctx, owner.binding, "assistant", body)
			require.NoError(t, err)
			lane := f.m.laneFor(owner.binding.AgentID)
			lane.serial.Lock()
			defer lane.serial.Unlock()
			p := &embeddedPersistence{turnSeq: user.Sequence, turnDigest: user.Digest, seq: final.Sequence, digest: final.Digest, claudeRestartGeneration: lane.claudeRestartGeneration.Load()}
			_, err = f.raw.Exec(`UPDATE managed_agents SET selected_model_provider=?, selected_model_id=? WHERE agent_id=?`, provider, "sonnet", owner.binding.AgentID)
			require.NoError(t, err)
			_, err = f.m.ReserveRestart(ctx, RestartRequest{AgentID: owner.binding.AgentID, Method: "POST", CanonicalPath: "/restart", RequestID: "restart-stale-empty", CanonicalBodyHash: "hash"})
			require.NoError(t, err)
			lane.consecutiveEmptyTurns.Store(1)
			lane.goalAttempt.Store(4)
			require.NoError(t, f.m.evaluateEmbeddedTurn(ctx, owner.binding, p, &receipt, &ai.Model{Provider: provider, ID: "sonnet"}, nil))
			require.EqualValues(t, 1, lane.consecutiveEmptyTurns.Load())
			require.EqualValues(t, 4, lane.goalAttempt.Load())
		})
	}
}
