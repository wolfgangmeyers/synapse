package store

import (
	"context"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func TestRecurringDeadLetterRearmsWithoutHoldingQueryRows(t *testing.T) {
	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	ids := deterministic.NewSequenceIDSource(testUUID("0001"), testUUID("0002"), "delivery-batch")
	store := openTestStoreWithOptions(t, Options{Clock: clock, IDSource: ids})
	interval := int64(60_000)
	request := reminderRequest("rearm", &interval)
	_, _, err := store.CreateReminder(context.Background(), request)
	require.NoError(t, err)
	_, err = store.AcquireAgentLease(context.Background(), LeaseRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a", LeaseDuration: time.Minute})
	require.NoError(t, err)
	outcomes, err := store.FireDueReminders(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	_, err = store.ClaimNextMessage(context.Background(), ClaimRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a", DeliveryLeaseDuration: time.Minute})
	require.NoError(t, err)
	_, err = store.RecordInjectionRequested(context.Background(), ReceiptRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a", MessageID: "reminder-rearm-0", AttemptNumber: 1})
	require.NoError(t, err)
	failed, err := store.NackDelivery(context.Background(), ReceiptRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a", MessageID: "reminder-rearm-0", AttemptNumber: 1}, "client_error", 1)
	require.NoError(t, err)
	require.Equal(t, "dead_letter", failed.State)
	reminder, err := store.ReminderFor(context.Background(), "rearm")
	require.NoError(t, err)
	require.Equal(t, "scheduled", reminder.State)
	require.Nil(t, reminder.LinkedMessageID)
	require.Equal(t, 1, reminder.AttemptCount)
	require.Equal(t, []string{testUUID("0001"), testUUID("0002"), "delivery-batch"}, ids.Calls())
}

func TestRecurringMissedSlotsAdvanceToTheNextFutureSlot(t *testing.T) {
	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	store := openTestStoreWithOptions(t, Options{Clock: clock, IDSource: deterministic.NewSequenceIDSource(testUUID("0001"))})
	interval := int64(60_000)
	request := reminderRequest("missed", &interval)
	_, _, err := store.CreateReminder(context.Background(), request)
	require.NoError(t, err)
	_, err = store.AcquireAgentLease(context.Background(), LeaseRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a", LeaseDuration: time.Minute})
	require.NoError(t, err)
	require.NoError(t, clock.Advance(5*time.Minute))
	outcomes, err := store.FireDueReminders(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, outcomes)
	reminder, err := store.ReminderFor(context.Background(), "missed")
	require.NoError(t, err)
	require.Equal(t, "2026-08-29T12:06:00.000Z", reminder.NextDueAt)
	require.Equal(t, 0, reminder.OccurrenceNumber)
}

func TestReminderValidationMatchesNodeForSafeIntegerAndCancelID(t *testing.T) {
	store := openTestStore(t)
	interval := int64(9_007_199_254_740_992)
	request := reminderRequest("too-large", &interval)
	_, _, err := store.CreateReminder(context.Background(), request)
	var validation *ReminderValidationError
	require.ErrorAs(t, err, &validation)
	_, err = store.CancelReminder(context.Background(), "bad/id")
	require.ErrorAs(t, err, &validation)
}
