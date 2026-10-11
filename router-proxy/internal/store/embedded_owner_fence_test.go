package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

// Two Store handles share one file and fixed clock. The token is checked on
// every call, including idempotent retries, before an append or an ACK.
func TestEmbeddedOwnerFenceStaleExpiredAndUnownedOperations(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "fence.db")
	a, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer a.Close()
	b, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer b.Close()
	agent := ManagedAgentRecord{AgentID: "fenced-root", Name: "fenced-root", Cwd: t.TempDir(), SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5"}, Runtime: "embedded", PiSessionID: "fenced-session", SessionEpoch: 1, RuntimeGeneration: "generation", ManifestRevision: 1, ManifestHash: testManifestHash}
	op := ManagedOperation{OperationID: "fenced-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "fenced-launch", CanonicalBodyHash: "hash", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, a.ReserveManagedLaunch(ctx, agent, op))
	base := EmbeddedBinding{AgentID: agent.AgentID, SessionID: agent.PiSessionID, Epoch: 1, Generation: agent.RuntimeGeneration}
	require.NoError(t, a.BindEmbeddedSession(ctx, base, "root"))
	require.NoError(t, a.BindEmbeddedSession(ctx, base, "root"))
	mail := testEnvelope("fenced-mail", "message")
	mail.To = base.AgentID
	_, err = a.Enqueue(ctx, mail)
	require.NoError(t, err)
	// Allocate only after the new owner claims the unowned session.

	checkRejected := func(label string, binding EmbeddedBinding) {
		t.Helper()
		t.Run(label, func(t *testing.T) {
			_, err := a.AppendEmbeddedEntryAt(ctx, binding, 0, embeddedGenesis, "fenced-append", "user", []byte("prompt"))
			require.Error(t, err)
			_, err = a.AppendEmbeddedEntry(ctx, binding, "user", []byte("prompt"))
			require.Error(t, err)
			require.Error(t, a.RecordEmbeddedEffectIntent(ctx, binding, "fenced-effect", []byte("intent")))
			require.Error(t, a.ObserveEmbeddedEffect(ctx, binding, "fenced-effect", []byte("result")))
			_, err = a.RecoverEmbeddedEffect(ctx, binding, "fenced-effect")
			require.Error(t, err)
			require.Error(t, a.CommitEmbeddedCheckpoint(ctx, binding, 1, "missing", []byte("summary")))
			require.Error(t, a.ReadyEmbeddedRoot(ctx, binding, op.OperationID))
			require.Error(t, a.AppendEmbeddedMailAndACK(ctx, binding, "fenced-attempt", []byte("message")))
			_, err = a.ReopenEmbedded(ctx, binding)
			require.Error(t, err)
			require.Zero(t, embeddedRedCount(t, a, "embedded_entries"))
			require.Zero(t, embeddedRedCount(t, a, "embedded_effects"))
			require.Zero(t, embeddedRedCount(t, a, "embedded_checkpoints"))
			require.Zero(t, embeddedRedCount(t, a, "embedded_acks"))
		})
	}
	staleEpoch := base
	staleEpoch.Epoch++
	checkRejected("stale-epoch", staleEpoch)

	staleGen := base
	staleGen.Generation = "wrong-generation"
	checkRejected("stale-generation", staleGen)

	staleSession := base
	staleSession.SessionID = "wrong-session"
	checkRejected("stale-session", staleSession)

	aToken, err := a.AcquireEmbeddedOwner(ctx, base, "owner-a", time.Second)
	require.NoError(t, err)
	current := base
	current.OwnerToken = aToken
	entry, err := b.AppendEmbeddedEntryAt(ctx, current, 0, embeddedGenesis, "fenced-append", "user", []byte("prompt"))
	require.NoError(t, err)
	require.NoError(t, b.CommitEmbeddedCheckpoint(ctx, current, entry.Sequence, entry.Digest, []byte("summary")))
	require.NoError(t, b.RecordEmbeddedEffectIntent(ctx, current, "fenced-effect", []byte("intent")))
	require.NoError(t, b.ReadyEmbeddedRoot(ctx, current, op.OperationID))
	attempt := reserveEmbeddedMailTest(t, b, current, "fenced-mail")
	require.NoError(t, b.AppendEmbeddedMailAndACK(ctx, current, attempt.AttemptID, []byte("message")))
	require.Equal(t, 1, embeddedRedCount(t, b, "embedded_acks"))
}
