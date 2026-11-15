package managed

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/coding"
	"github.com/stretchr/testify/require"
)

func TestSchemaDiagnosticsGenericHostMetadata(t *testing.T) {
	for _, name := range []string{"write", "edit", "bash"} {
		tool, err := coding.CreateTool(name, t.TempDir())
		require.NoError(t, err)
		require.NotEmpty(t, tool.Parameters.Required)
		field := tool.Parameters.Required[0]
		msg := embeddedInvalidToolArguments(&tool, map[string]any{}, true)
		require.Contains(t, msg, name)
		require.Contains(t, msg, "missing required field "+field)
	}
	tool := agent.AgentTool{Name: "goal_snooze", Parameters: ai.Object(ai.Prop("delay", ai.Integer()))}
	msg := embeddedInvalidToolArguments(&tool, map[string]any{"delay": "secret-value"}, true)
	require.Contains(t, msg, "goal_snooze: field delay must be integer")
	require.NotContains(t, msg, "secret-value")
}

func TestSchemaDiagnosticsUntrustedAndNonTypeFailures(t *testing.T) {
	secret := "secret-registration-token"
	for _, name := range []string{secret, "bad\nname", strings.Repeat("x", 100)} {
		tool := agent.AgentTool{Name: name, Parameters: ai.Object(ai.Prop(secret, ai.String()))}
		msg := embeddedInvalidToolArguments(&tool, map[string]any{}, false)
		require.Equal(t, "invalid arguments for (unavailable tool): check the tool schema", msg)
		require.NotContains(t, msg, secret)
	}
	tool := agent.AgentTool{Name: "example", Parameters: ai.Object(ai.Prop("mode", ai.EnumOf("a", "b")))}
	require.Equal(t, "invalid arguments for example: check the tool schema", embeddedInvalidToolArguments(&tool, map[string]any{"mode": "secret-value"}, true))
	tool.Parameters = ai.Object(ai.Prop("unsafe\nfield", ai.String()))
	require.NotContains(t, embeddedInvalidToolArguments(&tool, nil, true), "unsafe")
}

func TestSchemaDiagnosticsHostWriteDenial(t *testing.T) {
	a, m, _ := schemaAuthorityFixture(t)
	// Numeric content is coercible to a string by the SDK, so use an object
	// to exercise a genuinely invalid content type at the host boundary.
	call := ai.ToolCall{ID: "invalid-write-review", Name: "write", Arguments: map[string]any{"path": "secret-value", "content": map[string]any{"nested": "secret-content"}}}
	err := a.BeforeToolExecute(context.Background(), agent.BeforeToolCallContext{ToolCall: call})
	require.Error(t, err)
	require.Contains(t, err.Error(), "write: field content must be string")
	require.NotContains(t, err.Error(), "secret-value")
	_, recoverErr := m.store.RecoverEmbeddedEffect(context.Background(), a.persistence.binding, call.ID)
	require.ErrorIs(t, recoverErr, sql.ErrNoRows)
}
