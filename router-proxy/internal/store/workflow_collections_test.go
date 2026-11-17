package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkflowCollectionCRUD(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// 1. Verify default collection exists out of the box and has 0 workflows
	defaultCol, err := s.GetWorkflowCollection(ctx, DefaultWorkflowCollectionID)
	require.NoError(t, err)
	require.NotNil(t, defaultCol)
	require.Equal(t, DefaultWorkflowCollectionID, defaultCol.ID)
	require.Equal(t, "Default", defaultCol.Name)
	require.Equal(t, 0, defaultCol.WorkflowCount)

	// 2. Create collection
	createReq := WorkflowCollectionRequest{
		Name:        "ops-collection",
		Description: "Operations workflows",
	}
	col1, err := s.CreateWorkflowCollection(ctx, createReq)
	require.NoError(t, err)
	require.NotEmpty(t, col1.ID)
	require.Equal(t, "ops-collection", col1.Name)
	require.Equal(t, "Operations workflows", col1.Description)
	require.Equal(t, 0, col1.WorkflowCount)
	require.NotEmpty(t, col1.CreatedAt)
	require.NotEmpty(t, col1.UpdatedAt)

	// Name conflict
	_, err = s.CreateWorkflowCollection(ctx, createReq)
	require.Error(t, err)
	var nameConflict *WorkflowCollectionNameConflictError
	require.ErrorAs(t, err, &nameConflict)

	// 3. Get collection
	fetched, err := s.GetWorkflowCollection(ctx, col1.ID)
	require.NoError(t, err)
	require.NotNil(t, fetched)
	require.Equal(t, col1.ID, fetched.ID)
	require.Equal(t, col1.Name, fetched.Name)
	require.Equal(t, col1.Description, fetched.Description)
	require.Equal(t, 0, fetched.WorkflowCount)

	// Non-existent get
	missing, err := s.GetWorkflowCollection(ctx, "non-existent")
	require.NoError(t, err)
	require.Nil(t, missing)

	// 4. Create workflow in default collection and in col1 to verify workflow_count tracking
	wf1, err := s.CreateWorkflow(ctx, WorkflowRequest{
		Name: "wf-default",
	})
	require.NoError(t, err)
	require.Equal(t, DefaultWorkflowCollectionID, wf1.CollectionID)

	wf2, err := s.CreateWorkflow(ctx, WorkflowRequest{
		CollectionID: col1.ID,
		Name:         "wf-ops-1",
	})
	require.NoError(t, err)
	require.Equal(t, col1.ID, wf2.CollectionID)

	wf3, err := s.CreateWorkflow(ctx, WorkflowRequest{
		CollectionID: col1.ID,
		Name:         "wf-ops-2",
	})
	require.NoError(t, err)
	require.Equal(t, col1.ID, wf3.CollectionID)

	// List collections: verify default is first and workflow counts are tracked
	cols, err := s.ListWorkflowCollections(ctx)
	require.NoError(t, err)
	require.Len(t, cols, 2)
	require.Equal(t, DefaultWorkflowCollectionID, cols[0].ID)
	require.Equal(t, 1, cols[0].WorkflowCount)
	require.Equal(t, col1.ID, cols[1].ID)
	require.Equal(t, 2, cols[1].WorkflowCount)

	// Get collection tracks count too
	fetchedOps, err := s.GetWorkflowCollection(ctx, col1.ID)
	require.NoError(t, err)
	require.Equal(t, 2, fetchedOps.WorkflowCount)

	// 5. Update collection (name/desc)
	updateReq := WorkflowCollectionRequest{
		Name:        "ops-collection-v2",
		Description: "Updated operations workflows",
	}
	updated, err := s.UpdateWorkflowCollection(ctx, col1.ID, updateReq)
	require.NoError(t, err)
	require.Equal(t, "ops-collection-v2", updated.Name)
	require.Equal(t, "Updated operations workflows", updated.Description)
	require.Equal(t, 2, updated.WorkflowCount)

	// Verify update is persisted
	fetchedUpdated, err := s.GetWorkflowCollection(ctx, col1.ID)
	require.NoError(t, err)
	require.Equal(t, "ops-collection-v2", fetchedUpdated.Name)
	require.Equal(t, "Updated operations workflows", fetchedUpdated.Description)

	// 6. Verify default collection cannot be renamed or deleted
	_, err = s.UpdateWorkflowCollection(ctx, DefaultWorkflowCollectionID, WorkflowCollectionRequest{
		Name: "RenamedDefault",
	})
	require.Error(t, err)
	var cannotModifyDefault *CannotModifyDefaultCollectionError
	require.ErrorAs(t, err, &cannotModifyDefault)

	err = s.DeleteWorkflowCollection(ctx, DefaultWorkflowCollectionID, false)
	require.Error(t, err)
	var cannotDeleteDefault *CannotDeleteDefaultCollectionError
	require.ErrorAs(t, err, &cannotDeleteDefault)

	err = s.DeleteWorkflowCollection(ctx, DefaultWorkflowCollectionID, true)
	require.Error(t, err)
	require.ErrorAs(t, err, &cannotDeleteDefault)
}

