package managed

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

// These tests join the manager, SQLite ledger, real Python host ledger and
// deterministic Pi receipt boundary. No model or external service is used.
type retirementHost struct {
	t                    *testing.T
	home                 string
	executes, reconciles int
}

func (h *retirementHost) ExecuteRootControl(context.Context, RootControlHandlerIdentity, RootControlInvocation) (RootControlHandlerResult, error) {
	h.executes++
	h.t.Fatal("retired operation reached Execute")
	return RootControlHandlerResult{}, nil
}
func (h *retirementHost) ReconcileRetiredRootControl(ctx context.Context, identity RootControlHandlerIdentity, in RootControlInvocation) (RootControlHandlerResult, error) {
	return h.ReconcileRootControl(ctx, identity, in)
}
func (h *retirementHost) ReconcileRootControl(ctx context.Context, _ RootControlHandlerIdentity, in RootControlInvocation) (RootControlHandlerResult, error) {
	h.reconciles++
	data, err := json.Marshal(map[string]any{"request_id": in.RequestID, "operation_id": in.OperationID, "arguments": in.Arguments, "fence": map[string]any{"agent_id": in.Fence.AgentID, "pi_session_id": in.Fence.PiSessionID, "session_epoch": in.Fence.SessionEpoch, "process_pid": in.Fence.ProcessPID, "process_start_marker": in.Fence.ProcessStartMarker, "process_incarnation": in.Fence.ProcessIncarnation, "runtime_generation": in.Fence.RuntimeGeneration, "manifest_revision": in.Fence.ManifestRevision, "manifest_hash": in.Fence.ManifestHash}})
	require.NoError(h.t, err)
	python := os.Getenv("PYTHON")
	if python == "" {
		python = "python3"
	}
	cmd := exec.CommandContext(ctx, python, "../../../tools/managed_root_control.py", "reconcile", "--home", h.home)
	cmd.Stdin = bytes.NewReader(data)
	out, err := cmd.CombinedOutput()
	require.NoError(h.t, err, string(out))
	var decoded struct {
		Status       string         `json:"status"`
		Content      string         `json:"content"`
		Details      map[string]any `json:"details"`
		ErrorCode    string         `json:"error_code"`
		ErrorMessage string         `json:"error_message"`
	}
	require.NoError(h.t, json.Unmarshal(out, &decoded))
	return RootControlHandlerResult{Status: decoded.Status, Content: decoded.Content, Details: decoded.Details, ErrorCode: decoded.ErrorCode, ErrorMessage: decoded.ErrorMessage}, nil
}

type retirementRPC struct{ *rootAdmissionRPC }

func (r retirementRPC) ReconcileRootControlResult(ctx context.Context, payload map[string]any) (RootControlPersistedReceipt, error) {
	return (&managedGoalStatusRootControlRPC{}).ReconcileRootControlResult(ctx, payload)
}

