package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Contract RED against the smallest router-owned SQLite boundary. These
// methods/types deliberately do not exist at HEAD. No pi-go API or production
// migration is added by this test-only draft. The SQLite schema is supplied
// only by embedded_router_red_harness_test.go.
func redBind(t *testing.T, s *Store, agent, session string, epoch int64, generation, role string) EmbeddedBinding {
	t.Helper()
	// Legacy fixture writes the session directly, but only after reserving its
	// authoritative managed row. Rebind on reopen models a committed replacement.
	_, err := s.db.Exec(`INSERT INTO agents(agent_id,first_seen_at) VALUES(?, '2026-01-01T00:00:00Z') ON CONFLICT(agent_id) DO NOTHING`, agent)
	require.NoError(t, err)
	_, err = s.db.Exec(`INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,pi_session_id,session_epoch,runtime_generation,manifest_revision,manifest_hash,created_at,updated_at) VALUES(?,?,?,'openai-codex','gpt-5','running','running','idle','embedded',?,?,?,1,?,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z') ON CONFLICT(agent_id) DO UPDATE SET pi_session_id=excluded.pi_session_id,session_epoch=excluded.session_epoch,runtime_generation=excluded.runtime_generation`, agent, agent, t.TempDir(), session, epoch, generation, testManifestHash)
	require.NoError(t, err)
	_, err = s.db.Exec(`INSERT INTO embedded_sessions(agent_id,session_id,epoch,generation,role,current) VALUES(?,?,?,?,?,1)`, agent, session, epoch, generation, role)
	require.NoError(t, err)
	b := EmbeddedBinding{AgentID: agent, SessionID: session, Epoch: epoch, Generation: generation}
	return claimEmbeddedFixture(t, s, b)
}

func claimEmbeddedFixture(t *testing.T, s *Store, b EmbeddedBinding) EmbeddedBinding {
	t.Helper()
	token, err := s.AcquireEmbeddedOwner(context.Background(), b, "fixture:"+b.AgentID, time.Minute)
	require.NoError(t, err)
	b.OwnerToken = token
	return b
}

func redAppend(t *testing.T, s *Store, b EmbeddedBinding, kind, body string) EmbeddedEntry {
	t.Helper()
	entry, err := s.AppendEmbeddedEntry(context.Background(), b, kind, []byte(body))
	require.NoError(t, err)
	return entry
}

func redReopen(t *testing.T, s *Store, b EmbeddedBinding) EmbeddedSnapshot {
	t.Helper()
	snapshot, err := s.ReopenEmbedded(context.Background(), b)
	require.NoError(t, err)
	return snapshot
}

func TestEmbeddedRouterFirstPromptFenceAndOrderedReopen(t *testing.T) {
	s, path := embeddedRedStore(t)
	root := redBind(t, s, "root", "root-session", 1, "run-1", "root")
	worker := redBind(t, s, "worker", "worker-session", 1, "worker-run-1", "worker")
	first := redAppend(t, s, root, "user", "first prompt before any assistant output")
	require.Equal(t, int64(1), first.Sequence)
	require.NotEmpty(t, first.Digest)
	second := redAppend(t, s, root, "assistant", "response")
	require.Equal(t, int64(2), second.Sequence)
	require.Equal(t, first.Digest, second.ParentDigest)
	redAppend(t, s, worker, "user", "worker independent prompt")
	// Close and reopen the actual file, not merely a query on the writer handle.
	require.NoError(t, s.Close())
	reopened, err := Open(context.Background(), path, Options{})
	require.NoError(t, err)
	defer reopened.Close()
	snapshot := redReopen(t, reopened, root)
	require.Len(t, snapshot.Entries, 2)
	require.Equal(t, []byte("first prompt before any assistant output"), snapshot.Entries[0].Body)
	require.Equal(t, second.Digest, snapshot.Entries[1].Digest)
	require.Len(t, redReopen(t, reopened, worker).Entries, 1)
	// Current-session fence must reject wrong agent, session, epoch and generation.
	for _, wrong := range []EmbeddedBinding{
		{AgentID: "worker", SessionID: root.SessionID, Epoch: 1, Generation: "run-1"},
		{AgentID: "root", SessionID: "wrong", Epoch: 1, Generation: "run-1"},
		{AgentID: "root", SessionID: root.SessionID, Epoch: 2, Generation: "run-1"},
		{AgentID: "root", SessionID: root.SessionID, Epoch: 1, Generation: "late-run"},
	} {
		_, err := reopened.AppendEmbeddedEntry(context.Background(), wrong, "user", []byte("late"))
		require.Error(t, err)
		_, err = reopened.ReopenEmbedded(context.Background(), wrong)
		require.Error(t, err)
	}
	require.Len(t, redReopen(t, reopened, root).Entries, 2)
	// A restarted generation can read committed history only under the new bind;
	// old callbacks cannot append or acknowledge after the CAS.
	_, err = reopened.db.Exec(`UPDATE embedded_sessions SET current=0 WHERE agent_id='root'`)
	require.NoError(t, err)
	next := redBind(t, reopened, "root", "root-session", 1, "run-2", "root")
	_, err = reopened.AppendEmbeddedEntry(context.Background(), root, "assistant", []byte("stale"))
	require.Error(t, err)
	require.Len(t, redReopen(t, reopened, next).Entries, 2)
}

