package store

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestClaudeBindingStartedPersistsAndFences(t *testing.T) {
	ctx := context.Background()
	s, b, path := embeddedMailFixture(t)
	started, err := s.ClaudeBindingStarted(ctx, b)
	require.NoError(t, err)
	require.False(t, started)
	stale := b
	stale.OwnerToken++
	require.Error(t, s.MarkClaudeBindingStarted(ctx, stale))
	require.NoError(t, s.MarkClaudeBindingStarted(ctx, b))
	reopened, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer reopened.Close()
	started, err = reopened.ClaudeBindingStarted(ctx, b)
	require.NoError(t, err)
	require.True(t, started)
	next := resetReservationNext(b)
	require.NoError(t, s.ResetEmbeddedSessionEpoch(ctx, b, next, "root"))
	next = claimEmbeddedFixture(t, s, next)
	started, err = s.ClaudeBindingStarted(ctx, next)
	require.NoError(t, err)
	require.False(t, started)
	require.Error(t, s.MarkClaudeBindingStarted(ctx, b))
}
