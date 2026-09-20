package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var workflowTestSnapshot = []byte(`{"format":"mecha.workflow-definition/v1","inputs":[],"entryStateId":"123e4567-e89b-42d3-a456-426614170001","states":[{"stateId":"123e4567-e89b-42d3-a456-426614170001","name":"hello","entryText":"Say hello.","compactOnExit":false,"exits":[{"targetKind":"complete","description":"Finish."}]}]}`)
var workflowValidResult = []byte(`{"valid":true,"issues":[]}`)
var workflowInvalidResult = []byte(`{"valid":false,"issues":[{"path":"snapshot.states","code":"state_required","message":"A state is required."}]}`)

func workflowMutationFor(method string, n int, path string, response string) WorkflowMutation {
	return WorkflowMutation{Method: method, RequestID: testUUID(fmt.Sprintf("%04d", n)), CanonicalPath: path, CanonicalBodyHash: strings.Repeat(fmt.Sprintf("%x", n%16), 64), ResponseStatus: 200, ResponseBody: []byte(response)}
}

func createWorkflowForTest(t *testing.T, durable *Store, id, name string, n int) WorkflowMutationResult {
	t.Helper()
	result, err := durable.CreateWorkflowDefinition(context.Background(), WorkflowDefinitionCreate{DefinitionID: id, Name: name, Description: "description", Status: "disabled", SnapshotJSON: workflowTestSnapshot, ValidationValid: true, ValidationJSON: workflowValidResult}, workflowMutationFor("POST", n, "/v1/workflow-definitions", `{"ok":true}`))
	require.NoError(t, err)
	return result
}

