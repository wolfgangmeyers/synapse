package managed

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

// A direct Claude prompt must obey the same lifecycle gate as the normal lane:
// once the store says the agent is not accepting prompts, it must not be
// steered into the still-open Claude turn.
func TestClaudeDirectSteerRespectsAcceptingPromptsGate(t *testing.T) {
	ctx := context.Background()
	db, b, _, dbPath := wakePersistenceFixtureWithClaim(t, false)

	// Simulate the state committed by a termination/reset/restart reservation
	// while the Claude turn is still open.
	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO agents(agent_id,first_seen_at) VALUES('wake-agent','2026-01-01T00:00:00Z')`)
	require.NoError(t, err)
	_, err = raw.Exec(`UPDATE managed_agents SET desired_state='terminated', lifecycle_state='terminating', accepting_prompts=0 WHERE agent_id='wake-agent'`)
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	stdin := &steerRecordStdin{}
	proc := &ClaudeCodeProcess{stdin: stdin, inputs: make(map[string]claudeCodePendingInput), done: make(chan struct{}), closeDone: make(chan struct{}), notify: make(chan struct{}, 1)}
	p := &embeddedPersistence{db: db, binding: b, digest: "genesis"}
	claude := &claudeCodeRuntime{
		p:            p,
		process:      proc,
		beforeSubmit: func(context.Context, int) error { return nil },
		active:       true,
		steerOpen:    true,
		snapshot:     embeddedSnapshot{ContextWindow: 1000000},
	}

	m := &Manager{store: db, embeddedOwners: map[string]embeddedOwner{"wake-agent": {binding: b}}}
	lane := m.laneFor("wake-agent")
	lane.activeSession = claude

	done := make(chan error, 1)
	go func() {
		done <- m.AdmitPromptWithRequestID(ctx, "wake-agent", "direct steer during termination", "req-steer-gate")
	}()
	select {
	case err := <-done:
		require.ErrorAs(t, err, new(*store.ManagedAgentNotReadyError))
	case <-time.After(5 * time.Second):
		t.Fatal("AdmitPromptWithRequestID did not return")
	}
	require.Empty(t, stdin.writes, "a prompt refused by the lifecycle gate must not reach Claude stdin")
}
