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
