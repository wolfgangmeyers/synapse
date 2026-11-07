package managed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"syscall"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
)

// Claude uses only the configured default. Pi defaults must construct an
// authenticated stream; Claude aliases are preflighted at execution.
func (m *Manager) claudeCodeEvaluatorModel(ctx context.Context) (*ai.Model, error) {
	unavailable := fmt.Errorf("Claude goal evaluation unavailable: configure an authenticated router default: %w", errClaudeEvaluatorMissingDefault)
	selected, err := m.store.GetDefaultModel(ctx)
	if err != nil || selected == nil {
		return nil, unavailable
	}
	return m.configuredGoalEvaluatorModel(ctx, *selected)
}

func (m *Manager) configuredGoalEvaluatorModel(ctx context.Context, selected store.ManagedModel) (*ai.Model, error) {
	unavailable := fmt.Errorf("Claude goal evaluation unavailable: configure an authenticated router default: %w", errClaudeEvaluatorMissingDefault)
	var err error
	if selected.Provider == claudeCodeProvider {
		alias, err := claudeCodeModel(selected.ID)
		if err != nil {
			return nil, unavailable
		}
		return &ai.Model{Provider: claudeCodeProvider, Api: claudeCodeProvider, ID: alias.ID, ContextWindow: alias.ContextWindow}, nil
	}
	if _, err = m.admitEmbeddedModel(ctx, Model{Provider: selected.Provider, ID: selected.ID}); err != nil {
		return nil, unavailable
	}
	model, err := ResolveModel(selected)
	if err != nil || model == nil || model.ContextWindow <= embeddedContextHeadroom {
		return nil, unavailable
	}
	authCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	switch model.Provider {
	case "google":
		_, err = m.resolveGoogleAPIKey(authCtx)
	case "google-vertex":
		_, _, _, err = m.embeddedVertexAuth(authCtx, false, model.ID)
	case "openai-codex":
		_, err = m.embeddedCodexAuth(authCtx)
	default:
		return nil, unavailable
	}
	if err != nil {
		return nil, unavailable
	}
	return model, nil
}

var (
	errClaudeEvaluatorProcess        = errors.New("CLI process failed")
	errClaudeEvaluatorOutput         = errors.New("output exceeded limit")
	errClaudeEvaluatorMissingDefault = errors.New("missing router default")
	errClaudeEvaluatorAuth           = errors.New("CLI sign-in preflight failed")
)

// Diagnostics are allowlisted; never publish CLI stderr, prompts or credentials.
func claudeEvaluatorDiagnostic(err error) string {
	reason := "evaluation failed"
	switch {
	case errors.Is(err, errClaudeEvaluatorMissingDefault):
		reason = "configure an authenticated router default"
	case errors.Is(err, context.DeadlineExceeded):
		reason = "evaluation timed out"
	case errors.Is(err, context.Canceled):
		reason = "evaluation canceled"
	case errors.Is(err, errClaudeEvaluatorAuth):
		reason = "CLI unavailable or signed out; install Claude and sign in on the router host"
	case errors.Is(err, errClaudeEvaluatorOutput):
		reason = "evaluator output exceeded limit"
	case errors.Is(err, errClaudeEvaluatorProcess):
		reason = "CLI process failed; check local CLI configuration and managed MCP policy"
	}
	return "Claude goal evaluation unavailable: " + reason + "; continuation stopped"
}

// The model receives no built-in tools or MCP servers. This is not an OS
// sandbox and does not isolate trusted local hooks, plugins, or managed policy.
func runClaudeGoalEvaluator(ctx context.Context, cfg ClaudeCodeProcessConfig, model, prompt string) (string, error) {
	if cfg.admitExternal != nil {
		release, err := cfg.admitExternal()
		if err != nil {
			return "", err
		}
		defer release()
		// The outer admission covers preflight as well as the evaluator.
		cfg.admitExternal = nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	bin, err := PreflightClaudeCode(ctx, cfg)
	if err != nil {
		if errors.Is(err, errClaudeCodeGroupUnconfirmed) {
			return "", err
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("%w: %v", errClaudeEvaluatorAuth, err)
	}
	cmd, gate, err := claudeCodeGatedCommand(bin, []string{"-p", "--model", model, "--tools", "", "--strict-mcp-config", "--disable-slash-commands", "--no-session-persistence", "--system-prompt", "Return only the requested goal verdict."})
	if err != nil {
		return "", err
	}
	defer gate.Close()
	defer cmd.ExtraFiles[0].Close()
	cmd.Dir = cfg.Cwd
	cmd.Env = claudeCodeEnvironment(cfg.Environment)
	cmd.Stdin = strings.NewReader(prompt)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	output := &claudeCodeBoundedCapture{limit: 64 << 10}
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	var orphanPath string
	if err = ctx.Err(); err == nil {
		orphanPath, err = startClaudeShortCommand(cmd, gate, cfg)
	}
	if err != nil {
		if errors.Is(err, errClaudeCodeGroupUnconfirmed) {
			return "", err
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.Join(errClaudeEvaluatorProcess, err)
	}
	err = waitClaudeShortCommand(ctx, cmd, cfg, orphanPath)
	if ctx.Err() != nil && !errors.Is(err, errClaudeCodeGroupUnconfirmed) {
		return "", ctx.Err()
	}
	if err != nil {
		return "", errors.Join(errClaudeEvaluatorProcess, err)
	}
	if output.overflow {
		return "", errClaudeEvaluatorOutput
	}
	return string(output.data), nil
}

// claudeShortCommandConfig shares the managed process recovery fence.
func (m *Manager) claudeShortCommandConfig(cwd string) ClaudeCodeProcessConfig {
	return ClaudeCodeProcessConfig{Cwd: cwd, OrphanRoot: m.config.SessionRoot, OnUnconfirmedStop: m.claudeCodeProcesses.fence, admitExternal: m.claudeCodeProcesses.admitExternal, releaseGate: m.claudeCodeProcesses.releaseGate}
}