func TestEmbeddedRouterAppendAndCheckpointFaultsAreAtomic(t *testing.T) {
	t.Run("first prompt", func(t *testing.T) {
		s, _ := embeddedRedStore(t)
		b := redBind(t, s, "root", "session", 1, "run", "root")
		embeddedRedFault(t, s, "fail_prompt", "embedded_entries")
		_, err := s.AppendEmbeddedEntry(context.Background(), b, "user", []byte("first"))
		require.ErrorContains(t, err, "injected embedded red fault")
		require.Zero(t, embeddedRedCount(t, s, "embedded_entries"))
		require.Empty(t, redReopen(t, s, b).Entries)
	})
	t.Run("checkpoint", func(t *testing.T) {
		s, _ := embeddedRedStore(t)
		b := redBind(t, s, "root", "session", 1, "run", "root")
		entry := redAppend(t, s, b, "user", "first")
		embeddedRedFault(t, s, "fail_checkpoint", "embedded_checkpoints")
		err := s.CommitEmbeddedCheckpoint(context.Background(), b, entry.Sequence, entry.Digest, []byte("summary"))
		require.ErrorContains(t, err, "injected embedded red fault")
		require.Zero(t, embeddedRedCount(t, s, "embedded_checkpoints"))
		got := redReopen(t, s, b)
		require.Len(t, got.Entries, 1)
		require.Nil(t, got.Checkpoint, "failed commit must not publish checkpoint")
	})
}

func TestEmbeddedRouterReopenRejectsDigestOrderAndCheckpointAncestry(t *testing.T) {
	for _, corruption := range []struct{ name, sql string }{
		{"gap", `DELETE FROM embedded_entries WHERE seq=1`},
		{"checkpoint ancestor", `UPDATE embedded_checkpoints SET ancestor_digest='wrong'`},
		{"checkpoint body", `UPDATE embedded_checkpoints SET body=X'00'`},
		{"checkpoint position", `UPDATE embedded_checkpoints SET at_seq=99`},
	} {
		t.Run(corruption.name, func(t *testing.T) {
			s, path := embeddedRedStore(t)
			b := redBind(t, s, "root", "session", 1, "run", "root")
			first := redAppend(t, s, b, "user", "first")
			redAppend(t, s, b, "tool_result", "result")
			require.NoError(t, s.CommitEmbeddedCheckpoint(context.Background(), b, first.Sequence, first.Digest, []byte("summary")))
			_, err := s.db.Exec(corruption.sql)
			if corruption.name == "gap" || corruption.name == "checkpoint position" {
				require.Error(t, err, "foreign key must reject orphaned ancestry")
				return
			}
			require.NoError(t, err)
			require.NoError(t, s.Close())
			reopened, err := Open(context.Background(), path, Options{})
			require.NoError(t, err)
			defer reopened.Close()
			_, err = reopened.ReopenEmbedded(context.Background(), b)
			require.Error(t, err, "corrupt ordered state must fail closed, not truncate/reorder")
		})
	}
}

