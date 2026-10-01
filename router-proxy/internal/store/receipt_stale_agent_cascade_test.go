package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// SweepLifecycle must leave queued expiry to the transport recovery path.
// A stale recipient is not safe to cascade-delete while it owns a delivery.
func TestConversationReceiptLifecycleREDStaleAgentQueuedDelivery(t *testing.T) {
	for _, tc := range []struct {
		name         string
		disconnected bool
	}{
		{name: "never_connected"},
		{name: "disconnected", disconnected: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, clock, _ := lifecycleFixture(t)
			envelope := testEnvelope("stale-recipient-id", "queued body")
			envelope.To = "stale-recipient"
			accepted, err := s.Enqueue(ctx, envelope, EnqueueOptions{Conversation: &ConversationOptions{Body: &envelope.Body}})
			require.NoError(t, err)
			require.True(t, accepted.Created)
			if tc.disconnected {
				_, err = s.db.ExecContext(ctx, `UPDATE agents SET last_connected_at=first_seen_at, disconnected_at=first_seen_at WHERE agent_id=?`, envelope.To)
				require.NoError(t, err)
			}
			require.NoError(t, clock.Advance(receiptHorizon+time.Hour))
			result, err := s.SweepLifecycle(ctx, DefaultRetentionPolicy)
			require.NoError(t, err)
			require.Zero(t, result.ReceiptsPruned)
			for table, column := range map[string]string{"agents": "agent_id", "messages": "id", "conversation_send_receipts": "message_id"} {
				key := envelope.ID
				if table == "agents" {
					key = envelope.To
				}
				var count int
				require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE `+column+`=?`, key).Scan(&count))
				require.Equal(t, 1, count, table)
			}
			for _, body := range []string{envelope.Body, "changed body"} {
				retry := envelope
				retry.Body = body
				_, err = s.Enqueue(ctx, retry, EnqueueOptions{Conversation: &ConversationOptions{Body: &retry.Body}})
				var expired *ConversationReceiptExpiredUnresolvedError
				require.ErrorAs(t, err, &expired)
				require.Equal(t, "conversation_receipt_expired_unresolved", expired.Code())
				var count int
				require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE id=? AND message_incarnation_id=?`, envelope.ID, accepted.Message.MessageIncarnationID).Scan(&count))
				require.Equal(t, 1, count, "retry cannot enqueue another delivery")
			}
		})
	}
	t.Run("acked_resolved_prunes", func(t *testing.T) {
		s, clock, _ := lifecycleFixture(t)
		first, err := lifecycleSend(s, "acked-stale-id", "acked body")
		require.NoError(t, err)
		lifecycleACK(t, s, first.Message.ID)
		require.NoError(t, clock.Advance(receiptHorizon+time.Hour))
		result, err := s.SweepLifecycle(context.Background(), DefaultRetentionPolicy)
		require.NoError(t, err)
		require.Equal(t, 1, result.ReceiptsPruned)
		var count int
		require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM conversation_send_receipts WHERE message_id=?`, first.Message.ID).Scan(&count))
		require.Zero(t, count)
	})
}
