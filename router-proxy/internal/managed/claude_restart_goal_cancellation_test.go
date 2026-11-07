package managed

import (
	"context"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestClaudeRestartCancelsHeldGoalEvaluator(t *testing.T) {
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
	_, err := f.db.UpdateManagedGoalConfig(context.Background(), owner.binding.AgentID, store.ManagedGoalConfig{Enabled: true, Goal: "original", Settings: store.DefaultManagedGoalSettings()})
	require.NoError(t, err)
	evaluateClaudeFixture(t, f, owner)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("evaluator did not enter")
	}
	_, err = f.raw.Exec(`UPDATE managed_agents SET selected_model_provider=?, selected_model_id=? WHERE agent_id=?`, claudeCodeProvider, "sonnet", owner.binding.AgentID)
	require.NoError(t, err)
	_, err = f.m.ReserveRestart(context.Background(), RestartRequest{AgentID: owner.binding.AgentID, Method: "POST", CanonicalPath: "/restart", RequestID: "restart-evaluator", CanonicalBodyHash: "hash"})
	require.NoError(t, err)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("successful restart did not cancel evaluator")
	}
}

func TestClaudeRestartFailedAndReplayDoNotCancelNewFlight(t *testing.T) {
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	f.m.startWG.Wait()
	_, err := f.raw.Exec(`UPDATE managed_agents SET selected_model_provider=?, selected_model_id=? WHERE agent_id=?`, claudeCodeProvider, "sonnet", owner.binding.AgentID)
	require.NoError(t, err)
	req := RestartRequest{AgentID: owner.binding.AgentID, Method: "POST", CanonicalPath: "/restart", RequestID: "restart-replay-flight", CanonicalBodyHash: "hash"}
	flight, release := f.m.registerGoalEvaluation(owner.binding.AgentID, 1)
	defer release()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = f.m.ReserveRestart(canceled, req)
	require.Error(t, err)
	require.NoError(t, flight.Err())
	_, err = f.m.ReserveRestart(context.Background(), req)
	require.NoError(t, err)
	require.ErrorIs(t, flight.Err(), context.Canceled)
	f.m.startWG.Wait()
	next, releaseNext := f.m.registerGoalEvaluation(owner.binding.AgentID, 1)
	defer releaseNext()
	result, err := f.m.ReserveRestart(context.Background(), req)
	require.NoError(t, err)
	require.True(t, result.Replayed)
	require.NoError(t, next.Err())
}

func TestClaudeRestartFencesQueuedGoalEvaluator(t *testing.T) {
	f := newFailureFixture(t)
	called := make(chan struct{}, 1)
	f.m.embeddedGoalEvaluator = func(context.Context, store.EmbeddedGoalEvaluationKey) (string, string, error) {
		called <- struct{}{}
		return store.EmbeddedGoalEvaluationDone, "done", nil
	}
	owner := f.launch(t, "")
	f.m.startWG.Wait()
	_, err := f.db.UpdateManagedGoalConfig(context.Background(), owner.binding.AgentID, store.ManagedGoalConfig{Enabled: true, Goal: "original", Settings: store.DefaultManagedGoalSettings()})
	require.NoError(t, err)
	lane := f.m.laneFor(owner.binding.AgentID)
	lane.serial.Lock()
	evaluateClaudeFixture(t, f, owner)
	_, err = f.raw.Exec(`UPDATE managed_agents SET selected_model_provider=?, selected_model_id=? WHERE agent_id=?`, claudeCodeProvider, "sonnet", owner.binding.AgentID)
	require.NoError(t, err)
	_, err = f.m.ReserveRestart(context.Background(), RestartRequest{AgentID: owner.binding.AgentID, Method: "POST", CanonicalPath: "/restart", RequestID: "restart-queued", CanonicalBodyHash: "hash"})
	lane.serial.Unlock()
	require.NoError(t, err)
	f.m.startWG.Wait()
	select {
	case <-called:
		t.Fatal("pre-restart evaluator ran after successful reservation")
	default:
	}
}
