package store

import (
	"context"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func reminderRequest(id string, interval *int64) ReminderRequest {
	return ReminderRequest{ID: id, Target: "agent-a", Creator: "creator", Body: "reminder body", CreatedAt: "2026-08-29T12:00:00.000Z", NextDueAt: "2026-08-29T12:00:00.000Z", IntervalMS: interval}
}

func TestCreateAndFireOneShotReminderQueuesAndLinksMessage(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	ids := deterministic.NewSequenceIDSource(testUUID("reminder-incarnation"))
	store := openTestStoreWithOptions(t, Options{Clock: clock, IDSource: ids})
	created, reminder, err := store.CreateReminder(context.Background(), reminderRequest("one-shot", nil))
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "scheduled", reminder.State)
	var attempts []ReminderFireAttempt
	outcomes, err := store.FireDueReminders(context.Background(), func(attempt ReminderFireAttempt) { attempts = append(attempts, attempt) })
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	require.Equal(t, []ReminderFireAttempt{{ReminderID: "one-shot", ScheduledFor: "2026-08-29T12:00:00.000Z"}}, attempts)
	require.Equal(t, "message_queued", outcomes[0].Reminder.State)
	require.Equal(t, "reminder-one-shot-0", outcomes[0].Message.ID)
	require.Equal(t, "reminder", outcomes[0].Message.Source.Kind)
	require.Equal(t, outcomes[0].Message.ID, *outcomes[0].Reminder.LinkedMessageID)
	var count int
	require.NoError(t, store.db.QueryRow("SELECT COUNT(*) FROM conversation_messages WHERE message_id = ?", outcomes[0].Message.ID).Scan(&count))
	require.Equal(t, 1, count)
}

func TestFireDueRemindersAdvancesRecurringRowsAndOrdersByDueTime(t *testing.T) {
	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	ids := deterministic.NewSequenceIDSource(testUUID("first"), testUUID("second"))
	store := openTestStoreWithOptions(t, Options{Clock: clock, IDSource: ids})
	interval := int64(time.Minute / time.Millisecond)
	first := reminderRequest("b-reminder", &interval)
	first.NextDueAt = "2026-08-29T12:00:02.000Z"
	second := reminderRequest("a-reminder", &interval)
	second.Body = "another reminder body"
	second.NextDueAt = "2026-08-29T12:00:01.000Z"
	_, _, err := store.CreateReminder(context.Background(), first)
	require.NoError(t, err)
	_, _, err = store.CreateReminder(context.Background(), second)
	require.NoError(t, err)
	_, err = store.AcquireAgentLease(context.Background(), LeaseRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a", LeaseDuration: time.Minute})
	require.NoError(t, err)
	require.NoError(t, clock.Advance(3*time.Second))
	var order []string
	outcomes, err := store.FireDueReminders(context.Background(), func(attempt ReminderFireAttempt) { order = append(order, attempt.ReminderID) })
	require.NoError(t, err)
	require.Len(t, outcomes, 2)
	require.Equal(t, []string{"a-reminder", "b-reminder"}, order)
	for _, outcome := range outcomes {
		require.Equal(t, "scheduled", outcome.Reminder.State)
		require.NotNil(t, outcome.Reminder.LinkedMessageID)
		require.Equal(t, int64(60000), *outcome.Reminder.IntervalMS)
		require.Equal(t, 1, outcome.Reminder.OccurrenceNumber)
	}
}

func TestCreateReminderIsIdempotentAndConflictsOnChangedEnvelope(t *testing.T) {
	store := openTestStore(t)
	request := reminderRequest("same", nil)
	created, _, err := store.CreateReminder(context.Background(), request)
	require.NoError(t, err)
	require.True(t, created)
	created, _, err = store.CreateReminder(context.Background(), request)
	require.NoError(t, err)
	require.False(t, created)
	request.Body = "changed"
	_, _, err = store.CreateReminder(context.Background(), request)
	var conflict *ReminderIdempotencyConflictError
	require.ErrorAs(t, err, &conflict)
}
