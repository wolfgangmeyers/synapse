package managed

import (
	"context"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

// This seeds a completed Claude turn; it is not a full Claude provider turn.
func TestClaudeRestartFencesEvaluatorAtCommitGap(t *testing.T) {
	f := newFailureFixture(t)
	reached, release := make(chan struct{}), make(chan struct{})
	keys := make(chan store.EmbeddedGoalEvaluationKey, 1)
	f.m.embeddedGoalEvaluator = func(_ context.Context, k store.EmbeddedGoalEvaluationKey) (string, string, error) {
		keys <- k
		return store.EmbeddedGoalEvaluationDone, "stale", nil
	}
	f.m.embeddedBeforeGoalCommit = func() { close(reached); <-release }
	owner := f.launch(t, "")
	f.m.startWG.Wait()
	_, err := f.db.UpdateManagedGoalConfig(context.Background(), owner.binding.AgentID, store.ManagedGoalConfig{Enabled: true, Goal: "original", Settings: store.DefaultManagedGoalSettings()})
	require.NoError(t, err)
	evaluateClaudeFixture(t, f, owner)
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("commit barrier not reached")
	}
	key := <-keys
	require.NotZero(t, key.UserSeq)
	require.NotEmpty(t, key.UserDigest)
	require.NotZero(t, key.GoalRevision)
	require.Greater(t, key.FinalSeq, key.UserSeq)
	require.NotEmpty(t, key.FinalDigest)
	lane := f.m.laneFor(owner.binding.AgentID)
	lane.goalAttempt.Store(7)
	lane.consecutiveContinuations.Store(3)
	_, err = f.raw.Exec(`UPDATE managed_agents SET selected_model_provider=?, selected_model_id=? WHERE agent_id=?`, claudeCodeProvider, "sonnet", owner.binding.AgentID)
	require.NoError(t, err)
	_, err = f.m.ReserveRestart(context.Background(), RestartRequest{AgentID: owner.binding.AgentID, Method: "POST", CanonicalPath: "/restart", RequestID: "restart-commit-gap", CanonicalBodyHash: "hash"})
	require.NoError(t, err)
	close(release)
	done := make(chan struct{})
	go func() { f.m.startWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock after restart")
	}
	row, err := f.db.EmbeddedEvaluationRow(context.Background(), owner.binding, key)
	require.NoError(t, err)
	require.Equal(t, store.EmbeddedGoalEvaluationUnknown, row.State)
	require.EqualValues(t, 7, lane.goalAttempt.Load())
	require.EqualValues(t, 3, lane.consecutiveContinuations.Load())
}
