package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedOwnReminderLinkedUpdateRetainsOccurrence(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "router.db")
	s, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	t.Cleanup(func() {
		if !s.closed {
			require.NoError(t, s.Close())
		}
	})
	root := redBind(t, s, "linked-root", "root-session", 1, "root-generation", "root")
	worker := redBind(t, s, "linked-worker", "worker-session", 1, "worker-generation", "worker")
	for _, b := range []EmbeddedBinding{root, worker} {
		_, err = s.db.Exec(`INSERT INTO recipient_sources(recipient_name,source_kind,owner_identity,state,created_at,updated_at) VALUES(?,'managed',?,'active',?,?)`, b.AgentID, b.AgentID, formatTimestamp(clock.Now()), formatTimestamp(clock.Now()))
		require.NoError(t, err)
		_, err = s.db.Exec(`UPDATE managed_agents SET accepting_prompts=1,slot_held=1 WHERE agent_id=?`, b.AgentID)
		require.NoError(t, err)
	}
	due := formatTimestamp(clock.Now().Add(20 * time.Second))
	oneArgs := []byte(fmt.Sprintf(`{"action":"create","body":"one old","due_at":%q}`, due))
	recurArgs := []byte(fmt.Sprintf(`{"action":"create","body":"recurring old","due_at":%q,"interval_ms":60000}`, due))
	one, _, err := s.EmbeddedOwnReminderCall(ctx, root, "one-create", oneArgs)
	require.NoError(t, err)
	recur, _, err := s.EmbeddedOwnReminderCall(ctx, worker, "recurring-create", recurArgs)
	require.NoError(t, err)
	require.NoError(t, clock.Advance(20*time.Second))
	outcomes, err := s.FireDueReminders(ctx, nil)
	require.NoError(t, err)
	require.Len(t, outcomes, 2)
	byID := map[string]ReminderFireOutcome{}
	for _, outcome := range outcomes {
		byID[outcome.ReminderID] = outcome
	}
	oneMail, recurMail := byID[one.ReminderID].Message, byID[recur.ReminderID].Message
	require.Equal(t, "one old", oneMail.Body)
	require.Equal(t, "recurring old", recurMail.Body)
	require.Equal(t, "reminders", oneMail.From)
	require.Equal(t, "reminders", recurMail.From)
	require.Equal(t, root.AgentID, oneMail.To)
	require.Equal(t, worker.AgentID, recurMail.To)
	reserved := reserveEmbeddedMailTest(t, s, worker, recurMail.ID)
	require.Equal(t, recurMail.MessageIncarnationID, reserved.MessageIncarnationID)
	require.Equal(t, "recurring old", reserved.Body)
	updatedDue := formatTimestamp(clock.Now().Add(3 * time.Minute))
	updateArgs := []byte(fmt.Sprintf(`{"action":"update","id":%q,"body":"recurring new","due_at":%q,"interval_ms":120000}`, recur.ReminderID, updatedDue))
	deleted, replay, err := s.EmbeddedOwnReminderCall(ctx, root, "one-delete", []byte(fmt.Sprintf(`{"action":"delete","id":%q}`, one.ReminderID)))
	require.NoError(t, err)
	require.False(t, replay)
	require.True(t, deleted.Deleted)
	updated, replay, err := s.EmbeddedOwnReminderCall(ctx, worker, "recurring-update", updateArgs)
	require.NoError(t, err)
	require.False(t, replay)
	require.True(t, updated.Updated)
	same, replay, err := s.EmbeddedOwnReminderCall(ctx, worker, "recurring-update", updateArgs)
	require.NoError(t, err)
	require.True(t, replay)
	require.Equal(t, updated, same)
	_, _, err = s.EmbeddedOwnReminderCall(ctx, worker, "recurring-update", []byte(fmt.Sprintf(`{"action":"update","id":%q,"body":"conflict"}`, recur.ReminderID)))
	require.ErrorContains(t, err, "conflicting")
	_, _, err = s.EmbeddedOwnReminderCall(ctx, root, "recurring-update", updateArgs)
	require.Error(t, err)
	stale := worker
	stale.OwnerToken++
	_, _, err = s.EmbeddedOwnReminderCall(ctx, stale, "stale-update", updateArgs)
	require.Error(t, err)
	require.Zero(t, embeddedRedCount(t, s, "embedded_own_reminder_calls WHERE call_id='stale-update'"))
	oneParent, err := s.ReminderFor(ctx, one.ReminderID)
	require.NoError(t, err)
	require.Nil(t, oneParent)
	r, err := s.ReminderFor(ctx, recur.ReminderID)
	require.NoError(t, err)
	require.Equal(t, recurMail.ID, *r.LinkedMessageID)
	require.Equal(t, "recurring new", r.Body)
	require.Equal(t, updatedDue, r.NextDueAt)
	require.Equal(t, int64(120000), *r.IntervalMS)
	require.Equal(t, 1, r.OccurrenceNumber)
	for _, original := range []*domain.Message{oneMail, recurMail} {
		m, e := s.MessageFor(ctx, original.ID)
		require.NoError(t, e)
		require.Equal(t, original, m)
	}
	again, err := s.ReserveEmbeddedMail(ctx, worker, recurMail.ID)
	require.NoError(t, err)
	require.Equal(t, reserved, again)
	oneAttempt := reserveEmbeddedMailTest(t, s, root, oneMail.ID)
	for _, entry := range []struct {
		b       EmbeddedBinding
		attempt *EmbeddedMailAttempt
	}{{root, oneAttempt}, {worker, reserved}} {
		msg, e := EmbeddedMailUserMessage(entry.attempt, clock.Now().UnixMilli())
		require.NoError(t, e)
		payload, e := json.Marshal(msg)
		require.NoError(t, e)
		decoded, e := ai.UnmarshalMessage(payload)
		require.NoError(t, e)
		require.IsType(t, ai.UserMessage{}, decoded)
		require.Contains(t, string(payload), entry.attempt.Sender)
		require.Contains(t, string(payload), entry.attempt.Body)
		require.NoError(t, s.AppendEmbeddedMailAndACK(ctx, entry.b, entry.attempt.AttemptID, []byte(entry.attempt.Body)))
		m, e := s.MessageFor(ctx, entry.attempt.MessageID)
		require.NoError(t, e)
		require.Equal(t, "acknowledged", m.State)
		snap := redReopen(t, s, entry.b)
		require.Len(t, snap.Entries, 1)
		require.Equal(t, "user", snap.Entries[0].Kind)
		stored, e := ai.UnmarshalMessage(snap.Entries[0].Body)
		require.NoError(t, e)
		require.IsType(t, ai.UserMessage{}, stored)
		storedJSON, e := json.Marshal(stored)
		require.NoError(t, e)
		for _, value := range []string{entry.attempt.AttemptID, entry.attempt.MessageID, entry.attempt.MessageIncarnationID, entry.attempt.Sender, entry.b.AgentID, entry.attempt.Body} {
			require.Contains(t, string(storedJSON), value)
		}
		require.Equal(t, 1, embeddedRedCount(t, s, "embedded_acks WHERE message_id='"+entry.attempt.MessageID+"'"))
	}
	r, err = s.ReminderFor(ctx, recur.ReminderID)
	require.NoError(t, err)
	require.Nil(t, r.LinkedMessageID)
	require.Equal(t, updatedDue, r.NextDueAt)
	require.NoError(t, clock.Advance(time.Minute))
	none, err := s.FireDueReminders(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, none)
	require.NoError(t, clock.Advance(2*time.Minute))
	next, err := s.FireDueReminders(ctx, nil)
	require.NoError(t, err)
	require.Len(t, next, 1)
	require.Equal(t, recur.ReminderID, next[0].ReminderID)
	require.Equal(t, "recurring new", next[0].Message.Body)
	require.Equal(t, "reminder-"+recur.ReminderID+"-1", next[0].Message.ID)
	require.Equal(t, 2, next[0].Reminder.OccurrenceNumber)
	require.NoError(t, s.Close())
	cold, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer cold.Close()
	require.Zero(t, embeddedRedCount(t, cold, "reminders WHERE id='"+one.ReminderID+"'"))
	require.Equal(t, 1, embeddedRedCount(t, cold, "messages WHERE id='"+oneMail.ID+"' AND state='acknowledged'"))
	require.Equal(t, 1, embeddedRedCount(t, cold, "messages WHERE id='"+recurMail.ID+"' AND state='acknowledged'"))
	require.Equal(t, 1, embeddedRedCount(t, cold, "messages WHERE id='"+next[0].Message.ID+"' AND state='queued'"))
	require.Equal(t, 3, embeddedRedCount(t, cold, "messages WHERE sender='reminders'"), "no sender-bound reminder reply")
	persisted, err := cold.ReminderFor(ctx, recur.ReminderID)
	require.NoError(t, err)
	require.Equal(t, "recurring new", persisted.Body)
	require.Equal(t, int64(120000), *persisted.IntervalMS)
	require.Equal(t, 2, persisted.OccurrenceNumber)
	require.Equal(t, next[0].Message.ID, *persisted.LinkedMessageID)
	stable, err := cold.FireDueReminders(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, stable)
}

