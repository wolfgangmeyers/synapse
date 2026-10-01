package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Charge from the persisted JSON, not from the input envelope or Go struct.
func sweepReceiptCharge(t *testing.T, s *Store, id string) int64 {
	t.Helper()
	var bytes int64
	require.NoError(t, s.db.QueryRow(`SELECT length(CAST(message_id AS BLOB))+length(CAST(conversation_json AS BLOB))+length(CAST(delivery_json AS BLOB)) FROM conversation_send_receipts WHERE message_id=?`, id).Scan(&bytes))
	return bytes
}

func sweepReceiptRows(t *testing.T, s *Store) (int64, int64) {
	t.Helper()
	var count, bytes int64
	require.NoError(t, s.db.QueryRow(`SELECT count(*),coalesce(sum(length(CAST(message_id AS BLOB))+length(CAST(conversation_json AS BLOB))+length(CAST(delivery_json AS BLOB))),0) FROM conversation_send_receipts`).Scan(&count, &bytes))
	return count, bytes
}

func sweepReceiptAccounting(t *testing.T, s *Store, wantCount, wantBytes int64) {
	t.Helper()
	count, bytes := lifecycleUsage(t, s)
	rows, serialized := sweepReceiptRows(t, s)
	require.Equal(t, wantCount, count)
	require.Equal(t, wantBytes, bytes)
	require.Equal(t, rows, count, "count must agree with persisted rows")
	require.Equal(t, serialized, bytes, "bytes must equal persisted UTF-8 JSON charge")
}

func TestReceiptLifecycleCapacitySweepSkipsUnresolvedPrefixAndDrainsBounded(t *testing.T) {
	for _, mode := range []string{"Sweep", "SweepLifecycle"} {
		t.Run(mode, func(t *testing.T) {
			s, clock, _ := lifecycleFixture(t)
			ctx := context.Background()
			// All deadlines are equal. The unresolved IDs sort before the eligible
			// IDs, so LIMIT before checking resolution would starve the batch.
			var eligibleBytes int64
			for i := 0; i <= receiptPruneBatch; i++ {
				id := fmt.Sprintf("a-unresolved-%03d", i)
				e := testEnvelope(id, "é queued "+id)
				e.To = fmt.Sprintf("agent-unresolved-%03d", i)
				_, err := s.Enqueue(ctx, e, EnqueueOptions{Conversation: &ConversationOptions{Body: &e.Body}})
				require.NoError(t, err)
				// A queued delivery can remain live past the receipt deadline.
				// Keep this owned fixture queued beyond ordinary Sweep expiry.
				_, err = s.db.ExecContext(ctx, `UPDATE messages SET expires_at=? WHERE id=?`, formatTimestamp(clock.Now().Add(receiptHorizon+48*time.Hour)), id)
				require.NoError(t, err)
			}
			for i := 0; i <= receiptPruneBatch; i++ {
				id := fmt.Sprintf("z-resolved-%03d", i)
				_, err := lifecycleSend(s, id, "é accepted "+id)
				require.NoError(t, err)
				eligibleBytes += sweepReceiptCharge(t, s, id)
				lifecycleACK(t, s, id)
			}
			initialCount, initialBytes := lifecycleUsage(t, s)
			sweepReceiptAccounting(t, s, initialCount, initialBytes)
			require.NoError(t, clock.Advance(receiptHorizon))
			invoke := func() (SweepResult, error) {
				if mode == "Sweep" {
					return s.Sweep(ctx, DefaultRetentionPolicy)
				}
				return s.SweepLifecycle(ctx, DefaultRetentionPolicy)
			}
			first, err := invoke()
			require.NoError(t, err)
			require.Equal(t, receiptPruneBatch, first.ReceiptsPruned)
			// The last eligible receipt stays, even though >batch unresolved
			// receipts are earlier in deadline order.
			sweepReceiptAccounting(t, s, initialCount-int64(receiptPruneBatch), initialBytes-eligibleBytes+sweepReceiptCharge(t, s, "z-resolved-128"))
			second, err := invoke()
			require.NoError(t, err)
			require.Equal(t, 1, second.ReceiptsPruned)
			third, err := invoke()
			require.NoError(t, err)
			require.Zero(t, third.ReceiptsPruned)
			sweepReceiptAccounting(t, s, initialCount-int64(receiptPruneBatch+1), initialBytes-eligibleBytes)
			for _, id := range []string{"a-unresolved-000", "a-unresolved-128"} {
				var receipts, deliveries int
				require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM conversation_send_receipts WHERE message_id=?`, id).Scan(&receipts))
				require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM messages WHERE id=? AND state='queued'`, id).Scan(&deliveries))
				require.Equal(t, 1, receipts)
				require.Equal(t, 1, deliveries)
			}
		})
	}
}

