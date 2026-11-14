package managed

import (
	"context"
	"errors"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedRestartOwnerValidationErrors(t *testing.T) {
	for _, tc := range []struct {
		name       string
		breakOwner func(*testing.T, *failureFixture, embeddedOwner)
		stale      bool
	}{
		{name: "stale owner", stale: true, breakOwner: func(t *testing.T, f *failureFixture, owner embeddedOwner) {
			_, err := f.raw.Exec(`UPDATE embedded_sessions SET owner_id='another-owner' WHERE agent_id=? AND current=1`, owner.binding.AgentID)
			require.NoError(t, err)
		}},
		{name: "database failure", breakOwner: func(t *testing.T, f *failureFixture, owner embeddedOwner) {
			_, err := f.raw.Exec(`ALTER TABLE embedded_sessions RENAME TO embedded_sessions_unavailable`)
			require.NoError(t, err)
			t.Cleanup(func() {
				_, err := f.raw.Exec(`ALTER TABLE embedded_sessions_unavailable RENAME TO embedded_sessions`)
				require.NoError(t, err)
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFailureFixture(t)
			owner := f.launch(t, "")
			lane := f.m.laneFor(owner.binding.AgentID)
			runtime := &resetBarrierRuntime{interrupted: make(chan struct{}), closed: make(chan struct{})}
			require.True(t, lane.installEmbeddedRuntime(runtime, nil))
			defer lane.removeEmbeddedRuntime(runtime)
			tc.breakOwner(t, f, owner)
			_, err := f.m.reserveClaudeCodeRestart(context.Background(), RestartRequest{AgentID: owner.binding.AgentID, Method: "POST", CanonicalPath: "/restart", RequestID: "owner-error", CanonicalBodyHash: "empty"})
			require.Error(t, err)
			var notReady *store.ManagedAgentNotReadyError
			if tc.stale {
				require.ErrorAs(t, err, &notReady)
			} else {
				require.False(t, errors.As(err, &notReady))
				require.ErrorContains(t, err, "embedded_sessions")
			}
			select {
			case <-runtime.interrupted:
				t.Fatal("failed restart interrupted runtime")
			default:
			}
			require.Equal(t, 0, f.count(t, `SELECT count(*) FROM managed_http_operations WHERE request_id=?`, "owner-error"))
		})
	}
}
