package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestRootControlRunnerUsesSnakeCaseFenceContract(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required by the managed root-control runner")
	}
	toolsDir := t.TempDir()
	runner := `import json
import sys

request = json.load(sys.stdin)
fence = request.get("fence")
expected = {
    "agent_id", "pi_session_id", "session_epoch", "process_pid",
    "process_start_marker", "process_incarnation", "runtime_generation",
    "manifest_revision", "manifest_hash",
}
if not isinstance(fence, dict) or set(fence) != expected or fence.get("agent_id") != "managed-worker":
    print("request is invalid", file=sys.stderr)
    raise SystemExit(1)
print(json.dumps({"status": "ok", "content": "mail sent", "details": {"history": "quiet"}}))
`
	require.NoError(t, os.WriteFile(filepath.Join(toolsDir, managedRootControlRunner), []byte(runner), 0o600))
	executor := mailboxRouterRootControlExecutor{python: python, toolsDir: toolsDir, managedHome: t.TempDir()}

	result, err := executor.ExecuteRootControl(context.Background(), managed.RootControlHandlerIdentity{OperationID: "mail.send"}, managed.RootControlInvocation{
		RequestID: "request-1", ResultID: "result-1", ToolCallID: "call-1", OperationID: "mail.send",
		Arguments: map[string]any{"recipient": "supervisor", "body": "quiet message"},
		Fence: store.ManagedRuntimeFence{
			AgentID: "managed-worker", PiSessionID: "pi-session", SessionEpoch: 3, ProcessPID: 42,
			ProcessStartMarker: "started", ProcessIncarnation: "incarnation", RuntimeGeneration: "generation",
			ManifestRevision: 7, ManifestHash: "manifest",
		},
	})

	require.NoError(t, err)
	require.Equal(t, "ok", result.Status)
	require.Equal(t, "mail sent", result.Content)
	require.Equal(t, map[string]any{"history": "quiet"}, result.Details)
}
