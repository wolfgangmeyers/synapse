package managed

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBackgroundBashReadBudgetLaneIsolationConcurrencyAndRefill(t *testing.T) {
	var lane managedAgentLane
	now := time.Unix(1000, 0)
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if lane.consumeBackgroundBashReadCredit(now) == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := successes.Load(); got != 3 {
		t.Fatalf("got %d successes, want 3", got)
	}
	if err := lane.consumeSubagentReadCredit(now); err != nil {
		t.Fatalf("background reads consumed subagent budget: %v", err)
	}
	if lane.consumeBackgroundBashReadCredit(now.Add(59*time.Second)) == nil {
		t.Fatal("credit refilled too early")
	}
	if err := lane.consumeBackgroundBashReadCredit(now.Add(time.Minute)); err != nil {
		t.Fatalf("credit did not refill: %v", err)
	}
	if lane.consumeBackgroundBashReadCredit(now.Add(time.Minute)) == nil {
		t.Fatal("extra credit")
	}
	var other managedAgentLane
	if err := other.consumeBackgroundBashReadCredit(now); err != nil {
		t.Fatalf("other lane affected: %v", err)
	}
}
