package managed

import (
	"encoding/json"
	"maps"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadinessCanonicalJSONVectors(t *testing.T) {
	for _, test := range []struct {
		name, input, canonical, hash string
	}{
		{"default policy", `{"compaction":{"deferred":false},"inline":{"credits":{},"enabled":false},"recall":{"content":""},"settled":{"enabled":false}}`, `{"compaction":{"deferred":false},"inline":{"credits":{},"enabled":false},"recall":{"content":""},"settled":{"enabled":false}}`, "dcb17869020df00dd27a11e14cc0facada376f729a13002185a3d4b87f3c3648"},
		{"UTF-16 key order", `{"\ue000":1,"😀":2}`, `{"😀":2,"":1}`, "28c95d1bbb2209223307e62f489020e8f9e0cfa16adf2daf6d88127a1e8dd22a"},
		{"Unicode and escaping", `{"z":"\u000f\n\"\\","a":"€"}`, `{"a":"€","z":"\u000f\n\"\\"}`, "98f26eda458b7d2c608d427a3cc241632efa23705871286bd5d95bde7917cca7"},
		{"literal Unicode escape", `{"literal":"\\uD800","paired":"\ud83d\ude00"}`, `{"literal":"\\uD800","paired":"😀"}`, "79077a6609d12b2b3f6572139f10b86fcb6dfe86381f473c39cf7d9b512a3a65"},
		{"numeric boundaries", `{"a":-0,"b":1e30,"c":2e-3,"d":333333333.33333329,"e":1e-27,"f":0.000001,"g":100000000000000000000,"h":1e21}`, `{"a":0,"b":1e+30,"c":0.002,"d":333333333.3333333,"e":1e-27,"f":0.000001,"g":100000000000000000000,"h":1e+21}`, "c94f76fd49312072de4b95bcd57daa68330cb61b46cec8fb1ac5a55af5ad2855"},
	} {
		t.Run(test.name, func(t *testing.T) {
			canonical, hash, err := HashReadinessCanonicalJSON(test.input)
			require.NoError(t, err)
			require.Equal(t, test.canonical, string(canonical))
			require.Equal(t, test.hash, hash)
		})
	}
}

func TestReadinessCanonicalJSONHandlesArraysNullAndEveryGoNumberType(t *testing.T) {
	canonical, err := CanonicalizeReadinessJSON(`{"array":[null,true,false,{"b":2,"a":1}],"empty":[]}`)
	require.NoError(t, err)
	require.Equal(t, `{"array":[null,true,false,{"a":1,"b":2}],"empty":[]}`, string(canonical))

	for _, test := range []struct {
		name  string
		input any
		want  string
	}{
		{"int", int(-1), "-1"},
		{"int8", int8(-2), "-2"},
		{"int16", int16(-3), "-3"},
		{"int32", int32(-4), "-4"},
		{"int64", int64(-5), "-5"},
		{"uint", uint(1), "1"},
		{"uint8", uint8(2), "2"},
		{"uint16", uint16(3), "3"},
		{"uint32", uint32(4), "4"},
		{"uint64", uint64(5), "5"},
		{"float32", float32(1.5), "1.5"},
		{"float64", 2.5, "2.5"},
		{"json number", json.Number("3e2"), "300"},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := CanonicalizeReadinessJSON(test.input)
			require.NoError(t, err)
			require.Equal(t, test.want, string(encoded))
		})
	}
}

