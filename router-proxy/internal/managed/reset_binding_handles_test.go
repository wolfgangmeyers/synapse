package managed

import (
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestResetBindingCaptureAndCompareClear(t *testing.T) {
	old := store.EmbeddedBinding{AgentID: "a", SessionID: "s", Epoch: 1, Generation: "generation-1"}
	next := old
	next.Generation = "generation-2"
	lane := &managedAgentLane{}
	runtime := &resetBarrierRuntime{}
	require.True(t, lane.installEmbeddedRuntime(runtime, &embeddedPersistence{binding: next}))
	captured, ok := lane.beginEmbeddedReset(old)
	require.True(t, ok)
	require.Nil(t, captured)
	lane.endEmbeddedReset()
	captured, ok = lane.beginEmbeddedReset(next)
	require.True(t, ok)
	require.Same(t, runtime, captured)
	lane.endEmbeddedReset()
	lane.removeEmbeddedRuntime(&resetBarrierRuntime{})
	require.Same(t, runtime, lane.activeSession)
	lane.removeEmbeddedRuntime(runtime)
	require.Nil(t, lane.activeSession)
	require.Equal(t, store.EmbeddedBinding{}, lane.activeBinding)
}

func TestClaudeStopBindingDoesNotTouchSuccessor(t *testing.T) {
	old := store.EmbeddedBinding{AgentID: "a", SessionID: "s", Epoch: 1, Generation: "generation-1"}
	next := old
	next.Epoch++
	entry := &claudeCodeProcessEntry{binding: next}
	pool := &claudeCodeProcessPool{entries: map[string]*claudeCodeProcessEntry{"a": entry}}
	require.NoError(t, pool.stopBinding(old))
	require.Same(t, entry, pool.entries["a"])
	require.Nil(t, entry.stopping)
}

// The process selected before the pool lock is reacquired must not close a
// successor installed in that interval.
func TestClaudeStopSnapshotReplacementBarrier(t *testing.T) {
	old := &claudeCodeProcessEntry{binding: store.EmbeddedBinding{AgentID: "a", Generation: "old"}}
	successor := &claudeCodeProcessEntry{binding: store.EmbeddedBinding{AgentID: "a", Generation: "new"}}
	pool := &claudeCodeProcessPool{entries: map[string]*claudeCodeProcessEntry{"a": old}}
	selected := make(chan *claudeCodeProcessEntry, 1)
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		pool.mu.Lock()
		entry := pool.entries["a"]
		pool.mu.Unlock()
		selected <- entry
		<-release
		done <- pool.stopEntry("a", entry)
	}()
	require.Same(t, old, <-selected)
	pool.mu.Lock()
	pool.entries["a"] = successor
	pool.mu.Unlock()
	close(release)
	require.NoError(t, <-done)
	require.Same(t, successor, pool.entries["a"])
	require.Nil(t, successor.stopping)
}

func TestClaudeStopBindingConcurrentJoin(t *testing.T) {
	stdin := &cleanupBarrierStdin{make(chan struct{}), make(chan struct{})}
	b := store.EmbeddedBinding{AgentID: "a", Generation: "old"}
	entry := &claudeCodeProcessEntry{binding: b, process: cleanupTestProcess(stdin)}
	pool := &claudeCodeProcessPool{entries: map[string]*claudeCodeProcessEntry{"a": entry}}
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { first <- pool.stopBinding(b) }()
	<-stdin.entered
	go func() { second <- pool.stopBinding(b) }()
	select {
	case err := <-second:
		t.Fatalf("second stop returned before close: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(stdin.release)
	require.NoError(t, <-first)
	require.NoError(t, <-second)
}

func TestResetCompareAndClearPreservesReplacement(t *testing.T) {
	oldBinding := store.EmbeddedBinding{AgentID: "a", SessionID: "old", Epoch: 1, Generation: "g"}
	newBinding := oldBinding
	newBinding.SessionID = "new"
	newBinding.Epoch++
	lane := &managedAgentLane{}
	old := &resetBarrierRuntime{}
	successor := &resetBarrierRuntime{}
	require.True(t, lane.installEmbeddedRuntime(old, &embeddedPersistence{binding: oldBinding}))
	captured, ok := lane.beginEmbeddedReset(oldBinding)
	require.True(t, ok)
	require.Same(t, old, captured)
	// Simulate the handoff reopening admissions before the old caller unwinds.
	lane.reopenEmbeddedResetAdmissionsForBinding(oldBinding)
	require.True(t, lane.installEmbeddedRuntime(successor, &embeddedPersistence{binding: newBinding}))
	lane.removeEmbeddedRuntime(captured)
	require.Same(t, successor, lane.activeSession)
	require.Equal(t, newBinding, lane.activeBinding)
	lane.endEmbeddedReset()
}
