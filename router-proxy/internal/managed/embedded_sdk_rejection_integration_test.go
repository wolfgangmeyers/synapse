package managed

import (
	"testing"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// The SDK reports type failures in sorted field order, unlike schema declaration order.
func TestEmbeddedSDKStructuredRejection(t *testing.T) {
	tool := agent.AgentTool{Name: "read", Parameters: ai.Object(ai.Prop("path", ai.String()), ai.Prop("offset", ai.Integer()))}
	msg := embeddedInvalidToolArguments(&tool, map[string]any{"path": 4, "offset": "secret"}, true)
	require.Contains(t, msg, "field offset must be integer")
	require.NotContains(t, msg, "secret")
}

func TestEmbeddedSDKStructuredRejectionUntrusted(t *testing.T) {
	tool := agent.AgentTool{Name: "secret-token", Parameters: ai.Object(ai.Prop("path", ai.String()))}
	msg := embeddedInvalidToolArguments(&tool, map[string]any{"path": 7}, false)
	require.Equal(t, "invalid arguments for (unavailable tool): check the tool schema", msg)
	require.NotContains(t, msg, "secret-token")
	require.NotContains(t, msg, "path")
}
