package managed

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCapacityReviewResumeCannotReplaceActiveAttempt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := &subagentTask{childID: "same", attempt: 1}
	next := &subagentTask{childID: "same", attempt: 2, status: "queued"}
	m := &Manager{runCtx: ctx, subagentsActive: map[string]*subagentTask{"same": old}, subagentsQueue: []*subagentTask{next}}
	m.drainQueueLocked()
	require.Same(t, old, m.subagentsActive["same"])
	require.Len(t, m.subagentsQueue, 1)
}
