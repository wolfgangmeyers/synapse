package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
)

type tmuxCommandRunner func(context.Context, ...string) (string, error)

type tmuxAdapter struct {
	run tmuxCommandRunner
}

func newTmuxAdapter(binary string) *tmuxAdapter {
	return &tmuxAdapter{run: func(ctx context.Context, args ...string) (string, error) {
		output, err := exec.CommandContext(ctx, binary, args...).Output()
		if err == nil {
			return strings.TrimSpace(string(output)), nil
		}
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
			return "", fmt.Errorf("%w: %v", transport.ErrTmuxUnavailable, err)
		}
		return "", err
	}}
}

func (adapter *tmuxAdapter) Sessions(ctx context.Context) ([]string, error) {
	output, err := adapter.run(ctx, "list-sessions", "-F", "#{session_name}")
	if errors.Is(err, transport.ErrTmuxUnavailable) {
		// tmux uses exit 1 for an empty server. It is an empty inventory, so the
		// transport can report its normal unavailable selection state.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return splitTmuxLines(output), nil
}

func (adapter *tmuxAdapter) Windows(ctx context.Context, session string) ([]transport.TmuxWindow, error) {
	output, err := adapter.run(ctx, "list-windows", "-t", session, "-F", "#{window_name}")
	if err != nil {
		return nil, err
	}
	names := splitTmuxLines(output)
	windows := make([]transport.TmuxWindow, 0, len(names))
	for _, name := range names {
		windows = append(windows, transport.TmuxWindow{Name: name, Target: session + ":" + name})
	}
	return windows, nil
}

func (adapter *tmuxAdapter) Provision(ctx context.Context, session, name string) error {
	_, err := adapter.run(ctx, "new-window", "-d", "-t", session, "-n", name)
	return err
}

func (adapter *tmuxAdapter) Remove(ctx context.Context, session, name string) error {
	_, err := adapter.run(ctx, "kill-window", "-t", session+":"+name)
	return err
}

func (adapter *tmuxAdapter) Rename(ctx context.Context, session, name, nextName string) error {
	_, err := adapter.run(ctx, "rename-window", "-t", session+":"+name, nextName)
	return err
}

func (adapter *tmuxAdapter) SendKeys(ctx context.Context, session, name, keys, _ string) error {
	_, err := adapter.run(ctx, "send-keys", "-t", session+":"+name, "-l", keys)
	return err
}

func (adapter *tmuxAdapter) Start(ctx context.Context, session, name, sessionID string) (string, string, error) {
	args := []string{"send-keys", "-t", session + ":" + name, "start"}
	if sessionID != "" {
		args = append(args, sessionID)
	}
	args = append(args, "Enter")
	_, err := adapter.run(ctx, args...)
	return name, "started", err
}

func (adapter *tmuxAdapter) Stop(ctx context.Context, session, name, _ string) (string, string, error) {
	_, err := adapter.run(ctx, "send-keys", "-t", session+":"+name, "C-c")
	return name, "stopped", err
}

func (adapter *tmuxAdapter) Restart(ctx context.Context, session, name, sessionID string) (string, string, error) {
	if _, _, err := adapter.Stop(ctx, session, name, sessionID); err != nil {
		return name, "", err
	}
	return adapter.Start(ctx, session, name, sessionID)
}

func (adapter *tmuxAdapter) Resume(ctx context.Context, session, name, sessionID string) (string, string, error) {
	_, err := adapter.run(ctx, "send-keys", "-t", session+":"+name, "resume", sessionID, "Enter")
	return name, "resumed", err
}

func splitTmuxLines(output string) []string {
	if output == "" {
		return nil
	}
	return strings.Split(output, "\n")
}
