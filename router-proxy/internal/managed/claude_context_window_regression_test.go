package managed

import (
	"encoding/json"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestClaudeContextWindowBindingModelAndFallback(t *testing.T) {
	lane := &managedAgentLane{}
	b := store.EmbeddedBinding{AgentID: "a", SessionID: "s", Epoch: 1, Generation: "g"}
	require.Equal(t, 1000000, lane.claudeWindow(b, "sonnet"))
	lane.setClaudeWindow(b, "sonnet", 200000)
	require.Equal(t, 200000, lane.claudeWindow(b, "sonnet"))
	lane.setClaudeWindow(b, "sonnet", 0)
	require.Equal(t, 200000, lane.claudeWindow(b, "sonnet"))
	require.Equal(t, 1000000, lane.claudeWindow(b, "opus"))
	b.Epoch++
	require.Equal(t, 1000000, lane.claudeWindow(b, "sonnet"))
}

func TestClaudeWindowMatchAndHeadroom(t *testing.T) {
	usage := map[string]json.RawMessage{"claude-sonnet-5-5": json.RawMessage(`200000`), "haiku": json.RawMessage(`100000`)}
	require.Equal(t, 200000, claudeReportedWindow("sonnet", "claude-sonnet-5-5", usage))
	require.Zero(t, claudeReportedWindow("sonnet", "", usage))
	usage["sonnet"] = json.RawMessage(`300000`)
	require.Zero(t, claudeReportedWindow("sonnet", "claude-sonnet-5-5", usage))
	require.True(t, claudeHeadroomFits(200000, 180000, 3616))
	require.False(t, claudeHeadroomFits(200000, 180000, 3617))
}

func TestClaudeQueuedRenderedBytesHeadroom(t *testing.T) {
	r := &claudeCodeRuntime{snapshot: embeddedSnapshot{ContextWindow: 200000, ContextTokens: 180000}}
	require.NoError(t, r.checkInputHeadroom(2000))
	r.admissionBytes = 2000
	require.NoError(t, r.checkInputHeadroom(1616))
	require.ErrorContains(t, r.checkInputHeadroom(1617), "headroom")
}
