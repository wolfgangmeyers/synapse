package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func goalBind(t *testing.T, s *Store, agent, session string, epoch int64, generation, role string) EmbeddedBinding {
	t.Helper()
	b := redBind(t, s, agent, session, epoch, generation, role)
	require.NoError(t, s.reserveManagedHistoryBoundaryTx(context.Background(), agent, "goal-history-"+agent, "2026-01-01T00:00:00Z"))
	return b
}

func TestEmbeddedGoalOwnPolicyAndReplay(t *testing.T) {
	ctx := context.Background()
	s, path := embeddedRedStore(t)
	root := goalBind(t, s, "goal-root", "root-session", 1, "root-gen", "root")
	worker := goalBind(t, s, "goal-worker", "worker-session", 1, "worker-gen", "worker")
	settings := DefaultManagedGoalSettings()
	set := EmbeddedGoalCommand{Action: "set", Goal: "ship it", Settings: settings}
	a, replay, err := s.ApplyEmbeddedGoalCall(ctx, root, "call-1", set)
	require.NoError(t, err)
	require.False(t, replay)
	require.Equal(t, int64(1), a.DesiredRevision)
	require.Equal(t, int64(0), a.AppliedRevision)
	require.Equal(t, "pending", a.ProjectionState)
	again, replay, err := s.ApplyEmbeddedGoalCall(ctx, root, "call-1", set)
	require.NoError(t, err)
	require.True(t, replay)
	require.Equal(t, a, again)
	_, _, err = s.ApplyEmbeddedGoalCall(ctx, root, "call-1", EmbeddedGoalCommand{Action: "set", Goal: "different", Settings: settings})
	require.Error(t, err)
	_, _, err = s.ApplyEmbeddedGoalCall(ctx, worker, "call-1", set)
	require.Error(t, err, "call IDs must not cross bindings")
	w, replay, err := s.ApplyEmbeddedGoalCall(ctx, worker, "worker-set", EmbeddedGoalCommand{Action: "set", Goal: "worker goal", Settings: settings})
	require.NoError(t, err)
	require.False(t, replay)
	require.Equal(t, int64(1), w.DesiredRevision)
	status, _, err := s.ApplyEmbeddedGoalCall(ctx, root, "status", EmbeddedGoalCommand{Action: "status"})
	require.NoError(t, err)
	require.Equal(t, a.Config, status.Config)
	require.Equal(t, a.DesiredRevision, status.DesiredRevision)
	off, _, err := s.ApplyEmbeddedGoalCall(ctx, root, "off", EmbeddedGoalCommand{Action: "off"})
	require.NoError(t, err)
	require.Equal(t, int64(2), off.DesiredRevision)
	require.False(t, off.Config.Enabled)
	require.Empty(t, off.Config.Goal)
	ws, _, err := s.ApplyEmbeddedGoalCall(ctx, worker, "worker-status", EmbeddedGoalCommand{Action: "status"})
	require.NoError(t, err)
	require.Equal(t, "worker goal", ws.Config.Goal)
	require.NoError(t, s.Close())
	reopened, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer reopened.Close()
	prior, replay, err := reopened.ApplyEmbeddedGoalCall(ctx, root, "call-1", set)
	require.NoError(t, err)
	require.True(t, replay)
	require.Equal(t, a, prior)
	current, _, err := reopened.ApplyEmbeddedGoalCall(ctx, root, "later-status", EmbeddedGoalCommand{Action: "status"})
	require.NoError(t, err)
	require.Equal(t, int64(2), current.DesiredRevision)
}

