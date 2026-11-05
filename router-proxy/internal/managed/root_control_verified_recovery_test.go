package managed

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestRetiredRecoveryVerifiedSQLiteAndPythonLedger(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	for _, tc := range []struct{ name, resultStatus, mismatch string }{
		{"success", "ok", ""}, {"error", "error", ""}, {"sqlite hash mismatch", "ok", "sqlite"}, {"host arguments mismatch", "ok", "host"}, {"host fence mismatch", "ok", "fence"}, {"arguments unavailable", "ok", "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			m, db, binding, fence, _, rpc, _ := newRootAdmissionFixtureWithReceiver(t, false)
			binding.rpc = retirementRPC{rpc}
			host := &retirementHost{t: t, home: t.TempDir()}
			handlers, err := NewOperationalRootControlHandlers(m.manifest, host)
			require.NoError(t, err)
			m.rootControlHandlers, err = NewRootControlHandlerRegistry(m.manifest, handlers)
			require.NoError(t, err)
			id := "123e4567-e89b-42d3-a456-426614174390"
			args := map[string]any{"action": "send", "message": "original"}
			raw, err := canonicalJSON(args)
			require.NoError(t, err)
			hash := sha256.Sum256(raw)
			durableHash := fmt.Sprintf("%x", hash)
			if tc.mismatch == "sqlite" {
				durableHash = strings.Repeat("b", 64)
			}
			_, err = db.ReserveManagedControlRequest(ctx, store.ManagedControlRequestInput{RequestID: id, ResultID: "123e4567-e89b-42d3-a456-426614174391", AgentID: fence.AgentID, ToolCallID: "call", CapabilityID: "mecha_worker", ToolName: "mecha_worker", DefinitionID: "def_" + strings.Repeat("a", 64), OperationID: "ops.worker", CanonicalArgumentsSHA256: durableHash, RequestFingerprint: strings.Repeat("c", 64), Fence: fence, ReceivedAt: formatManagedTime(time.Now())})
			require.NoError(t, err)
			_, err = db.ClaimManagedControlHandler(ctx, id, "123e4567-e89b-42d3-a456-426614174392", fence)
			require.NoError(t, err)
			paths, err := filepath.Glob(filepath.Join(filepath.Dir(host.home), "*", "router.db"))
			require.NoError(t, err)
			require.Len(t, paths, 1)
			conn, err := sql.Open("sqlite", paths[0])
			require.NoError(t, err)
			_, err = conn.Exec("UPDATE managed_control_requests SET handler_deadline_at='2000-01-01T00:00:00.000Z' WHERE request_id=?", id)
			require.NoError(t, err)
			require.NoError(t, conn.Close())
			hostFence := map[string]any{"agent_id": fence.AgentID, "pi_session_id": fence.PiSessionID, "session_epoch": fence.SessionEpoch, "process_pid": fence.ProcessPID, "process_start_marker": fence.ProcessStartMarker, "process_incarnation": fence.ProcessIncarnation, "runtime_generation": fence.RuntimeGeneration, "manifest_revision": fence.ManifestRevision, "manifest_hash": fence.ManifestHash}
			hostArgs := args
			if tc.mismatch == "host" {
				hostArgs = map[string]any{"action": "send", "message": "different"}
			}
			if tc.mismatch == "fence" {
				hostFence["process_incarnation"] = "different"
			}
			hostInput, err := canonicalJSON(map[string]any{"operation_id": "ops.worker", "arguments": hostArgs, "fence": hostFence})
			require.NoError(t, err)
			hostHash := sha256.Sum256(hostInput)
			result := map[string]any{"status": tc.resultStatus, "content": "historical " + tc.resultStatus, "details": map[string]any{}}
			if tc.resultStatus == "error" {
				result["error_code"] = "OLD_ERROR"
				result["error_message"] = "old failure"
			}
			ledger, err := json.Marshal(map[string]any{"version": 1, "settings": map[string]any{}, "requests": map[string]any{id: map[string]any{"fingerprint": fmt.Sprintf("%x", hostHash), "recorded_at": time.Now().Unix(), "result": result}}})
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(filepath.Join(host.home, "root-control"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(host.home, "root-control", "requests.json"), ledger, 0600))
			var candidate any = args
			if tc.mismatch == "missing" {
				candidate = nil
			}
			m.resumeRootControl(ctx, binding, id, candidate)
			row, err := db.ManagedControlRequest(ctx, id)
			require.NoError(t, err)
			require.Equal(t, "delivered", row.State)
			if tc.mismatch != "" {
				require.Equal(t, "ROOT_OPERATION_UNKNOWN", row.ErrorCode)
			} else {
				require.Equal(t, "historical "+tc.resultStatus, row.ResultContent)
				require.Equal(t, tc.resultStatus, row.ResultStatus)
				if tc.resultStatus == "error" {
					require.Equal(t, "OLD_ERROR", row.ErrorCode)
				}
			}
			if tc.mismatch == "sqlite" || tc.mismatch == "missing" {
				require.Zero(t, host.reconciles)
			} else {
				require.Equal(t, 1, host.reconciles)
			}
			require.Zero(t, host.executes)
			require.Equal(t, 1, rpc.submissionCount())
			m.resumeRootControl(ctx, binding, id, candidate)
			m.recoverRootControls(binding)
			require.Equal(t, 1, rpc.submissionCount())
			require.Zero(t, host.executes)
		})
	}
}
