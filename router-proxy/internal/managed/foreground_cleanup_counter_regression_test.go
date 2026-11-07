package managed

import (
	"context"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestForegroundCleanupResetsCounterWithoutRestart(t *testing.T) {
	f := newFailureFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	f.reg.SetResponses([]providers.FauxResponseStep{
		func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("bash", map[string]any{"command": "true"}, "call")}, ai.StopToolUse)
		},
		func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
			close(entered)
			<-release
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
		},
	})
	owner := f.launch(t, "")
	f.m.startWG.Wait()
	saved, err := f.db.ManagedAgent(context.Background(), owner.binding.AgentID)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		ctx := context.WithValue(context.Background(), embeddedOwnerContextKey{}, owner)
		done <- f.m.runEmbeddedSessionTurn(ctx, *saved, "work", nil)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider barrier")
	}
	lane := f.m.laneFor(owner.binding.AgentID)
	lane.selection.Lock()
	lane.consecutiveContinuations.Store(7)
	lane.selection.Unlock()
	close(release)
	require.NoError(t, <-done)
	require.Zero(t, lane.consecutiveContinuations.Load())
}
