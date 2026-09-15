package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
	"github.com/stretchr/testify/require"
)

func TestValidateEventRejectsUnknownAndUnsafeFields(t *testing.T) {
	tests := []struct {
		name   string
		event  string
		fields map[string]any
	}{
		{"unknown event", "made_up", nil},
		{"unknown field", "router_stopped", map[string]any{"path": "/private"}},
		{"nested value", "router_error", map[string]any{"error_code": map[string]any{"raw": "secret"}}},
		{"oversized string", "router_error", map[string]any{"error_code": strings.Repeat("x", maxEventStringBytes+1)}},
		{"invalid utf8", "router_error", map[string]any{"error_code": string([]byte{0xff})}},
		{"negative count", "lifecycle_broadcast_failed", map[string]any{"failure_count": -1}},
		{"oversized count", "lifecycle_broadcast_failed", map[string]any{"failure_count": maxEventCount + 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Error(t, validateEvent(test.event, test.fields))
		})
	}
}

func TestTypedEventLoggerWritesClosedBuilderEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "router.jsonl")
	writer, err := newLogWriter(path)
	require.NoError(t, err)
	var output strings.Builder
	logger := typedEventLogger(writer, &output)
	logger(transport.RouterStartedEvent(8765))
	logger(transport.RouterStoppedEvent())
	require.Empty(t, output.String())
	require.NoError(t, writer.Close())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), `"event":"router_started"`)
	require.Contains(t, string(data), `"event":"router_stopped"`)
}

func TestTypedEventLoggerRejectsZeroEventDeterministically(t *testing.T) {
	writer, err := newLogWriter(filepath.Join(t.TempDir(), "router.jsonl"))
	require.NoError(t, err)
	var output strings.Builder
	typedEventLogger(writer, &output)(transport.Event{})
	require.Contains(t, output.String(), "unknown event")
	require.NoError(t, writer.Close())
}

func TestLogWriterWritesRepresentativeSafeEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "router.jsonl")
	writer, err := newLogWriter(path)
	require.NoError(t, err)
	require.NoError(t, writer.Log("router_started", map[string]any{"port": 8765}))
	require.NoError(t, writer.Log("lifecycle_sweep_completed", map[string]any{
		"stale_agent_count":        1,
		"expired_message_count":    0,
		"timed_out_delivery_count": 2,
		"reminder_success_count":   0,
		"reminder_failure_count":   0,
		"server_job_success_count": 0,
		"server_job_failure_count": 0,
	}))
	require.NoError(t, writer.Close())

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 2)
	for _, line := range lines {
		var record map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &record))
		require.Contains(t, record, "event")
		require.Contains(t, record, "timestamp")
	}
}

func TestLogWriterRejectsUnsafeRecordBeforeWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "router.jsonl")
	writer, err := newLogWriter(path)
	require.NoError(t, err)
	require.Error(t, writer.Log("router_stopped", map[string]any{"body": "private"}))
	require.NoError(t, writer.Close())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Empty(t, data)
}
