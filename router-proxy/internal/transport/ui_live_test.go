package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func newUILiveTestRouter(t *testing.T) (*Router, *store.Store, *httptest.Server) {
	t.Helper()
	durable, _ := store.OpenTestStore(t)
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	manager, err := managed.NewManager(managed.ManagerOptions{Store: durable, Config: managed.Config{Enabled: false}, Manifest: manifest})
	require.NoError(t, err)
	router, err := New(Options{Store: durable, Managed: manager, RuntimeIdentity: RuntimeIdentity{SourceCommit: "0123456789abcdef0123456789abcdef01234567", SourceTree: "clean"}})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	t.Cleanup(func() { server.Close(); _ = router.Close(context.Background()); _ = durable.Close() })
	return router, durable, server
}

func dialUILive(t *testing.T, server *httptest.Server, origin string) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + uiLivePath
	headers := http.Header{}
	if origin != "" {
		headers.Set("Origin", origin)
	}
	conn, response, err := websocket.Dial(context.Background(), url, &websocket.DialOptions{HTTPHeader: headers})
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	require.NoError(t, err)
	return conn
}

func readUILiveFrame(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, body, err := conn.Read(ctx)
	require.NoError(t, err)
	var frame map[string]any
	require.NoError(t, json.Unmarshal(body, &frame))
	return frame
}

func mapKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func reserveUILiveAgent(t *testing.T, durable *store.Store, id string) {
	t.Helper()
	require.NoError(t, durable.ReserveManagedLaunch(context.Background(), store.ManagedAgentRecord{AgentID: id, Name: id, SlotHeld: true, Cwd: "/tmp", SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: id + "-session", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: strings.Repeat("a", 64)}, store.ManagedOperation{OperationID: id + "-operation", Method: http.MethodPost, CanonicalPath: "/v1/managed-agents", RequestID: id + "-request", CanonicalBodyHash: strings.Repeat("b", 64), ResponseStatus: http.StatusAccepted, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}))
}

func TestUILiveIsDirectAndStartsWithRedactedSnapshot(t *testing.T) {
	router, durable, server := newUILiveTestRouter(t)
	health, err := http.Get(server.URL + "/v1/health")
	require.NoError(t, err)
	defer health.Body.Close()
	var healthBody map[string]any
	require.NoError(t, json.NewDecoder(health.Body).Decode(&healthBody))
	runtime, ok := healthBody["runtime"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "0123456789abcdef0123456789abcdef01234567", runtime["source_commit"])
	require.Equal(t, "clean", runtime["source_tree"])
	reserveUILiveAgent(t, durable, "alpha")
	first := dialUILive(t, server, "https://unrelated.example")
	defer first.Close(websocket.StatusNormalClosure, "done")
	frame := readUILiveFrame(t, first)
	require.Equal(t, "managed_agents_snapshot", frame["type"])
	require.Equal(t, uiLiveProtocol, frame["protocol"])
	require.NotEmpty(t, frame["projection_version"])
	agents, ok := frame["agents"].([]any)
	require.True(t, ok)
	require.Len(t, agents, 1)
	agent, ok := agents[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "router-managed", agent["source"])
	forbidden := []string{"pi_session_id", "session_epoch", "process_pid", "credential_path", "pending_initial_instruction"}
	for _, field := range forbidden {
		_, present := agent[field]
		require.False(t, present, field)
	}

	children := readUILiveFrame(t, first)
	require.Equal(t, "managed_children_snapshot", children["type"])

	second := dialUILive(t, server, "")
	defer second.Close(websocket.StatusNormalClosure, "done")
	require.Equal(t, frame["type"], readUILiveFrame(t, second)["type"])
	require.Equal(t, "managed_children_snapshot", readUILiveFrame(t, second)["type"])

	queryURL := "ws" + strings.TrimPrefix(server.URL, "http") + uiLivePath + "?ticket=forbidden"
	_, response, err := websocket.Dial(context.Background(), queryURL, nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	require.Error(t, err)

	require.NoError(t, durable.MarkManagedReady(context.Background(), "alpha"))
	router.uiLive.publish(context.Background())
	delta := readUILiveFrame(t, first)
	require.Equal(t, "managed_agent_delta", delta["type"])
	require.Equal(t, frame["projection_version"], delta["base_version"])
}

func TestUILiveReportsProcessLossAsFailedDelta(t *testing.T) {
	router, durable, server := newUILiveTestRouter(t)
	ctx := context.Background()
	reserveUILiveAgent(t, durable, "lost-agent")
	require.NoError(t, durable.UpdateManagedAgentStarting(ctx, "lost-agent", "lost-agent-session", store.ProcessIdentityRecord{PID: 501, StartMarker: "lost-marker", Incarnation: "lost-incarnation"}, "lost-generation"))
	fence := store.ManagedRuntimeFence{AgentID: "lost-agent", PiSessionID: "lost-agent-session", SessionEpoch: 1, ProcessPID: 501, ProcessStartMarker: "lost-marker", ProcessIncarnation: "lost-incarnation", RuntimeGeneration: "lost-generation", ManifestRevision: 1, ManifestHash: strings.Repeat("a", 64)}
	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, fence, store.DefaultManagedReadinessPolicy()))
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, fence, store.ManagedChildSnapshot{SnapshotID: "lost-snapshot", OwnerSessionID: fence.PiSessionID, Sequence: 0, Canonical: []byte(`{"owner_session_id":"lost-agent-session","protocol":"mecha.children/v1","runs":[],"sequence":0,"snapshot_id":"lost-snapshot"}`), SHA256: strings.Repeat("a", 64)}))
	require.NoError(t, durable.MarkManagedReadyFromSources(ctx, fence))

	live := dialUILive(t, server, "")
	defer live.Close(websocket.StatusNormalClosure, "done")
	initial := readUILiveFrame(t, live)
	require.Equal(t, "managed_agents_snapshot", initial["type"])
	require.Equal(t, "managed_children_snapshot", readUILiveFrame(t, live)["type"])

	updated, err := durable.RecordManagedParentProcessLost(ctx, fence)
	require.NoError(t, err)
	require.True(t, updated)
	require.NoError(t, durable.MarkManagedFailed(ctx, fence.AgentID, "PROCESS_EXITED", "The managed process is no longer available."))
	router.uiLive.publish(ctx)

	delta := readUILiveFrame(t, live)
	require.Equal(t, "managed_agent_delta", delta["type"])
	require.Equal(t, initial["projection_version"], delta["base_version"])
	changes, ok := delta["changes"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "failed", changes["lifecycle_state"])
	require.Equal(t, false, changes["accepting_prompts"])
	errorView, ok := changes["last_error"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "PROCESS_EXITED", errorView["code"])
}