func TestReceiptLifecycleExactByteCapRollbackAndPruneBeforeAdmission(t *testing.T) {
	s, clock, _ := lifecycleFixture(t)
	ctx := context.Background()
	protectedEnvelope := testEnvelope("protected-utf8", "é queued")
	protectedEnvelope.To = "agent-unresolved"
	protected, err := s.Enqueue(ctx, protectedEnvelope, EnqueueOptions{Conversation: &ConversationOptions{Body: &protectedEnvelope.Body}})
	require.NoError(t, err)
	resolved, err := lifecycleSend(s, "resolved-utf8", "éé old")
	require.NoError(t, err)
	lifecycleACK(t, s, resolved.Message.ID)
	protectedCharge := sweepReceiptCharge(t, s, protected.Message.ID)
	resolvedCharge := sweepReceiptCharge(t, s, resolved.Message.ID)
	pinned := protectedCharge + resolvedCharge
	sweepReceiptAccounting(t, s, 2, pinned)
	var defaultMaxCount, defaultMaxBytes int64
	require.NoError(t, s.db.QueryRow(`SELECT max_count,max_bytes FROM conversation_send_receipt_capacity WHERE singleton=1`).Scan(&defaultMaxCount, &defaultMaxBytes))
	require.Equal(t, int64(100000), defaultMaxCount)
	require.Equal(t, int64(67108864), defaultMaxBytes)
	_, err = s.db.ExecContext(ctx, `UPDATE conversation_send_receipt_capacity SET max_count=2,max_bytes=? WHERE singleton=1`, pinned)
	require.NoError(t, err)
	replay, err := s.Enqueue(ctx, protectedEnvelope, EnqueueOptions{Conversation: &ConversationOptions{Body: &protectedEnvelope.Body}})
	require.NoError(t, err)
	require.False(t, replay.Created)
	require.Equal(t, protected.Message, replay.Message)

	assertNoEffects := func(id string, wantCount, wantBytes int64) {
		t.Helper()
		sweepReceiptAccounting(t, s, wantCount, wantBytes)
		for table, column := range map[string]string{"messages": "id", "conversation_messages": "message_id", "conversation_send_receipts": "message_id"} {
			var n int
			require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM `+table+` WHERE `+column+`=?`, id).Scan(&n))
			require.Zero(t, n, table)
		}
	}
	_, err = lifecycleSend(s, "cap-rejected-utf8", "é new")
	require.ErrorAs(t, err, new(*ConversationReceiptCapacityError))
	assertNoEffects("cap-rejected-utf8", 2, pinned)
	require.NoError(t, clock.Advance(receiptHorizon))
	// The protected queued receipt is still live at the exact deadline.
	_, err = s.Enqueue(ctx, protectedEnvelope, EnqueueOptions{Conversation: &ConversationOptions{Body: &protectedEnvelope.Body}})
	require.ErrorAs(t, err, new(*ConversationReceiptExpiredUnresolvedError))
	// New payload cannot fit even after the eligible receipt is pruned.
	_, err = lifecycleSend(s, "too-large-utf8", "é"+strings.Repeat("x", int(resolvedCharge)))
	require.ErrorAs(t, err, new(*ConversationReceiptCapacityError))
	assertNoEffects("too-large-utf8", 2, pinned)
	// The 507 rolled the prune back; the old receipt and counters remain.
	require.Equal(t, resolvedCharge, sweepReceiptCharge(t, s, resolved.Message.ID))
	// A short new send must prune before charging and fit the pinned bytes.
	fresh, err := lifecycleSend(s, "new-utf8", "é")
	require.NoError(t, err)
	require.True(t, fresh.Created)
	require.LessOrEqual(t, sweepReceiptCharge(t, s, fresh.Message.ID), resolvedCharge)
	sweepReceiptAccounting(t, s, 2, protectedCharge+sweepReceiptCharge(t, s, fresh.Message.ID))
	var old int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM conversation_send_receipts WHERE message_id=?`, resolved.Message.ID).Scan(&old))
	require.Zero(t, old)
}

func TestReceiptLifecyclePruneAndAdmissionFailureRollBackTogether(t *testing.T) {
	s, clock, _ := lifecycleFixture(t)
	ctx := context.Background()
	first, err := lifecycleSend(s, "fault-resolved-utf8", "é")
	require.NoError(t, err)
	lifecycleACK(t, s, first.Message.ID)
	beforeCount, beforeBytes := lifecycleUsage(t, s)
	require.NoError(t, clock.Advance(receiptHorizon))
	// Same narrow failure injection used by other Store tests. Admission
	// has already pruned and inserted its message/history when this fires.
	_, err = s.db.ExecContext(ctx, `CREATE TEMP TRIGGER receipt_admission_fault BEFORE INSERT ON main.conversation_send_receipts WHEN NEW.message_id='fault-new-utf8' BEGIN SELECT RAISE(ABORT,'injected receipt insert failure'); END`)
	require.NoError(t, err)
	_, err = lifecycleSend(s, "fault-new-utf8", "é new")
	require.ErrorContains(t, err, "injected receipt insert failure")
	sweepReceiptAccounting(t, s, beforeCount, beforeBytes)
	for table, column := range map[string]string{"messages": "id", "conversation_messages": "message_id", "conversation_send_receipts": "message_id"} {
		var n int
		require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM `+table+` WHERE `+column+`='fault-new-utf8'`).Scan(&n))
		require.Zero(t, n, table)
	}
	require.Equal(t, beforeBytes, sweepReceiptCharge(t, s, first.Message.ID))
}
