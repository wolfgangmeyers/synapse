package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// A genuine SDK producer: checked call, saved original intent, immutable
// DOMAIN result, checked result append. No successful receipt is SQL seeded.
func goalSDKCommand(t *testing.T, s *Store, b EmbeddedBinding, id string, command EmbeddedGoalCommand) *ManagedGoalPolicy {
	t.Helper()
	args := map[string]any{"action": command.Action}
	if command.Action == "set" {
		args["goal"] = command.Goal
	}
	call := providers.FauxToolCall("goal", args, id)
	receiptSDKAppend(t, s, b, *providers.FauxAssistantMessage(ai.ContentList{call}, ai.StopToolUse))
	intent, err := json.Marshal(call)
	require.NoError(t, err)
	require.NoError(t, s.RecordEmbeddedEffectIntent(context.Background(), b, id, intent))
	policy, replay, err := s.ApplyFreshEmbeddedGoalCall(context.Background(), b, id, command)
	require.NoError(t, err)
	require.False(t, replay)
	payload, err := json.Marshal(policy)
	require.NoError(t, err)
	require.NoError(t, s.CheckEmbeddedGoalResult(context.Background(), b, id, payload))
	receiptSDKAppend(t, s, b, ai.ToolResultMessage{ToolCallID: id, ToolName: "goal", Content: ai.ContentList{ai.TextContent{Text: string(payload)}}, Timestamp: 1})
	return policy
}

func TestEmbeddedGoalAppliedReceiptBoundary(t *testing.T) {
	ctx := context.Background()
	s, path := embeddedRedStore(t)
	root := goalBind(t, s, "receipt-root", "root-session", 1, "root-gen", "root")
	worker := goalBind(t, s, "receipt-worker", "worker-session", 1, "worker-gen", "worker")
	first := receiptProviderUser(t, s, root, "saved root turn")
	other := receiptProviderUser(t, s, worker, "saved worker turn")
	set := func(b EmbeddedBinding, id, goal string) {
		p := goalSDKCommand(t, s, b, id, EmbeddedGoalCommand{Action: "set", Goal: goal, Settings: DefaultManagedGoalSettings()})
		require.Equal(t, "pending", p.ProjectionState)
		require.Zero(t, p.AppliedRevision)
	}
	set(root, "root-set", "finish root")
	set(worker, "worker-set", "finish worker")
	var count int
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM embedded_goal_applied_receipts`).Scan(&count))
	require.Zero(t, count, "desired alone is not evidence of a provider request")
	args := EmbeddedGoalAppliedReceiptRequest{TurnKey: "root-turn", TurnEntrySeq: first.Sequence, TurnEntryDigest: first.Digest, GoalRevision: 1}
	receipt, replay, err := s.CommitEmbeddedGoalAppliedReceipt(ctx, root, args)
	require.NoError(t, err)
	require.False(t, replay)
	require.Equal(t, "root", receipt.Role)
	require.Equal(t, "finish root", receipt.GoalText)
	require.True(t, receipt.GoalEnabled)
	p, err := s.ManagedGoalPolicyFor(ctx, root.AgentID)
	require.NoError(t, err)
	require.EqualValues(t, 1, p.AppliedRevision)
	require.Equal(t, "applied", p.ProjectionState)
	version := p.UpdatedAt
	var projectionVersion int64
	require.NoError(t, s.db.QueryRow(`SELECT projection_version FROM managed_settings WHERE singleton_id=1`).Scan(&projectionVersion))
	receipt2, replay, err := s.CommitEmbeddedGoalAppliedReceipt(ctx, root, args)
	require.NoError(t, err)
	require.True(t, replay)
	require.Equal(t, receipt, receipt2)
	p, err = s.ManagedGoalPolicyFor(ctx, root.AgentID)
	require.NoError(t, err)
	require.Equal(t, version, p.UpdatedAt)
	var replayVersion int64
	require.NoError(t, s.db.QueryRow(`SELECT projection_version FROM managed_settings WHERE singleton_id=1`).Scan(&replayVersion))
	require.Equal(t, projectionVersion, replayVersion)
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM embedded_goal_applied_receipts`).Scan(&count))
	require.Equal(t, 1, count)
	bad := args
	bad.GoalRevision = 2
	_, _, err = s.CommitEmbeddedGoalAppliedReceipt(ctx, root, bad)
	require.Error(t, err)
	bad = args
	bad.TurnEntryDigest = "bad"
	_, _, err = s.CommitEmbeddedGoalAppliedReceipt(ctx, root, bad)
	require.Error(t, err)
	bad = args
	bad.TurnKey = "other-key"
	_, _, err = s.CommitEmbeddedGoalAppliedReceipt(ctx, root, bad)
	require.Error(t, err)
	_, _, err = s.CommitEmbeddedGoalAppliedReceipt(ctx, worker, args)
	require.Error(t, err, "turn keys cannot cross agents")
	wargs := EmbeddedGoalAppliedReceiptRequest{TurnKey: "worker-turn", TurnEntrySeq: other.Sequence, TurnEntryDigest: other.Digest, GoalRevision: 1}
	w, _, err := s.CommitEmbeddedGoalAppliedReceipt(ctx, worker, wargs)
	require.NoError(t, err)
	require.Equal(t, "worker", w.Role)
	require.Equal(t, "finish worker", w.GoalText)
	require.NoError(t, s.Close())
	reopened, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer reopened.Close()
	persisted, replay, err := reopened.CommitEmbeddedGoalAppliedReceipt(ctx, root, args)
	require.NoError(t, err)
	require.True(t, replay)
	require.Equal(t, receipt, persisted)
	require.NoError(t, reopened.db.QueryRow(`SELECT COUNT(*) FROM embedded_goal_applied_receipts`).Scan(&count))
	require.Equal(t, 2, count)
}

