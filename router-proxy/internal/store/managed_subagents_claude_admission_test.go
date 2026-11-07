package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClaudeSubagentDefaultAdmissionTransaction(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	parent := subagentParent(t, s, "claude-parent")
	_, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET selected_model_provider='claude-code', selected_model_id='sonnet' WHERE agent_id=?`, parent.AgentID)
	require.NoError(t, err)
	child := func(id string) ManagedSubagentRecord {
		return ManagedSubagentRecord{AgentID: id, ParentAgentID: parent.AgentID, Status: "queued"}
	}
	absent := func(id string) {
		t.Helper()
		for _, table := range []string{"agents", "managed_agents", "managed_subagents"} {
			var n int
			require.NoError(t, s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE agent_id=?`, id).Scan(&n))
			require.Zero(t, n, table)
		}
	}
	require.ErrorContains(t, s.CreateManagedSubagent(ctx, parent, child("missing-default")), "configured router default")
	absent("missing-default")
	require.NoError(t, s.SetDefaultModel(ctx, &ManagedModel{Provider: "claude-code", ID: "not-an-alias"}))
	require.Error(t, s.CreateManagedSubagent(ctx, parent, child("invalid-default")))
	absent("invalid-default")
	require.NoError(t, s.SetDefaultModel(ctx, &ManagedModel{Provider: "claude-code", ID: "opus"}))
	require.NoError(t, s.CreateManagedSubagent(ctx, parent, child("claude-child")))
	require.NoError(t, s.SetDefaultModel(ctx, &ManagedModel{Provider: "google", ID: "other"}))
	var provider, model string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT selected_model_provider,selected_model_id FROM managed_agents WHERE agent_id='claude-child'`).Scan(&provider, &model))
	require.Equal(t, "claude-code", provider)
	require.Equal(t, "opus", model)
	require.NoError(t, s.CreateManagedSubagent(ctx, parent, child("pi-child")))
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT selected_model_provider,selected_model_id FROM managed_agents WHERE agent_id='pi-child'`).Scan(&provider, &model))
	require.Equal(t, "google", provider)
	require.Equal(t, "other", model)
	stale := parent
	stale.Epoch++
	require.Error(t, s.CreateManagedSubagent(ctx, stale, child("stale-child")))
	absent("stale-child")
}
