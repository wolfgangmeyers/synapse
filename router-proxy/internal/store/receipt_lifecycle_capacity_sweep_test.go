package store

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

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
	require.Equal(t, rows, count)
	require.Equal(t, serialized, bytes)
}

func TestReceiptLifecycleIndependentCleanupDrainsMultipleBatches(t *testing.T) {
	s, clock, _ := lifecycleFixture(t)
	ctx := context.Background()
	for i := 0; i < 2*receiptPruneBatch+1; i++ {
		id := fmt.Sprintf("queued-%03d", i)
		e := testEnvelope(id, "é queued "+id)
		e.To = fmt.Sprintf("target-%03d", i)
		_, err := s.Enqueue(ctx, e, EnqueueOptions{Conversation: &ConversationOptions{Body: &e.Body}})
		require.NoError(t, err)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET state='dead_letter' WHERE id='queued-000'`)
	require.NoError(t, err)
	require.NoError(t, clock.Advance(receiptHorizon))
	n, err := s.PruneExpiredConversationReceipts(ctx)
	require.NoError(t, err)
	require.Equal(t, 2*receiptPruneBatch+1, n)
	sweepReceiptAccounting(t, s, 0, 0)
	var deliveries, dead, history int
	require.NoError(t, s.db.QueryRow(`SELECT count(*),sum(state='dead_letter') FROM messages`).Scan(&deliveries, &dead))
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM conversation_messages`).Scan(&history))
	require.Equal(t, 2*receiptPruneBatch+1, deliveries)
	require.Equal(t, 1, dead)
	require.Equal(t, deliveries, history)
}

func TestReceiptLifecycleExactByteCapCleanupSurvivesAdmissionFailure(t *testing.T) {
	s, clock, _ := lifecycleFixture(t)
	ctx := context.Background()
	protected, err := lifecycleSend(s, "protected-utf8", "é queued")
	require.NoError(t, err)
	charge := sweepReceiptCharge(t, s, protected.Message.ID)
	var maxCount, maxBytes int64
	require.NoError(t, s.db.QueryRow(`SELECT max_count,max_bytes FROM conversation_send_receipt_capacity WHERE singleton=1`).Scan(&maxCount, &maxBytes))
	require.Equal(t, int64(100000), maxCount)
	require.Equal(t, int64(200*1024*1024), maxBytes)
	_, err = s.db.ExecContext(ctx, `UPDATE conversation_send_receipt_capacity SET max_count=1,max_bytes=? WHERE singleton=1`, charge)
	require.NoError(t, err)
	_, err = lifecycleSend(s, "blocked", "new")
	require.ErrorAs(t, err, new(*ConversationReceiptCapacityError))
	sweepReceiptAccounting(t, s, 1, charge)
	require.NoError(t, clock.Advance(receiptHorizon))
	_, err = lifecycleSend(s, "too-large", "é"+strings.Repeat("x", int(charge)))
	require.ErrorAs(t, err, new(*ConversationReceiptCapacityError))
	sweepReceiptAccounting(t, s, 0, 0)
	var oldDelivery, failed int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM messages WHERE id=?`, protected.Message.ID).Scan(&oldDelivery))
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM messages WHERE id='too-large'`).Scan(&failed))
	require.Equal(t, 1, oldDelivery)
	require.Zero(t, failed)
	fresh, err := lifecycleSend(s, "new", "é")
	require.NoError(t, err)
	require.True(t, fresh.Created)
	sweepReceiptAccounting(t, s, 1, sweepReceiptCharge(t, s, fresh.Message.ID))
}

func TestReceiptLifecycleAdmissionFailureDoesNotUndoCommittedCleanup(t *testing.T) {
	s, clock, _ := lifecycleFixture(t)
	ctx := context.Background()
	first, err := lifecycleSend(s, "fault-old", "é")
	require.NoError(t, err)
	require.NoError(t, clock.Advance(receiptHorizon))
	_, err = s.db.ExecContext(ctx, `CREATE TEMP TRIGGER receipt_admission_fault BEFORE INSERT ON main.conversation_send_receipts WHEN NEW.message_id='fault-new' BEGIN SELECT RAISE(ABORT,'injected receipt insert failure'); END`)
	require.NoError(t, err)
	_, err = lifecycleSend(s, "fault-new", "é new")
	require.ErrorContains(t, err, "injected receipt insert failure")
	sweepReceiptAccounting(t, s, 0, 0)
	for table, column := range map[string]string{"messages": "id", "conversation_messages": "message_id", "conversation_send_receipts": "message_id"} {
		var n int
		require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM `+table+` WHERE `+column+`='fault-new'`).Scan(&n))
		require.Zero(t, n, table)
	}
	var original int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM messages WHERE id=?`, first.Message.ID).Scan(&original))
	require.Equal(t, 1, original)
}