func TestEmbeddedRouterUnknownEffectNeverReplaysOnMissingResult(t *testing.T) {
	s, path := embeddedRedStore(t)
	b := redBind(t, s, "root", "session", 1, "run", "root")
	redAppend(t, s, b, "tool_call", "launch worker")
	require.NoError(t, s.RecordEmbeddedEffectIntent(context.Background(), b, "launch-1", []byte("worker launch")))
	// Simulated crash: external launch may have happened, but no result or tool
	// result append is present. Absence is NOT evidence the effect did not run.
	require.NoError(t, s.Close())
	reopened, err := Open(context.Background(), path, Options{})
	require.NoError(t, err)
	defer reopened.Close()
	effect, err := reopened.RecoverEmbeddedEffect(context.Background(), b, "launch-1")
	require.NoError(t, err)
	require.Equal(t, "unknown", effect.State)
	require.False(t, effect.ShouldReplay)
	require.Empty(t, effect.Result)
	require.Len(t, redReopen(t, reopened, b).Entries, 1)
	// Same intent ID must not execute an effect twice after a restart.
	require.Error(t, reopened.RecordEmbeddedEffectIntent(context.Background(), b, "launch-1", []byte("different launch")))
}

func TestEmbeddedRouterExpectedHeadAndStableAppendKey(t *testing.T) {
	s, _ := embeddedRedStore(t)
	b := redBind(t, s, "root", "session", 1, "run", "root")
	first, err := s.AppendEmbeddedEntryAt(context.Background(), b, 0, embeddedGenesis, "request-1", "user", []byte("prompt"))
	require.NoError(t, err)
	require.Equal(t, int64(1), first.Sequence)
	retry, err := s.AppendEmbeddedEntryAt(context.Background(), b, 0, embeddedGenesis, "request-1", "user", []byte("prompt"))
	require.NoError(t, err)
	require.Equal(t, first, retry)
	_, err = s.AppendEmbeddedEntryAt(context.Background(), b, 0, embeddedGenesis, "request-1", "user", []byte("different"))
	require.Error(t, err)
	_, err = s.AppendEmbeddedEntryAt(context.Background(), b, 0, embeddedGenesis, "request-2", "assistant", []byte("late"))
	require.Error(t, err)
	second, err := s.AppendEmbeddedEntryAt(context.Background(), b, first.Sequence, first.Digest, "request-2", "assistant", []byte("answer"))
	require.NoError(t, err)
	require.Equal(t, first.Digest, second.ParentDigest)
	require.Len(t, redReopen(t, s, b).Entries, 2)

	// Keys up to 1024 bytes are accepted; keys exceeding 1024 bytes are rejected
	longKey := strings.Repeat("k", 1024)
	third, err := s.AppendEmbeddedEntryAt(context.Background(), b, second.Sequence, second.Digest, longKey, "user", []byte("prompt-3"))
	require.NoError(t, err)
	require.Equal(t, int64(3), third.Sequence)

	tooLongKey := strings.Repeat("k", 1025)
	_, err = s.AppendEmbeddedEntryAt(context.Background(), b, third.Sequence, third.Digest, tooLongKey, "user", []byte("prompt-4"))
	require.Error(t, err)
}

func TestEmbeddedRouterObservedEffectIsImmutable(t *testing.T) {
	s, _ := embeddedRedStore(t)
	b := redBind(t, s, "root", "session", 1, "run", "root")
	require.NoError(t, s.RecordEmbeddedEffectIntent(context.Background(), b, "effect-1", []byte("launch")))
	require.NoError(t, s.ObserveEmbeddedEffect(context.Background(), b, "effect-1", []byte("worker-id")))
	require.Error(t, s.ObserveEmbeddedEffect(context.Background(), b, "effect-1", []byte("other-id")))
	e, err := s.RecoverEmbeddedEffect(context.Background(), b, "effect-1")
	require.NoError(t, err)
	require.Equal(t, "observed", e.State)
	require.Equal(t, []byte("worker-id"), e.Result)
	require.False(t, e.ShouldReplay)
}

