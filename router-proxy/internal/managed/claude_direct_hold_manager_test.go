package managed

import (
	"context"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

// Exercise the manager against a real reservation and durable write-start fence.
// The older Pi attempt is synthetic; its incarnation is the actual accepted one.
func TestManagerHeldDirectReleaseDoesNotSettleOrRetry(t *testing.T) {
	for _, path := range []string{"live", "recovery", "retry-live", "retry-recovery"} {
		t.Run(path, func(t *testing.T) {
			ctx := context.Background()
			db, binding, _, _ := wakePersistenceFixtureWithClaim(t, false)
			require.NoError(t, db.ReconcileManagedRecipientSources(ctx))
			_, err := db.Enqueue(ctx, protocol.Envelope{ID: "held-manager", From: "sender", To: binding.AgentID, Body: "input", CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")}, store.EnqueueOptions{})
			require.NoError(t, err)
			_, err = db.ResolveMessageTarget(ctx, "held-manager")
			require.NoError(t, err)
			reserved, err := db.ReserveEmbeddedMail(ctx, binding, "held-manager")
			require.NoError(t, err)
			require.NoError(t, db.MarkClaudeMailWriteStarted(ctx, binding, reserved.AttemptID))
			attempt := store.ManagedDirectAttempt{MessageID: reserved.MessageID, MessageIncarnationID: reserved.MessageIncarnationID, AttemptNumber: 1, Fence: store.ManagedRuntimeFence{AgentID: binding.AgentID}}
			b := &managedBinding{agentID: binding.AgentID}
			_, ok := admitManagedTurnLocked(b, &attempt, 0)
			require.True(t, ok)
			runCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			m := &Manager{store: db, runCtx: runCtx, bindings: map[string]*managedBinding{binding.AgentID: b}}
			switch path {
			case "live":
				m.releaseManagedDirect(attempt, "failure", "")
			case "recovery":
				m.releaseManagedDirectRecovery(attempt, "failure")
			case "retry-live":
				m.retryManagedDirectRelease(attempt, "failure", "", false)
				m.startWG.Wait()
			case "retry-recovery":
				m.retryManagedDirectRelease(attempt, "failure", "", true)
				m.startWG.Wait()
			}
			require.False(t, b.turnAdmitted)
			require.Nil(t, b.turnDirectAttempt)
			_, admitted := admitManagedTurnLocked(b, nil, 0)
			require.True(t, admitted, "held direct turn must not block subsequent work")
			held, err := db.ClaudeMailHeld(ctx, attempt.MessageID)
			require.NoError(t, err)
			require.True(t, held)
			page, err := db.ManagedHistoryPage(ctx, binding.AgentID, "complete", 0, 100)
			require.NoError(t, err)
			require.Empty(t, page.Entries)
			id, err := db.NextEmbeddedMail(ctx, binding.AgentID)
			require.NoError(t, err)
			require.Empty(t, id)
		})
	}
}
