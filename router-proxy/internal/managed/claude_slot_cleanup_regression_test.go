package managed

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClaudeSlotWaitsForToolCleanup(t *testing.T) {
	done := make(chan struct{})
	task := &subagentTask{childID: "child", attempt: 1, claude: true, resourcesDone: done}
	m := &Manager{subagentsActive: map[string]*subagentTask{"child": task}}
	m.releaseSubagentSlot(task)
	m.subagentsMu.Lock()
	require.Same(t, task, m.subagentsActive["child"], "slot must remain held until cleanup")
	m.subagentsMu.Unlock()
	close(done)
	require.Eventually(t, func() bool {
		m.subagentsMu.Lock()
		defer m.subagentsMu.Unlock()
		return m.subagentsActive["child"] == nil
	}, time.Second, time.Millisecond)
}

func TestClaudeOldCleanupCannotReleaseReplacementAttempt(t *testing.T) {
	done := make(chan struct{})
	old := &subagentTask{childID: "child", attempt: 1, claude: true, resourcesDone: done}
	replacement := &subagentTask{childID: "child", attempt: 2, claude: true}
	m := &Manager{subagentsActive: map[string]*subagentTask{"child": old}}
	m.releaseSubagentSlot(old)
	m.subagentsMu.Lock()
	m.subagentsActive["child"] = replacement
	m.subagentsMu.Unlock()
	close(done)
	// Recheck synchronously after the old resources finish; the identity guard
	// must reject this stale release regardless of the deferred waiter.
	m.releaseSubagentSlot(old)
	m.subagentsMu.Lock()
	defer m.subagentsMu.Unlock()
	require.Same(t, replacement, m.subagentsActive["child"])
}

func TestClaudeUnconfirmedStopRetainsSlot(t *testing.T) {
	done := make(chan struct{})
	close(done)
	task := &subagentTask{childID: "child", attempt: 1, claude: true, resourcesDone: done, retainSlot: true}
	m := &Manager{subagentsActive: map[string]*subagentTask{"child": task}}
	m.releaseSubagentSlot(task)
	require.Same(t, task, m.subagentsActive["child"])
}