func TestWorkflowCollectionDelete(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// 1. Test deleting empty collection (succeeds)
	emptyCol, err := s.CreateWorkflowCollection(ctx, WorkflowCollectionRequest{
		Name: "empty-collection",
	})
	require.NoError(t, err)

	err = s.DeleteWorkflowCollection(ctx, emptyCol.ID, false)
	require.NoError(t, err)

	missing, err := s.GetWorkflowCollection(ctx, emptyCol.ID)
	require.NoError(t, err)
	require.Nil(t, missing)

	// 2. Create non-empty collection with a workflow and states
	nonEmptyCol, err := s.CreateWorkflowCollection(ctx, WorkflowCollectionRequest{
		Name: "non-empty-collection",
	})
	require.NoError(t, err)

	wf, err := s.CreateWorkflow(ctx, WorkflowRequest{
		CollectionID: nonEmptyCol.ID,
		Name:         "flow-in-col",
	})
	require.NoError(t, err)

	st, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{
		WorkflowID: &wf.ID,
		Name:       "Initial",
	})
	require.NoError(t, err)
	require.NotEmpty(t, st.ID)

	// Deleting non-empty collection with cascade=false fails with CollectionNotEmptyError
	err = s.DeleteWorkflowCollection(ctx, nonEmptyCol.ID, false)
	require.Error(t, err)
	var notEmptyErr *CollectionNotEmptyError
	require.ErrorAs(t, err, &notEmptyErr)
	require.Equal(t, nonEmptyCol.ID, notEmptyErr.ID)
	require.Equal(t, 1, notEmptyErr.Count)

	// Verify workflow and collection still exist
	colCheck, err := s.GetWorkflowCollection(ctx, nonEmptyCol.ID)
	require.NoError(t, err)
	require.NotNil(t, colCheck)

	wfCheck, err := s.GetWorkflow(ctx, wf.ID)
	require.NoError(t, err)
	require.NotNil(t, wfCheck)

	// 3. Deleting non-empty collection with cascade=true succeeds and removes its workflows
	err = s.DeleteWorkflowCollection(ctx, nonEmptyCol.ID, true)
	require.NoError(t, err)

	colCheck, err = s.GetWorkflowCollection(ctx, nonEmptyCol.ID)
	require.NoError(t, err)
	require.Nil(t, colCheck)

	wfCheck, err = s.GetWorkflow(ctx, wf.ID)
	require.NoError(t, err)
	require.Nil(t, wfCheck)

	stCheck, err := s.GetWorkflowState(ctx, st.ID)
	require.NoError(t, err)
	require.Nil(t, stCheck)

	// 4. Verify deleting a collection cleans up its git_sync_targets entry (workflows:<id>)
	gitSyncCol, err := s.CreateWorkflowCollection(ctx, WorkflowCollectionRequest{
		Name: "git-sync-cleanup-col",
	})
	require.NoError(t, err)

	syncTarget := "workflows:" + gitSyncCol.ID
	_, err = s.SetGitSyncTarget(ctx, syncTarget, "https://github.com/example/test-workflows.git")
	require.NoError(t, err)

	// Confirm target exists
	targetRow, err := s.GetGitSyncTarget(ctx, syncTarget)
	require.NoError(t, err)
	require.NotNil(t, targetRow)
	require.Equal(t, "https://github.com/example/test-workflows.git", targetRow.RepoURL)

	// Delete collection (empty collection)
	err = s.DeleteWorkflowCollection(ctx, gitSyncCol.ID, false)
	require.NoError(t, err)

	// Verify git_sync_targets entry is removed
	targetRowAfter, err := s.GetGitSyncTarget(ctx, syncTarget)
	require.NoError(t, err)
	require.Nil(t, targetRowAfter)
}

