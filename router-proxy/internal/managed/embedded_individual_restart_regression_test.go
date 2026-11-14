package managed

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// A ready embedded owner has no legacy RPC binding, but must accept an
// individual restart without an explicit model.
func TestEmbeddedIndividualRestartWithoutModel(t *testing.T) {
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	result, err := f.m.ReserveRestart(context.Background(), RestartRequest{
		AgentID: owner.binding.AgentID, Method: "POST", CanonicalPath: "/v1/managed-agents/failure-root/restart",
		RequestID: "e2222222-2222-4222-8222-222222222222", CanonicalBodyHash: "empty-body",
	})
	require.NoError(t, err)
	require.Equal(t, 202, result.Operation.ResponseStatus)
}