func TestReadinessCanonicalJSONRejectsMalformedInputAndKeepsLiteralEscapes(t *testing.T) {
	literal, err := CanonicalizeReadinessJSON(`{"literal":"\\uD800","paired":"\ud83d\ude00"}`)
	require.NoError(t, err)
	require.Equal(t, `{"literal":"\\uD800","paired":"😀"}`, string(literal))

	for _, input := range []string{
		``, ` `, `{`, `[`, `]`, `{"a":}`, `{"a":1,}`, `[1,]`, `true false`, `tru`, `fals`, `nul`,
		`-`, `--1`, `01`, `-01`, `1.`, `1e`, `1e+`, `"unterminated`, `"\x"`, `"\u12x4"`,
		`"\ud800"`, `"\udc00"`, `{"a":1,"a":2}`, `{"nested":{"a":1,"a":2}}`, `{"value":1e999}`,
	} {
		_, err := CanonicalizeReadinessJSON(input)
		require.Error(t, err, input)
	}
	_, err = CanonicalizeReadinessJSON([]byte{'"', 0xff, '"'})
	require.Error(t, err)
}

func defaultDesiredPolicyValue() map[string]any {
	return map[string]any{
		"inline":     map[string]any{"enabled": false, "credits": map[string]any{}},
		"recall":     map[string]any{"content": ""},
		"settled":    map[string]any{"enabled": false},
		"compaction": map[string]any{"deferred": false},
	}
}

func TestNormalizeAppliedPolicyReceiptAcceptsRetainedGoalProjectionAndIgnoresAdditiveFields(t *testing.T) {
	// This is the applied object captured from the V21 managed session. The
	// extension adds goal_revision after a goal is projected; the router only
	// persists the policy fields it owns.
	retained := map[string]any{
		"inline_enabled": false, "inline_credits": map[string]any{}, "recall_content": "",
		"settled_enabled": false, "compaction_deferred": false,
		"goal_revision": int64(1), "future_extension_field": map[string]any{"enabled": true},
	}

	normalized, err := normalizeAppliedPolicyReceipt(retained)
	require.NoError(t, err)
	require.Equal(t, defaultDesiredPolicyValue(), normalized)

	for _, malformed := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing required field", func(value map[string]any) { delete(value, "goal_lock_enabled") }},
		{"wrong required field type", func(value map[string]any) { value["inline_enabled"] = "false" }},
		{"wrong required credits type", func(value map[string]any) { value["inline_credits"] = []any{} }},
	} {
		t.Run(malformed.name, func(t *testing.T) {
			value := maps.Clone(retained)
			malformed.mutate(value)
			_, err := normalizeAppliedPolicyReceipt(value)
			require.Error(t, err)
		})
	}
}

func TestReadinessPolicyNormalizersRejectClosedShapeFailures(t *testing.T) {
	for _, test := range []struct {
		name  string
		value func() any
	}{
		{"root is not an object", func() any { return []any{} }},
		{"root has extra field", func() any { value := defaultDesiredPolicyValue(); value["extra"] = true; return value }},
		{"missing nested field", func() any {
			value := defaultDesiredPolicyValue()
			delete(value["inline"].(map[string]any), "credits")
			return value
		}},
		{"invalid inline flag", func() any {
			value := defaultDesiredPolicyValue()
			value["inline"].(map[string]any)["enabled"] = "false"
			return value
		}},
		{"unknown credit", func() any {
			value := defaultDesiredPolicyValue()
			value["inline"].(map[string]any)["credits"] = map[string]any{"shell": 1}
			return value
		}},
		{"fractional credit", func() any {
			value := defaultDesiredPolicyValue()
			value["inline"].(map[string]any)["credits"] = map[string]any{"bash": 1.5}
			return value
		}},
		{"unsafe credit", func() any {
			value := defaultDesiredPolicyValue()
			value["inline"].(map[string]any)["credits"] = map[string]any{"bash": float64(readinessMaxSafeInteger + 1)}
			return value
		}},
		{"too-large credit", func() any {
			value := defaultDesiredPolicyValue()
			value["inline"].(map[string]any)["credits"] = map[string]any{"bash": 6}
			return value
		}},
		{"recall too long", func() any {
			value := defaultDesiredPolicyValue()
			value["recall"].(map[string]any)["content"] = strings.Repeat("x", 24001)
			return value
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeDesiredPolicy(test.value())
			require.Error(t, err)
		})
	}

	validApplied := map[string]any{"inline_enabled": false, "inline_credits": map[string]any{}, "recall_content": "", "settled_enabled": false, "compaction_deferred": false}
	_, err := normalizeAppliedPolicyReceipt(validApplied)
	require.NoError(t, err)
	for _, malformed := range []map[string]any{
		{"inline_enabled": false},
		{"inline_enabled": false, "inline_credits": map[string]any{"bash": -1}, "recall_content": "", "settled_enabled": false, "compaction_deferred": false},
	} {
		_, err := normalizeAppliedPolicyReceipt(malformed)
		require.Error(t, err)
	}
}