func TestEmbeddedGoalAppliedReceiptProjectionFaultRollsBack(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	b := goalBind(t, s, "receipt-fault", "session", 1, "gen", "worker")
	turn := receiptProviderUser(t, s, b, "saved turn")
	goalSDKCommand(t, s, b, "set", EmbeddedGoalCommand{Action: "set", Goal: "finish", Settings: DefaultManagedGoalSettings()})
	_, err := s.db.Exec(`CREATE TRIGGER receipt_projection_fault BEFORE UPDATE OF applied_revision ON managed_agent_policies BEGIN SELECT RAISE(ABORT,'projection fault'); END`)
	require.NoError(t, err)
	args := EmbeddedGoalAppliedReceiptRequest{TurnKey: "fault", TurnEntrySeq: turn.Sequence, TurnEntryDigest: turn.Digest, GoalRevision: 1}
	_, _, err = s.CommitEmbeddedGoalAppliedReceipt(ctx, b, args)
	require.ErrorContains(t, err, "projection fault")
	var count int
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM embedded_goal_applied_receipts`).Scan(&count))
	require.Zero(t, count)
	p, err := s.ManagedGoalPolicyFor(ctx, b.AgentID)
	require.NoError(t, err)
	require.Zero(t, p.AppliedRevision)
	require.Equal(t, "pending", p.ProjectionState)
}

func TestEmbeddedGoalAppliedReceiptOffAndFences(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	b := goalBind(t, s, "receipt-off", "session", 1, "gen", "root")
	turn := receiptProviderUser(t, s, b, "first")
	args := EmbeddedGoalAppliedReceiptRequest{TurnKey: "first", TurnEntrySeq: turn.Sequence, TurnEntryDigest: turn.Digest, GoalRevision: 1}
	_, _, err := s.CommitEmbeddedGoalAppliedReceipt(ctx, b, args)
	require.Error(t, err, "no desired policy")
	goalSDKCommand(t, s, b, "off", EmbeddedGoalCommand{Action: "off"})
	p, err := s.ManagedGoalPolicyFor(ctx, b.AgentID)
	require.NoError(t, err)
	require.Equal(t, "pending", p.ProjectionState)
	require.Zero(t, p.AppliedRevision)
	invalid := args
	invalid.TurnEntrySeq = turn.Sequence + 1
	_, _, err = s.CommitEmbeddedGoalAppliedReceipt(ctx, b, invalid)
	require.Error(t, err)
	wrongBinding := b
	wrongBinding.Epoch++
	_, _, err = s.CommitEmbeddedGoalAppliedReceipt(ctx, wrongBinding, args)
	require.Error(t, err)
	_, _, err = s.CommitEmbeddedGoalAppliedReceipt(ctx, b, args)
	require.NoError(t, err)
	var enabled int
	var text string
	require.NoError(t, s.db.QueryRow(`SELECT goal_enabled,goal_text FROM embedded_goal_applied_receipts WHERE turn_key='first'`).Scan(&enabled, &text))
	require.Zero(t, enabled)
	require.Empty(t, text)
	receiptSDKAppend(t, s, b, *providers.FauxAssistantMessage(ai.ContentList{}, ai.StopStop))
	next := receiptProviderUser(t, s, b, "second")
	stale := EmbeddedGoalAppliedReceiptRequest{TurnKey: "second", TurnEntrySeq: next.Sequence, TurnEntryDigest: next.Digest, GoalRevision: 1}
	goalSDKCommand(t, s, b, "set", EmbeddedGoalCommand{Action: "set", Goal: "new", Settings: DefaultManagedGoalSettings()})
	_, _, err = s.CommitEmbeddedGoalAppliedReceipt(ctx, b, stale)
	require.ErrorContains(t, err, "stale embedded goal revision", "superseded revision cannot apply")
	stale.GoalRevision = 2
	wrongEpoch := b
	wrongEpoch.Epoch++
	_, _, err = s.CommitEmbeddedGoalAppliedReceipt(ctx, wrongEpoch, stale)
	require.Error(t, err, "old or invented epoch cannot apply")
	_, err = s.db.Exec(`UPDATE embedded_sessions SET current=0 WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	_, _, err = s.CommitEmbeddedGoalAppliedReceipt(ctx, b, stale)
	require.ErrorContains(t, err, "stale embedded owner")
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM embedded_goal_applied_receipts`).Scan(&enabled))
	require.Equal(t, 1, enabled)
}