func TestEmbeddedGoalCheckedSDKResult(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	b := goalBind(t, s, "goal-result", "session", 1, "gen", "root")
	other := goalBind(t, s, "goal-result-other", "other-session", 1, "other-gen", "worker")
	policy, _, err := s.ApplyEmbeddedGoalCall(ctx, b, "goal-result-id", EmbeddedGoalCommand{Action: "set", Goal: "own goal", Settings: DefaultManagedGoalSettings()})
	require.NoError(t, err)
	payload, err := json.Marshal(policy)
	require.NoError(t, err)
	require.NoError(t, s.CheckEmbeddedGoalResult(ctx, b, "goal-result-id", payload))
	require.Error(t, s.CheckEmbeddedGoalResult(ctx, other, "goal-result-id", payload))
	require.Error(t, s.CheckEmbeddedGoalResult(ctx, b, "goal-result-id", []byte(`{}`)))
	stale := b
	stale.Epoch++
	require.Error(t, s.CheckEmbeddedGoalResult(ctx, stale, "goal-result-id", payload))
}

func TestEmbeddedGoalResultFaultRollsBackPolicy(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	b := goalBind(t, s, "goal-fault", "session", 1, "gen", "root")
	_, err := s.db.Exec(`CREATE TRIGGER embedded_goal_result_fault BEFORE UPDATE OF result_json ON embedded_goal_calls BEGIN SELECT RAISE(ABORT,'injected result fault'); END`)
	require.NoError(t, err)
	_, _, err = s.ApplyEmbeddedGoalCall(ctx, b, "fault", EmbeddedGoalCommand{Action: "set", Goal: "not committed", Settings: DefaultManagedGoalSettings()})
	require.ErrorContains(t, err, "injected result fault")
	var count int
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM embedded_goal_calls WHERE call_id='fault'`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM managed_agent_policies WHERE agent_id=?`, b.AgentID).Scan(&count))
	require.Zero(t, count)
}

func TestEmbeddedGoalFenceAndUnknownIntent(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	b := goalBind(t, s, "goal-owner", "session", 1, "gen", "worker")
	cmd := EmbeddedGoalCommand{Action: "set", Goal: "one", Settings: DefaultManagedGoalSettings()}
	_, _, err := s.ApplyEmbeddedGoalCall(ctx, EmbeddedBinding{AgentID: b.AgentID, SessionID: "wrong-session", Epoch: b.Epoch, Generation: b.Generation}, "unowned", cmd)
	require.Error(t, err)
	_, err = s.db.Exec(`INSERT INTO embedded_goal_calls(call_id,agent_id,session_id,epoch,generation,role,action,args_json,state) VALUES(?,?,?,?,?,?,'set',?,'unknown')`, "uncertain", b.AgentID, b.SessionID, b.Epoch, b.Generation, "worker", `{"action":"set","goal":"one","settings":{"goal_lock_enabled":false,"snooze_enabled":true,"snooze_duration_ms":60000,"snooze_turn_limit":null,"snooze_budget_cap_ms":null,"snooze_refill_yield_ms":30000,"snooze_refill_elapsed_ms":60000}}`)
	require.NoError(t, err)
	_, _, err = s.ApplyEmbeddedGoalCall(ctx, b, "uncertain", cmd)
	require.ErrorContains(t, err, "unknown")
	_, _, err = s.ApplyEmbeddedGoalCall(ctx, b, "uncertain", EmbeddedGoalCommand{Action: "off"})
	require.Error(t, err)
	_, _, err = s.ApplyEmbeddedGoalCall(ctx, b, "good", cmd)
	require.NoError(t, err)
	_, err = s.db.Exec(`UPDATE embedded_goal_calls SET result_json='{}' WHERE call_id='good'`)
	require.ErrorContains(t, err, "immutable")
	_, err = s.db.Exec(`UPDATE embedded_sessions SET current=0 WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	_, _, err = s.ApplyEmbeddedGoalCall(ctx, b, "good", cmd)
	require.ErrorContains(t, err, "stale embedded owner")
	_, _, err = s.ApplyEmbeddedGoalCall(ctx, b, "another", cmd)
	require.ErrorContains(t, err, "stale embedded owner")
	require.NoError(t, s.Close())
}
