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

// A claimed result cannot be checkpointed against a different ordered head.
// The rejected commit must not turn the one-shot UNKNOWN request into replay permission.
func TestEmbeddedOwnCompactionCheckpointWrongOrderedDigest(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	const callID = "compaction-conflict-call"
	agent := ManagedAgentRecord{
		AgentID: "compaction-conflict-root", Name: "compaction-conflict-root", Cwd: t.TempDir(),
		SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5"}, Runtime: "embedded",
		PiSessionID: "compaction-conflict-session", SessionEpoch: 1,
		RuntimeGeneration: "compaction-conflict-generation", ManifestRevision: 1, ManifestHash: testManifestHash,
	}
	op := ManagedOperation{
		OperationID: "compaction-conflict-launch", Method: "POST", CanonicalPath: "/v1/managed-agents",
		RequestID: "compaction-conflict-launch", CanonicalBodyHash: "hash", ResponseStatus: 202,
		ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`),
	}
	require.NoError(t, s.ReserveManagedLaunch(ctx, agent, op))
	b := EmbeddedBinding{AgentID: agent.AgentID, SessionID: agent.PiSessionID, Epoch: agent.SessionEpoch, Generation: agent.RuntimeGeneration}
	require.NoError(t, s.BindEmbeddedSession(ctx, b, "root"))
	token, err := s.AcquireEmbeddedOwner(ctx, b, "compaction-conflict-owner", time.Minute)
	require.NoError(t, err)
	b.OwnerToken = token

	call, err := s.AppendEmbeddedEntry(ctx, b, string(ai.RoleAssistant), []byte(`{"role":"assistant"}`))
	require.NoError(t, err)
	intent := []byte(`{"id":"compaction-conflict-call","name":"mecha_context","arguments":{"action":"compact"}}`)
	require.NoError(t, s.RecordEmbeddedEffectIntent(ctx, b, callID, intent))
	result, err := s.RequestEmbeddedOwnCompaction(ctx, b, callID, call.Sequence, call.Digest, true)
	require.NoError(t, err)
	body, err := json.Marshal(result)
	require.NoError(t, err)
	entry, err := s.AppendEmbeddedEntry(ctx, b, string(ai.RoleToolResult), body)
	require.NoError(t, err)
	require.NoError(t, s.ClaimEmbeddedOwnCompaction(ctx, b, callID, entry.Sequence, entry.Digest))

	checkpoint := coding.OrderedCheckpoint{
		ThroughSequence: entry.Sequence, FirstKeptSequence: call.Sequence, Summary: "## Goal\nKeep working.\n",
	}
	require.ErrorContains(t, s.CommitEmbeddedOwnCompactionCheckpoint(ctx, b, callID, entry.Sequence, call.Digest, checkpoint), "compaction checkpoint ordered head changed")
	snapshot, err := s.ReopenEmbedded(ctx, b)
	require.NoError(t, err)
	require.Nil(t, snapshot.Checkpoint)
	require.Equal(t, []EmbeddedEntry{call, entry}, snapshot.Entries)
	require.Zero(t, embeddedRedCount(t, s, "embedded_checkpoints WHERE agent_id='compaction-conflict-root'"))
	effect, err := s.RecoverEmbeddedEffect(ctx, b, callID)
	require.NoError(t, err)
	require.Equal(t, "unknown", effect.State)
	require.False(t, effect.ShouldReplay)
	require.ErrorContains(t, s.ClaimEmbeddedOwnCompaction(ctx, b, callID, entry.Sequence, entry.Digest), "already used")
	_, err = s.RequestEmbeddedOwnCompaction(ctx, b, callID, call.Sequence, call.Digest, true)
	require.Error(t, err)
	var claimed int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT claimed FROM embedded_own_compaction_requests WHERE call_id=?`, callID).Scan(&claimed))
	require.Equal(t, 1, claimed)
}
