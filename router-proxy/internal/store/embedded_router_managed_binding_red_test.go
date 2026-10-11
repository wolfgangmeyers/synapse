package store

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// The proposed minimum CAS is deliberately test-only: no fixture inserts a
// successful embedded session on behalf of this API. Both operations must run
// under runImmediate with the authoritative managed_agents row.
type embeddedManagedBinder interface {
	BindEmbeddedSession(context.Context, EmbeddedBinding, string) error
	ResetEmbeddedSession(context.Context, EmbeddedBinding, EmbeddedBinding, string) error
}

func embeddedManagedAgent(t *testing.T, s *Store, id string) EmbeddedBinding {
	t.Helper()
	ctx := context.Background()
	runtime := "embedded"
	if available, err := managedAgentColumnTx(ctx, s.db, "runtime"); err != nil || !available {
		runtime = "rpc"
	}
	agent := ManagedAgentRecord{AgentID: id, Name: id, Cwd: t.TempDir(), SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5"}, Runtime: runtime, PiSessionID: id + "-session", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: testManifestHash}
	op := ManagedOperation{OperationID: id + "-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: id + "-launch", CanonicalBodyHash: "hash", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, s.ReserveManagedLaunch(ctx, agent, op))
	require.NoError(t, s.UpdateManagedAgentStarting(ctx, id, agent.PiSessionID, ProcessIdentityRecord{PID: 321, StartMarker: "start", Incarnation: "incarnation"}, "generation-1"))
	require.NoError(t, s.MarkManagedReady(ctx, id))
	var session, generation string
	var epoch int64
	require.NoError(t, s.db.QueryRow(`SELECT pi_session_id,session_epoch,runtime_generation FROM managed_agents WHERE agent_id=?`, id).Scan(&session, &epoch, &generation))
	require.Equal(t, agent.PiSessionID, session)
	require.Equal(t, int64(1), epoch)
	require.Equal(t, "generation-1", generation)
	return EmbeddedBinding{AgentID: id, SessionID: session, Epoch: epoch, Generation: generation}
}

func embeddedRequireBinder(t *testing.T, s *Store) embeddedManagedBinder {
	t.Helper()
	binder, ok := any(s).(embeddedManagedBinder)
	require.True(t, ok, "missing managed-agent bind/reset CAS API; direct embedded_sessions INSERT is not an authority")
	return binder
}

func TestEmbeddedManagedFixtureReservationIsReal(t *testing.T) {
	s, _ := embeddedRedStore(t)
	b := embeddedManagedAgent(t, s, "embedded-real-fixture")
	var count int
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM managed_agents WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND runtime_generation=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&count))
	require.Equal(t, 1, count)
	require.Zero(t, embeddedRedCount(t, s, "embedded_sessions"))
}

