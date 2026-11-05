package managed

import (
	"context"
	"testing"

	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// The fake emits two distinct assistant messages after the checked terminating
// result. Cleanup/interrupt is held until the durable snapshot is inspected.
func TestClaudeD11DurableOutputBeforeCleanup(t *testing.T) {
	r, p, _, _ := claudeRuntimeFixture(t, `{"turns":[[{"tool":{"id":"end","name":"mecha_worker_terminate","input":{},"result":"Worker self-termination scheduled"}},{"text":"FORBIDDEN one"},{"text":"FORBIDDEN two"}]]}`)
	checked := make(chan struct{})
	releaseCleanup := make(chan struct{})
	cleanupDone := make(chan struct{})
	p.goalResult = func(_ context.Context, result ai.ToolResultMessage, committed bool) error {
		if committed && result.ToolCallID == "end" {
			r.mu.Lock()
			r.discardPartial = true
			r.mu.Unlock()
			close(checked)
			go func() { <-releaseCleanup; close(cleanupDone) }()
		}
		return nil
	}
	done := make(chan error, 1)
	go func() { _, err := r.Run(context.Background(), "terminate"); done <- err }()
	<-checked
	err := <-done
	snapshot, snapshotErr := p.db.ReopenEmbedded(context.Background(), p.binding)
	close(releaseCleanup)
	<-cleanupDone
	require.NoError(t, err)
	require.NoError(t, snapshotErr)
	resultSeen := false
	for _, entry := range snapshot.Entries {
		msg, err := ai.UnmarshalMessage(entry.Body)
		require.NoError(t, err)
		if result, ok := msg.(ai.ToolResultMessage); ok && result.ToolCallID == "end" {
			resultSeen = true
			continue
		}
		if resultSeen {
			require.NotEqual(t, "assistant", entry.Kind, "assistant persisted after checked terminating result: %s", entry.Body)
		}
	}
	require.True(t, resultSeen, "terminating result must be durable")
}
