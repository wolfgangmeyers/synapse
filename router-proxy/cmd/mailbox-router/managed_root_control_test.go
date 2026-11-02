package main

import (
	"context"
	"encoding/json"
	"io"
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

func TestRootControlRouterOperationsUseConfiguredEndpointOnly(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required by the managed root-control runner")
	}
	var targetPaths []string
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		targetPaths = append(targetPaths, request.URL.Path)
		switch request.URL.Path {
		case "/v1/messages":
			_, _ = writer.Write([]byte(`{"message":{"id":"target-message","state":"queued"}}`))
		case "/v1/reminders":
			body, _ := io.ReadAll(request.Body)
			var reminder map[string]any
			require.NoError(t, json.Unmarshal(body, &reminder))
			response, marshalErr := json.Marshal(map[string]any{"reminder": reminder})
			require.NoError(t, marshalErr)
			_, _ = writer.Write(response)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer target.Close()
	decoyRequests := 0
	decoy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		decoyRequests++
		http.Error(writer, "decoy", http.StatusInternalServerError)
	}))
	defer decoy.Close()
	t.Setenv("MECHA_ROUTER_URL", decoy.URL)

	toolsDir := t.TempDir()
	repositoryRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	runner, err := os.ReadFile(filepath.Join(repositoryRoot, "tools", managedRootControlRunner))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(toolsDir, managedRootControlRunner), runner, 0o700))
	mailSender, err := os.ReadFile(filepath.Join(repositoryRoot, "tools", "mailbox_send.py"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(toolsDir, "mailbox_send.py"), mailSender, 0o700))
	t.Setenv("MECHA_SUPERVISOR_TOOLS", toolsDir)
	executor, err := newMailboxRouterRootControlExecutor(managed.Config{ManagedHome: t.TempDir(), RouterURL: target.URL})
	require.NoError(t, err)

	mailResult, err := executor.ExecuteRootControl(context.Background(), managed.RootControlHandlerIdentity{OperationID: "mail.send"}, managed.RootControlInvocation{
		RequestID: "request-mail", ResultID: "result-mail", ToolCallID: "call-mail", OperationID: "mail.send",
		Arguments: map[string]any{"recipient": "supervisor", "body": "configured target"}, Fence: store.ManagedRuntimeFence{AgentID: "managed-worker"},
	})
	require.NoError(t, err)
	require.Equal(t, "ok", mailResult.Status)

	reminderResult, err := executor.ExecuteRootControl(context.Background(), managed.RootControlHandlerIdentity{OperationID: "reminder.control"}, managed.RootControlInvocation{
		RequestID: "request-reminder", ResultID: "result-reminder", ToolCallID: "call-reminder", OperationID: "reminder.control",
		Arguments: map[string]any{"action": "create", "message": "check target", "delayMinutes": 1}, Fence: store.ManagedRuntimeFence{AgentID: "managed-worker"},
	})
	require.NoError(t, err)
	require.Equal(t, "ok", reminderResult.Status)
	require.Equal(t, []string{"/v1/messages", "/v1/reminders"}, targetPaths)
	require.Zero(t, decoyRequests)
}

func TestRootControlExecutorRejectsMissingOrInvalidRouterEndpoint(t *testing.T) {
	for _, routerURL := range []string{"", "http://127.0.0.1:8765/path", "http://localhost:8765"} {
		_, err := newMailboxRouterRootControlExecutor(managed.Config{ManagedHome: t.TempDir(), RouterURL: routerURL})
		require.Error(t, err, "router URL %q must fail closed", routerURL)
	}
}

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
	toolsDir, err := filepath.Abs(filepath.Join("..", "..", "..", "tools"))
	require.NoError(t, err)
	executor := mailboxRouterRootControlExecutor{python: python, toolsDir: toolsDir, managedHome: t.TempDir(), routerURL: "http://127.0.0.1:18775"}

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
	executor := mailboxRouterRootControlExecutor{python: python, toolsDir: toolsDir, managedHome: t.TempDir(), routerURL: "http://127.0.0.1:18775"}

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
