package managed

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBackgroundBashInlineDisabledReproduction(t *testing.T) {
	lane := &managedAgentLane{}
	now := time.Now()
	for i := 0; i < InlineToolPoolCapacity; i++ {
		require.True(t, lane.consumeInlineAllowance("bash", now), "initial pool credit %d", i+1)
	}
	require.False(t, lane.consumeInlineAllowance("bash", now), "foreground bash must be denied once the pool is exhausted")
}