func TestWorkflowDefinitionMigrationFoundation(t *testing.T) {
	durable := openTestStore(t)
	for _, table := range []string{"workflow_definitions", "workflow_definition_revisions", "workflow_runs", "workflow_state_occurrences", "workflow_entry_notifications", "workflow_pending_transitions", "workflow_journal_events", "workflow_http_mutations"} {
		var count int
		require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count))
		require.Equal(t, 1, count, table)
	}
	var partial string
	require.NoError(t, durable.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name='workflow_runs_one_nonterminal_per_agent_idx'`).Scan(&partial))
	require.Contains(t, partial, "WHERE status='active'")
	var foreignProblems int
	require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignProblems))
	require.Zero(t, foreignProblems)
}

func TestWorkflowDefinitionPersistsValidationResult(t *testing.T) {
	durable := openTestStore(t)
	created := createWorkflowForTest(t, durable, testUUID("1001"), "validated", 1)
	require.Equal(t, int64(1), created.Definition.Version)
	require.True(t, created.Revision.ValidationValid)
	require.JSONEq(t, string(workflowValidResult), string(created.Revision.ValidationJSON))
	definition, revision, err := durable.WorkflowDefinition(context.Background(), created.Definition.DefinitionID)
	require.NoError(t, err)
	require.Equal(t, created.Definition.DefinitionID, definition.DefinitionID)
	require.JSONEq(t, string(workflowValidResult), string(revision.ValidationJSON))
}

func TestWorkflowDefinitionUniqueNamesAndDeterministicListing(t *testing.T) {
	durable := openTestStore(t)
	ctx := context.Background()
	createWorkflowForTest(t, durable, testUUID("1101"), "zeta", 11)
	createWorkflowForTest(t, durable, testUUID("1102"), "alpha", 12)
	_, err := durable.CreateWorkflowDefinition(ctx, WorkflowDefinitionCreate{DefinitionID: testUUID("1103"), Name: "alpha", Status: "disabled", SnapshotJSON: workflowTestSnapshot, ValidationJSON: workflowValidResult, ValidationValid: true}, workflowMutationFor("POST", 13, "/v1/workflow-definitions", `{}`))
	var conflict *WorkflowDefinitionNameConflictError
	require.ErrorAs(t, err, &conflict)
	definitions, err := durable.ListWorkflowDefinitions(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"alpha", "zeta"}, []string{definitions[0].Name, definitions[1].Name})

	mutation := workflowMutationFor("POST", 14, "/v1/workflow-definitions", `{"definition":"first"}`)
	first, err := durable.CreateWorkflowDefinition(ctx, WorkflowDefinitionCreate{DefinitionID: testUUID("1104"), Name: "replay", Status: "disabled", SnapshotJSON: workflowTestSnapshot, ValidationJSON: workflowValidResult, ValidationValid: true}, mutation)
	require.NoError(t, err)
	replayed, err := durable.CreateWorkflowDefinition(ctx, WorkflowDefinitionCreate{DefinitionID: testUUID("9999"), Name: "would-conflict", Status: "disabled", SnapshotJSON: workflowTestSnapshot, ValidationJSON: workflowValidResult, ValidationValid: true}, mutation)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.Equal(t, first.ResponseBody, replayed.ResponseBody)
	mutation.CanonicalBodyHash = strings.Repeat("f", 64)
	_, err = durable.CreateWorkflowDefinition(ctx, WorkflowDefinitionCreate{DefinitionID: testUUID("1105"), Name: "changed", Status: "disabled", SnapshotJSON: workflowTestSnapshot, ValidationJSON: workflowValidResult, ValidationValid: true}, mutation)
	var idem *WorkflowIdempotencyConflictError
	require.ErrorAs(t, err, &idem)
	definitions, err = durable.ListWorkflowDefinitions(ctx)
	require.NoError(t, err)
	require.Len(t, definitions, 3, "replay and conflicting reuse create no extra definition")
}

func TestWorkflowDefinitionUpdateIsImmutableOptimisticAndRetainsLatestThreePlusPinned(t *testing.T) {
	durable, fence := readyManagedDirectFixture(t)
	ctx := context.Background()
	definitionID := testUUID("1201")
	createWorkflowForTest(t, durable, definitionID, "retained", 21)
	insertWorkflowRunForTest(t, durable, fence, definitionID, 1, "active")

	for revision := int64(1); revision < 5; revision++ {
		validation := workflowValidResult
		valid := true
		if revision == 2 {
			validation = workflowInvalidResult
			valid = false
		}
		result, err := durable.UpdateWorkflowDefinition(ctx, WorkflowDefinitionUpdate{DefinitionID: definitionID, ExpectedVersion: revision, ExpectedRevision: revision, Name: "retained", Description: fmt.Sprintf("revision %d", revision+1), Status: "disabled", SnapshotJSON: workflowTestSnapshot, ValidationValid: valid, ValidationJSON: validation}, workflowMutationFor("PUT", 21+int(revision), "/v1/workflow-definitions/"+definitionID, fmt.Sprintf(`{"revision":%d}`, revision+1)))
		require.NoError(t, err)
		require.Equal(t, revision+1, result.Revision.Revision)
	}
	revisions, err := durable.ListWorkflowDefinitionRevisions(ctx, definitionID)
	require.NoError(t, err)
	var numbers []int64
	for _, revision := range revisions {
		numbers = append(numbers, revision.Revision)
	}
	require.Equal(t, []int64{5, 4, 3, 1}, numbers, "pinned revision one plus latest three are retained")

	_, err = durable.UpdateWorkflowDefinition(ctx, WorkflowDefinitionUpdate{DefinitionID: definitionID, ExpectedVersion: 4, ExpectedRevision: 5, Name: "retained", Status: "disabled", SnapshotJSON: workflowTestSnapshot, ValidationJSON: workflowValidResult, ValidationValid: true}, workflowMutationFor("PUT", 30, "/v1/workflow-definitions/"+definitionID, `{}`))
	var stale *WorkflowDefinitionRevisionConflictError
	require.ErrorAs(t, err, &stale)
	_, err = durable.db.Exec(`UPDATE workflow_definition_revisions SET validation_valid=0 WHERE definition_id=? AND revision=1`, definitionID)
	require.Error(t, err, "database trigger rejects revision mutation")
}

func TestWorkflowDefinitionHardDeleteBlocksActiveAndCascadesTerminalData(t *testing.T) {
	durable, fence := readyManagedDirectFixture(t)
	ctx := context.Background()
	definitionID := testUUID("1301")
	createWorkflowForTest(t, durable, definitionID, "deletable", 31)
	runID, entryID := insertWorkflowRunForTest(t, durable, fence, definitionID, 1, "active")
	mutation := workflowMutationFor("DELETE", 32, "/v1/workflow-definitions/"+definitionID, `{"deleted":true}`)
	_, err := durable.DeleteWorkflowDefinition(ctx, definitionID, mutation)
	var active *WorkflowDefinitionHasActiveRunError
	require.ErrorAs(t, err, &active)

	_, err = durable.db.Exec(`UPDATE workflow_state_occurrences SET status='closed',closed_at='2026-01-01T00:01:00.000Z' WHERE entry_id=?`, entryID)
	require.NoError(t, err)
	_, err = durable.db.Exec(`UPDATE workflow_runs SET status='completed',phase='terminal',current_state_id=NULL,current_entry_id=NULL,final_exit='complete',completed_at='2026-01-01T00:01:00.000Z',updated_at='2026-01-01T00:01:00.000Z' WHERE run_id=?`, runID)
	require.NoError(t, err)
	deleted, err := durable.DeleteWorkflowDefinition(ctx, definitionID, mutation)
	require.NoError(t, err)
	require.False(t, deleted.Replayed)
	replayed, err := durable.DeleteWorkflowDefinition(ctx, definitionID, mutation)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	for _, table := range []string{"workflow_definitions", "workflow_definition_revisions", "workflow_runs", "workflow_state_occurrences", "workflow_journal_events"} {
		var count int
		require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE `+map[string]string{"workflow_definitions": "definition_id", "workflow_definition_revisions": "definition_id", "workflow_runs": "run_id", "workflow_state_occurrences": "run_id", "workflow_journal_events": "run_id"}[table]+`=?`, map[string]string{"workflow_definitions": definitionID, "workflow_definition_revisions": definitionID, "workflow_runs": runID, "workflow_state_occurrences": runID, "workflow_journal_events": runID}[table]).Scan(&count))
		require.Zero(t, count, table)
	}
	_, _, err = durable.WorkflowDefinition(ctx, definitionID)
	require.True(t, errors.Is(err, sql.ErrNoRows))
}

