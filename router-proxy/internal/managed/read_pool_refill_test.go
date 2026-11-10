package managed

import (
	"testing"
	"time"
)

func TestReadPoolsIndependentRefillAndBackwardClock(t *testing.T) {
	var lane managedAgentLane
	start := time.Unix(1000, 0)
	for _, consume := range []func(time.Time) error{lane.consumeSubagentReadCredit, lane.consumeBackgroundBashReadCredit} {
		for i := 0; i < SubagentReadPoolCapacity; i++ {
			if err := consume(start); err != nil {
				t.Fatal(err)
			}
		}
		if err := consume(start); err == nil {
			t.Fatal("pool should be exhausted")
		}
	}
	// A backward clock does not add credit, but becomes the new refill baseline.
	back := start.Add(-time.Minute)
	if lane.consumeSubagentReadCredit(back) == nil {
		t.Fatal("backward clock refilled subagent pool")
	}
	if lane.consumeSubagentReadCredit(start) != nil {
		t.Fatal("subagent pool did not refill from new baseline")
	}
	if lane.consumeBackgroundBashReadCredit(start) == nil {
		t.Fatal("subagent refill affected bash pool")
	}
	if lane.consumeBackgroundBashReadCredit(back) == nil {
		t.Fatal("backward clock refilled bash pool")
	}
	if lane.consumeBackgroundBashReadCredit(start) != nil {
		t.Fatal("bash pool did not refill from new baseline")
	}
	if lane.consumeSubagentReadCredit(start) == nil {
		t.Fatal("bash refill affected subagent pool")
	}
	for _, consume := range []func(time.Time) error{lane.consumeSubagentReadCredit, lane.consumeBackgroundBashReadCredit} {
		future := start.Add(10 * time.Minute)
		for i := 0; i < SubagentReadPoolCapacity; i++ {
			if err := consume(future); err != nil {
				t.Fatal(err)
			}
		}
		if consume(future) == nil {
			t.Fatal("refill exceeded capacity")
		}
	}
}
