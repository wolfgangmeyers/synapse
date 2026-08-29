package protocol

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func frame(t *testing.T, value map[string]any) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return rawFrame(t, raw)
}

func rawFrame(t *testing.T, raw []byte) map[string]json.RawMessage {
	t.Helper()
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

func TestSanitizeClientFrameStripsTopLevelNestedAndArrayFields(t *testing.T) {
	value := rawFrame(t, []byte(`{"version":"v1","frame_id":"frame","type":"activity","agent_id":"agent","instance_id":"instance","lease_id":"lease","activity_state":"active","future":true,"goal":{"enabled":true,"goal":"work","future":{"ignored":true}},"subagents":[{"id":"child","description":"working","status":"running","future":[1,2]}]}`))
	sanitized := SanitizeClientFrame(value)
	require.NotContains(t, sanitized, "future")
	var goal map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(sanitized["goal"], &goal))
	require.Equal(t, `true`, string(goal["enabled"]))
	require.Equal(t, `"work"`, string(goal["goal"]))
	require.NotContains(t, goal, "future")
	var subagents []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(sanitized["subagents"], &subagents))
	require.Len(t, subagents, 1)
	require.Equal(t, `"child"`, string(subagents[0]["id"]))
	require.NotContains(t, subagents[0], "future")
	require.NoError(t, ValidateClientFrame(sanitized))
}

func TestSanitizeClientFrameStripsModelArrayAndGoalStateFields(t *testing.T) {
	value := rawFrame(t, []byte(`{"version":"v1","frame_id":"frame","type":"model_catalog","agent_id":"agent","instance_id":"instance","lease_id":"lease","model_summaries":[{"provider":"p","id":"m","name":"Model","future":true}],"current_model":{"provider":"p","id":"m","future":true},"future":true}`))
	sanitized := SanitizeClientFrame(value)
	require.NoError(t, ValidateClientFrame(sanitized))
	var models []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(sanitized["model_summaries"], &models))
	require.NotContains(t, models[0], "future")
	var current map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(sanitized["current_model"], &current))
	require.NotContains(t, current, "future")
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

func TestValidateClientFrameAcceptsNodeIntegralNumberSpellingsAndRejectsFractionsAndUnsafeIntegers(t *testing.T) {
	for _, number := range []string{"1.0", "1e3", "9.007199254740991e15"} {
		require.NoError(t, ValidateClientFrame(goalControlWithDuration(t, number)), number)
	}
	for _, number := range []string{"1.5", "9007199254740992", "9.007199254740992e15"} {
		require.Error(t, ValidateClientFrame(goalControlWithDuration(t, number)), number)
	}
}

func goalControlWithDuration(t *testing.T, duration string) map[string]json.RawMessage {
	t.Helper()
	return rawFrame(t, []byte(fmt.Sprintf(`{"version":"v1","frame_id":"frame","type":"goal_control","agent_id":"agent","instance_id":"instance","lease_id":"lease","target_agent_id":"target","target_instance_id":"target-instance","control_id":"control","action":"set","goal":"do work","settings":{"goal_lock_enabled":true,"snooze_enabled":true,"snooze_duration_ms":%s,"snooze_turn_limit":null,"snooze_budget_cap_ms":null,"snooze_refill_yield_ms":1,"snooze_refill_elapsed_ms":1}}`, duration)))
}
