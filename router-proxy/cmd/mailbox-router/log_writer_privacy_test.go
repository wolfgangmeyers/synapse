package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
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

func TestValidateEventAcceptsEveryProductionClosedCallSiteValue(t *testing.T) {
	tests := []struct {
		name  string
		event transport.Event
	}{
		{"delivery timed out dead letter", transport.DeliveryTimedOutEvent("message", "agent", 1, "dead_letter", "delivery_timeout")},
		{"delivery timed out dead lettered", transport.DeliveryTimedOutEvent("message", "agent", 1, "dead_lettered", "delivery_timeout")},
		{"reminder invalid", transport.ReminderFailedEvent("reminder", "2026-01-01T00:00:00Z", "invalid_reminder")},
		{"reminder queue full", transport.ReminderFailedEvent("reminder", "2026-01-01T00:00:00Z", "queue_full")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.NoError(t, validateEvent(test.event.Name(), test.event.Fields()))
		})
	}
	require.Error(t, validateEvent("delivery_timed_out", transport.DeliveryTimedOutEvent("message", "agent", 1, "arbitrary", "delivery_timeout").Fields()))
	require.Error(t, validateEvent("reminder_failed", transport.ReminderFailedEvent("reminder", "2026-01-01T00:00:00Z", "arbitrary").Fields()))
}

func TestValidateEventAcceptsEveryLifecycleEmitterValue(t *testing.T) {
	base := map[string]any{"agent_id": "agent", "event_id": "event", "client_sequence": 1, "subagent_count": 1}
	for _, kind := range []string{"started", "terminal", "snapshot"} {
		for _, event := range []string{"lifecycle_ingest_failed", "lifecycle_ingested", "lifecycle_persist_failed", "lifecycle_persisted", "lifecycle_ack_failed", "lifecycle_acknowledged"} {
			fields := map[string]any{}
			for key, value := range base {
				fields[key] = value
			}
			fields["lifecycle_event"] = kind
			if event == "lifecycle_ingest_failed" || event == "lifecycle_persist_failed" || event == "lifecycle_ack_failed" {
				fields["reason"] = map[string]string{"lifecycle_ingest_failed": "invalid_candidate", "lifecycle_persist_failed": "store_error", "lifecycle_ack_failed": "send_failed"}[event]
			}
			if event == "lifecycle_persisted" || event == "lifecycle_acknowledged" {
				fields["disposition"], fields["router_version"] = "applied", 1
			}
			require.NoError(t, validateEvent(event, fields), "%s %s", event, kind)
		}
	}
	for _, disposition := range []string{"applied", "accepted_noop", "conflict", "duplicate", "stale"} {
		fields := map[string]any{"lifecycle_event": "terminal", "disposition": disposition, "router_version": 1}
		for _, event := range []string{"lifecycle_persisted", "lifecycle_acknowledged"} {
			require.NoError(t, validateEvent(event, fields), "%s", disposition)
		}
	}
	for _, event := range []string{"lifecycle_ingested", "lifecycle_persisted", "lifecycle_acknowledged"} {
		require.Error(t, validateEvent(event, map[string]any{"lifecycle_event": "arbitrary"}))
	}
	require.Error(t, validateEvent("lifecycle_persisted", map[string]any{"disposition": "arbitrary"}))
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

func TestManagedLogWriterPreservesBoundedTextExactly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "router.jsonl")
	writer, err := newLogWriter(path)
	require.NoError(t, err)
	body := "line one\r\nline two\n雪"
	require.NoError(t, writer.LogManagedEvent(managed.Event{
		Kind: managed.EventManagedMailboxIncoming, AgentID: "agent", MessageID: "message", OperationID: "operation",
		Subject: "subject\r\n雪", Body: body, ErrorText: "error\r\n雪",
	}))
	require.NoError(t, writer.Close())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var record map[string]any
	require.NoError(t, json.Unmarshal(data, &record))
	require.Equal(t, "subject\r\n雪", record["subject"])
	require.Equal(t, body, record["body"])
	require.Equal(t, "error\r\n雪", record["error_text"])
}

