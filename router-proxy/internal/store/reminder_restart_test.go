package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/scheduler"
	"github.com/stretchr/testify/require"
)

func TestRecurringReminderRearmsAcrossStoreAndSchedulerRestart(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	path := filepath.Join(t.TempDir(), "router.db")
	store, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	fence := readyManagedDirectAgent(t, store, "agent-a")
	interval := int64(time.Minute / time.Millisecond)
	request := reminderRequest("restart-rearm", &interval)
	_, _, err = store.CreateReminder(ctx, request)
	require.NoError(t, err)

	var firstOutcomes []ReminderFireOutcome
	firstScheduler, err := scheduler.New(clock, func(ctx context.Context, _ time.Time) error {
		var sweepErr error
		firstOutcomes, sweepErr = store.FireDueReminders(ctx, nil)
		return sweepErr
	})
	require.NoError(t, err)
	// scheduler start/stop is part of the lifecycle; the callback runs its
	// startup sweep synchronously.
	require.NoError(t, firstScheduler.Start(ctx))
	require.NoError(t, firstScheduler.Stop())
	require.Len(t, firstOutcomes, 1)
	require.Equal(t, "reminder-restart-rearm-0", firstOutcomes[0].Message.ID)
	require.Equal(t, "scheduled", firstOutcomes[0].Reminder.State)
	require.Equal(t, 1, firstOutcomes[0].Reminder.OccurrenceNumber)
	require.Equal(t, "2026-08-29T12:01:00.000Z", firstOutcomes[0].Reminder.NextDueAt)
	require.Equal(t, firstOutcomes[0].Message.ID, *firstOutcomes[0].Reminder.LinkedMessageID)
	require.NoError(t, func() error { _, err := store.ResolveMessageTarget(ctx, firstOutcomes[0].Message.ID); return err }())

	firstAttempt, err := store.AllocateManagedDirectAttempt(ctx, fence)
	require.NoError(t, err)
	require.NotNil(t, firstAttempt)
	require.NoError(t, store.MarkManagedDirectPrompted(ctx, *firstAttempt, "restart-first-prompt"))
	require.NoError(t, store.AcknowledgeManagedDirect(ctx, *firstAttempt, "restart-first-receipt", true))
	settled, err := store.ReminderFor(ctx, request.ID)
	require.NoError(t, err)
	require.Equal(t, "scheduled", settled.State)
	require.Nil(t, settled.LinkedMessageID)
	require.Equal(t, 1, settled.OccurrenceNumber)
	require.Equal(t, "2026-08-29T12:01:00.000Z", settled.NextDueAt)
	assertReminderCounts(t, store, 0, 1, 1)
	require.NoError(t, store.Close())

	require.NoError(t, clock.Advance(time.Minute))
	store, err = Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	var secondOutcomes []ReminderFireOutcome
	secondScheduler, err := scheduler.New(clock, func(ctx context.Context, _ time.Time) error {
		var sweepErr error
		secondOutcomes, sweepErr = store.FireDueReminders(ctx, nil)
		return sweepErr
	})
	require.NoError(t, err)
	require.NoError(t, secondScheduler.Start(ctx))
	require.NoError(t, secondScheduler.Stop())
	require.Len(t, secondOutcomes, 1)
	require.Equal(t, "reminder-restart-rearm-1", secondOutcomes[0].Message.ID)
	require.Equal(t, "scheduled", secondOutcomes[0].Reminder.State)
	require.Equal(t, 2, secondOutcomes[0].Reminder.OccurrenceNumber)
	require.Equal(t, "2026-08-29T12:02:00.000Z", secondOutcomes[0].Reminder.NextDueAt)
	require.Equal(t, secondOutcomes[0].Message.ID, *secondOutcomes[0].Reminder.LinkedMessageID)
	assertReminderCounts(t, store, 1, 2, 1)
	var linkedID string
	require.NoError(t, store.db.QueryRow(`SELECT linked_message_id FROM reminders WHERE id=?`, request.ID).Scan(&linkedID))
	require.Equal(t, secondOutcomes[0].Message.ID, linkedID)
}

