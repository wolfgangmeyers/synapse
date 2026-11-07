package managed

import (
	"context"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestReview2cBlockIdentity(t *testing.T) {
	a := &store.EmbeddedMailAttempt{MessageID: "m", MessageIncarnationID: "one", Binding: store.EmbeddedBinding{AgentID: "a", OwnerToken: 1}}
	require.True(t, sameClaudeBlockedAttempt(a, a))
	b := *a
	b.MessageIncarnationID = "two"
	require.False(t, sameClaudeBlockedAttempt(a, &b))
	b = *a
	b.Binding.OwnerToken = 2
	require.False(t, sameClaudeBlockedAttempt(a, &b))
}

func TestReview2cEarliestRetryDeadline(t *testing.T) {
	f := newFailureFixture(t)
	lane := f.m.laneFor("review-timer")
	lane.serial.Lock()
	f.m.scheduleClaudeMailRetry("review-timer", time.Hour)
	first := lane.claudeRetryDeadline
	f.m.scheduleClaudeMailRetry("review-timer", 20*time.Millisecond)
	second := lane.claudeRetryDeadline
	f.m.scheduleClaudeMailRetry("review-timer", time.Hour)
	require.True(t, second.Before(first))
	require.Equal(t, second, lane.claudeRetryDeadline)
	lane.serial.Unlock()
}

func TestReview2cUnclassifiedVisible(t *testing.T) {
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	waitGateStarts(t, f.m)
	_, err := f.raw.Exec(`UPDATE managed_agents SET selected_model_provider='claude-code', selected_model_id='sonnet' WHERE agent_id=?`, owner.binding.AgentID)
	require.NoError(t, err)
	token, err := f.db.EnterEmbeddedSDKExecution(context.Background(), owner.binding, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.db.ClearEmbeddedExecution(context.Background(), owner.binding, token) })
	f.enqueue(t, "e9999999-7777-4777-8777-777777777777")
	f.m.dispatchEmbeddedMail(owner.binding.AgentID)
	history, err := f.db.GetManagedCompleteHistory(context.Background(), owner.binding.AgentID)
	require.NoError(t, err)
	found := false
	for _, e := range history {
		if e.MessageContent != nil && *e.MessageContent == claudeUnclassifiedDiagnostic {
			found = true
		}
	}
	require.True(t, found)
}
