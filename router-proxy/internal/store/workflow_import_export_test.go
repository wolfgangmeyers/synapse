package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkflowImportExport(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// 1. Create a workflow with variables and states
	wf, err := s.CreateWorkflow(ctx, WorkflowRequest{
		Name:        "Deployment Pipeline",
		Description: "Build, test, deploy",
		Variables: &[]WorkflowVariableDefinition{
			{
				Name:         "ENV",
				Label:        "Environment",
				Description:  "Target environment",
				Type:         WorkflowVariableTypeText,
				Required:     true,
				DefaultValue: "staging",
			},
			{
				Name:         "TAG",
				Label:        "Image Tag",
				Description:  "Git commit or semver tag",
				Type:         WorkflowVariableTypeText,
				Required:     false,
				DefaultValue: "latest",
			},
		},
	})
	require.NoError(t, err)

	x1, y1 := 100.0, 150.0
	stateBuild, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{
		WorkflowID:   &wf.ID,
		Name:         "Build",
		Description:  "Compiles the binaries",
		EntryText:    "Compiling version ${TAG}",
		Goal:         "Produce build artifact",
		AllowedTools: []string{"bash", "read"},
		UIPosX:       &x1,
		UIPosY:       &y1,
		Reminders: []WorkflowReminderConfig{
			{
				ID:           "rem-1",
				Type:         WorkflowReminderOneShot,
				Body:         "Check build logs",
				DelaySeconds: 120,
			},
		},
		Monitors: []WorkflowMonitorConfig{
			{
				Name:                 "Build watcher",
				Description:          "Watches compilation",
				QuestionInstructions: "Is build failing?",
				NudgeTemplate:        "Please fix build error",
				IntervalSeconds:      60,
				CooldownSeconds:      30,
				MaxAlerts:            3,
				TrailingMessagesCount: 5,
			},
		},
	})
	require.NoError(t, err)

	x2, y2 := 400.0, 150.0
	exitFalse := false
	stateDeploy, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{
		WorkflowID:   &wf.ID,
		Name:         "Deploy",
		Description:  "Deploy to cluster",
		EntryText:    "Deploying to ${ENV}",
		Goal:         "Deploy containers",
		AllowedTools: []string{"bash"},
		AllowExit:    &exitFalse,
		UIPosX:       &x2,
		UIPosY:       &y2,
	})
	require.NoError(t, err)

	// Add transition from Build -> Deploy
	_, err = s.UpdateWorkflowState(ctx, stateBuild.ID, WorkflowStateRequest{
		Name:         stateBuild.Name,
		Description:  stateBuild.Description,
		EntryText:    stateBuild.EntryText,
		Goal:         stateBuild.Goal,
		AllowedTools: stateBuild.AllowedTools,
		Reminders:    stateBuild.Reminders,
		Monitors:     stateBuild.Monitors,
		Transitions: []WorkflowTransitionConfig{
			{
				TargetStateID: stateDeploy.ID,
				Description:   "Proceed to deploy",
			},
		},
	})
	require.NoError(t, err)

	// Set entry state on workflow
	_, err = s.UpdateWorkflow(ctx, wf.ID, WorkflowRequest{
		Name:         wf.Name,
		Description:  wf.Description,
		EntryStateID: &stateBuild.ID,
	})
	require.NoError(t, err)

	// 2. Export the workflow
	bundle, err := s.ExportWorkflow(ctx, wf.ID)
	require.NoError(t, err)
	require.NotNil(t, bundle)
	require.Equal(t, 1, bundle.SchemaVersion)
	require.NotEmpty(t, bundle.ExportedAt)
	require.Equal(t, "Deployment Pipeline", bundle.Workflow.Name)
	require.Equal(t, "Build, test, deploy", bundle.Workflow.Description)
	require.Len(t, bundle.Workflow.Variables, 2)
	require.Equal(t, "ENV", bundle.Workflow.Variables[0].Name)
	require.Equal(t, "TAG", bundle.Workflow.Variables[1].Name)

	require.Equal(t, stateBuild.ID, bundle.EntryStateKey)
	require.Len(t, bundle.States, 2)

	// Check exported states details
	var exportedBuild, exportedDeploy *WorkflowStateExport
	for i := range bundle.States {
		st := &bundle.States[i]
		if st.Name == "Build" {
			exportedBuild = st
		} else if st.Name == "Deploy" {
			exportedDeploy = st
		}
	}
	require.NotNil(t, exportedBuild)
	require.NotNil(t, exportedDeploy)
	require.True(t, exportedBuild.IsEntryState)
	require.False(t, exportedDeploy.IsEntryState)
	require.True(t, exportedBuild.AllowExit)
	require.False(t, exportedDeploy.AllowExit)
	require.Equal(t, 100.0, exportedBuild.UIPosX)
	require.Equal(t, 150.0, exportedBuild.UIPosY)
	require.Len(t, exportedBuild.Transitions, 1)
	require.Equal(t, exportedDeploy.Key, exportedBuild.Transitions[0].TargetStateKey)
	require.Equal(t, "Proceed to deploy", exportedBuild.Transitions[0].Description)
	require.Len(t, exportedBuild.Reminders, 1)
	require.Equal(t, "rem-1", exportedBuild.Reminders[0].ID)
	require.Len(t, exportedBuild.Monitors, 1)
	require.Equal(t, "Build watcher", exportedBuild.Monitors[0].Name)
	require.Equal(t, "Watches compilation", exportedBuild.Monitors[0].Description)
	require.Equal(t, "Is build failing?", exportedBuild.Monitors[0].QuestionInstructions)
	require.Equal(t, "Please fix build error", exportedBuild.Monitors[0].NudgeTemplate)
	require.Equal(t, 60, exportedBuild.Monitors[0].IntervalSeconds)
	require.Equal(t, 30, exportedBuild.Monitors[0].CooldownSeconds)
	require.Equal(t, 3, exportedBuild.Monitors[0].MaxAlerts)
	require.Equal(t, 5, exportedBuild.Monitors[0].TrailingMessagesCount)

	// 3. Import the workflow into the store
	importedWf, importedStates, err := s.ImportWorkflow(ctx, *bundle)
	require.NoError(t, err)
	require.NotNil(t, importedWf)
	require.NotEqual(t, wf.ID, importedWf.ID)
	// Because original name exists, candidate name was resolved to "Deployment Pipeline (Imported)"
	require.Equal(t, "Deployment Pipeline (Imported)", importedWf.Name)
	require.Equal(t, wf.Description, importedWf.Description)
	require.Len(t, importedWf.Variables, 2)
	require.Equal(t, 2, importedWf.StateCount)
	require.NotNil(t, importedWf.EntryStateID)
	require.NotEqual(t, stateBuild.ID, *importedWf.EntryStateID)

	require.Len(t, importedStates, 2)
	var impBuild, impDeploy *WorkflowState
	for i := range importedStates {
		st := &importedStates[i]
		if st.Name == "Build" {
			impBuild = st
		} else if st.Name == "Deploy" {
			impDeploy = st
		}
	}
	require.NotNil(t, impBuild)
	require.NotNil(t, impDeploy)
	require.Equal(t, *importedWf.EntryStateID, impBuild.ID)
	require.Equal(t, &importedWf.ID, impBuild.WorkflowID)
	require.Equal(t, &importedWf.ID, impDeploy.WorkflowID)
	require.True(t, impBuild.AllowExit)
	require.False(t, impDeploy.AllowExit)
	require.Equal(t, 100.0, impBuild.UIPosX)
	require.Equal(t, 150.0, impBuild.UIPosY)
	require.Equal(t, []string{"bash", "read"}, impBuild.AllowedTools)
	require.Equal(t, []string{"bash"}, impDeploy.AllowedTools)
	require.Len(t, impBuild.Reminders, 1)
	require.Equal(t, "rem-1", impBuild.Reminders[0].ID)
	require.Len(t, impBuild.Monitors, 1)
	require.Equal(t, "Build watcher", impBuild.Monitors[0].Name)
	require.Equal(t, "Watches compilation", impBuild.Monitors[0].Description)
	require.Equal(t, "Is build failing?", impBuild.Monitors[0].QuestionInstructions)
	require.Equal(t, "Please fix build error", impBuild.Monitors[0].NudgeTemplate)
	require.Equal(t, 60, impBuild.Monitors[0].IntervalSeconds)
	require.Equal(t, 30, impBuild.Monitors[0].CooldownSeconds)
	require.Equal(t, 3, impBuild.Monitors[0].MaxAlerts)
	require.Equal(t, 5, impBuild.Monitors[0].TrailingMessagesCount)

	// Transitions remapped to the newly generated UUID of Deploy
	require.Len(t, impBuild.Transitions, 1)
	require.Equal(t, impDeploy.ID, impBuild.Transitions[0].TargetStateID)
	require.Equal(t, "Proceed to deploy", impBuild.Transitions[0].Description)

	// 4. Test validation errors on bad bundles
	t.Run("empty states", func(t *testing.T) {
		badBundle := *bundle
		badBundle.States = []WorkflowStateExport{}
		_, _, err := s.ImportWorkflow(ctx, badBundle)
		require.Error(t, err)
		var valErr *WorkflowValidationError
		require.ErrorAs(t, err, &valErr)
		require.Equal(t, "states", valErr.Field)
	})

	t.Run("invalid transition target key", func(t *testing.T) {
		badBundle := *bundle
		badBundle.States = make([]WorkflowStateExport, len(bundle.States))
		copy(badBundle.States, bundle.States)
		badBundle.States[0].Transitions = []WorkflowTransitionExport{
			{TargetStateKey: "non-existent-key", Description: "bad"},
		}
		_, _, err := s.ImportWorkflow(ctx, badBundle)
		require.Error(t, err)
		var valErr *WorkflowValidationError
		require.ErrorAs(t, err, &valErr)
		require.Contains(t, valErr.Field, "transitions")
	})

	t.Run("self transition", func(t *testing.T) {
		badBundle := *bundle
		badBundle.States = make([]WorkflowStateExport, len(bundle.States))
		copy(badBundle.States, bundle.States)
		badBundle.States[0].Transitions = []WorkflowTransitionExport{
			{TargetStateKey: badBundle.States[0].Key, Description: "self"},
		}
		_, _, err := s.ImportWorkflow(ctx, badBundle)
		require.Error(t, err)
		var valErr *WorkflowValidationError
		require.ErrorAs(t, err, &valErr)
		require.Contains(t, valErr.Message, "self-transition")
	})

	t.Run("export non-existent", func(t *testing.T) {
		_, err := s.ExportWorkflow(ctx, "00000000-0000-0000-0000-000000000000")
		require.Error(t, err)
		var notFound *WorkflowNotFoundError
		require.ErrorAs(t, err, &notFound)
	})
}