func TestWorkflowMoveAndDuplicateAcrossCollections(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	colA, err := s.CreateWorkflowCollection(ctx, WorkflowCollectionRequest{
		Name: "collection-a",
	})
	require.NoError(t, err)

	colB, err := s.CreateWorkflowCollection(ctx, WorkflowCollectionRequest{
		Name: "collection-b",
	})
	require.NoError(t, err)

	// Create workflow in colA with a state
	wf, err := s.CreateWorkflow(ctx, WorkflowRequest{
		CollectionID: colA.ID,
		Name:         "transferrable-workflow",
		Description:  "Original description",
	})
	require.NoError(t, err)
	require.Equal(t, colA.ID, wf.CollectionID)

	st, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{
		WorkflowID: &wf.ID,
		Name:       "State1",
		EntryText:  "Hello from State 1",
	})
	require.NoError(t, err)

	// Duplicate workflow into collection B
	dup, err := s.DuplicateWorkflow(ctx, wf.ID, colB.ID, "duplicated-in-b")
	require.NoError(t, err)
	require.NotNil(t, dup)
	require.Equal(t, colB.ID, dup.CollectionID)
	require.Equal(t, "duplicated-in-b", dup.Name)
	require.NotEqual(t, wf.ID, dup.ID)

	// Verify states were duplicated into dup
	dupStates, err := s.ListWorkflowStatesByWorkflow(ctx, dup.ID)
	require.NoError(t, err)
	require.Len(t, dupStates, 1)
	require.Equal(t, "State1", dupStates[0].Name)
	require.NotEqual(t, st.ID, dupStates[0].ID)

	// Verify original workflow is still in colA
	origWf, err := s.GetWorkflow(ctx, wf.ID)
	require.NoError(t, err)
	require.Equal(t, colA.ID, origWf.CollectionID)

	// Create a third collection for testing MoveWorkflow
	colC, err := s.CreateWorkflowCollection(ctx, WorkflowCollectionRequest{
		Name: "collection-c",
	})
	require.NoError(t, err)

	// Move workflow from colA to colC
	moved, err := s.MoveWorkflow(ctx, wf.ID, colC.ID)
	require.NoError(t, err)
	require.NotNil(t, moved)
	require.Equal(t, colC.ID, moved.CollectionID)
	require.Equal(t, wf.ID, moved.ID)

	// Verify workflow lists in colA and colC reflect the move
	wfsInA, err := s.ListWorkflowsByCollection(ctx, colA.ID)
	require.NoError(t, err)
	require.Empty(t, wfsInA)

	wfsInC, err := s.ListWorkflowsByCollection(ctx, colC.ID)
	require.NoError(t, err)
	require.Len(t, wfsInC, 1)
	require.Equal(t, wf.ID, wfsInC[0].ID)

	// Test moving with name conflict in destination collection
	// Create another workflow in colB with the same name "transferrable-workflow"
	_, err = s.CreateWorkflow(ctx, WorkflowRequest{
		CollectionID: colB.ID,
		Name:         "transferrable-workflow",
	})
	require.NoError(t, err)

	// Attempt to move wf into colB - should fail with WorkflowNameConflictError
	_, err = s.MoveWorkflow(ctx, wf.ID, colB.ID)
	require.Error(t, err)
	var wfNameConflict *WorkflowNameConflictError
	require.ErrorAs(t, err, &wfNameConflict)
	require.Equal(t, "transferrable-workflow", wfNameConflict.Name)

	// Edge case: Moving a workflow to its current collection is an idempotent no-op and does not cause a self-conflict
	sameColMoved, err := s.MoveWorkflow(ctx, wf.ID, colC.ID)
	require.NoError(t, err)
	require.NotNil(t, sameColMoved)
	require.Equal(t, colC.ID, sameColMoved.CollectionID)
	require.Equal(t, wf.ID, sameColMoved.ID)
	require.Equal(t, "transferrable-workflow", sameColMoved.Name)
}

