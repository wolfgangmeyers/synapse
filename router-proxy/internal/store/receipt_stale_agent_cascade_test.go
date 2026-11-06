package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Lifecycle cleanup must not cascade-delete a stale recipient's live delivery.
func TestConversationReceiptStaleAgentQueuedDeliverySurvivesReceiptExpiry(t *testing.T) {
	for _, disconnected := range []bool{false, true} {
		t.Run(map[bool]string{false: "never_connected", true: "disconnected"}[disconnected], func(t *testing.T) {
			ctx := context.Background()
			s, clock, _ := lifecycleFixture(t)
			envelope := testEnvelope("stale-recipient-id", "queued body")
			envelope.To = "stale-recipient"
			accepted, err := s.Enqueue(ctx, envelope, EnqueueOptions{Conversation: &ConversationOptions{Body: &envelope.Body}})
			require.NoError(t, err)
			if disconnected {
				_, err = s.db.ExecContext(ctx, `UPDATE agents SET last_connected_at=first_seen_at, disconnected_at=first_seen_at WHERE agent_id=?`, envelope.To)
				require.NoError(t, err)
			}
			require.NoError(t, clock.Advance(receiptHorizon+time.Hour))
			result, err := s.SweepLifecycle(ctx, DefaultRetentionPolicy)
			require.NoError(t, err)
			require.Equal(t, 1, result.ReceiptsPruned)
			for table, column := range map[string]string{"agents": "agent_id", "messages": "id", "conversation_messages": "message_id"} {
				key := envelope.ID
				if table == "agents" {
					key = envelope.To
				}
				var count int
				require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE `+column+`=?`, key).Scan(&count))
				require.Equal(t, 1, count, table)
			}
			retry, err := s.Enqueue(ctx, envelope, EnqueueOptions{Conversation: &ConversationOptions{Body: &envelope.Body}})
			require.NoError(t, err)
			require.False(t, retry.Created)
			require.Equal(t, accepted.Message.MessageIncarnationID, retry.Message.MessageIncarnationID)
		})
	}
}
