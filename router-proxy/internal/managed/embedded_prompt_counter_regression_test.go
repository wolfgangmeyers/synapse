package managed

import (
	"context"
	"testing"
	"time"
)

// A prompt resets pacing before waiting for admission, as in cbd9352. Later
// continuation work may increment the counters again; reset is not a fence.
func TestPromptResetsCountersBeforeAdmissionWait(t *testing.T) {
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	f.m.startWG.Wait()
	lane := f.m.laneFor(owner.binding.AgentID)
	lane.consecutiveContinuations.Store(7)
	lane.consecutiveEmptyTurns.Store(2)
	lane.goalAttempt.Store(5)

	lane.admission.Lock()
	done := make(chan error, 1)
	go func() {
		done <- f.m.AdmitPromptWithRequestID(context.Background(), owner.binding.AgentID, "test", "counter-reset-test")
	}()
	deadline := time.Now().Add(5 * time.Second)
	for lane.consecutiveContinuations.Load() != 0 || lane.consecutiveEmptyTurns.Load() != 0 || lane.goalAttempt.Load() != 0 {
		if time.Now().After(deadline) {
			lane.admission.Unlock()
			<-done
			t.Fatal("counters were not reset while admission was blocked")
		}
		time.Sleep(time.Millisecond)
	}
	// These writes deliberately overlap the in-flight admission and exercise the
	// same atomic operations used by continuation and empty-turn work.
	for i := 0; i < 1000; i++ {
		lane.consecutiveContinuations.Add(1)
		lane.consecutiveEmptyTurns.Add(1)
		incrementGoalAttempt(&lane.goalAttempt)
	}
	lane.resetPending = true
	lane.admission.Unlock()
	<-done
	if got := lane.consecutiveContinuations.Load(); got != 1000 {
		t.Fatalf("later continuation increments = %d, want 1000", got)
	}
}