func TestCancelWinsAgainstDueSweepInEitherOrdering(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name         string
		cancelDuring bool
	}{
		{name: "cancel-before-sweep"},
		{name: "cancel-after-selection", cancelDuring: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			clock := deterministic.NewFixedClock(start)
			store := openTestStoreWithOptions(t, Options{Clock: clock})
			_ = readyManagedDirectAgent(t, store, "agent-a")
			interval := int64(time.Minute / time.Millisecond)
			request := reminderRequest("cancel-order-"+test.name, &interval)
			_, _, err := store.CreateReminder(ctx, request)
			require.NoError(t, err)

			if !test.cancelDuring {
				canceled, err := store.CancelReminder(ctx, request.ID)
				require.NoError(t, err)
				require.True(t, canceled)
			}
			var attempts []ReminderFireAttempt
			var beforeCalls int
			outcomes, err := store.FireDueRemindersWithBefore(ctx, func(attempt ReminderFireAttempt) {
				attempts = append(attempts, attempt)
			}, func(attempt ReminderFireAttempt) {
				beforeCalls++
				if !test.cancelDuring {
					return
				}
				require.Equal(t, request.ID, attempt.ReminderID)
				canceled, cancelErr := store.CancelReminder(ctx, request.ID)
				require.NoError(t, cancelErr)
				require.True(t, canceled)
			})
			require.NoError(t, err)
			require.Empty(t, outcomes)
			require.Empty(t, attempts)
			if test.cancelDuring {
				require.Equal(t, 1, beforeCalls)
			}
			reminder, err := store.ReminderFor(ctx, request.ID)
			require.NoError(t, err)
			require.Nil(t, reminder)
			assertReminderCounts(t, store, 0, 0, 0)
		})
	}
}