func TestWorkflowDuplicateComplexStateGraph(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	colSource, err := s.CreateWorkflowCollection(ctx, WorkflowCollectionRequest{
		Name: "source-collection",
	})
	require.NoError(t, err)

	colTarget, err := s.CreateWorkflowCollection(ctx, WorkflowCollectionRequest{
		Name: "target-collection",
	})
	require.NoError(t, err)

	wfOriginal, err := s.CreateWorkflow(ctx, WorkflowRequest{
		CollectionID: colSource.ID,
		Name:         "interconnected-workflow",
	})
	require.NoError(t, err)

	// Create state A and state B interconnected: State A -> State B, State B -> State A
	stateA, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{
		WorkflowID: &wfOriginal.ID,
		Name:       "State A",
	})
	require.NoError(t, err)

	stateB, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{
		WorkflowID: &wfOriginal.ID,
		Name:       "State B",
		Transitions: []WorkflowTransitionConfig{
			{TargetStateID: stateA.ID, Description: "Go from B to A"},
		},
	})
	require.NoError(t, err)

	// Update state A to point to state B
	stateAUpdated, err := s.UpdateWorkflowState(ctx, stateA.ID, WorkflowStateRequest{
		WorkflowID: &wfOriginal.ID,
		Name:       "State A",
		Transitions: []WorkflowTransitionConfig{
			{TargetStateID: stateB.ID, Description: "Go from A to B"},
		},
	})
	require.NoError(t, err)
	require.Len(t, stateAUpdated.Transitions, 1)
	require.Equal(t, stateB.ID, stateAUpdated.Transitions[0].TargetStateID)

	// Set entry state to state A
	_, err = s.UpdateWorkflow(ctx, wfOriginal.ID, WorkflowRequest{
		Name:         wfOriginal.Name,
		EntryStateID: &stateA.ID,
	})
	require.NoError(t, err)

	// Duplicate into colTarget
	duplicatedWf, err := s.DuplicateWorkflow(ctx, wfOriginal.ID, colTarget.ID, "interconnected-duplicated")
	require.NoError(t, err)
	require.NotNil(t, duplicatedWf)
	require.NotEqual(t, wfOriginal.ID, duplicatedWf.ID)
	require.Equal(t, colTarget.ID, duplicatedWf.CollectionID)
	require.NotNil(t, duplicatedWf.EntryStateID)

	// Fetch states of duplicated workflow
	dupStates, err := s.ListWorkflowStatesByWorkflow(ctx, duplicatedWf.ID)
	require.NoError(t, err)
	require.Len(t, dupStates, 2)

	var dupStateA, dupStateB *WorkflowState
	for i := range dupStates {
		if dupStates[i].Name == "State A" {
			dupStateA = &dupStates[i]
		} else if dupStates[i].Name == "State B" {
			dupStateB = &dupStates[i]
		}
	}
	require.NotNil(t, dupStateA, "expected duplicated State A to exist")
	require.NotNil(t, dupStateB, "expected duplicated State B to exist")

	// Ensure new state IDs are distinct from original IDs
	require.NotEqual(t, stateA.ID, dupStateA.ID)
	require.NotEqual(t, stateB.ID, dupStateB.ID)

	// Assert entry state remapped
	require.Equal(t, dupStateA.ID, *duplicatedWf.EntryStateID)

	// Assert State A -> State B transition remapped to new state B ID
	require.Len(t, dupStateA.Transitions, 1)
	require.Equal(t, dupStateB.ID, dupStateA.Transitions[0].TargetStateID)
	require.NotEqual(t, stateB.ID, dupStateA.Transitions[0].TargetStateID)
	require.Equal(t, "Go from A to B", dupStateA.Transitions[0].Description)

	// Assert State B -> State A transition remapped to new state A ID
	require.Len(t, dupStateB.Transitions, 1)
	require.Equal(t, dupStateA.ID, dupStateB.Transitions[0].TargetStateID)
	require.NotEqual(t, stateA.ID, dupStateB.Transitions[0].TargetStateID)
	require.Equal(t, "Go from B to A", dupStateB.Transitions[0].Description)
}

