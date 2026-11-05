package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
)

const managedRootControlRunner = "managed_root_control.py"

// mailboxRouterRootControlExecutor is the concrete host owner for managed
// root tools. The Python runner is deliberately fixed to the supervisor-tools
// directory; it receives a request ID and full fence so it can reconcile a
// prior external action instead of replaying an uncertain mutation.
type mailboxRouterRootControlExecutor struct {
	python, toolsDir, managedHome, routerURL string
}

type managedRootControlRunnerRequest struct {
	RequestID   string                             `json:"request_id"`
	ResultID    string                             `json:"result_id"`
	ToolCallID  string                             `json:"tool_call_id"`
	OperationID string                             `json:"operation_id"`
	Identity    managed.RootControlHandlerIdentity `json:"identity"`
	Arguments   any                                `json:"arguments"`
	Fence       managedRootControlFence            `json:"fence"`
}

type managedRootControlFence struct {
	AgentID            string `json:"agent_id"`
	PiSessionID        string `json:"pi_session_id"`
	SessionEpoch       int64  `json:"session_epoch"`
	ProcessPID         int64  `json:"process_pid"`
	ProcessStartMarker string `json:"process_start_marker"`
	ProcessIncarnation string `json:"process_incarnation"`
	RuntimeGeneration  string `json:"runtime_generation"`
	ManifestRevision   int64  `json:"manifest_revision"`
	ManifestHash       string `json:"manifest_hash"`
}

type managedRootControlRunnerResult struct {
	Status       string         `json:"status"`
	Content      string         `json:"content"`
	ErrorCode    string         `json:"error_code"`
	ErrorMessage string         `json:"error_message"`
	Details      map[string]any `json:"details"`
}

func newMailboxRouterRootControlExecutor(config managed.Config) (mailboxRouterRootControlExecutor, error) {
	if err := managed.ValidateManagedRouterURL(config.RouterURL); err != nil {
		return mailboxRouterRootControlExecutor{}, fmt.Errorf("managed root control router URL: %w", err)
	}
	toolsDir, err := supervisorToolsDirectory()
	if err != nil {
		return mailboxRouterRootControlExecutor{}, err
	}
	python := os.Getenv("PYTHON")
	if python == "" {
		python = "python3"
	}
	managedHome := config.ManagedHome
	if configured := os.Getenv("MECHA_MANAGED_ROOT_CONTROL_HOME"); configured != "" {
		managedHome = configured
	}
	if managedHome == "" {
		return mailboxRouterRootControlExecutor{}, errors.New("managed root control home is required")
	}
	return mailboxRouterRootControlExecutor{python: python, toolsDir: toolsDir, managedHome: managedHome, routerURL: config.RouterURL}, nil
}

func supervisorToolsDirectory() (string, error) {
	if configured := os.Getenv("MECHA_SUPERVISOR_TOOLS"); configured != "" {
		resolved, err := filepath.Abs(configured)
		if err != nil {
			return "", err
		}
		if info, err := os.Stat(filepath.Join(resolved, managedRootControlRunner)); err == nil && !info.IsDir() {
			return resolved, nil
		}
		return "", errors.New("MECHA_SUPERVISOR_TOOLS does not contain managed_root_control.py")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for _, root := range []string{cwd, filepath.Dir(cwd)} {
		for _, sub := range []string{"tools", filepath.Join(".claude", "skills", "supervisor", "tools")} {
			candidate := filepath.Join(root, sub)
			if info, statErr := os.Stat(filepath.Join(candidate, managedRootControlRunner)); statErr == nil && !info.IsDir() {
				return candidate, nil
			}
		}
	}
	return "", errors.New("managed root control runner is unavailable")
}

func (e mailboxRouterRootControlExecutor) ExecuteRootControl(ctx context.Context, identity managed.RootControlHandlerIdentity, invocation managed.RootControlInvocation) (managed.RootControlHandlerResult, error) {
	return e.run(ctx, "execute", identity, invocation)
}
func (e mailboxRouterRootControlExecutor) ReconcileRootControl(ctx context.Context, identity managed.RootControlHandlerIdentity, invocation managed.RootControlInvocation) (managed.RootControlHandlerResult, error) {
	return e.run(ctx, "reconcile", identity, invocation)
}

func (e mailboxRouterRootControlExecutor) ReconcileRetiredRootControl(ctx context.Context, identity managed.RootControlHandlerIdentity, invocation managed.RootControlInvocation) (managed.RootControlHandlerResult, error) {
	return e.run(ctx, "reconcile", identity, invocation)
}

func (e mailboxRouterRootControlExecutor) run(ctx context.Context, mode string, identity managed.RootControlHandlerIdentity, invocation managed.RootControlInvocation) (managed.RootControlHandlerResult, error) {
	if mode != "execute" && mode != "reconcile" {
		return managed.RootControlHandlerResult{}, errors.New("managed root control mode is invalid")
	}
	request := managedRootControlRunnerRequest{
		RequestID: invocation.RequestID, ResultID: invocation.ResultID, ToolCallID: invocation.ToolCallID, OperationID: invocation.OperationID,
		Identity: identity, Arguments: invocation.Arguments,
		Fence: managedRootControlFence{AgentID: invocation.Fence.AgentID, PiSessionID: invocation.Fence.PiSessionID, SessionEpoch: invocation.Fence.SessionEpoch, ProcessPID: int64(invocation.Fence.ProcessPID), ProcessStartMarker: invocation.Fence.ProcessStartMarker, ProcessIncarnation: invocation.Fence.ProcessIncarnation, RuntimeGeneration: invocation.Fence.RuntimeGeneration, ManifestRevision: int64(invocation.Fence.ManifestRevision), ManifestHash: invocation.Fence.ManifestHash},
	}
	input, err := json.Marshal(request)
	if err != nil {
		return managed.RootControlHandlerResult{}, err
	}
	if err := managed.ValidateManagedRouterURL(e.routerURL); err != nil {
		return managed.RootControlHandlerResult{}, fmt.Errorf("managed root control router URL: %w", err)
	}
	command := exec.CommandContext(ctx, e.python, filepath.Join(e.toolsDir, managedRootControlRunner), mode, "--home", e.managedHome)
	command.Env = managedRootControlEnvironment(e.routerURL)
	command.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if detail == "" {
			detail = "managed root control runner failed"
		}
		return managed.RootControlHandlerResult{}, fmt.Errorf("%s", truncateRootControlDetail(detail))
	}
	var result managedRootControlRunnerResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || (result.Status != "ok" && result.Status != "error") || result.Content == "" {
		return managed.RootControlHandlerResult{}, errors.New("managed root control runner returned an invalid result")
	}
	if result.Details == nil {
		result.Details = map[string]any{}
	}
	if result.Status == "error" && (result.ErrorCode == "" || result.ErrorMessage == "") {
		return managed.RootControlHandlerResult{}, errors.New("managed root control runner returned an incomplete error")
	}
	return managed.RootControlHandlerResult{Status: result.Status, Content: result.Content, Details: result.Details, ErrorCode: result.ErrorCode, ErrorMessage: result.ErrorMessage}, nil
}

func managedRootControlEnvironment(routerURL string) []string {
	environment := make([]string, 0, len(os.Environ())+1)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "MECHA_ROUTER_URL=") {
			environment = append(environment, value)
		}
	}
	return append(environment, "MECHA_ROUTER_URL="+routerURL)
}

func truncateRootControlDetail(value string) string {
	if len(value) <= 2000 {
		return value
	}
	return value[:2000]
}
