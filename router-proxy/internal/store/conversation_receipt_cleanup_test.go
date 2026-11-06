package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestConversationReceiptCleanupDeadlineScheduling(t *testing.T) {
	s, clock, _ := lifecycleFixture(t)
	ctx := context.Background()
	delay, err := s.ConversationReceiptCleanupDelay(ctx, time.Second)
	require.NoError(t, err)
	require.Equal(t, time.Second, delay)
	_, err = lifecycleSend(s, "scheduled", "body")
	require.NoError(t, err)
	require.NoError(t, clock.Advance(receiptHorizon-700*time.Millisecond))
	delay, err = s.ConversationReceiptCleanupDelay(ctx, time.Second)
	require.NoError(t, err)
	require.Equal(t, 700*time.Millisecond, delay)
	require.NoError(t, clock.Advance(700*time.Millisecond))
	delay, err = s.ConversationReceiptCleanupDelay(ctx, time.Second)
	require.NoError(t, err)
	require.Zero(t, delay)
	n, err := s.PruneExpiredConversationReceipts(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
}

func TestConversationReceiptCleanupFailureSettlesManagedProducer(t *testing.T) {
	s, clock, _ := lifecycleFixture(t)
	ctx := context.Background()
	_, err := lifecycleSend(s, "old-for-producer", "old")
	require.NoError(t, err)
	require.NoError(t, clock.Advance(receiptHorizon))
	_, err = s.db.Exec(`CREATE TEMP TRIGGER fail_producer_cleanup BEFORE DELETE ON main.conversation_send_receipts BEGIN SELECT RAISE(ABORT,'cleanup unavailable'); END`)
	require.NoError(t, err)
	producer := &EmbeddedToolInvocation{}
	e := testEnvelope("producer-failed", "new")
	_, err = s.enqueue(ctx, e, &EnqueueOptions{Conversation: &ConversationOptions{Body: &e.Body}}, producer)
	require.ErrorContains(t, err, "cleanup unavailable")
	require.True(t, producer.domainSettled)
	require.True(t, producer.domainConclusive)
	require.False(t, producer.domainCommitted)
	var n int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM messages WHERE id='producer-failed'`).Scan(&n))
	require.Zero(t, n)
}

func TestConversationReceiptCleanupRollsBackFailedAccounting(t *testing.T) {
	s, clock, _ := lifecycleFixture(t)
	ctx := context.Background()
	message, err := lifecycleSend(s, "prune-fault", "é payload")
	require.NoError(t, err)
	count, bytes := lifecycleUsage(t, s)
	require.NoError(t, clock.Advance(receiptHorizon))
	_, err = s.db.Exec(`CREATE TEMP TRIGGER fail_receipt_accounting BEFORE UPDATE ON main.conversation_send_receipt_capacity BEGIN SELECT RAISE(ABORT,'injected accounting failure'); END`)
	require.NoError(t, err)
	n, err := s.PruneExpiredConversationReceipts(ctx)
	require.ErrorContains(t, err, "injected accounting failure")
	require.Zero(t, n)
	sweepReceiptAccounting(t, s, count, bytes)
	var deliveries int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM messages WHERE id=?`, message.Message.ID).Scan(&deliveries))
	require.Equal(t, 1, deliveries)
	_, err = s.db.Exec(`DROP TRIGGER fail_receipt_accounting`)
	require.NoError(t, err)
	n, err = s.PruneExpiredConversationReceipts(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	sweepReceiptAccounting(t, s, 0, 0)
}
