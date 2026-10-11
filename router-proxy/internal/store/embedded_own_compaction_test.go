package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/coding"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedOwnCompactionCheckpointClaimAndFault(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	b := redBind(t, s, "compact-barrier", "session-barrier", 1, "gen-barrier", "root")
	call, err := s.AppendEmbeddedEntry(ctx, b, "assistant", []byte(`{"role":"assistant"}`))
	require.NoError(t, err)
	const id = "barrier-call"
	require.NoError(t, s.RecordEmbeddedEffectIntent(ctx, b, id, []byte(`{"id":"barrier-call","name":"mecha_context","arguments":{"action":"compact"}}`)))
	result, err := s.RequestEmbeddedOwnCompaction(ctx, b, id, call.Sequence, call.Digest, true)
	require.NoError(t, err)
	body, err := json.Marshal(result)
	require.NoError(t, err)
	entry, err := s.AppendEmbeddedEntry(ctx, b, string(ai.RoleToolResult), body)
	require.NoError(t, err)
	require.ErrorContains(t, s.ClaimEmbeddedOwnCompaction(ctx, b, id, entry.Sequence+1, entry.Digest), "stale")
	require.NoError(t, s.ClaimEmbeddedOwnCompaction(ctx, b, id, entry.Sequence, entry.Digest))
	require.ErrorContains(t, s.ClaimEmbeddedOwnCompaction(ctx, b, id, entry.Sequence, entry.Digest), "already used")
	checkpoint := coding.OrderedCheckpoint{ThroughSequence: entry.Sequence, FirstKeptSequence: call.Sequence, Summary: "## Goal\nKeep working.\n"}
	_, err = s.db.Exec(`CREATE TEMP TRIGGER compact_checkpoint_fault BEFORE INSERT ON main.embedded_checkpoints BEGIN SELECT RAISE(ABORT,'injected checkpoint fault'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, s.CommitEmbeddedOwnCompactionCheckpoint(ctx, b, id, entry.Sequence, entry.Digest, checkpoint), "injected checkpoint fault")
	effect, err := s.RecoverEmbeddedEffect(ctx, b, id)
	require.NoError(t, err)
	require.Equal(t, "unknown", effect.State)
	require.Zero(t, embeddedRedCount(t, s, "embedded_checkpoints WHERE agent_id='"+b.AgentID+"'"))
	_, err = s.db.Exec(`DROP TRIGGER compact_checkpoint_fault`)
	require.NoError(t, err)
	require.NoError(t, s.CommitEmbeddedOwnCompactionCheckpoint(ctx, b, id, entry.Sequence, entry.Digest, checkpoint))
	snapshot, err := s.ReopenEmbedded(ctx, b)
	require.NoError(t, err)
	require.NotNil(t, snapshot.Checkpoint)
	require.Equal(t, entry.Digest, snapshot.Checkpoint.AncestorDigest)
	effect, err = s.RecoverEmbeddedEffect(ctx, b, id)
	require.NoError(t, err)
	require.Equal(t, "observed", effect.State)
	require.Error(t, s.CommitEmbeddedOwnCompactionCheckpoint(ctx, b, id, entry.Sequence, entry.Digest, checkpoint))
}

func TestEmbeddedOwnCompactionDeferredRequestFence(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	for _, role := range []string{"root", "worker"} {
		b := redBind(t, s, "compact-"+role, "session-"+role, 1, "gen-"+role, role)
		entry, err := s.AppendEmbeddedEntry(ctx, b, "user", []byte(`{"role":"user"}`))
		require.NoError(t, err)
		intent := []byte(`{"id":"call-` + role + `","name":"mecha_context","arguments":{"action":"compact"}}`)
		id := "call-" + role
		require.NoError(t, s.RecordEmbeddedEffectIntent(ctx, b, id, intent))
		_, err = s.RequestEmbeddedOwnCompaction(ctx, b, id, entry.Sequence, "wrong", true)
		require.ErrorContains(t, err, "head")
		require.Zero(t, embeddedRedCount(t, s, "embedded_own_compaction_requests WHERE call_id='"+id+"'"))
		result, err := s.RequestEmbeddedOwnCompaction(ctx, b, id, entry.Sequence, entry.Digest, true)
		require.NoError(t, err)
		require.Equal(t, id, result.ToolCallID)
		require.Equal(t, "pending", result.Content[0].(ai.TextContent).Text)
		_, err = s.RequestEmbeddedOwnCompaction(ctx, b, id, entry.Sequence, entry.Digest, true)
		require.Error(t, err)
		require.Equal(t, 1, embeddedRedCount(t, s, "embedded_own_compaction_requests WHERE call_id='"+id+"'"))
		require.Zero(t, embeddedRedCount(t, s, "embedded_checkpoints WHERE agent_id='"+b.AgentID+"'"))
		require.NoError(t, s.RecordEmbeddedEffectIntent(ctx, b, id+"-unknown", []byte(`{"id":"`+id+`-unknown","name":"mecha_context","arguments":{"action":"compact"}}`)))
		_, err = s.db.Exec(`UPDATE embedded_sessions SET owner_expires_at=? WHERE agent_id=?`, formatTimestamp(s.clock.Now().Add(-time.Second)), b.AgentID)
		require.NoError(t, err)
		token, err := s.AcquireEmbeddedOwner(ctx, b, "new-owner", time.Minute)
		require.NoError(t, err)
		recovered := b
		recovered.OwnerToken = token
		stale := b
		stale.Epoch++
		_, err = s.RequestEmbeddedOwnCompaction(ctx, stale, id+"-unknown", entry.Sequence, entry.Digest, true)
		require.Error(t, err)
	}
}
