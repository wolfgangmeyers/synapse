package managed

import (
	"context"
	"database/sql"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/sky-valley/pi/ai/providers"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

type resetBarrierRuntime struct {
	interrupted chan struct{}
	closed      chan struct{}
}

func (*resetBarrierRuntime) Run(context.Context, string, ...ai.ImageContent) (*embeddedOutcome, error) {
	return nil, nil
}
func (*resetBarrierRuntime) RunMessages(context.Context, []agent.AgentMessage) (*embeddedOutcome, error) {
	return nil, nil
}
func (*resetBarrierRuntime) Continue(context.Context) error { return nil }
func (*resetBarrierRuntime) Steer(agent.AgentMessage)       {}
func (r *resetBarrierRuntime) Interrupt()                   { close(r.interrupted) }
func (r *resetBarrierRuntime) Close()                       { close(r.closed) }
func (*resetBarrierRuntime) Snapshot() embeddedSnapshot     { return embeddedSnapshot{} }

// The serial owner represents a Run whose persistence callback is still live.
// Interruption must not be mistaken for callback completion or permit snapshot.
func TestEmbeddedResetDrainsBeforeSnapshot(t *testing.T) {
	m := &Manager{}
	lane := m.laneFor("reset-barrier")
	runtime := &resetBarrierRuntime{make(chan struct{}), make(chan struct{})}
	require.True(t, lane.installEmbeddedRuntime(runtime, nil))
	lane.serial.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.ExecuteContextReset(ctx, store.EmbeddedBinding{AgentID: "reset-barrier"}, "browser") }()
	<-runtime.interrupted
	require.True(t, lane.embeddedResetPending())
	require.False(t, lane.installEmbeddedRuntime(&resetBarrierRuntime{}, nil))
	select {
	case <-runtime.closed:
		t.Fatal("closed before callback join")
	default:
	}
	select {
	case <-done:
		t.Fatal("reset returned before callback join")
	default:
	}
	cancel()
	lane.serial.Unlock()
	require.ErrorIs(t, <-done, context.Canceled)
	<-runtime.closed
	require.False(t, lane.embeddedResetPending())
}

func TestEmbeddedRuntimeRemovalIsIdentityChecked(t *testing.T) {
	lane := &managedAgentLane{}
	old := &resetBarrierRuntime{}
	next := &resetBarrierRuntime{}
	require.True(t, lane.installEmbeddedRuntime(old, nil))
	lane.removeEmbeddedRuntime(old)
	require.True(t, lane.installEmbeddedRuntime(next, nil))
	lane.removeEmbeddedRuntime(old)
	require.Equal(t, embeddedRuntime(next), lane.activeSession)
}

func TestEmbeddedResetGateLifecycle(t *testing.T) {
	lane := &managedAgentLane{}
	runtime := &resetBarrierRuntime{}
	require.True(t, lane.installEmbeddedRuntime(runtime, nil))
	captured, ok := lane.beginEmbeddedReset()
	require.True(t, ok)
	require.Same(t, runtime, captured)
	_, ok = lane.beginEmbeddedReset()
	require.False(t, ok)
	require.False(t, lane.installEmbeddedRuntime(&resetBarrierRuntime{}, nil))
	lane.reopenEmbeddedResetAdmissions()
	require.False(t, lane.embeddedResetPending())
	_, ok = lane.beginEmbeddedReset()
	require.False(t, ok, "wake still owns reset coordinator")
	lane.endEmbeddedReset()
	_, ok = lane.beginEmbeddedReset()
	require.True(t, ok, "next reset admitted only after prior wake settled")
	lane.endEmbeddedReset()
}

func TestEmbeddedToolResetDoesNotReenterSerial(t *testing.T) {
	m := &Manager{}
	lane := m.laneFor("tool-reset")
	runtime := &resetBarrierRuntime{make(chan struct{}), make(chan struct{})}
	require.True(t, lane.installEmbeddedRuntime(runtime, nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	lane.serial.Lock()
	defer lane.serial.Unlock()
	require.ErrorIs(t, m.resetEmbeddedContextAfterDrain(ctx, store.EmbeddedBinding{AgentID: "tool-reset"}, "tool"), context.Canceled)
	<-runtime.interrupted
	<-runtime.closed
	require.False(t, lane.embeddedResetPending())
}

func TestEmbeddedResetRollbackAndFreshReservation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "reset.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{Models: []providers.FauxModelDefinition{{ID: "reset-reservation", ContextWindow: 1000000, MaxTokens: 4096, Input: []string{"text"}}}})
	defer reg.Unregister()
	reg.SetResponses([]providers.FauxResponseStep{func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "reset awake"}}, ai.StopStop)
	}})
	m := makeSubagentTestManager(t, db, reg, 4)
	defer m.Close(ctx)
	old := subagentTestParent(t, db, "reset-reservations")
	registerParentOwner(m, old)
	_, err = db.Enqueue(ctx, protocol.Envelope{ID: "reset-live", From: "sender", To: old.AgentID, Body: "unconsumed", CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")}, store.EnqueueOptions{})
	require.NoError(t, err)
	_, err = db.ResolveMessageTarget(ctx, "reset-live")
	require.NoError(t, err)
	attempt, err := db.ReserveEmbeddedMail(ctx, old, "reset-live")
	require.NoError(t, err)
	_, err = raw.Exec(`CREATE TRIGGER reset_fault BEFORE INSERT ON embedded_sessions BEGIN SELECT RAISE(ABORT,'reset fault'); END`)
	require.NoError(t, err)
	lane := m.laneFor(old.AgentID)
	lane.serial.Lock()
	defer lane.serial.Unlock()
	require.ErrorContains(t, m.resetEmbeddedContextAfterDrain(ctx, old, "rollback"), "reset fault")
	require.False(t, m.laneFor(old.AgentID).embeddedResetPending())
	saved, err := db.ManagedAgent(ctx, old.AgentID)
	require.NoError(t, err)
	require.Equal(t, old.Epoch, saved.SessionEpoch)
	var count int
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_attempts WHERE attempt_id=?`, attempt.AttemptID).Scan(&count))
	require.Equal(t, 1, count)
	_, err = raw.Exec(`DROP TRIGGER reset_fault`)
	require.NoError(t, err)
	require.NoError(t, m.resetEmbeddedContextAfterDrain(ctx, old, "retry"))
	m.mu.Lock()
	next := m.embeddedOwners[old.AgentID].binding
	m.mu.Unlock()
	require.Equal(t, old.Epoch+1, next.Epoch)
	replacement, err := db.ReserveEmbeddedMail(ctx, next, "reset-live")
	require.NoError(t, err)
	require.NotEqual(t, attempt.AttemptID, replacement.AttemptID)
	require.Error(t, db.AppendEmbeddedMailAndACK(ctx, old, attempt.AttemptID, []byte(attempt.Body)))
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_attempts WHERE attempt_id=?`, replacement.AttemptID).Scan(&count))
	require.Equal(t, 1, count)
}
