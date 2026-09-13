package store

import (
	"context"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/scheduler"
	"github.com/stretchr/testify/require"
)

// TestRecurringReminderReplacementCannotRearmSettledOccurrence exercises the
// replacement race through deterministic scheduler barriers and durable direct
// delivery transitions. An old recovery receipt must not rearm a new owner.
func TestRecurringReminderReplacementCannotRearmSettledOccurrence(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	durable := openTestStoreWithOptions(t, Options{Clock: clock})
	oldFence := readyManagedDirectAgent(t, durable, "replacement-old")
	replacement := readyManagedDirectAgent(t, durable, "replacement-new")
	interval := int64(time.Minute / time.Millisecond)
	created, reminder, err := durable.CreateReminder(ctx, ReminderRequest{
		ID: "replacement-race-functional", Target: oldFence.AgentID, Creator: "replacement-test", Body: "replacement body",
		CreatedAt: "2026-09-15T12:00:00.000Z", NextDueAt: "2026-09-15T12:00:00.000Z", IntervalMS: &interval,
	})
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "scheduled", reminder.State)

	sweepScheduler, err := scheduler.New(clock, func(ctx context.Context, _ time.Time) error {
		_, err := durable.FireDueReminders(ctx, nil)
		return err
	})
	require.NoError(t, err)
	ran, err := sweepScheduler.Run(ctx)
	require.NoError(t, err)
	require.True(t, ran)
	first, err := durable.ReminderFor(ctx, reminder.ID)
	require.NoError(t, err)
	require.NotNil(t, first.LinkedMessageID)
	firstMessageID := *first.LinkedMessageID
	_, err = durable.ResolveMessageTarget(ctx, firstMessageID)
	require.NoError(t, err)
	oldAttempt, err := durable.AllocateManagedDirectAttempt(ctx, oldFence)
	require.NoError(t, err)
	require.NoError(t, durable.MarkManagedDirectPrompted(ctx, *oldAttempt, "old-prompt"))

	require.NoError(t, execTestData(durable, `UPDATE recipient_sources SET owner_identity=? WHERE recipient_name=?`, replacement.AgentID, reminder.Target))
	require.NoError(t, durable.AcknowledgeManagedDirectRecovered(ctx, *oldAttempt, "old-recovered"))
	settledOld, err := durable.ManagedDirectAttempt(ctx, oldAttempt.MessageID, oldAttempt.AttemptNumber)
	require.NoError(t, err)
	require.Equal(t, "acknowledged", settledOld.State)
	unchanged, err := durable.ReminderFor(ctx, reminder.ID)
	require.NoError(t, err)
	require.Equal(t, firstMessageID, *unchanged.LinkedMessageID)
	require.Equal(t, 1, unchanged.OccurrenceNumber)

	// The old queued occurrence must be completed before the recurring store can
	// advance its durable incarnation; do that only as fixture setup, after the
	// stale-settlement assertion above.
	require.NoError(t, execTestData(durable, `UPDATE managed_direct_delivery_attempts SET state='direct_prompted' WHERE message_id=? AND attempt_number=?`, oldAttempt.MessageID, oldAttempt.AttemptNumber))
	require.NoError(t, execTestData(durable, `UPDATE recipient_sources SET owner_identity=? WHERE recipient_name=?`, oldFence.AgentID, reminder.Target))
	require.NoError(t, durable.AcknowledgeManagedDirectRecovered(ctx, *oldAttempt, "old-recovered-fixture"))
	require.NoError(t, execTestData(durable, `UPDATE recipient_sources SET owner_identity=? WHERE recipient_name=?`, replacement.AgentID, reminder.Target))
	require.NoError(t, clock.Advance(time.Minute))
	_, err = sweepScheduler.Run(ctx)
	require.NoError(t, err)
	second, err := durable.ReminderFor(ctx, reminder.ID)
	require.NoError(t, err)
	require.NotNil(t, second)
	require.NotNil(t, second.LinkedMessageID)
	secondMessageID := *second.LinkedMessageID
	require.NotEqual(t, firstMessageID, secondMessageID)
	_, err = durable.ResolveMessageTarget(ctx, secondMessageID)
	require.NoError(t, err)
	require.NoError(t, execTestData(durable, `UPDATE message_target_snapshots SET owner_identity=? WHERE message_id=?`, replacement.AgentID, secondMessageID))
	require.NoError(t, execTestData(durable, `UPDATE messages SET state='queued' WHERE id=?`, secondMessageID))
	replacementAttempt, err := durable.AllocateManagedDirectAttempt(ctx, replacement)
	require.NoError(t, err)
	require.NotNil(t, replacementAttempt)
	require.Equal(t, secondMessageID, replacementAttempt.MessageID)
	require.NoError(t, durable.MarkManagedDirectPrompted(ctx, *replacementAttempt, "replacement-prompt"))

	require.NoError(t, durable.AcknowledgeManagedDirectRecovered(ctx, *oldAttempt, "old-recovered-again"))
	stillCurrent, err := durable.ReminderFor(ctx, reminder.ID)
	require.NoError(t, err)
	require.Equal(t, secondMessageID, *stillCurrent.LinkedMessageID)
	storedReplacement, err := durable.ManagedDirectAttempt(ctx, replacementAttempt.MessageID, replacementAttempt.AttemptNumber)
	require.NoError(t, err)
	require.Equal(t, "direct_prompted", storedReplacement.State)

	require.NoError(t, durable.AcknowledgeManagedDirect(ctx, *replacementAttempt, "replacement-recovered", true))
	cancelled, err := durable.CancelReminder(ctx, reminder.ID)
	require.NoError(t, err)
	require.True(t, cancelled)
	gone, err := durable.ReminderFor(ctx, reminder.ID)
	require.NoError(t, err)
	require.Nil(t, gone)
	history, err := durable.ConversationHistory(ctx, "reminders", reminder.Target)
	require.NoError(t, err)
	require.Len(t, history, 2)
	require.Equal(t, firstMessageID, history[0].ID)
	require.Equal(t, secondMessageID, history[1].ID)

	require.NoError(t, clock.Advance(time.Minute))
	_, err = sweepScheduler.Run(ctx)
	require.NoError(t, err)
	restarted, err := scheduler.New(clock, func(ctx context.Context, _ time.Time) error {
		_, err := durable.FireDueReminders(ctx, nil)
		return err
	})
	require.NoError(t, err)
	require.NoError(t, restarted.Start(ctx))
	require.NoError(t, restarted.Stop())
	history, err = durable.ConversationHistory(ctx, "reminders", reminder.Target)
	require.NoError(t, err)
	require.Len(t, history, 2)
}
