package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// NormalizeEmbeddedWorkflowSnoozeArguments validates the original closed intent,
// preserving optional fields while deriving a bounded effective duration.
func NormalizeEmbeddedWorkflowSnoozeArguments(raw []byte, defaultMS int64) ([]byte, int64, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, 0, errors.New("workflow snooze arguments must be an object")
	}
	fields := map[string]json.RawMessage{}
	var minutes, ms int64
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

func (p EmbeddedGoalSnoozePreference) WorkflowResultJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"ok": true, "snoozed_until": p.DueAt, "duration_ms": p.DurationMS, "duration_minutes": p.DurationMS / 60000, "workflow": p.Workflow, "guidance": SnoozeBlockedGuidance})
}

// SnoozeBlockedGuidance distinguishes a timed delay from a user dependency.
const SnoozeBlockedGuidance = "If waiting on the user, explicitly call mecha_blocked with the reason."

func (p EmbeddedGoalSnoozePreference) GoalResultJSON() ([]byte, error) {
	return json.Marshal(struct {
		EmbeddedGoalSnoozePreference
		Guidance string `json:"guidance"`
	}{p, SnoozeBlockedGuidance})
}
