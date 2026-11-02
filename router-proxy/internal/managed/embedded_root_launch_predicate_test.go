package managed

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/sky-valley/pi/coding"
	"github.com/stretchr/testify/require"
)

// The SDK fake sees only synthetic tool declarations. No Manager, store,
// credentials, real tool execution, or whole-root fixture is involved.
func TestEmbeddedRootLaunchPredicateSDKDeclarations(t *testing.T) {
	retained := []string{"mecha_goal.set", "mecha_goal.snooze", "mecha_context.compact", "mecha_reminder.create", "bash", "read", "edit", "write"}
	cases := []struct {
		name, role                       string
		token                            int64
		pending, initialTurn, wantLaunch bool
	}{
		{"first-root", "root", 1, true, true, !embeddedRootLaunchAblation},
		{"first-worker", "worker", 1, true, true, true},
		{"later-root", "root", 2, true, true, true},
		{"continued-root", "root", 1, true, false, true},
		{"no-instruction-root", "root", 1, false, true, true},
		{"other-role", "other", 1, true, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
			defer reg.Unregister()
			var observed []ai.Tool
			reg.SetResponses([]providers.FauxResponseStep{func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
				for _, message := range h.Messages {
					if system, ok := message.(ai.SystemMessage); ok {
						observed = append(observed, system.ToolsAdded...)
					}
				}
				return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
			}})
			var tools []agent.AgentTool
			for _, name := range retained {
				tools = append(tools, agent.AgentTool{Name: name, Description: "retained synthetic " + name, Parameters: ai.Object(ai.Prop("sentinel", ai.String()))})
			}
			if embeddedShouldAppendWorkerLaunch(tc.role, tc.token, tc.pending, tc.initialTurn) {
				tools = append(tools, agent.AgentTool{Name: "mecha_worker", Description: "synthetic launch", Parameters: embeddedWorkerSchema()})
			}
			sdk := coding.NewSession(coding.SessionOptions{SessionID: "predicate-" + tc.name, Model: reg.GetModel(), Cwd: t.TempDir(), Tools: tools})
			_, err := sdk.Run(context.Background(), "synthetic input")
			require.NoError(t, err)
			want := append([]string(nil), retained...)
			if tc.wantLaunch {
				want = append(want, "mecha_worker")
			}
			var got []string
			for _, tool := range observed {
				got = append(got, tool.Name)
			}
			require.Equal(t, want, got)
			require.Len(t, observed, len(tools))
			for i, tool := range tools {
				actual, err := json.Marshal(observed[i])
				require.NoError(t, err)
				expected, err := json.Marshal(ai.Tool{Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters})
				require.NoError(t, err)
				require.JSONEq(t, string(expected), string(actual), "schema/order changed for %s", tool.Name)
			}
		})
	}
}
