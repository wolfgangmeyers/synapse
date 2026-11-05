package managed

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/stretchr/testify/require"
)

// Write a real prefix into the mock CLI pipe, then report an uncertain failure.
type uncertainClaudePipe struct{ io.WriteCloser }

func (w uncertainClaudePipe) Write(b []byte) (int, error) {
	n, err := w.WriteCloser.Write(b[:len(b)/2])
	if err != nil {
		return n, err
	}
	return n, errors.New("injected partial stdin write")
}

func TestClaudeRuntimePartialMailWriteHoldsExactDelivery(t *testing.T) {
	ctx := context.Background()
	r, p, pool, cfg := claudeRuntimeFixture(t, `{}`)
	require.NoError(t, p.db.ReconcileManagedRecipientSources(ctx))
	_, err := p.db.Enqueue(ctx, protocol.Envelope{ID: "uncertain-runtime", From: "sender", To: p.binding.AgentID, Body: "never replay", CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	require.NoError(t, err)
	_, err = p.db.ResolveMessageTarget(ctx, "uncertain-runtime")
	require.NoError(t, err)
	attempt, err := p.db.ReserveEmbeddedMail(ctx, p.binding, "uncertain-runtime")
	require.NoError(t, err)
	p.mail = attempt
	proc, err := pool.acquire(ctx, p.binding, cfg, nil)
	require.NoError(t, err)
	proc.stdin = uncertainClaudePipe{proc.stdin}
	user, err := store.EmbeddedMailUserMessage(attempt, time.Now().UnixMilli())
	require.NoError(t, err)
	_, err = r.RunMessages(ctx, []agent.AgentMessage{user})
	require.ErrorContains(t, err, "injected partial stdin write")
	held, err := p.db.ClaudeMailHeld(ctx, attempt.MessageID)
	require.NoError(t, err)
	require.True(t, held)
	require.NoError(t, p.db.ReleaseEmbeddedUnconsumedAttempt(ctx, p.binding, attempt.AttemptID))
	id, err := p.db.NextEmbeddedMail(ctx, p.binding.AgentID)
	require.NoError(t, err)
	require.Empty(t, id)
	id, err = p.db.NextEmbeddedSteerMail(ctx, p.binding.AgentID)
	require.NoError(t, err)
	require.Empty(t, id)
	_, err = p.db.ReserveEmbeddedMail(ctx, p.binding, attempt.MessageID)
	require.Error(t, err)
	msg, err := p.db.MessageFor(ctx, attempt.MessageID)
	require.NoError(t, err)
	require.Equal(t, "queued", msg.State)
}

func TestClaudeRuntimeMailPrewriteDenialRemainsEligible(t *testing.T) {
	ctx := context.Background()
	r, p, _, _ := claudeRuntimeFixture(t, `{}`)
	require.NoError(t, p.db.ReconcileManagedRecipientSources(ctx))
	_, err := p.db.Enqueue(ctx, protocol.Envelope{ID: "prewrite-runtime", From: "sender", To: p.binding.AgentID, Body: "retry safely", CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	require.NoError(t, err)
	_, err = p.db.ResolveMessageTarget(ctx, "prewrite-runtime")
	require.NoError(t, err)
	attempt, err := p.db.ReserveEmbeddedMail(ctx, p.binding, "prewrite-runtime")
	require.NoError(t, err)
	p.mail = attempt
	r.beforeSubmit = func(context.Context, int) error { return errors.New("prewrite denied") }
	user, err := store.EmbeddedMailUserMessage(attempt, time.Now().UnixMilli())
	require.NoError(t, err)
	_, err = r.RunMessages(ctx, []agent.AgentMessage{user})
	require.ErrorContains(t, err, "prewrite denied")
	held, err := p.db.ClaudeMailHeld(ctx, attempt.MessageID)
	require.NoError(t, err)
	require.False(t, held)
	require.NoError(t, p.db.ReleaseEmbeddedUnconsumedAttempt(ctx, p.binding, attempt.AttemptID))
	id, err := p.db.NextEmbeddedMail(ctx, p.binding.AgentID)
	require.NoError(t, err)
	require.Equal(t, attempt.MessageID, id)
}

func TestClaudeRuntimeRejectedSteerRemainsEligible(t *testing.T) {
	ctx := context.Background()
	r, p, _, _ := claudeRuntimeFixture(t, `{"turns":[[{"text":"first answer"}],[{"text":"must not answer"}]]}`)
	require.NoError(t, p.db.ReconcileManagedRecipientSources(ctx))
	_, err := p.db.Enqueue(ctx, protocol.Envelope{ID: "steer-uncertain", From: "sender", To: p.binding.AgentID, Body: "uncertain steer", CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	require.NoError(t, err)
	_, err = p.db.ResolveMessageTarget(ctx, "steer-uncertain")
	require.NoError(t, err)
	attempt, err := p.db.ReserveEmbeddedMail(ctx, p.binding, "steer-uncertain")
	require.NoError(t, err)
	accepted := make(chan struct{})
	release := make(chan struct{})
	p.afterUserAppend = func(context.Context) {
		select {
		case <-accepted:
		default:
			close(accepted)
			<-release
		}
	}
	done := make(chan error, 1)
	go func() { _, e := r.Run(ctx, "first"); done <- e }()
	select {
	case <-accepted:
	case <-time.After(10 * time.Second):
		t.Fatal("first echo not checked")
	}
	require.False(t, r.TrySteer(buildSteerUserMessage(attempt, 2), attempt))
	close(release)
	select {
	case err = <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("runtime did not stop")
	}
	held, err := p.db.ClaudeMailHeld(ctx, attempt.MessageID)
	require.NoError(t, err)
	require.False(t, held)
	require.NoError(t, p.db.ReleaseEmbeddedUnconsumedAttempt(ctx, p.binding, attempt.AttemptID))
	id, err := p.db.NextEmbeddedSteerMail(ctx, p.binding.AgentID)
	require.NoError(t, err)
	require.Equal(t, attempt.MessageID, id)
	msg, err := p.db.MessageFor(ctx, attempt.MessageID)
	require.NoError(t, err)
	require.Equal(t, "queued", msg.State)
}
