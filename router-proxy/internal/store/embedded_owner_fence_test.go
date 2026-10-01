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
	require.NoError(t, a.BindEmbeddedSession(ctx, base, "root")) // first bind bootstraps an unowned row
	require.ErrorContains(t, a.BindEmbeddedSession(ctx, base, "root"), "stale embedded owner")
	mail := testEnvelope("fenced-mail", "message")
	mail.To = base.AgentID
	_, err = a.Enqueue(ctx, mail)
	require.NoError(t, err)
	// Allocate only after the new owner claims the unowned session.

	checkRejected := func(label string, binding EmbeddedBinding) {
		t.Helper()
		t.Run(label, func(t *testing.T) {
			require.ErrorContains(t, a.BindEmbeddedSession(ctx, binding, "root"), "stale embedded owner")
			_, err := a.AppendEmbeddedEntryAt(ctx, binding, 0, embeddedGenesis, "fenced-append", "user", []byte("prompt"))
			require.ErrorContains(t, err, "stale embedded owner")
			_, err = a.AppendEmbeddedEntry(ctx, binding, "user", []byte("prompt"))
			require.ErrorContains(t, err, "stale embedded owner")
			require.ErrorContains(t, a.RecordEmbeddedEffectIntent(ctx, binding, "fenced-effect", []byte("intent")), "stale embedded owner")
			require.ErrorContains(t, a.ObserveEmbeddedEffect(ctx, binding, "fenced-effect", []byte("result")), "stale embedded owner")
			_, err = a.RecoverEmbeddedEffect(ctx, binding, "fenced-effect")
			require.ErrorContains(t, err, "stale embedded owner")
			require.ErrorContains(t, a.CommitEmbeddedCheckpoint(ctx, binding, 1, "missing", []byte("summary")), "stale embedded owner")
			require.ErrorContains(t, a.ReadyEmbeddedRoot(ctx, binding, op.OperationID), "stale embedded owner")
			require.ErrorContains(t, a.AppendEmbeddedMailAndACK(ctx, binding, "fenced-attempt", []byte("message")), "stale embedded owner")
			_, err = a.ReopenEmbedded(ctx, binding)
			require.ErrorContains(t, err, "stale embedded owner")
			require.Zero(t, embeddedRedCount(t, a, "embedded_entries"))
			require.Zero(t, embeddedRedCount(t, a, "embedded_effects"))
			require.Zero(t, embeddedRedCount(t, a, "embedded_checkpoints"))
			require.Zero(t, embeddedRedCount(t, a, "embedded_acks"))
		})
	}
	checkRejected("unowned", base)
	aToken, err := a.AcquireEmbeddedOwner(ctx, base, "owner-a", time.Second)
	require.NoError(t, err)
	old := base
	old.OwnerToken = aToken
	require.NoError(t, clock.Advance(time.Second))
	checkRejected("expired", old)
	bToken, err := b.AcquireEmbeddedOwner(ctx, base, "owner-b", time.Minute)
	require.NoError(t, err)
	require.Greater(t, bToken, aToken)
	checkRejected("stale", old)
	current := base
	current.OwnerToken = bToken
	require.NoError(t, b.BindEmbeddedSession(ctx, current, "root"))
	entry, err := b.AppendEmbeddedEntryAt(ctx, current, 0, embeddedGenesis, "fenced-append", "user", []byte("prompt"))
	require.NoError(t, err)
	require.NoError(t, b.CommitEmbeddedCheckpoint(ctx, current, entry.Sequence, entry.Digest, []byte("summary")))
	require.NoError(t, b.RecordEmbeddedEffectIntent(ctx, current, "fenced-effect", []byte("intent")))
	require.NoError(t, b.ReadyEmbeddedRoot(ctx, current, op.OperationID))
	attempt := reserveEmbeddedMailTest(t, b, current, "fenced-mail")
	require.NoError(t, b.AppendEmbeddedMailAndACK(ctx, current, attempt.AttemptID, []byte("message")))
	require.Equal(t, 1, embeddedRedCount(t, b, "embedded_acks"))
	// Even a successful previous key/ACK or readiness retry is fenced after takeover.
	require.ErrorContains(t, a.AppendEmbeddedMailAndACK(ctx, old, "fenced-attempt", []byte("message")), "stale embedded owner")
	require.ErrorContains(t, a.ReadyEmbeddedRoot(ctx, old, op.OperationID), "stale embedded owner")
	require.ErrorContains(t, a.CommitEmbeddedCheckpoint(ctx, old, entry.Sequence, entry.Digest, []byte("summary")), "stale embedded owner")
}
