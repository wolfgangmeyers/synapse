package managed

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRemovedWorkerCannotEnterCurrentAdmission(t *testing.T) {
	m, db, binding, fence, tool, rpc, _ := newRootAdmissionFixtureWithReceiver(t, false)
	event := rootAdmissionEvent(fence, tool, "123e4567-e89b-42d3-a456-426614174390", map[string]any{"action": "send"})
	event.CapabilityID = "mecha_worker"
	event.ToolName = "mecha_worker"
	event.OperationID = "ops.worker"
	event.DefinitionID = "def_" + strings.Repeat("a", 64)
	m.admitAndRunRootControl(context.Background(), binding, event)
	_, err := db.ManagedControlRequest(context.Background(), event.RequestID)
	require.ErrorIs(t, err, sql.ErrNoRows)
	require.Zero(t, rpc.submissionCount())
}
