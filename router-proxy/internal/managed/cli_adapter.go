package managed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CLIProcessConfig describes the process boundary parameters passed to a CLIAdapter.
type CLIProcessConfig struct {
	Binary           string
	Cwd              string
	SessionID        string
	Model            string
	MCPConfigFile    string
	SystemPromptFile string
	RootAuthority    bool
	ChildAuthority   bool
	Resume           bool
	Environment      []string
}

// UserTurnInput describes user turn inputs to format for the CLI stdin.
type UserTurnInput struct {
	ID        string          `json:"id"`
	SessionID string          `json:"session_id"`
	Content   json.RawMessage `json:"content"`
}

// NormalizedEvent preserves unified event information translated from raw CLI output.
type NormalizedEvent struct {
	Type            string          `json:"type"`
	Subtype         string          `json:"subtype"`
	SessionID       string          `json:"session_id"`
	UUID            string          `json:"uuid"`
	IsReplay        bool            `json:"isReplay"`
	ParentToolUseID *string         `json:"parent_tool_use_id"`
	Message         json.RawMessage `json:"message"`
	Control         *CLIControlResponse `json:"control,omitempty"`
	Raw             json.RawMessage `json:"-"`
}

// CLIControlResponse represents a control response from a CLI process.
type CLIControlResponse struct {
	Subtype   string          `json:"subtype"`
	RequestID string          `json:"request_id"`
	Response  json.RawMessage `json:"response"`
	Error     string          `json:"error"`
}

// CLIAdapter defines the process management, flags, event parsing, and settings
// abstraction for an external CLI agent (e.g. Claude Code, Antigravity).
type CLIAdapter interface {
	// Provider returns the canonical provider string ("claude-code" or "antigravity").
	Provider() string

	// Executable resolves or validates the executable path for the CLI.
	Executable(ctx context.Context) (string, error)

	// Preflight performs a bounded verification of the executable and authentication state.
	Preflight(ctx context.Context) error

	// BuildArgs constructs the command-line arguments for launching a worker process.
	BuildArgs(cfg CLIProcessConfig, settingsPath string) []string

	// CleanEnvironment sanitizes process environment variables.
	CleanEnvironment(env []string) []string

	// ParseLine parses a single line of NDJSON from stdout into a NormalizedEvent.
	ParseLine(line []byte, sessionID string) (NormalizedEvent, error)

	// FormatUserTurn formats a user turn into the JSON line bytes expected on stdin.
	FormatUserTurn(msg UserTurnInput) ([]byte, error)

	// EphemeralSettingsContent creates configuration JSON defining the loopback HTTP MCP server.
	EphemeralSettingsContent(mcpURL string, mcpToken string) ([]byte, error)
}

// --- ClaudeCodeAdapter Implementation ---

type ClaudeCodeAdapter struct {
	binaryOverride string
	env            []string
}

func NewClaudeCodeAdapter(binaryOverride string, env []string) *ClaudeCodeAdapter {
	return &ClaudeCodeAdapter{
		binaryOverride: binaryOverride,
		env:            env,
	}
}

func (a *ClaudeCodeAdapter) Provider() string {
	return "claude-code"
}

func (a *ClaudeCodeAdapter) CleanEnvironment(env []string) []string {
	return claudeCodeEnvironment(env)
}

func (a *ClaudeCodeAdapter) Executable(ctx context.Context) (string, error) {
	env := a.CleanEnvironment(a.env)
	cfg := ClaudeCodeProcessConfig{
		Binary:      a.binaryOverride,
		Environment: env,
	}
	return resolveClaudeCodeBinary(cfg, env)
}

func (a *ClaudeCodeAdapter) Preflight(ctx context.Context) error {
	bin, err := a.Executable(ctx)
	if err != nil {
		return fmt.Errorf("install Claude CLI and sign in on the host: %w", err)
	}
	pCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(pCtx, bin, "auth", "status", "--json")
	cmd.Env = a.CleanEnvironment(a.env)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return errors.New("Claude CLI unavailable or signed out; run claude auth status and sign in on the host")
	}
	var status struct {
		LoggedIn *bool `json:"loggedIn"`
	}
	if json.Unmarshal(out, &status) != nil || status.LoggedIn == nil || !*status.LoggedIn {
		return errors.New("Claude CLI unavailable or signed out; run claude auth status and sign in on the host")
	}
	return nil
}

