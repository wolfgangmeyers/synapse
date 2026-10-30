package managed

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSubagentReadBudgetInitialCapacity(t *testing.T) {
	lane := &managedAgentLane{}
	now := time.Unix(1000, 0)

	rem, whole := lane.subagentReadPoolRemaining(now)
	require.Equal(t, float64(SubagentReadPoolCapacity), rem)
	require.Equal(t, SubagentReadPoolCapacity, whole)
}

func TestSubagentReadBudgetConsumptionAndExhaustion(t *testing.T) {
	lane := &managedAgentLane{}
	now := time.Unix(1000, 0)

	// Consume 3 available credits
	for i := 0; i < SubagentReadPoolCapacity; i++ {
		err := lane.consumeSubagentReadCredit(now)
		require.NoError(t, err)
	}

	rem, whole := lane.subagentReadPoolRemaining(now)
	require.Equal(t, 0.0, rem)
	require.Equal(t, 0, whole)

	// 4th consumption should fail with exhaustion error
	err := lane.consumeSubagentReadCredit(now)
	require.Error(t, err)
	require.Equal(t, SubagentReadExhaustionError, err.Error())
}

func TestSubagentReadBudgetReplenishment(t *testing.T) {
	lane := &managedAgentLane{}
	t0 := time.Unix(1000, 0)

	// Exhaust all 3 credits at t0
	for i := 0; i < SubagentReadPoolCapacity; i++ {
		require.NoError(t, lane.consumeSubagentReadCredit(t0))
	}
	require.Error(t, lane.consumeSubagentReadCredit(t0))

	// Advance time by 30 seconds (half a credit)
	t30 := t0.Add(30 * time.Second)
	rem, whole := lane.subagentReadPoolRemaining(t30)
	require.InDelta(t, 0.5, rem, 0.001)
	require.Equal(t, 0, whole)
	// Still insufficient for 1 full credit
	require.Error(t, lane.consumeSubagentReadCredit(t30))

	// Advance time by another 30 seconds (total 60s -> 1 credit)
	t60 := t0.Add(60 * time.Second)
	rem, whole = lane.subagentReadPoolRemaining(t60)
	require.InDelta(t, 1.0, rem, 0.001)
	require.Equal(t, 1, whole)

	// Now 1 consumption succeeds
	require.NoError(t, lane.consumeSubagentReadCredit(t60))
	// And fails again immediately
	require.Error(t, lane.consumeSubagentReadCredit(t60))

	// Advance time by 10 minutes (600s) -> should be capped at SubagentReadPoolCapacity (3)
	tLong := t60.Add(10 * time.Minute)
	rem, whole = lane.subagentReadPoolRemaining(tLong)
	require.Equal(t, float64(SubagentReadPoolCapacity), rem)
	require.Equal(t, SubagentReadPoolCapacity, whole)
}

func TestSubagentReadBudgetNegativeClockJump(t *testing.T) {
	lane := &managedAgentLane{}
	now := time.Unix(1000, 0)

	// Consume 1 credit so balance is 2 credits
	require.NoError(t, lane.consumeSubagentReadCredit(now))
	rem, whole := lane.subagentReadPoolRemaining(now)
	require.Equal(t, float64(SubagentReadPoolCapacity-1), rem)
	require.Equal(t, SubagentReadPoolCapacity-1, whole)

	// Jump time backward by 10 minutes
	past := now.Add(-10 * time.Minute)
	remPast, wholePast := lane.subagentReadPoolRemaining(past)
	// Advancing time backward must not decrease credits or corrupt pool balance
	require.Equal(t, float64(SubagentReadPoolCapacity-1), remPast)
	require.Equal(t, SubagentReadPoolCapacity-1, wholePast)

	// Consuming with backward time should still consume from the remaining credits normally without corruption
	require.NoError(t, lane.consumeSubagentReadCredit(past))
	remAfter, wholeAfter := lane.subagentReadPoolRemaining(past)
	require.Equal(t, float64(SubagentReadPoolCapacity-2), remAfter)
	require.Equal(t, SubagentReadPoolCapacity-2, wholeAfter)
}

func TestSubagentReadBudgetNilLane(t *testing.T) {
	var lane *managedAgentLane
	now := time.Unix(1000, 0)

	require.NoError(t, lane.consumeSubagentReadCredit(now))
	rem, whole := lane.subagentReadPoolRemaining(now)
	require.Equal(t, 0.0, rem)
	require.Equal(t, 0, whole)
}