func TestRetiredWorkerRootRecovery(t *testing.T) {
	python := os.Getenv("PYTHON")
	if python == "" {
		python = "python3"
	}
	if _, err := exec.LookPath(python); err != nil {
		t.Skipf("Python executable %q unavailable: %v", python, err)
	}
	for _, tc := range []struct {
		name, state  string
		saved, stale bool
	}{
		{"pending", "received", false, false}, {"pending ignores saved success", "received", true, false},
		{"executing saved host success", "executing", true, false}, {"committed saved host success", "side_effect_committed", true, false},
		{"executing unknown", "executing", false, false}, {"committed unknown", "side_effect_committed", false, false},
		{"stale fence", "executing", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			m, durable, binding, fence, _, rpc, _ := newRootAdmissionFixtureWithReceiver(t, false)
			binding.rpc = retirementRPC{rpc}
			host := &retirementHost{t: t, home: t.TempDir()}
			handlers, err := NewOperationalRootControlHandlers(m.manifest, host)
			require.NoError(t, err)
			m.rootControlHandlers, err = NewRootControlHandlerRegistry(m.manifest, handlers)
			require.NoError(t, err)
			requestID := "123e4567-e89b-42d3-a456-426614174390"
			_, err = durable.ReserveManagedControlRequest(ctx, store.ManagedControlRequestInput{RequestID: requestID, ResultID: "123e4567-e89b-42d3-a456-426614174391", AgentID: fence.AgentID, ToolCallID: "retired-call", CapabilityID: "mecha_worker", ToolName: "mecha_worker", DefinitionID: "def_" + strings.Repeat("a", 64), OperationID: "ops.worker", CanonicalArgumentsSHA256: strings.Repeat("b", 64), RequestFingerprint: strings.Repeat("c", 64), Fence: fence, ReceivedAt: formatManagedTime(time.Now())})
			require.NoError(t, err)
			if tc.state != "received" {
				owner := "123e4567-e89b-42d3-a456-426614174392"
				_, err = durable.ClaimManagedControlHandler(ctx, requestID, owner, fence)
				require.NoError(t, err)
				if tc.state == "side_effect_committed" {
					require.NoError(t, durable.CommitManagedControlSideEffect(ctx, requestID, owner, fence))
				}
				// Expire the lease at the real persisted boundary, without sleeping.
				paths, err := filepath.Glob(filepath.Join(filepath.Dir(host.home), "*", "router.db"))
				require.NoError(t, err)
				require.Len(t, paths, 1)
				db, err := sql.Open("sqlite", paths[0])
				require.NoError(t, err)
				_, err = db.Exec("UPDATE managed_control_requests SET handler_deadline_at='2000-01-01T00:00:00.000Z' WHERE request_id=?", requestID)
				require.NoError(t, err)
				require.NoError(t, db.Close())
			}
			if tc.saved {
				require.NoError(t, os.MkdirAll(filepath.Join(host.home, "root-control"), 0700))
				data, err := json.Marshal(map[string]any{"version": 1, "settings": map[string]any{}, "requests": map[string]any{requestID: map[string]any{"fingerprint": "original-input-fingerprint", "recorded_at": time.Now().Unix(), "result": map[string]any{"status": "ok", "content": "saved worker send success", "details": map[string]any{"message_id": "saved-mail"}}}}})
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(host.home, "root-control", "requests.json"), data, 0600))
			}
			if tc.stale {
				binding.runtimeGeneration = "123e4567-e89b-42d3-a456-426614174399"
			}
			m.recoverRootControls(binding)
			row, err := durable.ManagedControlRequest(ctx, requestID)
			require.NoError(t, err)
			if tc.stale {
				require.Equal(t, tc.state, row.State)
				require.Zero(t, host.reconciles)
				require.Zero(t, rpc.submissionCount())
				return
			}
			require.Equal(t, "delivered", row.State)
			if tc.state == "received" {
				require.Equal(t, "ROOT_OPERATION_UNAVAILABLE", row.ErrorCode)
			} else {
				require.Equal(t, "ROOT_OPERATION_UNKNOWN", row.ErrorCode)
			}
			require.Zero(t, host.executes)
			require.Zero(t, host.reconciles, "cold recovery has no verified arguments")
			m.rootControlHandlers = nil
			m.recoverRootControls(binding)
			m.resumeRootControl(ctx, binding, requestID, nil)
			require.Equal(t, 1, rpc.submissionCount(), "durable delivery must not append twice")
		})
	}
}

func TestRetiredWorkerRecoveryPreservesOtherControlAndDurableResult(t *testing.T) {
	ctx := context.Background()
	m, durable, binding, fence, _, rpc, _ := newRootAdmissionFixtureWithReceiver(t, false)
	// The shared admission fixture selects retired ops.git_status. Use a live
	// operation here so this test actually verifies unrelated dispatch.
	var tool ManifestTool
	for _, candidate := range m.manifest.Tools {
		if candidate.OperationID == "reminder.control" {
			tool = candidate
			break
		}
	}
	require.NotEmpty(t, tool.Name)
	handler := &rootAdmissionHandler{identity: RootControlHandlerIdentity{CapabilityID: tool.CapabilityID, ToolName: tool.Name, DefinitionID: tool.DefinitionID, DefinitionSHA256: tool.DefinitionSHA256, OperationID: tool.OperationID}}
	handlers := defaultRootControlHandlers(m.manifest)
	for i, candidate := range m.manifest.Tools {
		if candidate.OperationID == tool.OperationID {
			handlers[i] = handler
		}
	}
	var err error
	m.rootControlHandlers, err = NewRootControlHandlerRegistry(m.manifest, handlers)
	require.NoError(t, err)
	binding.rpc = retirementRPC{rpc}
	rpc.armErr = context.Canceled
	requestID := "123e4567-e89b-42d3-a456-426614174393"
	m.admitAndRunRootControl(ctx, binding, rootAdmissionEvent(fence, tool, requestID, map[string]any{"action": "list"}))
	row, err := durable.ManagedControlRequest(ctx, requestID)
	require.NoError(t, err)
	require.Equal(t, "completed", row.State)
	require.Equal(t, 1, handler.callCount())
	// A persisted result remains deliverable after its live handler disappears.
	m.rootControlHandlers = nil
	rpc.armErr = nil
	m.recoverRootControls(binding)
	m.recoverRootControls(binding)
	row, err = durable.ManagedControlRequest(ctx, requestID)
	require.NoError(t, err)
	require.Equal(t, "delivered", row.State)
	require.Equal(t, 1, rpc.submissionCount())
	require.Equal(t, 1, handler.callCount())
}

