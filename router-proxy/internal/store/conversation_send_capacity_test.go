package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func TestConversationReceiptSurvivesACKSweepAndColdReopen(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	path := filepath.Join(t.TempDir(), "router.db")
	s, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	original := testEnvelope("durable-receipt", "body")
	body := original.Body
	options := EnqueueOptions{Conversation: &ConversationOptions{Body: &body}}
	first, err := s.Enqueue(ctx, original, options)
	require.NoError(t, err)
	require.True(t, first.Created)
	_, err = s.AcquireAgentLease(ctx, LeaseRequest{AgentID: original.To, InstanceID: "instance-a", LeaseID: "lease-a"})
	require.NoError(t, err)
	delivery, err := s.ClaimNextMessage(ctx, ClaimRequest{AgentID: original.To, InstanceID: "instance-a", LeaseID: "lease-a"})
	require.NoError(t, err)
	require.NotNil(t, delivery)
	receipt := ReceiptRequest{AgentID: original.To, InstanceID: "instance-a", LeaseID: "lease-a", MessageID: original.ID, AttemptNumber: delivery.AttemptNumber, DeliveryItems: delivery.Items}
	_, err = s.RecordInjectionRequested(ctx, receipt)
	require.NoError(t, err)
	_, err = s.AcknowledgeDelivery(ctx, receipt)
	require.NoError(t, err)
	require.NoError(t, clock.Advance(30*24*time.Hour-time.Millisecond))
	replayed, err := s.Enqueue(ctx, original, options)
	require.NoError(t, err)
	require.False(t, replayed.Created)
	require.Equal(t, first.Message, replayed.Message)
	require.Equal(t, first.ConversationMessage, replayed.ConversationMessage)
	changed := original
	changed.Body = "changed"
	changedOptions := EnqueueOptions{Conversation: &ConversationOptions{Body: &changed.Body}}
	_, err = s.Enqueue(ctx, changed, changedOptions)
	require.ErrorAs(t, err, new(*IdempotencyConflictError))

	require.NoError(t, clock.Advance(24*time.Hour+time.Millisecond))
	swept, err := s.SweepLifecycle(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	require.Equal(t, 1, swept.ConversationMessages)
	require.Equal(t, 1, swept.ReceiptsPruned)
	require.NoError(t, s.Close())
	s, err = Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer s.Close()
	for _, table := range []string{"messages", "conversation_messages", "conversation_send_receipts"} {
		var count int
		column := "message_id"
		if table == "messages" {
			column = "id"
		}
		require.NoError(t, s.db.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE "+column+"=?", original.ID).Scan(&count))
		require.Zero(t, count, table)
	}
	fresh, err := s.Enqueue(ctx, changed, changedOptions)
	require.NoError(t, err)
	require.True(t, fresh.Created, "resolved receipt can admit a new effect after its 30-day deadline")
	require.True(t, fresh.ConversationCreated)
	require.NotEqual(t, first.Message.MessageIncarnationID, fresh.Message.MessageIncarnationID)
	replayed, err = s.Enqueue(ctx, changed, changedOptions)
	require.NoError(t, err)
	require.False(t, replayed.Created)
	require.Equal(t, fresh.Message, replayed.Message)
	_, err = s.Enqueue(ctx, original, options)
	require.ErrorAs(t, err, new(*IdempotencyConflictError))
}

func TestConversationReceiptConcurrentAdmissionUsesOneDurableSlot(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	first, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer first.Close()
	second, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer second.Close()
	_, err = first.db.ExecContext(ctx, `UPDATE conversation_send_receipt_capacity SET max_count=1 WHERE singleton=1`)
	require.NoError(t, err)
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i, handle := range []*Store{first, second} {
		wg.Add(1)
		go func(i int, s *Store) {
			defer wg.Done()
			e := testEnvelope(fmt.Sprintf("concurrent-cap-%d", i), "body")
			body := e.Body
			_, err := s.Enqueue(ctx, e, EnqueueOptions{Conversation: &ConversationOptions{Body: &body}})
			results <- err
		}(i, handle)
	}
	wg.Wait()
	close(results)
	accepted, rejected := 0, 0
	for err := range results {
		if err == nil {
			accepted++
		} else {
			var capacity *ConversationReceiptCapacityError
			require.ErrorAs(t, err, &capacity)
			rejected++
		}
	}
	require.Equal(t, 1, accepted)
	require.Equal(t, 1, rejected)
	var used, receipts int
	require.NoError(t, first.db.QueryRowContext(ctx, `SELECT used_count FROM conversation_send_receipt_capacity WHERE singleton=1`).Scan(&used))
	require.NoError(t, first.db.QueryRowContext(ctx, `SELECT count(*) FROM conversation_send_receipts`).Scan(&receipts))
	require.Equal(t, 1, used)
	require.Equal(t, used, receipts)
}

func TestConversationReceiptAdmissionCapsPersistAcrossOpen(t *testing.T) {
	for _, boundary := range []string{"count", "bytes"} {
		t.Run(boundary, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "router.db")
			s, err := Open(ctx, path, Options{})
			require.NoError(t, err)
			send := func(id string) (EnqueueResult, error) {
				e := testEnvelope(id, "body-"+id)
				body := e.Body
				return s.Enqueue(ctx, e, EnqueueOptions{Conversation: &ConversationOptions{Body: &body}})
			}
			first, err := send("cap-first")
			require.NoError(t, err)
			var bytes int64
			require.NoError(t, s.db.QueryRowContext(ctx, `SELECT used_bytes FROM conversation_send_receipt_capacity WHERE singleton=1`).Scan(&bytes))
			switch boundary {
			case "count":
				_, err = s.db.ExecContext(ctx, `UPDATE conversation_send_receipt_capacity SET max_count=1 WHERE singleton=1`)
			case "bytes":
				_, err = s.db.ExecContext(ctx, `UPDATE conversation_send_receipt_capacity SET max_bytes=? WHERE singleton=1`, bytes)
			}
			require.NoError(t, err)
			require.NoError(t, s.Close())
			s, err = Open(ctx, path, Options{})
			require.NoError(t, err)
			defer s.Close()
			for i := 0; i < 2; i++ {
				rejected := fmt.Sprintf("cap-rejected-%d", i)
				_, err = send(rejected)
				var capacity *ConversationReceiptCapacityError
				require.ErrorAs(t, err, &capacity)
				require.Equal(t, "conversation_receipt_capacity", capacity.Code())
				for _, table := range []string{"messages", "conversation_messages", "conversation_send_receipts"} {
					var count int
					column := "message_id"
					if table == "messages" {
						column = "id"
					}
					require.NoError(t, s.db.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE "+column+"=?", rejected).Scan(&count))
					require.Zero(t, count)
				}
			}
			replay, err := send("cap-first")
			require.NoError(t, err)
			require.False(t, replay.Created)
			require.Equal(t, first.Message, replay.Message)
			e := testEnvelope("cap-first", "different")
			b := e.Body
			_, err = s.Enqueue(ctx, e, EnqueueOptions{Conversation: &ConversationOptions{Body: &b}})
			require.ErrorAs(t, err, new(*IdempotencyConflictError))
			var count int
			var afterBytes int64
			require.NoError(t, s.db.QueryRowContext(ctx, `SELECT used_count,used_bytes FROM conversation_send_receipt_capacity WHERE singleton=1`).Scan(&count, &afterBytes))
			require.Equal(t, 1, count)
			require.Equal(t, bytes, afterBytes)
		})
	}
}
