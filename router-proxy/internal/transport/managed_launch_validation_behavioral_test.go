package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

type launchValidationProcessFactory struct {
	mu     sync.Mutex
	starts []managed.CommandSpec
}

func (f *launchValidationProcessFactory) Start(_ context.Context, spec managed.CommandSpec) (managed.Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts = append(f.starts, spec)
	return &managedTestProcess{identity: managed.ProcessIdentity{PID: 30000 + len(f.starts), StartMarker: spec.SessionID, Incarnation: "validation"}, stopped: make(chan struct{})}, nil
}
func (f *launchValidationProcessFactory) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.starts)
}
func (f *launchValidationProcessFactory) last() managed.CommandSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts[len(f.starts)-1]
}

type launchValidationRPCFactory struct {
	mu        sync.Mutex
	prompts   []string
	active    []string
	bootstrap chan struct{}
	current   managed.Model
	setCalls  int
}
type launchValidationRPC struct {
	managedTestRPC
	owner *launchValidationRPCFactory
}

func (f *launchValidationRPCFactory) Open(_ context.Context, process managed.Process) (managed.RPC, error) {
	session := process.Identity().StartMarker
	return &launchValidationRPC{managedTestRPC: managedTestRPC{session: &session, active: f.active}, owner: f}, nil
}
func (r *launchValidationRPC) Bootstrap(ctx context.Context, payload map[string]any) error {
	if r.owner.bootstrap != nil {
		r.owner.bootstrap <- struct{}{}
	}
	return r.managedTestRPC.Bootstrap(ctx, payload)
}
func (r *launchValidationRPC) Prompt(ctx context.Context, message string) error {
	r.owner.mu.Lock()
	r.owner.prompts = append(r.owner.prompts, message)
	r.owner.mu.Unlock()
	return nil
}
func (r *launchValidationRPC) GetState(ctx context.Context) (map[string]any, error) {
	state, err := r.managedTestRPC.GetState(ctx)
	if err != nil {
		return nil, err
	}
	r.owner.mu.Lock()
	current := r.owner.current
	r.owner.mu.Unlock()
	if current.Provider != "" {
		state["model"] = map[string]any{"provider": current.Provider, "id": current.ID, "name": current.Name}
	}
	return state, nil
}
func (r *launchValidationRPC) SetModel(_ context.Context, provider, modelID string) (managed.Model, error) {
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	r.owner.setCalls++
	r.owner.current = managed.Model{Provider: provider, ID: modelID, Name: map[string]string{"gemini-2.5-pro": "Gemini 2.5 Pro"}[modelID], Available: true}
	return r.owner.current, nil
}
func (f *launchValidationRPCFactory) promptsSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.prompts...)
}
func (f *launchValidationRPCFactory) setCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.setCalls
}

func newLaunchValidationServer(t *testing.T, catalog managed.ModelSource, rpcFactory *launchValidationRPCFactory) (*store.Store, *launchValidationProcessFactory, *managed.Manager, *httptest.Server) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	rpcFactory.active = expectedManagedActiveToolsForTransportTest(manifest)
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	processFactory := &launchValidationProcessFactory{}
	manager, err := managed.NewManager(managed.ManagerOptions{Store: durable, Config: managed.Config{SessionRoot: t.TempDir(), AllowedWorkingDirectoryRoots: []string{root}, RouterURL: "http://127.0.0.1:1"}, Manifest: manifest, ProcessFactory: processFactory, RPCFactory: rpcFactory, CatalogSource: catalog, ProcessGroupLeaderVerifier: func(managed.ProcessIdentity) bool { return true }})
	require.NoError(t, err)
	router, err := New(Options{Store: durable, Managed: manager})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	t.Cleanup(func() { server.Close(); _ = router.Close(ctx); _ = durable.Close() })
	return durable, processFactory, manager, server
}

func launchValidationRequest(t *testing.T, server *httptest.Server, requestID, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/managed-agents", bytes.NewBufferString(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", requestID)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(raw, &payload))
	return resp.StatusCode, payload
}

