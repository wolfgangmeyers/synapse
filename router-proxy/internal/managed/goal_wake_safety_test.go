package managed

import (
	"context"
	"testing"
	"time"

	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// SCN-GWS-002/003: exercise real idle admission, SDK failure and SQLite
// policy with the existing deterministic clock; only the provider is replaced.
func TestGoalWakeSafetyFailedIdleAttemptsArePaced(t *testing.T) {
	f := newFailureFixture(t)
	fail := func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
		f.calls.Add(1)
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "deterministic backend failure"}}, ai.StopError)
	}
	steps := []providers.FauxResponseStep{f.response("startup settled")}
	for range 64 {
		steps = append(steps, fail)
	}
	f.reg.SetResponses(steps)
	owner := f.launch(t, "start")
	_, err := f.m.directEmbeddedBrowserGoal(context.Background(), owner.binding.AgentID, "goal.control", map[string]any{"action": "set", "goal": "keep working despite backend outage"}, nil)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return f.calls.Load() >= 2 }, 3*time.Second, time.Millisecond, "first automatic wake must reach failure fixture")
	f.laneBarrier(t, owner.binding.AgentID)
	require.EqualValues(t, 2, f.calls.Load(), "one initial turn and one immediate automatic attempt")
	for range 5 {
		require.NoError(t, f.m.ResumeIdleEmbeddedGoals(context.Background()))
		f.m.MaybeResumeEmbeddedGoal(owner.binding.AgentID)
	}
	// A marker admitted through the same lane joins already queued idle work.
	f.laneBarrier(t, owner.binding.AgentID)
	require.Never(t, func() bool { return f.calls.Load() > 2 }, 150*time.Millisecond, time.Millisecond,
		"failed automatic attempt must not retry while the deterministic clock remains at its settlement time")
	require.NoError(t, f.clock.Advance(9999*time.Millisecond))
	require.NoError(t, f.m.ResumeIdleEmbeddedGoals(context.Background()))
	f.laneBarrier(t, owner.binding.AgentID)
	require.EqualValues(t, 2, f.calls.Load(), "no retry at T+9999ms")
	require.NoError(t, f.clock.Advance(time.Millisecond))
	require.NoError(t, f.m.ResumeIdleEmbeddedGoals(context.Background()))
	require.Eventually(t, func() bool { return f.calls.Load() == 3 }, 3*time.Second, time.Millisecond, "one retry eligible at T+10000ms")
	for attempt := 3; attempt <= 20; attempt++ {
		f.laneBarrier(t, owner.binding.AgentID)
		require.NoError(t, f.clock.Advance(10*time.Second))
		require.NoError(t, f.m.ResumeIdleEmbeddedGoals(context.Background()))
		require.Eventually(t, func() bool { return f.calls.Load() == int32(attempt+1) }, 3*time.Second, time.Millisecond)
	}
	f.laneBarrier(t, owner.binding.AgentID)
	for range 3 {
		require.NoError(t, f.clock.Advance(10*time.Second))
		require.NoError(t, f.m.ResumeIdleEmbeddedGoals(context.Background()))
	}
	require.Never(t, func() bool { return f.calls.Load() > 21 }, 150*time.Millisecond, time.Millisecond, "twenty automatic attempts includes the first, not twenty retries")
	policy, err := f.db.ManagedGoalPolicyFor(context.Background(), owner.binding.AgentID)
	require.NoError(t, err)
	require.True(t, policy.Config.Enabled, "failure cap must not clear the goal")
	require.NotEqual(t, "done", policy.EvaluatorState)
}

// SCN-GWS-004/006: a failed startup must not leave its predecessor cached.
// Corrupt SDK bytes model an unreadable persisted session, not a provider error.
func TestGoalWakeSafetyFailedRecoveryEvictsOriginOwner(t *testing.T) {
	ctx := context.Background()
	f := newFailureFixture(t)
	f.reg.SetResponses([]providers.FauxResponseStep{f.response("startup settled"), f.response("must not dispatch")})
	origin := f.launch(t, "start")
	saved, err := f.db.ManagedAgent(ctx, origin.binding.AgentID)
	require.NoError(t, err)
	_, err = f.raw.Exec(`UPDATE embedded_entries SET body=X'FF' WHERE agent_id=? AND kind='assistant'`, origin.binding.AgentID)
	require.NoError(t, err)
	recovery := productionPolicyManager(t, f.db, f.root, f.reg)
	t.Cleanup(func() { require.NoError(t, recovery.Close(ctx)) })
	recovery.mu.Lock()
	recovery.embeddedOwners = map[string]embeddedOwner{origin.binding.AgentID: origin}
	recovery.mu.Unlock()
	var oldID string
	require.NoError(t, f.raw.QueryRow(`SELECT owner_id FROM embedded_sessions WHERE agent_id=? AND current=1`, origin.binding.AgentID).Scan(&oldID))
	recovery.startEmbeddedRoot(*saved, saved.LaunchOperationID, true)
	require.Eventually(t, func() bool {
		var id string
		err := f.raw.QueryRow(`SELECT owner_id FROM embedded_sessions WHERE agent_id=? AND current=1`, origin.binding.AgentID).Scan(&id)
		return err == nil && id != oldID
	}, 3*time.Second, time.Millisecond, "recovery must acquire successor B after cached owner A")
	lane := recovery.laneFor(origin.binding.AgentID)
	lane.serial.Lock()
	lane.serial.Unlock()
	recovery.mu.Lock()
	selected, exists := recovery.embeddedOwners[origin.binding.AgentID]
	recovery.mu.Unlock()
	require.False(t, exists && selected == origin, "failed recovery B must evict origin A, not just compare cache against B")
	require.EqualValues(t, 1, f.calls.Load(), "unreadable recovery must fail before provider dispatch")
}

// SCN-GWS-007: cancelling after a failed automatic turn overrides stale idle
// notifications even after the retry deadline has passed.
func TestGoalWakeSafetyGoalOffCancelsFailedRetry(t *testing.T) {
	f := newFailureFixture(t)
	fail := func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
		f.calls.Add(1)
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "backend unavailable"}}, ai.StopError)
	}
	f.reg.SetResponses([]providers.FauxResponseStep{f.response("startup settled"), fail, fail, fail})
	owner := f.launch(t, "start")
	ctx := context.Background()
	_, err := f.m.directEmbeddedBrowserGoal(ctx, owner.binding.AgentID, "goal.control", map[string]any{"action": "set", "goal": "continue until cancelled"}, nil)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return f.calls.Load() == 2 }, 3*time.Second, time.Millisecond)
	f.laneBarrier(t, owner.binding.AgentID)
	_, err = f.m.directEmbeddedBrowserGoal(ctx, owner.binding.AgentID, "goal.control", map[string]any{"action": "off"}, nil)
	require.NoError(t, err)
	for range 3 {
		require.NoError(t, f.clock.Advance(10*time.Second))
		require.NoError(t, f.m.ResumeIdleEmbeddedGoals(ctx))
		f.m.MaybeResumeEmbeddedGoal(owner.binding.AgentID)
		f.laneBarrier(t, owner.binding.AgentID)
	}
	require.Never(t, func() bool { return f.calls.Load() > 2 }, 150*time.Millisecond, time.Millisecond)
	policy, err := f.db.ManagedGoalPolicyFor(ctx, owner.binding.AgentID)
	require.NoError(t, err)
	require.False(t, policy.Config.Enabled)
}
