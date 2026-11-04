package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A stale Pi receipt can arrive after a runtime transition. Model the persisted
// state at that boundary: an old prompted direct attempt and the same accepted
// message incarnation fenced by a later Claude write.
func TestClaudeHeldMailRejectsStaleManagedDirectSettlement(t *testing.T) {
	for _, recovered := range []bool{false, true} {
		name := "live"
		if recovered {
			name = "recovered"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s, fence, attempt := preparedInboundAcknowledgement(t, "agent", "held-"+name)
			_, err := s.db.ExecContext(ctx, `UPDATE messages SET claude_stdin_started=1 WHERE id=? AND message_incarnation_id=?`, attempt.MessageID, attempt.MessageIncarnationID)
			require.NoError(t, err)
			var changed bool
			if recovered {
				changed, _, err = s.AcknowledgeManagedDirectRecoveredResultWithHistory(ctx, *attempt, "stale-receipt")
			} else {
				changed, _, err = s.AcknowledgeManagedDirectResultWithHistory(ctx, *attempt, "stale-receipt", false)
			}
			require.ErrorIs(t, err, ErrManagedDirectClaudeInputHeld)
			require.False(t, changed)
			held, err := s.ClaudeMailHeld(ctx, attempt.MessageID)
			require.NoError(t, err)
			require.True(t, held)
			require.Empty(t, historyRowsFor(t, s, fence.AgentID))
			var state string
			require.NoError(t, s.db.QueryRowContext(ctx, `SELECT state FROM managed_direct_delivery_attempts WHERE message_id=? AND attempt_number=?`, attempt.MessageID, attempt.AttemptNumber).Scan(&state))
			require.Equal(t, "direct_prompted", state)
		})
	}
}

// A terminal stale release must not mutate the held message, direct attempt,
// or its recurring reminder, regardless of whether recovery owns the release.
func TestClaudeHeldMailRejectsManagedDirectRelease(t *testing.T) {
	for _, recovered := range []bool{false, true} {
		for _, held := range []bool{false, true} {
			t.Run(fmt.Sprintf("recovery=%t/held=%t", recovered, held), func(t *testing.T) {
				ctx := context.Background()
				s, _, attempt := preparedInboundAcknowledgement(t, "reminder", fmt.Sprintf("release-%t-%t", recovered, held))
				_, err := s.db.ExecContext(ctx, `UPDATE reminders SET interval_ms=60000,state='failed' WHERE linked_message_id=?`, attempt.MessageID)
				require.NoError(t, err)
				_, err = s.db.ExecContext(ctx, `UPDATE managed_direct_delivery_attempts SET attempt_number=5 WHERE message_id=?`, attempt.MessageID)
				require.NoError(t, err)
				attempt.AttemptNumber = 5
				if held {
					_, err = s.db.ExecContext(ctx, `UPDATE messages SET claude_stdin_started=1 WHERE id=? AND message_incarnation_id=?`, attempt.MessageID, attempt.MessageIncarnationID)
					require.NoError(t, err)
				}
				var terminal bool
				if recovered {
					terminal, err = s.ReleaseManagedDirectRecovery(ctx, *attempt, "stale")
				} else {
					terminal, err = s.ReleaseManagedDirect(ctx, *attempt, "stale", "failure")
				}
				if held {
					require.ErrorIs(t, err, ErrManagedDirectClaudeInputHeld)
				} else {
					require.NoError(t, err)
				}
				require.Equal(t, !held, terminal)
				var messageState, attemptState, reminderState string
				var linked *string
				require.NoError(t, s.db.QueryRowContext(ctx, `SELECT state FROM messages WHERE id=?`, attempt.MessageID).Scan(&messageState))
				require.NoError(t, s.db.QueryRowContext(ctx, `SELECT state FROM managed_direct_delivery_attempts WHERE message_id=? AND attempt_number=5`, attempt.MessageID).Scan(&attemptState))
				require.NoError(t, s.db.QueryRowContext(ctx, `SELECT state,linked_message_id FROM reminders WHERE id=?`, fmt.Sprintf("release-%t-%t", recovered, held)).Scan(&reminderState, &linked))
				if held {
					require.Equal(t, "queued", messageState)
					require.Equal(t, "direct_prompted", attemptState)
					require.Equal(t, "failed", reminderState)
					require.NotNil(t, linked)
					require.Equal(t, attempt.MessageID, *linked)
					isHeld, err := s.ClaudeMailHeld(ctx, attempt.MessageID)
					require.NoError(t, err)
					require.True(t, isHeld)
				} else {
					require.Equal(t, "dead_letter", messageState)
					require.Equal(t, "failed", attemptState)
					require.Equal(t, "scheduled", reminderState)
					require.Nil(t, linked)
				}
			})
		}
	}
}