func validChildRun() map[string]any {
	return map[string]any{
		"task_id": "task", "run_id": "run", "parent_entry_id": "entry", "description": "",
		"model": nil, "lifecycle_state": "completed", "started_at": "2026-01-02T03:04:05Z", "finished_at": nil,
		"duration_ms": nil, "compaction": map[string]any{"state": "idle", "count": 0, "started_at": nil, "last_outcome": nil}, "sequence": 0,
	}
}

func validChildSnapshot() map[string]any {
	return map[string]any{"protocol": "mecha.children/v1", "snapshot_id": "snapshot", "owner_session_id": "owner", "sequence": 0, "runs": []any{validChildRun()}}
}

func TestNormalizeChildSnapshotReceiptEnforcesTheClosedExtensionShape(t *testing.T) {
	valid, err := normalizeChildSnapshotReceipt(validChildSnapshot())
	require.NoError(t, err)
	require.Equal(t, "snapshot", valid["snapshot_id"])

	tooManyRuns := make([]any, readinessMaxJSONMembers+1)
	for index := range tooManyRuns {
		tooManyRuns[index] = validChildRun()
	}
	for _, test := range []struct {
		name  string
		value func() any
	}{
		{"top-level context reference", func() any { value := validChildSnapshot(); value["context_ref"] = "ctx1_secret"; return value }},
		{"nested context reference", func() any {
			value := validChildSnapshot()
			value["runs"].([]any)[0].(map[string]any)["context_ref"] = "ctx1_secret"
			return value
		}},
		{"too many runs", func() any { value := validChildSnapshot(); value["runs"] = tooManyRuns; return value }},
		{"missing source field", func() any {
			value := validChildSnapshot()
			delete(value["runs"].([]any)[0].(map[string]any), "model")
			return value
		}},
		{"unknown source field", func() any {
			value := validChildSnapshot()
			value["runs"].([]any)[0].(map[string]any)["extra"] = true
			return value
		}},
		{"invalid lifecycle", func() any {
			value := validChildSnapshot()
			value["runs"].([]any)[0].(map[string]any)["lifecycle_state"] = "idle"
			return value
		}},
		{"invalid timestamp", func() any {
			value := validChildSnapshot()
			value["runs"].([]any)[0].(map[string]any)["started_at"] = "yesterday"
			return value
		}},
		{"invalid model", func() any {
			value := validChildSnapshot()
			value["runs"].([]any)[0].(map[string]any)["model"] = map[string]any{"provider": "p", "id": "i", "name": "n", "extra": true}
			return value
		}},
		{"invalid compaction", func() any {
			value := validChildSnapshot()
			value["runs"].([]any)[0].(map[string]any)["compaction"] = map[string]any{"state": "idle", "count": -1, "started_at": nil, "last_outcome": nil}
			return value
		}},
		{"unsafe sequence", func() any {
			value := validChildSnapshot()
			value["sequence"] = float64(readinessMaxSafeInteger + 1)
			return value
		}},
		{"identity too long", func() any {
			value := validChildSnapshot()
			value["snapshot_id"] = strings.Repeat("x", readinessMaxJSONStringBytes+1)
			return value
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeChildSnapshotReceipt(test.value())
			require.Error(t, err)
		})
	}
}

