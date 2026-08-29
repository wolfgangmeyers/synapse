package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/stretchr/testify/require"
)

func testEnvelope(id, body string) protocol.Envelope {
	return protocol.Envelope{ID: id, From: "sender", To: "agent-a", Body: body, CreatedAt: "2026-08-29T12:00:00.000Z"}
}
func testUUID(n string) string { return fmt.Sprintf("123e4567-e89b-42d3-a456-42661417%s", n) }

func TestEnqueueIsIdempotentAndSuppressesUnchangedLiveDuplicates(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	ids := deterministic.NewSequenceIDSource(testUUID("0001"), testUUID("0002"))
	store := openTestStoreWithOptions(t, Options{Clock: clock, IDSource: ids})
	first, err := store.Enqueue(context.Background(), testEnvelope("message-1", "same"))
	require.NoError(t, err)
	require.True(t, first.Created)
	sameID, err := store.Enqueue(context.Background(), testEnvelope("message-1", "same"))
	require.NoError(t, err)
	require.False(t, sameID.Created)
	require.False(t, sameID.Deduped)
	require.Equal(t, first.Message.ID, sameID.Message.ID)
	duplicate, err := store.Enqueue(context.Background(), testEnvelope("message-2", "same"))
	require.NoError(t, err)
	require.False(t, duplicate.Created)
	require.True(t, duplicate.Deduped)
	require.Equal(t, "message-1", duplicate.Message.ID)
	changed, err := store.Enqueue(context.Background(), testEnvelope("message-3", "changed"))
	require.NoError(t, err)
	require.True(t, changed.Created)
	_, err = store.Enqueue(context.Background(), protocol.Envelope{ID: "message-1", From: "sender", To: "agent-a", Body: "changed", CreatedAt: "2026-08-29T12:00:00.000Z"})
	var conflict *IdempotencyConflictError
	require.ErrorAs(t, err, &conflict)
}

func TestClaimBatchesRowsAndAckSettlesEveryItem(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	ids := deterministic.NewSequenceIDSource(testUUID("0001"), testUUID("0002"), "batch-1")
	store := openTestStoreWithOptions(t, Options{Clock: clock, IDSource: ids})
	for i, body := range []string{"one", "two"} {
		_, err := store.Enqueue(context.Background(), testEnvelope("message-"+string(rune('1'+i)), body))
		require.NoError(t, err)
	}
	lease, err := store.AcquireAgentLease(context.Background(), LeaseRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a"})
	require.NoError(t, err)
	require.Equal(t, "2026-08-29T12:00:30.000Z", lease.ExpiresAt)
	delivery, err := store.ClaimNextMessage(context.Background(), ClaimRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a"})
	require.NoError(t, err)
	require.Len(t, delivery.Items, 2)
	require.Contains(t, delivery.Message.Body, "Mailbox message batch")
	require.Equal(t, "message-1", delivery.Items[0].MessageID)
	require.Equal(t, "message-2", delivery.Items[1].MessageID)
	require.Equal(t, delivery.Items[0].AttemptNumber, delivery.AttemptNumber)
	receipt := ReceiptRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a", MessageID: "message-1", AttemptNumber: delivery.AttemptNumber, DeliveryItems: delivery.Items}
	_, err = store.RecordInjectionRequested(context.Background(), receipt)
	require.NoError(t, err)
	_, err = store.AcknowledgeDelivery(context.Background(), receipt)
	require.NoError(t, err)
	for _, id := range []string{"message-1", "message-2"} {
		message, err := store.MessageFor(context.Background(), id)
		require.NoError(t, err)
		require.Nil(t, message)
	}
	_, err = store.ClaimNextMessage(context.Background(), ClaimRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a"})
	require.NoError(t, err)
}

func TestNackDeadLettersAndRetryRestoresTheMessage(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	ids := deterministic.NewSequenceIDSource(testUUID("0001"), "batch-1", "batch-2")
	store := openTestStoreWithOptions(t, Options{Clock: clock, IDSource: ids})
	_, err := store.Enqueue(context.Background(), testEnvelope("message-1", "body"))
	require.NoError(t, err)
	_, err = store.AcquireAgentLease(context.Background(), LeaseRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a"})
	require.NoError(t, err)
	delivery, err := store.ClaimNextMessage(context.Background(), ClaimRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a"})
	require.NoError(t, err)
	receipt := ReceiptRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a", MessageID: "message-1", AttemptNumber: 1, DeliveryItems: delivery.Items}
	require.NoError(t, func() error { _, err := store.RecordInjectionRequested(context.Background(), receipt); return err }())
	failed, err := store.NackDelivery(context.Background(), receipt, "client_error", 1)
	require.NoError(t, err)
	require.Equal(t, "dead_letter", failed.State)
	message, err := store.MessageFor(context.Background(), "message-1")
	require.NoError(t, err)
	require.Equal(t, "dead_letter", message.State)
	message, err = store.RetryDeadLetter(context.Background(), "message-1")
	require.NoError(t, err)
	require.Equal(t, "queued", message.State)
	delivery, err = store.ClaimNextMessage(context.Background(), ClaimRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a"})
	require.NoError(t, err)
	require.Equal(t, 1, delivery.Items[0].AttemptNumber)
}

func TestClaimStopsAtSentinelBoundary(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	ids := deterministic.NewSequenceIDSource(testUUID("0001"), testUUID("0002"), testUUID("0003"), "batch-1", "batch-2", "batch-3")
	store := openTestStoreWithOptions(t, Options{Clock: clock, IDSource: ids})
	_, err := store.Enqueue(context.Background(), testEnvelope("normal-1", "one"))
	require.NoError(t, err)
	sentinel := testEnvelope("sentinel-1", "signal")
	sentinel.Sentinel = &protocol.SentinelDescriptor{Model: "model"}
	_, err = store.Enqueue(context.Background(), sentinel)
	require.NoError(t, err)
	_, err = store.Enqueue(context.Background(), testEnvelope("normal-2", "two"))
	require.NoError(t, err)
	_, err = store.AcquireAgentLease(context.Background(), LeaseRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a"})
	require.NoError(t, err)
	first, err := store.ClaimNextMessage(context.Background(), ClaimRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a"})
	require.NoError(t, err)
	require.Len(t, first.Items, 1)
	require.Equal(t, "normal-1", first.Items[0].MessageID)
	receipt := ReceiptRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a", MessageID: "normal-1", AttemptNumber: 1, DeliveryItems: first.Items}
	_, err = store.RecordInjectionRequested(context.Background(), receipt)
	require.NoError(t, err)
	_, err = store.AcknowledgeDelivery(context.Background(), receipt)
	require.NoError(t, err)
	second, err := store.ClaimNextMessage(context.Background(), ClaimRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a"})
	require.NoError(t, err)
	require.Len(t, second.Items, 1)
	require.Equal(t, "sentinel-1", second.Items[0].MessageID)
}
