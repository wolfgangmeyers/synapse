package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// An alias owns its own replay window but never owns the shared delivery.
func TestReceiptLifecycleDedupAliasExpiresWithoutDeletingDelivery(t *testing.T) {
	s, clock, _ := lifecycleFixture(t)
	ctx := context.Background()
	a := testEnvelope("alias-delivery-a", "shared body")
	first, err := s.Enqueue(ctx, a, EnqueueOptions{Conversation: &ConversationOptions{Body: &a.Body}})
	require.NoError(t, err)
	require.NoError(t, clock.Advance(time.Millisecond))
	b := testEnvelope("alias-receipt-b", a.Body)
	alias, err := s.Enqueue(ctx, b, EnqueueOptions{Conversation: &ConversationOptions{Body: &b.Body}})
	require.NoError(t, err)
	require.True(t, alias.Deduped)
	require.Equal(t, first.Message, alias.Message)
	require.NotEqual(t, first.ConversationMessage.MessageIncarnationID, alias.ConversationMessage.MessageIncarnationID)
	replay, err := s.Enqueue(ctx, b, EnqueueOptions{Conversation: &ConversationOptions{Body: &b.Body}})
	require.NoError(t, err)
	require.Equal(t, alias.ConversationMessage, replay.ConversationMessage)
	changed := b
	changed.Body = "changed body"
	_, err = s.Enqueue(ctx, changed, EnqueueOptions{Conversation: &ConversationOptions{Body: &changed.Body}})
	require.ErrorAs(t, err, new(*IdempotencyConflictError))

	require.NoError(t, clock.Advance(receiptHorizon))
	count, err := s.PruneExpiredConversationReceipts(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, count)
	sweepReceiptAccounting(t, s, 0, 0)
	var deliveries, history int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM messages WHERE id=? AND message_incarnation_id=? AND state='queued'`, a.ID, first.Message.MessageIncarnationID).Scan(&deliveries))
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM conversation_messages WHERE message_id=?`, b.ID).Scan(&history))
	require.Equal(t, 1, deliveries)
	require.Equal(t, 1, history)

	// A late alias is a new acceptance; normal live-content deduplication may
	// point it at A again, but it receives a new receipt and history identity.
	fresh, err := s.Enqueue(ctx, b, EnqueueOptions{Conversation: &ConversationOptions{Body: &b.Body}})
	require.NoError(t, err)
	require.True(t, fresh.Deduped)
	require.True(t, fresh.ConversationCreated)
	require.Equal(t, first.Message, fresh.Message)
	require.NotEqual(t, alias.ConversationMessage.Sequence, fresh.ConversationMessage.Sequence)
	rows, bytes := sweepReceiptRows(t, s)
	sweepReceiptAccounting(t, s, rows, bytes)
	require.Equal(t, int64(1), rows)
}