func TestEmbeddedOwnReminderAtomicCalls(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	root := redBind(t, s, "own-root", "root-session", 1, "root-gen", "root")
	worker := redBind(t, s, "own-worker", "worker-session", 1, "worker-gen", "worker")
	for _, b := range []EmbeddedBinding{root, worker} {
		_, err := s.db.Exec(`INSERT INTO recipient_sources(recipient_name,source_kind,owner_identity,state,created_at,updated_at) VALUES(?,'managed',?,'active',?,?)`, b.AgentID, b.AgentID, formatTimestamp(s.clock.Now()), formatTimestamp(s.clock.Now()))
		require.NoError(t, err)
	}
	// The fixture clock, not the wall clock, is authoritative.
	now := formatTimestamp(s.clock.Now().Add(time.Hour))
	create := []byte(fmt.Sprintf(`{"action":"create","body":"wake up","due_at":%q}`, now))
	first, replay, err := s.EmbeddedOwnReminderCall(ctx, root, "call-1", create)
	require.NoError(t, err)
	require.False(t, replay)
	require.NotEmpty(t, first.ReminderID)
	require.Equal(t, 1, embeddedRedCount(t, s, "reminders WHERE target='own-root'"))
	same, replay, err := s.EmbeddedOwnReminderCall(ctx, root, "call-1", create)
	require.NoError(t, err)
	require.True(t, replay)
	require.Equal(t, first, same)
	_, _, err = s.EmbeddedOwnReminderCall(ctx, root, "call-1", []byte(fmt.Sprintf(`{"action":"create","body":"changed","due_at":%q}`, now)))
	require.Error(t, err)
	_, _, err = s.EmbeddedOwnReminderCall(ctx, worker, "call-1", create)
	require.Error(t, err)
	_, _, err = s.EmbeddedOwnReminderCall(ctx, root, "bad", []byte(`{"action":"list","extra":1}`))
	require.Error(t, err)
	_, _, err = s.EmbeddedOwnReminderCall(ctx, root, "bad", []byte(`{"action":"list","action":"create"}`))
	require.Error(t, err)
	require.Zero(t, embeddedRedCount(t, s, "embedded_own_reminder_calls WHERE call_id='bad'"))
	listing, _, err := s.EmbeddedOwnReminderCall(ctx, root, "list-1", []byte(`{"action":"list"}`))
	require.NoError(t, err)
	require.Len(t, listing.Reminders, 1)
	require.Equal(t, first.ReminderID, listing.Reminders[0].ID)
	other, _, err := s.EmbeddedOwnReminderCall(ctx, worker, "list-worker", []byte(`{"action":"list"}`))
	require.NoError(t, err)
	require.Empty(t, other.Reminders)
	canceled, _, err := s.EmbeddedOwnReminderCall(ctx, root, "cancel-1", []byte(fmt.Sprintf(`{"action":"cancel","id":%q}`, first.ReminderID)))
	require.NoError(t, err)
	require.True(t, canceled.Canceled)
	require.Zero(t, embeddedRedCount(t, s, "reminders WHERE id='"+first.ReminderID+"'"))
	same, replay, err = s.EmbeddedOwnReminderCall(ctx, root, "call-1", create)
	require.NoError(t, err)
	require.True(t, replay)
	require.Equal(t, first, same)
	_, _, err = s.EmbeddedOwnReminderCall(ctx, root, "cancel-other", []byte(fmt.Sprintf(`{"action":"cancel","id":%q}`, first.ReminderID)))
	require.Error(t, err)
	// No second side effect when the persisted intent is unresolved.
	_, err = s.db.Exec(`INSERT INTO embedded_own_reminder_calls(call_id,agent_id,session_id,epoch,role,args_digest,state) VALUES('unknown',?,?,?,'root','unknown','unknown')`, root.AgentID, root.SessionID, root.Epoch)
	require.NoError(t, err)
	_, _, err = s.EmbeddedOwnReminderCall(ctx, root, "unknown", []byte(`{"action":"list"}`))
	require.Error(t, err)
	require.Zero(t, embeddedRedCount(t, s, "embedded_own_reminder_calls WHERE call_id='unknown' AND state='observed'"))
}

