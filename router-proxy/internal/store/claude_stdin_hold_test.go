package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClaudeStdinHoldSurvivesReleaseAndReopen(t *testing.T) {
	ctx := context.Background()
	s, b, path := embeddedMailFixture(t)
	a, err := s.ReserveEmbeddedMail(ctx, b, "embedded-mail-g2")
	require.NoError(t, err)
	wrong := b
	wrong.Generation = "stale"
	require.Error(t, s.MarkClaudeMailWriteStarted(ctx, wrong, a.AttemptID))
	id, err := s.NextEmbeddedMail(ctx, b.AgentID)
	require.NoError(t, err)
	require.Equal(t, a.MessageID, id)
	require.NoError(t, s.MarkClaudeMailWriteStarted(ctx, b, a.AttemptID))
	require.NoError(t, s.ReleaseEmbeddedUnconsumedAttempt(ctx, b, a.AttemptID))
	require.NoError(t, s.Close())
	s, err = Open(ctx, path, Options{})
	require.NoError(t, err)
	defer s.Close()
	id, err = s.NextEmbeddedMail(ctx, b.AgentID)
	require.NoError(t, err)
	require.Empty(t, id)
	id, err = s.NextEmbeddedSteerMail(ctx, b.AgentID)
	require.NoError(t, err)
	require.Empty(t, id)
	_, err = s.ReserveEmbeddedMail(ctx, b, a.MessageID)
	require.Error(t, err)
	m, err := s.MessageFor(ctx, a.MessageID)
	require.NoError(t, err)
	require.Equal(t, "queued", m.State)
}
