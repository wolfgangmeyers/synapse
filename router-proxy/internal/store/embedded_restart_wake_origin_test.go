package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func wakeFinalFixture(t *testing.T) []byte {
	t.Helper()
	b, err := json.Marshal(ai.AssistantMessage{Content: ai.ContentList{ai.TextContent{Text: "done"}}, StopReason: ai.StopStop})
	require.NoError(t, err)
	return b
}

func TestEmbeddedWakeCheckedOriginUpgrade080ReopenRollback(t *testing.T) {
	ctx := context.Background()
	migrations := DefaultMigrations()
	idx := migrationIndex(t, migrations, "081_embedded_restart_wake_checked_origin")
	path := filepath.Join(t.TempDir(), "already-080.db")
	before, err := Open(ctx, path, Options{Migrations: migrations[:idx]})
	require.NoError(t, err)
	var applied int
	require.NoError(t, before.db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE id='080_embedded_restart_wakes'`).Scan(&applied))
	require.Equal(t, 1, applied)
	require.NoError(t, before.Close())
	broken := append([]Migration(nil), migrations...)
	broken[idx].SQL += "; INSERT INTO missing_081_table VALUES (1);"
	_, err = Open(ctx, path, Options{Migrations: broken})
	require.Error(t, err)
	before, err = Open(ctx, path, Options{Migrations: migrations[:idx]})
	require.NoError(t, err)
	require.NoError(t, before.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='embedded_restart_wake_checked_origins'`).Scan(&applied))
	require.Zero(t, applied)
	require.NoError(t, before.db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE id='081_embedded_restart_wake_checked_origin'`).Scan(&applied))
	require.Zero(t, applied)
	require.NoError(t, before.Close())
	for i := 0; i < 2; i++ {
		s, err := Open(ctx, path, Options{})
		require.NoError(t, err)
		require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE id IN ('080_embedded_restart_wakes','081_embedded_restart_wake_checked_origin')`).Scan(&applied))
		require.Equal(t, 2, applied)
		require.NoError(t, s.Close())
	}
}

func TestEmbeddedWakeCheckedOriginFencesRecoveryAndRetention(t *testing.T) {
	ctx := context.Background()
	s, path := embeddedRedStore(t)
	b := redBind(t, s, "origin-root", "session", 1, "runtime", "root")
	g, err := s.MintEmbeddedProcessGeneration(ctx, "origin-process")
	require.NoError(t, err)
	_, err = s.ReserveEmbeddedRestartWake(ctx, b, g, "wake-one", "root-idle", "projection")
	require.NoError(t, err)
	final := wakeFinalFixture(t)
	_, err = s.AppendEmbeddedRestartWakeFinalAt(ctx, b, g, "wake-one", 0, embeddedGenesis, "preclaim", final)
	require.Error(t, err)
	require.NoError(t, s.ClaimEmbeddedRestartWake(ctx, b, g, "wake-one"))
	errorFinal, err := json.Marshal(ai.AssistantMessage{StopReason: ai.StopError})
	require.NoError(t, err)
	toolFinal, err := json.Marshal(ai.AssistantMessage{StopReason: ai.StopToolUse})
	require.NoError(t, err)
	forgedTool, err := json.Marshal(ai.AssistantMessage{Content: ai.ContentList{ai.ToolCall{ID: "call", Name: "effect", Arguments: map[string]any{}}}, StopReason: ai.StopStop})
	require.NoError(t, err)
	for _, body := range [][]byte{errorFinal, toolFinal, forgedTool, []byte(`{"stopReason":"stop"}`), []byte(`{"role":"assistant","stopReason":"stop"}`), []byte(`{"role":"user","content":[],"stopReason":"stop"}`), []byte(`{"role":"assistant","content":[],"stopReason":"stop","usage":{}}`)} {
		_, err = s.AppendEmbeddedRestartWakeFinalAt(ctx, b, g, "wake-one", 0, embeddedGenesis, "bad", body)
		require.Error(t, err)
	}
	_, err = s.AppendEmbeddedRestartWakeFinalAt(ctx, b, g, "wrong", 0, embeddedGenesis, "wrong-wake", final)
	require.Error(t, err)
	_, err = s.AppendEmbeddedRestartWakeFinalAt(ctx, b, g+1, "wake-one", 0, embeddedGenesis, "wrong-gen", final)
	require.Error(t, err)
	stale := b
	stale.OwnerToken++
	_, err = s.AppendEmbeddedRestartWakeFinalAt(ctx, stale, g, "wake-one", 0, embeddedGenesis, "wrong-owner", final)
	require.Error(t, err)
	stale = b
	stale.Epoch++
	_, err = s.AppendEmbeddedRestartWakeFinalAt(ctx, stale, g, "wake-one", 0, embeddedGenesis, "wrong-epoch", final)
	require.Error(t, err)
	entry, err := s.AppendEmbeddedRestartWakeFinalAt(ctx, b, g, "wake-one", 0, embeddedGenesis, "checked-one", final)
	require.NoError(t, err)
	retry, err := s.AppendEmbeddedRestartWakeFinalAt(ctx, b, g, "wake-one", 0, embeddedGenesis, "checked-one", final)
	require.NoError(t, err)
	require.Equal(t, entry, retry)
	_, err = s.AppendEmbeddedEntryAt(ctx, b, 0, embeddedGenesis, "checked-one", "assistant", final)
	require.Error(t, err)
	_, err = s.AppendEmbeddedRestartWakeFinalAt(ctx, b, g, "wake-one", 0, embeddedGenesis, "checked-one", errorFinal)
	require.Error(t, err)
	require.Error(t, s.CompleteEmbeddedRestartWake(ctx, b, g, "wake-one", entry.Sequence, "wrong-digest"))
	require.Error(t, s.CompleteEmbeddedRestartWake(ctx, b, g, "wrong", entry.Sequence, entry.Digest))
	require.NoError(t, s.Close())
	s, err = Open(ctx, path, Options{})
	require.NoError(t, err)
	require.NoError(t, s.CompleteEmbeddedRestartWake(ctx, b, g, "wake-one", entry.Sequence, entry.Digest))
	require.Error(t, s.CompleteEmbeddedRestartWake(ctx, b, g, "wake-one", entry.Sequence, entry.Digest))
	next, err := s.MintEmbeddedProcessGeneration(ctx, "origin-process-2")
	require.NoError(t, err)
	_, err = s.ReserveEmbeddedRestartWake(ctx, b, next, "wake-two", "root-idle", "projection")
	require.NoError(t, err)
	var n int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_restart_wake_checked_origins WHERE agent_id=?`, b.AgentID).Scan(&n))
	require.Equal(t, 1, n)
	third, err := s.MintEmbeddedProcessGeneration(ctx, "origin-process-3")
	require.NoError(t, err)
	_, err = s.ReserveEmbeddedRestartWake(ctx, b, third, "wake-three", "root-idle", "projection")
	require.NoError(t, err)
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_restart_wake_checked_origins WHERE agent_id=?`, b.AgentID).Scan(&n))
	require.Zero(t, n)
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_restart_wake_slots WHERE agent_id=?`, b.AgentID).Scan(&n))
	require.Equal(t, 2, n)
	require.NoError(t, s.Close())
}
