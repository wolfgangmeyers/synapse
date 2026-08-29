package store

import (
	"context"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func TestClaimReplaysAndAdoptsAnUnsettledBatchAfterLeaseReplacement(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	ids := deterministic.NewSequenceIDSource(testUUID("0001"), testUUID("0002"), "batch-1")
	store := openTestStoreWithOptions(t, Options{Clock: clock, IDSource: ids})
	_, err := store.Enqueue(context.Background(), testEnvelope("message-1", "body"))
	require.NoError(t, err)
	_, err = store.AcquireAgentLease(context.Background(), LeaseRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a"})
	require.NoError(t, err)
	first, err := store.ClaimNextMessage(context.Background(), ClaimRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a"})
	require.NoError(t, err)
	require.NoError(t, clock.Advance(31*time.Second))
	lease, err := store.AcquireAgentLease(context.Background(), LeaseRequest{AgentID: "agent-a", InstanceID: "instance-b", LeaseID: "lease-b"})
	require.NoError(t, err)
	require.True(t, lease.Recovered)
	replayed, err := store.ClaimNextMessage(context.Background(), ClaimRequest{AgentID: "agent-a", InstanceID: "instance-b", LeaseID: "lease-b"})
	require.NoError(t, err)
	require.Equal(t, first.Items, replayed.Items)
	require.Equal(t, first.Message, replayed.Message)
	stored, err := store.LeaseFor(context.Background(), "agent-a")
	require.NoError(t, err)
	require.Equal(t, "lease-b", stored.LeaseID)
}

func TestTimedOutDeliveryReturnsToQueueAndReachesDeadLetter(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	ids := deterministic.NewSequenceIDSource(testUUID("0001"), "batch-1", "batch-2", "batch-3")
	store := openTestStoreWithOptions(t, Options{Clock: clock, IDSource: ids})
	_, err := store.Enqueue(context.Background(), testEnvelope("message-1", "body"))
	require.NoError(t, err)
	_, err = store.AcquireAgentLease(context.Background(), LeaseRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a"})
	require.NoError(t, err)
	_, err = store.ClaimNextMessage(context.Background(), ClaimRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a"})
	require.NoError(t, err)
	require.NoError(t, clock.Advance(31*time.Second))
	outcomes, err := store.RecoverTimedOutDeliveries(context.Background(), 2)
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	require.Equal(t, "queued", outcomes[0].State)
	_, err = store.AcquireAgentLease(context.Background(), LeaseRequest{AgentID: "agent-a", InstanceID: "instance-b", LeaseID: "lease-b"})
	require.NoError(t, err)
	delivery, err := store.ClaimNextMessage(context.Background(), ClaimRequest{AgentID: "agent-a", InstanceID: "instance-b", LeaseID: "lease-b"})
	require.NoError(t, err)
	require.Equal(t, 2, delivery.Items[0].AttemptNumber)
	require.NoError(t, clock.Advance(31*time.Second))
	outcomes, err = store.RecoverTimedOutDeliveries(context.Background(), 2)
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	require.Equal(t, "dead_letter", outcomes[0].State)
	message, err := store.MessageFor(context.Background(), "message-1")
	require.NoError(t, err)
	require.Equal(t, "dead_letter", message.State)
}
