package managed

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/sky-valley/pi/coding"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestClaimedWakeSDKFinalRecoveredByNewOwnerWithoutAnotherSDKRun(t *testing.T) {
	ctx := context.Background()
	db, old, g, path := wakePersistenceFixture(t)
	scope := embeddedClaimedWakeScope{binding: old, processGeneration: g, id: "wake-1", kind: "root-idle"}
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	reg.SetResponses([]providers.FauxResponseStep{providers.FauxStatic(providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "checked before crash"}}, ai.StopStop))})
	p := &embeddedPersistence{db: db, binding: old, digest: "genesis"}
	require.NoError(t, p.checkClaimedWake(ctx, scope))
	p.wakeUsed, p.wake = true, &scope
	sdk := coding.NewSession(coding.SessionOptions{SessionID: old.SessionID, Model: reg.GetModel(), Cwd: t.TempDir(), NoTools: coding.NoToolsAll, OrderedPersistence: p})
	_, err := sdk.RunMessages(ctx, []agent.AgentMessage{ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "wake"}}}})
	require.NoError(t, err)
	require.NotNil(t, p.wakeFinal)
	require.NoError(t, db.Close()) // crash after SDK final append, before Complete
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = raw.Exec(`UPDATE embedded_sessions SET owner_expires_at='2000-01-01T00:00:00Z' WHERE agent_id=?`, old.AgentID)
	require.NoError(t, err)
	require.NoError(t, raw.Close())
	db, err = store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	b := old
	token, err := db.AcquireEmbeddedOwner(ctx, b, "new-process", time.Minute)
	require.NoError(t, err)
	b.OwnerToken = token
	next, err := db.MintEmbeddedProcessGeneration(ctx, "verified-wake-process-next")
	require.NoError(t, err)
	require.Equal(t, g+1, next)
	staleOld := old
	staleOld.Epoch++
	require.Error(t, db.CompleteEmbeddedRestartWake(ctx, staleOld, g, scope.id, p.wakeFinal.Sequence, p.wakeFinal.Digest))
	require.NoError(t, db.ReconcileEmbeddedRestartWake(ctx, b, g, scope.id))
	wake, err := db.EmbeddedRestartWake(ctx, b, g, scope.id)
	require.NoError(t, err)
	require.Equal(t, "checked-complete", wake.State)
	require.Error(t, db.ClaimEmbeddedRestartWake(ctx, b, g, scope.id))
	// The SDK has no second run here; recovery only reads the exact origin.
	snap, err := db.ReopenEmbedded(ctx, b)
	require.NoError(t, err)
	require.Len(t, snap.Entries, 3)
	require.Equal(t, p.wakeFinal.Digest, snap.Entries[2].Digest)
}