func TestUILiveProjectionContainsEveryLiveRecordAndNoArchives(t *testing.T) {
	key := "123e4567-e89b-42d3-a456-426614174081"
	stored := store.ManagedAgentsSnapshot{
		GeneratedAt: time.Unix(1, 0), ProjectionVersion: 4,
		Agents: []*store.ManagedAgentRecord{
			{AgentID: "alpha", Name: "alpha", Cwd: "/tmp/alpha", SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, ManifestRevision: 1, ManifestHash: strings.Repeat("a", 64)},
			{AgentID: "beta", Name: "beta", Cwd: "/tmp/beta", SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, ManifestRevision: 1, ManifestHash: strings.Repeat("b", 64)},
		},
		Archives: []*store.ManagedAgentArchive{{RecordKind: "archived", LifecycleState: "archived", AgentID: "alpha", Name: "alpha", ArchiveKey: key, TerminalOutcome: "terminated", CompletedAt: "2026-09-01T12:00:00.000Z"}},
	}
	projection := uiLiveProjectionFromStored(stored, 4, "", nil)
	frame := projection.frame()
	records := frame["agents"].([]map[string]any)
	require.Len(t, records, 2)
	require.Equal(t, []string{"alpha", "beta"}, []string{records[0]["agent_id"].(string), records[1]["agent_id"].(string)})
	for _, record := range records {
		require.NotContains(t, record, "record_kind")
		require.NotContains(t, record, "archive_key")
	}
}

func TestUILiveTransitionUsesLiveAgentIdentityAndDocumentedKeys(t *testing.T) {
	prior := &uiLiveProjection{version: 3, generatedAt: "2030-01-01T00:00:00.000Z", agents: map[string]map[string]any{"alpha": {"agent_id": "alpha", "activity_state": "idle"}, "beta": {"agent_id": "beta", "activity_state": "idle"}}}
	updated := uiLiveTransition(prior, &uiLiveProjection{version: 4, generatedAt: "2030-01-01T00:00:01.000Z", agents: map[string]map[string]any{"alpha": {"agent_id": "alpha", "activity_state": "active"}, "beta": {"agent_id": "beta", "activity_state": "idle"}}})
	require.Equal(t, "managed_agent_delta", updated["type"])
	require.Equal(t, "alpha", updated["agent_id"])
	require.Equal(t, int64(3), updated["base_version"])
	require.Equal(t, int64(4), updated["projection_version"])
	require.NotContains(t, updated, "record_key")
	require.Equal(t, []string{"agent_id", "base_version", "changes", "projection_version", "protocol", "type"}, mapKeys(updated))
	require.Equal(t, "alpha", updated["changes"].(map[string]any)["agent_id"])

	removed := uiLiveTransition(&uiLiveProjection{version: 4, generatedAt: "2030-01-01T00:00:00.000Z", agents: map[string]map[string]any{"alpha": {"agent_id": "alpha"}, "beta": {"agent_id": "beta"}}}, &uiLiveProjection{version: 5, generatedAt: "2030-01-01T00:00:01.000Z", agents: map[string]map[string]any{"beta": {"agent_id": "beta"}}})
	require.Equal(t, "managed_agent_removed", removed["type"])
	require.Equal(t, "alpha", removed["agent_id"])
	require.NotContains(t, removed, "record_key")
	require.Equal(t, []string{"agent_id", "base_version", "projection_version", "protocol", "type"}, mapKeys(removed))
}

