package managed

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEmbeddedGoalOriginalArgumentsClosed(t *testing.T) {
	for _, args := range []map[string]any{
		{"action": "set"}, {"action": "set", "goal": "  \n"}, {"action": "set", "goal": strings.Repeat("x", 4001)},
		{"action": "set", "goal": "ok", "agent_id": "other"}, {"action": "set", "goal": "ok", "settings": map[string]any{}},
		{"action": "status", "goal": ""}, {"action": "off", "target": "other"}, {"action": "unknown"},
	} {
		_, err := embeddedGoalCommand(args)
		require.Error(t, err, "%v", args)
	}
	for _, args := range []map[string]any{{"action": "set", "goal": strings.Repeat("x", 4000)}, {"action": "status"}, {"action": "off"}} {
		_, err := embeddedGoalCommand(args)
		require.NoError(t, err)
		require.True(t, embeddedGoalSchema().Check(args))
	}
}
