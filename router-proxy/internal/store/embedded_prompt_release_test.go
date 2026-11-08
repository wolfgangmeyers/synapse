package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// A direct steer refused before any stdin write must not leave its request ID
// pending forever. Release reopens only that exact pending reservation.
func TestEmbeddedPromptReleaseReopensRefusedSteer(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	defer s.Close()
	b := redBind(t, s, "release-agent", "session", 1, "runtime", "root")
	_, err := s.MintEmbeddedProcessGeneration(ctx, "release-process")
	require.NoError(t, err)

	replay, err := s.ReserveEmbeddedPrompt(ctx, b, "steer-refused", "steer me")
	require.NoError(t, err)
	require.False(t, replay)
	_, err = s.ReserveEmbeddedPrompt(ctx, b, "steer-refused", "steer me")
	require.ErrorContains(t, err, "outcome unknown", "an unreleased pending request stays ambiguous")

	require.NoError(t, s.ReleaseEmbeddedPrompt(ctx, b, "steer-refused"))

	replay, err = s.ReserveEmbeddedPrompt(ctx, b, "steer-refused", "steer me")
	require.NoError(t, err, "a released request ID must be reservable again")
	require.False(t, replay)

	require.Error(t, s.ReleaseEmbeddedPrompt(ctx, b, "never-reserved"), "releasing an unknown request must fail")
}
