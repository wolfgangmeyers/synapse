package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedC2UnenteredOriginalAndCheckedFinalStayNeutral(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	defer s.Close()
	unentered := redBind(t, s, "c2-unentered", "session", 1, "runtime", "worker")
	finished := redBind(t, s, "c2-finished", "session", 1, "runtime", "root")
	body, e := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "saved only"}}})
	require.NoError(t, e)
	user, e := s.AppendEmbeddedEntry(ctx, unentered, "user", body)
	require.NoError(t, e)
	completed, e := s.AppendEmbeddedEntry(ctx, finished, "user", body)
	require.NoError(t, e)
	_, e = s.AppendEmbeddedEntry(ctx, finished, "assistant", wakeFinalFixture(t))
	require.NoError(t, e)
	gen, e := s.MintEmbeddedProcessGeneration(ctx, "c2-controls")
	require.NoError(t, e)
	decision, e := s.ClassifyEmbeddedRestart(ctx, finished, gen)
	require.NoError(t, e)
	require.Equal(t, "neutral", decision.Kind)
	require.Equal(t, EmbeddedEntry{}, decision.Source, "a checked final without provider-entry proof cannot schedule awareness")
	require.NoError(t, s.EnterEmbeddedNonMailProviderTurn(ctx, finished, completed.Sequence, completed.Digest))
	decision, e = s.ClassifyEmbeddedRestart(ctx, unentered, gen)
	require.NoError(t, e)
	require.Equal(t, "unentered-original", decision.Kind)
	require.Equal(t, user, decision.Source)
	require.Zero(t, embeddedRedCount(t, s, "embedded_restart_wake_slots"), "classification cannot authorize original dispatch")
	require.NoError(t, s.EnterEmbeddedNonMailProviderTurn(ctx, unentered, user.Sequence, user.Digest))
	decision, e = s.ClassifyEmbeddedRestart(ctx, unentered, gen)
	require.NoError(t, e)
	require.Equal(t, "interrupted", decision.Kind)
	decision, e = s.ClassifyEmbeddedRestart(ctx, finished, gen)
	require.NoError(t, e)
	require.Equal(t, "neutral", decision.Kind)
	require.Equal(t, completed, decision.Source, "only the exact checked nonmail provider-entry source supplies awareness authority")
	require.Zero(t, embeddedRedCount(t, s, "embedded_acks"))
	require.Zero(t, embeddedRedCount(t, s, "embedded_reply_outbox"))
	require.Equal(t, 1, embeddedRedCount(t, s, "embedded_restart_wake_slots")) // one interrupted slot only
	_, e = s.ClassifyEmbeddedRestart(ctx, unentered, gen-1)
	require.Error(t, e, "old or absent generation is not a recovery authority")
	_, e = s.db.ExecContext(ctx, `UPDATE managed_agents SET desired_state='terminated' WHERE agent_id=?`, unentered.AgentID)
	require.NoError(t, e)
	_, e = s.ClassifyEmbeddedRestart(ctx, unentered, gen)
	require.Error(t, e, "stopped agents cannot be classified")
	var foreignProblems int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&foreignProblems))
	require.Zero(t, foreignProblems)
	// 076 notices reference the original entry without ON DELETE CASCADE.
	// A bare agent deletion must fail closed rather than discard that proof.
	_, e = s.db.ExecContext(ctx, `DELETE FROM managed_agents WHERE agent_id=?`, unentered.AgentID)
	require.Error(t, e)
	require.Equal(t, 1, embeddedRedCount(t, s, "embedded_restart_wake_slots"))
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&foreignProblems))
	require.Zero(t, foreignProblems)

	// An idle slot has no dependent 076 notice and follows the 080/081
	// agent-deletion cascade, including any checked origin for that slot.
	idle := redBind(t, s, "c2-deleted-idle", "session", 1, "runtime", "worker")
	_, e = s.ReserveEmbeddedRestartWake(ctx, idle, gen, "c2-idle-wake", "worker-idle", "projection")
	require.NoError(t, e)
	_, e = s.db.ExecContext(ctx, `DELETE FROM managed_agents WHERE agent_id=?`, idle.AgentID)
	require.NoError(t, e)
	var remaining int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_restart_wake_slots WHERE agent_id=?`, idle.AgentID).Scan(&remaining))
	require.Zero(t, remaining)
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_restart_wake_checked_origins WHERE agent_id=?`, idle.AgentID).Scan(&remaining))
	require.Zero(t, remaining)
}
