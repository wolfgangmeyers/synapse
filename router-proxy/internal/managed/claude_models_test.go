package managed

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestClaudeCatalogAndPiDefault(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "catalog.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	source := StoreCatalogSource{Store: db}
	catalog, err := source.Catalog(ctx)
	require.NoError(t, err)
	require.Len(t, catalog.Models, 4)
	for _, alias := range []string{"fable", "opus", "sonnet", "haiku"} {
		selected, err := source.Select(ctx, &Model{Provider: "claude-code", ID: alias})
		require.NoError(t, err)
		require.Equal(t, alias, selected.ID)
	}
	catalog.Models = append([]Model{{Provider: "google", ID: "unavailable", Available: false}}, catalog.Models...)
	selected, err := source.Select(ctx, nil)
	require.NoError(t, err)
	require.NotEqual(t, "claude-code", selected.Provider)
	selected, err = SelectModel(nil, nil, catalog)
	require.NoError(t, err)
	require.Equal(t, "openai-codex", selected.Provider)
	selected, err = SelectModel(nil, &store.ManagedModel{Provider: "claude-code", ID: "sonnet"}, catalog)
	require.NoError(t, err)
	require.Equal(t, "claude-code", selected.Provider)
	require.Equal(t, "sonnet", selected.ID)
	require.NoError(t, db.SetDefaultModel(ctx, &store.ManagedModel{Provider: "claude-code", ID: "sonnet"}))
	selected, err = source.Select(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, "sonnet", selected.ID)
	require.Equal(t, "claude-code", selected.Provider)
	_, err = source.Select(ctx, &Model{Provider: "claude-code", ID: "unknown"})
	require.Error(t, err)
}

func TestClaudeStoreRejectsCrossEngineSelection(t *testing.T) {
	db, b, _ := completionFixture(t)
	ctx := context.Background()
	op := store.ManagedOperation{OperationID: "claude-change", RequestID: "claude-change", Method: "POST", CanonicalPath: "/v1/managed-agents/" + b.AgentID + "/restart", CanonicalBodyHash: "hash", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	_, _, err := db.ReserveEmbeddedModelSelection(ctx, b.AgentID, store.ManagedModel{Provider: "claude-code", ID: "sonnet"}, op)
	require.Error(t, err)
	saved, err := db.ManagedAgent(ctx, b.AgentID)
	require.NoError(t, err)
	require.Equal(t, "openai-codex", saved.SelectedModel.Provider)
	_, err = db.ManagedOperation(ctx, op.OperationID)
	require.Error(t, err)
}

func TestClaudeLaunchPreflightAndModelSelection(t *testing.T) {
	ctx := context.Background()
	db, b, path := completionFixture(t)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	binary, err := filepath.Abs(filepath.Join("..", "..", "..", "tests", "fixtures", "fake-claude", "claude.mjs"))
	require.NoError(t, err)
	t.Setenv("MECHA_CLAUDE_BIN", binary)
	record := filepath.Join(t.TempDir(), "calls.jsonl")
	t.Setenv("FAKE_CLAUDE_RECORD", record)
	saved, err := db.ManagedAgent(ctx, b.AgentID)
	require.NoError(t, err)
	started := make(chan store.ManagedAgentRecord, 1)
	m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest,
		Config:                Config{SessionRoot: t.TempDir(), AllowedWorkingDirectoryRoots: []string{saved.Cwd}},
		CatalogSource:         StoreCatalogSource{Store: db},
		EmbeddedRunnerFactory: func(_ context.Context, record store.ManagedAgentRecord) error { started <- record; return nil },
	})
	require.NoError(t, err)
	defer m.Close(ctx)
	result, err := m.Launch(ctx, LaunchRequest{Name: "claude-worker", Cwd: saved.Cwd, Runtime: "embedded", Model: &Model{Provider: "claude-code", ID: "sonnet"}})
	require.NoError(t, err)
	require.Equal(t, "embedded", result.Agent.Runtime)
	require.Equal(t, "claude-code", result.Agent.SelectedModel.Provider)
	select {
	case actual := <-started:
		require.Equal(t, "sonnet", actual.SelectedModel.ID)
	case <-time.After(5 * time.Second):
		t.Fatal("runner not started")
	}
	evidence, err := os.ReadFile(record)
	require.NoError(t, err)
	require.Contains(t, string(evidence), `"argv":["auth","status","--json"]`)
	// Reuse the already-running fixture to exercise manager and store together.
	m.mu.Lock()
	if m.embeddedOwners == nil {
		m.embeddedOwners = make(map[string]embeddedOwner)
	}
	m.embeddedOwners[b.AgentID] = embeddedOwner{binding: b, identity: "wake-fixture"}
	m.mu.Unlock()
	req := RestartRequest{AgentID: b.AgentID, RequestID: "engine-change-1", Method: "POST", CanonicalPath: "/v1/managed-agents/" + b.AgentID + "/restart", CanonicalBodyHash: "change", Model: &Model{Provider: "claude-code", ID: "haiku"}}
	_, err = m.ReserveRestart(ctx, req)
	var engine *EngineSelectionError
	require.ErrorAs(t, err, &engine)
	// A fixture-only SQL update establishes the other engine without exercising turn dispatch.
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()
	_, err = raw.Exec(`UPDATE managed_agents SET selected_model_provider='claude-code',selected_model_id='sonnet' WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	req.RequestID = "same-engine-change"
	changed, err := m.ReserveRestart(ctx, req)
	require.NoError(t, err)
	require.Equal(t, "haiku", changed.Agent.SelectedModel.ID)
	replay, err := m.ReserveRestart(ctx, req)
	require.NoError(t, err)
	require.Equal(t, changed.Operation.ResponseBody, replay.Operation.ResponseBody)
	req.RequestID = "cross-engine-back"
	req.Model = &Model{Provider: "google", ID: "gemini-3.8-flash"}
	// The explicit Pi choice must be selectable before testing its engine fence.
	m.catalog = CatalogFromModels(Model{Provider: "google", ID: "gemini-3.8-flash", Available: true})
	_, err = m.ReserveRestart(ctx, req)
	require.ErrorAs(t, err, &engine)
	final, err := db.ManagedAgent(ctx, b.AgentID)
	require.NoError(t, err)
	require.Equal(t, "claude-code", final.SelectedModel.Provider)
	require.Equal(t, "haiku", final.SelectedModel.ID)
}

func TestClaudeLaunchReportsProcessFenceAndCLIUnavailable(t *testing.T) {
	ctx := context.Background()
	db, b, _ := completionFixture(t)
	saved, err := db.ManagedAgent(ctx, b.AgentID)
	require.NoError(t, err)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	t.Setenv("MECHA_CLAUDE_BIN", "/no/such/claude")
	m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: t.TempDir(), AllowedWorkingDirectoryRoots: []string{saved.Cwd}}, CatalogSource: StoreCatalogSource{Store: db}})
	require.NoError(t, err)
	defer m.Close(ctx)
	request := LaunchRequest{Name: "fence-test", Cwd: saved.Cwd, Runtime: "embedded", Model: &Model{Provider: "claude-code", ID: "sonnet"}}
	_, err = m.Launch(ctx, request)
	var unavailable *ClaudeUnavailableError
	require.ErrorAs(t, err, &unavailable)
	m.claudeCodeProcesses.fence()
	_, err = m.Launch(ctx, request)
	var fenced *ClaudeAdmissionFencedError
	require.ErrorAs(t, err, &fenced)
	require.ErrorIs(t, err, errClaudeCodeGroupUnconfirmed)
	require.NotContains(t, err.Error(), "sign in")
}
