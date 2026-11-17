package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// NormalizeEmbeddedWorkflowSnoozeArguments validates the original closed intent,
// requiring a substantive justification while deriving a bounded effective duration.
func NormalizeEmbeddedWorkflowSnoozeArguments(raw []byte, defaultMS int64) ([]byte, int64, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, 0, errors.New("workflow snooze arguments must be an object")
	}
	fields := map[string]json.RawMessage{}
	var minutes, ms int64
	var justification string
	hasJustification := false
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return nil, 0, err
		}
		name, ok := key.(string)
		if !ok {
			return nil, 0, errors.New("invalid argument")
		}
		if _, ok := fields[name]; ok {
			return nil, 0, errors.New("duplicate argument")
		}
		var v json.RawMessage
		if err = d.Decode(&v); err != nil {
			return nil, 0, err
		}
		fields[name] = v
		switch name {
		case "justification":
			if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
				return nil, 0, errors.New("justification is required for workflow_snooze and must describe the external asynchronous process being awaited (at least 8 characters)")
			}
			if err = json.Unmarshal(v, &justification); err != nil {
				return nil, 0, errors.New("justification is required for workflow_snooze and must describe the external asynchronous process being awaited (at least 8 characters)")
			}
			hasJustification = true
		case "duration_minutes", "duration_ms":
			var n int64
			if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
				return nil, 0, errors.New("duration must be an integer")
			}
			if err = json.Unmarshal(v, &n); err != nil {
				return nil, 0, err
			}
			if name == "duration_minutes" {
				minutes = n
			} else {
				ms = n
			}
		case "reason":
			var reason string
			if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
				return nil, 0, errors.New("reason must be a string")
			}
			if err = json.Unmarshal(v, &reason); err != nil {
				return nil, 0, err
			}
		default:
			return nil, 0, errors.New("unknown workflow snooze argument")
		}
	}
	if _, err = d.Token(); err != nil {
		return nil, 0, err
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return nil, 0, errors.New("trailing arguments")
	}
	if !hasJustification || len(strings.TrimSpace(justification)) < 8 {
		return nil, 0, errors.New("justification is required for workflow_snooze and must describe the external asynchronous process being awaited (at least 8 characters)")
	}
	limit := defaultMS
	if limit <= 0 {
		limit = 600000
	}
	if limit > EmbeddedGoalSnoozeMaxDurationMS {
		limit = EmbeddedGoalSnoozeMaxDurationMS
	}
	duration := limit
	if minutes > 0 {
		if minutes >= EmbeddedGoalSnoozeMaxDurationMS/60000 {
			duration = EmbeddedGoalSnoozeMaxDurationMS
		} else {
			duration = minutes * 60000
		}
	} else if ms > 0 {
		duration = ms
	}
	if duration < 1000 {
		duration = 1000
	}
	if duration > limit {
		duration = limit
	}
	canonical, err := json.Marshal(fields)
	return canonical, duration, err
}

// WorkflowResultJSON formats the snooze tool execution response including duration, workflow name, cumulative snooze count, and escalating anti-abuse guidance.
func (p EmbeddedGoalSnoozePreference) WorkflowResultJSON() ([]byte, error) {
	count := p.SnoozeCount
	if count <= 0 {
		count = 1
	}

	result := map[string]any{
		"ok":               true,
		"snoozed_until":    p.DueAt,
		"duration_ms":      p.DurationMS,
		"duration_minutes": p.DurationMS / 60000,
		"workflow":         p.Workflow,
		"snooze_count":     count,
	}

	switch {
	case count >= 4:
		warning := fmt.Sprintf("🚨 Warning: Workflow snooze count for this run: %d. You have snoozed %d times in this workflow run. Snooze is strictly prohibited as a stalling tactic. If an external build or CI process is not actively running, you MUST call mecha_blocked immediately or make active progress.", count, count)
		result["guidance"] = warning
		result["warning"] = warning
	case count >= 2:
		warning := fmt.Sprintf("⚠️ Advisory: Workflow snooze count for this run: %d. Repeated snoozing without waiting on an active external process violates workflow execution discipline. If you are stuck or waiting on user/environment input, call mecha_blocked.", count)
		result["guidance"] = warning
		result["warning"] = warning
	default:
		result["guidance"] = "Workflow snooze count for this run: 1. Snooze is ONLY for waiting on active external asynchronous processes (such as CI test runs or remote deployments). If progress is stalled due to ambiguity, missing credentials, test failures, or lack of external input, you must call mecha_blocked instead of repeatedly snoozing."
	}

	return json.Marshal(result)
}

// SnoozeBlockedGuidance distinguishes a timed delay from a user dependency.
const SnoozeBlockedGuidance = "If waiting on the user, explicitly call mecha_blocked with the reason."

func (p EmbeddedGoalSnoozePreference) GoalResultJSON() ([]byte, error) {
	return json.Marshal(struct {
		EmbeddedGoalSnoozePreference
		Guidance string `json:"guidance"`
	}{p, SnoozeBlockedGuidance})
}

