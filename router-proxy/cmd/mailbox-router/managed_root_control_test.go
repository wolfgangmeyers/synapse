package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestRootControlledWebSearchReturnsBoundedSourceResults(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required by the managed root-control runner")
	}
	search := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/search", request.URL.Path)
		require.Equal(t, "managed root control", request.URL.Query().Get("q"))
		_, _ = writer.Write([]byte(`<!doctype html><ol><li class="b_algo"><h2><a href="https://source.example/report">Source report</a></h2><div class="b_caption"><p>Fresh source-backed result.</p></div></li></ol>`))
	}))
	defer search.Close()
	t.Setenv("MECHA_MANAGED_WEB_SEARCH_ENDPOINT", search.URL+"/search")
	toolsDir, err := filepath.Abs(filepath.Join("..", "..", "..", ".claude", "skills", "supervisor", "tools"))
	require.NoError(t, err)
	executor := mailboxRouterRootControlExecutor{python: python, toolsDir: toolsDir, managedHome: t.TempDir()}

	result, err := executor.ExecuteRootControl(context.Background(), managed.RootControlHandlerIdentity{OperationID: "web.search"}, managed.RootControlInvocation{
		RequestID: "request-1", ResultID: "result-1", ToolCallID: "call-1", OperationID: "web.search",
		Arguments: map[string]any{"query": "managed root control", "freshness": "live", "maxResults": 1},
		Fence:     store.ManagedRuntimeFence{AgentID: "managed-worker"},
	})

	require.NoError(t, err)
	require.Equal(t, "ok", result.Status)
	require.Contains(t, result.Content, "Source report")
	require.Equal(t, "bing", result.Details["provider"])
	sources, ok := result.Details["sources"].([]any)
	require.True(t, ok)
	require.Len(t, sources, 1)
	require.Equal(t, "https://source.example/report", sources[0].(map[string]any)["url"])
}

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
