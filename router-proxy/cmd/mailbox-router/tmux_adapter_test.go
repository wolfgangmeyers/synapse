package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
	"github.com/stretchr/testify/require"
)

func TestTmuxAdapterUsesEstablishedCommands(t *testing.T) {
	var calls [][]string
	adapter := &tmuxAdapter{run: func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		switch args[0] {
		case "list-sessions":
			return "mecha\nother", nil
		case "if-shell":
			return "supervisor\t@1\t0123456789abcdef0123456789abcdef\nworker\t@2\t0123456789abcdef0123456789abcdef", nil
		case "list-windows":
			return "0\n1", nil
		case "show-options":
			return "0", nil
		default:
			return "", nil
		}
	}}

	sessions, err := adapter.Sessions(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"mecha", "other"}, sessions)
	windows, err := adapter.Windows(context.Background(), "mecha")
	require.NoError(t, err)
	require.Equal(t, []transport.TmuxWindow{{Name: "supervisor", Target: "mecha:supervisor", WindowID: "@1", ServerID: "0123456789abcdef0123456789abcdef"}, {Name: "worker", Target: "mecha:worker", WindowID: "@2", ServerID: "0123456789abcdef0123456789abcdef"}}, windows)
	require.NoError(t, adapter.ProvisionSession(context.Background(), "mecha", "supervisor"))
	require.NoError(t, adapter.Provision(context.Background(), "mecha", "worker"))
	require.NoError(t, adapter.Rename(context.Background(), "mecha", "worker", "renamed"))
	require.NoError(t, adapter.SendKeys(context.Background(), "mecha", "worker", "echo hi", "ignored"))
	_, state, err := adapter.Restart(context.Background(), "mecha", "worker", "pi-session")
	require.NoError(t, err)
	require.Equal(t, "started", state)
	_, state, err = adapter.Resume(context.Background(), "mecha", "worker", "pi-session")
	require.NoError(t, err)
	require.Equal(t, "resumed", state)
	require.NoError(t, adapter.Remove(context.Background(), "mecha", "worker"))

	require.Len(t, calls, 12)
	require.Equal(t, []string{"list-sessions", "-F", "#{session_name}"}, calls[0])
	require.Equal(t, "if-shell", calls[1][0])
	require.Equal(t, []string{";", "list-windows", "-t", "mecha", "-F", "#{window_name}\t#{window_id}\t#{@mecha_server_id}"}, calls[1][5:])
	require.Equal(t, [][]string{
		{"new-session", "-d", "-s", "mecha", "-n", "supervisor"},
		{"show-options", "-t", "mecha", "-v", "base-index"},
		{"list-windows", "-t", "mecha", "-F", "#{window_index}"},
		{"new-window", "-d", "-t", "mecha:2", "-n", "worker"},
		{"rename-window", "-t", "mecha:worker", "renamed"},
		{"send-keys", "-t", "mecha:worker", "-l", "echo hi"},
		{"send-keys", "-t", "mecha:worker", "C-c"},
		{"send-keys", "-t", "mecha:worker", "start", "pi-session", "Enter"},
		{"send-keys", "-t", "mecha:worker", "resume", "pi-session", "Enter"},
		{"kill-window", "-t", "mecha:worker"},
	}, calls[2:])
}

func TestTmuxAdapterProvisionUsesFirstFreeIndexAtSessionBase(t *testing.T) {
	var calls [][]string
	adapter := &tmuxAdapter{run: func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		switch args[0] {
		case "show-options":
			return "28", nil
		case "list-windows":
			return "28\n29\n31", nil
		default:
			return "", nil
		}
	}}

	require.NoError(t, adapter.Provision(context.Background(), "mecha", "worker"))
	require.Equal(t, [][]string{
		{"show-options", "-t", "mecha", "-v", "base-index"},
		{"list-windows", "-t", "mecha", "-F", "#{window_index}"},
		{"new-window", "-d", "-t", "mecha:30", "-n", "worker"},
	}, calls)
}

func TestNewTmuxAdapterNormalizesExitOneAndPreservesOtherFailures(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "tmux")
	require.NoError(t, os.WriteFile(binary, []byte("#!/bin/sh\ncase \"$1\" in\nlist-sessions|list-windows) exit 1 ;;\n*) exit 2 ;;\nesac\n"), 0o700))
	adapter := newTmuxAdapter(binary)

	sessions, err := adapter.Sessions(context.Background())
	require.NoError(t, err)
	require.Empty(t, sessions)
	_, err = adapter.Windows(context.Background(), "mecha")
	require.ErrorIs(t, err, transport.ErrTmuxUnavailable)
	err = adapter.Provision(context.Background(), "mecha", "worker")
	require.Error(t, err)
	require.NotErrorIs(t, err, transport.ErrTmuxUnavailable)
}

// A restarted server can recycle @0. Old identities must not send even when
// the replacement has the same name and ID.
func TestTmuxServerRestartFencesReusedWindowID(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "tmux.sock")
	tmux := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", append([]string{"-S", socket}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	_, err := tmux("new-session", "-d", "-s", "mecha", "-n", "worker")
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = tmux("kill-server") })
	adapter := &tmuxAdapter{run: func(_ context.Context, args ...string) (string, error) { return tmux(args...) }}
	first, err := adapter.Windows(context.Background(), "mecha")
	require.NoError(t, err)
	require.Len(t, first, 1)
	_, err = tmux("kill-server")
	require.NoError(t, err)
	_, err = tmux("new-session", "-d", "-s", "mecha", "-n", "worker")
	require.NoError(t, err)
	second, err := adapter.Windows(context.Background(), "mecha")
	require.NoError(t, err)
	require.Len(t, second, 1)
	require.Equal(t, first[0].WindowID, second[0].WindowID)
	require.NotEqual(t, first[0].ServerID, second[0].ServerID)
	require.Error(t, adapter.SendKeysToWindowID(context.Background(), first[0].WindowID, first[0].ServerID, "must-not-arrive"))
	require.NoError(t, adapter.SendKeysToWindowID(context.Background(), second[0].WindowID, second[0].ServerID, "safe"))
}
