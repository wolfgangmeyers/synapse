package store

import (
	"context"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func TestGoalControlRevisionSurvivesDesiredStateExpiry(t *testing.T) {
	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	s := openTestStoreWithOptions(t, Options{Clock: clock})
	config := map[string]any{"sourceAgentId": "source", "controlId": "first", "action": "set", "goal": "first", "settings": map[string]any{}}

	first, err := s.UpsertGoalControlDesired(context.Background(), "target", "instance", config, formatTimestamp(start.Add(time.Second)))
	require.NoError(t, err)
	require.Equal(t, int64(1), first.Revision)
	require.NoError(t, clock.Advance(2*time.Second))
	require.Nil(t, func() *GoalControlDesired {
		value, readErr := s.GoalControlDesiredFor(context.Background(), "target", "instance")
		require.NoError(t, readErr)
		return value
	}())

	config["controlId"] = "second"
	second, err := s.UpsertGoalControlDesired(context.Background(), "target", "instance", config, formatTimestamp(start.Add(time.Hour)))
	require.NoError(t, err)
	require.Equal(t, int64(2), second.Revision)
}

func TestConsumeGoalControlDesiredFencesDuplicateResults(t *testing.T) {
	s := openTestStore(t)
	config := map[string]any{"sourceAgentId": "source", "controlId": "control", "action": "set", "goal": "goal", "settings": map[string]any{}}
	desired, err := s.UpsertGoalControlDesired(context.Background(), "target", "instance", config, "2030-01-01T00:10:00.000Z")
	require.NoError(t, err)

	consumed, err := s.ConsumeGoalControlDesired(context.Background(), "target", "instance", "source", "control", desired.Revision)
	require.NoError(t, err)
	require.NotNil(t, consumed)
	duplicate, err := s.ConsumeGoalControlDesired(context.Background(), "target", "instance", "source", "control", desired.Revision)
	require.NoError(t, err)
	require.Nil(t, duplicate)
}
