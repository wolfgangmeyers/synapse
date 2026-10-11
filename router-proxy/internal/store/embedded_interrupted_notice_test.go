package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// A completed reminder has a durable final but intentionally has no sender outbox.
func TestEmbeddedInterruptedNoticeCompletedReminderColdTakeover(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "completed-reminder.db")
	a, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer a.Close()
	b := redBind(t, a, "completed-root", "completed-session", 1, "completed-generation", "root")
	mail := testEnvelope("completed-reminder", "wake up")
	mail.To = b.AgentID
	mail.Source = &protocol.MessageSource{Kind: "reminder"}
	_, err = a.Enqueue(ctx, mail)
	require.NoError(t, err)
	attempt := reserveEmbeddedMailTest(t, a, b, mail.ID)
	require.NoError(t, a.AppendEmbeddedMailAndACK(ctx, b, attempt.AttemptID, []byte(attempt.Body)))
	snap, err := a.ReopenEmbedded(ctx, b)
	require.NoError(t, err)
	require.Len(t, snap.Entries, 1)
	require.Equal(t, "user", snap.Entries[0].Kind)
	final, err := json.Marshal(ai.AssistantMessage{StopReason: ai.StopStop, Content: ai.ContentList{ai.TextContent{Text: "done"}}})
	require.NoError(t, err)
	entry, err := a.AppendEmbeddedFinalAndReply(ctx, b, attempt.AttemptID, snap.Entries[0].Sequence, snap.Entries[0].Digest, final)
	require.NoError(t, err)
	require.Equal(t, "assistant", entry.Kind)
	require.Zero(t, embeddedRedCount(t, a, "embedded_reply_outbox"), "reminders have no sender outbox")
	require.NoError(t, a.Close())
	require.NoError(t, clock.Advance(2*time.Minute))
	cold, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer cold.Close()
	token, err := cold.AcquireEmbeddedOwner(ctx, b, "cold-owner", time.Minute)
	require.NoError(t, err)
	b.OwnerToken = token
	restored, err := cold.ReopenEmbedded(ctx, b)
	require.NoError(t, err)
	require.Len(t, restored.Entries, 2)
	require.Equal(t, entry.Digest, restored.Entries[1].Digest)
	var ackCount int
	require.NoError(t, cold.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_acks WHERE attempt_id=? AND entry_seq=?`, attempt.AttemptID, snap.Entries[0].Sequence).Scan(&ackCount))
	require.Equal(t, 1, ackCount, "the recovery candidate is ACKed and eligible")
	notice, err := cold.PrepareEmbeddedInterruptedNotice(ctx, b, snap.Entries[0].Sequence)
	require.NoError(t, err)
	require.Nil(t, notice, "completed reminder must not start a recovery provider call")
	require.Zero(t, embeddedRedCount(t, cold, "embedded_interrupted_notices"))
}

func TestEmbeddedInterruptedNoticeStopErrorIsNotCompletion(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	b := redBind(t, s, "error-root", "error-session", 1, "error-generation", "root")
	mail := testEnvelope("error-reminder", "wake up")
	mail.To = b.AgentID
	mail.Source = &protocol.MessageSource{Kind: "reminder"}
	_, err := s.Enqueue(ctx, mail)
	require.NoError(t, err)
	attempt := reserveEmbeddedMailTest(t, s, b, mail.ID)
	require.NoError(t, s.AppendEmbeddedMailAndACK(ctx, b, attempt.AttemptID, []byte(attempt.Body)))
	snap, err := s.ReopenEmbedded(ctx, b)
	require.NoError(t, err)
	require.Len(t, snap.Entries, 1)
	fragment, err := json.Marshal(ai.AssistantMessage{StopReason: ai.StopError, Content: ai.ContentList{ai.TextContent{Text: "partial"}}})
	require.NoError(t, err)
	_, err = s.AppendEmbeddedEntry(ctx, b, "assistant", fragment)
	require.NoError(t, err)
	notice, err := s.PrepareEmbeddedInterruptedNotice(ctx, b, snap.Entries[0].Sequence)
	require.NoError(t, err)
	require.NotNil(t, notice, "StopError cannot prove turn completion")
	require.Equal(t, "pending", notice.State)
}

func TestEmbeddedInterruptedNoticeRestartAndStaleOwner(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "notice.db")
	a, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer a.Close()
	agent := ManagedAgentRecord{AgentID: "notice-root", Name: "notice-root", Cwd: t.TempDir(), SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5"}, Runtime: "embedded", PiSessionID: "notice-session", SessionEpoch: 1, RuntimeGeneration: "notice-generation", ManifestRevision: 1, ManifestHash: testManifestHash}
	op := ManagedOperation{OperationID: "notice-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "notice-launch", CanonicalBodyHash: "hash", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, a.ReserveManagedLaunch(ctx, agent, op))
	base := EmbeddedBinding{AgentID: agent.AgentID, SessionID: agent.PiSessionID, Epoch: 1, Generation: agent.RuntimeGeneration}
	require.NoError(t, a.BindEmbeddedSession(ctx, base, "root"))
	token, err := a.AcquireEmbeddedOwner(ctx, base, "old", time.Second)
	require.NoError(t, err)
	old := base
	old.OwnerToken = token
	mail := testEnvelope("notice-mail", "text")
	mail.To = base.AgentID
	_, err = a.Enqueue(ctx, mail)
	require.NoError(t, err)
	attempt := reserveEmbeddedMailTest(t, a, old, "notice-mail")
	require.NoError(t, a.AppendEmbeddedMailAndACK(ctx, old, attempt.AttemptID, []byte("text")))
	snap, err := a.ReopenEmbedded(ctx, old)
	require.NoError(t, err)
	source := snap.Entries[len(snap.Entries)-1].Sequence
	require.NoError(t, clock.Advance(time.Second))
	b, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer b.Close()
	token, err = b.AcquireEmbeddedOwner(ctx, base, "new", time.Second)
	require.NoError(t, err)
	current := base
	current.OwnerToken = token
	stale := base
	stale.Epoch++
	_, err = a.PrepareEmbeddedInterruptedNotice(ctx, stale, source)
	require.Error(t, err)
	notice, err := b.PrepareEmbeddedInterruptedNotice(ctx, current, source)
	require.NoError(t, err)
	require.Equal(t, "pending", notice.State)
	duplicate, err := b.PrepareEmbeddedInterruptedNotice(ctx, current, source)
	require.NoError(t, err)
	require.Equal(t, notice.ID, duplicate.ID)
	_, err = a.AppendEmbeddedInterruptedNotice(ctx, stale, *notice, source, snap.Entries[len(snap.Entries)-1].Digest, []byte("notice"))
	require.Error(t, err)
	entry, err := b.AppendEmbeddedInterruptedNotice(ctx, current, *notice, source, snap.Entries[len(snap.Entries)-1].Digest, []byte("notice"))
	require.NoError(t, err)
	duplicate, err = b.PrepareEmbeddedInterruptedNotice(ctx, current, source)
	require.NoError(t, err)
	require.Equal(t, "appended", duplicate.State)
	require.Equal(t, entry.Sequence, duplicate.EntrySequence)
	_, err = b.AppendEmbeddedInterruptedNotice(ctx, current, *notice, source, snap.Entries[len(snap.Entries)-1].Digest, []byte("notice"))
	require.ErrorContains(t, err, "already appended")
	require.NoError(t, b.StartEmbeddedInterruptedNotice(ctx, current, *duplicate))
	require.ErrorContains(t, b.StartEmbeddedInterruptedNotice(ctx, current, *duplicate), "already started")
	require.NoError(t, clock.Advance(time.Second))
	c, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer c.Close()
	token, err = c.AcquireEmbeddedOwner(ctx, base, "third", time.Minute)
	require.NoError(t, err)
	latest := base
	latest.OwnerToken = token
	duplicate, err = c.PrepareEmbeddedInterruptedNotice(ctx, latest, source)
	require.NoError(t, err)
	require.Equal(t, "started", duplicate.State)
	require.ErrorContains(t, c.StartEmbeddedInterruptedNotice(ctx, latest, *duplicate), "already started")
	require.Error(t, b.StartEmbeddedInterruptedNotice(ctx, current, *duplicate))
	snap, err = c.ReopenEmbedded(ctx, latest)
	require.NoError(t, err)
	require.Len(t, snap.Entries, 2)
}