func TestEmbeddedManagedBindCASContentionRetryAndReopen(t *testing.T) {
	ctx := context.Background()
	s, path := embeddedRedStore(t)
	first := embeddedManagedAgent(t, s, "embedded-bind-agent")
	binder := embeddedRequireBinder(t, s)
	competing := first
	competing.SessionID = "other-session"
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, binding := range []EmbeddedBinding{first, competing} {
		wg.Add(1)
		go func(b EmbeddedBinding) {
			defer wg.Done()
			<-start
			results <- binder.BindEmbeddedSession(ctx, b, "root")
		}(binding)
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	require.Equal(t, 1, successes, "exactly the managed row's current session may win; no false CAS success")
	require.Error(t, binder.BindEmbeddedSession(ctx, competing, "root"), "conflicting session must not bind")
	require.Equal(t, 1, embeddedRedCount(t, s, "embedded_sessions WHERE current=1"))
	first = claimEmbeddedFixture(t, s, first)
	require.NoError(t, binder.BindEmbeddedSession(ctx, first, "root"), "owned retry")
	redAppend(t, s, first, "user", "first")
	require.NoError(t, s.Close())
	reopened, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer reopened.Close()
	require.Len(t, redReopen(t, reopened, first).Entries, 1)
	_, err = reopened.ReopenEmbedded(ctx, competing)
	require.Error(t, err)
}

func TestEmbeddedManagedResetFencesAllWritesAndReopen(t *testing.T) {
	ctx := context.Background()
	s, path := embeddedRedStore(t)
	old := embeddedManagedAgent(t, s, "embedded-reset-agent")
	binder := embeddedRequireBinder(t, s)
	require.NoError(t, binder.BindEmbeddedSession(ctx, old, "root"))
	old = claimEmbeddedFixture(t, s, old)
	first := redAppend(t, s, old, "user", "first")
	require.NoError(t, s.CommitEmbeddedCheckpoint(ctx, old, first.Sequence, first.Digest, []byte("summary")))
	require.NoError(t, s.RecordEmbeddedEffectIntent(ctx, old, "reset-effect", []byte("intent")))
	mail := testEnvelope("embedded-reset-mail", "mail")
	mail.To = old.AgentID
	_, err := s.Enqueue(ctx, mail)
	require.NoError(t, err)
	a := reserveEmbeddedMailTest(t, s, old, "embedded-reset-mail")
	next := old
	next.SessionID = "embedded-reset-new-session"
	next.Epoch++
	next.Generation = "generation-2"
	next.OwnerToken = 0 // replacement must claim its own lease after reset
	// Simulate the managed lifecycle's committed replacement without touching
	// embedded state. The embedded reset CAS must recognize ONLY this row.
	_, err = s.db.Exec(`UPDATE managed_agents SET pi_session_id=?,session_epoch=?,runtime_generation=? WHERE agent_id=?`, next.SessionID, next.Epoch, next.Generation, next.AgentID)
	require.NoError(t, err)
	require.NoError(t, binder.ResetEmbeddedSession(ctx, old, next, "root"))
	next = claimEmbeddedFixture(t, s, next)
	require.Error(t, binder.ResetEmbeddedSession(ctx, old, old, "root"))
	_, err = s.AppendEmbeddedEntry(ctx, old, "user", []byte("stale"))
	require.Error(t, err)
	require.Error(t, s.RecordEmbeddedEffectIntent(ctx, old, "stale-effect", []byte("intent")))
	require.Error(t, s.ObserveEmbeddedEffect(ctx, old, "reset-effect", []byte("result")))
	require.Error(t, s.AppendEmbeddedMailAndACK(ctx, old, a.AttemptID, []byte("mail")))
	require.Error(t, s.CommitEmbeddedCheckpoint(ctx, old, first.Sequence, first.Digest, []byte("stale")))
	require.Zero(t, embeddedRedCount(t, s, "embedded_acks"))
	_, err = s.ReopenEmbedded(ctx, old)
	require.Error(t, err)
	require.NoError(t, s.Close())
	reopened, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer reopened.Close()
	_, err = reopened.ReopenEmbedded(ctx, old)
	require.Error(t, err)
	require.Empty(t, redReopen(t, reopened, next).Entries)
}

func TestEmbeddedManagedBindFaultRollsBackWithoutFalseSuccess(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	b := embeddedManagedAgent(t, s, "embedded-fault-agent")
	binder := embeddedRequireBinder(t, s)
	embeddedRedFault(t, s, "managed_bind_fault", "embedded_sessions")
	require.ErrorContains(t, binder.BindEmbeddedSession(ctx, b, "root"), "injected embedded red fault")
	require.Zero(t, embeddedRedCount(t, s, "embedded_sessions"))
	managed, err := s.ManagedAgent(ctx, b.AgentID)
	require.NoError(t, err)
	require.Equal(t, b.SessionID, managed.PiSessionID)
	require.Equal(t, b.Epoch, managed.SessionEpoch)
	require.Equal(t, b.Generation, managed.RuntimeGeneration)
	_, err = s.ReopenEmbedded(ctx, b)
	require.Error(t, err)
}

func TestEmbeddedManagedDeleteCascadeAndNoOrphanOnFreshReopen(t *testing.T) {
	ctx := context.Background()
	s, path := embeddedRedStore(t)
	// A narrow 022 managed row avoids unrelated launch/history/readiness FKs
	// installed by ReserveManagedLaunch. This is an authoritative managed row,
	// not a synthetic embedded-only ID; the control DELETE isolates 065.
	insertAgent := func(id string) EmbeddedBinding {
		t.Helper()
		b := EmbeddedBinding{AgentID: id, SessionID: id + "-session", Epoch: 1, Generation: "generation-1"}
		_, err := s.db.Exec(`INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,pi_session_id,session_epoch,runtime_generation,manifest_revision,manifest_hash,created_at,updated_at) VALUES(?,?,?,'openai-codex','gpt-5','running','running','idle','embedded',?,?,?,1,?,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, id, id, t.TempDir(), b.SessionID, b.Epoch, b.Generation, testManifestHash)
		require.NoError(t, err)
		return b
	}
	b := insertAgent("embedded-deleted-agent")
	baseline := insertAgent("embedded-delete-control")
	_, err := s.db.Exec(`DELETE FROM managed_agents WHERE agent_id=?`, baseline.AgentID)
	require.NoError(t, err, "control: managed DELETE without 065 child rows must work")
	// Legacy direct insertion is intentional only here: prove the production 065
	// schema cannot obstruct authoritative termination DELETE or retain orphans.
	_, err = s.db.Exec(`INSERT INTO embedded_sessions(agent_id,session_id,epoch,generation,role,current) VALUES(?,?,?,?,?,1)`, b.AgentID, b.SessionID, b.Epoch, b.Generation, "root")
	require.NoError(t, err)
	b = claimEmbeddedFixture(t, s, b)
	redAppend(t, s, b, "user", "before deletion")
	_, err = s.db.Exec(`DELETE FROM managed_agents WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err, "managed termination must remain authoritative")
	require.Zero(t, embeddedRedCount(t, s, "embedded_sessions"), "065 must cascade or clean before managed DELETE")
	require.Zero(t, embeddedRedCount(t, s, "embedded_entries"))
	_, err = s.AppendEmbeddedEntry(ctx, b, "user", []byte("orphan"))
	require.Error(t, err)
	require.NoError(t, s.Close())
	reopened, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer reopened.Close()
	_, err = reopened.ReopenEmbedded(ctx, b)
	require.Error(t, err)
	_, err = reopened.db.Exec(`INSERT INTO embedded_sessions(agent_id,session_id,epoch,generation,role,current) VALUES(?,?,?,?,?,1)`, b.AgentID, b.SessionID, b.Epoch, b.Generation, "root")
	require.Error(t, err, "deleted managed agent cannot authorize an orphan session")
}
