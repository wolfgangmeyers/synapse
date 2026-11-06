package store

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// Independent handles must serialize expiry pruning, admission, and capacity charging.
func TestConversationReceiptLifecycleConcurrentExpiredResolvedSameID(t *testing.T) {
	for _, tc := range []struct {
		name, competingBody string
		conflict            bool
	}{
		{name: "exact_replay", competingBody: "second incarnation"},
		{name: "changed_body_conflict", competingBody: "different incarnation", conflict: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			firstHandle, clock, path := lifecycleFixture(t)
			old, err := lifecycleSend(firstHandle, "raced-id", "original")
			require.NoError(t, err)
			lifecycleACK(t, firstHandle, old.Message.ID)
			var oldDelivery int
			require.NoError(t, firstHandle.db.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE id=? AND message_incarnation_id=?`, old.Message.ID, old.Message.MessageIncarnationID).Scan(&oldDelivery))
			require.Zero(t, oldDelivery, "ACK removes delivery, not retained conversation history")
			require.NoError(t, clock.Advance(receiptHorizon))
			secondHandle, err := Open(ctx, path, Options{Clock: clock})
			require.NoError(t, err)
			defer secondHandle.Close()

			type outcome struct {
				result EnqueueResult
				err    error
			}
			results := make(chan outcome, 2)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i, handle := range []*Store{firstHandle, secondHandle} {
				body := tc.competingBody
				if tc.conflict && i == 1 {
					body = "different from competing body"
				}
				wg.Add(1)
				go func(s *Store, body string) {
					defer wg.Done()
					<-start
					result, err := lifecycleSend(s, old.Message.ID, body)
					results <- outcome{result: result, err: err}
				}(handle, body)
			}
			close(start)
			wg.Wait()
			close(results)
			created, replays, conflicts := 0, 0, 0
			var fresh EnqueueResult
			for attempt := range results {
				if attempt.err != nil {
					var conflict *IdempotencyConflictError
					require.ErrorAs(t, attempt.err, &conflict)
					conflicts++
					continue
				}
				if attempt.result.Created {
					created++
					fresh = attempt.result
				} else {
					replays++
					fresh = attempt.result
				}
			}
			require.Equal(t, 1, created, "only one new incarnation can be admitted")
			if tc.conflict {
				require.Zero(t, replays)
				require.Equal(t, 1, conflicts)
			} else {
				require.Equal(t, 1, replays)
				require.Zero(t, conflicts)
			}
			require.NotEqual(t, old.Message.MessageIncarnationID, fresh.Message.MessageIncarnationID)
			require.NotEqual(t, old.ConversationMessage.Sequence, fresh.ConversationMessage.Sequence)
			var deliveries, histories, receipts int
			require.NoError(t, firstHandle.db.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE id=?`, old.Message.ID).Scan(&deliveries))
			require.NoError(t, firstHandle.db.QueryRowContext(ctx, `SELECT count(*) FROM conversation_messages WHERE message_id=?`, old.Message.ID).Scan(&histories))
			require.NoError(t, firstHandle.db.QueryRowContext(ctx, `SELECT count(*) FROM conversation_send_receipts WHERE message_id=?`, old.Message.ID).Scan(&receipts))
			require.Equal(t, 1, deliveries)
			require.Equal(t, 2, histories, "old history remains separate from the new incarnation")
			require.Equal(t, 1, receipts)
			count, bytes := lifecycleUsage(t, firstHandle)
			require.Equal(t, int64(1), count, "prune once and charge once")
			var exactBytes int64
			require.NoError(t, firstHandle.db.QueryRowContext(ctx, `SELECT length(CAST(message_id AS BLOB))+length(CAST(conversation_json AS BLOB))+length(CAST(delivery_json AS BLOB)) FROM conversation_send_receipts WHERE message_id=?`, old.Message.ID).Scan(&exactBytes))
			require.Equal(t, exactBytes, bytes)
		})
	}
}

func TestConversationReceiptLifecycleFullCapInWindowReplay(t *testing.T) {
	ctx := context.Background()
	s, _, _ := lifecycleFixture(t)
	first, err := lifecycleSend(s, "full-cap-replay-id", "original")
	require.NoError(t, err)
	lifecycleACK(t, s, first.Message.ID)
	count, bytes := lifecycleUsage(t, s)
	require.Equal(t, int64(1), count)
	_, err = s.db.ExecContext(ctx, `UPDATE conversation_send_receipt_capacity SET max_count=1,max_bytes=? WHERE singleton=1`, bytes)
	require.NoError(t, err)
	_, err = lifecycleSend(s, "blocked-by-cap", "other")
	require.ErrorAs(t, err, new(*ConversationReceiptCapacityError))
	replayed, err := lifecycleSend(s, first.Message.ID, "original")
	require.NoError(t, err)
	require.False(t, replayed.Created)
	require.Equal(t, first.Message, replayed.Message)
	require.Equal(t, first.ConversationMessage, replayed.ConversationMessage)
	_, err = lifecycleSend(s, first.Message.ID, "changed")
	require.ErrorAs(t, err, new(*IdempotencyConflictError))
	gotCount, gotBytes := lifecycleUsage(t, s)
	require.Equal(t, count, gotCount)
	require.Equal(t, bytes, gotBytes)
	for table, column := range map[string]string{"messages": "id", "conversation_messages": "message_id", "conversation_send_receipts": "message_id"} {
		var n int
		require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE `+column+`='blocked-by-cap'`).Scan(&n))
		require.Zero(t, n, table)
	}
}

// Store lifecycle sweep deliberately does not own queued expiry publication.
// The transport's recovery/publication sequence must be tested at the Router layer.
func TestConversationReceiptLifecycleQueuedExpiryRequiresTransportRecovery(t *testing.T) {
	ctx := context.Background()
	s, clock, _ := lifecycleFixture(t)
	first, err := lifecycleSend(s, "queued-recovery-id", "original")
	require.NoError(t, err)
	require.NoError(t, clock.Advance(receiptHorizon))
	result, err := s.SweepLifecycle(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	require.Equal(t, 1, result.ReceiptsPruned)
	replay, err := lifecycleSend(s, first.Message.ID, "original")
	require.NoError(t, err)
	require.False(t, replay.Created)
	_, err = lifecycleSend(s, first.Message.ID, "different")
	require.ErrorAs(t, err, new(*IdempotencyConflictError))
	count, bytes := lifecycleUsage(t, s)
	require.Zero(t, count)
	require.Zero(t, bytes)
	var delivery, history int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE id=? AND message_incarnation_id=?`, first.Message.ID, first.Message.MessageIncarnationID).Scan(&delivery))
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM conversation_messages WHERE message_id=?`, first.Message.ID).Scan(&history))
	require.Equal(t, 1, delivery)
	require.Equal(t, 1, history)
}