func insertWorkflowRunForTest(t *testing.T, durable *Store, fence ManagedRuntimeFence, definitionID string, revision int64, status string) (string, string) {
	t.Helper()
	runID := testUUID("8001")
	entryID := testUUID("8002")
	stateID := testUUID("8003")
	now := "2026-01-01T00:00:00.000Z"
	phase := "active"
	var completed any
	var final any
	var currentState any = stateID
	var currentEntry any = entryID
	if status != "active" {
		phase = "terminal"
		completed = now
		final = "complete"
		currentState = nil
		currentEntry = nil
	}
	_, err := durable.db.Exec(`INSERT INTO workflow_runs(run_id,definition_id,revision,definition_name,agent_id,inputs_json,status,phase,version,current_state_id,current_entry_id,final_exit,launch_pi_session_id,launch_session_epoch,launch_process_pid,launch_process_start_marker,launch_process_incarnation,launch_runtime_generation,launch_manifest_revision,launch_manifest_hash,started_at,completed_at,updated_at) VALUES(?,?,?,?,?,'{}',?,?,1,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, runID, definitionID, revision, "name", fence.AgentID, status, phase, currentState, currentEntry, final, fence.PiSessionID, fence.SessionEpoch, fence.ProcessPID, fence.ProcessStartMarker, fence.ProcessIncarnation, fence.RuntimeGeneration, fence.ManifestRevision, fence.ManifestHash, now, completed, now)
	require.NoError(t, err)
	if status == "active" {
		_, err = durable.db.Exec(`INSERT INTO workflow_state_occurrences(entry_id,run_id,state_id,ordinal,status,entered_at) VALUES(?,?,?,1,'active',?)`, entryID, runID, stateID, now)
		require.NoError(t, err)
		_, err = durable.db.Exec(`INSERT INTO workflow_journal_events(run_id,sequence,semantic_key,event_type,occurred_at) VALUES(?,1,'launch','run_launched',?)`, runID, now)
		require.NoError(t, err)
	}
	return runID, entryID
}