func TestManagedModelHTTPSuccessPreservesBindingAndReplays(t *testing.T) {
	ctx := context.Background()
	rpc := &launchValidationRPCFactory{current: managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}}
	catalog := managed.CatalogFromModels(
		managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true},
		managed.Model{Provider: "google", ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro", Available: true},
	)
	durable, _, manager, server := newLaunchValidationServer(t, catalog, rpc)
	root := manager.AllowedWorkingDirectoryRoots()[0]
	status, launch := launchValidationRequest(t, server, "123e4567-e89b-42d3-a456-426614174096", `{"name":"model-http","cwd":"`+root+`","model":{"provider":"openai-codex","id":"gpt-5.6-terra"}}`)
	require.Equal(t, http.StatusAccepted, status)
	agentID := launch["agent"].(map[string]any)["agent_id"].(string)
	require.Eventually(t, func() bool {
		agent, err := durable.ManagedAgent(ctx, agentID)
		return err == nil && agent.LifecycleState == "running" && agent.AcceptingPrompts
	}, 5*time.Second, 10*time.Millisecond)
	before, err := durable.ManagedAgent(ctx, agentID)
	require.NoError(t, err)
	recipe, err := store.ManagedLaunchRecipeDigest(store.ManagedLaunchRecipe{PiExecutable: "/test/pi", SessionRoot: "/test/sessions", Cwd: before.Cwd, Provider: before.SelectedModel.Provider, ModelID: before.SelectedModel.ID, MechaRPCExtension: "/test/mecha-rpc.ts", PiSubagentsExtension: "/test/subagents.ts", TestOnlyExtensions: []string{}, Environment: []string{}})
	require.NoError(t, err)
	require.NoError(t, durable.PutManagedLaunchRecipe(ctx, agentID, recipe))

	put := func(requestID, body string) (int, []byte) {
		req, err := http.NewRequest(http.MethodPut, server.URL+"/v1/managed-agents/"+agentID+"/model", bytes.NewBufferString(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Request-ID", requestID)
		response, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		return response.StatusCode, raw
	}
	requestID := "123e4567-e89b-42d3-a456-426614174097"
	body := `{"model":{"provider":"google","id":"gemini-2.5-pro"}}`
	status, first := put(requestID, body)
	require.Equal(t, http.StatusOK, status, string(first))
	var payload map[string]any
	require.NoError(t, json.Unmarshal(first, &payload))
	require.Equal(t, requestID, payload["operation_id"])
	after, err := durable.ManagedAgent(ctx, agentID)
	require.NoError(t, err)
	require.Equal(t, before.ProcessPID, after.ProcessPID)
	require.Equal(t, before.ProcessStartMarker, after.ProcessStartMarker)
	require.Equal(t, before.ProcessIncarnation, after.ProcessIncarnation)
	require.Equal(t, before.RuntimeGeneration, after.RuntimeGeneration)
	require.Equal(t, before.PiSessionID, after.PiSessionID)
	require.Equal(t, "gemini-2.5-pro", after.SelectedModel.ID)
	updatedRecipe, err := durable.ManagedLaunchRecipe(ctx, agentID)
	require.NoError(t, err)
	require.Equal(t, "google", updatedRecipe.Provider)
	require.Equal(t, "gemini-2.5-pro", updatedRecipe.ModelID)
	require.NotEqual(t, recipe.SHA256, updatedRecipe.SHA256)
	operation, err := durable.FindManagedOperation(ctx, http.MethodPut, requestID)
	require.NoError(t, err)
	require.Equal(t, "terminal", operation.State)
	require.Equal(t, http.StatusOK, operation.ResponseStatus)
	require.NoError(t, manager.AdmitPrompt(ctx, agentID, "next prompt"))
	require.Equal(t, []string{"next prompt"}, rpc.promptsSnapshot())

	replayStatus, replay := put(requestID, body)
	require.Equal(t, status, replayStatus)
	require.Equal(t, first, replay)
	require.Equal(t, 1, rpc.setCount())
	conflictStatus, conflict := put(requestID, `{"model":{"provider":"openai-codex","id":"gpt-5.6-terra"}}`)
	require.Equal(t, http.StatusConflict, conflictStatus, string(conflict))
	require.Contains(t, string(conflict), "IDEMPOTENCY_CONFLICT")
	require.Equal(t, 1, rpc.setCount())
}

func TestManagedLaunchValidationBehavioral(t *testing.T) {
	t.Run("AC-002 canonical paths and invalid path effects", func(t *testing.T) {
		rpc := &launchValidationRPCFactory{}
		catalog := managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true})
		_, processes, manager, server := newLaunchValidationServer(t, catalog, rpc)
		root := manager.AllowedWorkingDirectoryRoots()[0]
		inside := filepath.Join(root, "inside")
		require.NoError(t, os.Mkdir(inside, 0o700))
		file := filepath.Join(root, "file")
		require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
		escape := filepath.Join(root, "escape")
		require.NoError(t, os.Symlink(t.TempDir(), escape))
		insideLink := filepath.Join(root, "inside-link")
		require.NoError(t, os.Symlink(inside, insideLink))
		cases := []struct {
			name, cwd string
			status    int
		}{
			{"directory", inside + string(os.PathSeparator), http.StatusAccepted}, {"directory symlink", insideLink, http.StatusAccepted},
			{"file", file, http.StatusUnprocessableEntity}, {"nonexistent", filepath.Join(root, "missing"), http.StatusUnprocessableEntity}, {"escaping symlink", escape, http.StatusUnprocessableEntity},
		}
		for i, tc := range cases {
			before := processes.count()
			body := `{"name":"path` + string(rune('a'+i)) + `","cwd":` + string(launchValidationJSON(tc.cwd)) + `}`
			status, payload := launchValidationRequest(t, server, "123e4567-e89b-42d3-a456-42661417408"+string(rune('0'+i)), body)
			require.Equal(t, tc.status, status, tc.name)
			if tc.status != http.StatusAccepted {
				require.Equal(t, "INVALID_WORKING_DIRECTORY", payload["error"].(map[string]any)["code"])
				require.Eventually(t, func() bool { return processes.count() == before }, 2*time.Second, time.Millisecond)
			} else {
				require.Eventually(t, func() bool { return processes.count() == before+1 }, 2*time.Second, time.Millisecond)
			}
		}
		require.Eventually(t, func() bool { return processes.count() == 2 }, 2*time.Second, time.Millisecond)
		canonicalInside, err := filepath.EvalSymlinks(inside)
		require.NoError(t, err)
		require.Equal(t, canonicalInside, processes.last().Cwd)
	})

	t.Run("AC-003 exact five model outcomes", func(t *testing.T) {
		var mu sync.Mutex
		catalog := managed.Catalog{Models: []managed.Model{{Provider: "openai-codex", ID: "gpt-5.6-sol", Available: true}, {Provider: "google", ID: "gemini-2.5-pro", Available: true}}}
		source := managed.ModelSourceFunc(func(context.Context) (managed.Catalog, error) { mu.Lock(); defer mu.Unlock(); return catalog, nil })
		rpc := &launchValidationRPCFactory{}
		durable, processes, manager, server := newLaunchValidationServer(t, source, rpc)
		root := manager.AllowedWorkingDirectoryRoots()[0]
		cases := []struct {
			name, body, provider, model string
			status                      int
		}{
			{"explicit", `{"name":"explicit","cwd":` + string(launchValidationJSON(root)) + `,"model":{"provider":"google","id":"gemini-2.5-pro"}}`, "google", "gemini-2.5-pro", 202},
			{"saved default", `{"name":"saved","cwd":` + string(launchValidationJSON(root)) + `}`, "google", "gemini-2.5-pro", 202},
			{"unavailable saved default", `{"name":"fallback","cwd":` + string(launchValidationJSON(root)) + `}`, "openai-codex", "gpt-5.6-sol", 202},
			{"unavailable explicit", `{"name":"bad-explicit","cwd":` + string(launchValidationJSON(root)) + `,"model":{"provider":"google","id":"missing"}}`, "", "", 422},
			{"unavailable Sol", `{"name":"bad-sol","cwd":` + string(launchValidationJSON(root)) + `}`, "openai-codex", "gpt-5.6-sol", 202},
		}
		require.NoError(t, durable.SetDefaultModel(context.Background(), &store.ManagedModel{Provider: "google", ID: "gemini-2.5-pro"}))
		for i, tc := range cases {
			if tc.name == "unavailable saved default" {
				require.NoError(t, durable.SetDefaultModel(context.Background(), &store.ManagedModel{Provider: "missing", ID: "model"}))
			}
			if tc.name == "unavailable Sol" {
				mu.Lock()
				catalog = managed.Catalog{}
				mu.Unlock()
				require.NoError(t, durable.SetDefaultModel(context.Background(), nil))
			}
			before := processes.count()
			status, _ := launchValidationRequest(t, server, "223e4567-e89b-42d3-a456-42661417408"+string(rune('0'+i)), tc.body)
			require.Equal(t, tc.status, status, tc.name)
			if tc.status == 202 {
				require.Eventually(t, func() bool { return processes.count() > before }, 2*time.Second, time.Millisecond)
				spec := processes.last()
				require.Equal(t, tc.provider, spec.Provider)
				require.Equal(t, tc.model, spec.ModelID)
			} else {
				require.Equal(t, before, processes.count())
			}
		}
	})

	t.Run("AC-004 instruction bound and ordering", func(t *testing.T) {
		rpc := &launchValidationRPCFactory{bootstrap: make(chan struct{}, 1)}
		catalog := managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true})
		_, processes, manager, server := newLaunchValidationServer(t, catalog, rpc)
		root := manager.AllowedWorkingDirectoryRoots()[0]
		instruction := string(bytes.Repeat([]byte{'x'}, managed.MaxInitialInstructionBytes-1))
		body := `{"name":"bounded","cwd":` + string(launchValidationJSON(root)) + `,"initial_instruction":` + string(launchValidationJSON(instruction)) + `}`
		status, _ := launchValidationRequest(t, server, "323e4567-e89b-42d3-a456-426614174101", body)
		require.Equal(t, http.StatusAccepted, status)
		select {
		case <-rpc.bootstrap:
		case <-time.After(2 * time.Second):
			t.Fatal("manifest bootstrap did not complete")
		}
		require.Eventually(t, func() bool { return len(rpc.promptsSnapshot()) == 1 }, 2*time.Second, time.Millisecond)
		require.Equal(t, instruction, rpc.promptsSnapshot()[0])
		before := processes.count()
		tooLargeInstruction := string(bytes.Repeat([]byte{'x'}, managed.MaxInitialInstructionBytes+1))
		body = `{"name":"too-large-instruction","cwd":` + string(launchValidationJSON(root)) + `,"initial_instruction":` + string(launchValidationJSON(tooLargeInstruction)) + `}`
		status, payload := launchValidationRequest(t, server, "323e4567-e89b-42d3-a456-426614174102", body)
		require.Equal(t, http.StatusRequestEntityTooLarge, status)
		require.Equal(t, "PAYLOAD_TOO_LARGE", payload["error"].(map[string]any)["code"])
		require.Equal(t, before, processes.count())

		padding := string(bytes.Repeat([]byte{'x'}, 64*1024))
		body = `{"name":"too-large-body","cwd":` + string(launchValidationJSON(root)) + `,"padding":` + string(launchValidationJSON(padding)) + `}`
		status, payload = launchValidationRequest(t, server, "323e4567-e89b-42d3-a456-426614174103", body)
		require.Equal(t, http.StatusRequestEntityTooLarge, status)
		require.Equal(t, "PAYLOAD_TOO_LARGE", payload["error"].(map[string]any)["code"])
		require.Equal(t, before, processes.count())
	})
}
func launchValidationJSON(value string) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}
