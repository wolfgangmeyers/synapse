package store

import (
	"context"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func expiredReceiptFixture(t *testing.T) (*Store, *deterministic.FixedClock, ReceiptRequest) {
	t.Helper()
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	ids := deterministic.NewSequenceIDSource(testUUID("0001"), "batch-1")
	store := openTestStoreWithOptions(t, Options{Clock: clock, IDSource: ids})
	_, err := store.Enqueue(context.Background(), testEnvelope("message-1", "body"))
	require.NoError(t, err)
	_, err = store.AcquireAgentLease(context.Background(), LeaseRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a", LeaseDuration: 30 * time.Second})
	require.NoError(t, err)
	delivery, err := store.ClaimNextMessage(context.Background(), ClaimRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a", DeliveryLeaseDuration: time.Second})
	require.NoError(t, err)
	return store, clock, ReceiptRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a", MessageID: "message-1", AttemptNumber: delivery.AttemptNumber, DeliveryItems: delivery.Items}
}

func assertTimedOutAttempt(t *testing.T, store *Store, wantMessageState string) {
	t.Helper()
	var attemptState, reason, messageState string
	require.NoError(t, store.db.QueryRow(`SELECT delivery_attempts.state, delivery_attempts.failure_reason, messages.state FROM delivery_attempts JOIN messages ON messages.id = delivery_attempts.message_id WHERE messages.id = 'message-1'`).Scan(&attemptState, &reason, &messageState))
	require.Equal(t, "timed_out", attemptState)
	require.Equal(t, "delivery_timeout", reason)
	require.Equal(t, wantMessageState, messageState)
}

func TestManagedDirectDiagnosticRetention(t *testing.T) {
	durable, fence := readinessSourceFixture(t)
	for index := 0; index < 201; index++ {
		require.NoError(t, durable.RecordManagedDirectDiagnostic(context.Background(), fence.AgentID, "", 0, "stale_receipt", ""))
	}
	var count int
	require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM managed_direct_delivery_diagnostics WHERE agent_id = ?`, fence.AgentID).Scan(&count))
	require.Equal(t, 200, count)
}

func TestRecordInjectionRequestedExpiresWithTimeoutTransitionBeforeError(t *testing.T) {
	store, clock, request := expiredReceiptFixture(t)
	require.NoError(t, clock.Advance(2*time.Second))
	_, err := store.RecordInjectionRequested(context.Background(), request)
	var inactive *DeliveryNotActiveError
	require.ErrorAs(t, err, &inactive)
	assertTimedOutAttempt(t, store, "queued")
}

func TestAcknowledgeDeliveryExpiresWithTimeoutTransitionBeforeError(t *testing.T) {
	store, clock, request := expiredReceiptFixture(t)
	_, err := store.RecordInjectionRequested(context.Background(), request)
	require.NoError(t, err)
	require.NoError(t, clock.Advance(2*time.Second))
	_, err = store.AcknowledgeDelivery(context.Background(), request)
	var inactive *DeliveryNotActiveError
	require.ErrorAs(t, err, &inactive)
	assertTimedOutAttempt(t, store, "queued")
}

func TestNackDeliveryExpiresAtRetryLimitAndDeadLettersBeforeError(t *testing.T) {
	store, clock, request := expiredReceiptFixture(t)
	_, err := store.RecordInjectionRequested(context.Background(), request)
	require.NoError(t, err)
	require.NoError(t, clock.Advance(2*time.Second))
	_, err = store.NackDelivery(context.Background(), request, "client_error", 1)
	var inactive *DeliveryNotActiveError
	require.ErrorAs(t, err, &inactive)
	assertTimedOutAttempt(t, store, "dead_letter")
	var attempts int
	require.NoError(t, store.db.QueryRow("SELECT COUNT(*) FROM delivery_attempts WHERE message_id = 'message-1' AND state = 'timed_out'").Scan(&attempts))
	require.Equal(t, 1, attempts)

	message, err := store.RetryDeadLetter(context.Background(), "message-1")
	require.NoError(t, err)
	require.Equal(t, "queued", message.State)
	var state string
	require.NoError(t, store.db.QueryRow("SELECT state FROM messages WHERE id = 'message-1'").Scan(&state))
	require.Equal(t, "queued", state)
}
