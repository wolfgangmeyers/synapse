package managed

import (
	"context"
	"testing"
	"time"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestClaudeLifecycleRejectsSteerAndMultipleInputs(t *testing.T) {
	r, _, _, _ := claudeRuntimeFixture(t, `{}`)
	defer r.Close()
	r.mu.Lock()
	r.active = true
	r.mu.Unlock()
	require.False(t, r.TrySteer(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "later"}}}, nil))
	r.mu.Lock()
	r.active = false
	r.mu.Unlock()
	_, err := r.RunMessages(context.Background(), []agent.AgentMessage{ai.UserMessage{}, ai.UserMessage{}})
	require.ErrorContains(t, err, "exactly one input")
}

func TestClaudeStopBudgetFencesBlockedClose(t *testing.T) {
	s := &cleanupBarrierStdin{make(chan struct{}), make(chan struct{})}
	p := cleanupTestProcess(s)
	fenced := make(chan struct{})
	p.onUnconfirmedStop = func() { close(fenced) }
	result := make(chan error, 1)
	start := time.Now()
	go func() { result <- p.Close() }()
	<-s.entered
	select {
	case err := <-result:
		require.ErrorContains(t, err, "unconfirmed")
		require.Less(t, time.Since(start), 6*time.Second)
	case <-time.After(6 * time.Second):
		t.Fatal("stop exceeded its total budget")
	}
	select {
	case <-fenced:
	default:
		t.Fatal("unconfirmed stop did not fence")
	}
	close(s.release)
}
