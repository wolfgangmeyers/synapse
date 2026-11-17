package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Supporting raw-parser coverage, not an end-to-end workflow test.
func TestNormalizeEmbeddedWorkflowSnoozeArgumentsRejectsRawJSON(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
	}{
		{"duplicate minutes", `{"duration_minutes":1,"duration_minutes":2}`},
		{"duplicate milliseconds", `{"duration_ms":60000,"duration_ms":120000}`},
		{"duplicate reason", `{"reason":"first","reason":"second"}`},
		{"escaped duplicate key", `{"reason":"first","rea\u0073on":"second"}`},
		{"trailing object", `{"duration_ms":60000} {}`},
		{"trailing array", `{"duration_ms":60000} []`},
		{"trailing null", `{"duration_ms":60000} null`},
		{"trailing string", `{"duration_ms":60000} "extra"`},
		{"array", `[]`},
		{"null", `null`},
		{"string", `"snooze"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			normalized, duration, err := NormalizeEmbeddedWorkflowSnoozeArguments([]byte(tt.raw), 120000)
			require.Error(t, err)
			require.Nil(t, normalized, "rejected arguments must not produce normalized output")
			require.Zero(t, duration, "rejected arguments must not produce an effective duration")
		})
	}
}

func TestNormalizeEmbeddedWorkflowSnoozeArgumentsJustificationValidation(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
	}{
		{"missing justification", `{}`},
		{"missing justification with duration", `{"duration_minutes":5}`},
		{"null justification", `{"justification":null}`},
		{"integer justification", `{"justification":12345678}`},
		{"boolean justification", `{"justification":true}`},
		{"empty string justification", `{"justification":""}`},
		{"whitespace only justification", `{"justification":"       "}`},
		{"too short justification (7 chars)", `{"justification":"1234567"}`},
		{"whitespace padded too short (6 trimmed chars)", `{"justification":"   123456   "}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			normalized, duration, err := NormalizeEmbeddedWorkflowSnoozeArguments([]byte(tt.raw), 120000)
			require.Error(t, err)
			require.Equal(t, "justification is required for workflow_snooze and must describe the external asynchronous process being awaited (at least 8 characters)", err.Error())
			require.Nil(t, normalized)
			require.Zero(t, duration)
		})
	}
}

func TestNormalizeEmbeddedWorkflowSnoozeArgumentsValidRawJSONControls(t *testing.T) {
	for _, tt := range []struct {
		name     string
		raw      string
		duration int64
	}{
		{"justification only uses workflow default", `{"justification":"Awaiting CI build #1042"}`, 120000},
		{"positive milliseconds with nonpositive minutes", `{"justification":"Awaiting CI build #1042","duration_minutes":0,"duration_ms":2000}`, 2000},
		{"configured maximum", `{"justification":"Awaiting CI build #1042","duration_ms":900000}`, 120000},
		{"optional reason and trailing whitespace", " {\"justification\":\"Awaiting CI build #1042\",\"reason\":\"later\",\"duration_minutes\":1} \n\t", 60000},
	} {
		t.Run(tt.name, func(t *testing.T) {
			normalized, duration, err := NormalizeEmbeddedWorkflowSnoozeArguments([]byte(tt.raw), 120000)
			require.NoError(t, err)
			require.JSONEq(t, tt.raw, string(normalized))
			require.Equal(t, tt.duration, duration)
		})
	}
}
