package store

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/stretchr/testify/require"
)

func preparedInboundAcknowledgement(t *testing.T, sourceKind, id string) (*Store, ManagedRuntimeFence, *ManagedDirectAttempt) {
	t.Helper()
	ctx := context.Background()
	durable, fence := readyManagedDirectFixture(t)
	if sourceKind == "reminder" {
		now := formatTimestamp(time.Now().UTC().Add(-time.Second))
		created, _, err := durable.CreateReminder(ctx, ReminderRequest{ID: id, Target: fence.AgentID, Creator: "sender", Body: "private reminder body", CreatedAt: now, NextDueAt: now})
		require.NoError(t, err)
		require.True(t, created)
		outcomes, err := durable.FireDueReminders(ctx, nil)
		require.NoError(t, err)
		require.Len(t, outcomes, 1)
		require.NotNil(t, outcomes[0].Message)
		_, err = durable.ResolveMessageTarget(ctx, outcomes[0].Message.ID)
		require.NoError(t, err)
	} else {
		source := protocol.MessageSource{Kind: "agent"}
		_, err := durable.Enqueue(ctx, protocol.Envelope{ID: id, From: "sender", To: fence.AgentID, Body: "private message body", CreatedAt: "2026-09-15T08:00:00.000Z", Source: &source})
		require.NoError(t, err)
		_, err = durable.ResolveMessageTarget(ctx, id)
		require.NoError(t, err)
	}
	attempt, err := durable.AllocateManagedDirectAttempt(ctx, fence)
	require.NoError(t, err)
	require.NotNil(t, attempt)
	require.NoError(t, durable.MarkManagedDirectPrompted(ctx, *attempt, "cursor-1"))
	return durable, fence, attempt
}

func historyRowsFor(t *testing.T, durable *Store, agentID string) []ManagedHistoryEntry {
	t.Helper()
	page, err := durable.ManagedHistoryPage(context.Background(), agentID, "complete", 0, maxManagedHistoryEntries)
	require.NoError(t, err)
	return page.Entries
}

func TestInboundCompleteAcknowledgementIsAtomicIdempotentAndBodyFree(t *testing.T) {
	ctx := context.Background()
	for _, sourceKind := range []string{"agent", "reminder"} {
		t.Run(sourceKind, func(t *testing.T) {
			durable, fence, attempt := preparedInboundAcknowledgement(t, sourceKind, "ack-"+sourceKind)
			changed, entry, err := durable.AcknowledgeManagedDirectResultWithHistory(ctx, *attempt, "receipt-1", true)
			require.NoError(t, err)
			require.True(t, changed)
			require.Equal(t, fence.AgentID, entry.AgentID)
			require.Equal(t, "info", entry.Level)
			require.NotContains(t, entry.Message, "private")
			require.NotContains(t, fmt.Sprintf("%+v", entry), "body")

			rows := historyRowsFor(t, durable, fence.AgentID)
			require.Len(t, rows, 1)
			require.Equal(t, entry, rows[0])
			var messages, reminders int
			require.NoError(t, durable.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages WHERE id=?", attempt.MessageID).Scan(&messages))
			require.NoError(t, durable.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM reminders WHERE linked_message_id=? OR id=?", attempt.MessageID, "ack-"+sourceKind).Scan(&reminders))
			require.Zero(t, messages)
			if sourceKind == "reminder" {
				require.Zero(t, reminders)
			}

			changed, err = durable.AcknowledgeManagedDirectResult(ctx, *attempt, "receipt-2", true)
			require.NoError(t, err)
			require.False(t, changed)
			require.Len(t, historyRowsFor(t, durable, fence.AgentID), 1)
		})
	}
}

func TestInboundCompleteRecoveredReminderIsAtomicAndIdempotent(t *testing.T) {
	ctx := context.Background()
	durable, fence, attempt := preparedInboundAcknowledgement(t, "reminder", "recovered-reminder")
	changed, entry, err := durable.AcknowledgeManagedDirectRecoveredResultWithHistory(ctx, *attempt, "recovery-receipt")
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "reminder", entry.Presentation.Kind)
	require.Len(t, historyRowsFor(t, durable, fence.AgentID), 1)
	changed, err = durable.AcknowledgeManagedDirectRecoveredResult(ctx, *attempt, "recovery-receipt")
	require.NoError(t, err)
	require.False(t, changed)
	require.Len(t, historyRowsFor(t, durable, fence.AgentID), 1)
}

func TestInboundCompleteRejectsStaleIncarnationAndAttemptAndSeparatesIncarnations(t *testing.T) {
	ctx := context.Background()
	durable, fence, first := preparedInboundAcknowledgement(t, "agent", "same-message-id")
	changed, err := durable.AcknowledgeManagedDirectResult(ctx, *first, "first-receipt", true)
	require.NoError(t, err)
	require.True(t, changed)

	// Reusing the message ID after settlement creates a new durable incarnation.
	source := protocol.MessageSource{Kind: "agent"}
	_, err = durable.Enqueue(ctx, protocol.Envelope{ID: first.MessageID, From: "sender", To: fence.AgentID, Body: "replacement", CreatedAt: "2026-09-15T08:00:01.000Z", Source: &source})
	require.NoError(t, err)
	_, err = durable.ResolveMessageTarget(ctx, first.MessageID)
	require.NoError(t, err)
	second, err := durable.AllocateManagedDirectAttempt(ctx, fence)
	require.NoError(t, err)
	require.NotNil(t, second)
	require.NotEqual(t, first.MessageIncarnationID, second.MessageIncarnationID)
	require.NoError(t, durable.MarkManagedDirectPrompted(ctx, *second, "cursor-2"))

	stale := *first
	changed, err = durable.AcknowledgeManagedDirectResult(ctx, stale, "stale-receipt", true)
	require.False(t, changed)
	// The old incarnation is fenced out. It may be reported as an already
	// disposed route, but it never changes the replacement row.
	wrongAttempt := *second
	wrongAttempt.AttemptNumber++
	changed, err = durable.AcknowledgeManagedDirectResult(ctx, wrongAttempt, "wrong-attempt", true)
	require.Error(t, err)
	require.False(t, changed)
	changed, err = durable.AcknowledgeManagedDirectResult(ctx, *second, "second-receipt", true)
	require.NoError(t, err)
	require.True(t, changed)

	var keys []string
	rows, err := durable.db.QueryContext(ctx, "SELECT event_key FROM managed_complete_history WHERE category='managed_message' ORDER BY sequence")
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var key string
		require.NoError(t, rows.Scan(&key))
		keys = append(keys, key)
	}
	require.NoError(t, rows.Err())
	require.Len(t, keys, 2)
	require.NotEqual(t, keys[0], keys[1])
}