func TestUILiveTransitionUsesSnapshotForVersionGapsAndRemovalForOneRecord(t *testing.T) {
	prior := &uiLiveProjection{version: 3, generatedAt: "2030-01-01T00:00:00.000Z", agents: map[string]map[string]any{"alpha": {"agent_id": "alpha"}}}
	removed := uiLiveTransition(prior, &uiLiveProjection{version: 4, generatedAt: "2030-01-01T00:00:01.000Z", agents: map[string]map[string]any{}})
	require.Equal(t, "managed_agent_removed", removed["type"])
	require.Equal(t, "alpha", removed["agent_id"])
	gap := uiLiveTransition(prior, &uiLiveProjection{version: 5, generatedAt: "2030-01-01T00:00:02.000Z", agents: map[string]map[string]any{"alpha": {"agent_id": "alpha"}}})
	require.Equal(t, "managed_agents_snapshot", gap["type"])
}

func TestUILiveTransitionEmitsSnapshotWhenVersionUnchangedButAgentsDiffer(t *testing.T) {
	prior := &uiLiveProjection{version: 3, generatedAt: "2030-01-01T00:00:00.000Z", agents: map[string]map[string]any{"alpha": {"agent_id": "alpha", "active_workflow_state_id": ""}}}
	sameVersionUpdated := &uiLiveProjection{version: 3, generatedAt: "2030-01-01T00:00:01.000Z", agents: map[string]map[string]any{"alpha": {"agent_id": "alpha", "active_workflow_state_id": "wf-1"}}}
	transition := uiLiveTransition(prior, sameVersionUpdated)
	// Falls back to full snapshot so the client is guaranteed to receive the change
	require.Equal(t, "managed_agents_snapshot", transition["type"])
}

func TestManagedHistoryFramePreservesPresentEmptyMessageDetails(t *testing.T) {
	emptyBody := ""
	sender := "original-sender"
	frame := managedHistoryFrame(store.ManagedHistoryEntry{ID: "complete:1", AgentID: "agent", At: "2030-01-01T00:00:00.000Z", Level: "info", Message: "Message received", MessageContent: &emptyBody, SenderIdentity: &sender})
	require.Contains(t, frame, "message_content")
	require.Equal(t, "", frame["message_content"])
	require.Equal(t, sender, frame["sender_identity"])

	legacy := managedHistoryFrame(store.ManagedHistoryEntry{ID: "complete:2", AgentID: "agent", At: "2030-01-01T00:00:00.000Z", Level: "info", Message: "Legacy event"})
	require.NotContains(t, legacy, "message_content")
	require.NotContains(t, legacy, "sender_identity")
}

func TestManagedLifecycleHistoryFactsSuppressGenericLifecycleProgress(t *testing.T) {
	prior := &uiLiveProjection{agents: map[string]map[string]any{"alpha": {"lifecycle_state": "starting", "activity_state": "starting", "pi_session_id": "private"}, "gone": {"lifecycle_state": "stopped"}}}
	current := &uiLiveProjection{agents: map[string]map[string]any{"alpha": {"lifecycle_state": "running", "activity_state": "idle", "pi_session_id": "still-private"}, "beta": {"lifecycle_state": "starting"}}}
	facts := managedLifecycleHistoryFacts(prior, current)
	require.Empty(t, facts)
}

func TestManagedLifecycleHistoryFactsRecordsASelectedModelChange(t *testing.T) {
	prior := &uiLiveProjection{version: 4, agents: map[string]map[string]any{"alpha": {"lifecycle_state": "running", "activity_state": "idle", "model": map[string]any{"provider": "openai-codex", "id": "gpt-5.6-terra"}}}}
	current := &uiLiveProjection{version: 5, agents: map[string]map[string]any{"alpha": {"lifecycle_state": "running", "activity_state": "idle", "model": map[string]any{"provider": "google", "id": "gemini-2.5-pro"}}}}
	facts := managedLifecycleHistoryFacts(prior, current)
	require.Equal(t, []managedLifecycleHistoryFact{{agentID: "alpha", message: "Model changed", eventKey: "model:alpha:5", presentation: &store.ManagedHistoryPresentation{Kind: "model", Outcome: "success", Summary: "Google · gemini-2.5-pro"}}}, facts)
}

