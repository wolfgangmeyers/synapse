package transport

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// A3: the existing single picker is the durable selection, not the provider
// currently serving an already-admitted turn. Its admission credential is offline.
func TestEmbeddedRestartPickerA3HTTPAndLiveRED(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	info, statErr := os.Stat(root)
	require.NoError(t, statErr)
	t.Logf("Cwd fixture source=embedded_restart_picker_a3_red_test.go:t.TempDir path=%s before_mode=%#o", root, info.Mode().Perm())
	// Only this test-owned Cwd, never shared TMPDIR or a production root.
	require.NoError(t, os.Chmod(root, 0700))
	info, statErr = os.Stat(root)
	require.NoError(t, statErr)
	require.Equal(t, os.FileMode(0700), info.Mode().Perm())
	t.Logf("Cwd fixture path=%s after_mode=%#o", root, info.Mode().Perm())
	path := filepath.Join(t.TempDir(), "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	a := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	b := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	t.Cleanup(a.Unregister)
	t.Cleanup(b.Unregister)
	entered := make(chan struct{})
	servedB := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var callsA, callsB atomic.Int32
	a.SetResponses([]providers.FauxResponseStep{func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		callsA.Add(1)
		close(entered)
		<-release
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "answer from A"}}, ai.StopStop)
	}})
	b.SetResponses([]providers.FauxResponseStep{func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		callsB.Add(1)
		servedB <- struct{}{}
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "answer from B"}}, ai.StopStop)
	}})
	modelA := managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}
	modelB := managed.Model{Provider: "openai-codex", ID: "gpt-5.6-sol", Available: true}
	manager, err := managed.NewManager(managed.ManagerOptions{Store: db, Manifest: manifest, CodexCredentialStore: testfixture.CodexAdmission(t), Config: managed.Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: managed.CatalogFromModels(modelA, modelB), EmbeddedModelResolver: func(agent store.ManagedAgentRecord) *ai.Model {
		if agent.SelectedModel.Provider == modelB.Provider && agent.SelectedModel.ID == modelB.ID {
			return b.GetModel()
		}
		return a.GetModel()
	}})
	require.NoError(t, err)
	router, err := New(Options{Store: db, Managed: manager})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	t.Cleanup(func() { server.Close(); _ = router.Close(ctx); _ = db.Close() })
	defer func() {
		if t.Failed() {
			t.Logf("failure snapshot providers_A=%d providers_B=%d owner_published=unmeasured(private Manager map)", callsA.Load(), callsB.Load())
			cwdPrivacyTransportSnapshot(t, path)
		}
	}()
	call := func(method, route, id, body string) (int, map[string]any) {
		t.Helper()
		req, e := http.NewRequest(method, server.URL+route, strings.NewReader(body))
		require.NoError(t, e)
		req.Header.Set("Content-Type", "application/json")
		if id != "" {
			req.Header.Set("X-Request-ID", id)
		}
		response, e := http.DefaultClient.Do(req)
		require.NoError(t, e)
		defer response.Body.Close()
		raw, e := io.ReadAll(response.Body)
		require.NoError(t, e)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(raw, &payload), string(raw))
		return response.StatusCode, payload
	}
	const agentID = "a3-root"
	status, launch := call(http.MethodPost, "/v1/managed-agents", "a3333333-3333-4333-8333-333333333301", `{"name":"a3-root","cwd":"`+root+`","runtime":"embedded","model":{"provider":"openai-codex","id":"gpt-5.6-terra"}}`)
	require.Equal(t, http.StatusAccepted, status, launch)
	require.Eventually(t, func() bool {
		row, e := db.ManagedAgent(ctx, agentID)
		return e == nil && row.LifecycleState == "running" && row.AcceptingPrompts
	}, 3*time.Second, 10*time.Millisecond)
	ws := dialUILive(t, server, "")
	defer ws.Close(websocket.StatusNormalClosure, "done")
	initial := readUILiveFrame(t, ws)
	require.Equal(t, "managed_agents_snapshot", initial["type"])
	require.Equal(t, "managed_children_snapshot", readUILiveFrame(t, ws)["type"])
	beforeStatus, before := call(http.MethodGet, "/v1/managed-agents/"+agentID, "", "")
	require.Equal(t, http.StatusOK, beforeStatus)
	v := before["projection_version"].(float64)
	require.Equal(t, v, initial["projection_version"])
	require.Equal(t, modelA.ID, before["agent"].(map[string]any)["model"].(map[string]any)["id"])
	promptDone := make(chan struct {
		status  int
		payload map[string]any
	}, 1)
	go func() {
		req, e := http.NewRequest(http.MethodPost, server.URL+"/v1/managed-agents/"+agentID+"/prompt", strings.NewReader(`{"message":"active A"}`))
		if e != nil {
			promptDone <- struct {
				status  int
				payload map[string]any
			}{0, map[string]any{"error": e.Error()}}
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Request-ID", "a3333333-3333-4333-8333-333333333302")
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			promptDone <- struct {
				status  int
				payload map[string]any
			}{0, map[string]any{"error": e.Error()}}
			return
		}
		defer resp.Body.Close()
		var payload map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&payload)
		promptDone <- struct {
			status  int
			payload map[string]any
		}{resp.StatusCode, payload}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("active A request did not reach fake provider")
	}
	const restartID = "a3333333-3333-4333-8333-333333333303"
	restartPath := "/v1/managed-agents/" + agentID + "/restart"
	restartBody := `{"model":{"provider":"openai-codex","id":"gpt-5.6-sol"}}`
	status, accepted := call(http.MethodPost, restartPath, restartID, restartBody)
	require.Equal(t, http.StatusAccepted, status, accepted) // first behavioral RED if embedded restart is still RPC-only
	require.Equal(t, restartID, accepted["operation_id"])
	replayStatus, replayed := call(http.MethodPost, restartPath, restartID, restartBody)
	require.Equal(t, status, replayStatus)
	require.Equal(t, accepted, replayed)
	conflictStatus, _ := call(http.MethodPost, restartPath, restartID, `{"model":{"provider":"openai-codex","id":"gpt-5.6-terra"}}`)
	require.Equal(t, http.StatusConflict, conflictStatus)
	for _, route := range []string{"/v1/managed-agents", "/v1/managed-agents/" + agentID} {
		code, projection := call(http.MethodGet, route, "", "")
		require.Equal(t, http.StatusOK, code)
		require.GreaterOrEqual(t, projection["projection_version"], v+1, route)
		var agent map[string]any
		if route == "/v1/managed-agents" {
			agent = projection["agents"].([]any)[0].(map[string]any)
		} else {
			agent = projection["agent"].(map[string]any)
		}
		require.Equal(t, modelB.ID, agent["model"].(map[string]any)["id"], route)
		require.Equal(t, "running", agent["lifecycle_state"])
		require.Equal(t, true, agent["accepting_prompts"])
	}
	reopened, e := store.Open(ctx, path, store.Options{})
	require.NoError(t, e)
	durable, e := reopened.ManagedAgent(ctx, agentID)
	require.NoError(t, e)
	require.Equal(t, modelB.ID, durable.SelectedModel.ID)
	require.NoError(t, reopened.Close())
	router.uiLive.publish(ctx)
	var delta map[string]any
	for {
		frame := readUILiveFrame(t, ws)
		if frame["type"] == "managed_agent_delta" {
			if changes, ok := frame["changes"].(map[string]any); ok {
				if m, ok := changes["model"].(map[string]any); ok && m["id"] == modelB.ID {
					delta = frame
					break
				}
			}
		}
	}
	require.Equal(t, "managed_agent_delta", delta["type"])
	require.Equal(t, modelB.ID, delta["changes"].(map[string]any)["model"].(map[string]any)["id"])
	once.Do(func() { close(release) })
	select {
	case result := <-promptDone:
		require.Equal(t, http.StatusOK, result.status, result.payload)
	case <-time.After(3 * time.Second):
		t.Fatal("A did not finish")
	}
	status, next := call(http.MethodPost, "/v1/managed-agents/"+agentID+"/prompt", "a3333333-3333-4333-8333-333333333304", `{"message":"next B"}`)
	require.Equal(t, http.StatusOK, status, next)
	select {
	case <-servedB:
	default:
		t.Fatal("next turn did not reach B")
	}
	invalidStatus, _ := call(http.MethodPost, restartPath, "a3333333-3333-4333-8333-333333333305", `{"model":{"provider":"missing","id":"unknown"}}`)
	require.NotEqual(t, http.StatusAccepted, invalidStatus)
	after, e := db.ManagedAgent(ctx, agentID)
	require.NoError(t, e)
	require.Equal(t, modelB.ID, after.SelectedModel.ID)
	versionAfterInvalid, e := db.GetProjectionVersion(ctx)
	require.NoError(t, e)
	require.GreaterOrEqual(t, versionAfterInvalid, int64(v+1))
}
