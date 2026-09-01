package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
		case "list-windows":
			return "supervisor\nworker", nil
		default:
			return "", nil
		}
	}}

	sessions, err := adapter.Sessions(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"mecha", "other"}, sessions)
	windows, err := adapter.Windows(context.Background(), "mecha")
	require.NoError(t, err)
	require.Equal(t, []transport.TmuxWindow{{Name: "supervisor", Target: "mecha:supervisor"}, {Name: "worker", Target: "mecha:worker"}}, windows)
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

	require.Equal(t, [][]string{
		{"list-sessions", "-F", "#{session_name}"},
		{"list-windows", "-t", "mecha", "-F", "#{window_name}"},
		{"new-window", "-d", "-t", "mecha", "-n", "worker"},
		{"rename-window", "-t", "mecha:worker", "renamed"},
		{"send-keys", "-t", "mecha:worker", "-l", "echo hi"},
		{"send-keys", "-t", "mecha:worker", "C-c"},
		{"send-keys", "-t", "mecha:worker", "start", "pi-session", "Enter"},
		{"send-keys", "-t", "mecha:worker", "resume", "pi-session", "Enter"},
		{"kill-window", "-t", "mecha:worker"},
	}, calls)
}

func TestNewTmuxAdapterNormalizesExitOneAndPreservesOtherFailures(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "tmux")
	require.NoError(t, os.WriteFile(binary, []byte("#!/bin/sh\ncase \"$1\" in\nlist-sessions) exit 1 ;;\n*) exit 2 ;;\nesac\n"), 0o700))
	adapter := newTmuxAdapter(binary)

	sessions, err := adapter.Sessions(context.Background())
	require.NoError(t, err)
	require.Empty(t, sessions)
	require.Error(t, adapter.Provision(context.Background(), "mecha", "worker"))
}

func TestTmuxAdapterTreatsAnEmptyServerAsNoSessions(t *testing.T) {
	adapter := &tmuxAdapter{run: func(context.Context, ...string) (string, error) {
		return "", fmt.Errorf("%w: empty server", transport.ErrTmuxUnavailable)
	}}
	sessions, err := adapter.Sessions(context.Background())
	require.NoError(t, err)
	require.Empty(t, sessions)
}
