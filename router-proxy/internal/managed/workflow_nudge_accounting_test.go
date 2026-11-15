package managed

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkflowNudgeAccountingConcurrentAndStale(t *testing.T) {
	var lane managedAgentLane
	lane.consecutiveContinuations.Store(19)
	first := lane.beginWorkflowNudge()
	require.True(t, lane.dispatchWorkflowNudge(first, 20))
	require.EqualValues(t, 1, lane.workflowUnanswered.Load())
	require.True(t, lane.respondWorkflowNudge(first))
	require.Zero(t, lane.consecutiveContinuations.Load())
	require.Zero(t, lane.workflowUnanswered.Load())
	second := lane.beginWorkflowNudge()
	lane.consecutiveContinuations.Store(7)
	require.True(t, lane.dispatchWorkflowNudge(second, 20))
	require.False(t, lane.respondWorkflowNudge(first), "late output cannot reset a newer attempt")
	require.EqualValues(t, 7, lane.consecutiveContinuations.Load())
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); lane.respondWorkflowNudge(second) }()
	}
	wg.Wait()
	require.Zero(t, lane.consecutiveContinuations.Load())
	require.Zero(t, lane.workflowUnanswered.Load())
	require.False(t, lane.respondWorkflowNudge(second), "duplicate response must be inert")
}

func TestWorkflowNudgeAccountingCapAndReset(t *testing.T) {
	var lane managedAgentLane
	for i := 0; i < 20; i++ {
		id := lane.beginWorkflowNudge()
		require.True(t, lane.dispatchWorkflowNudge(id, 20))
	}
	id := lane.beginWorkflowNudge()
	require.False(t, lane.dispatchWorkflowNudge(id, 20))
	require.EqualValues(t, 20, lane.workflowUnanswered.Load())
	lane.resetWorkflowNudgeAccounting()
	require.False(t, lane.respondWorkflowNudge(id))
	require.Zero(t, lane.workflowUnanswered.Load())
}

func TestWorkflowNudgeCapReconsideredOnResponse(t *testing.T) {
	var lane managedAgentLane
	id := lane.beginWorkflowNudge()
	require.True(t, lane.dispatchWorkflowNudge(id, 20))
	lane.consecutiveContinuations.Store(20)
	woke := make(chan struct{}, 1)
	require.True(t, lane.reconsiderWorkflowCap(20, func() { woke <- struct{}{} }))
	require.True(t, lane.respondWorkflowNudge(id))
	select {
	case <-woke:
	default:
		t.Fatal("suppressed wake was not reconsidered")
	}
	require.False(t, lane.respondWorkflowNudge(id))
}

func TestWorkflowCapReleasedBeforeRegistrationMustRetry(t *testing.T) {
	var lane managedAgentLane
	id := lane.beginWorkflowNudge()
	require.True(t, lane.dispatchWorkflowNudge(id, 20))
	lane.consecutiveContinuations.Store(20)
	require.True(t, lane.respondWorkflowNudge(id))
	require.False(t, lane.reconsiderWorkflowCap(20, func() {}), "caller must retry when cap was cleared before registration")
}