func TestInboundCompleteConcurrentDuplicateHasOneTransitionAndHistoryRow(t *testing.T) {
	ctx := context.Background()
	durable, fence, attempt := preparedInboundAcknowledgement(t, "agent", "concurrent-ack")
	results := make(chan bool, 2)
	errs := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			changed, err := durable.AcknowledgeManagedDirectResult(ctx, *attempt, "same-receipt", true)
			results <- changed
			errs <- err
		}()
	}
	group.Wait()
	close(results)
	close(errs)
	changedCount := 0
	for changed := range results {
		if changed {
			changedCount++
		}
	}
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, 1, changedCount)
	require.Len(t, historyRowsFor(t, durable, fence.AgentID), 1)
}

func TestInboundCompleteUsesPersistedMetadataAndFailsClosedOnEventConflict(t *testing.T) {
	ctx := context.Background()
	durable, fence, attempt := preparedInboundAcknowledgement(t, "agent", "persisted-metadata")
	attempt.Message.To = "wrong-agent"
	attempt.Message.Body = "secret token=wrong"
	attempt.Message.Source.Kind = "reminder"
	changed, entry, err := durable.AcknowledgeManagedDirectResultWithHistory(ctx, *attempt, "receipt", true)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, fence.AgentID, entry.AgentID)
	require.Equal(t, "message", entry.Presentation.Kind)
	require.NotContains(t, entry.Message, "secret")
	var category string
	require.NoError(t, durable.db.QueryRowContext(ctx, "SELECT category FROM managed_complete_history WHERE sequence=(SELECT MAX(sequence) FROM managed_complete_history)").Scan(&category))
	require.Equal(t, "managed_message", category)

	durable, fence, attempt = preparedInboundAcknowledgement(t, "agent", "conflicting-event")
	key := managedDirectAcknowledgementEventKey(fence.AgentID, attempt.MessageID, attempt.MessageIncarnationID, attempt.AttemptNumber)
	_, err = durable.db.ExecContext(ctx, `INSERT INTO managed_complete_history(agent_id,category,level,message,occurred_at,event_key,presentation_json) VALUES (?,?,?,?,?,?,?)`, fence.AgentID, "reminder", "info", "Reminder acknowledged", formatTimestamp(time.Now().UTC()), key, `{"kind":"reminder","outcome":"success","summary":"Reminder completed"}`)
	require.NoError(t, err)
	changed, err = durable.AcknowledgeManagedDirectResult(ctx, *attempt, "receipt", true)
	require.Error(t, err)
	require.False(t, changed)
	stored, err := durable.ManagedDirectAttempt(ctx, attempt.MessageID, attempt.AttemptNumber)
	require.NoError(t, err)
	require.Equal(t, "direct_prompted", stored.State)
	var messages int
	require.NoError(t, durable.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages WHERE id=?", attempt.MessageID).Scan(&messages))
	require.Equal(t, 1, messages)
}

