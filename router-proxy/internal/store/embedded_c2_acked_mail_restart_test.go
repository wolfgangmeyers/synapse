package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// The original non-mail turn settled. The last turn is a checked mail SDK user
// with a durable ACK, not a non-mail provider-entry marker or a final.
func TestEmbeddedC2ColdACKedWorkerMailInterruptsNotSettledOriginal(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "acked-worker.db")
	s, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	b := redBind(t, s, "acked-worker", "worker-session", 1, "worker-runtime", "worker")
	originalBody, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "original"}}})
	require.NoError(t, err)
	original, err := s.AppendEmbeddedEntry(ctx, b, "user", originalBody)
	require.NoError(t, err)
	require.NoError(t, s.EnterEmbeddedNonMailProviderTurn(ctx, b, original.Sequence, original.Digest))
	_, err = s.AppendEmbeddedEntry(ctx, b, "assistant", wakeFinalFixture(t))
	require.NoError(t, err)
	_, err = s.db.Exec(`INSERT INTO recipient_sources(recipient_name,source_kind,owner_identity,state,created_at,updated_at) VALUES(?, 'managed',?, 'active',?,?)`, b.AgentID, b.AgentID, formatTimestamp(clock.Now()), formatTimestamp(clock.Now()))
	require.NoError(t, err)
	envelope := testEnvelope("acked-worker-mail", "mail work")
	envelope.To = b.AgentID
	_, err = s.Enqueue(ctx, envelope)
	require.NoError(t, err)
	target, err := s.ResolveMessageTarget(ctx, envelope.ID)
	require.NoError(t, err)
	require.NotNil(t, target)
	attempt, err := s.ReserveEmbeddedMail(ctx, b, envelope.ID)
	require.NoError(t, err)
	require.NotNil(t, attempt)
	sdk, err := embeddedMailUser(attempt, clock.Now().UnixMilli())
	require.NoError(t, err)
	require.NoError(t, s.AppendEmbeddedMailSDKAndACK(ctx, b, attempt.AttemptID, []byte(attempt.Body), sdk))
	snap := redReopen(t, s, b)
	require.Len(t, snap.Entries, 3)
	mail := snap.Entries[2]
	require.Equal(t, "user", mail.Kind)
	require.Equal(t, int64(3), mail.Sequence)
	require.Equal(t, 1, embeddedRedCount(t, s, "embedded_nonmail_provider_entries"))
	require.Equal(t, 1, embeddedRedCount(t, s, "embedded_acks"))
	_, err = s.MintEmbeddedProcessGeneration(ctx, "old-process")
	require.NoError(t, err)
	require.NoError(t, s.Close())
	require.NoError(t, clock.Advance(2*time.Minute))
	cold, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer cold.Close()
	token, err := cold.AcquireEmbeddedOwner(ctx, b, "new-owner", time.Minute)
	require.NoError(t, err)
	b.OwnerToken = token
	gen, err := cold.MintEmbeddedProcessGeneration(ctx, "new-process")
	require.NoError(t, err)
	decision, err := cold.ClassifyEmbeddedRestart(ctx, b, gen)
	require.NoError(t, err)
	require.Equal(t, "interrupted", decision.Kind, "ACKed mail is entered, not a settled original wake")
	require.Equal(t, mail.Sequence, decision.Notice.SourceSequence)
	require.Equal(t, "appended", decision.Notice.State)
	require.Contains(t, decision.Notice.Text, attempt.AttemptID)
	require.Contains(t, decision.Notice.Text, "unknown")
	require.Equal(t, decision.Notice.ID, decision.Wake.ID)
	require.Equal(t, "planned", decision.Wake.State)
	require.NotEqual(t, original.Sequence, decision.Notice.SourceSequence)
	require.Equal(t, 1, embeddedRedCount(t, cold, "embedded_acks"))
	require.Equal(t, 1, embeddedRedCount(t, cold, "embedded_nonmail_provider_entries"))
	require.Zero(t, embeddedRedCount(t, cold, "embedded_reply_outbox"))
	require.Len(t, redReopen(t, cold, b).Entries, 4)
	same, err := cold.ClassifyEmbeddedRestart(ctx, b, gen)
	require.NoError(t, err)
	require.Equal(t, decision, same)
	require.NoError(t, cold.ClaimEmbeddedRestartWake(ctx, b, gen, decision.Wake.ID))
	require.Error(t, cold.ClaimEmbeddedRestartWake(ctx, b, gen, decision.Wake.ID))
	started, err := cold.PrepareEmbeddedInterruptedNotice(ctx, b, mail.Sequence)
	require.NoError(t, err)
	require.Equal(t, "started", started.State)
	require.NoError(t, cold.Close())
	require.NoError(t, clock.Advance(2*time.Minute))
	takeover, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer takeover.Close()
	token, err = takeover.AcquireEmbeddedOwner(ctx, b, "third-owner", time.Minute)
	require.NoError(t, err)
	b.OwnerToken = token
	nextGen, err := takeover.MintEmbeddedProcessGeneration(ctx, "third-process")
	require.NoError(t, err)
	after, err := takeover.ClassifyEmbeddedRestart(ctx, b, nextGen)
	require.NoError(t, err)
	require.Equal(t, "neutral", after.Kind)
	require.True(t, after.Suppressed)
	require.Len(t, redReopen(t, takeover, b).Entries, 4)
	require.Equal(t, 1, embeddedRedCount(t, takeover, "embedded_acks"))
	require.Zero(t, embeddedRedCount(t, takeover, "embedded_reply_outbox"))
}
