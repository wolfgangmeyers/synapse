package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnqueueConversationReceiptSurvivesDeliveryACKAndHistoryPrune(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	original := testEnvelope("post-ack-retry", "accepted body")
	body := original.Body
	options := EnqueueOptions{Conversation: &ConversationOptions{Body: &body}}
	first, err := s.Enqueue(ctx, original, options)
	require.NoError(t, err)
	require.True(t, first.Created)
	// Ordinary ACK deletes the delivery. History retention can remove the
	// conversation too, but neither operation revokes an accepted send ID.
	_, err = s.db.ExecContext(ctx, `DELETE FROM messages WHERE id=?`, original.ID)
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `DELETE FROM conversation_messages WHERE message_id=?`, original.ID)
	require.NoError(t, err)
	again, err := s.Enqueue(ctx, original, options)
	require.NoError(t, err)
	require.False(t, again.Created)
	require.False(t, again.ConversationCreated)
	require.Equal(t, first.Message, again.Message)
	require.Equal(t, first.ConversationMessage, again.ConversationMessage)
	for _, conflict := range []struct{ from, to, body string }{
		{original.From, original.To, "changed body"},
		{original.From, "other-recipient", original.Body},
		{"other-sender", original.To, original.Body},
	} {
		candidate := original
		candidate.From, candidate.To, candidate.Body = conflict.from, conflict.to, conflict.body
		candidateBody := candidate.Body
		_, err := s.Enqueue(ctx, candidate, EnqueueOptions{Conversation: &ConversationOptions{Body: &candidateBody}})
		require.ErrorAs(t, err, new(*IdempotencyConflictError))
	}
	var messages, conversations, receipts int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE id=?`, original.ID).Scan(&messages))
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM conversation_messages WHERE message_id=?`, original.ID).Scan(&conversations))
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM conversation_send_receipts WHERE message_id=?`, original.ID).Scan(&receipts))
	require.Zero(t, messages)
	require.Zero(t, conversations)
	require.Equal(t, 1, receipts)
}
