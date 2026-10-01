package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// B owns a receipt and history row, but its deduped delivery is A's exact
// incarnation. B's history incarnation must not be used to decide resolution.
func TestReceiptLifecycleDedupAliasTracksOriginalDeliveryIncarnation(t *testing.T) {
	s, clock, _ := lifecycleFixture(t)
	ctx := context.Background()
	a := testEnvelope("alias-delivery-a", "shared body")
	first, err := s.Enqueue(ctx, a, EnqueueOptions{Conversation: &ConversationOptions{Body: &a.Body}})
	require.NoError(t, err)
	require.True(t, first.Created)
	require.NoError(t, clock.Advance(time.Millisecond))
	b := testEnvelope("alias-receipt-b", a.Body)
	alias, err := s.Enqueue(ctx, b, EnqueueOptions{Conversation: &ConversationOptions{Body: &b.Body}})
	require.NoError(t, err)
	require.True(t, alias.Deduped)
	require.False(t, alias.Created)
	require.Equal(t, first.Message, alias.Message)
	require.NotEqual(t, first.ConversationMessage.MessageIncarnationID, alias.ConversationMessage.MessageIncarnationID)
	beforeCount, beforeBytes := lifecycleUsage(t, s)
	require.Equal(t, int64(2), beforeCount)

	var deliveryID, deliveryIncarnation, accepted, deadline, originalDeliveryJSON string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT json_extract(delivery_json,'$.id'),json_extract(delivery_json,'$.message_incarnation_id'),accepted_at,deadline_at,delivery_json FROM conversation_send_receipts WHERE message_id=?`, b.ID).Scan(&deliveryID, &deliveryIncarnation, &accepted, &deadline, &originalDeliveryJSON))
	require.Equal(t, a.ID, deliveryID)
	require.Equal(t, first.Message.MessageIncarnationID, deliveryIncarnation)
	require.Equal(t, formatTimestamp(clock.Now()), accepted)
	require.Equal(t, formatTimestamp(clock.Now().Add(receiptHorizon)), deadline)
	// Keep A live until B's deadline; ordinary delivery expiry is only an
	// hour, while this regression concerns the receipt's 30-day clock.
	_, err = s.db.ExecContext(ctx, `UPDATE messages SET expires_at=? WHERE id=? AND message_incarnation_id=?`, formatTimestamp(clock.Now().Add(receiptHorizon+time.Hour)), a.ID, deliveryIncarnation)
	require.NoError(t, err)

	checkAlias := func(wantReceipt int) {
		t.Helper()
		count, bytes := lifecycleUsage(t, s)
		require.Equal(t, beforeCount, count)
		require.Equal(t, beforeBytes, bytes)
		var receipts, history, delivery, bDelivery int
		var persistedDeliveryJSON string
		require.NoError(t, s.db.QueryRowContext(ctx, `SELECT delivery_json FROM conversation_send_receipts WHERE message_id=?`, b.ID).Scan(&persistedDeliveryJSON))
		require.Equal(t, originalDeliveryJSON, persistedDeliveryJSON)
		require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM conversation_send_receipts WHERE message_id=?`, b.ID).Scan(&receipts))
		require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM conversation_messages WHERE message_id=?`, b.ID).Scan(&history))
		require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE id=? AND message_incarnation_id=? AND state='queued'`, a.ID, deliveryIncarnation).Scan(&delivery))
		require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE id=?`, b.ID).Scan(&bDelivery))
		require.Equal(t, wantReceipt, receipts)
		require.Equal(t, 1, history)
		require.Equal(t, 1, delivery)
		require.Zero(t, bDelivery)
	}

	replayed, err := s.Enqueue(ctx, b, EnqueueOptions{Conversation: &ConversationOptions{Body: &b.Body}})
	require.NoError(t, err)
	require.False(t, replayed.Created)
	require.Equal(t, alias.Message, replayed.Message)
	require.Equal(t, alias.ConversationMessage, replayed.ConversationMessage)
	changed := b
	changed.Body = "changed body"
	_, err = s.Enqueue(ctx, changed, EnqueueOptions{Conversation: &ConversationOptions{Body: &changed.Body}})
	require.ErrorAs(t, err, new(*IdempotencyConflictError))
	checkAlias(1)

	require.NoError(t, clock.Advance(receiptHorizon))
	for _, envelope := range []struct {
		name string
		body string
	}{{"same", b.Body}, {"changed", changed.Body}} {
		t.Run(envelope.name, func(t *testing.T) {
			retry := b
			retry.Body = envelope.body
			_, err := s.Enqueue(ctx, retry, EnqueueOptions{Conversation: &ConversationOptions{Body: &retry.Body}})
			var unresolved *ConversationReceiptExpiredUnresolvedError
			require.ErrorAs(t, err, &unresolved)
			require.Equal(t, "conversation_receipt_expired_unresolved", unresolved.Code())
			require.Equal(t, b.ID, unresolved.MessageID)
			require.Equal(t, accepted, unresolved.AcceptedAt)
			require.Equal(t, deadline, unresolved.DeadlineAt)
			require.Equal(t, "queued", unresolved.DeliveryState)
			require.Equal(t, deliveryIncarnation, unresolved.IncarnationID)
			checkAlias(1)
		})
	}
	result, err := s.SweepLifecycle(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	require.Zero(t, result.ReceiptsPruned, "A and B both still refer to the live original delivery")
	checkAlias(1)

	lifecycleACK(t, s, a.ID)
	// Replace A's ID with a different live incarnation. That replacement
	// cannot keep B pinned to the already-ACKed original delivery.
	replacement := a
	replacement.Body = "replacement body"
	// An ordinary replacement avoids pruning B as a side effect of admitting A.
	newA, err := s.Enqueue(ctx, replacement)
	require.NoError(t, err)
	require.True(t, newA.Created)
	require.NotEqual(t, deliveryIncarnation, newA.Message.MessageIncarnationID)
	var oldBReceipt int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM conversation_send_receipts WHERE message_id=?`, b.ID).Scan(&oldBReceipt))
	require.Equal(t, 1, oldBReceipt, "replacement A does not erase B's receipt before pruning")
	var retainedDeliveryJSON string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT delivery_json FROM conversation_send_receipts WHERE message_id=?`, b.ID).Scan(&retainedDeliveryJSON))
	require.Equal(t, originalDeliveryJSON, retainedDeliveryJSON)
	fresh, err := s.Enqueue(ctx, b, EnqueueOptions{Conversation: &ConversationOptions{Body: &b.Body}})
	require.NoError(t, err)
	require.True(t, fresh.Created, "ACK of the original permits a new B effect")
	require.False(t, fresh.Deduped)
	require.Equal(t, b.ID, fresh.Message.ID)
	require.NotEqual(t, alias.ConversationMessage.MessageIncarnationID, fresh.Message.MessageIncarnationID)
	require.NotEqual(t, alias.ConversationMessage.Sequence, fresh.ConversationMessage.Sequence)
	var bDeliveries, bHistory, bReceipts int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE id=?`, b.ID).Scan(&bDeliveries))
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM conversation_messages WHERE message_id=?`, b.ID).Scan(&bHistory))
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM conversation_send_receipts WHERE message_id=?`, b.ID).Scan(&bReceipts))
	require.Equal(t, 1, bDeliveries)
	require.Equal(t, 2, bHistory)
	require.Equal(t, 1, bReceipts)
	var aReceipts int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM conversation_send_receipts WHERE message_id=?`, a.ID).Scan(&aReceipts))
	require.Zero(t, aReceipts)
	var freshDeliveryID, freshDeliveryIncarnation string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT json_extract(delivery_json,'$.id'),json_extract(delivery_json,'$.message_incarnation_id') FROM conversation_send_receipts WHERE message_id=?`, b.ID).Scan(&freshDeliveryID, &freshDeliveryIncarnation))
	require.Equal(t, fresh.Message.ID, freshDeliveryID)
	require.Equal(t, fresh.Message.MessageIncarnationID, freshDeliveryIncarnation)
	count, bytes := lifecycleUsage(t, s)
	require.Equal(t, int64(1), count)
	rows, charged := sweepReceiptRows(t, s)
	require.Equal(t, rows, count)
	require.Equal(t, charged, bytes)
}