func TestEmbeddedOwnReminderEffectOwnerAdmission(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	b := redBind(t, s, "effect-own", "effect-session", 1, "effect-generation", "worker")
	now := formatTimestamp(s.clock.Now())
	_, err := s.db.Exec(`INSERT INTO recipient_sources(recipient_name,source_kind,owner_identity,state,created_at,updated_at) VALUES(?,'managed',?,'active',?,?)`, b.AgentID, b.AgentID, now, now)
	require.NoError(t, err)
	create := []byte(fmt.Sprintf(`{"action":"create","body":"first","due_at":%q}`, formatTimestamp(s.clock.Now().Add(time.Hour))))
	intent := func(id, body string) []byte {
		return []byte(fmt.Sprintf(`{"id":%q,"name":"mecha_reminder","arguments":{"action":"create","body":%q,"due_at":%q}}`, id, body, formatTimestamp(s.clock.Now().Add(time.Hour))))
	}
	require.NoError(t, s.RecordEmbeddedEffectIntent(ctx, b, "first", intent("first", "first")))
	_, _, err = s.EmbeddedOwnReminderAdmittedCall(ctx, b, "first", []byte(fmt.Sprintf(`{"action":"create","body":"changed","due_at":%q}`, formatTimestamp(s.clock.Now().Add(time.Hour)))), true)
	require.ErrorContains(t, err, "conflicting")
	require.Zero(t, embeddedRedCount(t, s, "reminders WHERE target='effect-own'"))
	_, _, err = s.EmbeddedOwnReminderCall(ctx, b, "first", create)
	require.ErrorContains(t, err, "unknown intent")
	first, replay, err := s.EmbeddedOwnReminderAdmittedCall(ctx, b, "first", create, true)
	require.NoError(t, err)
	require.False(t, replay)
	require.NotEmpty(t, first.ReminderID)
	_, replay, err = s.EmbeddedOwnReminderCall(ctx, b, "first", create)
	require.NoError(t, err)
	require.True(t, replay)
	require.NoError(t, s.RecordEmbeddedEffectIntent(ctx, b, "unresolved", intent("unresolved", "pending")))
	// Same generation and exact arguments do not suffice after lease takeover.
	_, err = s.db.Exec(`UPDATE embedded_sessions SET owner_expires_at=? WHERE agent_id=?`, formatTimestamp(s.clock.Now().Add(-time.Second)), b.AgentID)
	require.NoError(t, err)
	token, err := s.AcquireEmbeddedOwner(ctx, b, "replacement-owner", time.Minute)
	require.NoError(t, err)
	recovered := b
	recovered.OwnerToken = token
	require.Greater(t, recovered.OwnerToken, b.OwnerToken)
	pending := []byte(fmt.Sprintf(`{"action":"create","body":"pending","due_at":%q}`, formatTimestamp(s.clock.Now().Add(time.Hour))))
	_, _, err = s.EmbeddedOwnReminderAdmittedCall(ctx, recovered, "unresolved", pending, true)
	require.ErrorContains(t, err, "unknown intent")
	require.Equal(t, 1, embeddedRedCount(t, s, "reminders WHERE target='effect-own'"))
}

