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

func TestRecurringDeadLetterRearmsOnClaudeFailureSettlement(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	s, b, _ := embeddedMailFixture(t)
	s.clock = clock
	_, err := s.db.Exec(`UPDATE managed_agents SET accepting_prompts=1,slot_held=1 WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)

	interval := int64(60_000)
	req := ReminderRequest{
		ID:         "rec-claude-fail",
		Target:     b.AgentID,
		Creator:    "sender",
		Body:       "recurring ping",
		CreatedAt:  formatTimestamp(start),
		NextDueAt:  formatTimestamp(start),
		IntervalMS: &interval,
	}
	created, reminder, err := s.CreateReminder(ctx, req)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "scheduled", reminder.State)

	// Fire the reminder to generate occurrence 0 message
	outcomes, err := s.FireDueReminders(ctx, nil)
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	require.NotNil(t, outcomes[0].Message)
	msgID := outcomes[0].Message.ID

	// Verify reminder is linked to this message
	reminder, err = s.ReminderFor(ctx, "rec-claude-fail")
	require.NoError(t, err)
	require.Equal(t, "scheduled", reminder.State)
	require.NotNil(t, reminder.LinkedMessageID)
	require.Equal(t, msgID, *reminder.LinkedMessageID)

	// Target snapshot is needed for embedded reservation
	_, err = s.ResolveMessageTarget(ctx, msgID)
	require.NoError(t, err)

	attempt, err := s.ReserveEmbeddedMail(ctx, b, msgID)
	require.NoError(t, err)
	require.NotNil(t, attempt)

	// Advance clock slightly before failure settlement
	require.NoError(t, clock.Advance(10*time.Second))

	// Claude worker crashes or fails settlement
	_, err = s.SettleClaudeUnsubmittedMail(ctx, b, attempt)
	require.NoError(t, err)

	// Verify message transitioned to dead_letter
	msg, err := s.MessageFor(ctx, msgID)
	require.NoError(t, err)
	require.Equal(t, "dead_letter", string(msg.State))

	// Verify linked recurring reminder is re-armed
	reminder, err = s.ReminderFor(ctx, "rec-claude-fail")
	require.NoError(t, err)
	require.Equal(t, "scheduled", reminder.State)
	require.Nil(t, reminder.LinkedMessageID)
	require.Equal(t, 1, reminder.AttemptCount)
	// next_due_at should be advanced to next cycle (start + 60s = 12:01:00)
	require.Equal(t, "2026-08-29T12:01:00.000Z", reminder.NextDueAt)
}

func TestRecurringFireSelfHealsDeadLetterLinkedMessage(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	ids := deterministic.NewSequenceIDSource(testUUID("0001"), testUUID("0002"), "batch-1", "batch-2")
	store := openTestStoreWithOptions(t, Options{Clock: clock, IDSource: ids})
	fence := readyManagedDirectAgent(t, store, "agent-a")

	interval := int64(60_000)
	req := ReminderRequest{
		ID:         "self-heal-rec",
		Target:     fence.AgentID,
		Creator:    "sender",
		Body:       "self heal body",
		CreatedAt:  formatTimestamp(start),
		NextDueAt:  formatTimestamp(start),
		IntervalMS: &interval,
	}
	created, reminder, err := store.CreateReminder(ctx, req)
	require.NoError(t, err)
	require.True(t, created)

	// First tick: fires occurrence 0
	outcomes, err := store.FireDueReminders(ctx, nil)
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	firstMsgID := outcomes[0].Message.ID

	reminder, err = store.ReminderFor(ctx, "self-heal-rec")
	require.NoError(t, err)
	require.Equal(t, firstMsgID, *reminder.LinkedMessageID)

	// Simulate dead-lettering the message WITHOUT unlinking the reminder (e.g. direct dead-letter or missing)
	_, err = store.db.ExecContext(ctx, "UPDATE messages SET state='dead_letter' WHERE id=?", firstMsgID)
	require.NoError(t, err)

	// Advance clock to next due time
	require.NoError(t, clock.Advance(time.Minute))

	// Reminder should show linked_message_state as dead_letter
	reminder, err = store.ReminderFor(ctx, "self-heal-rec")
	require.NoError(t, err)
	require.NotNil(t, reminder.LinkedMessageID)
	require.NotNil(t, reminder.LinkedMessageState)
	require.Equal(t, "dead_letter", *reminder.LinkedMessageState)

	// Second tick: FireDueReminders should self-heal (unlink dead-letter message and fire fresh reminder message)
	outcomes, err = store.FireDueReminders(ctx, nil)
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	secondMsgID := outcomes[0].Message.ID
	require.NotEqual(t, firstMsgID, secondMsgID)
	require.Equal(t, "reminder-self-heal-rec-1", secondMsgID)

	// Reminder state should reflect the new message and advanced due time
	reminder, err = store.ReminderFor(ctx, "self-heal-rec")
	require.NoError(t, err)
	require.Equal(t, "scheduled", reminder.State)
	require.Equal(t, secondMsgID, *reminder.LinkedMessageID)
	require.Equal(t, 2, reminder.OccurrenceNumber)
	require.Equal(t, "2026-08-29T12:02:00.000Z", reminder.NextDueAt)

	// Also test subcase: linked message is completely deleted/missing from messages table
	_, err = store.db.ExecContext(ctx, "DELETE FROM messages WHERE id=?", secondMsgID)
	require.NoError(t, err)

	// Advance clock again
	require.NoError(t, clock.Advance(time.Minute))

	reminder, err = store.ReminderFor(ctx, "self-heal-rec")
	require.NoError(t, err)
	require.NotNil(t, reminder.LinkedMessageID)
	require.Nil(t, reminder.LinkedMessageState) // message is missing so linked state is nil

	// Third tick: FireDueReminders should self-heal missing message link as well
	outcomes, err = store.FireDueReminders(ctx, nil)
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	thirdMsgID := outcomes[0].Message.ID
	require.Equal(t, "reminder-self-heal-rec-2", thirdMsgID)

	reminder, err = store.ReminderFor(ctx, "self-heal-rec")
	require.NoError(t, err)
	require.Equal(t, "scheduled", reminder.State)
	require.Equal(t, thirdMsgID, *reminder.LinkedMessageID)
	require.Equal(t, 3, reminder.OccurrenceNumber)
	require.Equal(t, "2026-08-29T12:03:00.000Z", reminder.NextDueAt)
}

func TestRecurringFireInFlightMessageAdvancesWithoutDuplicate(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	store := openTestStoreWithOptions(t, Options{Clock: clock})
	fence := readyManagedDirectAgent(t, store, "agent-a")

	interval := int64(60_000)
	req := ReminderRequest{
		ID:         "inflight-rec",
		Target:     fence.AgentID,
		Creator:    "sender",
		Body:       "inflight body",
		CreatedAt:  formatTimestamp(start),
		NextDueAt:  formatTimestamp(start),
		IntervalMS: &interval,
	}
	created, reminder, err := store.CreateReminder(ctx, req)
	require.NoError(t, err)
	require.True(t, created)

	// Tick 1: fires occurrence 0
	outcomes, err := store.FireDueReminders(ctx, nil)
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	firstMsgID := outcomes[0].Message.ID

	reminder, err = store.ReminderFor(ctx, "inflight-rec")
	require.NoError(t, err)
	require.Equal(t, firstMsgID, *reminder.LinkedMessageID)
	require.Equal(t, 1, reminder.OccurrenceNumber)
	require.Equal(t, "2026-08-29T12:01:00.000Z", reminder.NextDueAt)

	// Message is still queued (in-flight). Advance clock to next due time
	require.NoError(t, clock.Advance(time.Minute))

	// Tick 2: Since message is still queued (in-flight), no duplicate message should be fired
	outcomes, err = store.FireDueReminders(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, outcomes)

	// next_due_at is advanced to next cycle (12:02:00), linked_message_id remains unchanged
	reminder, err = store.ReminderFor(ctx, "inflight-rec")
	require.NoError(t, err)
	require.Equal(t, "scheduled", reminder.State)
	require.Equal(t, firstMsgID, *reminder.LinkedMessageID)
	require.Equal(t, 1, reminder.OccurrenceNumber)
	require.Equal(t, "2026-08-29T12:02:00.000Z", reminder.NextDueAt)

	// Change state of message to leased (still active in-flight)
	_, err = store.db.ExecContext(ctx, "UPDATE messages SET state='leased' WHERE id=?", firstMsgID)
	require.NoError(t, err)

	// Advance clock again
	require.NoError(t, clock.Advance(time.Minute))

	// Tick 3: With message leased, still no duplicate message should be fired
	outcomes, err = store.FireDueReminders(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, outcomes)

	reminder, err = store.ReminderFor(ctx, "inflight-rec")
	require.NoError(t, err)
	require.Equal(t, "scheduled", reminder.State)
	require.Equal(t, firstMsgID, *reminder.LinkedMessageID)
	require.Equal(t, 1, reminder.OccurrenceNumber)
	require.Equal(t, "2026-08-29T12:03:00.000Z", reminder.NextDueAt)
}

func TestRecurringDismissalRearmsLinkedReminder(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	store := openTestStoreWithOptions(t, Options{Clock: clock})
	fence := readyManagedDirectAgent(t, store, "agent-a")

	interval := int64(60_000)
	req := ReminderRequest{
		ID:         "dismiss-rec",
		Target:     fence.AgentID,
		Creator:    "sender",
		Body:       "dismiss body",
		CreatedAt:  formatTimestamp(start),
		NextDueAt:  formatTimestamp(start),
		IntervalMS: &interval,
	}
	created, reminder, err := store.CreateReminder(ctx, req)
	require.NoError(t, err)
	require.True(t, created)

	// Fire occurrence 0
	outcomes, err := store.FireDueReminders(ctx, nil)
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	msgID := outcomes[0].Message.ID

	reminder, err = store.ReminderFor(ctx, "dismiss-rec")
	require.NoError(t, err)
	require.Equal(t, msgID, *reminder.LinkedMessageID)

	// Advance clock 15 seconds
	require.NoError(t, clock.Advance(15*time.Second))

	// User dismisses the message
	err = store.DismissMailboxMessage(ctx, msgID)
	require.NoError(t, err)

	// Verify message transitioned to dead_letter
	msg, err := store.MessageFor(ctx, msgID)
	require.NoError(t, err)
	require.Equal(t, "dead_letter", string(msg.State))

	// Verify linked recurring reminder is re-armed
	reminder, err = store.ReminderFor(ctx, "dismiss-rec")
	require.NoError(t, err)
	require.Equal(t, "scheduled", reminder.State)
	require.Nil(t, reminder.LinkedMessageID)
	require.Equal(t, 1, reminder.AttemptCount)
	require.Equal(t, "2026-08-29T12:01:00.000Z", reminder.NextDueAt)
}