func TestRetiredWorkerRecoveryExecutorFunc(t *testing.T) {
	for _, form := range []string{"value", "pointer", "nil pointer"} {
		for _, state := range []string{"executing", "side_effect_committed"} {
			for _, explicitReconcile := range []bool{false, true} {
				if form == "nil pointer" && explicitReconcile {
					continue
				}
				name := form + "/" + state + "/no reconcile"
				if explicitReconcile {
					name = strings.TrimSuffix(name, "no reconcile") + "explicit reconcile"
				}
				t.Run(name, func(t *testing.T) {
					ctx := context.Background()
					m, durable, binding, fence, _, rpc, _ := newRootAdmissionFixtureWithReceiver(t, false)
					binding.rpc = retirementRPC{rpc}
					executes, reconciles := 0, 0
					f := RootControlOperationExecutorFunc{
						ExecuteFunc: func(context.Context, RootControlHandlerIdentity, RootControlInvocation) (RootControlHandlerResult, error) {
							executes++
							return RootControlHandlerResult{Status: "ok", Content: "unsafe replay"}, nil
						},
					}
					if explicitReconcile {
						f.ReconcileFunc = func(_ context.Context, identity RootControlHandlerIdentity, in RootControlInvocation) (RootControlHandlerResult, error) {
							reconciles++
							require.Equal(t, "ops.worker", identity.OperationID)
							require.Equal(t, "mecha_worker", identity.CapabilityID)
							require.Equal(t, "ops.worker", in.OperationID)
							require.Equal(t, fence, in.Fence)
							return RootControlHandlerResult{Status: "ok", Content: "saved worker result"}, nil
						}
					}
					var executor RootControlOperationExecutor = f
					switch form {
					case "pointer":
						executor = &f
					case "nil pointer":
						executor = (*RootControlOperationExecutorFunc)(nil)
					}
					handlers, err := NewOperationalRootControlHandlers(m.manifest, executor)
					require.NoError(t, err)
					m.rootControlHandlers, err = NewRootControlHandlerRegistry(m.manifest, handlers)
					require.NoError(t, err)
					requestID := "123e4567-e89b-42d3-a456-426614174394"
					_, err = durable.ReserveManagedControlRequest(ctx, store.ManagedControlRequestInput{RequestID: requestID, ResultID: "123e4567-e89b-42d3-a456-426614174395", AgentID: fence.AgentID, ToolCallID: "retired-func-call", CapabilityID: "mecha_worker", ToolName: "mecha_worker", DefinitionID: "def_" + strings.Repeat("a", 64), OperationID: "ops.worker", CanonicalArgumentsSHA256: strings.Repeat("b", 64), RequestFingerprint: strings.Repeat("c", 64), Fence: fence, ReceivedAt: formatManagedTime(time.Now())})
					require.NoError(t, err)
					owner := "123e4567-e89b-42d3-a456-426614174396"
					_, err = durable.ClaimManagedControlHandler(ctx, requestID, owner, fence)
					require.NoError(t, err)
					if state == "side_effect_committed" {
						require.NoError(t, durable.CommitManagedControlSideEffect(ctx, requestID, owner, fence))
					}
					paths, err := filepath.Glob(filepath.Join(t.TempDir(), "..", "*", "router.db"))
					require.NoError(t, err)
					require.Len(t, paths, 1)
					db, err := sql.Open("sqlite", paths[0])
					require.NoError(t, err)
					_, err = db.Exec("UPDATE managed_control_requests SET handler_deadline_at='2000-01-01T00:00:00.000Z' WHERE request_id=?", requestID)
					require.NoError(t, err)
					require.NoError(t, db.Close())

					m.recoverRootControls(binding)
					row, err := durable.ManagedControlRequest(ctx, requestID)
					require.NoError(t, err)
					require.Zero(t, executes, "retired recovery must never fall back to Execute")
					require.Equal(t, "delivered", row.State)
					require.Zero(t, reconciles)
					require.Equal(t, "ROOT_OPERATION_UNKNOWN", row.ErrorCode)
					require.Equal(t, 1, rpc.submissionCount())
				})
			}
		}
	}
}
