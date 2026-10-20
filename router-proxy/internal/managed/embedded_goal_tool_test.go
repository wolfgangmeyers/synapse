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
		{"action": "off", "goal": 123}, {"action": "off", "target": "other"},
		{"action": "status", "settings": map[string]any{}}, {"action": "unknown"},
	} {
		_, err := embeddedGoalCommand(args)
		require.Error(t, err, "%v", args)
	}
	for _, args := range []map[string]any{
		{"action": "set", "goal": strings.Repeat("x", 4000)},
		{"action": "status"},
		{"action": "off"},
		{"action": "off", "goal": ""},
		{"action": "off", "goal": "   "},
		{"action": "off", "goal": "done with task"},
		{"action": "off", "goal": nil},
		{"action": "status", "goal": ""},
		{"action": "status", "goal": "inspect"},
	} {
		_, err := embeddedGoalCommand(args)
		require.NoError(t, err)
		if args["goal"] != nil {
			require.True(t, embeddedGoalSchema().Check(args))
		}
	}
}