func (a *ClaudeCodeAdapter) BuildArgs(cfg CLIProcessConfig, settingsPath string) []string {
	claudeCfg := ClaudeCodeProcessConfig{
		Binary:           cfg.Binary,
		Cwd:              cfg.Cwd,
		SessionID:        cfg.SessionID,
		Model:            cfg.Model,
		MCPConfigFile:    cfg.MCPConfigFile,
		SystemPromptFile: cfg.SystemPromptFile,
		RootAuthority:    cfg.RootAuthority,
		ChildAuthority:   cfg.ChildAuthority,
		Resume:           cfg.Resume,
	}
	if settingsPath != "" {
		claudeCfg.MCPConfigFile = settingsPath
	}
	return claudeCodeProcessArgs(claudeCfg)
}

func (a *ClaudeCodeAdapter) ParseLine(line []byte, sessionID string) (NormalizedEvent, error) {
	event, ctrl, err := parseClaudeCodeEvent(line, sessionID)
	if err != nil {
		return NormalizedEvent{}, err
	}
	norm := NormalizedEvent{
		Type:            event.Type,
		Subtype:         event.Subtype,
		SessionID:       event.SessionID,
		UUID:            event.UUID,
		IsReplay:        event.IsReplay,
		ParentToolUseID: event.ParentToolUseID,
		Message:         event.Message,
		Raw:             event.Raw,
	}
	if ctrl != nil {
		norm.Control = &CLIControlResponse{
			Subtype:   ctrl.Subtype,
			RequestID: ctrl.RequestID,
			Response:  ctrl.Response,
			Error:     ctrl.Error,
		}
	}
	return norm, nil
}

func (a *ClaudeCodeAdapter) FormatUserTurn(msg UserTurnInput) ([]byte, error) {
	if msg.ID == "" || !claudeCodeValidContent(msg.Content) {
		return nil, errors.New("Claude input requires ID and valid content")
	}
	payload := map[string]interface{}{
		"type":       "user",
		"uuid":       msg.ID,
		"session_id": msg.SessionID,
		"message": map[string]interface{}{
			"role":    "user",
			"content": msg.Content,
		},
	}
	return json.Marshal(payload)
}

func (a *ClaudeCodeAdapter) EphemeralSettingsContent(mcpURL string, mcpToken string) ([]byte, error) {
	config := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"mecha": map[string]interface{}{
				"type": "http",
				"url":  mcpURL,
				"headers": map[string]string{
					"Authorization": "Bearer " + mcpToken,
				},
			},
		},
	}
	return json.Marshal(config)
}

// --- AntigravityAdapter Implementation ---

type AntigravityAdapter struct {
	binaryOverride string
	env            []string
}

func NewAntigravityAdapter(binaryOverride string, env []string) *AntigravityAdapter {
	return &AntigravityAdapter{
		binaryOverride: binaryOverride,
		env:            env,
	}
}

func (a *AntigravityAdapter) Provider() string {
	return "antigravity"
}

// CleanEnvironment strips environment variables that could cause nested session or
// process interference. TMUX and TMUX_PANE are stripped to prevent the child CLI from
// attaching to or inheriting parent tmux sessions, while CLAUDECODE and
// CLAUDE_CODE_ENTRYPOINT are removed to prevent child CLI processes from detecting a
// recursive execution context.
func (a *AntigravityAdapter) CleanEnvironment(env []string) []string {
	if env == nil {
		env = os.Environ()
	}
	clean := make([]string, 0, len(env))
	for _, s := range env {
		k, _, _ := strings.Cut(s, "=")
		switch k {
		case "TMUX", "TMUX_PANE", "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT":
			continue
		}
		clean = append(clean, s)
	}
	return clean
}

func (a *AntigravityAdapter) Executable(ctx context.Context) (string, error) {
	env := a.CleanEnvironment(a.env)
	candidate := a.binaryOverride
	if candidate == "" {
		candidate = claudeCodeEnvValue(env, "MECHA_AGY_BIN")
	}
	executable := func(p string) (string, error) {
		p, err := filepath.Abs(p)
		if err != nil {
			return "", err
		}
		st, err := os.Stat(p)
		if err != nil {
			return "", err
		}
		if !st.Mode().IsRegular() || st.Mode().Perm()&0111 == 0 {
			return "", fmt.Errorf("not executable: %s", p)
		}
		return p, nil
	}
	if candidate != "" {
		if strings.ContainsRune(candidate, os.PathSeparator) {
			return executable(candidate)
		}
		for _, dir := range filepath.SplitList(claudeCodeEnvValue(env, "PATH")) {
			if p, e := executable(filepath.Join(dir, candidate)); e == nil {
				return p, nil
			}
		}
		return "", fmt.Errorf("Antigravity executable %q not found", candidate)
	}
	for _, dir := range filepath.SplitList(claudeCodeEnvValue(env, "PATH")) {
		if p, e := executable(filepath.Join(dir, "agy")); e == nil {
			return p, nil
		}
	}
	home := claudeCodeEnvValue(env, "HOME")
	if home != "" {
		if p, e := executable(filepath.Join(home, ".local", "bin", "agy")); e == nil {
			return p, nil
		}
	}
	return "", errors.New("Antigravity executable not found; install agy and ensure it is in PATH or ~/.local/bin")
}