func TestWorkflowCollectionsIsolationGitSync(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	col1, err := s.CreateWorkflowCollection(ctx, WorkflowCollectionRequest{
		Name: "team-alpha",
	})
	require.NoError(t, err)

	col2, err := s.CreateWorkflowCollection(ctx, WorkflowCollectionRequest{
		Name: "team-beta",
	})
	require.NoError(t, err)

	// Add workflows in team-alpha (col1)
	wfAlpha, err := s.CreateWorkflow(ctx, WorkflowRequest{
		CollectionID: col1.ID,
		Name:         "alpha-workflow",
		Description:  "Alpha description",
	})
	require.NoError(t, err)

	stAlpha, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{
		WorkflowID: &wfAlpha.ID,
		Name:       "AlphaStart",
		EntryText:  "Starting Alpha",
	})
	require.NoError(t, err)

	// Add workflows in team-beta (col2)
	wfBeta, err := s.CreateWorkflow(ctx, WorkflowRequest{
		CollectionID: col2.ID,
		Name:         "beta-workflow",
		Description:  "Beta description",
	})
	require.NoError(t, err)

	stBeta, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{
		WorkflowID: &wfBeta.ID,
		Name:       "BetaStart",
		EntryText:  "Starting Beta",
	})
	require.NoError(t, err)

	// Add workflow in default collection
	wfDefault, err := s.CreateWorkflow(ctx, WorkflowRequest{
		Name:        "default-workflow",
		Description: "Default description",
	})
	require.NoError(t, err)

	// 1. Export collection 1 (team-alpha)
	exportAlpha, err := s.ExportWorkflowCollection(ctx, col1.ID)
	require.NoError(t, err)
	require.NotNil(t, exportAlpha)
	require.Len(t, exportAlpha.Workflows, 1)
	require.Equal(t, "alpha-workflow", exportAlpha.Workflows[0].Workflow.Name)

	// 2. Export collection 2 (team-beta)
	exportBeta, err := s.ExportWorkflowCollection(ctx, col2.ID)
	require.NoError(t, err)
	require.NotNil(t, exportBeta)
	require.Len(t, exportBeta.Workflows, 1)
	require.Equal(t, "beta-workflow", exportBeta.Workflows[0].Workflow.Name)

	// 3. Prepare replacement bundles for col1 (team-alpha)
	newAlphaBundle := WorkflowExportBundle{
		SchemaVersion: 1,
		Workflow: WorkflowInfoExport{
			Name:             "new-alpha-workflow",
			Description:      "Brand new alpha flow",
			SnoozeDurationMS: 450000,
		},
		EntryStateKey: "init",
		States: []WorkflowStateExport{
			{
				Key:          "init",
				Name:         "InitState",
				EntryText:    "Init Alpha text",
				IsEntryState: true,
			},
		},
	}

	// 4. Replace workflows in col1
	err = s.ReplaceWorkflowsInCollection(ctx, col1.ID, []WorkflowExportBundle{newAlphaBundle})
	require.NoError(t, err)

	// 5. Verify col1 has replaced workflows
	wfsInCol1, err := s.ListWorkflowsByCollection(ctx, col1.ID)
	require.NoError(t, err)
	require.Len(t, wfsInCol1, 1)
	require.Equal(t, "new-alpha-workflow", wfsInCol1[0].Name)
	require.Equal(t, col1.ID, wfsInCol1[0].CollectionID)

	// Old wfAlpha should no longer exist
	oldWfCheck, err := s.GetWorkflow(ctx, wfAlpha.ID)
	require.NoError(t, err)
	require.Nil(t, oldWfCheck)

	oldStCheck, err := s.GetWorkflowState(ctx, stAlpha.ID)
	require.NoError(t, err)
	require.Nil(t, oldStCheck)

	// 6. Verify col2 (team-beta) is completely UNTOUCHED
	wfsInCol2, err := s.ListWorkflowsByCollection(ctx, col2.ID)
	require.NoError(t, err)
	require.Len(t, wfsInCol2, 1)
	require.Equal(t, wfBeta.ID, wfsInCol2[0].ID)
	require.Equal(t, "beta-workflow", wfsInCol2[0].Name)

	stBetaCheck, err := s.GetWorkflowState(ctx, stBeta.ID)
	require.NoError(t, err)
	require.NotNil(t, stBetaCheck)
	require.Equal(t, "BetaStart", stBetaCheck.Name)

	// 7. Verify default collection is also completely UNTOUCHED
	wfDefaultCheck, err := s.GetWorkflow(ctx, wfDefault.ID)
	require.NoError(t, err)
	require.NotNil(t, wfDefaultCheck)
	require.Equal(t, "default-workflow", wfDefaultCheck.Name)
	require.Equal(t, DefaultWorkflowCollectionID, wfDefaultCheck.CollectionID)
}
