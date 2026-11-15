package managed

import (
	"testing"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedDiagnosticDoesNotBlameCoercibleField(t *testing.T) {
	tool := agent.AgentTool{Name: "example", Parameters: ai.Object(
		ai.Prop("delay", ai.Integer()),
		ai.Prop("mode", ai.EnumOf("a", "b")),
	)}
	args := map[string]any{"delay": "12", "mode": "secret-value"}
	_, err := ai.ValidateToolArguments(ai.Tool{Name: tool.Name, Parameters: tool.Parameters}, ai.ToolCall{Arguments: args})
	require.Error(t, err)
	require.Equal(t, "invalid arguments for example: check the tool schema", embeddedInvalidToolArguments(&tool, args, true))
	require.Equal(t, "12", args["delay"])
}
