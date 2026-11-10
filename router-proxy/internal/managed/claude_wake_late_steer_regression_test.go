package managed

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestClaudeRestartWakeLateSteerKeepsFirstFinalOrigin(t *testing.T) {
	ctx := context.Background()
	r, p, _, cfg := claudeRuntimeFixture(t, `{"turns_by_input":[{"contains":"wake first","steps":[{"barrier":"wake-window"},{"text":"wake final"}]},{"contains":"late steer","steps":[{"text":"steer final"}]}]}`)
	g, err := p.db.MintEmbeddedProcessGeneration(ctx, "late-steer-wake")
	require.NoError(t, err)
	_, err = p.db.ReserveEmbeddedRestartWake(ctx, p.binding, g, "late-wake", "root-idle", "projection")
	require.NoError(t, err)
	require.NoError(t, p.db.ClaimEmbeddedRestartWake(ctx, p.binding, g, "late-wake"))
	require.NoError(t, p.db.ReconcileManagedRecipientSources(ctx))
	_, err = p.db.Enqueue(ctx, protocol.Envelope{ID: "late-wake-mail", From: "sender", To: p.binding.AgentID, Body: "late steer", CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	require.NoError(t, err)
	_, err = p.db.ResolveMessageTarget(ctx, "late-wake-mail")
	require.NoError(t, err)
	attempt, err := p.db.ReserveEmbeddedMail(ctx, p.binding, "late-wake-mail")
	require.NoError(t, err)
	scope := embeddedClaimedWakeScope{binding: p.binding, processGeneration: g, id: "late-wake", kind: "root-idle"}
	done := make(chan error, 1)
	go func() {
		done <- p.runClaimedWake(ctx, r, scope, []agent.AgentMessage{ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "wake first"}}}}, func() error { r.Close(); return nil })
	}()
	invocation := fakeBarrierInvocation(t, cfg.Cwd, "wake-window")
	require.Eventually(t, r.canSteer, 10*time.Second, time.Millisecond)
	outcome, err := r.steerInput(ctx, buildSteerUserMessage(attempt, time.Now().UnixMilli()), attempt, "")
	require.NoError(t, err)
	require.Equal(t, claudeSteerAccepted, outcome)
	require.Eventually(t, func() bool { return fakeUserWrites(t, cfg.Cwd) == 2 }, 10*time.Second, 10*time.Millisecond)
	require.NoError(t, os.WriteFile(filepath.Join(cfg.Cwd, "barrier-"+invocation), nil, 0600))
	select {
	case err = <-done:
		require.NoError(t, err)
	case <-time.After(15 * time.Second):
		t.Fatal("wake did not finish")
	}
	snap, err := p.db.ReopenEmbedded(ctx, p.binding)
	require.NoError(t, err)
	var finals []int64
	for _, e := range snap.Entries {
		if e.Kind == "assistant" {
			m, e2 := ai.UnmarshalMessage(e.Body)
			require.NoError(t, e2)
			if a, ok := m.(ai.AssistantMessage); ok && a.StopReason == ai.StopStop {
				finals = append(finals, e.Sequence)
			}
		}
	}
	require.Len(t, finals, 2)
	wake, err := p.db.EmbeddedRestartWake(ctx, p.binding, g, scope.id)
	require.NoError(t, err)
	require.Equal(t, "checked-complete", wake.State)
	require.Equal(t, finals[0], p.wakeFinal.Sequence)
	require.ErrorContains(t, p.AppendMessage(ctx, ai.AssistantMessage{StopReason: ai.StopStop}), "claimed wake final already appended")
	require.Error(t, p.db.CompleteEmbeddedRestartWake(ctx, p.binding, g, scope.id, p.wakeFinal.Sequence, p.wakeFinal.Digest))
	msg, err := p.db.MessageFor(ctx, attempt.MessageID)
	require.NoError(t, err)
	require.Equal(t, "acknowledged", string(msg.State))
}
