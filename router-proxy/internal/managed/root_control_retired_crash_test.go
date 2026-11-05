package managed

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

type retiredCrashHost struct {
	saved        bool
	reads, calls int
}

func (h *retiredCrashHost) ExecuteRootControl(context.Context, RootControlHandlerIdentity, RootControlInvocation) (RootControlHandlerResult, error) {
	h.calls++
	return RootControlHandlerResult{}, nil
}
func (h *retiredCrashHost) ReconcileRetiredRootControl(ctx context.Context, identity RootControlHandlerIdentity, in RootControlInvocation) (RootControlHandlerResult, error) {
	return h.ReconcileRootControl(ctx, identity, in)
}
func (h *retiredCrashHost) ReconcileRootControl(context.Context, RootControlHandlerIdentity, RootControlInvocation) (RootControlHandlerResult, error) {
	h.reads++
	if h.saved {
		return RootControlHandlerResult{Status: "ok", Content: "historical success"}, nil
	}
	return RootControlHandlerResult{Status: "error", Content: "outcome unknown", ErrorCode: "ROOT_OPERATION_UNKNOWN", ErrorMessage: "outcome unknown"}, nil
}
func TestRetiredRootClaimCrashRecovery(t *testing.T) {
	for _, op := range []string{"ops.state", "ops.priority", "ops.verification_handoff", "ops.pr_status", "ops.git_status"} {
		for _, state := range []string{"executing", "side_effect_committed"} {
			for _, saved := range []bool{false, true} {
				t.Run(op+"/"+state+"/"+map[bool]string{true: "historical", false: "new"}[saved], func(t *testing.T) {
					ctx := context.Background()
					m, db, binding, fence, _, rpc, _ := newRootAdmissionFixtureWithReceiver(t, false)
					binding.rpc = retirementRPC{rpc}
					h := &retiredCrashHost{saved: saved}
					handlers, err := NewOperationalRootControlHandlers(m.manifest, h)
					require.NoError(t, err)
					m.rootControlHandlers, err = NewRootControlHandlerRegistry(m.manifest, handlers)
					require.NoError(t, err)
					var tool ManifestTool
					for _, v := range m.manifest.Tools {
						if v.OperationID == op {
							tool = v
						}
					}
					require.NotEmpty(t, tool.Name)
					id := "123e4567-e89b-42d3-a456-426614174390"
					_, err = db.ReserveManagedControlRequest(ctx, store.ManagedControlRequestInput{RequestID: id, ResultID: "123e4567-e89b-42d3-a456-426614174391", AgentID: fence.AgentID, ToolCallID: "call", CapabilityID: tool.CapabilityID, ToolName: tool.Name, DefinitionID: tool.DefinitionID, OperationID: op, CanonicalArgumentsSHA256: strings.Repeat("b", 64), RequestFingerprint: strings.Repeat("c", 64), Fence: fence, ReceivedAt: formatManagedTime(time.Now())})
					require.NoError(t, err)
					owner := "123e4567-e89b-42d3-a456-426614174392"
					_, err = db.ClaimManagedControlHandler(ctx, id, owner, fence)
					require.NoError(t, err)
					if state == "side_effect_committed" {
						require.NoError(t, db.CommitManagedControlSideEffect(ctx, id, owner, fence))
					}
					home := t.TempDir()
					paths, err := filepath.Glob(filepath.Join(filepath.Dir(home), "*", "router.db"))
					require.NoError(t, err)
					require.Len(t, paths, 1)
					conn, err := sql.Open("sqlite", paths[0])
					require.NoError(t, err)
					_, err = conn.Exec("UPDATE managed_control_requests SET handler_deadline_at='2000-01-01T00:00:00.000Z' WHERE request_id=?", id)
					require.NoError(t, err)
					require.NoError(t, conn.Close())
					m.recoverRootControls(binding)
					row, err := db.ManagedControlRequest(ctx, id)
					require.NoError(t, err)
					require.Equal(t, "delivered", row.State)
					require.Equal(t, "ROOT_OPERATION_UNKNOWN", row.ErrorCode)
					require.Zero(t, h.reads)
					require.Zero(t, h.calls)
					require.Equal(t, 1, rpc.submissionCount())

				})
			}
		}
	}
}
