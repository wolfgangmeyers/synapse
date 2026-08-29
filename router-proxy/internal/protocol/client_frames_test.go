package protocol

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func frame(t *testing.T, value map[string]any) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	var decoded map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &decoded))
	return decoded
}

func TestValidateClientFrameChecksNestedShapesAndRanges(t *testing.T) {
	base := map[string]any{
		"version": "v1", "frame_id": "frame", "type": "activity",
		"agent_id": "agent", "instance_id": "instance", "lease_id": "lease",
		"activity_state": "active", "subagent_count": 1,
		"subagents": []any{map[string]any{"id": "child", "description": "working", "status": "running"}},
	}
	require.NoError(t, ValidateClientFrame(frame(t, base)))

	invalid := []map[string]any{
		{"subagent_count": 1000},
		{"subagents": []any{map[string]any{"id": "child", "description": "working", "status": "bogus"}}},
		{"activity_state": "unknown"},
	}
	for _, patch := range invalid {
		candidate := make(map[string]any, len(base))
		for key, value := range base {
			candidate[key] = value
		}
		for key, value := range patch {
			candidate[key] = value
		}
		require.Error(t, ValidateClientFrame(frame(t, candidate)))
	}
}

func TestValidateClientFrameAcceptsNullableGoalSettingsAndRejectsUnsafeIntegers(t *testing.T) {
	settings := map[string]any{
		"goal_lock_enabled": true, "snooze_enabled": true, "snooze_duration_ms": 1,
		"snooze_turn_limit": nil, "snooze_budget_cap_ms": nil,
		"snooze_refill_yield_ms": 1, "snooze_refill_elapsed_ms": 1,
	}
	value := map[string]any{
		"version": "v1", "frame_id": "frame", "type": "goal_control",
		"agent_id": "agent", "instance_id": "instance", "lease_id": "lease",
		"target_agent_id": "target", "target_instance_id": "target-instance", "control_id": "control",
		"action": "set", "goal": "do work", "settings": settings,
	}
	require.NoError(t, ValidateClientFrame(frame(t, value)))
	settings["snooze_duration_ms"] = float64(9007199254740992)
	require.Error(t, ValidateClientFrame(frame(t, value)))
}
