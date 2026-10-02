package managed

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

// TestCancelOperationFlipsInFlightToCancelled verifies that calling
// CancelOperation on an active operation:
//   - cancels the operation's context (triggering the watch goroutine's
//     ctx.Done branch),
//   - causes finish() to publish a "cancelled" status event,
//   - marks the operation settled (a.current cleared to nil),
//   - proves run.Cancel() was invoked (context propagated to the helper).
//
// A second CancelOperation call on the now-settled operation must return an
// error, confirming idempotent safety.
func TestCancelOperationFlipsInFlightToCancelled(t *testing.T) {
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, durable.Close()) })

	run := newFakeProviderAuthRun()
	coordinator, err := NewProviderAuthCoordinator(durable, nil, fakeProviderAuthHelper{run: run})
	require.NoError(t, err)
	t.Cleanup(coordinator.Close)

	// Collect published events asynchronously.
	var mu sync.Mutex
	var published []ProviderAuthEvent
	coordinator.SetPublisher(func(ev ProviderAuthEvent) {
		mu.Lock()
		published = append(published, ev)
		mu.Unlock()
	})

	const opID = "cancel-test-op-1"
	require.NoError(t, coordinator.Begin(context.Background(), opID, "openai-codex", "login"))

	// Confirm the operation is live before we cancel it.
	coordinator.mu.Lock()
	require.NotNil(t, coordinator.current, "operation must be current before cancel")
	require.Equal(t, opID, coordinator.current.id)
	coordinator.mu.Unlock()

	// Cancel the in-flight operation.
	require.NoError(t, coordinator.CancelOperation(opID))

	// The watch goroutine's ctx.Done branch and finish() must clear a.current.
	require.Eventually(t, func() bool {
		coordinator.mu.Lock()
		defer coordinator.mu.Unlock()
		return coordinator.current == nil
	}, 2*time.Second, 10*time.Millisecond, "a.current must be nil after cancel settles")

	// A second cancel on the now-settled operation must return an error.
	require.Error(t, coordinator.CancelOperation(opID), "second cancel on settled op must fail")

	// At least one published event must carry status "cancelled".
	mu.Lock()
	defer mu.Unlock()
	var foundCancelled bool
	for _, ev := range published {
		if ev.OperationID == opID && ev.Status == "cancelled" {
			foundCancelled = true
			break
		}
	}
	require.True(t, foundCancelled, "expected a 'cancelled' status event, got: %+v", published)

	// run.Cancel() must have been called by finish() — fakeProviderAuthRun
	// closes run.done exactly once (sync.Once), so a non-blocking receive
	// here confirms the helper was cancelled.
	select {
	case <-run.done:
		// good: helper cancel propagated
	default:
		t.Fatal("run.Cancel() was not called; context did not propagate to helper")
	}
}
