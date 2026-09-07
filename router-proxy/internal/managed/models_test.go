package managed

import (
	"context"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestSelectModelUsesExplicitSavedThenTerraAvailability(t *testing.T) {
	catalog := CatalogFromModels(Model{Provider: "saved", ID: "model", Available: true}, Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true})
	current, err := catalog.Catalog(context.Background())
	require.NoError(t, err)
	selected, err := SelectModel(&Model{Provider: "saved", ID: "model"}, nil, current)
	require.NoError(t, err)
	require.Equal(t, "saved", selected.Provider)
	selected, err = SelectModel(nil, &store.ManagedModel{Provider: "saved", ID: "model"}, current)
	require.NoError(t, err)
	require.Equal(t, "saved", selected.Provider)
	selected, err = SelectModel(nil, &store.ManagedModel{Provider: "missing", ID: "model"}, current)
	require.NoError(t, err)
	require.Equal(t, "gpt-5.6-terra", selected.ID)
}

func TestSelectModelRejectsUnavailableExplicitAndTerra(t *testing.T) {
	_, err := SelectModel(&Model{Provider: "missing", ID: "model"}, nil, Catalog{Models: []Model{}})
	require.Error(t, err)
	require.True(t, IsModelUnavailable(err))
	_, err = SelectModel(nil, nil, Catalog{Models: []Model{}})
	require.Error(t, err)
	require.True(t, IsModelUnavailable(err))
}

func TestPublicAgentOmitsPrivateProcessAndSessionIdentity(t *testing.T) {
	agent := &store.ManagedAgentRecord{
		AgentID: "alpha", Cwd: "/work", SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		DesiredState: "running", LifecycleState: "running", ActivityState: "idle", AcceptingPrompts: true,
		PiSessionID: "private-session", ProcessIncarnation: "private-process", RuntimeGeneration: "private-generation",
	}
	projection := PublicAgent(agent)
	_, hasSession := projection["pi_session_id"]
	_, hasProcess := projection["process_incarnation"]
	_, hasGeneration := projection["runtime_generation"]
	_, hasEpoch := projection["session_epoch"]
	require.False(t, hasSession)
	require.False(t, hasProcess)
	require.False(t, hasGeneration)
	require.False(t, hasEpoch)
}
