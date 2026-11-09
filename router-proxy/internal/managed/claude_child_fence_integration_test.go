package managed

import (
	"context"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestClaudeChildRejectsFencedOrShutdownPoolBeforeSetup(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "fenced", true: "shutdown"}[shutdown], func(t *testing.T) {
			m := &Manager{}
			if shutdown {
				require.NoError(t, m.claudeCodeProcesses.stopAll())
			} else {
				m.claudeCodeProcesses.fence()
			}
			// No store is configured: admission must fail before any child setup.
			status, _, diagnostic := m.runClaudeSubagent(context.Background(), &subagentTask{}, store.EmbeddedBinding{}, store.ManagedAgentRecord{})
			require.Equal(t, "failed", status)
			if shutdown {
				require.Contains(t, diagnostic, "pool closed")
			} else {
				require.Contains(t, diagnostic, "group absence unconfirmed")
			}
		})
	}
}