func TestManagedDirectoryBrowserListsOnlyContainedFolders(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "child", "project")
	require.NoError(t, os.Mkdir(filepath.Join(root, "child"), 0o700))
	require.NoError(t, os.Mkdir(project, 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(root, ".cache"), 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(root, "OrbStack"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "not-a-folder"), []byte("x"), 0o600))
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	manager, err := managed.NewManager(managed.ManagerOptions{Store: durable, Config: managed.Config{AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest})
	require.NoError(t, err)
	router, err := New(Options{Store: durable, Managed: manager})
	require.NoError(t, err)
	defer func() { _ = router.Close(context.Background()); _ = durable.Close() }()
	request := httptest.NewRequest(http.MethodGet, "/v1/managed-directories?path="+url.QueryEscape(root), nil)
	request.Header.Set("Origin", "https://unrelated.example")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	directories, ok := body["directories"].([]any)
	require.True(t, ok)
	require.Len(t, directories, 1)
	directory, ok := directories[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "child", directory["name"])
	require.NotContains(t, response.Body.String(), ".cache")
	require.NotContains(t, response.Body.String(), "OrbStack")

	emptyRequest := httptest.NewRequest(http.MethodGet, "/v1/managed-directories?path=", nil)
	emptyResponse := httptest.NewRecorder()
	router.ServeHTTP(emptyResponse, emptyRequest)
	require.Equal(t, http.StatusOK, emptyResponse.Code)
	var emptyBody map[string]any
	require.NoError(t, json.Unmarshal(emptyResponse.Body.Bytes(), &emptyBody))
	expectedRoot, expectedRootErr := filepath.EvalSymlinks(root)
	require.NoError(t, expectedRootErr)
	require.Equal(t, expectedRoot, emptyBody["path"])

	realProject, realProjectErr := filepath.EvalSymlinks(project)
	require.NoError(t, realProjectErr)
	projectRequest := httptest.NewRequest(http.MethodGet, "/v1/managed-directories?path="+url.QueryEscape(project), nil)
	projectResponse := httptest.NewRecorder()
	router.ServeHTTP(projectResponse, projectRequest)
	require.Equal(t, http.StatusOK, projectResponse.Code)
	var projectBody struct {
		Path        string `json:"path"`
		Parent      string `json:"parent"`
		Breadcrumbs []struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"breadcrumbs"`
	}
	require.NoError(t, json.Unmarshal(projectResponse.Body.Bytes(), &projectBody))
	require.Equal(t, realProject, projectBody.Path)
	require.Equal(t, filepath.Dir(realProject), projectBody.Parent)
	require.Equal(t, []struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}{{Name: filepath.Base(expectedRoot), Path: expectedRoot}, {Name: "child", Path: filepath.Join(expectedRoot, "child")}, {Name: "project", Path: realProject}}, projectBody.Breadcrumbs)
}

func TestStaticUIRejectsSymlinkedAssetsAndRechecksContainment(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "index.html"), []byte("ok"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.js"), []byte("not an asset"), 0o600))
	require.NoError(t, os.Symlink(filepath.Join(outside, "secret.js"), filepath.Join(root, "escape.js")))
	_, err := newStaticUI(root)
	require.Error(t, err)

	require.NoError(t, os.Remove(filepath.Join(root, "escape.js")))
	ui, err := newStaticUI(root)
	require.NoError(t, err)
	require.NoError(t, os.Symlink(filepath.Join(outside, "secret.js"), filepath.Join(root, "escape.js")))
	_, ok := ui.staticUIFile("escape.js")
	require.False(t, ok, "a symlink introduced after startup must not be served")
}

func TestRouterServesContainedStaticUIWithoutAuthorityState(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "index.html"), []byte("<!doctype html><div id=\"root\"></div>"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(root, "assets"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "assets", "app.js"), []byte("console.log('ok')"), 0o600))
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	router, err := New(Options{Store: durable, StaticUIRoot: root})
	require.NoError(t, err)
	defer func() { _ = router.Close(context.Background()); _ = durable.Close() }()
	request := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	request.Header.Set("Origin", "https://unrelated.example")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	require.Empty(t, response.Header().Get("Set-Cookie"))
	require.Empty(t, response.Header().Get("Access-Control-Allow-Credentials"))
	csp := response.Header().Get("Content-Security-Policy")
	require.Contains(t, csp, "connect-src 'self'")
	require.Contains(t, csp, "img-src 'self' data:")
	require.NotContains(t, csp, "robohash.org")
}
