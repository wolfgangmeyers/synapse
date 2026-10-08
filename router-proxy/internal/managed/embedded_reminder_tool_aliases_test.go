package managed

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedReminderArgumentAliases(t *testing.T) {
	// Wolfgang's screenshot exact payload:
	// action: "create", body: "Check on subagent...", due_at: "2026-10-08T16:30:00Z", interval_ms: 300000
	t.Run("screenshot payload with RFC3339 timestamp", func(t *testing.T) {
		args := map[string]any{
			"action":      "create",
			"body":        "Check on subagent...",
			"due_at":      "2026-10-08T16:30:00Z",
			"interval_ms": 300000,
		}
		raw, err := embeddedReminderArguments(args)
		require.NoError(t, err)
		require.NotEmpty(t, raw)

		parsed, err := store.ParseEmbeddedOwnReminderArguments(raw)
		require.NoError(t, err)
		require.Equal(t, "create", parsed.Action)
		require.Equal(t, "Check on subagent...", parsed.Body)
		require.Equal(t, "2026-10-08T16:30:00.000Z", parsed.DueAt)
		require.Equal(t, int64(300000), parsed.IntervalMS)
		require.True(t, parsed.HasBody)
		require.True(t, parsed.HasDueAt)
		require.True(t, parsed.HasInterval)

		require.True(t, embeddedReminderSchema().Check(args))
	})

	t.Run("aliases mapping message scheduled_at interval_minutes subject", func(t *testing.T) {
		args := map[string]any{
			"action":           "create",
			"message":          "Subagent status",
			"subject":          "Subagent Check",
			"scheduled_at":     "2026-10-08T16:30:00Z",
			"interval_minutes": 5,
		}
		raw, err := embeddedReminderArguments(args)
		require.NoError(t, err)

		parsed, err := store.ParseEmbeddedOwnReminderArguments(raw)
		require.NoError(t, err)
		require.Equal(t, "create", parsed.Action)
		require.Equal(t, "Subagent status", parsed.Body)
		require.Equal(t, "Subagent Check", parsed.Subject)
		require.Equal(t, "2026-10-08T16:30:00.000Z", parsed.DueAt)
		require.Equal(t, int64(300000), parsed.IntervalMS)
		require.True(t, parsed.HasBody)
		require.True(t, parsed.HasDueAt)
		require.True(t, parsed.HasInterval)
		require.True(t, parsed.HasSubject)
		require.True(t, embeddedReminderSchema().Check(args))
	})

	t.Run("delay_minutes creates valid future due_at", func(t *testing.T) {
		args := map[string]any{
			"action":        "create",
			"text":          "delayed reminder",
			"delay_minutes": 10,
		}
		raw, err := embeddedReminderArguments(args)
		require.NoError(t, err)

		parsed, err := store.ParseEmbeddedOwnReminderArguments(raw)
		require.NoError(t, err)
		require.Equal(t, "create", parsed.Action)
		require.Equal(t, "delayed reminder", parsed.Body)
		require.NotEmpty(t, parsed.DueAt)
		require.True(t, parsed.HasDueAt)

		parsedTime, err := time.Parse("2006-01-02T15:04:05.000Z", parsed.DueAt)
		require.NoError(t, err)
		require.True(t, parsedTime.After(time.Now().UTC()))
	})

	t.Run("tolerant of extra non-schema properties in JSON", func(t *testing.T) {
		raw := []byte(`{"action":"create","body":"test","due_at":"2026-10-08T16:30:00Z","thought":"thinking...","extra_field":123}`)
		parsed, err := store.ParseEmbeddedOwnReminderArguments(raw)
		require.NoError(t, err)
		require.Equal(t, "create", parsed.Action)
		require.Equal(t, "test", parsed.Body)
		require.Equal(t, "2026-10-08T16:30:00.000Z", parsed.DueAt)
	})
}

func TestGoalSnoozeDurationCap(t *testing.T) {
	t.Run("snooze duration capped to 60s when passed 300,000ms", func(t *testing.T) {
		args := map[string]any{
			"duration_ms": int64(300000),
		}
		raw, err := embeddedGoalSnoozeArguments(args)
		require.NoError(t, err)

		require.Equal(t, int64(60000), args["duration_ms"])

		var decoded map[string]any
		require.NoError(t, json.Unmarshal(raw, &decoded))
		require.Equal(t, float64(60000), decoded["duration_ms"])

		parsed, err := store.ParseEmbeddedGoalSnoozeArguments(raw)
		require.NoError(t, err)
		require.Equal(t, int64(60000), parsed.DurationMS)
	})

	t.Run("snooze duration within limit preserved", func(t *testing.T) {
		args := map[string]any{
			"duration_ms": int64(30000),
		}
		raw, err := embeddedGoalSnoozeArguments(args)
		require.NoError(t, err)
		require.Equal(t, int64(30000), args["duration_ms"])

		parsed, err := store.ParseEmbeddedGoalSnoozeArguments(raw)
		require.NoError(t, err)
		require.Equal(t, int64(30000), parsed.DurationMS)
	})

	t.Run("embeddedGoalSnoozeArguments directly clamps > 60s", func(t *testing.T) {
		args := map[string]any{"duration_ms": 500000}
		raw, err := embeddedGoalSnoozeArguments(args)
		require.NoError(t, err)
		require.Equal(t, int64(60000), args["duration_ms"])
		parsed, err := store.ParseEmbeddedGoalSnoozeArguments(raw)
		require.NoError(t, err)
		require.Equal(t, int64(60000), parsed.DurationMS)
	})
}
