package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// These are current Go contracts, not promises about the removed JS event schema.
func TestOrphanLoggerCurrentCorrelationAndPrivacy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "router.jsonl")
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	writer, err := newLogWriterAt(path, func() time.Time { return now })
	require.NoError(t, err)
	fields := map[string]any{"message_id": "message", "agent_id": "receiver", "attempt": 1, "state": "queued", "reason": "delivery_timeout"}
	require.NoError(t, writer.Log("delivery_timed_out", fields))
	fields["body"] = "private"
	require.Error(t, writer.Log("delivery_timed_out", fields))
	delete(fields, "body")
	fields["model"] = map[string]any{"id": "private"}
	require.Error(t, writer.Log("delivery_timed_out", fields))
	require.NoError(t, writer.Close())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 1)
	var record map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &record))
	require.Equal(t, map[string]any{"timestamp": "2030-01-01T00:00:00.000Z", "event": "delivery_timed_out", "message_id": "message", "agent_id": "receiver", "attempt": float64(1), "state": "queued", "reason": "delivery_timeout"}, record)
}

func TestOrphanLoggerBelowLimitKeepsNewestRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "router.jsonl")
	now := time.Now().UTC()
	writer, err := newLogWriterAt(path, func() time.Time { return now })
	require.NoError(t, err)
	require.NoError(t, writer.Log("router_started", nil))
	require.NoError(t, writer.Log("router_stopped", nil))
	require.NoError(t, writer.Close())
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 2)
	require.Contains(t, lines[1], `"event":"router_stopped"`)
	require.Equal(t, 16*1024*1024, maxLogBytes)
}
