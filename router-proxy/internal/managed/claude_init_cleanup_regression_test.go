package managed

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClaudeRuntimeInitFailureClearsExecution(t *testing.T) {
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	waitGateStarts(t, f.m)
	_, err := f.raw.Exec(`UPDATE managed_agents SET selected_model_provider='claude-code', selected_model_id='sonnet' WHERE agent_id=?`, owner.binding.AgentID)
	require.NoError(t, err)
	// Valid JSON with an unknown message role passes storage validation but fails runtime decoding.
	_, err = f.db.AppendEmbeddedEntry(context.Background(), owner.binding, "user", []byte(`{"role":"invalid"}`))
	require.NoError(t, err)
	saved, err := f.db.ManagedAgent(context.Background(), owner.binding.AgentID)
	require.NoError(t, err)
	ctx := context.WithValue(context.Background(), embeddedOwnerContextKey{}, owner)
	_, err = f.m.runEmbeddedSessionTurnScoped(ctx, *saved, "next", nil, nil, nil, nil)
	require.ErrorContains(t, err, `unknown message role`)
	require.Zero(t, f.count(t, `SELECT count(*) FROM embedded_sessions WHERE agent_id=? AND execution_json IS NOT NULL`, owner.binding.AgentID))
}
