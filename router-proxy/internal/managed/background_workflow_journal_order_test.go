package managed

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/stretchr/testify/require"
)

func TestBackgroundWorkflowJournalFailureNeverStartsWorker(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "db.sqlite")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	wf, err := db.CreateWorkflow(ctx, store.WorkflowRequest{Name: "race"})
	require.NoError(t, err)
	state, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "entry", WorkflowID: &wf.ID})
	require.NoError(t, err)
	_, err = db.UpdateWorkflow(ctx, wf.ID, store.WorkflowRequest{Name: "race", EntryStateID: &state.ID})
	require.NoError(t, err)
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()
	_, err = raw.Exec(`CREATE TRIGGER reject_bg_journal BEFORE INSERT ON workflow_journal_runs BEGIN SELECT RAISE(ABORT, 'journal failure'); END`)
	require.NoError(t, err)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	started := make(chan struct{}, 1)
	m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), CodexCredentialStore: testfixture.CodexAdmission(t), EmbeddedRunnerFactory: func(context.Context, store.ManagedAgentRecord) error { started <- struct{}{}; return nil }})
	require.NoError(t, err)
	defer m.Close(ctx)
	_, err = m.LaunchBackgroundWorkflow(ctx, wf.ID, nil, root, &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"})
	require.Error(t, err)
	select {
	case <-started:
		t.Fatal("worker started after journal transaction failed")
	case <-time.After(500 * time.Millisecond):
	}
	var count int
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM managed_agents WHERE is_background=1`).Scan(&count))
	require.Zero(t, count)
}

func TestBackgroundWorkflowRunnerSeesCommittedJournal(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "db.sqlite")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	wf, err := db.CreateWorkflow(ctx, store.WorkflowRequest{Name: "ordered"})
	require.NoError(t, err)
	state, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "entry", WorkflowID: &wf.ID})
	require.NoError(t, err)
	_, err = db.UpdateWorkflow(ctx, wf.ID, store.WorkflowRequest{Name: "ordered", EntryStateID: &state.ID})
	require.NoError(t, err)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	seen := make(chan error, 1)
	m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), CodexCredentialStore: testfixture.CodexAdmission(t), EmbeddedRunnerFactory: func(ctx context.Context, a store.ManagedAgentRecord) error {
		binding, e := db.WorkflowBindingVersion(ctx, a.AgentID)
		if e != nil {
			seen <- e
			return nil
		}
		if binding.StateID != state.ID {
			seen <- fmt.Errorf("state %q", binding.StateID)
			return nil
		}
		runs, e := db.ListWorkflowJournalRuns(ctx, a.AgentID, store.WorkflowJournalFilter{})
		if e != nil {
			seen <- e
			return nil
		}
		if len(runs.Runs) != 1 {
			seen <- fmt.Errorf("journal runs: %d", len(runs.Runs))
			return nil
		}
		seen <- nil
		return nil
	}})
	require.NoError(t, err)
	defer m.Close(ctx)
	_, err = m.LaunchBackgroundWorkflow(ctx, wf.ID, nil, root, &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"})
	require.NoError(t, err)
	select {
	case err := <-seen:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
}