func TestManagedLogWriterEnforcesFieldBoundsWithoutTruncating(t *testing.T) {
	path := filepath.Join(t.TempDir(), "router.jsonl")
	writer, err := newLogWriter(path)
	require.NoError(t, err)
	require.NoError(t, writer.LogManagedEvent(managed.Event{Kind: managed.EventManagedProcess, AgentID: strings.Repeat("a", maxEventAgentMessageIDBytes), OperationID: strings.Repeat("o", maxEventIdentifierBytes), ErrorText: strings.Repeat("e", maxEventErrorBytes), Content: strings.Repeat("c", protocol.AbsoluteMaxBodyBytes)}))
	require.Error(t, writer.LogManagedEvent(managed.Event{Kind: managed.EventManagedProcess, AgentID: strings.Repeat("a", maxEventAgentMessageIDBytes+1)}))
	require.Error(t, writer.LogManagedEvent(managed.Event{Kind: managed.EventManagedProcess, OperationID: strings.Repeat("o", maxEventIdentifierBytes+1)}))
	require.Error(t, writer.LogManagedEvent(managed.Event{Kind: managed.EventManagedProcess, ErrorText: strings.Repeat("e", maxEventErrorBytes+1)}))
	require.Error(t, writer.LogManagedEvent(managed.Event{Kind: managed.EventManagedProcess, Content: strings.Repeat("c", protocol.AbsoluteMaxBodyBytes+1)}))
	require.NoError(t, writer.Close())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Len(t, strings.Split(strings.TrimSpace(string(data)), "\n"), 1)
}

func TestManagedLogWriterRedactsTextAndRegisteredSecretsBeforeJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "router.jsonl")
	writer, err := newLogWriterWithKnownSecrets(path, []string{"synthetic-secret", "synthetic"})
	require.NoError(t, err)
	text := "Bearer synthetic-secret access_token=synthetic-secret"
	require.NoError(t, writer.LogManagedEvent(managed.Event{Kind: managed.EventManagedRPC, Message: text, ErrorText: "password=synthetic-secret"}))
	require.NoError(t, writer.Log("router_error", map[string]any{"error_code": "internal_error", "password": "synthetic-secret"}))
	require.NoError(t, writer.Close())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(data), "synthetic-secret")
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var record map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &record))
	require.Equal(t, "Bearer [REDACTED] access_token=[REDACTED]", record["message"])
	require.Equal(t, "password=[REDACTED]", record["error_text"])
	var keyRecord map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &keyRecord))
	require.Equal(t, redactedValue, keyRecord["password"])
	require.Equal(t, map[string]any{"password": redactedValue}, redactLogFields(map[string]any{"password": "synthetic-secret"}, nil))
}

func TestLogWriterRedactionIsIdempotentAndPreservesMixedSecrets(t *testing.T) {
	knownSecrets := []string{"synthetic-secret", "synthetic"}
	input := "before Bearer bearer-token access_token=access-token known=synthetic-secret marker=[REDACTED] after"
	redacted := redactLogText(input, knownSecrets)
	require.Equal(t, "before Bearer [REDACTED] access_token=[REDACTED] known=[REDACTED] marker=[REDACTED] after", redacted)
	require.Equal(t, redacted, redactLogText(redacted, knownSecrets))
	require.Equal(t, "[REDACTED]", redactLogText("[REDACTED]", []string{"[REDACTED]", "REDACTED"}))
}

func TestLogWriterRedactionPreservesOrdinaryTextAndDelimiters(t *testing.T) {
	ordinary := "secret sauce; cookie-cutter; access-tokenized; password reset"
	redacted := redactLogText("Bearer synthetic-token, next", []string{"synthetic-token"})
	require.Equal(t, "Bearer [REDACTED], next", redacted)
	require.Equal(t, ordinary, redactLogText(ordinary, nil))

	fields := redactLogFields(map[string]any{
		"accessToken": "synthetic-access-token",
		"api_key":     "synthetic-api-key",
		"message":     ordinary,
	}, nil)
	require.Equal(t, redactedValue, fields["accessToken"])
	require.Equal(t, redactedValue, fields["api_key"])
	require.Equal(t, ordinary, fields["message"])
}

func TestManagedEventAdapterFailureIsIsolated(t *testing.T) {
	writer, err := newLogWriter(filepath.Join(t.TempDir(), "router.jsonl"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	var output strings.Builder
	require.NotPanics(t, func() {
		managedEventLogger(writer, &output)(managed.Event{Kind: managed.EventManagedRecovery, Status: "failed"})
	})
	require.Contains(t, output.String(), "managed event log write failed")
}
