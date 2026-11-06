package transport

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testsupport/storetest"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// Exercises the ordinary HTTP admission, real SQLite receipt/queue, delivery
// acknowledgement, history retention sweep, and HTTP retry on one owned DB.
func TestConversationReceiptLifecycleHTTPStoreDelivery(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	path := filepath.Join(t.TempDir(), "router.db")
	opts := store.Options{Clock: clock}
	if opts.Migrations == nil {
		if err := storetest.CloneTemplateDB(t, path); err == nil {
			opts.Migrations = []store.Migration{}
		}
	}
	durable, err := store.Open(ctx, path, opts)
	require.NoError(t, err)
	defer durable.Close()
	router, err := New(Options{Store: durable, Clock: clock})
	require.NoError(t, err)
	defer router.Close(ctx)

	// Independent handle changes only this test's owned capacity configuration.
	check, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer check.Close()
	_, err = check.ExecContext(ctx, `UPDATE conversation_send_receipt_capacity SET max_count=2 WHERE singleton=1`)
	require.NoError(t, err)
	post := func(id, body string, recipient ...string) (int, map[string]any) {
		t.Helper()
		to := "receipt-target"
		if len(recipient) > 0 {
			to = recipient[0]
		}
		payload, e := json.Marshal(map[string]string{
			"id": id, "from": "receipt-sender", "to": to, "body": body,
			"created_at": "2026-10-01T12:00:00.000Z",
		})
		require.NoError(t, e)
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		var result map[string]any
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
		return response.Code, result
	}
	usage := func() (int, int) {
		t.Helper()
		var receipts, deliveries int
		require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM conversation_send_receipts`).Scan(&receipts))
		require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM messages`).Scan(&deliveries))
		return receipts, deliveries
	}
	originalBody := "first private content"
	status, original := post("resolved-id", originalBody)
	require.Equal(t, http.StatusCreated, status)
	firstIncarnation := original["message"].(map[string]any)["message_incarnation_id"]
	status, _ = post("unresolved-id", "unresolved private content", "receipt-unresolved-target")
	require.Equal(t, http.StatusCreated, status)
	// Resolve only the original delivery through the actual claim/injection/ACK path.
	_, err = durable.AcquireAgentLease(ctx, store.LeaseRequest{AgentID: "receipt-target", InstanceID: "fixture-instance", LeaseID: "fixture-lease"})
	require.NoError(t, err)
	claimed, err := durable.ClaimNextMessage(ctx, store.ClaimRequest{AgentID: "receipt-target", InstanceID: "fixture-instance", LeaseID: "fixture-lease"})
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.Equal(t, "resolved-id", claimed.Message.ID)
	ack := store.ReceiptRequest{AgentID: "receipt-target", InstanceID: "fixture-instance", LeaseID: "fixture-lease", MessageID: "resolved-id", AttemptNumber: claimed.AttemptNumber, DeliveryItems: claimed.Items}
	_, err = durable.RecordInjectionRequested(ctx, ack)
	require.NoError(t, err)
	_, err = durable.AcknowledgeDelivery(ctx, ack)
	require.NoError(t, err)

	require.NoError(t, clock.Advance(30*time.Minute-time.Millisecond))
	status, replay := post("resolved-id", originalBody)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, original["conversation_message"], replay["conversation_message"])
	status, conflict := post("resolved-id", "changed")
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "idempotency_conflict", conflict["error"].(map[string]any)["code"])
	status, capacity := post("blocked-id", "new")
	require.Equal(t, http.StatusInsufficientStorage, status)
	require.Equal(t, "conversation_receipt_capacity", capacity["error"].(map[string]any)["code"])
	receipts, deliveries := usage()
	require.Equal(t, 2, receipts)
	require.Equal(t, 1, deliveries)

	require.NoError(t, clock.Advance(time.Millisecond)) // exact accepted-at + 30 minutes
	pruned, err := durable.PruneExpiredConversationReceipts(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, pruned)
	status, _ = post("unresolved-id", "unresolved private content", "receipt-unresolved-target")
	require.Equal(t, http.StatusOK, status)
	status, conflict = post("unresolved-id", "different private content", "receipt-unresolved-target")
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "idempotency_conflict", conflict["error"].(map[string]any)["code"])
	receipts, deliveries = usage()
	require.Zero(t, receipts)
	require.Equal(t, 1, deliveries)
	status, reused := post("resolved-id", "new incarnation", "receipt-second-target")
	require.Equal(t, http.StatusCreated, status, reused)
	require.NotEqual(t, firstIncarnation, reused["message"].(map[string]any)["message_incarnation_id"])
	// Receipt expiry does not remove the older conversation history.
	rows, err := check.QueryContext(ctx, `SELECT conversation_sequence, message_incarnation_id, sender, recipient, receipt_backed FROM conversation_messages WHERE message_id='resolved-id' ORDER BY conversation_sequence`)
	require.NoError(t, err)
	var histories []struct {
		sequence                       int
		incarnation, sender, recipient string
		receiptBacked                  int
	}
	for rows.Next() {
		var h struct {
			sequence                       int
			incarnation, sender, recipient string
			receiptBacked                  int
		}
		require.NoError(t, rows.Scan(&h.sequence, &h.incarnation, &h.sender, &h.recipient, &h.receiptBacked))
		histories = append(histories, h)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Len(t, histories, 2, "old history must survive while reuse creates one fresh row")
	require.Equal(t, 1, histories[0].sequence)
	require.Equal(t, firstIncarnation, histories[0].incarnation)
	require.Equal(t, "receipt-sender", histories[0].sender)
	require.Equal(t, "receipt-target", histories[0].recipient)
	require.Equal(t, 1, histories[0].receiptBacked)
	require.Equal(t, 3, histories[1].sequence)
	require.Equal(t, reused["message"].(map[string]any)["message_incarnation_id"], histories[1].incarnation)
	require.NotEqual(t, histories[0].incarnation, histories[1].incarnation)
	require.Equal(t, "receipt-sender", histories[1].sender)
	require.Equal(t, "receipt-second-target", histories[1].recipient)
	require.Equal(t, 1, histories[1].receiptBacked)
	var originalDeliveries, freshDeliveries, unresolvedDeliveries int
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE id='resolved-id' AND message_incarnation_id=?`, firstIncarnation).Scan(&originalDeliveries))
	require.Zero(t, originalDeliveries, "ACKed original delivery must remain absent")
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE id='resolved-id' AND message_incarnation_id=? AND recipient='receipt-second-target' AND state='queued'`, histories[1].incarnation).Scan(&freshDeliveries))
	require.Equal(t, 1, freshDeliveries, "reuse must enqueue exactly one new delivery")
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE id='unresolved-id' AND recipient='receipt-unresolved-target' AND state='queued'`).Scan(&unresolvedDeliveries))
	require.Equal(t, 1, unresolvedDeliveries, "unresolved delivery must remain live")
	receipts, deliveries = usage()
	require.Equal(t, 1, receipts)
	require.Equal(t, 2, deliveries)
	_, err = durable.AcquireAgentLease(ctx, store.LeaseRequest{AgentID: "receipt-second-target", InstanceID: "second-instance", LeaseID: "second-lease"})
	require.NoError(t, err)
	fresh, err := durable.ClaimNextMessage(ctx, store.ClaimRequest{AgentID: "receipt-second-target", InstanceID: "second-instance", LeaseID: "second-lease"})
	require.NoError(t, err)
	require.NotNil(t, fresh)
	require.Equal(t, "resolved-id", fresh.Message.ID)
	freshACK := store.ReceiptRequest{AgentID: "receipt-second-target", InstanceID: "second-instance", LeaseID: "second-lease", MessageID: "resolved-id", AttemptNumber: fresh.AttemptNumber, DeliveryItems: fresh.Items}
	_, err = durable.RecordInjectionRequested(ctx, freshACK)
	require.NoError(t, err)
	_, err = durable.AcknowledgeDelivery(ctx, freshACK)
	require.NoError(t, err)
	receipts, deliveries = usage()
	require.Equal(t, 1, receipts)
	require.Equal(t, 1, deliveries)
}
