package managed

import (
	"context"
	"database/sql"
	"testing"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestHostAdmissionUsesSDKValidatedArguments(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"coercion", map[string]any{"path": "sample.txt", "offset": "1"}},
		{"optional-null", map[string]any{"path": "sample.txt", "offset": nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, _ := schemaAuthorityFixture(t)
			var read agent.AgentTool
			for _, tool := range a.Tools() {
				if tool.Name == "read" {
					read = tool
				}
			}
			call := ai.ToolCall{ID: "validated-" + tc.name, Name: "read", Arguments: tc.args}
			args, err := ai.ValidateToolArguments(ai.Tool{Name: read.Name, Parameters: read.Parameters}, call)
			require.NoError(t, err)
			require.NoError(t, a.BeforeToolExecute(context.Background(), agent.BeforeToolCallContext{ToolCall: call, Args: args}))
		})
	}
}

func TestHostAdmissionStillChecksCurrentCatalogWithValidatedArguments(t *testing.T) {
	a, m, id := schemaAuthorityFixture(t)
	var read agent.AgentTool
	for _, tool := range a.Tools() {
		if tool.Name == "read" {
			read = tool
		}
	}
	call := ai.ToolCall{ID: "changed-schema", Name: "read", Arguments: map[string]any{"path": "sample.txt", "offset": "1"}}
	args, err := ai.ValidateToolArguments(ai.Tool{Name: read.Name, Parameters: read.Parameters}, call)
	require.NoError(t, err)
	current := append([]agent.AgentTool(nil), a.baseTools...)
	for i := range current {
		if current[i].Name == "read" {
			current[i].Parameters = ai.Object(ai.Prop("path", ai.Integer()))
		}
	}
	m.laneFor(id).setTools(current, current)
	require.Error(t, a.BeforeToolExecute(context.Background(), agent.BeforeToolCallContext{ToolCall: call, Args: args}))
	_, err = m.store.RecoverEmbeddedEffect(context.Background(), a.persistence.binding, call.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestHostAdmissionRejectsInvalidValidatedArguments(t *testing.T) {
	a, m, _ := schemaAuthorityFixture(t)
	call := ai.ToolCall{ID: "invalid-validated", Name: "read", Arguments: map[string]any{"path": "sample.txt", "offset": "not-a-number"}}
	var read agent.AgentTool
	for _, tool := range a.Tools() {
		if tool.Name == "read" {
			read = tool
		}
	}
	_, err := ai.ValidateToolArguments(ai.Tool{Name: read.Name, Parameters: read.Parameters}, call)
	require.Error(t, err)
	require.Error(t, a.BeforeToolExecute(context.Background(), agent.BeforeToolCallContext{ToolCall: call, Args: call.Arguments}))
	_, err = m.store.RecoverEmbeddedEffect(context.Background(), a.persistence.binding, call.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)
}
