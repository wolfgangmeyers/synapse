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

func readyManagedDirectFixture(t *testing.T) (*Store, ManagedRuntimeFence) {
	t.Helper()
	durable, fence := readinessSourceFixture(t)
	policy := DefaultManagedReadinessPolicy()
	require.NoError(t, durable.ConfirmManagedPolicyApplied(context.Background(), fence, policy))
	snapshot := ManagedChildSnapshot{SnapshotID: "direct-snapshot", OwnerSessionID: "owner", Sequence: 0, Canonical: []byte(`{"owner_session_id":"owner","protocol":"mecha.children/v1","runs":[],"sequence":0,"snapshot_id":"direct-snapshot"}`), SHA256: testManifestHash}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(context.Background(), fence, snapshot))
	require.NoError(t, durable.MarkManagedReadyFromSources(context.Background(), fence))
	return durable, fence
}

func allocateTestDirectAttempt(t *testing.T, durable *Store, fence ManagedRuntimeFence, id string) *ManagedDirectAttempt {
	t.Helper()
	_, err := durable.Enqueue(context.Background(), protocol.Envelope{ID: id, From: "sender", To: fence.AgentID, Body: "body", CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	require.NoError(t, err)
	_, err = durable.ResolveMessageTarget(context.Background(), id)
	require.NoError(t, err)
	attempt, err := durable.AllocateManagedDirectAttempt(context.Background(), fence)
	require.NoError(t, err)
	require.NotNil(t, attempt)
	return attempt
}

func TestManagedDirectRetryBoundaries(t *testing.T) {
	require.Equal(t, time.Second, directRetryDelay(1))
	require.Equal(t, 5*time.Second, directRetryDelay(2))
	require.Equal(t, 30*time.Second, directRetryDelay(3))
	require.Equal(t, 120*time.Second, directRetryDelay(4))
	durable, fence := readyManagedDirectFixture(t)
	attempt := allocateTestDirectAttempt(t, durable, fence, "direct-retry")
	terminal, err := durable.ReleaseManagedDirect(context.Background(), *attempt, "prompt_failed", "")
	require.NoError(t, err)
	require.False(t, terminal)
	stored, err := durable.ManagedDirectAttempt(context.Background(), attempt.MessageID, 1)
	require.NoError(t, err)
	require.Equal(t, "released", stored.State)
	require.WithinDuration(t, time.Now().Add(time.Second), parseTimestamp(stored.NextAttemptAt), 2*time.Second)
}

func TestManagedDirectFifthAttemptTerminal(t *testing.T) {
	durable, fence := readyManagedDirectFixture(t)
	attempt := allocateTestDirectAttempt(t, durable, fence, "direct-fifth")
	for number := 1; number <= 5; number++ {
		terminal, err := durable.ReleaseManagedDirect(context.Background(), *attempt, "prompt_failed", "")
		require.NoError(t, err)
		require.Equal(t, number == 5, terminal)
		if number == 5 {
			break
		}
		require.NoError(t, func() error {
			_, err := durable.db.Exec(`UPDATE managed_direct_delivery_attempts SET next_attempt_at = ? WHERE message_id = ? AND attempt_number = ?`, formatTimestamp(time.Now().Add(-time.Second)), attempt.MessageID, number)
			return err
		}())
		attempt, err = durable.AllocateManagedDirectAttempt(context.Background(), fence)
		require.NoError(t, err)
		require.NotNil(t, attempt)
	}
	message, err := durable.MessageFor(context.Background(), "direct-fifth")
	require.NoError(t, err)
	require.Equal(t, "dead_letter", message.State)
}

func TestManagedDirectExpiryAndTmuxIsolation(t *testing.T) {
	durable, fence := readyManagedDirectFixture(t)
	attempt := allocateTestDirectAttempt(t, durable, fence, "direct-expiry")
	require.NoError(t, func() error {
		_, err := durable.db.Exec(`UPDATE messages SET expires_at = ? WHERE id = ?`, formatTimestamp(time.Now().Add(-time.Second)), attempt.MessageID)
		return err
	}())
	_, err := durable.Sweep(context.Background(), DefaultRetentionPolicy)
	require.NoError(t, err)
	message, err := durable.MessageFor(context.Background(), attempt.MessageID)
	require.NoError(t, err)
	require.Nil(t, message)
	stored, err := durable.ManagedDirectAttempt(context.Background(), attempt.MessageID, attempt.AttemptNumber)
	require.Error(t, err)
	require.Nil(t, stored)
}

func TestTargetSnapshotSelectsDurableTmuxOwner(t *testing.T) {
	ctx := context.Background()
	durable := openTestStore(t)
	owner, err := durable.ReserveTmuxRecipientSource(ctx, "agent-a", "tmux-source-v1:agent-a")
	require.NoError(t, err)
	require.Equal(t, "tmux-source-v1:agent-a", owner)
	require.NoError(t, durable.ActivateTmuxRecipientSource(ctx, "agent-a", owner))
	result, err := durable.Enqueue(ctx, testEnvelope("snapshot-message", "body"))
	require.NoError(t, err)
	target, err := durable.ResolveMessageTarget(ctx, result.Message.ID)
	require.NoError(t, err)
	require.Equal(t, "tmux", target.SourceKind)
	require.Equal(t, owner, target.OwnerIdentity)
	_, err = durable.AcquireAgentLease(ctx, LeaseRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a"})
	require.NoError(t, err)
	delivery, err := durable.ClaimNextMessage(ctx, ClaimRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a", SourceOwnerID: owner})
	require.NoError(t, err)
	require.Equal(t, "snapshot-message", delivery.Items[0].MessageID)
}

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
