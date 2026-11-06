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
	require.ErrorContains(t, err, "managed tool has no unused Execute admission")
	require.True(t, producer.domainSettled)
	require.True(t, producer.domainConclusive)
	require.False(t, producer.domainCommitted)
	var n int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM messages WHERE id='producer-failed'`).Scan(&n))
	require.Zero(t, n)
}

func TestConversationReceiptProtectedRetryIgnoresUnrelatedCleanupFailure(t *testing.T) {
	s, clock, _ := lifecycleFixture(t)
	ctx := context.Background()
	_, err := lifecycleSend(s, "expired-other", "old")
	require.NoError(t, err)
	require.NoError(t, clock.Advance(15*time.Minute))
	protected, err := lifecycleSend(s, "protected", "protected body")
	require.NoError(t, err)
	require.NoError(t, clock.Advance(15*time.Minute))
	_, err = s.db.Exec(`CREATE TEMP TRIGGER fail_other_cleanup BEFORE DELETE ON main.conversation_send_receipts WHEN OLD.message_id='expired-other' BEGIN SELECT RAISE(ABORT,'cleanup unavailable'); END`)
	require.NoError(t, err)
	count, bytes := lifecycleUsage(t, s)
	rateReservations := 0
	e := testEnvelope("protected", "protected body")
	opts := EnqueueOptions{Conversation: &ConversationOptions{Body: &e.Body}, OnFreshAdmission: func() (func(bool), error) {
		rateReservations++
		return func(bool) {}, nil
	}}
	replay, err := s.Enqueue(ctx, e, opts)
	require.NoError(t, err)
	require.False(t, replay.Created)
	require.Equal(t, protected.ConversationMessage, replay.ConversationMessage)
	changed := e
	changed.Body = "different"
	opts.Conversation = &ConversationOptions{Body: &changed.Body}
	_, err = s.Enqueue(ctx, changed, opts)
	require.ErrorAs(t, err, new(*IdempotencyConflictError))
	require.Zero(t, rateReservations, "protected replay and conflict must not reserve a fresh-send rate token")
	// The preflight hint cannot bypass the managed producer guard.
	witness := &EmbeddedToolInvocation{}
	_, err = s.enqueue(ctx, e, &EnqueueOptions{Conversation: &ConversationOptions{Body: &e.Body}}, witness)
	require.ErrorContains(t, err, "managed tool has no unused Execute admission")
	require.True(t, witness.domainSettled)
	require.False(t, witness.domainCommitted)
	sweepReceiptAccounting(t, s, count, bytes)
	_, err = lifecycleSend(s, "fresh-failed", "fresh")
	require.ErrorContains(t, err, "cleanup unavailable")
	for table, column := range map[string]string{"messages": "id", "conversation_messages": "message_id", "conversation_send_receipts": "message_id"} {
		var n int
		require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE `+column+`='fresh-failed'`).Scan(&n))
		require.Zero(t, n, table)
	}
	sweepReceiptAccounting(t, s, count, bytes)
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
