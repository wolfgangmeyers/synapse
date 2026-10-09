package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReserveEmbeddedReset(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	b := redBind(t, s, "embedded-reset-worker", "session-reset", 1, "gen-reset", "worker")
	_, err := s.db.Exec(`UPDATE managed_agents SET accepting_prompts=1,slot_held=1 WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)

	op := ManagedOperation{
		OperationID:       "op-reset-1",
		Method:            "POST",
		CanonicalPath:     "/v1/managed-agents/embedded-reset-worker/reset",
		RequestID:         "op-reset-1",
		CanonicalBodyHash: "hash-1",
		ResponseStatus:    202,
		ResponseBody:      []byte(`{"status":"accepted"}`),
	}

	// 1. Initial reservation succeeds
	committed, replayed, err := s.ReserveEmbeddedReset(ctx, b.AgentID, op)
	require.NoError(t, err)
	require.False(t, replayed)
	require.NotNil(t, committed)
	require.Equal(t, "op-reset-1", committed.OperationID)
	require.Equal(t, "terminal", committed.State)
	require.Equal(t, 202, committed.ResponseStatus)

	// 2. Replay with identical request reuses committed operation
	committedReplay, replayed2, err := s.ReserveEmbeddedReset(ctx, b.AgentID, op)
	require.NoError(t, err)
	require.True(t, replayed2)
	require.Equal(t, committed.OperationID, committedReplay.OperationID)

	// 3. Conflict on different body hash
	conflictingOp := op
	conflictingOp.CanonicalBodyHash = "hash-different"
	_, _, err = s.ReserveEmbeddedReset(ctx, b.AgentID, conflictingOp)
	require.Error(t, err)
	var conflictErr *ManagedIdempotencyConflictError
	require.ErrorAs(t, err, &conflictErr)
}