func TestEmbeddedRouterMailACKSeparateFromAcceptanceAndLegacyUI(t *testing.T) {
	s, _ := embeddedRedStore(t)
	b := redBind(t, s, "root", "session", 1, "run", "root")
	// Ordinary mailbox acceptance is already a separate SQLite commit. No
	// managed UI conversation/history or processing receipt is implied by it.
	envelope := testEnvelope("red-mail", "message")
	envelope.To = "root"
	accepted, err := s.Enqueue(context.Background(), envelope)
	require.NoError(t, err)
	require.Equal(t, "queued", accepted.Message.State)
	require.Zero(t, embeddedRedCount(t, s, "embedded_acks"))
	require.Zero(t, embeddedRedCount(t, s, "conversation_messages"))
	_, err = s.db.Exec(`INSERT INTO recipient_sources(recipient_name,source_kind,owner_identity,state,created_at,updated_at) VALUES('root','managed','root','active',?,?) ON CONFLICT(recipient_name) DO UPDATE SET source_kind='managed',owner_identity='root',state='active'`, formatTimestamp(s.clock.Now()), formatTimestamp(s.clock.Now()))
	require.NoError(t, err)
	_, err = s.ResolveMessageTarget(context.Background(), "red-mail")
	require.NoError(t, err)
	a, err := s.ReserveEmbeddedMail(context.Background(), b, "red-mail")
	require.NoError(t, err)
	for _, wrong := range []EmbeddedBinding{
		{AgentID: "worker", SessionID: "session", Epoch: 1, Generation: "run"},
		{AgentID: "root", SessionID: "session", Epoch: 2, Generation: "run"},
		{AgentID: "root", SessionID: "session", Epoch: 1, Generation: "old-run"},
	} {
		require.Error(t, s.AppendEmbeddedMailAndACK(context.Background(), wrong, a.AttemptID, []byte("message")))
	}
	require.Zero(t, embeddedRedCount(t, s, "embedded_acks"))
	embeddedRedFault(t, s, "fail_mail_ack", "embedded_acks")
	require.ErrorContains(t, s.AppendEmbeddedMailAndACK(context.Background(), b, a.AttemptID, []byte("message")), "injected embedded red fault")
	require.Zero(t, embeddedRedCount(t, s, "embedded_entries"), "ACK failure must roll back mail append too")
	require.Zero(t, embeddedRedCount(t, s, "embedded_acks"))
	_, err = s.db.Exec(`DROP TRIGGER temp.fail_mail_ack`)
	require.NoError(t, err)
	require.NoError(t, s.AppendEmbeddedMailAndACK(context.Background(), b, a.AttemptID, []byte("message")))
	require.Equal(t, 1, embeddedRedCount(t, s, "embedded_entries"))
	require.Equal(t, 1, embeddedRedCount(t, s, "embedded_acks"))
	require.NoError(t, s.AppendEmbeddedMailAndACK(context.Background(), b, a.AttemptID, []byte("message")), "exact retry is idempotent")
	require.Equal(t, 1, embeddedRedCount(t, s, "embedded_entries"))
	require.Zero(t, embeddedRedCount(t, s, "conversation_messages"), "legacy UI history is not an ordered session receipt")
	require.Zero(t, embeddedRedCount(t, s, "managed_direct_delivery_attempts"), "old RPC/tmux receipt table is not embedded ACK")
	message, err := s.MessageFor(context.Background(), "red-mail")
	require.NoError(t, err)
	require.Equal(t, "acknowledged", message.State)
	entry := redReopen(t, s, b).Entries[0]
	require.Equal(t, "user", entry.Kind)
	require.NoError(t, checkEmbeddedMailUser(entry.Body, a))
}