func TestNormalizeChildDeltaReceiptEnforcesTheClosedEventShape(t *testing.T) {
	validDelta := func() map[string]any {
		return map[string]any{"protocol": "mecha.children/v1", "owner_session_id": "owner", "sequence": 1, "task_id": "task", "run_id": "run", "parent_entry_id": "entry", "changes": map[string]any{"lifecycle_state": "running"}}
	}
	valid, err := normalizeChildDeltaReceipt(validDelta())
	require.NoError(t, err)
	require.EqualValues(t, 1, valid["sequence"])
	for _, test := range []struct {
		name  string
		value func() any
	}{
		{"service-only context reference", func() any { value := validDelta(); value["context_ref"] = "ctx1_secret"; return value }},
		{"unknown change", func() any {
			value := validDelta()
			value["changes"].(map[string]any)["context_ref"] = "ctx1_secret"
			return value
		}},
		{"malformed compaction", func() any {
			value := validDelta()
			value["changes"] = map[string]any{"compaction": map[string]any{"state": "wrong"}}
			return value
		}},
		{"invalid source identity", func() any { value := validDelta(); value["task_id"] = ""; return value }},
		{"unsafe sequence", func() any {
			value := validDelta()
			value["sequence"] = float64(readinessMaxSafeInteger + 1)
			return value
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeChildDeltaReceipt(test.value())
			require.Error(t, err)
		})
	}
}

func TestNormalizeParentCompactionEventPreservesNativeOutcomes(t *testing.T) {
	start, err := normalizeParentCompactionEvent(map[string]any{"type": "compaction_start"}, 1)
	require.NoError(t, err)
	require.Equal(t, "compacting", start["state"])
	end, err := normalizeParentCompactionEvent(map[string]any{"type": "compaction_end", "result": map[string]any{}}, 2)
	require.NoError(t, err)
	require.Equal(t, "completed", end["outcome"])
	end, err = normalizeParentCompactionEvent(map[string]any{"type": "compaction_end", "aborted": true}, 3)
	require.NoError(t, err)
	require.Equal(t, "aborted", end["outcome"])
	end, err = normalizeParentCompactionEvent(map[string]any{"type": "compaction_end", "aborted": false}, 4)
	require.NoError(t, err)
	require.Equal(t, "failed", end["outcome"])
	_, err = normalizeParentCompactionEvent(map[string]any{"type": "message_end"}, 5)
	require.Error(t, err)
}

func TestNormalizeParentActivityEventUsesOnlyNativeTurnBoundaries(t *testing.T) {
	start, err := normalizeParentActivityEvent(map[string]any{"type": "agent_start"}, 1)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"sequence": int64(1), "state": "active"}, start)
	settled, err := normalizeParentActivityEvent(map[string]any{"type": "agent_settled"}, 2)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"sequence": int64(2), "state": "idle"}, settled)
	_, err = normalizeParentActivityEvent(map[string]any{"type": "turn_started"}, 3)
	require.Error(t, err)
	_, err = normalizeParentActivityEvent(map[string]any{"type": "agent_start"}, uint64(readinessMaxSafeInteger)+1)
	require.Error(t, err)
}

func TestReadinessIntegerAcceptsWireNumberFormsAndRejectsUnsafeNumbers(t *testing.T) {
	for _, value := range []any{int(1), int8(1), int16(1), int32(1), int64(1), uint(1), uint8(1), uint16(1), uint32(1), uint64(1), float32(1), float64(1)} {
		actual, ok := readinessInteger(value)
		require.True(t, ok, "%T", value)
		require.Equal(t, int64(1), actual)
	}
	for _, value := range []any{1.5, math.Inf(1), math.NaN(), float64(readinessMaxSafeInteger + 1), int64(readinessMaxSafeInteger + 1), uint64(readinessMaxSafeInteger + 1)} {
		_, ok := readinessInteger(value)
		require.False(t, ok, "%T", value)
	}
}
