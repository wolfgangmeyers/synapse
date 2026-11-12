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

func TestNormalizeEmbeddedWorkflowSnoozeArgumentsValidRawJSONControls(t *testing.T) {
	for _, tt := range []struct {
		name     string
		raw      string
		duration int64
	}{
		{"empty object uses workflow default", `{}`, 120000},
		{"positive milliseconds with nonpositive minutes", `{"duration_minutes":0,"duration_ms":2000}`, 2000},
		{"configured maximum", `{"duration_ms":900000}`, 120000},
		{"optional reason and trailing whitespace", " {\"reason\":\"later\",\"duration_minutes\":1} \n\t", 60000},
	} {
		t.Run(tt.name, func(t *testing.T) {
			normalized, duration, err := NormalizeEmbeddedWorkflowSnoozeArguments([]byte(tt.raw), 120000)
			require.NoError(t, err)
			require.JSONEq(t, tt.raw, string(normalized))
			require.Equal(t, tt.duration, duration)
		})
	}
}
