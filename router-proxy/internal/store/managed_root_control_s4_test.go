package store

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagedRootControlDurablyCompletesAndDeliversOneExactResult(t *testing.T) {
	store, ctx, agent, _ := restartFixture(t)
	fence := restartFenceFromAgent(&agent)
	input := lifecycleControlInput(901, agent.AgentID, fence)
	require.NoError(t, func() error { _, err := store.ReserveManagedControlRequest(ctx, input); return err }())
	active, err := store.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "active", active.ActivityState)
	owner := lifecycleUUID(902)
	claim, err := store.ClaimManagedControlHandler(ctx, input.RequestID, owner, fence)
	require.NoError(t, err)
	require.Equal(t, "executing", claim.Request.State)
	require.NoError(t, store.CommitManagedControlSideEffect(ctx, input.RequestID, owner, fence))
	completed, err := store.CompleteManagedControl(ctx, input.RequestID, owner, fence, ManagedControlResult{Status: "ok", Content: "done", DetailsJSON: `{}`, PayloadSHA256: testManifestHash, DecodedPayloadBytes: 128})
	require.NoError(t, err)
	require.Equal(t, "completed", completed.State)
	require.Equal(t, "mcr1_"+strings.ReplaceAll(input.ResultID, "-", ""), completed.ResultEntryID)
	appendOwner := lifecycleUUID(903)
	appendClaim, err := store.ClaimManagedControlAppend(ctx, input.RequestID, appendOwner, "17", fence)
	require.NoError(t, err)
	require.Equal(t, 1, appendClaim.Request.AppendAttempt)
	require.NoError(t, store.MarkManagedControlAppendSubmitted(ctx, input.RequestID, appendOwner, 1, fence))
	require.NoError(t, store.DeliverManagedControlReceipt(ctx, ManagedControlReceipt{RequestID: input.RequestID, AppendOwner: appendOwner, Attempt: 1, EntryIdentity: completed.ResultEntryID, Fence: fence}))
	row, err := store.ManagedControlRequest(ctx, input.RequestID)
	require.NoError(t, err)
	require.Equal(t, "delivered", row.State)
	require.Equal(t, completed.ResultEntryID, row.ReceiptEntryIdentity)
	settled, err := store.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", settled.ActivityState)
	_, err = store.ClaimManagedControlAppend(context.Background(), input.RequestID, lifecycleUUID(904), "18", fence)
	var stale *ManagedControlStaleError
	require.ErrorAs(t, err, &stale)
}

func TestManagedRootControlStockReceiptTransitionsToReconcilePending(t *testing.T) {
	store, ctx, agent, _ := restartFixture(t)
	fence := restartFenceFromAgent(&agent)
	input := lifecycleControlInput(905, agent.AgentID, fence)
	_, err := store.ReserveManagedControlRequest(ctx, input)
	require.NoError(t, err)
	owner := lifecycleUUID(906)
	_, err = store.ClaimManagedControlHandler(ctx, input.RequestID, owner, fence)
	require.NoError(t, err)
	require.NoError(t, store.CommitManagedControlSideEffect(ctx, input.RequestID, owner, fence))
	_, err = store.CompleteManagedControl(ctx, input.RequestID, owner, fence, ManagedControlResult{Status: "ok", Content: "stock result", DetailsJSON: `{}`, PayloadSHA256: testManifestHash, DecodedPayloadBytes: 128})
	require.NoError(t, err)

	var protocol, payloadHash, reconciliation string
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT delivery_protocol,delivery_payload_sha256,reconciliation_state FROM managed_control_requests WHERE request_id=?`, input.RequestID).Scan(&protocol, &payloadHash, &reconciliation))
	require.Equal(t, "stock_transcript_receipt_v2", protocol)
	require.Equal(t, testManifestHash, payloadHash)
	require.Equal(t, "append_pending", reconciliation)

	appendOwner := lifecycleUUID(907)
	claim, err := store.ClaimManagedControlAppend(ctx, input.RequestID, appendOwner, "stock-append", fence)
	require.NoError(t, err)
	require.NoError(t, store.MarkManagedControlAppendSubmitted(ctx, input.RequestID, appendOwner, claim.Request.AppendAttempt, fence))
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT reconciliation_state FROM managed_control_requests WHERE request_id=?`, input.RequestID).Scan(&reconciliation))
	require.Equal(t, "reconcile_pending", reconciliation)
}

func TestManagedRootControlAppendRetryStopsAtFiveAndCancellationWins(t *testing.T) {
	store, ctx, agent, _ := restartFixture(t)
	fence := restartFenceFromAgent(&agent)
	input := lifecycleControlInput(911, agent.AgentID, fence)
	_, err := store.ReserveManagedControlRequest(ctx, input)
	require.NoError(t, err)
	owner := lifecycleUUID(912)
	_, err = store.ClaimManagedControlHandler(ctx, input.RequestID, owner, fence)
	require.NoError(t, err)
	require.NoError(t, store.CommitManagedControlSideEffect(ctx, input.RequestID, owner, fence))
	_, err = store.CompleteManagedControl(ctx, input.RequestID, owner, fence, ManagedControlResult{Status: "error", Content: "failed", DetailsJSON: `{}`, PayloadSHA256: testManifestHash, DecodedPayloadBytes: 128, ErrorCode: "HANDLER_ERROR", ErrorMessage: "failed"})
	require.NoError(t, err)
	appendOwner := lifecycleUUID(913)
	claim, err := store.ClaimManagedControlAppend(ctx, input.RequestID, appendOwner, "17", fence)
	require.NoError(t, err)
	require.NoError(t, store.RetryManagedControlAppend(ctx, input.RequestID, appendOwner, claim.Request.AppendAttempt, fence, "write failed"))
	row, err := store.ManagedControlRequest(ctx, input.RequestID)
	require.NoError(t, err)
	require.Equal(t, "completed", row.State)
	require.NotEmpty(t, row.NextAppendAt)
	require.NoError(t, store.CancelManagedControlRequests(ctx, agent.AgentID, "CANCELLED_BY_RESET"))
	row, err = store.ManagedControlRequest(ctx, input.RequestID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", row.State)
	require.False(t, row.Retryable)
}
