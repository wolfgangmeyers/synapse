package managed

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/sky-valley/pi/coding"
	"github.com/stretchr/testify/require"
)

// The shared sync path must refresh the live Pi session, not just its lane.
func TestSyncEmbeddedAgentToolsRefreshesActivePiSession(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	const agentID = "sync-pi-tools"
	subagentTestParent(t, db, agentID)
	state, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{
		Name: "Read only", AllowedTools: []string{"read"},
	})
	require.NoError(t, err)

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	var observed [][]string
	step := func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		var names []string
		for _, tool := range ai.GetCurrentTools(h.Messages) {
			names = append(names, tool.Name)
		}
		observed = append(observed, names)
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
	}
	reg.SetResponses([]providers.FauxResponseStep{step, step, step})
	session := coding.NewSession(coding.SessionOptions{Model: reg.GetModel(), Cwd: t.TempDir()})
	var base []agent.AgentTool
	for _, tool := range session.Agent.State().Tools {
		if tool.Name == "read" || tool.Name == "bash" {
			base = append(base, tool)
		}
	}
	require.Len(t, base, 2)
	session.Agent.SetTools(base)
	runtime := &piEmbeddedRuntime{Session: session}
	mgr := &Manager{store: db}
	lane := mgr.laneFor(agentID)
	lane.setTools(base, base)
	lane.activeMu.Lock()
	lane.activeSession = runtime
	lane.activeMu.Unlock()

	_, err = runtime.Run(ctx, "before restriction")
	require.NoError(t, err)
	require.NoError(t, db.SetManagedAgentActiveWorkflowState(ctx, agentID, &state.ID))
	require.NoError(t, mgr.SyncEmbeddedAgentTools(ctx, agentID))
	_, err = runtime.Run(ctx, "restricted")
	require.NoError(t, err)
	require.NoError(t, db.SetManagedAgentActiveWorkflowState(ctx, agentID, nil))
	require.NoError(t, mgr.SyncEmbeddedAgentTools(ctx, agentID))
	_, err = runtime.Run(ctx, "restored")
	require.NoError(t, err)

	require.Len(t, observed, 3)
	require.ElementsMatch(t, []string{"read", "bash"}, observed[0])
	require.ElementsMatch(t, []string{"read"}, observed[1])
	require.ElementsMatch(t, []string{"read", "bash"}, observed[2])
}