func TestInboundCompleteHistoryFailuresRollBackMessageAndReminderSettlement(t *testing.T) {
	ctx := context.Background()
	for _, sourceKind := range []string{"agent", "reminder"} {
		t.Run(sourceKind, func(t *testing.T) {
			durable, fence, attempt := preparedInboundAcknowledgement(t, sourceKind, "insert-failure-"+sourceKind)
			_, err := durable.db.ExecContext(ctx, `CREATE TRIGGER fail_complete_insert BEFORE INSERT ON managed_complete_history BEGIN SELECT RAISE(ABORT, 'injected complete insert failure'); END`)
			require.NoError(t, err)
			changed, err := durable.AcknowledgeManagedDirectResult(ctx, *attempt, "receipt", true)
			require.ErrorContains(t, err, "injected complete insert failure")
			require.False(t, changed)
			stored, err := durable.ManagedDirectAttempt(ctx, attempt.MessageID, attempt.AttemptNumber)
			require.NoError(t, err)
			require.Equal(t, "direct_prompted", stored.State)
			var messages, reminders, history int
			require.NoError(t, durable.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages WHERE id=?", attempt.MessageID).Scan(&messages))
			require.NoError(t, durable.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM reminders WHERE id=?", "insert-failure-"+sourceKind).Scan(&reminders))
			require.NoError(t, durable.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM managed_complete_history WHERE agent_id=?", fence.AgentID).Scan(&history))
			require.Equal(t, 1, messages)
			if sourceKind == "reminder" {
				require.Equal(t, 1, reminders)
			}
			require.Zero(t, history)
		})
	}

	durable, fence, attempt := preparedInboundAcknowledgement(t, "agent", "prune-failure")
	for i := 0; i < maxManagedHistoryEntries; i++ {
		_, err := durable.AppendManagedCompleteHistory(ctx, fence.AgentID, "seed", "info", fmt.Sprintf("seed-%03d", i))
		require.NoError(t, err)
	}
	_, err := durable.db.ExecContext(ctx, `CREATE TRIGGER fail_complete_prune BEFORE DELETE ON managed_complete_history BEGIN SELECT RAISE(ABORT, 'injected complete prune failure'); END`)
	require.NoError(t, err)
	changed, err := durable.AcknowledgeManagedDirectResult(ctx, *attempt, "receipt", true)
	require.ErrorContains(t, err, "injected complete prune failure")
	require.False(t, changed)
	stored, err := durable.ManagedDirectAttempt(ctx, attempt.MessageID, attempt.AttemptNumber)
	require.NoError(t, err)
	require.Equal(t, "direct_prompted", stored.State)
	var count int
	require.NoError(t, durable.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM managed_complete_history WHERE agent_id=?", fence.AgentID).Scan(&count))
	require.Equal(t, maxManagedHistoryEntries, count)
}

func TestCompleteHistoryGenericPruneFailureRollsBackInsert(t *testing.T) {
	ctx := context.Background()
	durable := openTestStore(t)
	for i := 0; i < maxManagedHistoryEntries; i++ {
		_, err := durable.AppendManagedCompleteHistory(ctx, "history-agent", "seed", "info", fmt.Sprintf("seed-%03d", i))
		require.NoError(t, err)
	}
	_, err := durable.db.ExecContext(ctx, `CREATE TRIGGER fail_generic_prune BEFORE DELETE ON managed_complete_history BEGIN SELECT RAISE(ABORT, 'injected generic prune failure'); END`)
	require.NoError(t, err)
	_, err = durable.AppendManagedCompleteHistory(ctx, "history-agent", "seed", "info", "new-row")
	require.ErrorContains(t, err, "injected generic prune failure")
	var count int
	require.NoError(t, durable.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM managed_complete_history").Scan(&count))
	require.Equal(t, maxManagedHistoryEntries, count)
}
