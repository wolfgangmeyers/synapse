package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/antigravitymodels"
	"github.com/stretchr/testify/require"
)

func TestAntigravitySubagentAdmission(t *testing.T) {
	ctx := context.Background()
	durable, err := Open(ctx, filepath.Join(t.TempDir(), "router.db"), Options{})
	require.NoError(t, err)
	defer durable.Close()

	parent := subagentParent(t, durable, "antigravity-parent")

	// Set parent model to antigravity/gemini-2.5-pro
	_, err = durable.db.Exec(`UPDATE managed_agents SET selected_model_provider=?, selected_model_id=? WHERE agent_id=?`, antigravitymodels.Provider, "gemini-2.5-pro", parent.AgentID)
	require.NoError(t, err)

	// Set default model to google-vertex / gemini-3.8-flash
	require.NoError(t, durable.SetDefaultModel(ctx, &ManagedModel{Provider: "google-vertex", ID: "gemini-3.8-flash"}))

	// Creating subagent under antigravity parent should inherit default model
	childID := "subagent-ag-child"
	err = durable.CreateManagedSubagent(ctx, parent, ManagedSubagentRecord{
		AgentID:       childID,
		ParentAgentID: parent.AgentID,
		Description:   "ag child test",
		SubagentType:  "general-purpose",
		Status:        "queued",
		MaxTurns:      1,
	})
	require.NoError(t, err)

	var gotProvider, gotModel string
	require.NoError(t, durable.db.QueryRow(`SELECT selected_model_provider, selected_model_id FROM managed_agents WHERE agent_id=?`, childID).Scan(&gotProvider, &gotModel))
	require.Equal(t, "google-vertex", gotProvider)
	require.Equal(t, "gemini-3.8-flash", gotModel)
}
