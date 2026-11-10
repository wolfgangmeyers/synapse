package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
)

type tmuxCommandRunner func(context.Context, ...string) (string, error)

type tmuxAdapter struct {
	run         tmuxCommandRunner
	provisionMu sync.Mutex
}

func newTmuxAdapter(binary string) *tmuxAdapter {
	return &tmuxAdapter{run: func(ctx context.Context, args ...string) (string, error) {
		output, err := exec.CommandContext(ctx, binary, args...).Output()
		if err == nil {
			return strings.TrimSpace(string(output)), nil
		}
		return "", err
	}}
}

func (adapter *tmuxAdapter) Sessions(ctx context.Context) ([]string, error) {
	output, err := adapter.run(ctx, "list-sessions", "-F", "#{session_name}")
	if exitCode(err, 1) {
		// tmux uses exit 1 for an empty server. It is an empty inventory, so the
		// transport can report its normal unavailable selection state.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return splitTmuxLines(output), nil
}

func (adapter *tmuxAdapter) ProvisionSession(ctx context.Context, session, name string) error {
	_, err := adapter.run(ctx, "new-session", "-d", "-s", session, "-n", name)
	return err
}

func (adapter *tmuxAdapter) Windows(ctx context.Context, session string) ([]transport.TmuxWindow, error) {
	var seed [16]byte
	if _, err := rand.Read(seed[:]); err != nil {
		return nil, err
	}
	// Both commands execute in one tmux queue. The option is scoped to the tmux
	// server and disappears when it dies; concurrent routers do not replace it.
	output, err := adapter.run(ctx, "if-shell", "-F", "#{==:#{@mecha_server_id},}", "set-option -g @mecha_server_id "+hex.EncodeToString(seed[:]), "", ";", "list-windows", "-t", session, "-F", "#{window_name}\t#{window_id}\t#{@mecha_server_id}")
	if exitCode(err, 1) {
		return nil, fmt.Errorf("%w: %v", transport.ErrTmuxUnavailable, err)
	}
	if err != nil {
		return nil, err
	}
	names := splitTmuxLines(output)
	windows := make([]transport.TmuxWindow, 0, len(names))
	for _, line := range names {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 || !validWindowID(fields[1]) || !validServerID(fields[2]) {
			return nil, fmt.Errorf("invalid tmux window inventory")
		}
		windows = append(windows, transport.TmuxWindow{Name: fields[0], Target: session + ":" + fields[0], WindowID: fields[1], ServerID: fields[2]})
	}
	return windows, nil
}

func (adapter *tmuxAdapter) Provision(ctx context.Context, session, name string) error {
	// A session-only target resolves to its active window. Choose an unused index
	// explicitly so provisioning never collides with that unrelated window.
	adapter.provisionMu.Lock()
	defer adapter.provisionMu.Unlock()

	target, err := adapter.nextFreeWindowTarget(ctx, session)
	if err != nil {
		return err
	}
	_, err = adapter.run(ctx, "new-window", "-d", "-t", target, "-n", name)
	return err
}

func (adapter *tmuxAdapter) nextFreeWindowTarget(ctx context.Context, session string) (string, error) {
	base, err := adapter.windowBaseIndex(ctx, session)
	if err != nil {
		return "", err
	}
	output, err := adapter.run(ctx, "list-windows", "-t", session, "-F", "#{window_index}")
	if err != nil {
		return "", err
	}
	occupied := make(map[int]struct{})
	for _, rawIndex := range splitTmuxLines(output) {
		index, parseErr := strconv.Atoi(rawIndex)
		if parseErr != nil || index < 0 {
			return "", fmt.Errorf("invalid tmux window index %q", rawIndex)
		}
		occupied[index] = struct{}{}
	}
	for index := base; ; index++ {
		if _, exists := occupied[index]; !exists {
			return fmt.Sprintf("%s:%d", session, index), nil
		}
	}
}

func (adapter *tmuxAdapter) windowBaseIndex(ctx context.Context, session string) (int, error) {
	output, err := adapter.run(ctx, "show-options", "-t", session, "-v", "base-index")
	if err != nil {
		return 0, err
	}
	if output == "" {
		output, err = adapter.run(ctx, "show-options", "-g", "-v", "base-index")
		if err != nil {
			return 0, err
		}
	}
	index, err := strconv.Atoi(output)
	if err != nil || index < 0 {
		return 0, fmt.Errorf("invalid tmux base index %q", output)
	}
	return index, nil
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

// Tmux window IDs are server-unique and never reused during the server lifetime.
func validWindowID(id string) bool {
	if len(id) < 2 || id[0] != '@' {
		return false
	}
	for _, c := range id[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func validServerID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && strings.ToLower(id) == id
}

func (adapter *tmuxAdapter) SendKeysToWindowID(ctx context.Context, id, serverID, keys string) error {
	if !validWindowID(id) || !validServerID(serverID) || keys == "" {
		return fmt.Errorf("invalid tmux window identity")
	}
	// tmux evaluates the server token and executes send-keys in the same server
	// command queue. Hex encoding keeps arbitrary terminal bytes out of tmux syntax.
	bytes := []byte(keys)
	hexKeys := make([]string, len(bytes))
	for i, b := range bytes {
		hexKeys[i] = fmt.Sprintf("%02x", b)
	}
	command := "send-keys -t " + id + " -H " + strings.Join(hexKeys, " ")
	_, err := adapter.run(ctx, "if-shell", "-F", "#{==:#{@mecha_server_id},"+serverID+"}", command, "send-keys -t @999999999999999999999999999999 -H 00")
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

func exitCode(err error, code int) bool {
	var exitError *exec.ExitError
	return errors.As(err, &exitError) && exitError.ExitCode() == code
}

func splitTmuxLines(output string) []string {
	if output == "" {
		return nil
	}
	return strings.Split(output, "\n")
}