func TestEmbeddedOwnReminderReservedSourceDuringPendingLaunch(t *testing.T) {
	for _, tc := range []struct {
		name, sourceKind, sourceOwner, sourceState, lifecycle, operation string
		missingSource, staleEpoch, staleGeneration, allowed              bool
	}{
		{name: "root pending launch", sourceKind: "managed", sourceOwner: "own-start", sourceState: "reserved", lifecycle: "starting", operation: "pending", allowed: true},
		{name: "wrong owner", sourceKind: "managed", sourceOwner: "another-agent", sourceState: "reserved", lifecycle: "starting", operation: "pending"},
		{name: "failed launch", sourceKind: "managed", sourceOwner: "own-start", sourceState: "reserved", lifecycle: "failed", operation: "pending"},
		{name: "terminal launch", sourceKind: "managed", sourceOwner: "own-start", sourceState: "reserved", lifecycle: "starting", operation: "terminal"},
		{name: "retired source", sourceKind: "managed", sourceOwner: "own-start", sourceState: "retired", lifecycle: "starting", operation: "pending"},
		{name: "conflicted source", sourceKind: "managed", sourceOwner: "own-start", sourceState: "conflicted", lifecycle: "starting", operation: "pending"},
		{name: "missing source", lifecycle: "starting", operation: "pending", missingSource: true},
		{name: "tmux source", sourceKind: "tmux", sourceOwner: "own-start", sourceState: "reserved", lifecycle: "starting", operation: "pending"},
		{name: "stale epoch", sourceKind: "managed", sourceOwner: "own-start", sourceState: "reserved", lifecycle: "starting", operation: "pending", staleEpoch: true},
		{name: "stale generation", sourceKind: "managed", sourceOwner: "own-start", sourceState: "reserved", lifecycle: "starting", operation: "pending", staleGeneration: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, _ := embeddedRedStore(t)
			b := redBind(t, s, "own-start", "own-session", 1, "own-generation", "root")
			now := formatTimestamp(s.clock.Now())
			require.NoError(t, s.PutManagedOperation(ctx, ManagedOperation{
				OperationID: "own-launch", Method: "POST", CanonicalPath: "/v1/managed-agents/launch",
				RequestID: "own-launch", CanonicalBodyHash: testManifestHash, State: tc.operation,
				ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`), CreatedAt: now,
			}))
			_, err := s.db.Exec(`UPDATE managed_agents SET lifecycle_state=?,activity_state=?,launch_operation_id='own-launch' WHERE agent_id=?`, tc.lifecycle, tc.lifecycle, b.AgentID)
			require.NoError(t, err)
			if !tc.missingSource {
				conflictKind, conflictOwner := any(nil), any(nil)
				if tc.sourceState == "conflicted" {
					conflictKind, conflictOwner = "tmux", "other-owner"
				}
				_, err = s.db.Exec(`INSERT INTO recipient_sources(recipient_name,source_kind,owner_identity,state,conflict_source_kind,conflict_owner_identity,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, b.AgentID, tc.sourceKind, tc.sourceOwner, tc.sourceState, conflictKind, conflictOwner, now, now)
				require.NoError(t, err)
			}
			if tc.staleEpoch {
				b.Epoch++
			}
			if tc.staleGeneration {
				b.Generation = "other-generation"
			}
			create := []byte(fmt.Sprintf(`{"action":"create","body":"first turn","due_at":%q}`, formatTimestamp(s.clock.Now().Add(time.Hour))))
			result, replay, err := s.EmbeddedOwnReminderCall(ctx, b, "first-turn", create)
			if tc.allowed {
				require.NoError(t, err)
				require.False(t, replay)
				require.NotEmpty(t, result.ReminderID)
				require.Equal(t, 1, embeddedRedCount(t, s, "reminders WHERE target='own-start'"))
			} else {
				require.Error(t, err)
				require.Zero(t, embeddedRedCount(t, s, "reminders WHERE target='own-start'"))
				require.Zero(t, embeddedRedCount(t, s, "embedded_own_reminder_calls WHERE call_id='first-turn'"))
			}
		})
	}
}

func TestEmbeddedOwnReminderCapAndSessionCleanup(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	b := redBind(t, s, "cap-agent", "cap-session", 1, "gen", "worker")
	_, err := s.db.Exec(`INSERT INTO recipient_sources(recipient_name,source_kind,owner_identity,state,created_at,updated_at) VALUES(?,'managed',?,'active',?,?)`, b.AgentID, b.AgentID, formatTimestamp(s.clock.Now()), formatTimestamp(s.clock.Now()))
	require.NoError(t, err)
	// Seed observed calls through SQL to avoid 4096 individual transactions.
	require.NoError(t, s.runImmediate(ctx, func() error {
		for i := 0; i < 4096; i++ {
			_, err := s.db.ExecContext(ctx, `INSERT INTO embedded_own_reminder_calls(call_id,agent_id,session_id,epoch,role,args_digest,state,result_json) VALUES(?,?,?,?,'worker','digest','observed','{}')`, fmt.Sprintf("cap-%d", i), b.AgentID, b.SessionID, b.Epoch)
			if err != nil {
				return err
			}
		}
		return nil
	}))
	_, _, err = s.EmbeddedOwnReminderCall(ctx, b, "cap-next", []byte(`{"action":"list"}`))
	require.ErrorContains(t, err, "4096")
	_, _, err = s.EmbeddedOwnReminderCall(ctx, b, "cap-0", []byte(`{"action":"list"}`))
	require.Error(t, err) // changed digest conflicts, not cap
	require.Equal(t, 4096, embeddedRedCount(t, s, "embedded_own_reminder_calls"))
	_, err = s.db.ExecContext(ctx, `DELETE FROM managed_agents WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	require.Zero(t, embeddedRedCount(t, s, "embedded_own_reminder_calls"))
}