// Preflight verifies that the Antigravity CLI binary exists and is authenticated.
// It executes `agy --version` to validate executable sanity, then probes `agy models`
// to check the host authentication state. If the output indicates unauthenticated status
// (e.g. "Please sign in"), an error is returned directing the user to sign in on the host.
func (a *AntigravityAdapter) Preflight(ctx context.Context) error {
	bin, err := a.Executable(ctx)
	if err != nil {
		return fmt.Errorf("Antigravity CLI unavailable or signed out; run agy to sign in on the host: %w", err)
	}
	pCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// 1. Run agy --version
	verCmd := exec.CommandContext(pCtx, bin, "--version")
	verCmd.Env = a.CleanEnvironment(a.env)
	if err := verCmd.Run(); err != nil {
		return fmt.Errorf("Antigravity CLI unavailable or signed out; run agy to sign in on the host: %w", err)
	}

	// 2. Run agy models to check authentication state
	modCmd := exec.CommandContext(pCtx, bin, "models")
	modCmd.Env = a.CleanEnvironment(a.env)
	out, err := modCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("Antigravity CLI unavailable or signed out; run agy to sign in on the host: %s", strings.TrimSpace(string(out)))
	}
	if strings.Contains(string(out), "Please sign in") {
		return errors.New("Antigravity CLI unavailable or signed out; run agy to sign in on the host")
	}

	return nil
}

// BuildArgs constructs the CLI arguments for running Antigravity in headless stream-json mode.
// The `--dangerously-skip-permissions` flag is required because managed router agents run
// unattended in headless background processes where interactive permission prompts cannot be
// answered; permission checks and security boundaries are enforced by the router harness itself.
func (a *AntigravityAdapter) BuildArgs(cfg CLIProcessConfig, settingsPath string) []string {
	args := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--dangerously-skip-permissions",
	}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	if cfg.SessionID != "" {
		args = append(args, "--conversation", cfg.SessionID)
	}
	if settingsPath != "" {
		args = append(args, "--settings", settingsPath)
	}
	return args
}

func (a *AntigravityAdapter) ParseLine(line []byte, sessionID string) (NormalizedEvent, error) {
	var raw struct {
		Type            string          `json:"type"`
		Subtype         string          `json:"subtype"`
		SessionID       string          `json:"session_id"`
		UUID            string          `json:"uuid"`
		IsReplay        bool            `json:"isReplay"`
		ParentToolUseID *string         `json:"parent_tool_use_id"`
		Message         json.RawMessage `json:"message"`
		Response        *CLIControlResponse `json:"response"`
	}
	if json.Unmarshal(line, &raw) != nil || raw.Type == "" {
		return NormalizedEvent{}, errors.New("malformed required Antigravity protocol record")
	}

	norm := NormalizedEvent{
		Type:            raw.Type,
		Subtype:         raw.Subtype,
		SessionID:       raw.SessionID,
		UUID:            raw.UUID,
		IsReplay:        raw.IsReplay,
		ParentToolUseID: raw.ParentToolUseID,
		Message:         raw.Message,
		Raw:             append(json.RawMessage(nil), line...),
	}

	if raw.Type == "control_response" && raw.Response != nil {
		norm.Control = raw.Response
	}

	return norm, nil
}

func (a *AntigravityAdapter) FormatUserTurn(msg UserTurnInput) ([]byte, error) {
	if msg.ID == "" || len(msg.Content) == 0 {
		return nil, errors.New("Antigravity input requires ID and valid content")
	}
	payload := map[string]interface{}{
		"type":       "user",
		"uuid":       msg.ID,
		"session_id": msg.SessionID,
		"message": map[string]interface{}{
			"role":    "user",
			"content": msg.Content,
		},
	}
	return json.Marshal(payload)
}

func (a *AntigravityAdapter) EphemeralSettingsContent(mcpURL string, mcpToken string) ([]byte, error) {
	config := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"mecha": map[string]interface{}{
				"url": mcpURL,
				"headers": map[string]string{
					"Authorization": "Bearer " + mcpToken,
				},
			},
		},
	}
	return json.Marshal(config)
}