func TestReplacementFenceCannotSettleOldAttemptOrCurrentRecurringOccurrence(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name                    string
		replaceBeforeSettlement bool
	}{
		{name: "replacement-before-old-settlement", replaceBeforeSettlement: true},
		{name: "old-settlement-before-replacement"},
	} {
		t.Run(test.name, func(t *testing.T) {
			clock := deterministic.NewFixedClock(start)
			store := openTestStoreWithOptions(t, Options{Clock: clock})
			oldFence := readyManagedDirectAgent(t, store, "agent-a")
			replacement := readyManagedDirectAgent(t, store, "replacement-agent")
			interval := int64(time.Minute / time.Millisecond)
			request := reminderRequest("replacement-order-"+test.name, &interval)
			_, _, err := store.CreateReminder(ctx, request)
			require.NoError(t, err)
			outcomes, err := store.FireDueReminders(ctx, nil)
			require.NoError(t, err)
			require.Len(t, outcomes, 1)
			require.NoError(t, func() error { _, err := store.ResolveMessageTarget(ctx, outcomes[0].Message.ID); return err }())
			oldAttempt, err := store.AllocateManagedDirectAttempt(ctx, oldFence)
			require.NoError(t, err)
			require.NotNil(t, oldAttempt)
			require.NoError(t, store.MarkManagedDirectPrompted(ctx, *oldAttempt, "old-prompt"))

			replaceRecipient := func() {
				_, err := store.db.Exec(`UPDATE recipient_sources SET owner_identity=? WHERE recipient_name=?`, replacement.AgentID, request.Target)
				require.NoError(t, err)
			}
			if test.replaceBeforeSettlement {
				replaceRecipient()
				// A replacement cannot use the old recovery receipt to clear the
				// linked occurrence or rearm it.
				require.NoError(t, store.AcknowledgeManagedDirectRecovered(ctx, *oldAttempt, "stale-receipt"))
				storedOld, err := store.ManagedDirectAttempt(ctx, oldAttempt.MessageID, oldAttempt.AttemptNumber)
				require.NoError(t, err)
				require.Equal(t, "acknowledged", storedOld.State)
				unchanged, err := store.ReminderFor(ctx, request.ID)
				require.NoError(t, err)
				require.NotNil(t, unchanged.LinkedMessageID)
				require.Equal(t, outcomes[0].Message.ID, *unchanged.LinkedMessageID)
				message, err := store.MessageFor(ctx, outcomes[0].Message.ID)
				require.NoError(t, err)
				require.NotNil(t, message)
				require.Equal(t, "queued", message.State)
				return
			} else {
				// The old runtime may settle its own occurrence before replacement.
				require.NoError(t, store.AcknowledgeManagedDirectRecovered(ctx, *oldAttempt, "old-receipt"))
				replaceRecipient()
			}

			rearmed, err := store.ReminderFor(ctx, request.ID)
			require.NoError(t, err)
			require.Equal(t, "scheduled", rearmed.State)
			require.Nil(t, rearmed.LinkedMessageID)
			require.Equal(t, 1, rearmed.OccurrenceNumber)
			require.Equal(t, "2026-08-29T12:01:00.000Z", rearmed.NextDueAt)
			require.NoError(t, clock.Advance(time.Minute))
			currentOutcomes, err := store.FireDueReminders(ctx, nil)
			require.NoError(t, err)
			require.Len(t, currentOutcomes, 1)
			current := currentOutcomes[0]
			require.Equal(t, "reminder-"+request.ID+"-1", current.Message.ID)
			require.NoError(t, func() error { _, err := store.ResolveMessageTarget(ctx, current.Message.ID); return err }())
			require.Equal(t, 2, current.Reminder.OccurrenceNumber)
			require.Equal(t, "2026-08-29T12:02:00.000Z", current.Reminder.NextDueAt)
			require.Equal(t, current.Message.ID, *current.Reminder.LinkedMessageID)

			currentAttempt, err := store.AllocateManagedDirectAttempt(ctx, replacement)
			require.NoError(t, err)
			require.NotNil(t, currentAttempt)
			require.Equal(t, current.Message.ID, currentAttempt.MessageID)
			require.NoError(t, store.MarkManagedDirectPrompted(ctx, *currentAttempt, "current-prompt"))
			// Both old settlement paths are harmless after the replacement. They
			// must not touch the current linked message or its direct attempt.
			require.NoError(t, store.AcknowledgeManagedDirect(ctx, *oldAttempt, "late-full-fence", true))
			require.NoError(t, store.AcknowledgeManagedDirectRecovered(ctx, *oldAttempt, "late-recovered"))
			stillCurrent, err := store.ReminderFor(ctx, request.ID)
			require.NoError(t, err)
			require.Equal(t, "scheduled", stillCurrent.State)
			require.Equal(t, current.Message.ID, *stillCurrent.LinkedMessageID)
			require.Equal(t, 2, stillCurrent.OccurrenceNumber)
			message, err := store.MessageFor(ctx, current.Message.ID)
			require.NoError(t, err)
			require.NotNil(t, message)
			require.Equal(t, "queued", message.State)
			storedCurrent, err := store.ManagedDirectAttempt(ctx, currentAttempt.MessageID, currentAttempt.AttemptNumber)
			require.NoError(t, err)
			require.Equal(t, "direct_prompted", storedCurrent.State)
			wantMessages := 2
			if !test.replaceBeforeSettlement {
				wantMessages = 1
			}
			assertReminderCounts(t, store, wantMessages, 2, 1)
		})
	}
}

func assertReminderCounts(t *testing.T, store *Store, messages, conversationMessages, reminders int) {
	t.Helper()
	var count int
	require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&count))
	require.Equal(t, messages, count)
	require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM conversation_messages`).Scan(&count))
	require.Equal(t, conversationMessages, count)
	require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM reminders`).Scan(&count))
	require.Equal(t, reminders, count)
}
