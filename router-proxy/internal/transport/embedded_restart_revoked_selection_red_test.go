package transport

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// A3's active A turn stays admitted, but a picker entry revoked since the
// client read the catalog cannot reserve a model-only embedded restart.
func TestEmbeddedRestartRevokedModelDoesNotMutateActiveTurnRED(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	a := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	b := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	t.Cleanup(a.Unregister)
	t.Cleanup(b.Unregister)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	servedB := make(chan struct{}, 1)
	a.SetResponses([]providers.FauxResponseStep{func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		close(entered)
		<-release
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "A finished"}}, ai.StopStop)
	}})
	b.SetResponses([]providers.FauxResponseStep{func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		servedB <- struct{}{}
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "unexpected B"}}, ai.StopStop)
	}})
	modelA := managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}
	modelB := managed.Model{Provider: "openai-codex", ID: "gpt-5.6-sol", Available: true}
	var catalogMu sync.Mutex
	catalog := managed.Catalog{AuthRevision: 1, Models: []managed.Model{modelA, modelB}}
	source := managed.ModelSourceFunc(func(context.Context) (managed.Catalog, error) {
		catalogMu.Lock()
		defer catalogMu.Unlock()
		return managed.Catalog{AuthRevision: catalog.AuthRevision, Models: append([]managed.Model(nil), catalog.Models...)}, nil
	})
	manager, err := managed.NewManager(managed.ManagerOptions{Store: db, Manifest: manifest, CodexCredentialStore: testfixture.CodexAdmission(t), Config: managed.Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: source, EmbeddedModelResolver: func(agent store.ManagedAgentRecord) *ai.Model {
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
	call := func(method, route, id, body string) (int, map[string]any) {
		t.Helper()
		req, e := http.NewRequest(method, server.URL+route, strings.NewReader(body))
		require.NoError(t, e)
		req.Header.Set("Content-Type", "application/json")
		if id != "" {
			req.Header.Set("X-Request-ID", id)
		}
		resp, e := http.DefaultClient.Do(req)
		require.NoError(t, e)
		defer resp.Body.Close()
		raw, e := io.ReadAll(resp.Body)
		require.NoError(t, e)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(raw, &payload), string(raw))
		return resp.StatusCode, payload
	}
	countOperations := func() int {
		t.Helper()
		check, e := sql.Open("sqlite", path)
		require.NoError(t, e)
		defer check.Close()
		var count int
		require.NoError(t, check.QueryRowContext(ctx, "SELECT COUNT(*) FROM managed_http_operations").Scan(&count))
		return count
	}
	const agentID = "revoked-picker-root"
	status, payload := call(http.MethodPost, "/v1/managed-agents", "b4444444-4444-4444-8444-444444444401", `{"name":"revoked-picker-root","cwd":"`+root+`","runtime":"embedded","model":{"provider":"openai-codex","id":"gpt-5.6-terra"}}`)
	require.Equal(t, http.StatusAccepted, status, payload)
	require.Eventually(t, func() bool {
		row, e := db.ManagedAgent(ctx, agentID)
		return e == nil && row.LifecycleState == "running" && row.AcceptingPrompts
	}, 3*time.Second, 10*time.Millisecond)

	promptDone := make(chan int, 1)
	go func() {
		req, e := http.NewRequest(http.MethodPost, server.URL+"/v1/managed-agents/"+agentID+"/prompt", strings.NewReader(`{"message":"active A"}`))
		if e != nil {
			promptDone <- 0
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Request-ID", "b4444444-4444-4444-8444-444444444402")
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			promptDone <- 0
			return
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		promptDone <- resp.StatusCode
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("active A did not reach fake provider")
	}

	// Fake auth/catalog boundary: B was selectable when the client read it,
	// then its provider access is revoked before the stale POST reaches us.
	catalogMu.Lock()
	catalog.AuthRevision++
	catalog.Models[1].Available = false
	catalogMu.Unlock()
	before, err := db.ManagedAgent(ctx, agentID)
	require.NoError(t, err)
	version, err := db.GetProjectionVersion(ctx)
	require.NoError(t, err)
	operations := countOperations()
	beforeStatus, beforeProjection := call(http.MethodGet, "/v1/managed-agents/"+agentID, "", "")
	require.Equal(t, http.StatusOK, beforeStatus)

	const rejectedID = "b4444444-4444-4444-8444-444444444403"
	status, payload = call(http.MethodPost, "/v1/managed-agents/"+agentID+"/restart", rejectedID, `{"model":{"provider":"openai-codex","id":"gpt-5.6-sol"}}`)
	require.Equal(t, http.StatusUnprocessableEntity, status, payload) // first behavioral RED if stale auth is admitted

	after, err := db.ManagedAgent(ctx, agentID)
	require.NoError(t, err)
	require.Equal(t, before.SelectedModel, after.SelectedModel)
	require.Equal(t, before.RuntimeGeneration, after.RuntimeGeneration)
	require.Equal(t, before.SessionEpoch, after.SessionEpoch)
	require.Equal(t, before.LifecycleState, after.LifecycleState)
	require.Equal(t, before.ActivityState, after.ActivityState)
	require.Equal(t, before.AcceptingPrompts, after.AcceptingPrompts)
	afterVersion, err := db.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, version, afterVersion)
	require.Equal(t, operations, countOperations())
	projectionStatus, afterProjection := call(http.MethodGet, "/v1/managed-agents/"+agentID, "", "")
	require.Equal(t, http.StatusOK, projectionStatus)
	require.Equal(t, beforeProjection["projection_version"], afterProjection["projection_version"])
	require.Equal(t, beforeProjection["agent"], afterProjection["agent"])
	select {
	case <-promptDone:
		t.Fatal("active A completed before release")
	default:
	}
	select {
	case <-servedB:
		t.Fatal("revoked B served a turn")
	default:
	}
	once.Do(func() { close(release) })
	select {
	case code := <-promptDone:
		require.Equal(t, http.StatusOK, code)
	case <-time.After(3 * time.Second):
		t.Fatal("active A did not finish after release")
	}
}
