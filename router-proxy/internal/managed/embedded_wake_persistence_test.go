package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
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

func wakePersistenceFixture(t *testing.T) (*store.Store, store.EmbeddedBinding, int64, string) {
	t.Helper()
	return wakePersistenceFixtureWithClaim(t, true)
}

func wakePersistenceFixtureWithClaim(t *testing.T, claimWake bool) (*store.Store, store.EmbeddedBinding, int64, string) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "wake.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,pi_session_id,session_epoch,runtime_generation,manifest_revision,manifest_hash,created_at,updated_at) VALUES('wake-agent','wake-agent',?,'openai-codex','gpt-5','running','running','idle','embedded','wake-session',1,'runtime',1,?,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, t.TempDir(), "hash")
	require.NoError(t, err)
	require.NoError(t, raw.Close())
	b := store.EmbeddedBinding{AgentID: "wake-agent", SessionID: "wake-session", Epoch: 1, Generation: "runtime"}
	require.NoError(t, db.BindEmbeddedSession(ctx, b, "root"))
	token, err := db.AcquireEmbeddedOwner(ctx, b, "wake-fixture", time.Minute)
	require.NoError(t, err)
	b.OwnerToken = token
	if !claimWake {
		return db, b, 0, path
	}
	g, err := db.MintEmbeddedProcessGeneration(ctx, "verified-wake-process")
	require.NoError(t, err)
	_, err = db.ReserveEmbeddedRestartWake(ctx, b, g, "wake-1", "root-idle", "projection")
	require.NoError(t, err)
	require.NoError(t, db.ClaimEmbeddedRestartWake(ctx, b, g, "wake-1"))
	return db, b, g, path
}

// A real SDK final must carry its origin at append time, not at completion.
func TestClaimedWakeSDKFinalCheckedPersistence(t *testing.T) {
	ctx := context.Background()
	db, b, g, path := wakePersistenceFixture(t)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	reg.SetResponses([]providers.FauxResponseStep{providers.FauxStatic(providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop))})
	p := &embeddedPersistence{db: db, binding: b, digest: "genesis"}
	session := coding.NewSession(coding.SessionOptions{SessionID: b.SessionID, Model: reg.GetModel(), Cwd: t.TempDir(), NoTools: coding.NoToolsAll, OrderedPersistence: p})
	scope := embeddedClaimedWakeScope{binding: b, processGeneration: g, id: "wake-1", kind: "root-idle"}
	require.NoError(t, p.runClaimedWake(ctx, session, scope, []agent.AgentMessage{ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "wake"}}}}))
	snap, err := db.ReopenEmbedded(ctx, b)
	require.NoError(t, err)
	require.Len(t, snap.Entries, 3)
	final := snap.Entries[2]
	require.Equal(t, "assistant", final.Kind)
	reopened, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer reopened.Close()
	wake, err := reopened.EmbeddedRestartWake(ctx, b, g, "wake-1")
	require.NoError(t, err)
	require.Equal(t, "checked-complete", wake.State)
	require.Error(t, reopened.CompleteEmbeddedRestartWake(ctx, b, g, "wake-1", final.Sequence, final.Digest), "completion is one-shot")
	require.Error(t, p.runClaimedWake(ctx, session, scope, []agent.AgentMessage{ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "duplicate"}}}}), "scope cannot be reused")
}

