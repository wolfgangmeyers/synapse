package managed

import (
	"context"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedGoalSnoozePolicyLimitClamping(t *testing.T) {
	t.Run("when policy is 60s and worker asks for 300s -> clamped to 60s", func(t *testing.T) {
		policyLimitMS := int64(60_000)
		args := map[string]any{
			"duration_ms": int64(300_000),
		}
		raw, err := embeddedGoalSnoozeArguments(args, policyLimitMS)
		require.NoError(t, err)

		require.Equal(t, int64(60_000), args["duration_ms"])

		parsed, err := store.ParseEmbeddedGoalSnoozeArguments(raw)
		require.NoError(t, err)
		require.Equal(t, int64(60_000), parsed.DurationMS)
	})

	t.Run("when policy is 5m (300s) and worker asks for 300s -> allowed 300s", func(t *testing.T) {
		policyLimitMS := int64(300_000)
		args := map[string]any{
			"duration_ms": int64(300_000),
		}
		raw, err := embeddedGoalSnoozeArguments(args, policyLimitMS)
		require.NoError(t, err)

		require.Equal(t, int64(300_000), args["duration_ms"])

		parsed, err := store.ParseEmbeddedGoalSnoozeArguments(raw)
		require.NoError(t, err)
		require.Equal(t, int64(300_000), parsed.DurationMS)
	})

	t.Run("when policy is 5m (300s) and worker asks for 600s -> clamped to 300s", func(t *testing.T) {
		policyLimitMS := int64(300_000)
		args := map[string]any{
			"duration_ms": int64(600_000),
		}
		raw, err := embeddedGoalSnoozeArguments(args, policyLimitMS)
		require.NoError(t, err)

		require.Equal(t, int64(300_000), args["duration_ms"])

		parsed, err := store.ParseEmbeddedGoalSnoozeArguments(raw)
		require.NoError(t, err)
		require.Equal(t, int64(300_000), parsed.DurationMS)
	})

	t.Run("when worker calls goal_snooze with no duration -> defaults to limitMs", func(t *testing.T) {
		policyLimitMS := int64(300_000)
		args := map[string]any{}
		raw, err := embeddedGoalSnoozeArguments(args, policyLimitMS)
		require.NoError(t, err)

		require.Equal(t, int64(300_000), args["duration_ms"])

		parsed, err := store.ParseEmbeddedGoalSnoozeArguments(raw)
		require.NoError(t, err)
		require.Equal(t, int64(300_000), parsed.DurationMS)
	})

	t.Run("when policy is 60s and worker calls with no duration -> defaults to 60s", func(t *testing.T) {
		policyLimitMS := int64(60_000)
		args := map[string]any{}
		raw, err := embeddedGoalSnoozeArguments(args, policyLimitMS)
		require.NoError(t, err)

		require.Equal(t, int64(60_000), args["duration_ms"])

		parsed, err := store.ParseEmbeddedGoalSnoozeArguments(raw)
		require.NoError(t, err)
		require.Equal(t, int64(60_000), parsed.DurationMS)
	})
}

func TestUserConfiguring5HoursOnGoalPolicySucceeds(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, fence, _ := readyGoalProjectionFixture(t)

	fiveHoursMS := int64(5 * 60 * 60 * 1000) // 18,000,000 ms = 5 hours
	settings := map[string]any{
		"snooze_enabled":           true,
		"snooze_duration_ms":       fiveHoursMS,
		"snooze_turn_limit":        nil,
		"snooze_budget_cap_ms":     nil,
		"snooze_refill_yield_ms":   int64(30_000),
		"snooze_refill_elapsed_ms": int64(60_000),
	}

	result, err := manager.executeManagedGoalControl(ctx, binding, RootControlInvocation{
		RequestID:   "123e4567-e89b-42d3-a456-426614174099",
		OperationID: "goal.control",
		Arguments:   map[string]any{"action": "set", "goal": "long migration", "settings": settings},
		Fence:       fence,
	})
	require.NoError(t, err)
	require.Equal(t, "ok", result.Status)

	state, ok := result.Details["goal_state"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, fiveHoursMS, state["snooze_duration_ms"])

	policy, err := durable.ManagedGoalPolicyFor(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, fiveHoursMS, policy.Config.Settings.SnoozeDurationMS)

	// Verify manager lookup reflects the active configured policy duration
	limit := manager.goalSnoozeLimitMS(ctx, fence.AgentID)
	require.Equal(t, fiveHoursMS, limit)

	// Worker can snooze up to the configured 5 hours
	workerArgs := map[string]any{"duration_ms": fiveHoursMS}
	raw, err := embeddedGoalSnoozeArguments(workerArgs, limit)
	require.NoError(t, err)
	require.Equal(t, fiveHoursMS, workerArgs["duration_ms"])
	parsed, err := store.ParseEmbeddedGoalSnoozeArguments(raw)
	require.NoError(t, err)
	require.Equal(t, fiveHoursMS, parsed.DurationMS)

	// Worker asking beyond 5 hours is clamped to 5 hours
	sixHoursMS := int64(6 * 60 * 60 * 1000)
	clampedArgs := map[string]any{"duration_ms": sixHoursMS}
	rawClamped, err := embeddedGoalSnoozeArguments(clampedArgs, limit)
	require.NoError(t, err)
	require.Equal(t, fiveHoursMS, clampedArgs["duration_ms"])
	parsedClamped, err := store.ParseEmbeddedGoalSnoozeArguments(rawClamped)
	require.NoError(t, err)
	require.Equal(t, fiveHoursMS, parsedClamped.DurationMS)
}