func TestStaleDirectDeadLetterDoesNotTouchReplacementIncarnation(t *testing.T) {
	ctx := context.Background()
	s, _, attempt := preparedInboundAcknowledgement(t, "reminder", "replacement-incarnation")
	_, err := s.db.ExecContext(ctx, `UPDATE reminders SET interval_ms=60000,state='failed' WHERE linked_message_id=?`, attempt.MessageID)
	require.NoError(t, err)
	stale := *attempt
	stale.MessageIncarnationID = "different-incarnation"
	require.NoError(t, s.runImmediate(ctx, func() error { return s.deadLetterDirectAttemptTx(ctx, stale, formatTimestamp(s.clock.Now())) }))
	message, err := s.MessageFor(ctx, attempt.MessageID)
	require.NoError(t, err)
	require.Equal(t, "queued", message.State)
	var state string
	var linked string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT state,linked_message_id FROM reminders WHERE id='replacement-incarnation'`).Scan(&state, &linked))
	require.Equal(t, "failed", state)
	require.Equal(t, attempt.MessageID, linked)
}

// The old attempt retains its real incarnation while the persisted message is
// replaced. Settlement may close the old attempt but cannot touch the new row.
func TestStaleDirectReleasePreservesActualReplacementIncarnation(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		t.Run(fmt.Sprintf("recovery=%t", recovery), func(t *testing.T) {
			ctx := context.Background()
			s, _, attempt := preparedInboundAcknowledgement(t, "reminder", fmt.Sprintf("actual-replacement-%t", recovery))
			_, err := s.db.ExecContext(ctx, `UPDATE reminders SET interval_ms=60000,state='failed' WHERE linked_message_id=?`, attempt.MessageID)
			require.NoError(t, err)
			_, err = s.db.ExecContext(ctx, `UPDATE managed_direct_delivery_attempts SET attempt_number=5 WHERE message_id=?`, attempt.MessageID)
			require.NoError(t, err)
			attempt.AttemptNumber = 5
			replacement := "123e4567-e89b-42d3-a456-426614174999"
			require.NotEqual(t, attempt.MessageIncarnationID, replacement)
			_, err = s.db.ExecContext(ctx, `UPDATE messages SET message_incarnation_id=? WHERE id=?`, replacement, attempt.MessageID)
			require.NoError(t, err)
			if recovery {
				_, err = s.ReleaseManagedDirectRecovery(ctx, *attempt, "stale")
			} else {
				_, err = s.ReleaseManagedDirect(ctx, *attempt, "stale", "")
			}
			require.NoError(t, err)
			var incarnation, state, reminderState, linked string
			require.NoError(t, s.db.QueryRowContext(ctx, `SELECT message_incarnation_id,state FROM messages WHERE id=?`, attempt.MessageID).Scan(&incarnation, &state))
			require.Equal(t, replacement, incarnation)
			require.Equal(t, "queued", state)
			require.NoError(t, s.db.QueryRowContext(ctx, `SELECT state,linked_message_id FROM reminders WHERE id=?`, fmt.Sprintf("actual-replacement-%t", recovery)).Scan(&reminderState, &linked))
			require.Equal(t, "failed", reminderState)
			require.Equal(t, attempt.MessageID, linked)
		})
	}
}

// Expiry ends this occurrence without ACK; recurrence schedules a new occurrence,
// never reuses the uncertain message identity.
func TestClaudeHeldRecurringReminderExpiryDoesNotReplayOccurrence(t *testing.T) {
	ctx := context.Background()
	s, _, attempt := preparedInboundAcknowledgement(t, "reminder", "held-expiry")
	_, err := s.db.ExecContext(ctx, `UPDATE reminders SET interval_ms=60000 WHERE id='held-expiry'`)
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `UPDATE messages SET claude_stdin_started=1, expires_at=? WHERE id=?`, formatTimestamp(s.clock.Now().Add(-time.Second)), attempt.MessageID)
	require.NoError(t, err)
	_, err = s.Sweep(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	var state string
	var linked *string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT state,linked_message_id FROM reminders WHERE id='held-expiry'`).Scan(&state, &linked))
	require.Equal(t, "scheduled", state)
	require.Nil(t, linked)
	message, err := s.MessageFor(ctx, attempt.MessageID)
	require.NoError(t, err)
	require.Nil(t, message)
	outcomes, err := s.FireDueReminders(ctx, nil)
	require.NoError(t, err)
	for _, outcome := range outcomes {
		if outcome.Message != nil {
			require.NotEqual(t, attempt.MessageID, outcome.Message.ID)
		}
	}
}
