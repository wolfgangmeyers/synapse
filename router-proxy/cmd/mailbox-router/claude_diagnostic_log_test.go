package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
	"github.com/stretchr/testify/require"
)

func TestClaudeDiagnosticBlockedWriterDoesNotBlockNormalLog(t *testing.T) {
	dir := t.TempDir()
	normal, err := newLogWriter(filepath.Join(dir, "router.jsonl"))
	require.NoError(t, err)
	defer normal.Close()
	diagnostic, err := newLogWriter(claudeDiagnosticLogPath(filepath.Join(dir, "router.jsonl")))
	require.NoError(t, err)
	defer diagnostic.Close()
	diagnostic.mu.Lock()
	sink := claudeDiagnosticSinkWithFactory(func() (*logWriter, error) { return diagnostic, nil })
	done := make(chan struct{})
	go func() {
		sink(managed.ClaudeProtocolDiagnostic{Reason: "result_with_pending_input", Pending: 1, Recent: "result"})
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("diagnostic writer should be blocked")
	case <-time.After(25 * time.Millisecond):
	}
	normalDone := make(chan error, 1)
	go func() { normalDone <- normal.Log("router_started", nil) }()
	select {
	case err := <-normalDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("normal log blocked behind diagnostic writer")
	}
	diagnostic.mu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("diagnostic did not finish")
	}
}

func TestClaudeDiagnosticLogIndependentRetention(t *testing.T) {
	dir := t.TempDir()
	path := claudeDiagnosticLogPath(filepath.Join(dir, "router.jsonl"))
	now := time.Now().UTC().Truncate(time.Millisecond)
	old := path + "." + now.Add(-logRetentionAge-time.Hour).Format(rotationLayout)
	require.NoError(t, os.WriteFile(old, []byte("old"), 0600))
	require.NoError(t, os.WriteFile(path, []byte(strings.Repeat("x", maxLogBytes+1)), 0600))
	writer, err := newLogWriterAt(path, func() time.Time { return now })
	require.NoError(t, err)
	defer writer.Close()
	sink := claudeDiagnosticSinkWithFactory(func() (*logWriter, error) { return writer, nil })
	sink(managed.ClaudeProtocolDiagnostic{Reason: "replay_content_mismatch", Pending: 1, Recent: "user_replay"})
	_, err = os.Stat(old)
	require.ErrorIs(t, err, os.ErrNotExist)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.LessOrEqual(t, len(data), maxLogBytes)
	require.Contains(t, string(data), "replay_content_mismatch")
	now = now.Add(logRotationAge)
	sink(managed.ClaudeProtocolDiagnostic{Reason: "result_with_pending_input", Pending: 1, Recent: "result"})
	entries, err := filepath.Glob(path + ".*")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	data, err = os.ReadFile(entries[0])
	require.NoError(t, err)
	require.Contains(t, string(data), "replay_content_mismatch")
}

func TestClaudeDiagnosticRotationDoesNotPolluteRouterLogReader(t *testing.T) {
	dir := t.TempDir()
	normalPath := filepath.Join(dir, "router.jsonl")
	normal, err := newLogWriter(normalPath)
	require.NoError(t, err)
	defer normal.Close()
	require.NoError(t, normal.Log("router_started", nil))
	now := time.Now().UTC().Truncate(time.Millisecond)
	diagnosticPath := claudeDiagnosticLogPath(normalPath)
	writer, err := newLogWriterAt(diagnosticPath, func() time.Time { return now })
	require.NoError(t, err)
	defer writer.Close()
	sink := claudeDiagnosticSinkWithFactory(func() (*logWriter, error) { return writer, nil })
	reader, err := transport.NewConfiguredRouterLogReader(normalPath)
	require.NoError(t, err)
	assertHealthy := func() {
		response, err := reader.ReadRouterLog(context.Background(), transport.RouterLogQuery{})
		require.NoError(t, err)
		require.Equal(t, "complete", response.Status)
		require.Empty(t, response.Diagnostics)
		require.Len(t, response.Lines, 1)
	}
	sink(managed.ClaudeProtocolDiagnostic{Reason: "replay_content_mismatch", Pending: 1, Recent: "user_replay"})
	assertHealthy()
	now = now.Add(logRotationAge)
	sink(managed.ClaudeProtocolDiagnostic{Reason: "result_with_pending_input", Pending: 1, Recent: "result"})
	rotations, err := filepath.Glob(diagnosticPath + ".*")
	require.NoError(t, err)
	require.Len(t, rotations, 1)
	assertHealthy()
}