func TestClaimedWakeFragmentsGenericTurnsAndOwnerFence(t *testing.T) {
	ctx := context.Background()
	db, b, g, path := wakePersistenceFixture(t)
	scope := embeddedClaimedWakeScope{binding: b, processGeneration: g, id: "wake-1", kind: "root-idle"}
	stale := scope
	stale.binding.OwnerToken++
	p := &embeddedPersistence{db: db, binding: b, digest: "genesis"}
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	reg.SetResponses([]providers.FauxResponseStep{
		providers.FauxStatic(providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("unknown", map[string]any{}, "call-1")}, ai.StopToolUse)),
		providers.FauxStatic(providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "checked answer"}}, ai.StopStop)),
		providers.FauxStatic(providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "ordinary answer"}}, ai.StopStop)),
	})
	session := coding.NewSession(coding.SessionOptions{SessionID: b.SessionID, Model: reg.GetModel(), Cwd: t.TempDir(), NoTools: coding.NoToolsAll, OrderedPersistence: p})
	require.Error(t, p.runClaimedWake(ctx, session, stale, []agent.AgentMessage{ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "not dispatched"}}}}))
	require.NoError(t, p.runClaimedWake(ctx, session, scope, []agent.AgentMessage{ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "wake"}}}}))
	snap, err := db.ReopenEmbedded(ctx, b)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(snap.Entries), 4)
	require.Equal(t, "assistant", snap.Entries[2].Kind)
	require.Equal(t, "assistant", snap.Entries[len(snap.Entries)-1].Kind)
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()
	var tagged int
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_restart_wake_checked_origins WHERE agent_id=? AND seq=?`, b.AgentID, snap.Entries[2].Sequence).Scan(&tagged))
	require.Zero(t, tagged, "tool-use fragment is not a final")
	final := snap.Entries[len(snap.Entries)-1]
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_restart_wake_checked_origins WHERE agent_id=? AND seq=? AND wake_id=?`, b.AgentID, final.Sequence, scope.id).Scan(&tagged))
	require.Equal(t, 1, tagged)
	ordinary := &embeddedPersistence{db: db, binding: b, digest: "genesis"}
	next := coding.NewSession(coding.SessionOptions{SessionID: b.SessionID, Model: reg.GetModel(), Cwd: t.TempDir(), NoTools: coding.NoToolsAll, OrderedPersistence: ordinary})
	require.NoError(t, ordinary.restore(ctx, next))
	_, err = next.RunMessages(ctx, []agent.AgentMessage{ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "ordinary"}}}})
	require.NoError(t, err)
	snap, err = db.ReopenEmbedded(ctx, b)
	require.NoError(t, err)
	last := snap.Entries[len(snap.Entries)-1]
	require.Equal(t, "assistant", last.Kind)
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_restart_wake_checked_origins WHERE agent_id=? AND seq=?`, b.AgentID, last.Sequence).Scan(&tagged))
	require.Zero(t, tagged, "other SDK turn stays generic")
	require.Error(t, db.CompleteEmbeddedRestartWake(ctx, b, g, scope.id, last.Sequence, last.Digest))
}

func TestClaimedInterruptedWakeContinuesAlreadyStartedNotice(t *testing.T) {
	ctx := context.Background()
	db, b, _, _ := wakePersistenceFixtureWithClaim(t, false)
	initial := &embeddedPersistence{db: db, binding: b, digest: "genesis"}
	require.NoError(t, initial.AppendMessage(ctx, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "interrupted turn"}}}))
	require.NoError(t, db.EnterEmbeddedNonMailProviderTurn(ctx, b, initial.seq, initial.digest))
	notice, err := db.PrepareEmbeddedInterruptedNotice(ctx, b, initial.seq)
	require.NoError(t, err)
	require.NotNil(t, notice)
	body, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: notice.Text}}})
	require.NoError(t, err)
	_, err = db.AppendEmbeddedInterruptedNotice(ctx, b, *notice, initial.seq, initial.digest, body)
	require.NoError(t, err)
	g, err := db.MintEmbeddedProcessGeneration(ctx, "verified-interrupted-process")
	require.NoError(t, err)
	_, err = db.ReserveEmbeddedRestartWake(ctx, b, g, notice.ID, "interrupted", "notice-projection")
	require.NoError(t, err)
	require.NoError(t, db.ClaimEmbeddedRestartWake(ctx, b, g, notice.ID))
	require.Error(t, db.StartEmbeddedInterruptedNotice(ctx, b, *notice), "076 claim already started the notice")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	reg.SetResponses([]providers.FauxResponseStep{providers.FauxStatic(providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "interruption acknowledged"}}, ai.StopStop))})
	p := &embeddedPersistence{db: db, binding: b, digest: "genesis"}
	session := coding.NewSession(coding.SessionOptions{SessionID: b.SessionID, Model: reg.GetModel(), Cwd: t.TempDir(), NoTools: coding.NoToolsAll, OrderedPersistence: p})
	require.NoError(t, p.restore(ctx, session))
	before := p.seq
	scope := embeddedClaimedWakeScope{binding: b, processGeneration: g, id: notice.ID, kind: "interrupted"}
	require.NoError(t, p.runClaimedWake(ctx, session, scope, nil))
	snap, err := db.ReopenEmbedded(ctx, b)
	require.NoError(t, err)
	require.Len(t, snap.Entries, int(before+1), "Continue must not append the notice a second time")
	wake, err := db.EmbeddedRestartWake(ctx, b, g, notice.ID)
	require.NoError(t, err)
	require.Equal(t, "checked-complete", wake.State)
	require.Error(t, db.StartEmbeddedInterruptedNotice(ctx, b, *notice))
}

func TestClaimedWakeCrashAfterFinalAppendReconcilesExactOrigin(t *testing.T) {
	ctx := context.Background()
	db, b, g, path := wakePersistenceFixture(t)
	scope := embeddedClaimedWakeScope{binding: b, processGeneration: g, id: "wake-1", kind: "root-idle"}
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	reg.SetResponses([]providers.FauxResponseStep{providers.FauxStatic(providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "committed before crash"}}, ai.StopStop))})
	p := &embeddedPersistence{db: db, binding: b, digest: "genesis"}
	require.NoError(t, p.checkClaimedWake(ctx, scope))
	p.wakeUsed = true
	p.wake = &scope
	session := coding.NewSession(coding.SessionOptions{SessionID: b.SessionID, Model: reg.GetModel(), Cwd: t.TempDir(), NoTools: coding.NoToolsAll, OrderedPersistence: p})
	_, err := session.RunMessages(ctx, []agent.AgentMessage{ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "wake"}}}})
	require.NoError(t, err)
	require.NotNil(t, p.wakeFinal)
	require.Error(t, p.AppendMessage(ctx, providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "duplicate final"}}, ai.StopStop)), "hook reuse must fail")
	reopened, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer reopened.Close()
	wake, err := reopened.EmbeddedRestartWake(ctx, b, g, scope.id)
	require.NoError(t, err)
	require.Equal(t, "claimed", wake.State)
	require.Error(t, reopened.CompleteEmbeddedRestartWake(ctx, b, g, scope.id, p.wakeFinal.Sequence, "wrong digest"))
	require.NoError(t, reopened.CompleteEmbeddedRestartWake(ctx, b, g, scope.id, p.wakeFinal.Sequence, p.wakeFinal.Digest))
	require.Error(t, reopened.CompleteEmbeddedRestartWake(ctx, b, g, scope.id, p.wakeFinal.Sequence, p.wakeFinal.Digest))
}
